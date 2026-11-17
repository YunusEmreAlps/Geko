# Coding agents and test runs

Read failures from scripts, MCP clients and test runs. [Back to the README](../README.md).

Query the configured local database with `geko failures --json`:

```sh
geko failures --json --service orders --status 503 --limit 10
```

The response contains `items` and, when another page exists, `next_cursor`.
Pass that value as `--cursor` with the same filters. Run
`geko failures --help` for all filters.

To connect an MCP client, set its stdio command to the absolute path to
`geko` and its argument list to `["mcp"]`. Set `GEKO_DB_PATH` or
`GEKO_CONFIG` in the client's server environment when using a custom
database. Use `list_failures` for recent failure evidence, `get_trace` for
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
