# Implementation status

This checklist distinguishes implemented functionality from planned work.
The release used for this validation is [v0.2.0](https://github.com/YunusEmreAlps/geko/releases/tag/v0.2.0).

## Next phases after v0.2.0 (planned 2026-11-23)

The new release closes the preceding code review findings. The next objective
is to make one developer's repeated debugging workflow reliable across several
projects, then validate it with other developers. This section preserves the
original sequence; checked items below describe implemented work, while unchecked
items remain evidence gates or deferred proposals. There are no committed
release dates. Human study results must come from actual participant sessions.

### Release follow-through: before the next feature release

- [ ] Verify checksummed v0.2.0 archives, install scripts, `demo`, MCP setup
      from another directory and the Action using the released binary.
- [x] Update Scoop and Homebrew from the published v0.2.0 checksums.
      Windows installation/checksum, seeded demo readiness and MCP absolute
      paths were verified with the installed release. Linux/amd64 archive
      installation, MCP setup paths and seeded demo also passed in Docker on
      2026-11-24 ([record](benchmarks/2026-11-24-docker-validation.md)).
      Linux Homebrew public-tap installation, formula test and smoke checks also
      passed in Docker on 2026-11-27. Scoop, native macOS and the hosted release
      Action remain unverified.
- [x] Capture the Linux nightly baseline from the actual job (2026-11-24),
      with the artifact digest/provenance and regression-guard self-tests.
- [x] Repeat the large-database migration measurement on Linux. Schema 15→21
      passed with 400,000 traces / 800,000 spans: 32.44 s and 1.53× peak
      main-plus-WAL size, including interruption/retry and concurrent-reader
      checks ([record](benchmarks/2026-11-24-linux-upgrade-schema15-21.json)).
      Docker Desktop/WSL2, reconstructed fixture; retain the Windows record
      and do not treat different data/schema/hosts as a speed comparison.
- [x] Record one genuine agent investigation of an explicitly synthetic defect
      (2026-11-27): actual MCP evidence, fresh-context diagnosis, source fix and
      repeated workload/run comparison. See [walkthrough](agent-walkthrough.md).
      A public demo remains optional; no production incident or human-study result
      is inferred from this fixture.

Additional release evidence (2026-11-27): published Windows/amd64 and Docker
Linux/amd64 checksum/install/demo/dashboard/MCP setup passed; actual released
Windows SDK reporting and MCP protocol queries passed from another directory.
[The record](benchmarks/2026-11-27-release-validation.md) distinguishes each
outcome. A manually triggered [release-validation workflow](../.github/workflows/release-validation.yml)
prepares native macOS, Scoop and release-installed Action checks; Linux
Homebrew installation, formula test and release smoke also passed locally.
Those hosted/native/package-manager jobs are not claimed executed; no workflow
was dispatched and no branch was pushed.

The local release evidence identifies v0.2.0. Executed paths are recorded
above; unexecuted native platforms, Scoop and hosted jobs remain open gates.

### Phase 12: connect a real project and explain missing data

**Outcome:** a developer can distinguish an exporter configuration problem,
rejected telemetry, missing run attributes and an observed quiet application.

- [x] Extend Connect with an explicitly started 60-second observation window
      for a chosen namespace/service. Reuse receiver diagnostics; a bounded
      recent-commit cache separates live OTLP arrivals from imports and old
      database traces. No background network discovery or schema is added.
- [x] Show last observed telemetry, observed namespace/environment/version,
      matching run ID when present and relevant rejection reasons. Global
      rejections must remain global when credentials/service cannot be mapped.
- [x] Add real SDK examples for Go, Node.js, Python, Java and .NET in small
      batches. Each has a successful request, a failing request and explicit
      exporter shutdown/flush. Keep the existing synthetic wire examples.
      The real Go SDK example reads resource environment variables and tests
      application/run-ID filtering. Node/Python/Java/.NET examples make two
      loopback HTTP requests and drain exports before exit. All four passed
      the real-receiver smoke test locally on Windows; CI coverage is configured
      in `sdk-examples`. See [SDK examples](../examples/sdk/README.md).
      Real-SDK Connect tests cover wrong endpoint, wrong token, missing run ID,
      successful export and an idle service without inferring absent SDKs.

**Acceptance:** automated smoke checks for the examples; demonstrate wrong
endpoint, wrong token, missing `geko.run.id`, successful export and an idle
service. Lack of observed data must not be labelled proof that the SDK is
absent. Never expose token values or introduce automatic instrumentation.

### Phase 13: compare two runs and verify the observed fix

**Outcome:** after changing code, a developer or coding agent can compare
before/after evidence without manually reconstructing the two runs.

- [x] Add explicit baseline/candidate run comparison through the existing
      CLI, query API, MCP and report model. Reuse group keys and run IDs.
- [x] Present new, recurring and no-longer-observed groups, with retained
      trace counts and environment/service/version context for both runs.
- [x] Report missing run tags, incomplete reports and missing/evicted history
      as unavailable or partial. Do not automatically resolve groups because
      they were not observed again; zero failures does not prove a code fix.
- [x] Let the existing Action render the comparison when a baseline is
      explicitly supplied. Keep the child exit code; no automatic cross-PR
      baseline selection, new CI failure policy or separate artifact service.

Implementation uses bounded portable `run.json` snapshots and existing run-ID
indexes, with no new ingest-time ledger or schema. Results are always retained-
only, partial observations; Geko cannot reconstruct partial deletion or prove
SDK/workload coverage. The [comparison guide](agents.md#compare-two-runs) explains
the distinction. Local regression/browser checks cover the implementation;
the hosted SDK and explicit-baseline Action checks for commit `e60da6b` passed
on 2026-11-24 ([CI run](https://github.com/YunusEmreAlps/Geko/actions/runs/45254844980)).
A recorded source-linked code-change walkthrough of a deliberate synthetic defect
passed on 2026-11-27; see [the agent record](agent-walkthrough.md). This is an actual
agent investigation, not a production incident or participant study. New changes
after that commit still need their own hosted run.

**Acceptance:** tests for unchanged, introduced and absent groups, different
environments, zero exports, omitted run IDs and removed baseline history;
one recorded failing-run → code-change → comparison walkthrough. Bound any
new run metadata and account for it under the existing capacity policy.

### Phase 14: retain useful telemetry-budget history

**Outcome:** answer which service's accepted volume changed over time,
including across a Geko restart.

- [x] Persist compact hourly service totals in the existing SQLite store,
      with configurable history retention and bounded service rows.
- [x] Show comparable completed/partial windows and changes by service in
      the existing Budget page and CLI. Keep the current byte definition:
      normalized protobuf size before masking, not network traffic or billing.
- [x] Keep cardinality fingerprints ephemeral. Do not store raw attribute
      values or claim exact counts after tracking caps. Reports retain the
      current lower-bound and Other services disclosures.
- [x] Define crash semantics before implementation: periodic checkpoints
      must disclose possible loss of the latest interval; exact transactionally
      committed totals require measured ingest cost. Budget persistence must
      not silently change the acknowledgement contract or block trace writes.

Implemented as opt-in `history_days: 1–30` (default off), one-minute cumulative
session/hour checkpoints in schema 21. Restart aggregation excludes the live
session's saved copy. Gaps and partial hours suppress like-for-like deltas.
History yields to busy ingestion and skips near capacity; it may briefly
contend for the serialized writer but never changes trace acknowledgements or
evicts trace data. Its 1,024-record bound and discarded-record counter keep
coverage loss explicit. See [history semantics](telemetry-budget.md#optional-volume-history).

**Acceptance:** restart, partial-hour, counter-reset, overflow, retention and
capacity tests; paired enabled/disabled ingest measurements before choosing
the persistence approach. Exceeding a reporting budget still does not reject
telemetry. No general-purpose metrics backend or billing estimate.

### Phase 15: improve repeated investigation using observed feedback

**Outcome:** fewer navigation steps to evidence across several applications.

The user separately requested expert-led design improvements on 2026-11-24.
The [design review and implementation record](design-review-2026-11-24.md)
covers that authorized work: evidence density, mobile search, status semantics,
Budget, Connect and Settings hierarchy. Automated and visual checks passed;
these changes do not complete the participant study or its outcome measures.

- [ ] Run the existing five-developer study before selecting UI changes;
      record time to evidence, navigation steps and incorrect conclusions.
- [ ] Study-dependent candidates: dedicated environment/owner filters and any
      further navigation changes need participant evidence before selection.
      Application/service saved views and selected-span context were separately
      authorized and implemented in phases 15/17; do not duplicate them or mark
      the participant study complete based on automated checks.
- [ ] Select only improvements supported by the sessions. Preserve keyboard
      navigation, both themes, narrow layouts and explicit partial-data states.

**Acceptance:** repeat the same tasks after changes, publish the observations
without claiming statistical significance from five participants, and run the
existing browser/accessibility checks. No redesign solely for novelty.

**Readiness check (2026-11-24):** the [study harness](../scripts/usability-study.py)
and [facilitator instructions](competitive-research.md#facilitated-study) are
available. The [results sheet](usability-results.csv) still contains only the
assigned attempt order, with no participant observations. Automated tests and
agent reviews do not replace these sessions. Collect the sessions before
choosing changes or marking this phase complete. Both study fixtures passed
against the current Docker image and
Jaeger on 2026-11-24 ([record](benchmarks/2026-11-24-docker-validation.md));
this validates their data, not participant usability outcomes.

### Accepted implementation sequence (2026-11-27; local work completed below)

1. Extend the existing runtime manager to Budget reporting limits. Add an
   immutable reporting policy supplier; preserve counters, fingerprint seeds,
   session IDs, persisted checkpoints and collection/worker configuration.
   Bound service rows even after repeated rule changes. Test persistence,
   rollback, cross-editor revisions, historical reporting and admin/CSRF rules;
   then check responsive and no-script forms and concurrent use under race.
2. Add local named project/filter views and recent investigations, using the
   existing filter URLs. Keep browser-local state bounded, removable and
   explicit; avoid storing trace payloads or credentials.
3. Unify Connect, receiver, Collector and storage diagnostics, using existing
   diagnostics instead of adding a parallel collection system.
4. Improve success-baseline selection and operation comparisons, then record a
   source-linked agent investigation/fix/verification walkthrough.

Own-project trials and the five-participant study remain open validation gates.
These were the accepted implementation steps; their completed local results
and remaining external evidence gates are recorded below.

### Phase 16: dynamic settings (implemented supported scope 2026-11-27)

Requested on 2026-11-24. On 2026-11-25 the user explicitly requested starting
this phase; phase 15's participant sessions remain outstanding. Implemented
slices cover trace links, owner mappings, Budget reporting limits and controlled
operational policies. Listener, database and access-surface settings still require
a restart.

**Outcome:** administrators can change supported settings without restarting,
with clear application status and durable configuration.

- [x] Add a central runtime settings manager with validation, immutable
      snapshots, configuration revision checks and last-known-good behavior.
- [x] Persist supported UI edits atomically to the resolved YAML file,
      preserving comments. Retain `env > YAML > default`; environment-managed
      fields are locked with their source shown. Handle read-only mounts.
- [x] Apply trace links live through Settings and an admin-only API. Preserve
      unrelated YAML values/comments and show env locks, conflicts, persistence
      errors and successful application. Audit never records edited values.
- [x] Extend live editing to service/application owner mappings (2026-11-26).
      The admin form/API shares YAML locking, revisions and atomic replacement
      with trace links. Rules publish only after persistence, with environment
      locks, bounded input, safe YAML subtree checks and value-free auditing.
      Dashboard, HTTP MCP and notifications use one immutable owner source.
      CLI commands read YAML on startup; separate stdio MCP sessions must be
      restarted to reload their own configuration. No file watcher was added.
- [x] Extend the manager to Budget reporting limits (2026-11-27). Persist and
      publish immutable policies through the same revision/file-lock boundary.
      Keep counters, cardinality fingerprints, session IDs and checkpoint history;
      reports use current limits for live and historical hours. Bound service
      rows across repeated rule changes. Tracker enablement and collected attribute
      keys remain startup settings; history retention is now live as described below.
- [x] Add controlled reconfiguration for retention, size/full policies,
      checkpoint workers and notification schedules. Preview destructive
      retention effects; applying a smaller file limit does not compact it.
- [x] Redesign admission concurrency before making it live: replacing the
      existing channel while requests are active is unsafe. Apply privacy
      policy changes at export boundaries and disclose historical-data effects.
- [x] Keep listener addresses, database paths and access-surface changes
      restart-required initially. Existing token-file reload stays supported.
- [x] Require admin access, CSRF protection, safe audit records and explicit
      saved/applied/restart-required/error states. Secret values never appear
      in configuration responses or audit diffs.

The [live settings guide](configuration.md#live-settings) documents the current
scope, revision conflicts, filesystem restrictions and recovery. These checked
items apply to trace links, ownership, Budget reporting limits and operational
policies. Persistence, lifecycle and concurrent-update validation are summarized
in the completion pass below.

Local validation: the full Go suite, vet and targeted staticcheck passed.
Windows persistence and Linux `-race` checks covered concurrent runtime edits,
snapshots, trace writes and dashboard reads. Browser checks covered both themes
at seven widths, successful saves, immediate trace-link changes and recovery
from invalid form submissions. A new hosted CI run is still needed after push.

Ownership validation (2026-11-26): full Windows Go suite, vet, targeted
staticcheck and `go mod tidy -diff` passed. Linux Docker `-race` passed for
config, ownership, notifications and web. Tests cover concurrent editors and
lookups, failed-write rollback, restart persistence, comment retention,
environment locks, unsafe YAML and admin/CSRF/audit isolation. Existing MCP
sessions and notification instances observe published rule changes. Chromium
checks passed at nine widths from 320 to 1440px in both themes, including
ownership save, invalid YAML recovery and no-script save. The query/ingest
schema, Budget tracker and worker lifecycles were not changed. Independent
code review found no important issue. No commits or release were made.

Budget validation (2026-11-27): full Windows Go suite, vet, targeted
staticcheck and `go mod tidy -diff` passed. Linux Docker `-race` passed for
config, budget and web; the final source-attribution/concurrent-editor changes
were additionally retested for config and budget. Regression tests cover
counter/session/checkpoint preservation, historical limits, cardinality retention,
service-row bounds, three-editor revision conflicts, failed-write rollback,
restart persistence, YAML comments/shared aliases and locked-editor source labels.
Admin/CSRF/audit checks passed. Chromium checks passed at nine widths in both
themes, including mobile saves, invalid-input recovery and no-script saves.
Independent review's source-attribution finding was fixed and re-reviewed without
significant remaining findings. Existing uncommitted work was retained; no commit
or release was made at that slice. The later saved-view and operational
policy implementations are recorded below.

**Acceptance:** concurrent ingest plus configuration updates under `-race`,
invalid-update rollback, restart persistence, stale-edit conflicts, env locks,
read-only filesystem failures, admin/viewer isolation and worker lifecycle
tests. Do not claim atomic multi-component rollback until it is implemented
and tested; expose partial failures if any operation cannot be reversed.

**Scope boundary:** retain single-instance SQLite and durable ingest by default.
Distributed storage, production HA, logs/metrics storage, account management,
automatic remediation and unsupported root-cause claims are outside this plan.
MCP alone is not the differentiation; the target is a shorter, evidence-backed
path from a failing operation to a checked change.

### Phase 17: local project views (implemented 2026-11-27)

**Goal:** reopen recurring investigations without rebuilding project filters.
**Design:** reuse existing list URLs with browser-local preferences, scoped to
origin and signed-in identity/role. Save only application, service, evidence
service, time range, status, pins and group view/state. Omit text queries, protected
attribute digests, run/group IDs, cursor tokens and trace payloads. Keep the
normal server-rendered filters functional without JavaScript. No schema or API.

- [x] Add bounded URL/state helpers in `web/assets/workspace.mjs` and regression
      tests in `scripts/workspace.test.mjs`: same-origin list routes only, field
      allowlist, malformed/oversized storage, immutable updates and history caps.
- [x] Add a quiet Saved views disclosure alongside list filters, names up to
      64 characters and up to 12 views. Saving an existing name replaces it;
      provide individual removal and clear. Add an explicit resume link instead
      of redirecting users or changing their active filters automatically.
- [x] Add opt-in recent investigations: up to 20 trace IDs and visit times, no
      payload/title/exception storage; disabling clears them. Deleted/expired
      traces continue through the existing missing-trace page. Scope local
      preferences by the current signed-in identity and role; storage is a
      browser convenience, not an authorization boundary.
- [x] Test the real save/reopen/delete/resume flow, keyboard and long names,
      mobile overflow, unavailable storage and opt-in history in the existing
      browser harness. Add Node checks to CI, run Go checks, and request one
      independent review. Preserve prior uncommitted work; do not commit.

Validation (2026-11-27): full Windows Go suite, vet, web staticcheck,
`go mod tidy -diff`, formatting and Node workspace/action-report checks passed.
Chromium checks passed for saved-view controls at nine widths from 320 to
1440px in both themes, keyboard save, reopen/replace/remove/resume, safe text
rendering, storage-event updates across tabs, opt-in/deduplicated history,
missing traces, corrupt storage, denied reads/writes and the no-script filter
fallback. Independent review found no significant issue. Existing uncommitted
Budget/ownership work was retained; no commit, push or release was made.
The subsequent phase 18 completed the unified delivery diagnostics.

### Phase 18: evidence-led delivery diagnostics (implemented 2026-11-27)

**Goal:** answer where to check when a printed trace ID never reaches Geko.
Reuse the selected Connect observation, global receiver counters, bounded SQLite
status lookup and a value-only summary of the last manually requested Collector
inspection. Never attribute global/cumulative counters to the selected service,
never label a metadata read an integrity check, and never scrape a Collector
implicitly from Connect. Keep CLI `geko doctor` and existing diagnostic routes.

- [x] Add a small delivery view model and regression tests covering unknown,
      observed, waiting/ended, read-only, historical rejections, capacity with
      reusable pages and unavailable storage. Keep failure explanations generic;
      no paths, credentials, raw telemetry or Collector URLs in the shared summary.
- [x] Render four stages on Connect: selected-service observation, receiver,
      storage and Collector. Each shows evidence, scope and an actionable next
      step, with existing snippets/check-again behavior retained. Reuse query
      admission/timeouts for storage; failure must leave exporter setup readable.
- [x] Publish only timestamp/coverage/severity counts from explicit Collector
      checks; preserve a validated local Connect return context for refresh and
      sampling links. Show age and absence of a check; no polling or new workers.
- [x] Verify no implicit Collector requests, unavailable partial diagnostics,
      safe return URLs, cancellation/concurrent reads and responsive/no-script
      interactions. Update docs and obtain one independent review; do not commit.

Validation (2026-11-27): full Windows Go suite, vet, web staticcheck, module
consistency and Node checks passed. Linux Docker `-race ./web` passed. Tests
cover global/historical scope, missing observations, read-only ingestion,
capacity with reusable pages, partial diagnostic failure, no implicit Collector
requests, value-only source summaries, stale checks, safe local return links,
query admission and cancellation. Concurrent Collector checks/Connect reads
passed; the final cancellation guard was additionally tested with the full web
suite and vet. Chromium checks passed at nine widths in both themes, including
Collector return, native repeated checks and the no-script flow. Independent
review found no significant issue. No schema, receiver protocol or CLI doctor
behavior was changed; prior uncommitted work was preserved. No commit was made.
The subsequent phase 19 completed user-selected successful baselines.

### Phase 19: explicitly selected successful baselines (implemented 2026-11-27)

Reuse the existing one-snapshot comparison and its 500-span/100-group bounds.
Add an optional point-lookup baseline selector without changing the automatic
method or existing CLI/MCP callers. A selected baseline must satisfy the same
clean HTTP 2xx, unique server-entry, application/service/operation/namespace/
environment eligibility rules as automatic matching. Never substitute another
baseline for an invalid manual choice.

- [x] Add storage selection and read-only-reader forwarding, with regression
      tests for older clean successes, missing/ineligible/self choices, mismatched
      identities, source errors and bounded span facts in one transaction.
- [x] Add a native GET form on Compare traces; preserve safe results context,
      disclose manual versus automatic selection and show recoverable errors.
      Keep the current automatic comparison when no baseline parameter exists.
- [x] Verify manual selection, automatic reset, invalid/missing references,
      safe navigation, mobile and no-script flows. Update the investigation guide,
      run Go/browser checks and obtain an independent review. Do not commit.

Validation (2026-11-27): full Windows Go suite, vet, targeted staticcheck,
`go mod tidy -diff`, formatting and Node checks passed. Linux Docker `-race`
passed for SQLite and web. Storage tests cover explicit older successes,
identity/status/evidence incompatibility, source/baseline absence, span bounds
and read-only-reader forwarding. Query plans keep indexed point lookups for
manual references and preserve automatic baseline lookup plans. Chromium checks
passed for selection, editable errors, automatic reset, results context and
native no-script submission; selector controls fit nine widths in both themes.
Independent review found no actionable issue. No migrations, dependencies,
commits or releases were introduced at that slice. The completion pass below
subsequently implemented operational reconfiguration and recorded the agent
investigation/fix/verification walkthrough. The real-user study remains open.

### Completion pass (validated 2026-11-27)

- [x] Genuine fresh-context agent investigation of a deliberately broken,
      instrumented fixture: actual MCP evidence, source-linked diagnosis, minimal
      fix and the same workload rerun. Baseline had two traces and one failure;
      candidate had two traces and no observed failure. Comparison coverage stays
      `partial`; absence in this run is not proof of a universal fix. The final
      runtime binary also passed an independent automated replay, recorded
      separately from the genuine agent session in [the evidence](agent-walkthrough.md).
- [x] Published v0.2.0 Windows/amd64 and Linux/amd64 installer/checksum, demo,
      dashboard and MCP setup checks; actual Windows SDK/run and stdio MCP calls.
      Linux Homebrew public-tap install, formula test and release smoke also passed.
      Add executable, manual native/package-manager/hosted Action jobs without
      claiming they ran. See [release evidence](benchmarks/2026-11-27-release-validation.md).
- [x] Immutable operational policies for retention, capacity/full behavior,
      admission, privacy, Budget-history enable/retention and digest scheduling.
      Existing workers read policy at export/write/tick boundaries; accepted
      exports are not canceled. Listener/DB paths/auth/webhook targets remain
      startup settings. Budget tracking must already be enabled for live history.
- [x] Admin preview/apply with exact current retention candidates, explicit
      acknowledgment for destructive/privacy changes, shared revision conflicts,
      environment locks, durable-before-publish YAML and value-free audit.
      Smaller file limits do not compact the file; current strict download
      masking does not rewrite historical records. No background file watcher.
- [x] Full Windows `go test ./...`, `go vet ./...`, pinned staticcheck and
      `go mod tidy -diff`. Linux Docker `-race` passed for config, OTLP receiver,
      SQLite, Budget, notifier, web and CLI packages. Full browser checks passed
      in both themes at nine widths, including live changes and no-JavaScript
      submission. Three release-smoke and nine benchmark-comparison Python tests,
      17 workspace/Action Node tests and workflow actionlint passed.
- [x] Independent code review and focused re-review. Fix current-policy export
      masking, history-change acknowledgment, capacity UI/metrics consistency and
      a stale full-capacity hint after increasing the limit. Regression coverage
      includes persistence failure, null Budget configuration, active requests,
      history and retention behavior. No important review findings remain.

**External evidence still required:** five real developer study sessions,
actual native macOS/ARM64 execution, Scoop installation and hosted release jobs.
Prepared workflows do not count as results. Linux Homebrew passed locally in
Docker; package-manager details and failed setup attempts are recorded in the
release evidence. These gates need the
corresponding people/platforms or a workflow run after publishing the changes.
No participant feedback, hosted run or new release was invented. Existing
uncommitted work was retained; no commit, push or workflow dispatch was performed.

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
- [x] Product, security and performance agent source reviews. Resolved findings
      and outstanding validation are tracked below and in the release
      follow-through checklist. These are not human usability sessions or a
      formal security audit.

Next development order from that review:

1. **Completed 2026-11-22:** Always include the selected primary evidence in `investigate_group`, even
   beyond the first 100 spans or after ten earlier errors; disclose partial data.
2. **Completed 2026-11-22:** Distinguish unavailable telemetry/reporting from zero failures in `geko run`
   reports and the GitHub Action, while preserving the child command's exit code.
3. **Release published 2026-11-23:** v0.2.0 is tagged and recorded in the
   changelog. Verification of install-from-archive, `demo`, MCP setup and the
   Action from that exact release remains a separate follow-through task.
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
   A Linux schema 15→21 run passed on 2026-11-24: 32.44 s, 1.53× peak
   main-plus-WAL size, successful atomic rollback/retry and the same reader
   behavior ([record](benchmarks/2026-11-24-linux-upgrade-schema15-21.json)).
   The fixture was reconstructed at the same scale, not byte-identical;
   platform and target-schema differences prevent a speed comparison.

Durable acknowledgement stays the default. Opportunistic group commit already
exists; an optional early-ack policy is a deferred decision, not a release gate.
The CI baseline must come from the actual nightly Linux job, not Windows data.
Hosted CI verification, release-path validation/a public demo, a real agent session recording
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
- [x] Release v0.2.0 includes `geko demo`.
- [x] Verify published v0.2.0 Windows/amd64 and Linux/amd64 installer paths,
      checksums and demo startup ([record](benchmarks/2026-11-27-release-validation.md)).
- [ ] Deferred until hosting is available: a public demo instance at a stable
      address linked from the README. Local read-only demo/deployment assets
      exist; no hosted instance is claimed.

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
- [x] Record the first real Linux nightly result as
      `docs/benchmarks/ci-baseline.json`, with artifact checksum provenance.
      The workflow now requires this file and tests its regression/refusal
      guards; Windows measurements are not used as the CI baseline.
- [x] A soak run of one million spans at a 64 MiB limit, with a restart
      halfway. Schema 16 was retested: the file held at the limit, no export
      was refused, sampled memory peaked at 36.26 MiB, and restart took
      0.511 seconds. It retained 47,808 traces at 702 main-file bytes per
      span, versus schema 15's 59,200 traces and 567 bytes per span. Groups
      took 149.9 ms on the smaller final store, versus 891.6 ms before.

## Phase 10: the coding agent as the first reader

Geko already serves `/mcp`, `compare_trace` and `compare_versions`. This phase
treats an agent, not a person, as the first thing that reads a failure, because
that is the workflow Geko aims to make easier. Agent access alone is not a
unique capability; evaluate the end-to-end investigation workflow.

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
- [x] A [recorded genuine agent investigation](agent-walkthrough.md) of a
      deliberately broken fixture: actual trace/MCP evidence, fresh-context
      diagnosis, minimal source fix and unchanged workload verification
      (2026-11-27). This does not replace a human study or a production trial.
- [x] Release v0.2.0 includes the Action and `geko run --report`.
- [ ] Verify `uses: YunusEmreAlps/geko@v0.2.0` with the release installer,
      without the source-built `geko-binary` override.

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

## OpenTelemetry fidelity completion (validated 2026-11-27)

**Goal:** retain bounded, privacy-filtered trace context from ingest through
SQLite, UI and OTLP export, with honest diagnostics about missing evidence.
Use the existing normalized model and a defaulted JSON metadata column; do not
store raw OTLP or bypass redaction. No logs/metrics backend or custom SDK.

- [x] Preserve validated Span Links, trace state/flags, resource/scope schema URLs
      and producer dropped counters; distinguish old records without coverage.
- [x] Preserve non-exception event attributes and opt-in custom scalar attributes
      with exact per-location allowlists, hard limits and strict-mode protection.
      Keep privacy filtering and truncation counts separate from producer drops.
- [x] Persist/query/show/export/restore metadata without losing integer precision;
      test legacy databases, current strict downloads and linked-trace navigation.
- [x] Make Collector Doctor validation coverage explicit; detect unresolved
      configuration inputs without resolving secrets by default. An explicit
      CLI-only local binary can run distribution-specific validation with a
      five-second timeout and discarded output; the UI never executes it.
- [x] Persist privacy-safe Budget cardinality counts through existing bounded
      checkpoints; report partial/lower-bound aggregation and measurement scope.
- [x] Add reproducible official SDK/protocol/Collector compatibility checks.
      Run available cells and distinguish prepared workflows from observed runs.
- [x] Run focused regressions, full Go/static checks, browser checks and Linux
      race tests; review security, allocation bounds and backwards compatibility.

No commit, push, release or hosted workflow dispatch is part of this work.

Validation (2026-11-27): the full Windows Go suite, vet, pinned
staticcheck, module-tidy diff, actionlint, 12 Python regression tests and
17 Node regression tests passed. Full Chromium checks passed in both themes
at nine widths, including opened link/context/drop/filter disclosures, long
status values and no-script settings. Official Collector validation accepted
a valid config and rejected unsupported exporter options without exposing
process output. The [recorded SDK matrix](benchmarks/2026-11-27-otel-compatibility.json)
passed all 30 selected cells / 60 traces against the final binary, including
Go link/event and numeric-type preservation through OTLP downloads. Linux
Docker `-race` passed for config, privacy, OTLP, SQLite, Budget, Collector Doctor,
web and CLI; the final shared-envelope accounting optimization also passed a
fresh OTLP race run. Resource/Scope filtering and schema checks run once per
envelope, then copy immutable counts to each span. Independent review findings
on double-type fidelity, transfer provenance, location-specific limits and
historical allocation bounds were fixed and re-reviewed without serious
remaining issues. No commit or workflow dispatch was performed.

Trace-only scope remains intentional: Budget bytes describe accepted Span
protobuf size, not wire traffic or billing. Historical cardinality is a bounded
privacy-safe lower bound, not an exact cross-session union. Extra attributes
are explicitly opted-in scalars; strict mode still takes precedence. The SDK
record names unexercised cells; prepared hosted CI is not a completed run.

## Validation follow-through (validated locally 2026-11-27)

Use the existing official SDK fixture and manual release workflow; do not add
application features without demonstrated need or claim external jobs ran.

- [x] Add official Go OTLP/gRPC exporter support to the finite fixture and
      compatibility matrix. First observe the current fixture refusing gRPC,
      then verify both Go protocols through direct, Collector HTTP and Collector
      gRPC paths, preserving links, event numeric types and privacy boundaries.
- [x] Add a separate published `YunusEmreAlps/geko@v0.2.0` Action job to the manual
      release workflow. Keep checkout-Action testing as a separate check; verify
      no source-built binary override, comments disabled and positive actual SDK
      failure evidence. Do not dispatch the workflow or mark its hosted result done.
- [x] Update protocol counts and evidence; run full Go/static/script checks and
      workflow validation. Native/macOS/Scoop/ARM64 and real-user sessions stay open.

No commits, staging, push, deployment or hosted workflow dispatch.

Local verification: all 33 selected SDK cells / 66 traces passed; all six Go
HTTP/gRPC route cells preserved links, retry integers above 2^53 and doubles in
stored API/OTLP downloads. The [new evidence](benchmarks/2026-11-27-otel-compatibility-grpc.json)
records matching binary and Collector checksums. Full Go tests and actionlint
passed, along with vet, pinned staticcheck and a clean module-tidy diff.
Focused independent review found no actionable issue. The published-tag Action
job is prepared, not run; existing external evidence checkboxes remain open.

## Ingest concurrency investigation (validated 2026-11-27)

- [x] Control the experiment before tuning: equal total exports and warm-up per
      level, isolated fresh databases, rotating level order and repeated rounds.
      Preserve the legacy mode for historical results; report request latency,
      client CPU and per-round spread without hiding any refused exports.
- [x] Reproduce 1/4/16-exporter behavior on the current binary, then profile the
      single writer separately with the same fixed total workload. Measurements
      run exclusively, not concurrently with profiling/tests.
- [x] Change production batching/admission only when measurements show a real
      bottleneck; preserve durable commit acknowledgment and failure isolation.
- [x] Record actual results and uncertainty; test the harness/summary and any
      demonstrated optimization. No baseline replacement, commit or push.

Result: deterministic tests reproduced admission barging. FIFO slot reservations
fixed it with cancellation, expiry and live-limit coverage. In four local paired
16-exporter samples, receiver-busy refusals fell from 52 to zero and median
accepted-request p95 fell from 1,458.77 to 938.10 ms. Local throughput ranges
overlap; four-exporter results show no established speedup. SQLite mutex waiting
was negligible, while SQL execution and durable commits dominated profiling.
No write-slot, durability or batch-policy change was made. Statement reuse is
a future A/B candidate, not an implemented optimization or promised gain.

See [the investigation](benchmarks/2026-11-27-ingest-concurrency.md) and its raw
JSON for workload, binary hashes, mixed ordering, accepted-only percentiles and
host caveats. Full Windows Go/static/module checks, 18 Python benchmark tests,
actionlint, independent review and Linux OTLP race (including 100 stress repeats)
passed. CI now measures 512 attempted exports per level in three rotated rounds;
its existing native baseline was retained. No hosted run, commit or push.
