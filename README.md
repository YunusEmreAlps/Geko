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
reachable inside the container. A non-loopback address also requires
`GEKO_ALLOW_PLAINTEXT=true`, which confirms that only a trusted network or a TLS
proxy can reach it. This example publishes the port on the Docker host's loopback
interface. [Docker port publishing](https://docs.docker.com/engine/network/port-publishing/)

```sh
export GEKO_ACCESS_TOKEN="$(openssl rand -hex 32)"
docker run --rm -p 127.0.0.1:4318:4318 -v geko-data:/data \
  -e GEKO_ACCESS_TOKEN -e GEKO_LISTEN_ADDR=0.0.0.0:4318 -e GEKO_ALLOW_PLAINTEXT=true geko
```

Open http://127.0.0.1:4318 and sign in with the token. The browser stays signed
in for 7 days, or until Geko restarts; **Sign out** in the sidebar ends the
session. Scripts can send the token as a Bearer token, or as the Basic password
for user `geko`. For OTLP/HTTP and OTLP/gRPC clients, set
`OTEL_EXPORTER_OTLP_TRACES_HEADERS="authorization=Bearer%20${GEKO_ACCESS_TOKEN}"`.
API and health check clients can also send `Authorization: Bearer <token>`.
To receive gRPC through Docker, also set `GEKO_GRPC_ADDR=0.0.0.0:4317` and
publish `127.0.0.1:4317:4317`.

For access from another machine, see [Run on a server](#run-on-a-server).

### Run on a server

One Geko instance can receive traces from several of your applications, for
example side projects or dev and staging environments. It is not a production
monitoring system: it stores traces only, keeps them in one SQLite file, and
does not send alerts. Production tracing is usually sampled, so a failure in a
dropped trace never reaches Geko.

[`deploy/`](deploy) contains a Docker Compose setup with Caddy, which obtains a
TLS certificate and serves OTLP/HTTP, OTLP/gRPC and the dashboard on port 443:

```sh
cd deploy
cp .env.example .env   # set GEKO_DOMAIN, GEKO_ACCESS_TOKEN and GEKO_INGEST_TOKENS
docker compose up -d --build
```

- **Access token** (`GEKO_ACCESS_TOKEN`): signs in to the dashboard with full
  access. Keep it out of application configuration.
- **Ingest tokens** (`GEKO_INGEST_TOKENS`): one per application, as
  `name=token` pairs. They can only export traces, so a leaked token can't read
  stored data, and you can revoke one application without changing the others.
  **Settings** lists the names, never the values.
- **Read tokens** (`GEKO_READ_TOKENS`): for coding agents, scripts and teammates
  who only look. Teammates sign in to the dashboard with one. They open the dashboard, the query API and `/mcp`, but can't
  change mappings, triage groups or restore traces.
- **Connect** in the sidebar generates the environment variables for a service
  in Bash, PowerShell or Docker Compose. Point each application at the domain
  with its ingest token:

  ```sh
  OTEL_EXPORTER_OTLP_ENDPOINT=https://geko.example.com
  OTEL_EXPORTER_OTLP_HEADERS="authorization=Bearer%20<ingest token>"
  OTEL_RESOURCE_ATTRIBUTES=service.namespace=Storefront
  ```

  The same endpoint works for `http/protobuf` and `grpc`.
- `GEKO_PUBLIC_URL` makes the dashboard show the public trace endpoint instead
  of the container address.
- `GET /healthz` and `GET /readyz` need no token. `/readyz` returns 503 when the
  database doesn't answer. `GET /metrics` serves receiver counters and storage
  sizes in Prometheus format to a read token.
- Rejected credentials are logged with the client address: the first at once,
  then a count per address each minute. Geko doesn't lock addresses out, because
  behind a proxy every client shares the proxy's address; tokens of 32 or more
  random characters can't be guessed online.
- Back up while Geko runs: `docker compose exec geko /geko backup /data/backup.db`,
  then copy the file out with `docker compose cp`.
- Set `GEKO_WEBHOOK_URL` to get a message when an error group first appears or
  a resolved group recurs. See [Notifications](#notifications).
- Coding agents reach the server through `/mcp` or `geko failures --server`.
  See [Read traces from coding tools](#read-traces-from-coding-tools).

Without Docker, [`deploy/geko.service`](deploy/geko.service) runs Geko under
systemd on loopback, behind your own reverse proxy. For any proxy, preserve the
Host and Authorization headers, allow request bodies of at least 5 MB (nginx
defaults to 1 MB), proxy gRPC over HTTP/2, and don't buffer `text/event-stream`
responses.

Tokens set in environment variables take effect at restart. To add or rotate
tokens without a restart, keep them in a file and set `GEKO_TOKENS_FILE` to its
path instead of the three token variables:

```yaml
access: <token>
ingest:
  storefront: <token>
  reporting: <token>
read:
  claude: <token>
```

Geko checks the file every 5 seconds. A valid change applies to every listener
at once and signs out every browser, so a rotated token also ends sessions that
started with the old one. An invalid file is logged and the current tokens stay
in use. Keep the file readable only by Geko's user.

### Upgrade an existing installation

Back up the SQLite database with `geko backup <new-file>`, which works while
Geko runs, or stop Geko and copy the file with any remaining `-wal` file beside
it. The database path is shown in **Settings** and `geko doctor`. Start the new binary with `geko serve` and the same
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
recurring failures with trace counts, receipt times, request HTTP outcomes and
triage state. Its `state` argument is `open` (the default, including reopened
groups), `resolved`, `muted` or `all`.
Ask your MCP client to summarize the groups and cite each `evidence_path`
relative to your Geko dashboard. The client generates the AI summary using
its own model. Geko computes the group statistics locally.

`compare_trace` returns a failing trace beside the latest successful request to
the same entry operation, service, application and environment, with the
duration difference. `compare_versions` compares failure rates and mean
durations per operation between two `service.version` values.

On a server, Geko serves the same read-only tools over Streamable HTTP at
`/mcp` on the dashboard listener. Give the agent a read token:

```sh
claude mcp add --transport http geko https://geko.example.com/mcp \
  --header "Authorization: Bearer <read token>"
```

`geko failures --json --server https://geko.example.com` queries a server's
API instead of a local file, with the token from `GEKO_TOKEN`. Without a token,
`/mcp` only answers requests to a loopback Host, like the rest of the dashboard.

With `GEKO_MCP_TRIAGE=true`, `/mcp` also offers `set_group_state` to clients
using the access token, or to any local client when no token is configured. An
agent can then resolve the group it fixed, with the version that contains the
fix. Read tokens and `geko mcp` over stdio never get it. Telemetry text can
contain instructions planted by whoever controls a service's input, so enable
this only for agents you supervise.

### Check a test run

`geko run` runs a command with exporter variables pointing at Geko, then lists
the failures received while it ran:

```sh
geko run -- go test ./...
geko run -- npm test
```

It sets `OTEL_EXPORTER_OTLP_ENDPOINT`, the protocol, a 500 ms batch delay, and
turns off metric and log exporters, unless the environment already sets them.
If no Geko is listening on `listen_addr`, it starts one for the duration of the
command. The summary goes to stderr, with links and the time window to pass to
`list_failures`, and Geko exits with the command's exit code. Traces are matched
by receipt time, not by test: the list can include failures from other clients
of the same Geko, and misses spans exported more than two seconds after the
command exits. Only processes that inherit the environment, such as the test
binary and servers it starts, send traces.

Locally, both commands open an existing SQLite database in read-only mode. MCP tool
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

Triage a group from its summary: **Resolve** hides it until a matching trace is
received again, when it returns to the open list marked **Reopened**. **Mute**
hides it for good and stops its notifications. **Open**, **Resolved** and **Muted**
next to the view switcher list each state. Groups first received since your
last visit to the open list are marked **New** in that browser. Triage states
expire with trace retention. Because the application is part of a group's
identity, changing a service mapping starts new groups.

Enter the release that contains the fix in **Fixed in version** when you resolve
a group. Geko then reopens it only for traces whose evidence service reports
that `service.version`, so failures from instances still running the old version
during a rollout don't reopen it. The group list shows **Resolved in 1.4.2**
while no matching trace from that version has arrived.

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

### Notifications

Set `GEKO_WEBHOOK_URL` to send a message when an error group is first received,
or when a resolved group recurs. Muted groups never notify. Geko checks every
30 seconds and sends at most five messages per check, then one summary.

`GEKO_WEBHOOK_FORMAT` is `slack`, `discord`, `ntfy` or `json`. Slack, Discord and
ntfy.sh URLs are recognized automatically; any other URL receives JSON with an
`event` (`new_group`, `reopened_group`, `summary` or `test`), a `text` summary,
the group's fields and a dashboard `url`. Links use `public_url`, or the
dashboard address when it isn't set. Messages contain the exception type,
operation, service and counts, but never exception messages, which can contain
secrets. **Settings** shows the webhook's host and has **Send test
notification**. This is not alerting: there are no thresholds, schedules or
escalation, and a failure in a trace that never reaches Geko sends nothing.

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
| `listen_addr` | `GEKO_LISTEN_ADDR` | `127.0.0.1:4318`. Non-loopback listeners require an access token and `GEKO_ALLOW_PLAINTEXT=true`. |
| — | `GEKO_ACCESS_TOKEN` | Empty. A token of 32–256 ASCII letters, digits, hyphens or underscores, with at least 10 different characters, enables authentication on all listeners. Generate one with `openssl rand -hex 32`. |
| — | `GEKO_INGEST_TOKENS` | Empty. Export-only tokens as comma-separated `name=token` pairs, with the same token rules. Requires `GEKO_ACCESS_TOKEN`. |
| — | `GEKO_ALLOW_PLAINTEXT` | `false`. Geko doesn't terminate TLS; set `true` to allow non-loopback listeners behind a TLS proxy or on a trusted network. |
| `public_url` | `GEKO_PUBLIC_URL` | Empty. External URL of the OTLP/HTTP listener, such as `https://geko.example.com`, shown as the trace endpoint and used in notification links. |
| — | `GEKO_READ_TOKENS` | Empty. Read-only tokens for the dashboard, query API and `/mcp`, as comma-separated `name=token` pairs. Requires `GEKO_ACCESS_TOKEN`. |
| — | `GEKO_TOKENS_FILE` | Empty. YAML file with `access`, `ingest` and `read` tokens, reloaded when it changes. Replaces the three token variables. |
| — | `GEKO_WEBHOOK_URL` | Empty. Webhook for new and reopened error groups. |
| — | `GEKO_WEBHOOK_FORMAT` | Inferred from the URL: `slack`, `discord`, `ntfy`, otherwise `json`. |
| `db_path` | `GEKO_DB_PATH` | `geko/geko.db` in the OS user configuration directory |
| `retention_days` | `GEKO_RETENTION_DAYS` | `7` (1–3650) |
| `dashboard_addr` | `GEKO_DASHBOARD_ADDR` | Empty: share the HTTP listener |
| `grpc_addr` | `GEKO_GRPC_ADDR` | Empty: disabled; set `127.0.0.1:4317` to enable |
| `max_db_mib` | `GEKO_MAX_DB_MIB` | `256` MiB for the main SQLite file (8–65536) |
| `ingest_concurrency` | `GEKO_INGEST_CONCURRENCY` | `4` exports decoded at once across OTLP/HTTP and gRPC (1–64). Each can hold a 4 MiB payload and its spans in memory. Raise it when slow clients over a WAN get `Receiver busy` rejections. |
| — | `GEKO_MCP_TRIAGE` | `false`. Offer `set_group_state` on `/mcp` to clients with full access. |
| `on_full` | `GEKO_ON_FULL` | `evict_oldest`; `evict_fair` to evict from the application holding the most spans first; or `reject` to keep stored traces and return HTTP 503 |
| `redaction` | `GEKO_REDACTION` | `best_effort`, or `strict` |

A relative `db_path` in YAML resolves from the YAML file's directory. A
relative `GEKO_DB_PATH` resolves from the working directory.

### Retention and capacity

- Geko expires whole traces by last receipt time, at startup and every minute.
- At the size limit, `evict_oldest` removes the oldest whole traces in the same
  transaction as the new export. Traces in the incoming export are protected.
  If the export still doesn't fit, nothing is evicted and Geko returns HTTP 503
  with `Retry-After: 30`. `reject` returns that response right away.
- `evict_fair` suits one Geko shared by several applications: it evicts the
  oldest traces of the application with the most stored spans, or of the
  service for spans without `service.namespace`, so one busy application can't
  push out the others' history. A trace spanning applications is removed whole.
  When that application's only traces are in the incoming export, it evicts the
  oldest traces of any application instead.
- If you lower the limit below the current file size, eviction reuses
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
| `geko serve` | Start the receiver and dashboard. `GET /healthz` returns 204 once it is listening, and `GET /readyz` once the database answers. Neither needs a token. |
| `geko doctor` | Check SQLite integrity, storage sizes, and that every configured port is free. |
| `geko failures --json` | Print a page of failures as JSON for scripts and coding agents, from the local database or a server with `--server`. See [Read traces from coding tools](#read-traces-from-coding-tools). |
| `geko mcp` | Serve read-only investigation tools to an MCP client over stdio. `geko serve` also serves them at `/mcp`. |
| `geko compact` | Reclaim free space from the main database file (stop the server first). |
| `geko backup <file>` | Write a consistent copy of the database to a new file, while the server runs. |
| `geko run -- <command>` | Run a command with exporters pointing at Geko, then list the failures received meanwhile. See [Check a test run](#check-a-test-run). |
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
| **Geko** | Starting from failure evidence on a developer machine or a shared dev/staging instance: filter failures, open the evidence span, follow the trace's calls. | Single binary, loopback by default, SQLite with retention and a size limit. Traces only. |
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
