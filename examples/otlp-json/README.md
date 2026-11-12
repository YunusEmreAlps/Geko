# OTLP/HTTP JSON examples

Each client sends the same synthetic HTTP 503 trace using the standard
library. The programs exercise the wire protocol directly. For an example
that instruments real HTTP requests through the OpenTelemetry SDK, see the
[Go checkout example](../go/README.md).

Start Geko, then run a client from the repository root.

| Runtime | Command |
| --- | --- |
| Node.js 20+ | `node examples/otlp-json/node.mjs` |
| Python 3 | `python examples/otlp-json/python.py` |
| Java 11+ | `java examples/otlp-json/ExportTrace.java` |
| .NET 10 SDK | `dotnet run --file examples/otlp-json/ExportTrace.cs` |

The client prints the trace ID. Paste it into **Go to trace**, or choose the
`otlp-json-demo` application in **Failures**. Set
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` if Geko listens at a different URL.
