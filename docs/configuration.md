# Configuration and reference

Settings, storage limits, privacy modes, upgrades, the CLI and the JSON API. [Back to the README](../README.md).

Copy [`geko.example.yaml`](../geko.example.yaml) to `geko.yaml` in the working
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
| `trace_link_url` | `GEKO_TRACE_LINK_URL` | Empty. Link that opens a trace in another tool, with `{trace_id}` in place of the trace ID, such as a Grafana Explore URL for Tempo or `https://jaeger.example.com/trace/{trace_id}`. Adds an **Open in** button to trace pages. |
| `trace_link_name` | `GEKO_TRACE_LINK_NAME` | `Tempo`. Tool name shown on that button, up to 40 bytes. |
| — | `GEKO_READ_TOKENS` | Empty. Read-only tokens for the dashboard, query API and `/mcp`, as comma-separated `name=token` pairs. Requires `GEKO_ACCESS_TOKEN`. |
| — | `GEKO_TRIAGE_TOKENS` | Empty. Tokens with read access that can also resolve, mute and reopen error groups and edit their notes, as comma-separated `name=token` pairs. Read and triage token names must differ. Requires `GEKO_ACCESS_TOKEN`. |
| — | `GEKO_TOKENS_FILE` | Empty. YAML file with `access`, `ingest`, `read` and `triage` tokens, reloaded when it changes. Replaces the four token variables. |
| — | `GEKO_PROXY_USER_HEADER` | Empty. Header in which an authenticating proxy, such as oauth2-proxy, names the signed-in user. Requires `GEKO_ACCESS_TOKEN` and `GEKO_PROXY_TRUSTED_IPS`. See [Sign in through a proxy](server.md#sign-in-through-a-proxy). |
| — | `GEKO_PROXY_TRUSTED_IPS` | Empty. Comma-separated IP addresses or CIDR prefixes of that proxy. The header is ignored on requests from other addresses. |
| — | `GEKO_PROXY_ROLES` | Empty. Proxy users' roles as comma-separated `user=role` pairs, where the role is `viewer`, `triager` or `admin`. Names compare without case. |
| — | `GEKO_PROXY_DEFAULT_ROLE` | `viewer`. Role of proxy users not in `GEKO_PROXY_ROLES`; `none` refuses them. |
| — | `GEKO_AUDIT_LOG` | Empty. File that receives one JSON line per sign-in and change, or `-` for standard error. See [Audit log](server.md#audit-log). |
| — | `GEKO_WEBHOOK_URL` | Empty. Webhook for new and reopened error groups. |
| — | `GEKO_WEBHOOK_FORMAT` | Inferred from the URL: `slack`, `discord`, `ntfy`, otherwise `json`. |
| `db_path` | `GEKO_DB_PATH` | `geko/geko.db` in the OS user configuration directory |
| `retention_days` | `GEKO_RETENTION_DAYS` | `7` (1–3650) |
| `dashboard_addr` | `GEKO_DASHBOARD_ADDR` | Empty: share the HTTP listener |
| `grpc_addr` | `GEKO_GRPC_ADDR` | Empty: disabled; set `127.0.0.1:4317` to enable |
| `max_db_mib` | `GEKO_MAX_DB_MIB` | `256` MiB for the main SQLite file (8–65536) |
| `ingest_concurrency` | `GEKO_INGEST_CONCURRENCY` | `4` exports decoded at once across OTLP/HTTP and gRPC (1–64). Each can hold a 4 MiB payload and its spans in memory. Raise it when slow clients over a WAN get `Receiver busy` rejections. |
| — | `GEKO_MCP_TRIAGE` | `false`. Offer `set_group_state` on `/mcp` to clients with the triager or admin role. |
| `on_full` | `GEKO_ON_FULL` | `evict_oldest`; `evict_fair` to evict from the application holding the most spans first; or `reject` to keep stored traces and return HTTP 503 |
| `redaction` | `GEKO_REDACTION` | `best_effort`, or `strict`. The default is `strict` once `GEKO_ACCESS_TOKEN` is set, because other people can then read the data. |

A relative `db_path` in YAML resolves from the YAML file's directory. A
relative `GEKO_DB_PATH` resolves from the working directory.

## Retention and capacity

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

## Privacy

Telemetry is filtered before it is stored or indexed.

- **`best_effort` (default without an access token):** an allowlist keeps
  service, scope, HTTP method, route, `url.path`, and `db.*`/`rpc.*` operation
  attributes. Headers, cookies, request and response bodies, full URLs, and
  custom attributes are dropped. A value that matches a recognizable credential
  or payload pattern is replaced by `[redacted]`. E-mail addresses, payment card
  numbers (Luhn check), IBANs (mod-97 check) and Turkish identity numbers
  (TCKN check digits) are masked in place as `[email]`, `[card]`, `[iban]` and
  `[tckn]`, so the rest of a message stays readable. Card masking covers 13–19
  digit numbers that start with 2 to 6; about one in ten unrelated numbers of
  that shape also passes the check and is masked. Searching for a masked value
  doesn't find it.
  Exception messages are limited to 1,024 bytes and stack traces to 32 KiB.
  Text that doesn't match a pattern can still contain sensitive data, so keep
  secrets out of your telemetry.
- **`strict` (default with an access token):** new spans store only the
  exception type and a SHA-256 hash of the message. Stack traces, status descriptions, and the extra `url`, `db`,
  and `rpc` attributes are dropped. Search matches the type, the exact original
  message, or the full hex digest. Search terms appear in the URL, so search by
  digest to keep a message out of browser history.

A mode change applies only to new spans. See
[Architecture](architecture.md) for details.

## CLI and local API

| Command | Purpose |
| --- | --- |
| `geko serve` | Start the receiver and dashboard. `GET /healthz` returns 204 once it is listening, and `GET /readyz` once the database answers. Neither needs a token. |
| `geko doctor` | Check SQLite integrity, storage sizes, and that every configured port is free. |
| `geko failures --json` | Print a page of failures as JSON for scripts and coding agents, from the local database or a server with `--server`. See [Read traces from coding tools](agents.md). |
| `geko mcp` | Serve read-only investigation tools to an MCP client over stdio. `geko serve` also serves them at `/mcp`. |
| `geko compact` | Reclaim free space from the main database file (stop the server first). |
| `geko backup <file>` | Write a consistent copy of the database to a new file, while the server runs. |
| `geko run -- <command>` | Run a command with exporters pointing at Geko, then list the failures received meanwhile. See [Check a test run](agents.md#check-a-test-run). |
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

## Upgrade an existing installation

Back up the SQLite database with `geko backup <new-file>`, which works while
Geko runs, or stop Geko and copy the file with any remaining `-wal` file beside
it. The database path is shown in **Settings** and `geko doctor`. Start the new binary with `geko serve` and the same
configuration. Geko updates the database schema at startup and derives
trace summaries and service membership from the existing spans. Startup
can take longer for larger databases, and the file can grow by roughly 15%.
If the database then exceeds `max_db_mib`, Geko prints a warning at startup;
with `on_full: reject`, raise the limit or exports are rejected. To return to an older binary after
a schema upgrade, restore the pre-upgrade backup before starting it.
