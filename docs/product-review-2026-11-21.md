# Product and engineering review — 2026-11-21

## Scope and evidence

Three read-only agents reviewed product/onboarding, security/correctness, and
performance/architecture after the requested storage and grouping work. They
examined the current working tree, CLI/MCP flows, templates, existing demo
screenshots and benchmark records. This is a source review with local automated
validation, not a five-person user study or a formal security certification.
No hosted CI success or public release was assumed from local test results.

The implementation reuses the existing SQLite writer, migrations, query
interfaces and dashboard. It adds no service, external database, background
agent or weaker acknowledgement mode. The original code baseline is `55d61fe`;
the working tree includes schemas 16–20 and the preceding uncommitted fixes.

## Completed code milestones

- **Less repeated group metadata.** Shared group keys and filter partitions
  replace a JSON identity in every trace member. Deleting the last member
  collects empty partitions and identities in the same transaction.
- **Exact grouped queries.** Complete hours use weighted totals; at most two
  edge hours use individual members. Narrow windows use the member time index.
  Assignment/pin changes, late spans and retries maintain totals transactionally.
  Service mappings remain query-time decisions. Unsupported evidence/search
  filters and oversized metadata retain exact fallbacks, with no sampling.
- **Visible application losses.** Storage shows persistent capacity evictions
  per application, including applications with no remaining spans. A trace
  counts once for every application it touched; retention does not increment
  these counters. Labels keep their deletion-time meaning. Pre-upgrade global
  losses are explicitly unattributed.

See [architecture](architecture.md), [configuration](configuration.md#retention-and-capacity)
and the [benchmark record](competitive-research.md).

## Strengths

1. **A coherent investigation workflow.** Failure-first navigation separates
   the request outcome from a child span's exception. Evidence links, healthy
   comparisons, version comparisons and portable bundles support the same task.
2. **Useful agent integration.** CLI, MCP and PR reports can bring failure
   evidence into a coding workflow without opening a general monitoring stack.
   Telemetry is labelled untrusted and the default MCP path is read-only.
3. **Predictable local operation.** One binary, SQLite/WAL, bounded ingestion,
   whole-trace retention/eviction, pinned traces and durable acknowledgement.
4. **Honest evidence boundaries.** Best-effort redaction, strict mode, sampled
   Overview disclosure, missing historical attribution and overflow are explicit.
5. **Measured development.** Historical artifacts are preserved; paired runs
   carry binary/workload hashes. Narrow/edge windows and diverse versions are
   now measured alongside the dense end-to-end fixture.

## Findings resolved or mitigated during this work

| Finding | Result | Evidence |
| --- | --- | --- |
| An append-only application-loss ledger could consume the size budget under namespace churn. | **Closed.** At most 512 labels of 512 UTF-8 bytes; excess/long labels accumulate in visible membership overflow. The global trace count stays complete. Migration retains totals. | `019_eviction_counter_budget.sql`, `evictions.go`, budget/upgrade tests |
| Overflow could be confused with the empty application name. | **Closed.** Overflow is a separate scalar, not a reserved application label. Tests preserve `No application`. | `TestApplicationEvictionBudgetKeepsOverflowVisible` |
| A failed export could retain counters for deletions that rolled back. | **Covered.** Ledger, overflow, global counters, deletes and export commit together. | `TestApplicationEvictionsAreAtomicAndPersistent`, budget rollback test |
| A physical file above the limit can block `reject` despite reusable pages. | **Mitigated.** Startup explains the physical limit and compact/limit/policy choices. A real-file test proves compact restores writes. No automatic VACUUM is introduced. | `storage_warning.go`, `TestRejectOvergrownFileWarnsAndCompactRestoresWrites` |
| Benchmark bytes/span counted refused exports. | **Closed.** The denominator uses accepted exports; a refused cold-connection export fails the run. Prior zero-refusal records remain valid. | `scripts/bench-native.py` |

The physical-file test validates the relevant capacity end state. It does not
replace a large, full schema16→20 migration test with cancellation, low free
disk space and concurrent readers. That operational validation remains open.

## Prioritized next plan

| Priority | Problem and source evidence | Acceptance criteria |
| --- | --- | --- |
| **P1** | `investigate_group` collects detailed evidence only from the first 100 spans and the first ten errors. The selected exception can be outside either limit. [`mcp.go`](../cmd/geko/mcp.go), `investigateGroup` | Always fetch and prioritize `group.SpanID` independently of timeline pagination. Test 101+ preceding spans and ten earlier lower-ranked errors. Disclose partial evidence/pagination; preserve allowlisted/redacted output. |
| **P1** | `geko run` logs report/query errors while preserving a successful child exit. The Action treats a missing failure-count file as zero. Unavailable telemetry can look like no failures. [`run.go`](../cmd/geko/run.go), [`action.yml`](../action.yml) | Preserve the child's exit code but report explicit `telemetry_status=unavailable`. Missing reports must show unknown/unavailable, never zero. Test failed query/report endpoints and the Action's output/summary path. |
| **P1 delivery** | Source features and installation examples are ahead of the release declared in the repository. The Action example references `v0.2.0`, while release work is still open in the roadmap. | Verify the actual release/tag before advertising it. Exercise install-from-archive, `demo`, MCP setup and the Action from that exact release. Until then label source/unreleased examples explicitly. |
| **P1 performance** | New partition maintenance adds ingest work. Timing and space gains depend on failure rate, version/topology diversity and hour boundaries. | Track export p95, single/four-connection throughput, full-DB throughput, retained traces, and partition/member ratio. Optimize batch tuple preparation before weakening acknowledgement. Keep dense, rare and diverse profiles. |
| **P2** | MCP setup records only explicitly set config/DB environment paths. An implicit YAML with a relative DB can resolve differently under another client working directory. [`mcp_setup.go`](../cmd/geko/mcp_setup.go) | Resolve the effective config/database during setup. A different-cwd client must read the same database; configuration output must not expose token values. |
| **P2** | `GroupVersions` silently limits its oldest-first result to 20 rows; an agent can mistake the last returned row for the newest release. [`groups.go`](../internal/storage/sqlite/groups.go), [`mcp.go`](../cmd/geko/mcp.go) | Test 21+ versions; add pagination or explicit truncation plus trustworthy oldest/latest metadata. Suggestions must not claim the truncated list is complete. |
| **P2 validation** | Existing triage setters accept well-formed group keys without proving a current group exists, and growing metadata does not consistently call the ingest capacity gate. Source-level risk, not reproduced here. | Reproduce with low limits before first ingest and after reopening. Define the policy for historical/nonexistent groups and apply an appropriate transactional capacity guard. Avoid changing triage semantics without that decision. |
| **P2 operations** | Cache backfill can temporarily require old and new pages; high-cardinality partitions may not compress. | Test full schema16→20 upgrade peak file/WAL size, cancellation/rollback and external-reader compact behavior. Publish required headroom and operator recovery steps. |

## Follow-up implemented on 2026-11-22

The first two P1 code findings above are resolved in the working tree:

- MCP fetches bounded evidence details independently of timeline pagination,
  always with the selected primary first. `evidence_has_more` exposes omission.
  Tests cover a primary exception at span 120, twelve earlier errors, read-only
  MCP access, attribute filtering and storage selection/limit boundaries.
- Run reports publish an explicit collection-state manifest. Failed queries,
  invalid run-filtered responses or failed artifact downloads invalidate stale
  counts. The Action reports `unknown`, warns in the summary and skips the
  failure-count comment when collection is unavailable. Tests cover complete
  zero results, stale counts, unavailable responses and preserved child exits
  0/7. Available means report collection succeeded, not proof of SDK coverage.

Release/install verification and the P2 items remain open. Local regression
tests do not substitute for a hosted GitHub Actions run.

## Follow-up implemented on 2026-11-23

The P2 code findings are resolved in the working tree:

- **MCP setup paths.** Local setup loads the effective configuration as
  `geko serve` would and pins absolute `GEKO_DB_PATH` and, when a file was
  read, `GEKO_CONFIG`. A test reads the same database from another directory
  through an implicit `geko.yaml` with a relative `db_path` and checks that no
  token value is written.
- **Version history.** `GroupVersions` returns the 10 oldest and 10 newest
  pairs with the total; `investigate_group` adds `version_count` and
  `versions_truncated`, and its suggestions say when rows are omitted. Tested
  with 25 versions.
- **Triage metadata capacity.** Reproduced: before the first export, 4,000 note
  and state writes grew a `reject` database past its limit with no error.
  Notes, states and pins now pass the export capacity check in their
  transaction, including after reopening; removals stay unconditional. The
  dashboard answers HTTP 503 with a capacity explanation. **Policy decision:**
  groups without stored traces keep their metadata, because a resolved or muted
  state must survive until the group recurs; retention already expires it.
- **Upgrade operations.** `TestUpgradeMeasurement` copies an older database
  and records peak sizes, an interrupted upgrade and a concurrent reader. The
  400,000-trace schema 15→20 run peaked at 1.43× the file, rolled back
  completely when cancelled at 10 s, and compacted only after the reader closed
  its transaction. Headroom and recovery steps are in
  [configuration](configuration.md#upgrade-an-existing-installation). A low
  free-disk run was not performed; the single migration transaction is what
  makes that case roll back.

Also fixed: `geko run --report` removes an earlier run's traces from a reused
directory, and the Action validates against the `limit` recorded in
`report.json` rather than a copied constant.

Still open: tagging v0.2.0 and verifying install, `demo`, MCP setup and the
Action from the release; a hosted CI run; the Linux nightly baseline; a public
demo; a recorded agent session; and the five-developer study.

## Performance interpretation

The dense fixture is deliberately favorable to hourly totals: repeated group
identities and receipt times close together. Its speed is not a promise for all
filters. The additional profile benchmark verifies equality with the original
query before timing mixed failures, 1% failures, unique versions, a 15-minute
window and two partial edge hours. In rare/unique-version profiles, partitions
can equal members; no universal compression claim is made.

The raw evidence lives under `docs/benchmarks/`: `shared-groups-*` for the staged
paired/capacity runs and `group-window-profiles.txt` for the diverse query cases.
Use the final schema20 artifacts for the current implementation; earlier
schema18/19 trials are preserved as intermediate measurements. Docker mixed-load
figures are not isolated regression evidence. Windows measurements must not be
promoted to the Linux nightly baseline.

## Release and adoption gates

- Verify hosted Windows/Linux/browser/analysis jobs after the changes are pushed.
- Capture a genuine Linux nightly baseline and confirm the comparator fails
  on deliberate regressions and incomplete records.
- Validate released installation and Action examples, not only source builds.
- Record an actual agent investigation and run the five-developer study;
  agent reviews and screenshots are not substitutes for those observations.
- Prioritize evidence completeness and report reliability before adding logs,
  metrics storage, cloud accounts, alerts or ungrounded root-cause claims.
