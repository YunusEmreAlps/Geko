# Official OpenTelemetry exporter compatibility checks

`scripts/otel-compatibility.py` uses actual official SDK exporters and checks
stored traces through Geko's authenticated API after SDK shutdown. Printing a
trace ID is insufficient: each cell must yield two accepted traces, a healthy
request without failure evidence, an exception with a stack trace, service and
namespace, environment, version and the run ID. The failures API must return
exactly one failure for that cell's unique run ID.

Every received span must also have `otel.present=true`. The Go SDK fixture
creates two independent traces and adds a real span link from the failing
trace to the successful trace. A non-exception `retry` event carries an int64
`retry.attempt=9007199254740993` and a double `retry.delay_ms=1.0`. The harness
checks stored link IDs and event values, then downloads OTLP JSON and requires
the exact `intValue` string and `doubleValue` types. This runs for the direct
and both Collector routes. Reports include Geko's version and binary SHA-256,
plus Collector's version and binary SHA-256 when used.

| SDK fixture | HTTP protobuf | HTTP JSON | gRPC |
| --- | --- | --- | --- |
| Node.js | Yes | Yes | Yes |
| Python | Yes | Not exercised | Yes |
| Java | Yes | Not exercised | Yes |
| .NET | Yes | Not exercised | Yes |
| Go | Yes | Not exercised | Yes |

These are tested fixture capabilities, not a claim that all SDKs support every
OTLP encoding. Node selects the official protobuf, JSON or gRPC exporter
package. Python selects its official HTTP or gRPC exporter. Java and .NET
reuse the real SDK examples and their environment configuration. Go uses a
finite HTTP protobuf/gRPC SDK fixture and never starts the interactive example on
port 8080. The hand-built payload examples under `examples/otlp-json` do not
count as official SDK compatibility evidence.

## Reproduce

Build Geko and the Java/.NET examples as described in
[SDK examples](../examples/sdk/README.md), then install the extra exporters:

```sh
go build -o bin/geko ./cmd/geko
npm ci --prefix scripts/otel-sdk --ignore-scripts --no-audit --no-fund
python -m pip install -r examples/sdk/python/requirements.txt opentelemetry-exporter-otlp-proto-grpc==1.45.2
python scripts/otel-compatibility.py --binary bin/geko --report dist/otel-direct.json
```

Use `--languages node python` and/or `--protocols http/protobuf` for a subset.
`--python`, `--node`, `--java`, `--dotnet` and `--go` select runtime executables.
On Windows use `bin/geko.exe` and your virtual environment's Python executable.
Unsupported cells are excluded; selecting no supported cells is an error.

The harness strips inherited `GEKO_*`/`OTEL_*` variables, generates temporary
credentials and run IDs, allocates free loopback ports and uses a temporary
database. It terminates only the children it started. It does not contact an
existing Geko, alter global runtime configuration or download dependencies.
Synthetic data is stored with `best_effort` redaction to verify stack traces.

## Official Collector hop

Download an official `otelcol` binary and pass its absolute path:

```sh
python scripts/otel-compatibility.py --binary bin/geko --collector /path/to/otelcol --report dist/otel-collector-http.json
python scripts/otel-compatibility.py --binary bin/geko --collector /path/to/otelcol --collector-export grpc --report dist/otel-collector-grpc.json
```

The harness starts the Collector with isolated HTTP and gRPC loopback
receivers and forwards to Geko with an ephemeral authorization header. The
first command verifies each SDK input protocol through Collector HTTP
protobuf output; the second verifies Collector gRPC output. Temporary config
and credentials are deleted after the children stop. Reports contain no
credentials. Export retries and API polling are bounded.

[The compatibility workflow](../.github/workflows/otel-compatibility.yml)
runs all eleven cells separately for direct, Collector HTTP and Collector gRPC
routes (33 cells). Collector 0.166.0 is downloaded from its official release
with its release checksum verified. The workflow supports manual dispatch;
creating this file does not dispatch or certify a hosted run.

The [2026-11-27 local results](benchmarks/2026-11-27-otel-compatibility.json)
record the observed cells and exact binary/Collector checksums. This evidence
describes that local run; hosted Linux workflow results are recorded separately
when the workflow actually runs.

Official references: [JavaScript exporters](https://opentelemetry.io/docs/languages/js/exporters/),
[Go exporters](https://opentelemetry.io/docs/languages/go/exporters/),
[Collector releases](https://github.com/open-telemetry/opentelemetry-collector-releases/releases).

## Recorded local validation — 2026-11-27

[Machine-readable results](benchmarks/2026-11-27-otel-compatibility.json) record
30 passing cells and 60 accepted traces on Windows/amd64: ten direct, ten via
Collector HTTP output and ten via Collector gRPC output. Every received span
reported metadata capture. The Go SDK fixture additionally verified an actual
cross-trace link and non-exception retry event, an integer above 2^53 and a
whole-valued double through the stored API and downloaded OTLP payload.

The record includes Geko/Collector versions and binary hashes; the official
Collector 0.166.0 archive was checksum verified. No hosted workflow was run.
Unexercised matrix cells remain explicitly listed rather than counted as passes.

## Go gRPC follow-through — 2026-11-27

The finite Go fixture selects the official HTTP protobuf or gRPC exporter from
`OTEL_EXPORTER_OTLP_TRACES_PROTOCOL`, falling back to
`OTEL_EXPORTER_OTLP_PROTOCOL` and then HTTP protobuf. Unsupported encodings fail
explicitly. Both exporters use their standard endpoint, TLS and header environment
configuration; no unconditional insecure override is added. The gRPC exporter
is pinned to v1.47.0, matching the existing SDK version.

```sh
python scripts/otel-compatibility.py --binary bin/geko --languages go --protocols grpc
```

The expanded matrix has eleven cells per route, 33 total. The earlier 30-cell
record above remains historical evidence. The
[follow-through record](benchmarks/2026-11-27-otel-compatibility-grpc.json) contains
33 passing cells and 66 accepted traces against the recorded binary hash. All
six Go protocol/route cells passed link/event/numeric-type checks. Python/Java/.NET/Go HTTP JSON remain unexercised, not
implicitly supported by those fixtures. See the
[official Go gRPC exporter](https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc@v1.47.0)
for environment configuration semantics.
