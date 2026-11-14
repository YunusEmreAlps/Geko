# Implementation status

This checklist distinguishes implemented functionality from planned work.
Release workflow configuration does not mean a release has been published.

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
instrumentation. Arbitrary custom attributes are not retained. Publishing and
verification of a tagged release remain a separate release operation.

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

- [ ] Tag and verify a first release (`v0.1`).
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

- [x] `geko run -- <command>` lists failures received while a command ran,
      matched by receipt time. Matching by test would need spans that carry
      test names.
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
- [ ] Resource comparison with Jaeger and the five-developer usability study
      from `competitive-research.md`. Both need people and a test setup.
- [ ] Visual regression coverage for both themes. Needs a browser in CI.

## Phase 6: trustworthy for a team

- [ ] Tag and verify `v0.1.0`; the release workflow now also publishes a
      multi-arch image to `ghcr.io/yunusemrealps/geko`. Tagging is a manual
      step, see [Releasing](releasing.md).
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
- [ ] Allowlisted attributes, such as a tenant or order number, stored as HMAC
      hashes and searchable by their value.
- [ ] Tokens bound to one application, and a webhook per application.
- [ ] `geko run --server` and matching traces to a test run ID.
- [ ] Pinned traces that outlive retention.

Not in scope: Geko keeps no user or password database; people sign in with a
token or through your identity provider's proxy.
