"""Compare local trace backends using identical OTLP/HTTP batches."""

import argparse
import base64
import copy
import json
import math
import statistics
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

CONFIGS = {
    "jaeger_badger": """service:
  extensions: [jaeger_storage, jaeger_query]
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [jaeger_storage_exporter]
extensions:
  jaeger_query:
    storage:
      traces: local
  jaeger_storage:
    backends:
      local:
        badger:
          directories: {keys: /tmp/geko-comparison, values: /tmp/geko-comparison}
          ephemeral: false
receivers:
  otlp:
    protocols:
      http: {endpoint: '0.0.0.0:4318'}
processors:
  batch: {}
exporters:
  jaeger_storage_exporter:
    trace_storage: local
""",
    "tempo_local": """server:
  http_listen_port: 3200
distributor:
  receivers:
    otlp:
      protocols:
        http: {endpoint: '0.0.0.0:4318'}
storage:
  trace:
    backend: local
    wal: {path: /tmp/geko-comparison/wal}
    local: {path: /tmp/geko-comparison/blocks}
usage_report:
  reporting_enabled: false
""",
}


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


def endpoint(container, port):
    return "http://" + docker("port", container, f"{port}/tcp").splitlines()[0]


def wait_ready(url):
    for _ in range(60):
        try:
            with urllib.request.urlopen(url, timeout=1):
                return
        except OSError:
            time.sleep(0.5)
    raise RuntimeError(f"server did not become ready: {url}")


def trace_id(number):
    return f"927c583d264b4cc4{number:016x}"


def workload(batches):
    template = Path(__file__).resolve().parents[1] / "examples/otlp-json/trace.json"
    sample = json.loads(template.read_text(encoding="utf-8"))
    scope = sample["resourceSpans"][0]["scopeSpans"][0]
    span = scope["spans"][0]
    now = time.time_ns()
    for batch in range(batches):
        traces = []
        for offset in range(64):
            number = batch * 64 + offset + 1
            root = dict(span, traceId=trace_id(number), spanId=f"{number:016x}",
                        startTimeUnixNano=str(now - 3_000_000), endTimeUnixNano=str(now))
            child = dict(copy.deepcopy(root), parentSpanId=root["spanId"],
                         spanId=f"{number + batches * 64:016x}", name="inventory.lookup",
                         kind=1, startTimeUnixNano=str(now - 2_000_000), endTimeUnixNano=str(now - 1_000_000))
            traces.extend((root, child))
        scope["spans"] = traces
        yield json.dumps(sample, separators=(",", ":")).encode().replace(b"@SERVICE@", b"benchmark")


def sample_stats(container):
    return json.loads(docker("stats", "--no-stream", "--format", "{{json .}}", container))


def percentile(values, fraction):
    return sorted(values)[math.ceil(len(values) * fraction) - 1]


def query_trace(query_url, trace_path, number, headers, wait=False):
    trace = trace_id(number)
    encoded = base64.b64encode(bytes.fromhex(trace))
    request = urllib.request.Request(query_url + trace_path + trace, headers={**headers, "Accept": "application/json"})
    for _ in range(40 if wait else 1):
        start = time.perf_counter()
        try:
            with urllib.request.urlopen(request, timeout=5) as response:
                body = response.read()
        except urllib.error.HTTPError as error:
            if not wait or error.code != 404:
                raise
        else:
            elapsed = time.perf_counter() - start
            if (trace.encode() in body or encoded in body) and b"GET /inventory" in body and b"inventory.lookup" in body:
                return elapsed
        if wait:
            time.sleep(0.25)
    raise RuntimeError(f"trace {trace} with both expected spans was not queryable")


def measure(image, options, query_port, health_path, trace_path, batches, headers, command=()):
    container = docker("run", "-d", "--rm", "-p", "127.0.0.1::4318",
                       *([] if query_port == 4318 else ["-p", f"127.0.0.1::{query_port}"]),
                       *options, image, *command)
    try:
        query_url = endpoint(container, query_port)
        wait_ready(query_url + health_path)
        before = sample_stats(container)
        disk_before = int(docker("inspect", "--size", "--format", "{{.SizeRw}}", container))
        url = endpoint(container, 4318) + "/v1/traces"
        durations = []
        for index, payload in enumerate(batches, 1):
            request = urllib.request.Request(url, payload, {"Content-Type": "application/json", **headers})
            start = time.perf_counter()
            try:
                with urllib.request.urlopen(request, timeout=15) as response:
                    result = json.loads(response.read() or b"{}")
                    partial = result.get("partialSuccess") or {}
                    if int(partial.get("rejectedSpans", 0)) or partial.get("errorMessage"):
                        raise RuntimeError(f"partial export from {image}: {result}")
            except urllib.error.HTTPError as error:
                raise RuntimeError(f"{image} rejected batch {index}: {error.code} {error.read().decode()}") from error
            durations.append(time.perf_counter() - start)
        after = sample_stats(container)
        disk_after = int(docker("inspect", "--size", "--format", "{{.SizeRw}}", container))
        trace_count = 64 * len(batches)
        query_trace(query_url, trace_path, trace_count, headers, wait=True)
        queries = [query_trace(query_url, trace_path, (index * trace_count // 16) + 1, headers)
                   for index in range(16)]
        return {
            "image_id": docker("image", "inspect", "--format", "{{.Id}}", image),
            "idle_memory": before["MemUsage"].split(" / ")[0],
            "post_ingest_memory": after["MemUsage"].split(" / ")[0],
            "writable_layer_before_bytes": disk_before,
            "writable_layer_after_bytes": disk_after,
            "writable_layer_growth_bytes": disk_after - disk_before,
            "median_export_ms": round(statistics.median(durations) * 1000, 2),
            "p95_export_ms": round(percentile(durations, .95) * 1000, 2),
            "median_trace_lookup_ms": round(statistics.median(queries) * 1000, 2),
            "p95_trace_lookup_ms": round(percentile(queries, .95) * 1000, 2),
        }
    except (OSError, RuntimeError):
        print(docker("logs", container), file=sys.stderr)
        raise
    finally:
        docker("rm", "-f", container)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--geko", default="geko:local-comparison")
    parser.add_argument("--jaeger", default="cr.jaegertracing.io/jaegertracing/jaeger:2.23.0")
    parser.add_argument("--tempo", metavar="IMAGE", help="also measure Tempo in monolithic local-storage mode")
    parser.add_argument("--jaeger-badger", action="store_true", help="also measure Jaeger with persistent Badger storage")
    parser.add_argument("--rounds", type=int, default=1, help="fresh-container repetitions with rotated backend order")
    parser.add_argument("--batches", type=int, default=32)
    parser.add_argument("--geko-only", action="store_true", help="smoke-test Geko without running Jaeger")
    args = parser.parse_args()
    if not 1 <= args.batches <= 1000:
        parser.error("--batches must be between 1 and 1000")
    if not 1 <= args.rounds <= 10 or args.geko_only and (args.tempo or args.jaeger_badger):
        parser.error("choose 1–10 rounds and use --geko-only without competitor options")
    token = "local-benchmark-token-only-do-not-reuse"
    geko_options = [
        "-e", "GEKO_LISTEN_ADDR=0.0.0.0:4318", "-e", "GEKO_ALLOW_PLAINTEXT=true",
        "-e", f"GEKO_ACCESS_TOKEN={token}", "-e", "GEKO_REDACTION=best_effort",
    ]
    result = {
        "traces": 64 * args.batches, "spans": 128 * args.batches,
        "batches": args.batches,
    }
    with tempfile.TemporaryDirectory(prefix="geko-benchmark-") as directory:
        def config_mount(name):
            path = Path(directory) / f"{name}.yaml"
            path.write_text(CONFIGS[name], encoding="utf-8")
            return ["--mount", f"type=bind,source={path},target=/benchmark.yaml,readonly"]

        backends = {"geko_sqlite": (args.geko, geko_options, 4318, "/healthz", "/api/v1/traces/",
                                     {"Authorization": f"Bearer {token}"}, ())}
        if not args.geko_only:
            backends["jaeger_in_memory"] = (args.jaeger, [], 16686, "/", "/api/traces/", {}, ())
        if args.jaeger_badger:
            backends["jaeger_badger"] = (args.jaeger, config_mount("jaeger_badger"), 16686, "/", "/api/traces/", {},
                                          ("--config", "/benchmark.yaml"))
        if args.tempo:
            backends["tempo_local"] = (args.tempo, config_mount("tempo_local"), 3200, "/ready", "/api/traces/", {},
                                        ("-config.file=/benchmark.yaml", "-target=all"))
        rounds = []
        names = list(backends)
        for index in range(args.rounds):
            batches = list(workload(args.batches))
            offset = index % len(names)
            values = {}
            for name in names[offset:] + names[:offset]:
                print(f"Round {index + 1}/{args.rounds}: {name}", file=sys.stderr, flush=True)
                image, options, port, health, trace, headers, command = backends[name]
                values[name] = measure(image, options, port, health, trace, batches, headers, command)
            rounds.append(values)
        if args.rounds == 1:
            result.update(rounds[0])
        else:
            result["rounds"] = rounds
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
