<div align="center">
  <img src="web/assets/geko.svg" alt="Geko" width="72" height="72">
  <h1>Geko</h1>
  <p><strong>A local dashboard for investigating backend failures from OpenTelemetry traces.</strong></p>
  <p>
    <a href="#quick-start">Quick start</a> ·
    <a href="#geko-jaeger-and-zipkin">Compare tracing tools</a> ·
    <a href="#inspect-a-failure">Inspect a failure</a> ·
    <a href="#group-multiple-applications">Group applications</a> ·
    <a href="#configuration">Configuration</a> ·
    <a href="docs/architecture.md">Architecture</a>
  </p>
</div>

![Geko Failures dashboard showing errors from a reporting API and an online store](docs/images/failures.jpg)

*The screenshots use synthetic traces from a reporting API that runs as a single
service and an online store with separate order, inventory, and payment services.*

Geko runs as a Go process with a built-in dashboard and local SQLite storage.
Point an OpenTelemetry trace exporter at Geko to see the request outcome, the
spans that recorded error evidence, and the observed calls between services.

| Task | Where to start |
| --- | --- |
| Find an exception for a request | Filter **Failures** by application, service, HTTP status, time, or search text. |
| Inspect what happened inside a request | Open the evidence span, then use the service map and waterfall to follow the recorded calls. |
| Keep several projects in one dashboard | Select an application, map namespace-free services, or assign historical traces by time. |
| Check whether data is arriving | Open **Settings → Receiver** for the effective endpoint, last accepted export, and rejection counts. |

## Geko, Jaeger, and Zipkin

All three collect and display distributed traces. Geko starts the investigation
with a list of requests whose spans recorded error evidence.

| Tool | Investigation workflow | Deployment and storage |
| --- | --- | --- |
| **Geko** | Filter **Failures** by application, entry service, evidence service, or HTTP status. Open the matching evidence span and follow calls in the trace’s service map. | One loopback-only process receives OTLP/HTTP protobuf and persists spans to a local SQLite database with configurable retention and a main database size limit. |
| **Jaeger** | Search and inspect traces, visualize span timing, and explore service dependencies. [Features](https://www.jaegertracing.io/docs/2.23/features/) | Supports an all-in-one process and separate collector and query roles. Deployments can scale with external storage. [Architecture](https://www.jaegertracing.io/docs/2.23/architecture/) |
| **Zipkin** | Look up a trace by ID or search by service, operation, tags, and duration. Its dependency diagram aggregates calls across traces. [Zipkin overview](https://zipkin.io/) | Supports in-memory storage and persistent backends such as Cassandra and Elasticsearch. [Architecture](https://zipkin.io/pages/architecture.html) |

For example, a Storefront checkout can return HTTP 200 after retrying a failed
payment attempt. Geko shows the successful request outcome alongside the
payment span’s recorded exception. Select `Storefront` under **Application**
and enter `payments` under **Evidence service** to find the trace.

## Quick start

Use Go 1.26 or later. From the repository root, start Geko in a terminal.

```sh
git clone https://github.com/YunusEmreAlps/geko.git
cd geko
go run ./cmd/geko serve
```

Open [http://127.0.0.1:4318](http://127.0.0.1:4318). In a second terminal, run the
synthetic trace demo from the repository root. The demo sends data to Geko
directly and does not open another listener.

```powershell
$env:OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = 'http://127.0.0.1:4318/v1/traces'
go run ./examples/service-map
```

On bash or zsh, run the same demo with the endpoint set for that command.

```sh
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces go run ./examples/service-map
```

Refresh **Failures** and open **POST /checkout**. To generate traces from real
HTTP requests, try the [Go checkout example](examples/go/README.md). It includes
success, failure, and retry routes.

## Connect your application

Your application can keep serving traffic on port 8080 while Geko listens on
4318. In the process that starts your instrumented application, configure its
OpenTelemetry exporter to send traces to Geko.

```powershell
$env:OTEL_SERVICE_NAME = 'orders-api'
$env:OTEL_EXPORTER_OTLP_TRACES_PROTOCOL = 'http/protobuf'
$env:OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = 'http://127.0.0.1:4318/v1/traces'
```

For bash or zsh, use `export NAME=value`. If Geko uses a different port,
copy the **Trace endpoint** from **Settings → Receiver**. Geko accepts both
uncompressed and gzip-compressed OTLP/HTTP protobuf traces. Your application
needs OpenTelemetry instrumentation and an exporter configured to read these
settings or their equivalent in code.

Send a request to your application, then check **Settings → Receiver**. An
accepted export confirms that Geko received and stored a batch with spans.
**Failures** shows only traces with recorded error evidence. Its empty states
distinguish an empty database, stored traces without failure evidence, and a
filter with no results.

## Inspect a failure

1. Find an operation in **Failures**. **Request service** filters the entry
   service. **Evidence service** filters the service whose span recorded an error.
2. Open the operation to jump to the matching evidence span. **Observed evidence**
   lists spans with an exception event, HTTP 5xx, or an explicit error status.
3. Follow the **Service map** to inspect calls in this trace. Expand **Span
   waterfall** to compare start times and durations. **Span details** shows
   captured attributes, events, and stack traces when available.

In the store example, `POST /checkout` returns HTTP 503. The service map shows
calls to inventory and payments. Open `payment.authorize` to see the recorded
gateway timeout.

![Online store checkout trace with payment timeout evidence, a service map, and selected span details](docs/images/trace.jpg)

An HTTP 200 response can appear beside a child exception. Geko keeps the
request outcome and the recorded evidence separate. The service map shows
observed parent–child relationships for up to 500 spans and marks partial
results. It groups instances by application, namespace, service name, and
deployment environment. Select **Merge environments** to combine environments
for the same service. The span details list instance IDs observed for the
selected node. Evidence and waterfall pages can reach spans beyond the map limit.

## Group multiple applications

The reporting API and the online store can send traces to the same Geko
endpoint, even if both APIs use port 8080 in their own environments. Set
`service.namespace` to `Reporting` and `Storefront` respectively so each
application has its own filter, even when both use `service.name=api`.

| Scenario | How to group it |
| --- | --- |
| The store adds a `payments` service | Give it `service.name=payments` and `service.namespace=Storefront`. Select **Storefront** in **Failures** to see it alongside the store’s `api` and other services. |
| A legacy `report-importer` exports spans without a namespace | In **Failures → Manage applications**, map the exact service name `report-importer` to `Reporting`. The rule applies to past and future spans without a namespace. |
| Both APIs previously exported spans as `api` without a namespace | If you know that only one app’s traces arrived during a specific window, use **Assign historical traces** with that UTC range and narrow it to `api`. The range uses when Geko last received each trace. |

If traces from both APIs arrived in the same window under the same service
name, keep that window unassigned unless you can narrow it further. For future
exports, add `service.namespace=Reporting` or `service.namespace=Storefront`
to each application’s existing `OTEL_RESOURCE_ATTRIBUTES` and set
`OTEL_SERVICE_NAME` per service.

![Applications screen with a legacy service mapping and UTC fields for assigning historical traces](docs/images/applications.jpg)

An explicit namespace takes priority. For spans without one, a historical trace
assignment takes priority over a service mapping. Clearing the assignment
restores the service mapping or **Ungrouped**. A trace spanning two applications
appears in both filters, and its detail page preserves the full trace.
Historical updates commit in batches. Submit the same range, service, and
application to resume an interrupted update. **Discard progress** leaves the
changes from completed batches in place. If a trace receives a new span
before its batch runs, its last receipt time may move outside the selected
range and the trace may be skipped.

## Configuration

Copy [`geko.example.yaml`](geko.example.yaml) to `geko.yaml` in the working
directory or set `GEKO_CONFIG` to the path of an existing YAML file. Nonempty
environment variables take precedence over YAML. Restart Geko to apply changes.
**Settings** shows the loaded file and whether each value came from the
default, YAML, or an environment variable.

| YAML key | Environment override | Default |
| --- | --- | --- |
| `listen_addr` | `GEKO_LISTEN_ADDR` | `127.0.0.1:4318`. Use a loopback IP and port, such as `[::1]:4318`. |
| `db_path` | `GEKO_DB_PATH` | `geko/geko.db` under the OS user configuration directory. |
| `retention_days` | `GEKO_RETENTION_DAYS` | 7 days, configurable from 1 to 3650. |
| `max_db_mib` | `GEKO_MAX_DB_MIB` | 256 MiB for the main SQLite file, configurable from 8 to 65536. |
| `on_full` | `GEKO_ON_FULL` | `evict_oldest`. Set `reject` to keep stored traces and return HTTP 503 at capacity. |
| `redaction` | `GEKO_REDACTION` | `best_effort`. Set `strict` to store exception types and message hashes for new spans. |

Relative YAML database paths are resolved from the YAML file's directory.
Relative `GEKO_DB_PATH` values are resolved from the working directory.
Geko expires traces based on their last receipt time at startup and once a
minute. At capacity, `evict_oldest` can remove whole traces ordered by last
receipt to make room for a new export. Traces in the incoming export are
protected. If the export still cannot fit, Geko returns HTTP 503 and rolls
back the attempted eviction. Capacity rejections include `Retry-After: 30`.
If the configured limit is below the current file size, `evict_oldest`
reuses existing pages and evicts traces as needed without growing the file.
Settings shows the database’s cumulative counts
of evicted traces and exports rejected at capacity.

Deleted pages can be reused. SQLite’s WAL and shared-memory files can use
additional space. Stop the server and run `geko compact` to reclaim free space
from the main file. `geko doctor` shows the main file, WAL, SHM, and reusable
page sizes separately.

Geko uses **best-effort redaction** before storing or indexing telemetry. An
allowlist drops headers, cookies, request and response bodies, and raw URLs.
Geko redacts allowed text that matches recognizable credential or payload
patterns and limits exception messages to 1,024 UTF-8 bytes. Allowed text
can still contain sensitive data that does not match these patterns.
With `redaction: strict`, new spans store the exception type and SHA-256 of
the original message. Geko drops exception stack traces and status
descriptions in this mode. Evidence search matches the type, exact original
message, or full hexadecimal digest of the first exception event on each span.
Search hashes the original query text, including case and whitespace.
Search terms appear in the URL, so use the digest to keep the original
message out of browser history. Previously stored spans retain their
original fields. See the
[storage and privacy notes](docs/architecture.md).

## CLI and local API

```sh
go build -o bin/geko ./cmd/geko
./bin/geko doctor
./bin/geko serve
```

On Windows, build with `go build -o bin/geko.exe ./cmd/geko` and run
`.\bin\geko.exe`. Use `geko version` for build information. Run `geko doctor`
before `serve` to check SQLite integrity, storage size, and listener availability.
`GET /healthz` returns HTTP 204 once the listener is ready.

| Endpoint | Response |
| --- | --- |
| `GET /api/v1/failures` | A page of traces with observed failure evidence. |
| `GET /api/v1/traces/{trace_id}` | The trace summary and a page of spans after best-effort redaction or strict exception filtering. |

Both query endpoints accept `limit` from 1 to 100 (default 50). Pass the
returned `next_cursor` as `cursor` to continue. The failure endpoint accepts
these filters.

| Parameter | Match |
| --- | --- |
| `application` | `group:<name>` for traces containing that application's spans, or `ungrouped` for traces containing ungrouped spans. A combined service filter matches the selected application. |
| `service` | Exact entry span service, or primary evidence service when a unique entry span is unavailable. |
| `evidence_service` | Exact service on a span with evidence. |
| `status` | HTTP status on the entry span. |
| `q` | Up to 4,096 bytes. Trace ID prefix, substring in the entry or evidence operation, service, exception message or type, full exception hash, or exact original message in strict mode. Substring and hexadecimal comparisons ignore ASCII case. |
| `from`, `to` | RFC3339 last receipt time, inclusive start and exclusive end. |

## Development

```sh
go test ./...
go vet ./...
go test ./internal/storage/sqlite -run '^$' -bench BenchmarkApplicationFailureFilter
```

See [Architecture](docs/architecture.md) for ingestion, privacy filtering,
SQLite, and evidence selection. The [service map demo](examples/service-map/README.md)
and [Go checkout example](examples/go/README.md) describe the scenarios they send.
