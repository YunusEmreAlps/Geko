# Run Geko for a team

Run one Geko for several applications or teammates: in Docker with a token, or on a server behind TLS. [Back to the README](../README.md).

## Docker with a token

The README's `docker run --network host` example uses Geko's default loopback
listener. To use Docker bridge networking and publish a port, configure an access token and an address
reachable inside the container. A non-loopback address also requires
`GEKO_ALLOW_PLAINTEXT=true`, which confirms that only a trusted network or a TLS
proxy can reach it. This example publishes the port on the Docker host's loopback
interface. [Docker port publishing](https://docs.docker.com/engine/network/port-publishing/)

```sh
export GEKO_ACCESS_TOKEN="$(openssl rand -hex 32)"
docker run --rm -p 127.0.0.1:4318:4318 -v geko-data:/data \
  -e GEKO_ACCESS_TOKEN -e GEKO_LISTEN_ADDR=0.0.0.0:4318 -e GEKO_ALLOW_PLAINTEXT=true geko
```

Open <http://127.0.0.1:4318> and sign in with the token. The browser stays signed
in for 7 days, or until Geko restarts; **Sign out** in the sidebar ends the
session. Scripts can send the token as a Bearer token, or as the Basic password
for user `geko`. For OTLP/HTTP and OTLP/gRPC clients, set
`OTEL_EXPORTER_OTLP_TRACES_HEADERS="authorization=Bearer%20${GEKO_ACCESS_TOKEN}"`.
API and health check clients can also send `Authorization: Bearer <token>`.
To receive gRPC through Docker, also set `GEKO_GRPC_ADDR=0.0.0.0:4317` and
publish `127.0.0.1:4317:4317`.

For access from another machine, see [Run on a server](#run-on-a-server) below.

## Run on a server

One Geko instance can receive traces from several of your applications, for
example side projects or dev and staging environments. It is not a production
monitoring system: it stores traces only, keeps them in one SQLite file, and
its [notifications](investigate.md#notifications) report new error groups, not thresholds or
uptime. Production tracing is usually sampled, so a failure in a
dropped trace never reaches Geko.

[`deploy/`](../deploy) contains a Docker Compose setup with Caddy, which obtains a
TLS certificate and serves OTLP/HTTP, OTLP/gRPC and the dashboard on port 443:

```sh
cd deploy
cp .env.example .env   # set GEKO_DOMAIN, GEKO_ACCESS_TOKEN and GEKO_INGEST_TOKENS
docker compose pull geko && docker compose up -d
```

This runs the published image `ghcr.io/yunusemrealps/geko`. Set `GEKO_VERSION`
in `.env` to pin a release, such as `0.1.0`, or run `docker compose up -d --build`
to build the checkout instead.

- **Access token** (`GEKO_ACCESS_TOKEN`): signs in to the dashboard with full
  access. Keep it out of application configuration.
- **Ingest tokens** (`GEKO_INGEST_TOKENS`): one per application, as
  `name=token` pairs. They can only export traces, so a leaked token can't read
  stored data, and you can revoke one application without changing the others.
  **Settings** lists the names, never the values.
- **Read tokens** (`GEKO_READ_TOKENS`): for coding agents, scripts and teammates
  who only look. Teammates sign in to the dashboard with one. They open the dashboard, the query API and `/mcp`, but can't
  change mappings, triage groups or restore traces.
- **Triage tokens** (`GEKO_TRIAGE_TOKENS`): read access plus resolving, muting
  and reopening error groups, for QA engineers and whoever is on call. Give each
  person their own: the token's name is shown as who resolved or muted a group,
  and is recorded in the [audit log](#audit-log).
- **Redaction** defaults to `strict` with an access token, so the server keeps
  exception types and message hashes, not messages or stack traces. Set
  `GEKO_REDACTION=best_effort` to keep them, with e-mail addresses, card
  numbers, IBANs and TCKNs masked. See [Privacy](configuration.md#privacy).
- **Connect** in the sidebar generates the environment variables for a service
  in Bash, PowerShell or Docker Compose. Point each application at the domain
  with its ingest token:

  ```sh
  OTEL_EXPORTER_OTLP_ENDPOINT=https://geko.example.com
  OTEL_EXPORTER_OTLP_HEADERS="authorization=Bearer%20<ingest token>"
  OTEL_RESOURCE_ATTRIBUTES=service.namespace=Storefront
  ```

  The same endpoint works for `http/protobuf` and `grpc`.
- `GEKO_PUBLIC_URL` makes the dashboard show the public trace endpoint instead
  of the container address.
- `GET /healthz` and `GET /readyz` need no token. `/readyz` returns 503 when the
  database doesn't answer. `GET /metrics` serves receiver counters and storage
  sizes in Prometheus format to a read token.
- Rejected credentials are logged with the client address: the first at once,
  then a count per address each minute. Geko doesn't lock addresses out, because
  behind a proxy every client shares the proxy's address; tokens of 32 or more
  random characters can't be guessed online.
- Back up while Geko runs: `docker compose exec geko /geko backup /data/backup.db`,
  then copy the file out with `docker compose cp`.
- Set `GEKO_WEBHOOK_URL` to get a message when an error group first appears or
  a resolved group recurs. See [Notifications](investigate.md#notifications).
- Coding agents reach the server through `/mcp` or `geko failures --server`.
  See [Read traces from coding tools](agents.md).

Without Docker, [`deploy/geko.service`](../deploy/geko.service) runs Geko under
systemd on loopback, behind your own reverse proxy. For any proxy, preserve the
Host and Authorization headers, allow request bodies of at least 5 MB (nginx
defaults to 1 MB), proxy gRPC over HTTP/2, and don't buffer `text/event-stream`
responses.

Tokens set in environment variables take effect at restart. To add or rotate
tokens without a restart, keep them in a file and set `GEKO_TOKENS_FILE` to its
path instead of the three token variables:

```yaml
access: <token>
ingest:
  storefront: <token>
  reporting: <token>
read:
  claude: <token>
```

Add `triage:` with the same `name: <token>` form for triage tokens. Geko checks
the file every 5 seconds. A valid change applies to every listener
at once and signs out every browser, so a rotated token also ends sessions that
started with the old one. An invalid file is logged and the current tokens stay
in use. Keep the file readable only by Geko's user.

## Roles

Every signed-in caller has one of three roles. Each includes the ones before it.

| Role | Who | Can |
| --- | --- | --- |
| `viewer` | Read tokens | Open the dashboard, the query API, `/metrics` and the read-only tools on `/mcp`. |
| `triager` | Triage tokens | Also resolve, mute and reopen error groups, in the dashboard and, with `GEKO_MCP_TRIAGE=true`, through `set_group_state` on `/mcp`. |
| `admin` | The access token | Also map services to applications, restore traces and send test notifications. |

The sidebar shows who is signed in and their role. Pages hide actions the role
can't use, and the server refuses them.

## Sign in through a proxy

Instead of handing out tokens, you can let an authenticating reverse proxy such
as [oauth2-proxy](https://oauth2-proxy.github.io/oauth2-proxy/) sign people in
with your identity provider. The proxy sets a header with the user's name or
e-mail address, and Geko trusts it from the proxy's addresses only:

```sh
GEKO_PROXY_USER_HEADER=X-Auth-Request-Email
GEKO_PROXY_TRUSTED_IPS=172.16.0.0/12          # the proxy's address or network
GEKO_PROXY_ROLES=alice@example.com=admin,qa@example.com=triager
GEKO_PROXY_DEFAULT_ROLE=viewer                 # or none to refuse unlisted users
```

- The proxy must remove any copy of the header a client sends, on every route,
  including routes that skip sign-in. Otherwise anyone who reaches the proxy
  can claim to be any user, because Geko trusts the proxy's address.
- Only the proxy may reach Geko's listeners. In the Compose setup, Geko
  publishes no ports, so only containers on its network reach it.
- A request from a trusted address without the header uses tokens as before,
  so exporters and agents keep using ingest and read tokens. Exclude
  `/v1/traces`, the gRPC path and `/mcp` from the proxy's sign-in, or give
  agents their own route. `/v1/traces` never accepts the header.
- The access token is still required and still works.
- Roles are read at startup; restart Geko after changing them.

With Caddy and oauth2-proxy, the site block could look like this:

```caddyfile
{$GEKO_DOMAIN} {
	# Runs before forward_auth: drop a client's copy of the user header on
	# every route, then forward_auth sets the signed-in user's.
	request_header -X-Auth-Request-Email

	@grpc path /opentelemetry.proto.collector.trace.v1.TraceService/*
	reverse_proxy @grpc h2c://geko:4317

	@token path /v1/traces /mcp /api/* /healthz /readyz
	reverse_proxy @token geko:4318

	handle /oauth2/* {
		reverse_proxy oauth2-proxy:4180
	}
	handle {
		forward_auth oauth2-proxy:4180 {
			uri /oauth2/auth
			copy_headers X-Auth-Request-Email
		}
		reverse_proxy geko:4318
	}
}
```

## Audit log

Set `GEKO_AUDIT_LOG` to a file, or to `-` for standard error, to record each
sign-in, sign-out and change as one JSON line. The Compose setup and the systemd
unit write `audit.jsonl` next to the database.

```json
{"time":"2026-11-14T09:00:00Z","action":"group.state","actor":"qa","role":"triager","via":"session","remote":"172.18.0.3 (forwarded for 198.51.100.7)","target":"<group id>","detail":{"state":"resolved","version":"2.1"},"result":"ok"}
```

| Action | Recorded when |
| --- | --- |
| `sign_in` | Someone signs in to the dashboard. A rejected token has actor `unknown` and result `denied`. |
| `sign_out` | A browser session ends. |
| `group.state` | An error group is resolved, muted or reopened. `detail.client` is `mcp` for changes made through `/mcp`. |
| `applications.mapping`, `applications.mapping-remove`, `applications.assign`, `applications.assign-clear`, `applications.assign-discard` | A service mapping or a historical assignment changes. |
| `trace.restore` | A trace is restored from OTLP JSON. |
| `notifications.test` | A test notification is sent. |

`actor` is a token name, a proxy user, `access` for the access token, or `local`
when Geko runs without a token. The log never contains token values, session
IDs or telemetry text. `remote` comes from the connection and the
`X-Forwarded-For` header, which a client can set, so treat it as a hint. Geko
appends to the file and doesn't rotate it; use logrotate with `copytruncate`, or
send standard error to your log collector.
