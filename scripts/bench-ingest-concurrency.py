"""Measure accepted ingest throughput against the number of exporters.

    go build -o bin/geko ./cmd/geko
    python scripts/bench-ingest-concurrency.py --binary bin/geko

Each level starts a fresh server, sends 32 warm-up exports over one
connection, then sends 32 exports per connection from that many connections at
once. The server keeps `ingest_concurrency` exports in flight (four by default)
and answers the rest with HTTP 503 and `Retry-After: 1`, which is what an OTLP
exporter retries after. The level therefore reports accepted exports, rejected
exports and the throughput of the accepted ones.
"""

import argparse
import http.client
import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path

HARNESS = Path(__file__).resolve().parent / "bench-native.py"
specification = importlib.util.spec_from_file_location("bench_native", HARNESS)
harness = importlib.util.module_from_spec(specification)
specification.loader.exec_module(harness)

WARMUP_EXPORTS = 32
EXPORTS_EACH = 32


def measure(connections, payloads, port, headers):
    counted, failures, lock = {"accepted": 0, "busy": 0}, [], threading.Lock()

    def worker(chunk):
        connection = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
        accepted = busy = 0
        try:
            for payload in chunk:
                if harness.export(connection, payload, headers):
                    accepted += 1
                else:
                    busy += 1
        except Exception as error:  # reported after the threads join
            failures.append(error)
        finally:
            connection.close()
            with lock:
                counted["accepted"] += accepted
                counted["busy"] += busy

    chunks = [payloads[index * EXPORTS_EACH:(index + 1) * EXPORTS_EACH] for index in range(connections)]
    threads = [threading.Thread(target=worker, args=(chunk,)) for chunk in chunks]
    start = time.perf_counter()
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    wall = time.perf_counter() - start
    if failures:
        raise failures[0]
    spans = 128 * counted["accepted"]
    return {
        "connections": connections,
        "exports_sent": EXPORTS_EACH * connections,
        "accepted_exports": counted["accepted"],
        "rejected_503_exports": counted["busy"],
        "accepted_spans": spans,
        "wall_seconds": round(wall, 3),
        "accepted_spans_per_second": round(spans / wall),
        "ms_per_accepted_export": round(wall / max(counted["accepted"], 1) * 1000, 2),
    }


def run_level(binary, connections, slots):
    port = harness.free_port()
    with tempfile.TemporaryDirectory(prefix="geko-ingest-", ignore_cleanup_errors=True) as directory:
        env = {**os.environ, "GEKO_CONFIG": "", "GEKO_LISTEN_ADDR": f"127.0.0.1:{port}",
               "GEKO_DB_PATH": str(Path(directory) / "geko.db"), "GEKO_ACCESS_TOKEN": harness.TOKEN,
               "GEKO_REDACTION": "best_effort", "GEKO_MAX_DB_MIB": "1024"}
        if slots:
            env["GEKO_INGEST_CONCURRENCY"] = str(slots)
        log = open(Path(directory) / "server.log", "wb")
        process = subprocess.Popen([str(binary), "serve"], env=env, stdout=log, stderr=log, cwd=directory)
        headers = {"Authorization": f"Bearer {harness.TOKEN}"}
        try:
            harness.wait_ready(f"http://127.0.0.1:{port}/healthz")
            payloads = harness.workload(WARMUP_EXPORTS + EXPORTS_EACH * connections)
            warmup = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
            for payload in payloads[:WARMUP_EXPORTS]:
                harness.export(warmup, payload, headers)
            warmup.close()
            return measure(connections, payloads[WARMUP_EXPORTS:], port, headers)
        except (OSError, RuntimeError):
            log.flush()
            sys.stderr.write((Path(directory) / "server.log").read_text(errors="replace")[-4000:])
            raise
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=20)
                except subprocess.TimeoutExpired:
                    process.kill()
            log.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--binary", default="bin/geko")
    parser.add_argument("--connections", default="1,2,4,8", help="exporter counts to measure, in order")
    parser.add_argument("--ingest-concurrency", type=int, help="GEKO_INGEST_CONCURRENCY for every level")
    parser.add_argument("--out", help="write the measurements to this JSON file")
    arguments = parser.parse_args()
    binary = Path(arguments.binary).resolve()
    if not binary.exists():
        parser.error(f"no binary at {binary}; build it with go build -o {arguments.binary} ./cmd/geko")
    levels = [int(value) for value in arguments.connections.split(",")]
    if not levels or any(level < 1 or level > 64 for level in levels):
        parser.error("--connections takes counts from 1 to 64")
    result = {
        "date": time.strftime("%Y-%m-%d"),
        "host": {"platform": sys.platform, "logical_cpus": os.cpu_count()},
        "ingest_concurrency": arguments.ingest_concurrency or "default",
        "exports_per_connection": EXPORTS_EACH,
        "fixture": harness.workload.__doc__,
        "levels": [],
    }
    for level in levels:
        measurement = run_level(binary, level, arguments.ingest_concurrency)
        print(json.dumps(measurement), file=sys.stderr, flush=True)
        result["levels"].append(measurement)
    if arguments.out:
        Path(arguments.out).parent.mkdir(parents=True, exist_ok=True)
        Path(arguments.out).write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
