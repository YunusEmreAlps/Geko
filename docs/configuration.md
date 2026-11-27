# Configuration and reference

Settings, storage limits, privacy modes, upgrades, the CLI and the JSON API. [Back to the README](../README.md).

Copy [`geko.example.yaml`](../geko.example.yaml) to `geko.yaml` in the working
directory, or set `GEKO_CONFIG` to a YAML file. Nonempty environment variables
override YAML. Most changes require a restart. **Settings** shows each value's
source: default, YAML, or environment.

## Live settings

In source builds after v0.2.0, administrators can change **Trace link URL** and
**Link name** under **Settings → Live settings**. **Save and apply** writes the
loaded YAML file and updates links on subsequent trace-page requests without
restarting. Clear both fields to disable the link. The URL must be HTTP(S),
contain `{trace_id}`, omit URL user credentials and fit in 4,096 bytes; the name
is limited to 40 bytes and defaults to Tempo when a URL is enabled.

Owner mappings, Budget reporting limits and supported operational policies can
also be changed live, as described below. Listener addresses, database paths,
authentication/access surfaces, webhook destinations, Collector sources, Budget
enablement and tracked attribute keys retain their restart/configuration flow.
Token-file reload remains independent; theme and saved views are browser-local.

- Live editing accepts configuration files up to 1 MiB. Larger existing files
  still load at startup but require external editing and a restart.
- A YAML file must already be loaded. Geko does not create a configuration file
  implicitly. The file and its directory must permit replacement; read-only
  instances, symlinked config files, root YAML merges and shared/aliased
  trace-link scalars are not editable through this UI.
- Nonempty `GEKO_TRACE_LINK_URL` / `GEKO_TRACE_LINK_NAME` lock their respective
  fields. Editing an unlocked field never writes the effective env value into
  YAML. Other keys and comments are retained; YAML formatting can change.
- Saves validate before writing, synchronize a temporary file in the same
  directory, then replace the existing file. Windows uses `ReplaceFileW` to
  retain destination ACLs; Unix uses rename and directory synchronization.
  A directory-sync warning is shown if the save applied but crash durability
  could not be confirmed. Failed replacement leaves the live values unchanged.
- A revision prevents stale tabs from overwriting newer saves. External YAML
  edits cause a conflict; restart Geko to load them. There is no automatic
  config-file watcher. Coordinate external editors with UI saves: revision
  checks cannot lock an editor that does not cooperate.
- Cooperating Geko writers use `<config file>.lock`. A crash can leave this
  lock behind. Remove a stale lock only after confirming no configuration
  writer is active. Single-file Docker bind mounts may prevent replacement;
  mount a writable configuration directory if UI persistence is required.
- Viewer/triager accounts and read-only instances cannot use the editor/API.
  Writes have the dashboard's CSRF protection. Audit records contain the actor,
  field group and result, not URL values, credentials or the full YAML file.

The admin-only API uses `GET /api/v1/settings/runtime` for the current state,
sources and revision. `POST` replaces the supported trace-link pair; both
fields and the revision are required (empty strings explicitly disable it):

```json
{
  "revision": "<revision from GET>",
  "trace_link_url": "https://jaeger.example/trace/{trace_id}",
  "trace_link_name": "Jaeger"
}
```

A successful response contains the applied state. Conflicts return 409, invalid
input 400, and persistence failures 503; effective settings remain unchanged on
these failures. The API never returns the complete server configuration.

### Live Budget reporting limits

When Budget was enabled at startup, **Settings → Budget limits** can change
`default`, `services` and `cardinality_limit` without a restart. Save the complete
reporting policy as YAML; omitted limits become unlimited and an omitted or zero
cardinality threshold uses 100. Existing counters, session IDs, cardinality
fingerprints and persisted hourly volume are preserved. Reports for current and
historical hours use the **current** reporting limits, not the limits that were
active when the spans arrived. These thresholds never reject an export. `geko budget --server` reads the running
instance's updated reports on its next request.

The form supports up to 128 distinct namespace/service rules and 128 KiB of YAML.
Nonnegative span/byte limits use zero for unlimited; the cardinality threshold
accepts 0–255. Service rules replace the default limits for that service. Tracking
stays bounded to at most 256 dedicated service rows per hour even after repeated
rule changes. Traffic already counted in Other cannot be attributed retroactively;
a newly configured service may remain in Other until the next hour if the row
cap is full. Fingerprints remain process-local and are not persisted.

Enabling/disabling Budget and attribute keys remain restart-required.
`history_days` is editable under Operational settings.
Live saves retain those settings and unrelated YAML. Anchors, aliases or merges
inside `telemetry_budget` disable this editor; configuration loading still supports
those existing YAML constructs. Source labels reflect loaded values even when the
editor is disabled. There are no environment overrides for these Budget limits.

Administrators use `GET /api/v1/settings/budget` for supported limits, their sources
and the shared revision, and `POST /api/v1/settings/budget` to replace the reporting
policy. The `limits` object is required; `{}` explicitly restores unlimited volume
and the default cardinality threshold. Unknown fields such as `enabled` are rejected.
The trace-link, ownership and Budget editors share revision checks, file locking,
atomic replacement, CSRF protection and value-free audit records. Saving any of
them invalidates stale forms in the others. No full configuration is returned.

```json
{
  "revision": "<revision from GET>",
  "limits": {
    "default": {"spans_per_hour": 100000, "bytes_per_hour": 10485760},
    "services": [
      {"namespace": "storefront", "service": "payments", "spans_per_hour": 50000}
    ],
    "cardinality_limit": 100
  }
}
```

### Operational policies: preview and apply

**Settings → Operational settings** edits these supported policies:

| Setting | Application boundary |
| --- | --- |
| `retention_days` | Next minute maintenance tick; an active prune finishes with its captured cutoff. Pins remain protected. |
| `max_db_mib`, `on_full` | New export/metadata-write boundary. Existing accepted work keeps its captured policy. Reducing a limit does not compact the database. |
| `ingest_concurrency` | New admission attempts; reducing the limit drains existing exports without cancelling them. Waiting attempts observe increases within the existing two-second admission budget. HTTP, gRPC and imports share the gate. |
| `redaction` | Captured for each admitted export/import and each future trace/bundle download. Stored records are not rewritten, so historical UI/MCP evidence remains as originally stored. |
| `telemetry_budget.history_days` | Next checkpoint tick and new report snapshots. Zero skips checkpoints and hides persisted-history reports without deleting saved rows; 1–30 requires Budget enabled at startup. Enabling can persist already observed counters for the current live hour. Shortening expires older checkpoints on a subsequent successful save. |
| `digest` | Next notification tick; changes reschedule the next weekly delivery without a catch-up message. Existing destinations must be configured at startup. Empty disables the digest. |

Choose **Preview operational changes** before applying. The preview does not
write configuration or delete data. It counts currently eligible unpinned traces
with the same last-observed cutoff as retention and, when history is enabled,
older Budget checkpoints eligible for expiry. Main-file and reusable sizes are
shown. Counts are a point-in-time observation, not a guarantee about later ingest,
pins or already running maintenance.

An explicit checkbox is required when shrinking trace retention/capacity,
enabling eviction from reject mode, weakening redaction, enabling Budget history
or shortening enabled history retention. New policy is published only after the
shared atomic YAML save; stale revisions, env locks and failed replacement retain
the old policy. Other YAML fields/comments are kept. Environment-managed fields
are read-only and their effective values are never copied into YAML by a save.
Supported edits require unaliased operational fields and an unaliased Budget
subtree; a root YAML merge disables these edits. No new listener or file watcher
is created. Worker counts remain fixed; disabled work skips future ticks and all
workers are joined at shutdown.

The admin-only API uses `GET /api/v1/settings/operations`,
`POST /api/v1/settings/operations/preview` and
`POST /api/v1/settings/operations`. A preview takes the complete `policy` and
shared `revision`; apply uses the same body plus `confirmed: true` for the effects
above. Confirmation acknowledges the policy's effects, not a fixed deletion count.
The existing CSRF/access rules apply. Audit records actor, operation and outcome,
never the submitted policy values. JSON responses omit secrets and full config.

```json
{
  "revision": "<revision from GET>",
  "policy": {
    "retention_days": 7,
    "max_db_mib": 256,
    "ingest_concurrency": 4,
    "on_full": "evict_oldest",
    "redaction": "strict",
    "budget_history_days": 7,
    "digest": "monday 09:00"
  },
  "confirmed": true
}
```

### Live ownership mappings

**Settings → Ownership → Owner rules (YAML)** edits the service/application
maps. Service rules take precedence. **Save ownership** persists the loaded
configuration before publishing new immutable rules. Matching mapping comments
and unrelated YAML are retained; removed rules are removed. Save `{}` in the
editor to clear both groups. Edits support up to 256 rules and 128 KiB of owner
YAML. Names use at most 256 bytes; team names at most 128 bytes and no control
characters. Aliases, anchors and merges inside `owners` are not editable.
An unsafe ownership subtree does not disable an otherwise safe trace-link
editor, and a trace-link anchor does not prevent a safe ownership edit.

The running dashboard, its HTTP MCP endpoint and future notifications use the
same published ownership source. Old or already queued notifications are not
rewritten. CLI commands reload YAML on their next invocation. A separately
running stdio MCP process has its own startup configuration: restart that
session to reload its rules, or connect to the running server's HTTP MCP.

Nonempty `GEKO_OWNERS` locks the entire ownership editor; its value is never
copied into YAML by another editor. Ownership and trace-link edits share the
same revision and file lock, so saving either invalidates stale forms in the
other. External file edits still require a Geko restart. Role, CSRF, atomic
replacement and filesystem restrictions above apply to both editors.

Administrators can read ownership with `GET /api/v1/settings/owners` and replace
the mappings with `POST /api/v1/settings/owners`:

```json
{
  "revision": "<revision from GET>",
  "owners": {
    "services": {"payments": "payments-team"},
    "applications": {"storefront": "web-team"}
  }
}
```

This replaces both maps, not a patch: omitted groups become empty. The `owners`
object is required; `{ "owners": {} }` with a current revision explicitly
clears all mappings. Invalid input returns 400, a locked field or non-admin
caller 403, stale revisions 409 and persistence failure 503. Rejected writes
keep the previous rules active. Audit records name the `owners` field group
and result, never rule names, team values or submitted YAML.

## Check an application's connection

On **Connect**, choose the service and optional application namespace, then
select **Start a 60-second check**. Send a request to the application and use
**Check again** after the SDK's batch delay. Matching accepted OTLP exports
show a representative namespace, environment, version, run ID and trace link.
Each **Check again** performs a fresh request, including without JavaScript,
and keeps the original observation deadline. **Start a new 60-second check**
starts a new window; it does not replay an earlier observation.

Observations are in memory, reset on restart and retain one representative
sample per namespace/service/environment for up to five minutes, with at most
128 identities. A newer export can replace a sample from an earlier window;
replacement is disclosed. This is not a complete arrival log or proof of a
particular credential's use. Imports and older stored traces do not establish
a live connection. A trace link can become unavailable after retention or
eviction. Run IDs and versions shown belong to the representative sample,
not every span of the service.

Receiver context is global and cumulative since process startup, not scoped to
the selected service or window. Authentication failures happen before the
receiver and must be checked in Geko logs. No sample can mean an idle
application, batching, sampling, a different namespace or a configuration
problem; Geko does not infer that the SDK is absent. No exporter endpoints
are probed and no background observation worker is started.

For the optional listeners, an explicitly empty `GEKO_GRPC_ADDR` disables gRPC,
and an explicitly empty `GEKO_DASHBOARD_ADDR` restores the shared HTTP listener,
even if YAML configures a separate address.

| YAML key | Environment variable | Default |
| --- | --- | --- |
| `listen_addr` | `GEKO_LISTEN_ADDR` | `127.0.0.1:4318`. Non-loopback listeners require an access token and `GEKO_ALLOW_PLAINTEXT=true`. |
| — | `GEKO_ACCESS_TOKEN` | Empty. A token of 32–256 ASCII letters, digits, hyphens or underscores, with at least 10 different characters, enables authentication on all listeners. Generate one with `openssl rand -hex 32`. |
| — | `GEKO_INGEST_TOKENS` | Empty. Export-only tokens as comma-separated `name=token` pairs, with the same token rules. Requires `GEKO_ACCESS_TOKEN`. |
| — | `GEKO_INGEST_APPLICATIONS` | Empty. Binds ingest tokens to applications as comma-separated `name=application` pairs. See [Run on a server](server.md#run-on-a-server). |
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
| — | `GEKO_APPLICATION_WEBHOOKS` | Empty. Comma-separated `application=url` pairs; each application's groups go to its own webhook instead of `GEKO_WEBHOOK_URL`. See [Notifications](investigate.md#notifications). |
| `db_path` | `GEKO_DB_PATH` | `geko/geko.db` in the OS user configuration directory |
| `retention_days` | `GEKO_RETENTION_DAYS` | `7` (1–3650) |
| `dashboard_addr` | `GEKO_DASHBOARD_ADDR` | Empty: share the HTTP listener |
| `grpc_addr` | `GEKO_GRPC_ADDR` | Empty: disabled; set `127.0.0.1:4317` to enable |
| `max_db_mib` | `GEKO_MAX_DB_MIB` | `256` MiB for the main SQLite file (8–65536) |
| `ingest_concurrency` | `GEKO_INGEST_CONCURRENCY` | `4` exports decoded at once across OTLP/HTTP and gRPC (1–64). Each can hold a 4 MiB payload and its spans in memory. An export beyond that waits up to two seconds for a slot, and is refused with HTTP 503 and `Retry-After: 1` only if none frees up. Raise it when `Receiver busy` rejections appear in Settings. |
| — | `GEKO_MCP_TRIAGE` | `false`. Offer `set_group_state` on `/mcp` to clients with the triager or admin role. |
| `on_full` | `GEKO_ON_FULL` | `evict_oldest`; `evict_fair` to evict from the application holding the most spans first; or `reject` to keep stored traces and return HTTP 503 |
| `redaction` | `GEKO_REDACTION` | `best_effort`, or `strict`. The default is `strict` once `GEKO_ACCESS_TOKEN` is set, because other people can then read the data. |
| `owners` | `GEKO_OWNERS` | Empty. Teams owning groups, by evidence service and then by application; YAML takes `services` and `applications` maps, the variable `service:<name>=<team>` and `application:<name>=<team>` pairs, and replaces the YAML. See [Owners](investigate.md#owners). |
| `digest` | `GEKO_DIGEST` | Off. A weekday and whole hour in UTC, such as `monday 09:00`, for a weekly summary to every webhook. Needs `GEKO_WEBHOOK_URL` or `GEKO_APPLICATION_WEBHOOKS`. See [Weekly digest](investigate.md#weekly-digest). |
| `read_only` | `GEKO_READ_ONLY` | `false`. Refuse exports, triage, pins, notes, imports, application mappings and notification tests with HTTP 403; reading, searching and signing in still work, and every page shows what a viewer sees. Cannot be combined with `grpc_addr`. `geko serve --read-only` sets it too. |
| `collector_doctor` | — | Disabled until `config_file` or `metrics_url` is set. See [Collector Doctor](collector-doctor.md). |
| `telemetry_budget` | — | Disabled. Set `enabled: true` to report hourly volume and cardinality. See [Telemetry Budget](telemetry-budget.md). |

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
  In `reject` mode the physical file must fit, even if many pages are reusable.
  Schema backfills can temporarily grow the file. Startup warns when this
  blocks exports; stop Geko and other database readers and run `geko compact`,
  raise the limit, or choose eviction. Upgrades do not run VACUUM automatically.
- Pinned traces are neither expired nor evicted, and still count toward the
  size limit. When only pinned traces remain to evict, exports get HTTP 503
  until you unpin some. After unpinning, a trace older than the retention
  period is removed within a minute.
- Group notes, issue links, triage states and pins count toward the same limit
  and are checked like an export, also before the first export after startup.
  When the file is full and has no reusable pages, setting one fails with the
  same capacity error; clearing a note or reopening a group always works.
  Notes and states of groups with no stored trace are kept, so a resolved or
  muted group stays so when it recurs, and expire with the retention period.
- The WAL and shared-memory files use space beyond the limit. Stop the server
  and run `geko compact` to shrink the main file. `geko doctor` reports each
  file's size and the reusable pages.

**Settings → Storage → By application** also reports persistent capacity
evictions. A trace counts once for each application it touched, using
namespace, then trace assignment, then service mapping at deletion time.
Cross-application totals can exceed the global trace count. Retention is not
counted, and later rule changes do not relabel historical losses. Applications
with losses remain visible even after their last stored span expires.

The breakdown tracks up to 512 application labels of at most 512 UTF-8 bytes.
Additional or oversized labels accumulate in a separate visible **overflow
membership** counter, so metadata cannot consume the entire trace budget.
The global evicted-trace counter remains complete. Empty application names
(`No application`) are distinct from overflow. Losses from before schema 18
are shown as earlier global evictions with no recoverable application breakdown.

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

A mode change does not rewrite stored spans. New exports/imports capture the
current policy; future trace/bundle downloads reapply the current export
redaction policy. Historical UI and MCP reads retain their stored evidence. See
[Architecture](architecture.md) for details.

To search a custom span attribute by exact value, set
`GEKO_SEARCH_ATTRIBUTES` to a comma-separated list such as
`order.id,tenant.id` and `GEKO_ATTRIBUTE_SEARCH_KEY` to a randomly generated
32-byte key encoded as 64 hex characters. For example, generate it with
`openssl rand -hex 32`. Geko stores an HMAC-SHA-256 digest of the attribute
name and its string or integer value. In **All traces**, open **Search a
protected attribute**, choose the name, and enter the original value.
The value is submitted in the request body, and the results URL carries
the digest. Access to this form follows dashboard access.

Use dedicated span attributes for these values. Values copied to other
fields remain subject to those fields' redaction rules. Existing spans are
unaffected, redacted OTLP downloads omit the digests, and changing the key
makes earlier digests unsearchable by their original values.

## CLI and local API

See [Collector Doctor](collector-doctor.md) to analyze Collector pipelines
and internal metrics, and [Telemetry Budget](telemetry-budget.md) to track
accepted span volume and attribute cardinality.

| Command | Purpose |
| --- | --- |
| `geko serve` | Start the receiver and dashboard. `GET /healthz` returns 204 once it is listening, and `GET /readyz` once the database answers. Neither needs a token. `--open` opens the dashboard in a browser once Geko listens; `--read-only` sets `read_only`. |
| `geko demo` | Seed six hours of synthetic traces from seven services into a temporary database, keep adding one every two seconds, serve them and open the dashboard. The data is deleted when it stops. It reads no configuration. `--listen` picks the address (default: a free loopback port); `--no-open` and `--read-only` do what they say. An address other than loopback serves the demo read-only to anyone, at 10 requests a second per client; see [Public demo](server.md#public-demo). |
| `geko doctor` | Check SQLite integrity, storage sizes, and that every configured port is free. |
| `geko doctor --collector` | Check the Collector sources configured in Geko YAML. Use `--collector-config`, `--collector-metrics` or `--metrics-file` to supply sources directly. |
| `geko budget` | Report the running instance's current UTC hour. Supports `--previous`, `--json` and `--server`. |
| `geko failures --json` | Print a page of failures as JSON for scripts and coding agents, from the local database or a server with `--server`. See [Read traces from coding tools](agents.md). |
| `geko mcp` | Serve read-only investigation tools to an MCP client over stdio. `geko serve` also serves them at `/mcp`. |
| `geko compact` | Reclaim free space from the main database file (stop the server first). If another process still holds the file open, it reports that instead of leaving the file unshrunk. |
| `geko backup <file>` | Write a consistent copy of the database to a new file, while the server runs. |
| `geko run -- <command>` | Run a command with exporters pointing at Geko, then list the failures received meanwhile. See [Check a test run](agents.md#check-a-test-run). |
| `geko version` | Print build information. |
| `geko compare-runs --baseline ID_OR_FILE --candidate ID_OR_FILE` | Compare observed run groups, locally or with `--server`; `--json` returns structured results. Unreleased; see [Compare two runs](agents.md#compare-two-runs). |

The dashboard's data is also available as JSON on the dashboard listener, which
is the shared HTTP listener unless `dashboard_addr` is set:

| Endpoint | Returns |
| --- | --- |
| `GET /api/v1/failures` | A page of traces with failure evidence |
| `GET /api/v1/traces/{trace_id}` | A trace summary and a page of its spans |
| `GET /api/v1/budget` | Hourly telemetry report when enabled. Pass `previous=true` for the prior UTC hour. |

The failures and trace endpoints accept `limit` (1–100, default 50) and `cursor`. Pass the
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
| `pinned` | `true` for pinned traces only |

Trace summaries of pinned traces include `pin` with `pinned_at` and `pinned_by`.

## Upgrade an existing installation

Back up the SQLite database with `geko backup <new-file>`, which works while
Geko runs, or stop Geko and copy the file with any remaining `-wal` file beside
it. The database path is shown in **Settings** and `geko doctor`. Start the new binary with `geko serve` and the same
configuration. Geko updates the database schema at startup and derives
trace summaries and service membership from the existing spans. Startup
can take longer for larger databases, and the file can grow by roughly 15–25%.
If the database then exceeds `max_db_mib`, Geko prints a warning at startup;
with `on_full: reject`, raise the limit or exports are rejected. To return to an older binary after
a schema upgrade, restore the pre-upgrade backup before starting it.

Plan disk space for the upgrade itself. Upgrading a 455 MB schema-15 database
of 400,000 traces to schema 20 took 20 seconds on a Windows workstation and
needed 1.43 times the original size at its peak: the main file grew 21% and
the WAL held another 21%. Keep free space of at least half the database size,
beyond the backup. Afterwards about a tenth of the file is reusable pages;
stop Geko and run `geko compact` to reclaim them. See the
[measurement](benchmarks/2026-11-23-upgrade-schema15-20.json).

All migrations run in one transaction. If the upgrade is interrupted, by
stopping the process or a full disk, the database stays at its previous
schema with its data intact: free space and start Geko again. Another process
reading the database does not block the upgrade, but `geko compact` cannot
truncate the WAL until that reader closes.

### Additional captured attributes

`attribute_retention` opts into exact extra resource/scope/span/event/link keys
using YAML lists. It is a startup setting; strict mode ignores extras and
protected span search stays HMAC-only. Known credential/payload keys and
wildcards are refused. See [capture policy and limits](connect.md#captured-opentelemetry-context)
for supported scalar types, per-location limits and a complete example.
