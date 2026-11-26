# Go checkout example

Run commands from the repository root with Go 1.26 or later. This example uses
Geko's existing module dependencies and the official OpenTelemetry Go SDK.

Start Geko in one terminal:

```powershell
go run ./cmd/geko serve
```

In another PowerShell terminal, configure the exporter and start the example:

```powershell
$env:OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = 'http://127.0.0.1:4318/v1/traces'
go run ./examples/go
```

For bash or zsh:

```sh
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces go run ./examples/go
```

The exporter uses HTTP protobuf. With no OTEL transport configuration, this
example defaults to `http://127.0.0.1:4318/v1/traces`, matching local Geko.
Explicit endpoint or TLS variables retain the SDK's behavior and precedence.
Change the endpoint when Geko uses another port.

If the terminal says `server gave HTTP response to HTTPS client`, the exporter
is trying HTTPS against a plain HTTP receiver. Stop the example, set the full
`http://.../v1/traces` endpoint above and restart it; existing process settings
do not change when environment variables are changed in another terminal.
Generate a new request afterwards. Trace IDs from rejected exports are not
proof that a trace reached Geko.
The SDK reads `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES`, including
`service.namespace`, environment, version and `geko.run.id`. Without a service
override it uses `geko-go-checkout`. The Connect page and `geko run` settings
therefore carry through to exported spans.
The example listens only on `127.0.0.1:8080`; that port must be available.

Wait for the example to print its listening URL. Open the
[checkout example](http://127.0.0.1:8080/) and select a scenario to generate a
trace. You can also send the requests from a third terminal.

```powershell
curl.exe -i http://127.0.0.1:8080/success
curl.exe -i http://127.0.0.1:8080/failure
curl.exe -i http://127.0.0.1:8080/retry
```

Use `curl` in bash or zsh. The HTTP 500 response from `/failure` is intentional.

| Route | Response | Expected Geko behavior |
| --- | --- | --- |
| `/success` | 200 | Trace stored, absent from Failures. |
| `/failure` | 500 | Server error with an exception on `payment.authorize`. |
| `/retry` | 200 | Successful request with exception evidence from the first payment attempt. |

Each response includes its trace ID in the body and `X-Trace-ID` header. Wait
about five seconds for batch export, then use **Go to trace ID** on
[Failures](http://127.0.0.1:4318/failures) to open any of the three traces by
its ID. Filter Failures by `geko-go-checkout` to browse the failure and retry
scenarios. The retry trace retains HTTP 200 as its request result even
though its first child span failed.

These are deliberately simulated payment operations, without outbound payment
requests or real customer data. Durations reflect execution time, not simulated
network latency. Every request starts a new trace; this example does not extract
incoming trace context. Unknown routes and methods are not instrumented.

Press Ctrl+C to drain HTTP requests and flush pending telemetry before exit.
Exporter delivery is best effort; Geko must be running for export to succeed.

## Verification

```sh
go test ./examples/go
```

The integration test sends all three scenarios through the real OTLP HTTP
exporter into Geko's receiver and SQLite. It verifies exception persistence,
request outcomes, exclusion of successful requests and export on SDK shutdown.

See the [official instrumentation guide](https://opentelemetry.io/docs/languages/go/instrumentation/)
for the distinction between recording an exception and setting span error status.
