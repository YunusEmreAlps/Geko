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
there. Locally the client starts `geko mcp` by its absolute path, with
`GEKO_DB_PATH` and `GEKO_CONFIG` copied from your environment when they are
set. With `--server https://geko.example.com` it connects to that server's
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
timings, the spans of that trace that recorded evidence with their status
description, exception events including any stack trace, and `code.*`
location, the latest successful request to the same operation with the
duration difference, and the group's traces by entry and evidence
`service.version`, oldest first, so the release it started in is the first row.
It returns no other span attributes, like `get_trace`. Ask for example
*"Investigate the open error groups in Geko and propose a fix for the largest
one"*; on the [demo dataset](../README.md#quick-start) the payment timeout group
points at `GatewayClient.authorize` and at web-frontend 5.13.0.

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
writes the same `report.md`, the failure count in `failures`, and the traces
under `traces/`. The report quotes every telemetry value as code, so a planted
mention, link or image in an exception message stays plain text.
