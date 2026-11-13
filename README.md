<div align="center">
  <img src="web/assets/geko.svg" alt="Geko" width="72" height="72">
  <h1>Geko</h1>
  <p><strong>Find the failure. Follow the trace.</strong></p>
  <p>A local, failure-first dashboard for OpenTelemetry traces. One Go binary, SQLite storage, no Docker or external services required.</p>
  <p>
    <a href="https://github.com/YunusEmreAlps/geko/actions/workflows/ci.yml"><img src="https://github.com/YunusEmreAlps/geko/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
    <a href="https://github.com/YunusEmreAlps/geko/releases"><img src="https://img.shields.io/github/v/release/YunusEmreAlps/geko?include_prereleases&sort=semver" alt="Release"></a>
    <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/YunusEmreAlps/geko" alt="Go version"></a>
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT License"></a>
  </p>
  <p>
    <a href="#quick-start">Quick start</a> ·
    <a href="#connect-your-application">Connect your app</a> ·
    <a href="#investigate-a-failure">Investigate</a> ·
    <a href="#configuration">Configuration</a> ·
    <a href="#how-geko-compares">Compare</a> ·
    <a href="docs/architecture.md">Architecture</a>
  </p>
</div>

![Geko Failures dashboard showing errors from a reporting API and an online store](docs/images/failures.jpg)

*Screenshots use synthetic traces from a single-service reporting API and an
online store with separate order, inventory, and payment services.*

## Why Geko

- **Starts with what broke.** The first screen lists requests whose spans
  recorded an exception, an HTTP 5xx, or an error status, not every trace you sent.
- **Keeps outcome and evidence apart.** A checkout that returns HTTP 200 after a
  retried payment timeout still shows the payment exception next to the
  successful response.
- **Runs entirely on your machine.** A single process receives OTLP/HTTP and optional OTLP/gRPC,
  stores spans in SQLite with retention and a size limit, and serves the
  dashboard on a loopback address by default.
- **Is careful with data.** An attribute allowlist drops headers, cookies,
  bodies, and full URLs before anything is stored. A `strict` mode keeps only
  exception types and message hashes.

## Quick start

Requires Go 1.26.11 or later. Start Geko:

```sh
git clone https://github.com/YunusEmreAlps/geko.git
cd geko
go run ./cmd/geko serve
```

Open <http://127.0.0.1:4318>. In a second terminal, send a synthetic
multi-service trace from the repository root:

```sh
# bash / zsh
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces go run ./examples/service-map
```

```powershell
# PowerShell
$env:OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = 'http://127.0.0.1:4318/v1/traces'
go run ./examples/service-map
```

Refresh **Failures** and open **POST /checkout**. More examples:

| Example | What it sends |
| --- | --- |
| [Go checkout](examples/go/README.md) | Real HTTP requests through the OpenTelemetry SDK, with success, failure, and retry routes. |
| [OTLP/HTTP JSON](examples/otlp-json/README.md) | A synthetic HTTP 503 trace from Node.js, Python, Java, or .NET, using only the standard library. |
| [Service map](examples/service-map/README.md) | One synthetic trace across five services, for exploring the map and waterfall. |

## Install

| Method | Command |
| --- | --- |
| Release binary | Download the archive for your platform from [GitHub Releases](https://github.com/YunusEmreAlps/geko/releases), extract it, and run `./geko serve` (`.\geko.exe serve` on Windows). |
| Go | `go install github.com/YunusEmreAlps/geko/cmd/geko@latest`, then `geko serve`. Replace `latest` with a tag to pin a version. |
| Docker | `docker build -t geko .` then `docker run --rm --network host -v geko-data:/data geko` |

The host networking example uses Geko's default loopback listener. To use Docker
bridge networking and publish a port, configure an access token and an address
reachable inside the container. This example publishes the port on the Docker
host's loopback interface. [Docker port publishing](https://docs.docker.com/engine/network/port-publishing/)

```sh
export GEKO_ACCESS_TOKEN="$(openssl rand -hex 32)"
docker run --rm -p 127.0.0.1:4318:4318 -v geko-data:/data \
  -e GEKO_ACCESS_TOKEN -e GEKO_LISTEN_ADDR=0.0.0.0:4318 geko
```

Open http://127.0.0.1:4318 and sign in as `geko` with the token as the
password. For OTLP/HTTP and OTLP/gRPC clients, set
`OTEL_EXPORTER_OTLP_TRACES_HEADERS="authorization=Bearer%20${GEKO_ACCESS_TOKEN}"`.
API and health check clients can also send `Authorization: Bearer <token>`.
To receive gRPC through Docker, also set `GEKO_GRPC_ADDR=0.0.0.0:4317` and
publish `127.0.0.1:4317:4317`.

For access from another machine, terminate TLS at a reverse proxy and keep
Geko's published port on host loopback. Preserve the request Host and
Authorization headers. Configure HTTP/2 proxying for gRPC if you expose it.
Restart Geko after rotating the token. At startup, Geko prints a warning for
every listener that is not bound to a loopback address.

### Upgrade an existing installation

Stop Geko and back up the SQLite database, including any remaining
`-wal` file beside it. The database path is shown in **Settings** and
`geko doctor`. Start the new binary with `geko serve` and the same
configuration. Geko updates the database schema at startup and derives
trace summaries and service membership from the existing spans. Startup
can take longer for larger databases, and the file can grow by roughly 15%.
If the database then exceeds `max_db_mib`, Geko prints a warning at startup;
with `on_full: reject`, raise the limit or exports are rejected. To return to an older binary after
a schema upgrade, restore the pre-upgrade backup before starting it.

## Connect your application

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
4318, set a different [`listen_addr`](#configuration) and copy the new trace
endpoint from **Settings → Receiver**.

## Investigate a failure

1. **Find it.** Filter **Failures** by application, request (entry) service,
   evidence service, HTTP status, time range, or search text. **Overview**
   summarizes the last 24 hours. To open any stored trace, including a
   successful one, paste its ID into **Go to trace ID** in the sidebar (press `/`). Open **All traces** to
   browse successful and failing requests together with the same filters.
2. **Open the evidence.** Selecting an operation jumps to the evidence span.
   **Evidence** lists every span with an exception event, an HTTP 5xx,
   or an explicit error status.
3. **Follow the calls.** The **Service map** shows the parent–child calls
   observed in this trace, and the **Span waterfall** compares start times and
   durations. **Span details** shows the stored attributes, events, and stack
   traces.

In the store example, `POST /checkout` returns HTTP 503. The map shows calls to
inventory and payments; open `payment.authorize` to see the gateway timeout.

![Online store checkout trace with payment timeout evidence, a service map, and selected span details](docs/images/trace.jpg)

The map groups instances by application, namespace, service name, and
deployment environment. **Merge environments** combines environments of the
same service. It reads up to 500 spans and marks partial results; the evidence
list and waterfall page through the whole trace.

## Group multiple applications

Several applications can share one Geko, even when each one names its service
`api`. Set `service.namespace` per application, for example through
`OTEL_RESOURCE_ATTRIBUTES=service.namespace=Storefront`, and each namespace
becomes an **Application** filter.

For spans exported without a namespace, open **Applications** in the sidebar:

| Situation | What to do |
| --- | --- |
| A legacy `report-importer` service never sets a namespace | Map the exact service name to `Reporting`. The rule applies to past and future spans without a namespace. |
| Two apps both exported `api` without a namespace, and you know which one was active during a time window | Use **Assign historical traces** with that UTC range, narrowed to `api`. The range matches when Geko last received each trace. |

![Applications screen with a legacy service mapping and UTC fields for assigning historical traces](docs/images/applications.jpg)

<details>
<summary>Precedence and edge cases</summary>

- An explicit `service.namespace` always wins. Without one, a historical trace
  assignment wins over a service mapping. Clearing the assignment restores the
  mapping, or **Ungrouped**.
- A trace that spans two applications appears under both filters. Its detail
  page always shows the full trace.
- If both apps sent traces under the same service name in the same window,
  leave that window unassigned unless you can narrow it further.
- Historical assignments commit in batches. To resume an interrupted update,
  submit the same range, service, and application. **Discard progress** keeps
  the batches that already completed.
- If a trace receives a new span before its batch runs, its last receipt time
  can move outside the range, and the trace is skipped.

</details>

For a trace with failure evidence and a unique entry span, select **Compare
with success**. Geko picks the most recently received trace with an HTTP 2xx
entry span and no failure evidence for the same application, namespace,
service, operation and environment. The comparison shows both request
durations, the difference, span counts and a link to each trace.

Open **Regressions** in the sidebar to compare two recorded `service.version`
values in a time range. Geko matches entry spans by application, namespace,
service, operation and environment. The table flags increases of at least
5 percentage points in failure evidence rate or 20% in mean request duration,
with at least 10 relevant samples in each version. These thresholds highlight
changes in the observed traces.

When a selected span has a recorded exception stack trace, expand **Open
source in VS Code** to open a recognized file and line. Links use absolute
paths recorded by the service, so those paths need to resolve on the machine
running VS Code. Relative frames and unmatched formats remain visible as
stack trace text.

## Read traces from coding tools

Query the configured local database with `geko failures --json`:

```sh
geko failures --json --service orders --status 503 --limit 10
```

The response contains `items` and, when another page exists, `next_cursor`.
Pass that value as `--cursor` with the same filters. Run
`geko failures --help` for all filters.

To connect an MCP client, set its stdio command to the absolute path to
`geko` and its argument list to `["mcp"]`. Set `GEKO_DB_PATH` or
`GEKO_CONFIG` in the client's server environment when using a custom
database. Use `list_failures` for recent failure evidence, `get_trace` for
paginated span timings and relationships, and `list_failure_groups` for
recurring failures with trace counts, receipt times, and request HTTP outcomes.
Ask your MCP client to summarize the groups and cite each `evidence_path`
relative to your Geko dashboard. The client generates the AI summary using
its own model. Geko computes the group statistics locally.

Both commands open an existing SQLite database in read-only mode. MCP tool
descriptions mark telemetry text as untrusted data, because exception messages
and operation names come from the instrumented services. A malformed request
line receives a JSON-RPC error and the session continues. They work
alongside a running Geko and can also read its database after shutdown.
Geko's stored evidence can appear in the MCP client's context. If the
database schema is older, run the matching `geko serve` to migrate it first.

## Transfer a trace

Open a trace and select **Download OTLP JSON**. To load it into another Geko
instance, choose **Restore trace from OTLP JSON** at the bottom of **All traces**. Each file
contains one trace and is limited to 4 MiB and 2,048 spans. A trace over the
limit cannot be downloaded through this feature.

Download and restore both apply the instance's current redaction policy.
The file carries span timing, IDs, relationships, and retained telemetry.
Geko assigns a new receipt time on restore. Application assignments and
retention settings belong to the destination database. A trace that is already
stored is not restored again (HTTP 409), so a file can never add spans to an
existing trace; open the stored trace instead.

## Error groups and live updates

In **Failures**, choose **Error groups** to see recurring failures with trace
counts and first/last receipt times. Click a count for an observed failure
summary and matching traces, or **Latest example** to inspect the evidence
span immediately. The summary separates request HTTP outcomes from the
evidence span's status and links to the latest matching span. Application,
service, search and time filters apply to the summary and trace list.

Groups use each trace's primary evidence. Application, namespace, service,
environment, operation, error classification, exception type and span HTTP status
identify a group. Different instance IDs or messages do not split it; exceptions
without a type form broader groups. Counts cover retained traces, not every
exception event.

The first page updates automatically through SSE. **Pause**, next to the live indicator, keeps
the current results in place. Focused links and selected text are preserved;
use **Refresh results** when prompted. Older pages stay stable, and manual
refresh works without JavaScript. Changes made by another process directly to
the database require manual refresh. Only Geko's own pages can open the live
stream; browsers that send a cross-site `Sec-Fetch-Site` header are refused.

## Configuration

Copy [`geko.example.yaml`](geko.example.yaml) to `geko.yaml` in the working
directory, or set `GEKO_CONFIG` to a YAML file. Nonempty environment variables
override YAML. Restart Geko to apply changes. **Settings** shows each value's
source: default, YAML, or environment.

For the optional listeners, an explicitly empty `GEKO_GRPC_ADDR` disables gRPC,
and an explicitly empty `GEKO_DASHBOARD_ADDR` restores the shared HTTP listener,
even if YAML configures a separate address.

| YAML key | Environment variable | Default |
| --- | --- | --- |
| `listen_addr` | `GEKO_LISTEN_ADDR` | `127.0.0.1:4318`. Non-loopback listeners require an access token. |
| — | `GEKO_ACCESS_TOKEN` | Empty. A token of 32–256 ASCII letters, digits, hyphens or underscores, with at least 10 different characters, enables authentication on all listeners. Generate one with `openssl rand -hex 32`. |
| `db_path` | `GEKO_DB_PATH` | `geko/geko.db` in the OS user configuration directory |
| `retention_days` | `GEKO_RETENTION_DAYS` | `7` (1–3650) |
| `dashboard_addr` | `GEKO_DASHBOARD_ADDR` | Empty: share the HTTP listener |
| `grpc_addr` | `GEKO_GRPC_ADDR` | Empty: disabled; set `127.0.0.1:4317` to enable |
| `max_db_mib` | `GEKO_MAX_DB_MIB` | `256` MiB for the main SQLite file (8–65536) |
| `on_full` | `GEKO_ON_FULL` | `evict_oldest`, or `reject` to keep stored traces and return HTTP 503 |
| `redaction` | `GEKO_REDACTION` | `best_effort`, or `strict` |

A relative `db_path` in YAML resolves from the YAML file's directory. A
relative `GEKO_DB_PATH` resolves from the working directory.

### Retention and capacity

- Geko expires whole traces by last receipt time, at startup and every minute.
- At the size limit, `evict_oldest` removes the oldest whole traces in the same
  transaction as the new export. Traces in the incoming export are protected.
  If the export still doesn't fit, nothing is evicted and Geko returns HTTP 503
  with `Retry-After: 30`. `reject` returns that response right away.
- If you lower the limit below the current file size, `evict_oldest` reuses
  existing pages and evicts traces as needed without growing the file.
- The WAL and shared-memory files use space beyond the limit. Stop the server
  and run `geko compact` to shrink the main file. `geko doctor` reports each
  file's size and the reusable pages.

### Privacy

Telemetry is filtered before it is stored or indexed.

- **`best_effort` (default):** an allowlist keeps service, scope, HTTP method,
  route, `url.path`, and `db.*`/`rpc.*` operation attributes. Headers, cookies,
  request and response bodies, full URLs, and custom attributes are dropped.
  Text that matches recognizable credential or payload patterns is redacted.
  Exception messages are limited to 1,024 bytes and stack traces to 32 KiB.
  Text that doesn't match a pattern can still contain sensitive data, so keep
  secrets out of your telemetry.
- **`strict`:** new spans store only the exception type and a SHA-256 hash of
  the message. Stack traces, status descriptions, and the extra `url`, `db`,
  and `rpc` attributes are dropped. Search matches the type, the exact original
  message, or the full hex digest. Search terms appear in the URL, so search by
  digest to keep a message out of browser history.

A mode change applies only to new spans. See
[Architecture](docs/architecture.md) for details.

## CLI and local API

| Command | Purpose |
| --- | --- |
| `geko serve` | Start the receiver and dashboard. `GET /healthz` returns 204 once it is listening. |
| `geko doctor` | Check SQLite integrity, storage sizes, and that every configured port is free. |
| `geko failures --json` | Print a page of failures as JSON for scripts and coding agents. See [Read traces from coding tools](#read-traces-from-coding-tools). |
| `geko mcp` | Serve read-only investigation tools to an MCP client over stdio. |
| `geko compact` | Reclaim free space from the main database file (stop the server first). |
| `geko version` | Print build information. |

The dashboard's data is also available as JSON on the dashboard listener, which
is the shared HTTP listener unless `dashboard_addr` is set:

| Endpoint | Returns |
| --- | --- |
| `GET /api/v1/failures` | A page of traces with failure evidence |
| `GET /api/v1/traces/{trace_id}` | A trace summary and a page of its spans |

Both endpoints accept `limit` (1–100, default 50) and `cursor`. Pass the
`next_cursor` from a response as `cursor` to continue. `/api/v1/failures`
accepts these filters:

| Parameter | Matches |
| --- | --- |
| `application` | `group:<name>` for traces containing that application's spans, or `ungrouped` |
| `service` | The entry span's service, or the primary evidence service when there is no single entry span |
| `evidence_service` | The service of any span with evidence |
| `status` | The entry span's HTTP status |
| `q` | Up to 4,096 bytes: a trace ID prefix, or a case-insensitive substring of the operation, service, or exception message or type. Also a full exception hash, or the exact original message in strict mode. |
| `from`, `to` | Last receipt time, in RFC 3339. `from` is inclusive and `to` is exclusive. |

## How Geko compares

| Tool | Best for | Deployment |
| --- | --- | --- |
| **Geko** | Starting from failure evidence on a developer machine: filter failures, open the evidence span, follow the trace's calls. | Single binary, loopback by default, SQLite with retention and a size limit. Traces only. |
| [Jaeger](https://www.jaegertracing.io/docs/2.23/features/) | Searching and inspecting traces and service dependencies at any scale. | All-in-one or separate collector and query roles with external storage. |
| [Zipkin](https://zipkin.io/) | Looking up traces by ID, service, operation, tags, and duration, with an aggregate dependency diagram. | In-memory or persistent backends such as Cassandra and Elasticsearch. |
| [OpenTelemetry Desktop Viewer](https://github.com/CtrlSpice/otel-desktop-viewer) | A local telemetry UI with SQL access. | Local CLI with a browser UI. |
| [otel-tui](https://github.com/ymtdzzz/otel-tui) | Browsing telemetry in a terminal UI. | Local terminal tool. |
| [Aspire Dashboard](https://aspire.dev/dashboard/standalone/) | Logs, traces, and metrics in one local dashboard. | Standalone container. |

## Development

```sh
go test ./...
go vet ./...
go test ./internal/ingest/otlp -run '^$' -fuzz '^FuzzReceiver$' -fuzztime 30s
go test ./internal/storage/sqlite -run '^$' -bench .
```

CI also runs staticcheck, govulncheck, race tests, `go mod tidy -diff`, and a
Docker smoke test with the JSON exporters. CI also builds a release snapshot,
verifies the archive checksums, and runs the Linux amd64 binary. Run
`goreleaser release --snapshot --clean` locally to generate the same archives
in `dist/`. Pushing a `v*` tag publishes checksummed archives for Linux, macOS,
and Windows on amd64 and arm64.

See [Architecture](docs/architecture.md) for ingestion, storage, privacy
filtering, and evidence selection.

## License

[MIT](LICENSE)
