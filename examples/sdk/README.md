# Real OpenTelemetry SDK examples

These examples start a temporary **loopback-only** HTTP server on a free port,
make one successful and one intentionally failing request, print their trace
IDs as JSON lines, flush the SDK and exit. They use official SDK/exporter
packages, not hand-built OTLP JSON. No payment service is contacted.
The request and child spans are recorded manually to keep the scenarios
deterministic; these are not complete framework auto-instrumentation guides.

For Go's interactive `/success`, `/failure` and `/retry` server, use
[`examples/go`](../go). The [`otlp-json`](../otlp-json) examples remain useful
for testing the wire protocol without SDK dependencies.

## Configure Geko and the exporter

Start `geko serve` in another terminal. These examples send traces only. For
Node and Python select `http/protobuf`; Java and .NET also accept their SDK's
gRPC configuration when Geko's optional gRPC listener is enabled.

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_METRICS_EXPORTER=none
export OTEL_LOGS_EXPORTER=none
export OTEL_RESOURCE_ATTRIBUTES=service.namespace=SDKExamples,deployment.environment.name=development,service.version=1.0.0
```

PowerShell equivalent:

```powershell
$env:OTEL_EXPORTER_OTLP_ENDPOINT = 'http://127.0.0.1:4318'
$env:OTEL_EXPORTER_OTLP_PROTOCOL = 'http/protobuf'
$env:OTEL_METRICS_EXPORTER = 'none'
$env:OTEL_LOGS_EXPORTER = 'none'
$env:OTEL_RESOURCE_ATTRIBUTES = 'service.namespace=SDKExamples,deployment.environment.name=development,service.version=1.0.0'
```

Override `OTEL_SERVICE_NAME` to match the service selected on **Connect**. If
Geko requires authentication, use an ingest token through
`OTEL_EXPORTER_OTLP_HEADERS="authorization=Bearer%20<ingest-token>"`. Do not
commit real tokens. An application's port is separate from Geko's OTLP port.

Run each command from the repository root unless indicated otherwise.

| Language | Requirements | Install/build | Run |
| --- | --- | --- | --- |
| Node.js | Node ≥22.15 | `npm ci --prefix examples/sdk/node --ignore-scripts` | `node examples/sdk/node/main.mjs` |
| Python | Python ≥3.10 | Create a virtual environment, then `python -m pip install -r examples/sdk/python/requirements.txt` | `python examples/sdk/python/main.py` |
| Java | JDK 21 and Maven 3.9 | `mvn -B -f examples/sdk/java/pom.xml package` | `java -jar examples/sdk/java/target/geko-sdk-example-1.0.0.jar` |
| .NET | .NET 10 SDK | `dotnet build examples/sdk/dotnet/GekoSdkExample.csproj -c Release` | `dotnet examples/sdk/dotnet/bin/Release/net10.0/GekoSdkExample.dll` |

For an isolated Python installation:

```sh
python -m venv examples/sdk/python/.venv
# Bash
source examples/sdk/python/.venv/bin/activate
# PowerShell instead: ./examples/sdk/python/.venv/Scripts/Activate.ps1
python -m pip install -r examples/sdk/python/requirements.txt
python examples/sdk/python/main.py
```

If local NuGet configuration has no public package source, restore explicitly:
`dotnet restore examples/sdk/dotnet/GekoSdkExample.csproj --source https://api.nuget.org/v3/index.json`.
The examples do not modify your global runtime, package-source or PATH settings.

## What to look for

Each program prints two records with `scenario`, `status` and `trace_id`:

- `success`: HTTP 200, stored under **All traces**, with no failure evidence.
- `failure`: HTTP 500, with an exception on `payment.authorize`, including a
  stack trace when Geko's redaction mode retains it.

Paste either trace ID into **Go to trace ID**. The exception is deliberate;
the program exits successfully after exercising both scenarios. This is not
a test command that should itself fail on the synthetic 500.

On a token-protected server, Geko defaults to `strict`: exception messages and
stack traces are omitted. `best_effort` retains them after masking. The smoke
test uses `best_effort` only on its temporary, synthetic-data receiver.

**Connect → Start a 60-second check** observes newly accepted telemetry. Run
an example, then select **Check again**. An SDK printing a trace ID or exiting
successfully does not prove export succeeded; inspect Geko or run the smoke
test. Batching, sampling and retry policies belong to the SDK.

## Use a run ID

After building/installing an example, wrap its run command:

```sh
geko run -- node examples/sdk/node/main.mjs
geko run -- python examples/sdk/python/main.py
geko run -- java -jar examples/sdk/java/target/geko-sdk-example-1.0.0.jar
geko run -- dotnet examples/sdk/dotnet/bin/Release/net10.0/GekoSdkExample.dll
```

The SDK reads `geko.run.id` from the resource environment set by `geko run`.
Keep existing application resource attributes. Do not put a fixed run ID in
application code. These finite examples explicitly drain exports before exit.

## Automated verification

Build Geko and the examples, install their dependencies, then run:

```sh
go build -o bin/geko ./cmd/geko
python scripts/smoke-sdk-examples.py --binary bin/geko
```

Use `--languages node python` for a subset, and `--python`, `--node`, `--java`
or `--dotnet` to select a runtime. On Windows, pass `bin/geko.exe` and, when
using the virtual environment, `--python examples/sdk/python/.venv/Scripts/python.exe`.

The test starts and stops its own token-protected Geko on a free loopback port,
generates ephemeral credentials and run IDs, and checks stored evidence after
each program exits. It verifies service/namespace/environment/version, HTTP
outcomes, successful-trace exclusion, exception stack traces and run-ID
filtering. It never uses your normal Geko configuration or database. CI runs
the same check in the `sdk-examples` job; local success does not certify a
hosted job that has not run yet.

Official references: [Node SDK](https://open-telemetry.github.io/opentelemetry-js/modules/_opentelemetry_sdk-node.html),
[Python SDK](https://opentelemetry.io/docs/languages/python/instrumentation/),
[Java autoconfiguration](https://opentelemetry.io/docs/languages/java/configuration/),
[.NET exporter](https://opentelemetry.io/docs/languages/dotnet/exporters/).
