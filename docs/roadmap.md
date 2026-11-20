# Implementation status

This checklist distinguishes implemented functionality from planned work.
The latest release is [v0.1.0](https://github.com/YunusEmreAlps/geko/releases/tag/v0.1.0).

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

- [ ] `geko demo` seeds a multi-service failing dataset into a temporary
      database, serves it, and opens the dashboard. No clone, no exporter
      variables, no configuration file.
- [ ] `geko serve --open` opens the dashboard once the listeners are ready.
- [ ] A recorded walkthrough from Failures to the evidence span, generated from
      the demo dataset by a script so it can be re-recorded for each release,
      at the top of the README.
- [ ] `--read-only` refuses ingest, triage and import, so a public instance can
      serve the demo dataset; add a rate limit and a Compose demo profile.
- [ ] Install without Git: an install script, a Scoop manifest, a Homebrew tap,
      and release notes that name the archive for each platform.
- [ ] A green build on master: the browser job builds with `-buildvcs=false`
      inside the Playwright image, and no test binary is tracked.

## Phase 9: evidence behind the numbers

Every performance and usability claim in [competitive research](competitive-research.md)
should come from a measurement someone else can repeat. The measured weaknesses
are addressed here, starting with the ones a shared instance hits first.

- [ ] Run the [five-developer study](competitive-research.md#facilitated-study)
      and publish time to evidence, navigation actions and wrong conclusions
      per tool.
- [ ] Group commit for ingest, with `ingest_ack: durable` (today's behaviour,
      acknowledge after commit) and `ingest_ack: batched`. Target: median
      export under 10 ms where a durable commit now takes 24 ms, and accepted
      throughput above 5,300 spans per second, with the durability difference
      stated in the configuration reference.
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
- [ ] A nightly run of [`scripts/bench-native.py`](../scripts/bench-native.py)
      and [`scripts/bench-ingest-concurrency.py`](../scripts/bench-ingest-concurrency.py)
      in CI that publishes their JSON and fails on a regression beyond a
      configured factor.
- [ ] A soak run of one million spans: eviction at the size limit, retention
      deletion, restart recovery, and the resulting bytes per span.

## Phase 10: the coding agent as the first reader

Geko already serves `/mcp`, `compare_trace` and `compare_versions`. This phase
treats an agent, not a person, as the first thing that reads a failure, because
that is the workflow no local trace viewer covers today.

- [ ] One command to register Geko with an MCP client, and a documented setup
      for Claude Code, Cursor and the VS Code extensions.
- [ ] A GitHub Action that runs a test suite through `geko run --server` and
      comments the failing traces, with evidence, on the pull request.
- [ ] One MCP call that returns a complete investigation bundle for a group:
      representative trace, evidence spans, the same operation when it
      succeeded, and the version where it started failing.
- [ ] A recorded session of an agent reading a failing trace and proposing the
      fix, next to the walkthrough from phase 8.

## Phase 11: a team that keeps using it

Adoption after the first week depends on Geko reaching people without their
opening it. Still not production monitoring: no logs, no metrics, no threshold
alerts.

- [ ] A weekly digest per application, over the existing webhooks: new groups,
      reopened groups, the largest groups, and what was resolved.
- [ ] Group ownership, so a new group's notification names the owning team.
- [ ] A portable failure bundle: one file with a group, its traces and its
      notes, exported from one instance and imported into another.
- [ ] Per-application retention and eviction figures in Settings, so a shared
      instance shows which application is consuming the size limit.

Not in scope: Geko keeps no user or password database; people sign in with a
token or through your identity provider's proxy.
