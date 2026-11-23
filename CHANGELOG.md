# Changelog

## v0.2.0 (2026-11-23)

- `investigate_group` lists the 10 oldest and 10 newest version pairs of a
  group with more than 20, with `version_count` and `versions_truncated`, so
  an agent no longer mistakes the 20th oldest release for the latest.
- `geko mcp setup` resolves the configuration and database the way `geko serve`
  would in the current directory, including an implicit `geko.yaml` with a
  relative `db_path`, and pins their absolute paths. A client started in
  another directory reads the same database. Token values are never written.
- Group notes, issue links, triage states and pins are held to `max_db_mib`
  like exports, also before the first export after startup. The dashboard
  explains a refused change as a capacity limit instead of a server error.
- `geko run --report DIR` removes an earlier run's traces from `DIR`, and
  `report.json` records the report's `limit`, which the Action validates
  instead of a copy of the number.
- `TestUpgradeMeasurement` upgrades a copy of an older database and records
  peak main/WAL size, an interrupted upgrade and a concurrent reader; see
  `docs/benchmarks/2026-11-23-upgrade-schema15-20.json`.
- MCP investigations always return the selected primary evidence first,
  independently of the first 100 timeline spans and ten-detail evidence limit.
  `evidence_has_more` discloses additional omitted details.
- Run reports publish `report.json` with an explicit available/unavailable
  collection state. Query or artifact errors invalidate stale counts. The
  Action reports unknown failures and warns instead of presenting zero,
  while preserving the child command's exit code.

- Shared group identities and exact hourly totals replace per-trace identity
  copies. Whole hours read weighted totals; at most two boundary hours read
  actual members. Pin/assignment changes, late spans, retries and eviction
  update totals transactionally. Export preparation is limited to the affected
  batch, and unchanged extrema are not reread on each deletion. Narrow and
  alternate-evidence paths remain exact; high partition diversity is measured
  separately rather than hidden by a constant-time claim (schemas 17 and 20).
- Storage shows persistent capacity losses per application, including labels
  with no remaining spans. Old global losses are explicitly unattributed;
  retention is excluded. A bounded 512-label/512-byte ledger sends excess
  application memberships to visible overflow without losing the global trace
  count (schemas 18–19). A failed export rolls all related changes back.
- Startup explains `reject` blocking on an oversized physical file even when
  pages are reusable, with compact/limit/policy recovery choices. No automatic
  VACUUM or deletion is added to startup.
- Product, security and performance agent reviews and the next acceptance
  criteria are recorded in `docs/product-review-2026-11-21.md`.
- `scripts/bench-paired.py` runs alternating baseline/candidate measurements,
  saves binary and workload hashes, and marks incomplete runs. The comparator
  reads paired candidate medians and rejects unfinished measurements. Native,
  concurrency and million-span capacity benchmarks were repeated; the capacity
  record documents schema 16's query gain and reduced retained history.
- Schema 16 backfills compact primary-evidence facts for error grouping and
  refreshes them within each export. Normal group queries avoid rereading span
  JSON while preserving exact counts, time windows, outcomes and application
  reassignment. Search and alternate-evidence filters keep the existing query;
  oversized historical metadata uses an indexed fallback instead of another
  large copy. Grouping still scales with the number of matching failures.
- Demo seeding keeps all 2,400 traces while writing at most 1,024 spans per
  export, reducing startup's durable commits from 60 to 12. The startup test
  allows for slow Windows CI storage, bounds browser and HTTP waits, and
  always waits for the demo server to stop after an assertion fails.
- `investigate_group` on `/mcp` and `geko mcp` returns, in one call, a group,
  its latest trace with span timings, the spans that recorded evidence with
  their exceptions, stack traces and code locations, the latest successful
  request to the same operation, and the versions the group's traces came
  from. Other span attributes stay out, as with `get_trace`.
- `geko mcp setup claude-code|cursor|vscode [--server URL]` adds Geko to the
  client's project configuration and keeps every other server in it.
- A GitHub Action (`action.yml`) runs a test command through `geko run`,
  writes the failing traces to the job summary, comments them on the pull
  request, updating its own comment on later pushes, and attaches each trace
  as OTLP JSON. `geko run --report DIR` writes the same report, the failure
  count and the traces. Telemetry text in the report is quoted as code, so
  planted mentions, links and images stay plain text.
- `owners` (`GEKO_OWNERS`) maps services and applications to teams. New and
  reopened group notifications add the owner, and error groups, the group
  summary, Settings and the MCP group tools show it.
- `GEKO_DIGEST=monday 09:00` sends every webhook a weekly digest of its
  applications' active, new, reopened, resolved and largest groups.
- **Download failure bundle** saves a group, its note and its ten newest traces
  in one file, and **Import a failure bundle** restores them into another
  instance, copying the note when the group there has none.
- **Settings → Storage → By application** shows each application's share of
  the stored spans and of the size limit, and the owner `evict_fair` evicts
  from first. `evict_fair` now looks the largest owner up every 10 seconds
  instead of every second.
- `scripts/bench-compare.py` and a nightly benchmark workflow compare
  `bench-native.py` and `bench-ingest-concurrency.py` with a baseline and fail
  on a regression beyond a factor, or any export refused with HTTP 503.
  `scripts/soak.py` sends a million spans into a database at its size limit,
  restarting the server halfway.
- `examples/service-map` reads `OTEL_RESOURCE_ATTRIBUTES`, so `geko run`
  attributes its trace to the run.
- `geko demo` seeds six hours of synthetic traces from seven services into a
  temporary database, keeps adding live traces, serves them and opens the
  dashboard, with no clone, exporter or configuration. Its error groups include
  a reopened, a muted and a noted group, and Version regressions flags the
  release that made payment timeouts frequent. Any address other than loopback
  serves it read-only to anyone, rate-limited per client;
  [`deploy/demo`](deploy/demo/compose.yaml) runs it behind Caddy.
- `geko serve --open` opens the dashboard once Geko is listening.
- `read_only` (`GEKO_READ_ONLY`, `geko serve --read-only`) refuses exports,
  triage, pins, notes, imports, application mappings and notification tests
  with HTTP 403, and shows every page as a viewer sees it.
- Install scripts for Linux, macOS and Windows that verify the archive against
  `checksums.txt`, a Scoop bucket and a Homebrew formula in this repository, and
  `scripts/update-packages.py` to refresh both after a release.
- `scripts/record-walkthrough.mjs` records the walkthrough from Error groups to
  the evidence span and Version regressions on the demo dataset.
- Version comparison reads the 10,000 most recent traces of each version from
  an index of the entry span's version, and says when it sampled: 854 ms to
  126 ms over 100,000 traces, and bounded however long the window. Schema 15
  adds that projection and an index of entry spans by operation.
- Comparing a failing trace with a success looks the operation up when none of
  the 1,000 newest successes matches: 451 ms to 6.8 ms over 100,000 traces.
- An export into a database at its size limit evicts once instead of failing
  and rewriting the export up to eight times, and `evict_fair` reuses the
  largest owner for up to a second instead of reading every `trace_services`
  row per attempt: 57 ms to 9.9 ms with `evict_oldest`, 161 ms to 12.7 ms with
  `evict_fair`, per 100-span export.
- Webhook checks group only failures received since the previous check unless
  one of them belongs to a group not yet seen: 200 ms to 2.6 ms per check over
  20,000 failing traces.
- Read connections map the database file and stay open between bursts. Error
  grouping, which also stopped joining pins and entry mappings it never used,
  takes 39 ms instead of 87 ms over 2,500 failing traces. `geko compact` now
  reports another process holding the file open instead of leaving it
  unshrunk, which a mapped file caused on Windows.
- The trace receiver waits up to two seconds for a free ingest slot before it
  refuses an export with HTTP 503 or gRPC `Unavailable`. An export that waits
  holds no payload, so the receiver's memory bound is unchanged. Eight
  exporters against the default four slots lost half of their exports before
  this change and lose none now.
- The application filter list no longer reads every stored span. It walks the
  namespace index one distinct application at a time, which takes 0.43 ms over
  50,000 spans instead of 64.76 ms, with 28 allocations instead of 3,236. Every
  page that offers the filter paid that cost; the Failures page went from
  247.75 ms to 2.52 ms over 17,408 failing traces.
- Filtering failures or error groups by an application that no stored span or
  assignment resolves to answers from an indexed lookup instead of examining
  every trace: 341 ms to 0.08 ms over 100,000 traces.
- Overview counts every failing trace in the window from an index, and reports
  the operation breakdown from the 10,000 most recent of them when there are
  more, which the page states. It no longer reads one span per failing trace in
  the window: 941 ms to 84 ms over 200,000 failing traces.

- [Collector Doctor](docs/collector-doctor.md) adds optional Collector YAML and
  Prometheus metrics analysis to `geko doctor` and Settings. Reports include
  pipeline findings, counter increases, queue pressure, and suggested actions.
  The dashboard can compare two metrics scrapes five seconds apart.
- [Telemetry Budget](docs/telemetry-budget.md) reports accepted OTLP span volume,
  hourly service limit overages, and high-cardinality attributes in Settings,
  `geko budget`, and `GET /api/v1/budget`.
- `geko run --server` uses separate query and ingest credentials and summarizes
  traces by run ID, indexed by SQLite.
- Ingest batches trace and service projection writes. Schema 14 skips
  exception JSON parsing for spans without events.
- Optional HMAC search matches exact values for configured custom span
  attributes on **All traces**.
- The Docker image includes a writable temporary directory for SQLite's
  larger ingest transactions.

- **Pin trace** keeps a trace past retention and capacity eviction. Pinned
  traces are marked in lists and on the trace page, filtered with `pinned=true`
  in the dashboard and `/api/v1/failures`, and counted in Settings. Triagers can
  pin and unpin; the audit log records `trace.pin` and `trace.unpin`.
- `GEKO_INGEST_APPLICATIONS` (or `ingest_applications:` in `GEKO_TOKENS_FILE`)
  binds ingest tokens to applications. Spans without `service.namespace` get
  the token's application; exports naming another application are refused
  with HTTP 403 or gRPC `PermissionDenied`. Settings and Connect show bindings.
- `GEKO_APPLICATION_WEBHOOKS` sends each application's new and reopened error
  groups to its own webhook; `GEKO_WEBHOOK_URL` then receives the other
  applications' groups. Settings lists each webhook's host, and **Send test
  notification** tests them all.
- The database migrates to schema 12, which adds the `trace_pins` table.

## v0.1.0 (2026-11-14)

First tagged release: everything in the [roadmap](docs/roadmap.md) phases 1 to 6,
and the first items of phase 7.

### Team use

- Triage tokens (`GEKO_TRIAGE_TOKENS`, or `triage:` in `GEKO_TOKENS_FILE`) read
  and resolve, mute or reopen error groups. Callers have the `viewer`,
  `triager` or `admin` role, the sidebar shows who is signed in, and pages hide
  actions the role can't use.
- Sign-in through an authenticating proxy such as oauth2-proxy:
  `GEKO_PROXY_USER_HEADER`, `GEKO_PROXY_TRUSTED_IPS`, `GEKO_PROXY_ROLES` and
  `GEKO_PROXY_DEFAULT_ROLE`.
- Error groups record who resolved or muted them, and `set_group_state` on
  `/mcp` is available to triagers.
- `GEKO_AUDIT_LOG` appends one JSON line per sign-in, sign-out and change.
- Error groups take a note and an issue link, shown in the group list and
  returned by `list_failure_groups` on `/mcp`. Triagers can edit them.
- **Copy as bug evidence** on trace pages and group summaries copies a Markdown
  description with links back to Geko, ready to paste into an issue.
- `trace_link_url` (`GEKO_TRACE_LINK_URL`) adds an **Open in Tempo** button to
  trace pages; `trace_link_name` changes the tool name.
- Error group titles show the classification instead of generic Go error types
  such as `*errors.errorString`.
- A container image for `linux/amd64` and `linux/arm64` at
  `ghcr.io/yunusemrealps/geko`; the Compose setup uses it.

### Privacy

- E-mail addresses, payment card numbers, IBANs and Turkish identity numbers
  are masked as `[email]`, `[card]`, `[iban]` and `[tckn]` before storage.
- Redaction defaults to `strict` once `GEKO_ACCESS_TOKEN` is set. Set
  `GEKO_REDACTION=best_effort` to keep exception messages and stack traces.

### Upgrading

- The database migrates to schema 11, which adds who changed a group's state
  and group notes.
- A token-protected instance without an explicit `redaction` setting switches
  to `strict` for new spans.
- Read token and triage token names must differ.
