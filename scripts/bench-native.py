"""Measure one Geko process end to end, without Docker or another backend.

Build the binary you want to measure, then run the harness:

    go build -o bin/geko ./cmd/geko
    python scripts/bench-native.py --binary bin/geko --rounds 3

Every round starts a fresh server on a free loopback port with its own
temporary database, so no round sees another round's data. Each round:

 1. records resident memory and database size while idle,
 2. sends --batches exports over one reused connection, which is how an
    OpenTelemetry SDK exports, and records per-export latency,
 3. sends 16 more exports over a new connection each time, to separate
    connection setup from the server's own work,
 4. sends 128 exports over four connections at once, for peak throughput,
 5. reads the API and the dashboard over a reused connection, after one
    discarded request, so the figures exclude connection setup.

Every trace in the fixture records HTTP 503 and an error status, so the
failure path is exercised for all of them. Memory is a snapshot of the
process working set; database size includes the WAL and shared-memory files.
"""

import argparse
from contextlib import closing
import copy
import http.client
import json
import math
import os
import socket
import sqlite3
import statistics
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
TOKEN = "local-benchmark-token-only-do-not-reuse"
COLD_EXPORTS = 16
PARALLEL_CONNECTIONS = 4
PARALLEL_EXPORTS_EACH = 32
QUERY_REQUESTS = 100


def free_port():
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


def trace_id(number):
    return f"927c583d264b4cc4{number:016x}"


def workload(batches):
    """One OTLP/HTTP JSON export per batch: 64 two-span traces, all failing."""
    sample = json.loads((REPO / "examples/otlp-json/trace.json").read_text(encoding="utf-8"))
    scope = sample["resourceSpans"][0]["scopeSpans"][0]
    span = scope["spans"][0]
    now = time.time_ns()
    payloads = []
    for batch in range(batches):
        spans = []
        for offset in range(64):
            number = batch * 64 + offset + 1
            root = dict(span, traceId=trace_id(number), spanId=f"{number:016x}",
                        startTimeUnixNano=str(now - 3_000_000), endTimeUnixNano=str(now))
            child = dict(copy.deepcopy(root), parentSpanId=root["spanId"],
                         spanId=f"{number + batches * 64:016x}", name="inventory.lookup",
                         kind=1, startTimeUnixNano=str(now - 2_000_000), endTimeUnixNano=str(now - 1_000_000))
            spans.extend((root, child))
        scope["spans"] = spans
        payloads.append(json.dumps(sample, separators=(",", ":")).encode().replace(b"@SERVICE@", b"benchmark"))
    return payloads


def percentile(values, fraction):
    return sorted(values)[math.ceil(len(values) * fraction) - 1]


def working_set_mib(pid):
    if sys.platform == "win32":
        output = subprocess.check_output(
            ["powershell", "-NoProfile", "-Command", f"(Get-Process -Id {pid}).WorkingSet64"], text=True)
        return round(int(output.strip()) / 1024 / 1024, 2)
    output = subprocess.check_output(["ps", "-o", "rss=", "-p", str(pid)], text=True)
    return round(int(output.strip()) / 1024, 2)


def wait_ready(url):
    for _ in range(120):
        try:
            with urllib.request.urlopen(url, timeout=1):
                return
        except OSError:
            time.sleep(0.5)
    raise RuntimeError(f"server did not become ready: {url}")


def export(connection, payload, headers, on_refusal=None):
    """Send one export. False means a temporary OTLP 503 refusal."""
    connection.request("POST", "/v1/traces", payload, {"Content-Type": "application/json", **headers})
    response = connection.getresponse()
    body = response.read()
    if response.status == 503:
        if on_refusal is not None:
            on_refusal(body)
        return False
    if response.status != 200:
        raise RuntimeError(f"export rejected: {response.status} {body[:300]!r}")
    partial = (json.loads(body or b"{}").get("partialSuccess") or {})
    if int(partial.get("rejectedSpans", 0)) or partial.get("errorMessage"):
        raise RuntimeError(f"partial export: {partial}")
    return True


def query_until_complete(base, headers, number):
    """Wait for a trace to hold both of its spans, then return its lookup time."""
    for _ in range(80):
        start = time.perf_counter()
        try:
            request = urllib.request.Request(f"{base}/api/v1/traces/{trace_id(number)}",
                                             headers={**headers, "Accept": "application/json"})
            with urllib.request.urlopen(request, timeout=10) as response:
                body = response.read()
        except urllib.error.HTTPError as error:
            if error.code != 404:
                raise
        else:
            if b"GET /inventory" in body and b"inventory.lookup" in body:
                return time.perf_counter() - start
        time.sleep(0.25)
    raise RuntimeError(f"trace {trace_id(number)} never became queryable")


def mean_request_ms(port, path, headers, count=QUERY_REQUESTS):
    """Mean milliseconds per request over a reused connection, setup excluded."""
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
    try:
        request_headers = {**headers, "Accept": "application/json"}
        connection.request("GET", path, headers=request_headers)
        connection.getresponse().read()
        start = time.perf_counter()
        for _ in range(count):
            connection.request("GET", path, headers=request_headers)
            response = connection.getresponse()
            body = response.read()
            if response.status != 200:
                raise RuntimeError(f"{path} returned {response.status}")
        return round((time.perf_counter() - start) / count * 1000, 2), len(body)
    finally:
        connection.close()


def database_bytes(directory):
    return {name: (Path(directory) / name).stat().st_size if (Path(directory) / name).exists() else 0
            for name in ("geko.db", "geko.db-wal", "geko.db-shm")}


def group_cache_shape(directory):
    """Read-only diagnostics after ingest; no telemetry strings leave SQLite."""
    try:
        return read_group_cache_shape(directory)
    except sqlite3.DatabaseError:
        # Host Python can ship an older SQLite than Geko's embedded engine.
        # Optional diagnostics must not invalidate otherwise successful runs.
        return {"unavailable": "host SQLite could not inspect the cache", "sqlite_version": sqlite3.sqlite_version}


def read_group_cache_shape(directory):
    uri = (Path(directory) / "geko.db").resolve().as_uri() + "?mode=ro"
    with closing(sqlite3.connect(uri, uri=True, timeout=10)) as database:
        database.execute("BEGIN")
        tables = {row[0] for row in database.execute("SELECT name FROM sqlite_schema WHERE type='table'")}
        if "failure_group_members" not in tables:
            return {}
        result = {"members": database.execute("SELECT count(*) FROM failure_group_members").fetchone()[0]}
        if "failure_group_partitions" in tables:
            result["partitions"] = database.execute("SELECT count(*) FROM failure_group_partitions").fetchone()[0]
            result["identities"] = database.execute("SELECT count(*) FROM failure_group_keys").fetchone()[0]
            result["cached_members"] = database.execute("SELECT count(*) FROM failure_group_members WHERE partition_id IS NOT NULL").fetchone()[0]
            result["partitions_per_cached_member"] = round(result["partitions"] / result["cached_members"], 6) if result["cached_members"] else None
        return result


def run_round(binary, batches, label):
    port = free_port()
    parallel = PARALLEL_CONNECTIONS * PARALLEL_EXPORTS_EACH
    with tempfile.TemporaryDirectory(prefix="geko-bench-", ignore_cleanup_errors=True) as directory:
        env = {**os.environ, "GEKO_CONFIG": "", "GEKO_LISTEN_ADDR": f"127.0.0.1:{port}",
               "GEKO_DB_PATH": str(Path(directory) / "geko.db"), "GEKO_ACCESS_TOKEN": TOKEN,
               "GEKO_REDACTION": "best_effort", "GEKO_RETENTION_DAYS": "7", "GEKO_MAX_DB_MIB": "1024"}
        log = open(Path(directory) / "server.log", "wb")
        process = subprocess.Popen([str(binary), "serve"], env=env, stdout=log, stderr=log, cwd=directory)
        headers = {"Authorization": f"Bearer {TOKEN}"}
        base = f"http://127.0.0.1:{port}"
        try:
            wait_ready(f"{base}/healthz")
            time.sleep(1)
            idle_memory, idle_database = working_set_mib(process.pid), database_bytes(directory)

            payloads = workload(batches + COLD_EXPORTS + parallel)
            connection = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
            exports = []
            sequential_start = time.perf_counter()
            for payload in payloads[:batches]:
                start = time.perf_counter()
                if not export(connection, payload, headers):
                    raise RuntimeError("one connection should never exhaust the ingest slots")
                exports.append(time.perf_counter() - start)
            sequential_wall = time.perf_counter() - sequential_start
            connection.close()

            cold = []
            for payload in payloads[batches:batches + COLD_EXPORTS]:
                fresh = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
                start = time.perf_counter()
                try:
                    if not export(fresh, payload, headers):
                        raise RuntimeError("one connection should never exhaust the ingest slots")
                finally:
                    fresh.close()
                cold.append(time.perf_counter() - start)

            failures, counted, lock = [], {"accepted": 0, "busy": 0}, threading.Lock()

            def send_chunk(chunk):
                parallel_connection = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
                accepted = busy = 0
                try:
                    for payload in chunk:
                        if export(parallel_connection, payload, headers):
                            accepted += 1
                        else:
                            busy += 1
                except Exception as error:  # reported after the threads join
                    failures.append(error)
                finally:
                    parallel_connection.close()
                    with lock:
                        counted["accepted"] += accepted
                        counted["busy"] += busy

            chunks = [payloads[batches + COLD_EXPORTS:][i * PARALLEL_EXPORTS_EACH:(i + 1) * PARALLEL_EXPORTS_EACH]
                      for i in range(PARALLEL_CONNECTIONS)]
            threads = [threading.Thread(target=send_chunk, args=(chunk,)) for chunk in chunks]
            parallel_start = time.perf_counter()
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join()
            parallel_wall = time.perf_counter() - parallel_start
            if failures:
                raise failures[0]

            query_until_complete(base, headers, 64 * batches)
            post_memory, post_database = working_set_mib(process.pid), database_bytes(directory)
            stored_spans = 128 * (batches + COLD_EXPORTS + counted["accepted"])
            failures_api, failures_bytes = mean_request_ms(port, "/api/v1/failures?limit=50", headers)
            search_api, _ = mean_request_ms(port, "/api/v1/failures?q=inventory&limit=50", headers)
            trace_api, trace_bytes = mean_request_ms(port, f"/api/v1/traces/{trace_id(32 * batches)}", headers)
            failures_page, _ = mean_request_ms(port, "/failures", headers)
            groups_page, _ = mean_request_ms(port, "/failures?view=groups", headers)
            overview_page, _ = mean_request_ms(port, "/overview", headers)
            return {
                "label": label,
                "idle_memory_mib": idle_memory,
                "post_ingest_memory_mib": post_memory,
                "idle_database_bytes": idle_database,
                "post_ingest_database_bytes": post_database,
                "database_growth_bytes": sum(post_database.values()) - sum(idle_database.values()),
                "stored_spans": stored_spans,
                "group_cache_shape": group_cache_shape(directory),
                "bytes_per_span": round((sum(post_database.values()) - sum(idle_database.values())) / stored_spans, 1),
                "sequential_exports": batches,
                "sequential_spans_per_second": round(128 * batches / sequential_wall),
                "median_export_ms": round(statistics.median(exports) * 1000, 2),
                "p95_export_ms": round(percentile(exports, .95) * 1000, 2),
                "max_export_ms": round(max(exports) * 1000, 2),
                "median_new_connection_export_ms": round(statistics.median(cold) * 1000, 2),
                "parallel_connections": PARALLEL_CONNECTIONS,
                "parallel_exports_sent": parallel,
                "parallel_exports_rejected_503": counted["busy"],
                "parallel_spans": 128 * counted["accepted"],
                "parallel_spans_per_second": round(128 * counted["accepted"] / parallel_wall),
                "mean_failures_api_ms": failures_api,
                "mean_search_api_ms": search_api,
                "mean_trace_api_ms": trace_api,
                "mean_failures_page_ms": failures_page,
                "mean_groups_page_ms": groups_page,
                "mean_overview_page_ms": overview_page,
                "failures_api_response_bytes": failures_bytes,
                "trace_api_response_bytes": trace_bytes,
            }
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
    parser.add_argument("--binary", default="bin/geko", help="geko binary to measure")
    parser.add_argument("--batches", type=int, default=128, help="sequential exports per round")
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--out", help="write the full measurements to this JSON file")
    arguments = parser.parse_args()
    if not 1 <= arguments.batches <= 1000 or not 1 <= arguments.rounds <= 10:
        parser.error("use 1-1000 batches and 1-10 rounds")
    binary = Path(arguments.binary).resolve()
    if not binary.exists():
        parser.error(f"no binary at {binary}; build it with go build -o {arguments.binary} ./cmd/geko")
    result = {
        "date": time.strftime("%Y-%m-%d"),
        "host": {"platform": sys.platform, "logical_cpus": os.cpu_count()},
        "sequential_traces": 64 * arguments.batches,
        "sequential_spans": 128 * arguments.batches,
        "batches": arguments.batches,
        "fixture": "OTLP/HTTP JSON, 64 two-span traces per export, every trace HTTP 503 with an error status",
        "rounds": [],
    }
    for index in range(arguments.rounds):
        print(f"round {index + 1}/{arguments.rounds}", file=sys.stderr, flush=True)
        result["rounds"].append(run_round(binary, arguments.batches, f"round-{index + 1}"))
    numeric = [key for key, value in result["rounds"][0].items() if isinstance(value, (int, float))]
    result["median_of_rounds"] = {key: round(statistics.median(round_[key] for round_ in result["rounds"]), 2)
                                 for key in numeric}
    if arguments.out:
        Path(arguments.out).parent.mkdir(parents=True, exist_ok=True)
        Path(arguments.out).write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(result["median_of_rounds"], indent=2))


if __name__ == "__main__":
    main()
