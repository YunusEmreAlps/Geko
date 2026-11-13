# Connect your application

Exporter settings, ports and receiver limits. [Back to the README](../README.md).

Point your OpenTelemetry trace exporter at Geko. Your application keeps its
own port; Geko listens on 4318.

OTLP/gRPC and a separate dashboard port are optional. For example, add these
settings to `geko.yaml` and restart Geko:

```yaml
listen_addr: "127.0.0.1:14318"
grpc_addr: "127.0.0.1:14317"
dashboard_addr: "127.0.0.1:8081"
```

Open the dashboard at `http://127.0.0.1:8081`. Send HTTP traces to
`http://127.0.0.1:14318/v1/traces`, or configure your SDK for gRPC:

```sh
OTEL_EXPORTER_OTLP_PROTOCOL=grpc
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:14317
```

These are SDK environment settings; use your shell's environment-variable
syntax. A gRPC endpoint has no `/v1/traces` suffix. Use `127.0.0.1:4317` for
the conventional gRPC port when available. The alternative ports above avoid
conflicting with a local Collector. If `dashboard_addr` is set, only that
listener serves the dashboard and query API; HTTP ingest remains on
`listen_addr`. Listeners default to loopback. `geko doctor` checks every
configured port. Both transports share redaction, capacity policy, admission
limits, and the receiver counters shown in Settings.

```sh
# bash / zsh
export OTEL_SERVICE_NAME=orders-api
export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces
```

```powershell
# PowerShell
$env:OTEL_SERVICE_NAME = 'orders-api'
$env:OTEL_EXPORTER_OTLP_TRACES_PROTOCOL = 'http/protobuf'
$env:OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = 'http://127.0.0.1:4318/v1/traces'
```

Send a request to your application, then open **Settings → Receiver**. It shows
the effective trace endpoint, the last accepted export, and rejection counts by
reason.

> [!IMPORTANT]
> The HTTP receiver accepts OTLP protobuf (`http/protobuf`) or JSON
> (`http/json`), uncompressed or gzip. OTLP/gRPC is available when `grpc_addr`
> is configured. Some SDKs default to gRPC on port 4317, so match the SDK protocol
> and endpoint to an enabled receiver.

| Limit | Value | What happens |
| --- | --- | --- |
| Request body | 4 MiB, before and after gzip | HTTP 413 |
| Spans per request | 2,048 | HTTP 413. SDK defaults (512) fit; lower a Collector's batch size if needed. |
| Invalid spans in a batch | Dropped individually | Valid spans are stored. The response reports `partial_success` and Settings counts them. |
| Concurrent exports | 4 | HTTP 503 with `Retry-After` |

If another process such as a local OpenTelemetry Collector already uses port
4318, set a different [`listen_addr`](configuration.md) and copy the new trace
endpoint from **Settings → Receiver**.
