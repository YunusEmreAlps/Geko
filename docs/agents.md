# Coding agents and test runs

Read failures from scripts, MCP clients and test runs. [Back to the README](../README.md).

Query the configured local database with `geko failures --json`:

```sh
geko failures --json --service orders --status 503 --limit 10
```

The response contains `items` and, when another page exists, `next_cursor`.
Pass that value as `--cursor` with the same filters. Run
`geko failures --help` for all filters.

Register Geko with an MCP client from your project directory:

```sh
geko mcp setup claude-code   # writes .mcp.json
geko mcp setup cursor        # writes .cursor/mcp.json
geko mcp setup vscode        # writes .vscode/mcp.json
```

Each adds a `geko` server to the file and keeps every other server listed
there. Locally the client starts `geko mcp` by its absolute path. Setup
resolves the configuration the way `geko serve` would in the current directory,
including an implicit `geko.yaml` and a relative `db_path`, and writes the
absolute `GEKO_DB_PATH` and, when a file was read, `GEKO_CONFIG`, so a client
started in another directory reads the same database. Only paths are written,
never token values. With `--server https://geko.example.com` it connects to that server's
`/mcp` instead: Claude Code and Cursor send `GEKO_TOKEN` from the environment
that starts them, and VS Code asks for the token once. `--print` prints the
entry without writing anything, for a file with comments that setup refuses to
rewrite. Use `list_failures` for recent failure evidence, `get_trace` for
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

`investigate_group` answers in one call what an agent needs to explain a
group: the group with its counts and note, its latest trace with up to 100 span
timings, up to ten evidence spans with their status
description, exception events including any stack trace, and `code.*`
location, the latest successful request to the same operation with the
duration difference, and the group's traces by entry and evidence
`service.version`, ordered by first receipt, so the release it started in is
the first row and the newest the last. With more than 20 version pairs it lists
the 10 oldest and the 10 newest, sets `versions_truncated` and counts all of
them in `version_count`.
It returns no other span attributes, like `get_trace`. Ask for example
*"Investigate the open error groups in Geko and propose a fix for the largest
one"*; on the [demo dataset](../README.md#quick-start) the payment timeout group
points at `GatewayClient.authorize` and at web-frontend 5.13.0.

The selected primary evidence is always first in `evidence_spans`, even when
it falls outside the first timeline page or follows ten earlier errors.
`evidence_has_more` is true when additional evidence details were omitted;
open `group.evidence_path` in the dashboard to inspect them. Timeline pagination
is independent: pass `representative_trace.next_cursor` to `get_trace`.

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
using a triage token or the access token, or to any local client when no token
is configured. An agent can then resolve the group it fixed, with the version
that contains the fix. The group shows the token's name as who resolved it, and
the [audit log](server.md#audit-log) records the change. Read tokens and
`geko mcp` over stdio never get it. Telemetry text can
contain instructions planted by whoever controls a service's input, so enable
this only for agents you supervise.

Locally, `geko failures` and `geko mcp` open an existing SQLite database in
read-only mode. MCP tool descriptions mark telemetry text as untrusted data,
because exception messages and operation names come from the instrumented
services. A malformed request line receives a JSON-RPC error and the session
continues. Both work alongside a running Geko and can also read its database
after shutdown. Geko's stored evidence can appear in the MCP client's context.
If the database schema is older, run the matching `geko serve` to migrate it
first.

## Check a test run

`geko run` runs a command with exporter variables pointing at Geko, then lists
the failures tagged with that run's ID:

```sh
geko run -- go test ./...
geko run -- npm test
geko run --server https://geko.example.com -- go test ./...
```

It sets `OTEL_EXPORTER_OTLP_ENDPOINT`, the protocol, a 500 ms batch delay, and
turns off metric and log exporters, unless the environment already sets them.
Each command gets a unique `geko.run.id` in `OTEL_RESOURCE_ATTRIBUTES`. The
SDK must read and export that resource attribute. Geko waits up to two seconds
after the child exits before querying traces with the ID. The dashboard link
can be refreshed to include late arrivals. Pass the ID to `list_failures` as
`run_id` or to `geko failures --json` as `--run-id`. The summary goes to stderr
and the command's exit code is preserved.

Local mode starts Geko if needed and uses `GEKO_ACCESS_TOKEN` for queries and
as the default export credential. With `--server`, set `GEKO_TOKEN` to a read
token and `GEKO_INGEST_TOKEN` to the export token for the application.
Existing OTLP endpoint and header settings take precedence. Use a server
version that supports run IDs. Processes already running before the command
need to receive the run ID through their own instrumentation to be included.

## Report failures on pull requests

The Geko action runs a test command through `geko run`, writes the failing
traces to the job summary, comments them on the pull request, and attaches
each one as OTLP JSON that anyone can open in their own Geko under
**Traces → All traces → Restore a trace**. It runs on Linux and macOS runners.

```yaml
permissions:
  contents: read
  pull-requests: write
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: YunusEmreAlps/geko@v0.2.0
        with:
          run: go test ./...
```

The command's OpenTelemetry SDK must read the standard `OTEL_*` variables and
export `geko.run.id` from `OTEL_RESOURCE_ATTRIBUTES`. The job keeps the
command's exit status. Later runs on the same pull request update the comment
instead of adding one, and pull requests from forks, whose token cannot
comment, still get the job summary. Outside the action, `geko run --report DIR`
writes the same `report.md`, a `report.json` status manifest with the report's
`limit` of listed traces, the failure count in `failures`, and the traces under
`traces/`. Each run first removes the count and the traces of an earlier run in
the same directory. The manifest is published as
`telemetry_status: available` only after the run-filtered query and all requested
trace artifacts have been collected. This confirms report collection, not SDK
export coverage. A successful query with no recorded failure evidence has
`failures: 0`; a query/download/report error has `telemetry_status: unavailable`
and `failures: null`. Earlier count files are removed on failure.

The Action exposes `telemetry-status` and `failures` outputs. An unavailable or
invalid manifest produces `failures: unknown`, a job-summary warning and no
failure-count PR comment. It preserves the child command's exit code. The report
quotes every telemetry value as code, so a planted
mention, link or image in an exception message stays plain text.

## Compare two runs

**Unreleased source feature:** the commands and `baseline` Action input below
require a build from this checkout; they are not in v0.2.0. Keep using the
released Action without `baseline`, or supply a newly built `geko-binary`.

```sh
geko run --report before -- <your test command>
# Change the application code, then run the same workload.
geko run --baseline before/run.json --report after -- <your test command>
geko compare-runs --baseline before/run.json --candidate after/run.json --json
```

`run.json` is a bounded comparison snapshot, separate from the failure-count
manifest `report.json`. It carries trace/span counts, entry service/environment/
version contexts, and up to 100 error groups and 100 contexts. It includes no
exception messages, stacks or group notes. Run IDs must be nonzero, lowercase
32-character hexadecimal values. Snapshot files are limited to 2 MiB and their
identity/count/schema consistency is validated; this does not authenticate
the file's author. Treat imported reports as untrusted telemetry.

The baseline is captured **before** the candidate command starts, including
when reusing the same output directory. Save it in a different directory if
you want to retain the standalone baseline artifact as well. Query/artifact
errors never change the child command's exit code. The comparison is printed
to stderr, saved as `comparison.json` and appended to `report.md` when requested.
An unavailable snapshot never silently becomes a zero-failure baseline.

For retained runs in a local database or a shared Geko, use explicit run IDs:

```sh
geko compare-runs --baseline <baseline-id> --candidate <candidate-id> --json
geko compare-runs --baseline <baseline-id> --candidate <candidate-id> --server https://geko.example.com --json
```

The dashboard's **Compare runs** page takes the same IDs. The API exposes
`GET /api/v1/runs/{runID}` and
`GET /api/v1/runs/compare?baseline=<id>&candidate=<id>`; read tokens suffice.
MCP offers `compare_runs` with `baseline_run_id` and `candidate_run_id`.
ID-to-ID comparisons share one read-only SQLite snapshot. Membership follows
the existing run filter: a trace belongs to a run when any of its spans carries
that run ID, so cross-service traces remain intact.

Results classify groups as:

- `newly_observed`: present only in the candidate's captured evidence.
- `recurring`: present in both, with both trace counts.
- `not_observed`: present in the baseline, absent from the captured candidate.

**The comparison status is deliberately `partial`, even for complete queries.**
Geko can compare stored observations but cannot prove exporter coverage, that
the same requests were executed, or that no data was sampled, delayed or
deleted. Counts describe retained traces at capture, not total requests sent.
No group is automatically resolved and absence is never labelled a verified
fix. Entirely missing/removed runs, unavailable artifacts, truncated group/
context lists and different or unknown entry application/service/environment
scope yield `unavailable` with no absence classifications. Versions may differ.
Downstream evidence identities retain their own namespace and environment.

A saved snapshot remains a record of its capture after the original traces
are deleted; its trace citations may no longer open. An ID queried later reads
only what remains. Partial deletion cannot be reconstructed from the current
database and is covered by the retained-only warning, not invented loss counts.

### Explicit CI baseline

Build Geko from the checkout and provide a baseline `run.json` downloaded by
your workflow, or copied from an earlier step. Geko does not select or download
another PR's artifacts automatically:

```yaml
- uses: ./
  id: evidence
  with:
    geko-binary: ./bin/geko
    run: npm test
    baseline: ./baseline/run.json
    comment: "false"
```

The Action attaches `run.json`, report files and any failing trace exports,
with an artifact name containing the run ID so multiple calls in one job do
not collide. Outputs include `run-id` and `comparison-status` (`partial`,
`unavailable`, or `not_requested`). An unavailable comparison adds a warning;
it never reports a fix or changes the command's exit status. The job summary
includes the comparison even when no failures were observed in the candidate.
