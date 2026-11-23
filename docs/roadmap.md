# Implementation status

This checklist distinguishes implemented functionality from planned work.
The latest release is [v0.1.0](https://github.com/YunusEmreAlps/geko/releases/tag/v0.1.0).

## Current implementation status (2026-11-21)

Reviewed against `55d61fe` plus the current working tree. The three requested
code milestones are implemented:

- [x] Shared group identities and compact member references (schema 17).
      Empty partitions and identities are collected transactionally. Schema 20
      prepares only the affected export and avoids recomputing unchanged extrema.
- [x] Exact hourly group totals, with individual members for at most two edge
      hours. Short windows and search/run/alternate-evidence filters retain
      exact member/legacy paths. There is no sampling or universal constant-time
      claim; high partition diversity can reduce compression.
- [x] Persistent application eviction counters (schemas 18–19), committed with
      deletion and the incoming export. Retention is excluded. A trace counts
      once for each application it touched. The breakdown tracks at most 512
      labels of up to 512 UTF-8 bytes; excess memberships go to a visible overflow
      counter. Earlier global losses are not assigned invented application names.
- [x] Product, security and performance agent source reviews. Their scope,
      evidence, resolved findings and next acceptance criteria are in the
      [product review](product-review-2026-11-21.md). These are not human usability
      sessions or a formal security audit.

Next development order from that review:

1. **Completed 2026-11-22:** Always include the selected primary evidence in `investigate_group`, even
   beyond the first 100 spans or after ten earlier errors; disclose partial data.
2. **Completed 2026-11-22:** Distinguish unavailable telemetry/reporting from zero failures in `geko run`
   reports and the GitHub Action, while preserving the child command's exit code.
3. Validate the release/install/action path before promoting unreleased commands.
   v0.2.0 is prepared in the changelog; tag it, then exercise install-from-archive,
   `demo`, MCP setup and the Action from that release.
4. **Completed 2026-11-23:** MCP setup pins the effective config/database paths;
   `investigate_group` keeps the oldest and newest versions and discloses
   truncation; group notes, states and pins are held to the size limit. Groups
   without stored traces keep their metadata, so a resolved or muted group stays
   so when it recurs; retention expires it as before.
5. Continue measuring write amplification and diverse partitions before
   considering any weaker ingest acknowledgement policy. A full schema 15→20
   upgrade of 400,000 traces was measured on 2026-11-23: 1.43× peak disk,
   atomic rollback when interrupted, and a concurrent reader that blocks only
   WAL truncation ([record](benchmarks/2026-11-23-upgrade-schema15-20.json)).
   A Linux run remains to be recorded.

Durable acknowledgement stays the default. Opportunistic group commit already
exists; an optional early-ack policy is a deferred decision, not a release gate.
The CI baseline must come from the actual nightly Linux job, not Windows data.
Hosted CI verification, a release/public demo, a real agent session recording
and the five-developer study remain delivery or validation work below.

## Phase 1

- [x] Indexed trace summaries and query-plan regression coverage.
- [x] One snapshot for all trace detail sections.
- [x] Loopback Host checks, OTLP partial success and bounded decoding.
- [x] Dependency updates, MIT license and GitHub module path.
- [x] GoReleaser configuration and release workflow.
- [x] Broader safe attribute policy and 32 KiB exception stack traces.
- [x] OTLP/HTTP JSON and direct lookup of healthy trace IDs.
- [x] Node, Python, Java and .NET synthetic JSON export examples.
- [x] CI configuration for staticcheck, govulncheck, race, tidy and fuzz.

The non-Go examples demonstrate the wire protocol, not full application SDK
instrumentation. Arbitrary custom attributes are not retained.

## Phase 2

- [x] Optional OTLP/gRPC listener, including the conventional port 4317.
- [x] Optional separate dashboard/query API listener.
- [x] Error grouping with trace count and first/last receipt timestamps.
- [x] Live updates on Failures using SSE.
- [x] Browse all traces, including successful requests.
- [x] Export and restore redacted OTLP JSON traces.
- [x] Token-protected Docker mode for explicitly configured remote use.
- [x] Indexed ingest-time trace/service projections and cross-export write batching.

Application grouping is separate from error grouping. Trace summaries and
service membership are maintained in SQLite as spans arrive. Application
assignments are resolved on reads, including after historical reassignment.

## Phase 3

- [x] Read-only MCP and `geko failures --json` for coding agents.
- [x] Compare failing and successful traces of the same operation.
- [x] Version-based regression detection.
- [x] VS Code links from recorded stack traces with absolute source paths.
- [x] Evidence-citing error group summaries in the dashboard and group evidence for AI summaries through MCP.

## Phase 4: shared dev and staging instance

One Geko instance on a server receives traces from several applications and
reaches their developers and coding agents. Scope: side projects, dev and
staging. Not production monitoring: no logs or metrics, no threshold alerts,
no multi-user accounts, no built-in ACME, no external databases.

- [x] Tag and verify a first release (`v0.1`).
- [x] Export-only ingest tokens per application (`GEKO_INGEST_TOKENS`).
- [x] Non-loopback listeners require `GEKO_ALLOW_PLAINTEXT=true`.
- [x] `public_url` for the trace endpoint shown behind a proxy.
- [x] Unauthenticated `/healthz` and database-backed `/readyz`.
- [x] `geko backup` with `VACUUM INTO` while the server runs.
- [x] Compose with Caddy (HTTP and gRPC on 443) and a systemd unit in `deploy/`.
- [x] Read-only tokens (`GEKO_READ_TOKENS`) for agents, scripts and viewers.
- [x] Read-only MCP over Streamable HTTP at `/mcp` and `geko failures --server`.
- [x] Webhooks for new and reopened error groups (JSON, Slack, Discord, ntfy).
- [x] Resolve, mute and reopen error groups; "New" marks groups first received
      since the browser's last visit to the open list.
- [x] A connect page that generates exporter environment variables.

## Phase 5: from failure to verified fix

- [x] `geko run -- <command>` configures OTLP for a child command and lists
      its failures. Phase 7 added filtering by run ID.
- [x] MCP `compare_trace` and `compare_versions`; `set_group_state` on `/mcp`
      for full-access clients when `GEKO_MCP_TRIAGE` is set.
- [x] Resolve in a version: only traces from the fixed `service.version` reopen
      the group, so rollouts don't.
- [x] Configurable ingest concurrency, rate-limited logging of rejected
      credentials, and `/metrics`.
- [x] `on_full: evict_fair` evicts from the application holding the most spans.
- [x] Browser sign-in with in-memory sessions and sign-out; no Basic dialog.
- [x] Token reload without restart through `GEKO_TOKENS_FILE`.
- [x] Go benchmarks for ingest, failures and error groups
      (`go test -run '^$' -bench . ./internal/storage/sqlite`).
- [ ] Five-developer usability study. The [study setup](competitive-research.md#facilitated-study)
      and results sheet are ready for participant sessions.
- [x] Repeatable resource comparison with Jaeger's in-memory setup.
- [x] Browser layout checks and screenshot artifacts for both themes in CI.

## Phase 6: trustworthy for a team

- [x] Tag and verify `v0.1.0`, with a multi-arch image at
      `ghcr.io/yunusemrealps/geko`. See [Releasing](releasing.md).
- [x] Triage tokens and `viewer`, `triager` and `admin` roles, in the dashboard,
      the API and `/mcp`.
- [x] Users from an authenticating proxy (oauth2-proxy or Caddy `forward_auth`)
      with per-user roles, trusted only from configured addresses.
- [x] Who resolved or muted a group is stored and shown.
- [x] JSON audit log of sign-ins and changes (`GEKO_AUDIT_LOG`).
- [x] E-mail, card number (Luhn), IBAN (mod-97) and TCKN masking before storage.
- [x] `strict` redaction by default once an access token is configured.

## Phase 7: people who don't write the code

- [x] A note and an issue link per error group, shown in the list and returned by
      `/mcp`.
- [x] **Copy as bug evidence**: Markdown for an issue tracker, from a trace or a
      group.
- [x] **Open in Tempo**: a configurable link from a trace to another trace tool.
- [x] Allowlisted attributes, such as a tenant or order number, stored as HMAC
      hashes and searchable by their value.
- [x] Ingest tokens bound to one application (`GEKO_INGEST_APPLICATIONS`).
- [x] A webhook per application (`GEKO_APPLICATION_WEBHOOKS`).
- [x] `geko run --server` and matching traces to a test run ID.
- [x] Pinned traces that outlive retention and capacity eviction.

## Collector diagnostics and telemetry volume

- [x] [Collector Doctor](collector-doctor.md) checks pipeline wiring and
      diagnoses failures and queue pressure from internal metrics.
- [x] [Telemetry Budget](telemetry-budget.md) reports hourly span volume,
      per-service budget overages, and high-cardinality attributes.

## Phase 8: the first minute

Phases 1 to 7 serve someone who already decided to run Geko. This phase serves
someone who has not: they should see failure evidence before instrumenting an
application, and find a binary without a Go toolchain.

- [x] `geko demo` seeds six hours of a seven-service failing dataset into a
      temporary database, keeps adding live traces, serves it, and opens the
      dashboard. No clone, no exporter variables, no configuration file.
- [x] `geko serve --open` opens the dashboard once the listeners are ready.
- [x] A walkthrough from Error groups to the evidence span and Version
      regressions, recorded from the demo dataset by
      [`scripts/record-walkthrough.mjs`](../scripts/record-walkthrough.mjs) so
      it can be re-recorded for each release, in the README's quick start.
- [x] `--read-only` refuses ingest, triage and import. `geko demo` on an
      address other than loopback serves the demo dataset read-only to anyone,
      at 10 requests a second per client, and
      [`deploy/demo`](../deploy/demo/compose.yaml) runs it behind Caddy.
- [x] Install without Git: install scripts that check `checksums.txt`, a Scoop
      bucket, a Homebrew formula, and release notes that name the archive for
      each platform.
- [x] A green build on master: the browser job builds with `-buildvcs=false`
      inside the Playwright image, and no test binary is tracked.
- [ ] A public demo instance at a stable address, linked from the README, and
      a release that ships `geko demo` so the install scripts reach it.

## Phase 9: evidence behind the numbers

Every performance and usability claim in [competitive research](competitive-research.md)
should come from a measurement someone else can repeat. The measured weaknesses
are addressed here, starting with the ones a shared instance hits first.

- [ ] Run the [five-developer study](competitive-research.md#facilitated-study)
      and publish time to evidence, navigation actions and wrong conclusions
      per tool.
- [ ] Deferred: evaluate an optional early-ack policy only after optimizing
      write amplification and proving a practical need. Group commit already
      exists; exports currently wait for commit. An earlier acknowledgement
      changes the durability guarantee and is not required for the next release.
- [x] Wait up to two seconds for an ingest slot instead of refusing at once.
      Eight exporters lost half their exports to HTTP 503 at the default four
      slots and sixteen lost 397 of 512; the same sweep now accepts every
      export at every level, at the same throughput.
- [x] Stop the Failures page reading every stored span to list its
      application filter, which cost 248 ms at 17,408 failing traces and
      varied by a factor of six between rounds. It walks the namespace index
      one application at a time and takes 2.5 ms, and filtering by an
      application no span resolves to answers in 0.08 ms instead of 341 ms
      over 100,000 traces.
- [x] Bound the Overview aggregation. It counted every failing trace in the
      window and read a span for each one, at 941 ms over 200,000 failing
      traces. The count stays exact and index-only; the breakdown covers the
      10,000 most recent traces and the page says so when it sampled. The same
      benchmark takes 84 ms.
- [x] Speed up the paths older than these measurements, measured as a paired
      run against the commit before them: version comparison reads the newest
      10,000 traces of each version (854 ms to 126 ms over 100,000 traces),
      trace comparison looks the operation up when no recent success matches
      (451 ms to 6.8 ms), an export at the size limit evicts once (57 ms to
      9.9 ms, and 161 ms to 12.7 ms with `evict_fair`), webhook checks group
      only new failures (200 ms to 2.6 ms), and read connections map the file
      (error grouping 87 ms to 39 ms).
- [x] Project compact primary-evidence facts for error grouping (schema 16),
      preserving exact counts and resolving application mappings at query time.
      Keep evidence-search and oversized-metadata fallbacks exact.
- [x] Aggregate complete hours from exact per-partition totals. Only up to two
      edge hours need member reads; narrow windows use the member time index.
      Search/run/alternate-evidence fallbacks remain exact. Diverse versions and
      topologies can increase partition count; see the profile measurements.
      This bounds history scanning, not every possible query's wall-clock time.
- [x] A nightly run of [`scripts/bench-native.py`](../scripts/bench-native.py)
      and [`scripts/bench-ingest-concurrency.py`](../scripts/bench-ingest-concurrency.py)
      in CI that publishes their JSON and fails on a regression beyond a
      configured factor, or any export refused with HTTP 503.
- [ ] Commit the first nightly result as `docs/benchmarks/ci-baseline.json`,
      so the nightly run compares instead of only reporting.
- [x] A soak run of one million spans at a 64 MiB limit, with a restart
      halfway. Schema 16 was retested: the file held at the limit, no export
      was refused, sampled memory peaked at 36.26 MiB, and restart took
      0.511 seconds. It retained 47,808 traces at 702 main-file bytes per
      span, versus schema 15's 59,200 traces and 567 bytes per span. Groups
      took 149.9 ms on the smaller final store, versus 891.6 ms before.

## Phase 10: the coding agent as the first reader

Geko already serves `/mcp`, `compare_trace` and `compare_versions`. This phase
treats an agent, not a person, as the first thing that reads a failure, because
that is the workflow no local trace viewer covers today.

- [x] One command to register Geko with an MCP client: `geko mcp setup
      claude-code|cursor|vscode`, locally over stdio or with `--server` over
      Streamable HTTP, keeping the other servers in the file.
- [x] A GitHub Action that runs a test suite through `geko run`, writes the
      failing traces to the job summary, comments them on the pull request,
      and attaches each as OTLP JSON to restore in any Geko. CI runs it
      against the example on every push.
- [x] One MCP call, `investigate_group`, that returns the group, its latest
      trace with span timings, the evidence spans with exceptions, stack
      traces and code locations, the same operation when it succeeded, and
      the versions its traces came from, oldest first.
- [ ] A recorded session of an agent reading a failing trace and proposing the
      fix, next to the walkthrough from phase 8.
- [ ] A release that ships the action's `geko run --report`, so
      `uses: YunusEmreAlps/geko@v0.2.0` works without `geko-binary`.

## Phase 11: a team that keeps using it

Adoption after the first week depends on Geko reaching people without their
opening it. Still not production monitoring: no logs, no metrics, no threshold
alerts.

- [x] A weekly digest per webhook, `GEKO_DIGEST=monday 09:00`: active, new,
      reopened, resolved and the largest groups of each application's week.
- [x] Group ownership: `owners` rules map services and applications to
      teams, and notifications, the digest, the dashboard and the MCP group
      tools name the owner.
- [x] A portable failure bundle: the group, its note and issue link and its
      ten newest traces in one file, downloaded from the group and imported
      into another instance, which copies the note when its group has none.
- [x] Per-application storage in Settings: each application's share of the
      stored spans and of the size limit, and the owner `evict_fair` evicts
      from first.
- [x] Count capacity evictions per application and show retained history,
      persistent losses, coverage gaps and bounded-label overflow on Storage.
      Retention is not counted; cross-application totals need not sum to the
      global trace count.

Not in scope: Geko keeps no user or password database; people sign in with a
token or through your identity provider's proxy.
