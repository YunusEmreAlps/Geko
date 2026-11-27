# Connect your application

Exporter settings, ports and receiver limits. [Back to the README](../README.md).

Point your OpenTelemetry trace exporter at Geko. Your application keeps its
own port; Geko listens on 4318.

For runnable applications using official SDKs, see [Go checkout](../examples/go/README.md)
and [Node.js, Python, Java and .NET examples](../examples/sdk/README.md). They
record real HTTP requests and flush exports before exit. On **Connect**, start
a 60-second check before running an example, then use **Check again** to see
newly accepted telemetry. A printed trace ID alone does not prove delivery.

## Follow delivery evidence

In source builds after v0.2.0, **Connect → Delivery diagnostics** brings four
existing sources together. The disclosure opens when a 60-second check starts;
exporter setup remains available even if one diagnostic source cannot be read.

| Stage | Evidence and scope | Next step |
| --- | --- | --- |
| Service observation | A recent committed export matching the selected service and namespace in the check window; not proof of credential or transport. | Generate a request, check again, then open the observed or retained service traces. A missing sample does not prove missing instrumentation. |
| Receiver | Accepted exports, partial rejections and rejection reasons across all applications, cumulative since this receiver started. | Compare exporter response codes with endpoint/protocol, batch limits, namespace/Host restrictions and admission/storage settings. Authentication failures happen before receiver counters; inspect Geko logs. |
| Storage | A bounded metadata read: main/WAL/reusable bytes, capacity policy, pinned traces and persistent eviction/rejection counts. These counters cover the database lifetime. | Review storage and retention; use the existing `geko doctor` for integrity and port checks. A file at its limit may still accept writes into reusable pages, and counters cannot identify a specific missing trace. |
| Collector | Whether sources are configured, and the time, coverage and severity counts of the last explicitly requested Collector Doctor report in this server session. | Open **Inspect Collector** for full evidence and recommendations; **Back to connection check** preserves the selected service/application/protocol/token name and observation start. |

Opening or refreshing Connect never scrapes a Collector or starts a polling
worker. Requesting **Collector Doctor**, its Refresh or sample comparison runs
that existing inspection and updates the shared summary. A summary older than
five minutes is labelled stale; every summary is historical evidence, not proof
of the Collector's current health or this service's delivery. Restarting Geko
clears it. An SDK exporting directly to Geko does not need a Collector check.
The shared summary contains no raw findings, configuration paths, metrics URLs
or credentials. Collector checks and return navigation retain the existing
dashboard access rules and work without JavaScript.

Storage diagnostics share dashboard query admission and have a 500 ms deadline.
Busy, failed or cancelled reads show **Unavailable** rather than blocking setup
behind a full database scan or suggesting that the database was integrity-checked.
Listener availability, SDK TLS configuration and external network delivery are
not probed by this page. Existing `geko doctor` and `geko doctor --collector`
commands retain their behavior.

## Exporter configuration

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

## Captured OpenTelemetry context

Source builds after v0.2.0 preserve bounded Span Links, flags, filtered trace
state, resource/scope schema URLs and producer-reported dropped counts. The
trace inspector separates **SDK-reported drops** from **Geko filtering**. These
are observations of this received span, not an end-to-end completeness claim.
Filtered-attribute counts describe omitted pairs; retained text can also contain
redaction replacements. No count certifies that all sensitive text was detected.
SDK counters do not count whole traces removed by upstream sampling.

Existing database records are retained; their new metadata field defaults to
unknown, not zero drops. Invalid/zero link IDs and links beyond 128 per span are
omitted and counted without discarding an otherwise valid span. Links open the
referenced trace/span; its target may be absent because of sampling, separate
exports or retention. Events retain their own producer attribute-drop count.

Non-exception events retain the scalar fields `retry.attempt`, `retry.count`,
`retry.delay_ms`, `error.type`, `event.name`, `event.domain` and
`messaging.operation.type`. Add other exact keys explicitly in YAML:

```yaml
attribute_retention:
  resource: [project.revision]
  scope: [library.mode]
  span: [order.id, custom.enabled]
  event: [retry.reason]
  link: [link.kind]
```

Lists accept at most 32 exact keys per location and require restart. Wildcards,
reserved `geko.*` keys and known credential/payload keys are refused. Opt-in
values pass the same best-effort text redaction; arbitrary sensitive data may
still have no recognizable pattern. Supported values are strings, signed
integers, booleans and finite doubles. Arrays/maps/bytes are omitted and counted,
not silently stringified. Extra strings are bounded to 1,024 UTF-8 bytes; built-in
fields retain their existing limits. Exact integer values survive storage and
OTLP export without conversion through float64.

Strict redaction ignores these extra-key lists and drops trace state. Protected
attribute-search keys remain HMAC-only, even when also opted into retention.
Schema URLs must be bounded HTTP(S) URLs without credentials, query or fragment;
invalid or privacy-filtered metadata is omitted and counted.

Trace and bundle downloads reapply the current privacy policy and configured
extra-key lists. Standard OTLP fields preserve allowed producer context. A small,
versioned set of reserved `geko.otel.*` scalar attributes carries Geko filtering
counts and legacy capture status across Geko export/restore. These are untrusted
transfer diagnostics, never invented producer dropped counts or an authenticity
claim. Other backends may ignore them. Restoring a file applies the receiving
instance's own policy and adds any new filtering to the reported counts.

See [SDK/Collector compatibility checks](otel-compatibility.md) for the tested
protocol matrix and how to reproduce it.
