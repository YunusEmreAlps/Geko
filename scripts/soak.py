"""Soak one Geko process with a million spans at its size limit.

    go build -o bin/geko ./cmd/geko
    python scripts/soak.py --binary bin/geko --spans 1000000 --limit-mib 64

The server starts on an empty database with GEKO_MAX_DB_MIB set low enough
that it reaches the limit early, so most of the run evicts. Four exporters
send 64 two-span failing traces per export. Every tenth of the run it records
export latency, throughput, the database files, memory and eviction counters.
Halfway it stops the server, restarts it on the same database and measures
the time until /readyz answers. At the end it times the API and dashboard
pages on the full database.
"""

import argparse
import http.client
import importlib.util
import json
import math
import os
import statistics
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request
from pathlib import Path

HARNESS = Path(__file__).resolve().parent / "bench-native.py"
specification = importlib.util.spec_from_file_location("bench_native", HARNESS)
harness = importlib.util.module_from_spec(specification)
specification.loader.exec_module(harness)

SPANS_PER_EXPORT = 128
CONNECTIONS = 4
SEGMENTS = 10


def metrics(port):
    request = urllib.request.Request(f"http://127.0.0.1:{port}/metrics", headers={"Authorization": f"Bearer {harness.TOKEN}"})
    with urllib.request.urlopen(request, timeout=10) as response:
        values = {}
        for line in response.read().decode().splitlines():
            if line.startswith("geko_") and " " in line:
                name, value = line.rsplit(" ", 1)
                if "{" not in name:
                    values[name] = float(value)
        return values


class Server:
    def __init__(self, binary, directory, limit_mib):
        self.binary, self.directory, self.limit_mib = binary, directory, limit_mib
        self.port = harness.free_port()
        self.process = None
        self.log = open(Path(directory) / "server.log", "ab")

    def start(self):
        env = {**os.environ, "GEKO_CONFIG": "", "GEKO_LISTEN_ADDR": f"127.0.0.1:{self.port}",
               "GEKO_DB_PATH": str(Path(self.directory) / "geko.db"), "GEKO_ACCESS_TOKEN": harness.TOKEN,
               "GEKO_REDACTION": "best_effort", "GEKO_MAX_DB_MIB": str(self.limit_mib)}
        started = time.perf_counter()
        self.process = subprocess.Popen([str(self.binary), "serve"], env=env, stdout=self.log, stderr=self.log, cwd=self.directory)
        for _ in range(600):
            try:
                request = urllib.request.Request(f"http://127.0.0.1:{self.port}/readyz",
                                                 headers={"Authorization": f"Bearer {harness.TOKEN}"})
                with urllib.request.urlopen(request, timeout=1):
                    return time.perf_counter() - started
            except OSError:
                time.sleep(0.05)
        raise RuntimeError("Geko did not become ready")

    def stop(self):
        if self.process and self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=30)
            except subprocess.TimeoutExpired:
                self.process.kill()
                raise RuntimeError("Geko did not stop within 30 seconds")


def segment_workload(first_export, count):
    """count exports whose trace IDs follow those of the exports before them."""
    offset = first_export * 64
    original = harness.trace_id
    harness.trace_id = lambda number: original(number + offset)
    try:
        return harness.workload(count)
    finally:
        harness.trace_id = original


def send(port, payloads, headers):
    """Send payloads over CONNECTIONS connections; return per-export seconds."""
    latencies, failures, lock = [], [], threading.Lock()

    def worker(chunk):
        connection = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
        local = []
        try:
            for payload in chunk:
                start = time.perf_counter()
                if not harness.export(connection, payload, headers):
                    raise RuntimeError("export refused with HTTP 503")
                local.append(time.perf_counter() - start)
        except Exception as error:  # reported after the threads join
            failures.append(error)
        finally:
            connection.close()
            with lock:
                latencies.extend(local)

    chunks = [payloads[i::CONNECTIONS] for i in range(CONNECTIONS)]
    threads = [threading.Thread(target=worker, args=(chunk,)) for chunk in chunks]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    if failures:
        raise failures[0]
    return latencies


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--binary", default="bin/geko")
    parser.add_argument("--spans", type=int, default=1_000_000)
    parser.add_argument("--limit-mib", type=int, default=64)
    parser.add_argument("--out", help="write the measurements to this JSON file")
    arguments = parser.parse_args()
    binary = Path(arguments.binary).resolve()
    exports = math.ceil(arguments.spans / SPANS_PER_EXPORT)
    per_segment = math.ceil(exports / SEGMENTS)
    headers = {"Authorization": f"Bearer {harness.TOKEN}"}
    result = {"date": time.strftime("%Y-%m-%d"), "host": {"platform": sys.platform, "logical_cpus": os.cpu_count()},
              "spans": exports * SPANS_PER_EXPORT, "limit_mib": arguments.limit_mib, "connections": CONNECTIONS,
              "segments": []}
    with tempfile.TemporaryDirectory(prefix="geko-soak-", ignore_cleanup_errors=True) as directory:
        server = Server(binary, directory, arguments.limit_mib)
        try:
            result["startup_seconds"] = round(server.start(), 3)
            sent = 0
            for segment in range(SEGMENTS):
                if segment == SEGMENTS // 2:
                    server.stop()
                    result["restart_seconds"] = round(server.start(), 3)
                count = min(per_segment, exports - sent)
                payloads = segment_workload(sent, count)
                started = time.perf_counter()
                latencies = send(server.port, payloads, headers)
                wall = time.perf_counter() - started
                sent += count
                values = metrics(server.port)
                files = harness.database_bytes(directory)
                row = {
                    "spans_sent": sent * SPANS_PER_EXPORT,
                    "spans_per_second": round(count * SPANS_PER_EXPORT / wall),
                    "median_export_ms": round(statistics.median(latencies) * 1000, 2),
                    "p95_export_ms": round(harness.percentile(latencies, .95) * 1000, 2),
                    "main_file_mib": round(files["geko.db"] / 2**20, 1),
                    "wal_mib": round(files["geko.db-wal"] / 2**20, 1),
                    "memory_mib": harness.working_set_mib(server.process.pid),
                    "evicted_traces": int(values.get("geko_storage_evicted_traces_total", 0)),
                    "rejected_exports": int(values.get("geko_storage_rejected_exports_total", 0)),
                }
                result["segments"].append(row)
                print(json.dumps(row), file=sys.stderr, flush=True)
            for name, path in [("failures_api", "/api/v1/failures?limit=50"), ("failures_page", "/failures"),
                               ("error_groups_page", "/failures?view=groups"), ("overview_page", "/overview")]:
                result[f"mean_{name}_ms"] = harness.mean_request_ms(server.port, path, headers, count=20)[0]
            result["group_cache_shape"] = harness.group_cache_shape(directory)
        finally:
            server.stop()
            server.log.close()
    last = result["segments"][-1]
    kept = result["spans"] // 2 - last["evicted_traces"]
    result["summary"] = {
        "kept_traces": kept,
        "main_file_bytes_per_kept_span": round(last["main_file_mib"] * 2**20 / (kept * 2)),
        "evicted_traces": last["evicted_traces"],
        "rejected_exports": last["rejected_exports"],
        "main_file_mib_max": max(row["main_file_mib"] for row in result["segments"]),
        "median_export_ms_first_last": [result["segments"][0]["median_export_ms"], last["median_export_ms"]],
        "memory_mib_max": max(row["memory_mib"] for row in result["segments"]),
    }
    if arguments.out:
        Path(arguments.out).parent.mkdir(parents=True, exist_ok=True)
        Path(arguments.out).write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
