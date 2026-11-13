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
docker compose up -d --build
```

- **Access token** (`GEKO_ACCESS_TOKEN`): signs in to the dashboard with full
  access. Keep it out of application configuration.
- **Ingest tokens** (`GEKO_INGEST_TOKENS`): one per application, as
  `name=token` pairs. They can only export traces, so a leaked token can't read
  stored data, and you can revoke one application without changing the others.
  **Settings** lists the names, never the values.
- **Read tokens** (`GEKO_READ_TOKENS`): for coding agents, scripts and teammates
  who only look. Teammates sign in to the dashboard with one. They open the dashboard, the query API and `/mcp`, but can't
  change mappings, triage groups or restore traces.
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

Geko checks the file every 5 seconds. A valid change applies to every listener
at once and signs out every browser, so a rotated token also ends sessions that
started with the old one. An invalid file is logged and the current tokens stay
in use. Keep the file readable only by Geko's user.
