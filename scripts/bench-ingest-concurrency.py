"""Measure ingest concurrency with isolated databases and explicit workloads.

Legacy mode sends 32 exports per connection. For a balanced comparison use:
  python scripts/bench-ingest-concurrency.py --binary bin/geko \
    --connections 1,4,16 --total-exports 512 --rounds 3

Fixed-total mode uses the same warm-up and number of exports at every level.
Rounds rotate level order. Connections are established before the timed gate;
requests still wait for durable commits. Summary rates/latencies are medians
of rounds, count fields sum every round, and raw observations are retained.
"""
from collections import Counter
import argparse
import hashlib
import http.client
import importlib.util
import json
import os
import statistics
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


def split_payloads(payloads, connections):
    if connections < 1 or len(payloads) < connections:
        raise ValueError("each exporter needs at least one payload")
    quotient, remainder = divmod(len(payloads), connections)
    chunks, offset = [], 0
    for index in range(connections):
        end = offset + quotient + (index < remainder)
        chunks.append(payloads[offset:end])
        offset = end
    return chunks


def level_order(levels, round_index):
    offset = round_index % len(levels)
    return levels[offset:] + levels[:offset]


def measure(connections, payloads, port, headers):
    counted, failures, lock = {"accepted": 0, "busy": 0}, [], threading.Lock()
    latencies, rejection_reasons = [], Counter()
    ready, start_gate = threading.Barrier(connections + 1), threading.Event()

    def worker(chunk):
        connection = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
        accepted, busy, timings = 0, 0, []
        reasons = Counter()
        def refused(body):
            reason = "other_503"
            try:
                value = json.loads(body) if len(body) <= 4096 else None
                message = value.get("message") if isinstance(value, dict) else None
                reason = {"trace receiver is busy": "receiver_busy",
                          "trace storage capacity reached": "storage_capacity",
                          "trace storage unavailable": "storage_unavailable",
                          "export cancelled by the client": "canceled"}.get(message, reason)
            except (ValueError, UnicodeError):
                pass
            reasons[reason] += 1
        try:
            connection.connect()
            ready.wait(timeout=65)
            start_gate.wait()
            for payload in chunk:
                started = time.perf_counter()
                if harness.export(connection, payload, headers, on_refusal=refused):
                    timings.append((time.perf_counter() - started) * 1000)
                    accepted += 1
                else:
                    busy += 1
        except Exception as error:
            with lock:
                failures.append(error)
            ready.abort()
        finally:
            connection.close()
            with lock:
                counted["accepted"] += accepted
                counted["busy"] += busy
                latencies.extend(timings)
                rejection_reasons.update(reasons)

    chunks = split_payloads(payloads, connections)
    threads = [threading.Thread(target=worker, args=(chunk,)) for chunk in chunks]
    for thread in threads:
        thread.start()
    try:
        ready.wait(timeout=65)
    except threading.BrokenBarrierError:
        start_gate.set()
        for thread in threads:
            thread.join()
        raise failures[0] if failures else RuntimeError("exporter readiness failed")
    start, cpu = time.perf_counter(), time.process_time()
    start_gate.set()
    for thread in threads:
        thread.join()
    wall, client_cpu = time.perf_counter() - start, time.process_time() - cpu
    if failures:
        raise failures[0]
    spans = 128 * counted["accepted"]
    return {
        "connections": connections, "exports_sent": len(payloads),
        "accepted_exports": counted["accepted"], "rejected_503_exports": counted["busy"],
        "rejection_reasons": dict(sorted(rejection_reasons.items())),
        "accepted_spans": spans, "wall_seconds": round(wall, 6),
        "accepted_spans_per_second": round(spans / wall),
        "ms_per_accepted_export": round(wall / max(counted["accepted"], 1) * 1000, 2),
        "p50_export_ms": round(statistics.median(latencies), 2) if latencies else None,
        "p95_export_ms": round(harness.percentile(latencies, .95), 2) if latencies else None,
        "client_cpu_seconds": round(client_cpu, 6),
    }


def summarize_levels(levels, observations):
    result = []
    count_fields = ("exports_sent", "accepted_exports", "rejected_503_exports", "accepted_spans")
    median_fields = ("accepted_spans_per_second", "ms_per_accepted_export", "p50_export_ms", "p95_export_ms")
    for level in levels:
        samples = [value for value in observations if value["connections"] == level]
        summary = {"connections": level, "rounds": len(samples)}
        reasons = Counter()
        for value in samples: reasons.update(value.get("rejection_reasons", {}))
        summary["rejection_reasons"] = dict(sorted(reasons.items()))
        for field in count_fields:
            summary[field] = sum(value[field] for value in samples)
        for field in median_fields:
            values = [value[field] for value in samples if value[field] is not None]
            summary[field] = round(statistics.median(values), 2) if values else None
        for field in ("wall_seconds", "client_cpu_seconds"):
            summary[field] = round(sum(value[field] for value in samples), 6)
        rates = [value["accepted_spans_per_second"] for value in samples]
        summary["accepted_spans_per_second_min"] = min(rates)
        summary["accepted_spans_per_second_max"] = max(rates)
        result.append(summary)
    return result


def run_level(binary, connections, slots, total_exports=None):
    port = harness.free_port()
    exports = total_exports if total_exports is not None else EXPORTS_EACH * connections
    with tempfile.TemporaryDirectory(prefix="geko-ingest-", ignore_cleanup_errors=True) as directory:
        env = {key: value for key, value in os.environ.items() if not key.startswith("GEKO_")}
        env.update(GEKO_LISTEN_ADDR=f"127.0.0.1:{port}", GEKO_DB_PATH=str(Path(directory) / "geko.db"),
                   GEKO_ACCESS_TOKEN=harness.TOKEN, GEKO_REDACTION="best_effort", GEKO_MAX_DB_MIB="1024")
        if slots:
            env["GEKO_INGEST_CONCURRENCY"] = str(slots)
        with open(Path(directory) / "server.log", "wb") as log:
            process = subprocess.Popen([str(binary), "serve"], env=env, stdout=log, stderr=log, cwd=directory)
            headers = {"Authorization": f"Bearer {harness.TOKEN}"}
            try:
                harness.wait_ready(f"http://127.0.0.1:{port}/healthz")
                payloads = harness.workload(WARMUP_EXPORTS + exports)
                warmup = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
                try:
                    for payload in payloads[:WARMUP_EXPORTS]:
                        if not harness.export(warmup, payload, headers):
                            raise RuntimeError("warm-up export refused")
                finally:
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
                        process.wait(timeout=5)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--binary", default="bin/geko")
    parser.add_argument("--connections", default="1,2,4,8", help="exporter counts; rotated each round")
    parser.add_argument("--ingest-concurrency", type=int, help="GEKO_INGEST_CONCURRENCY for every level")
    parser.add_argument("--total-exports", type=int, help="same total exports per level, rather than 32 per connection")
    parser.add_argument("--rounds", type=int, default=1)
    parser.add_argument("--out", help="write the measurements to this JSON file")
    arguments = parser.parse_args()
    binary = Path(arguments.binary).resolve()
    if not binary.is_file():
        parser.error(f"no binary at {binary}; build it first")
    try:
        levels = [int(value) for value in arguments.connections.split(",")]
    except ValueError:
        parser.error("--connections takes comma-separated counts from 1 to 64")
    if not levels or any(level < 1 or level > 64 for level in levels) or len(set(levels)) != len(levels):
        parser.error("--connections takes distinct counts from 1 to 64")
    if not 1 <= arguments.rounds <= 10:
        parser.error("--rounds must be between 1 and 10")
    if arguments.total_exports is not None and not max(levels) <= arguments.total_exports <= 4096:
        parser.error("--total-exports must be at least the exporter count and at most 4096")
    if arguments.ingest_concurrency is not None and not 1 <= arguments.ingest_concurrency <= 64:
        parser.error("--ingest-concurrency must be between 1 and 64")
    result = {
        "date": time.strftime("%Y-%m-%d"), "measurement_schema": 2,
        "host": {"platform": sys.platform, "logical_cpus": os.cpu_count()},
        "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "binary_version": subprocess.check_output([str(binary), "version"], text=True, timeout=10).strip(),
        "ingest_concurrency": arguments.ingest_concurrency or "default",
        "workload_mode": "fixed_total" if arguments.total_exports is not None else "per_connection",
        "total_exports_per_level": arguments.total_exports, "exports_per_connection": None if arguments.total_exports is not None else EXPORTS_EACH,
        "warmup_exports": WARMUP_EXPORTS, "rounds": arguments.rounds,
        "timing": "preconnected clients released together; durable HTTP responses; counts summed, rates/latencies median across rounds (latency percentiles cover accepted requests only)",
        "fixture": harness.workload.__doc__, "observations": [],
    }
    for round_index in range(arguments.rounds):
        for level in level_order(levels, round_index):
            measurement = run_level(binary, level, arguments.ingest_concurrency, arguments.total_exports)
            measurement["round"] = round_index + 1
            print(json.dumps(measurement), file=sys.stderr, flush=True)
            result["observations"].append(measurement)
    result["levels"] = summarize_levels(levels, result["observations"])
    if arguments.out:
        Path(arguments.out).parent.mkdir(parents=True, exist_ok=True)
        Path(arguments.out).write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
