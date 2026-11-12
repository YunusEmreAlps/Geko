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

The exporter uses HTTP protobuf. Change the endpoint when Geko uses another port.
The example listens only on `127.0.0.1:8080`; that port must be available.

Open these URLs in your browser, or run the commands in a third terminal:

```sh
curl -i http://127.0.0.1:8080/success
curl -i http://127.0.0.1:8080/failure
curl -i http://127.0.0.1:8080/retry
```

Use `curl.exe` on Windows if PowerShell aliases `curl` to `Invoke-WebRequest`.
The HTTP 500 response from `/failure` is intentional.

| Route | Response | Expected Geko behavior |
| --- | --- | --- |
| `/success` | 200 | Trace stored, absent from Failures. |
| `/failure` | 500 | Server error with an exception on `payment.authorize`. |
| `/retry` | 200 | Successful request with exception evidence from the first payment attempt. |

Each response includes its trace ID in the body and `X-Trace-ID` header. Wait
about five seconds for batch export, then use **Go to trace** on
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
