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
the failures received while it ran:

```sh
geko run -- go test ./...
geko run -- npm test
```

It sets `OTEL_EXPORTER_OTLP_ENDPOINT`, the protocol, a 500 ms batch delay, and
turns off metric and log exporters, unless the environment already sets them.
If no Geko is listening on `listen_addr`, it starts one for the duration of the
command. The summary goes to stderr, with links and the time window to pass to
`list_failures`, and Geko exits with the command's exit code. The summary comes
from the listening Geko, using `GEKO_ACCESS_TOKEN` when it requires a token. It
says so when Geko received no exports or rejected some during the run, so an
exporter that never reached Geko doesn't look like a clean run. Traces are matched
by receipt time, not by test: the list can include failures from other clients
of the same Geko, and misses spans exported more than two seconds after the
command exits. Only processes that inherit the environment, such as the test
binary and servers it starts, send traces.
