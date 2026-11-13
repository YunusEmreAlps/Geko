<div align="center">
  <img src="web/assets/geko.svg" alt="Geko" width="72" height="72">
  <h1>Geko</h1>
  <p><strong>Find the failure. Follow the trace.</strong></p>
  <p>A local, failure-first dashboard for OpenTelemetry traces. One Go binary, SQLite storage, no Docker or external services required.</p>
  <p>
    <a href="https://github.com/YunusEmreAlps/geko/actions/workflows/ci.yml"><img src="https://github.com/YunusEmreAlps/geko/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
    <a href="https://github.com/YunusEmreAlps/geko/releases"><img src="https://img.shields.io/github/v/release/YunusEmreAlps/geko?include_prereleases&sort=semver" alt="Release"></a>
    <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/YunusEmreAlps/geko" alt="Go version"></a>
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT License"></a>
  </p>
  <p>
    <a href="#quick-start">Quick start</a> ·
    <a href="#connect-your-application">Connect your app</a> ·
    <a href="docs/investigate.md">Investigate</a> ·
    <a href="docs/server.md">Run for a team</a> ·
    <a href="docs/configuration.md">Configuration</a> ·
    <a href="#how-geko-compares">Compare</a>
  </p>
</div>

![Geko Failures dashboard showing errors from a reporting API and an online store](docs/images/failures.jpg)

*Screenshots use synthetic traces from a single-service reporting API and an
online store with separate order, inventory, and payment services.*

## Why Geko

- **Starts with what broke.** The first screen lists requests whose spans
  recorded an exception, an HTTP 5xx, or an error status, not every trace you sent.
- **Keeps outcome and evidence apart.** A checkout that returns HTTP 200 after a
  retried payment timeout still shows the payment exception next to the
  successful response.
- **Runs entirely on your machine.** A single process receives OTLP/HTTP and optional OTLP/gRPC,
  stores spans in SQLite with retention and a size limit, and serves the
  dashboard on a loopback address by default.
- **Is careful with data.** An attribute allowlist drops headers, cookies,
  bodies, and full URLs before anything is stored. A `strict` mode keeps only
  exception types and message hashes.

## Quick start

Requires Go 1.26.11 or later. Start Geko:

```sh
git clone https://github.com/YunusEmreAlps/geko.git
cd geko
go run ./cmd/geko serve
```

Open <http://127.0.0.1:4318>. In a second terminal, send a synthetic
multi-service trace from the repository root:

```sh
# bash / zsh
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces go run ./examples/service-map
```

```powershell
# PowerShell
$env:OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = 'http://127.0.0.1:4318/v1/traces'
go run ./examples/service-map
```

Refresh **Failures** and open **POST /checkout**. More examples:

| Example | What it sends |
| --- | --- |
| [Go checkout](examples/go/README.md) | Real HTTP requests through the OpenTelemetry SDK, with success, failure, and retry routes. |
| [OTLP/HTTP JSON](examples/otlp-json/README.md) | A synthetic HTTP 503 trace from Node.js, Python, Java, or .NET, using only the standard library. |
| [Service map](examples/service-map/README.md) | One synthetic trace across five services, for exploring the map and waterfall. |

## Install

| Method | Command |
| --- | --- |
| Release binary | Download the archive for your platform from [GitHub Releases](https://github.com/YunusEmreAlps/geko/releases), extract it, and run `./geko serve` (`.\geko.exe serve` on Windows). |
| Go | `go install github.com/YunusEmreAlps/geko/cmd/geko@latest`, then `geko serve`. Replace `latest` with a tag to pin a version. |
| Docker | `docker build -t geko .` then `docker run --rm --network host -v geko-data:/data geko` |

To run Geko for a team, in Docker with a token or on a server behind TLS, see
[Run Geko for a team](docs/server.md).

## Connect your application

Point your OpenTelemetry trace exporter at Geko. Your application keeps its
own port; Geko listens on 4318.

```sh
# bash / zsh
export OTEL_SERVICE_NAME=orders-api
export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces
```

```powershell
# PowerShell
$env:OTEL_SERVICE_NAME = 'orders-api'
$env:OTEL_EXPORTER_OTLP_TRACES_PROTOCOL = 'http/protobuf'
$env:OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = 'http://127.0.0.1:4318/v1/traces'
```

Send a request to your application, then open **Settings → Receiver**. It shows
the effective trace endpoint, the last accepted export, and rejection counts by
reason.

OTLP/gRPC, a separate dashboard port and receiver limits are in
[Connect your application](docs/connect.md).

## Documentation

| Guide | Covers |
| --- | --- |
| [Investigate a failure](docs/investigate.md) | Failures, the service map and waterfall, applications, comparing with a success, version regressions, error groups and triage, notifications, moving a trace |
| [Run Geko for a team](docs/server.md) | Docker with a token, Compose with Caddy, access, ingest and read tokens, sign-in, token reload |
| [Coding agents and test runs](docs/agents.md) | `geko failures --json`, MCP over stdio and HTTP, `geko run` |
| [Connect your application](docs/connect.md) | Ports, OTLP/gRPC, receiver limits |
| [Configuration and reference](docs/configuration.md) | Every setting, retention and capacity, privacy modes, upgrades, CLI, JSON API |
| [Architecture](docs/architecture.md) | Ingestion, storage, privacy filtering, evidence selection |

## How Geko compares

| Tool | Best for | Deployment |
| --- | --- | --- |
| **Geko** | Starting from failure evidence on a developer machine or a shared dev/staging instance: filter failures, open the evidence span, follow the trace's calls. | Single binary, loopback by default, SQLite with retention and a size limit. Traces only. |
| [Jaeger](https://www.jaegertracing.io/docs/2.23/features/) | Searching and inspecting traces and service dependencies at any scale. | All-in-one or separate collector and query roles with external storage. |
| [Zipkin](https://zipkin.io/) | Looking up traces by ID, service, operation, tags, and duration, with an aggregate dependency diagram. | In-memory or persistent backends such as Cassandra and Elasticsearch. |
| [OpenTelemetry Desktop Viewer](https://github.com/CtrlSpice/otel-desktop-viewer) | A local telemetry UI with SQL access. | Local CLI with a browser UI. |
| [otel-tui](https://github.com/ymtdzzz/otel-tui) | Browsing telemetry in a terminal UI. | Local terminal tool. |
| [Aspire Dashboard](https://aspire.dev/dashboard/standalone/) | Logs, traces, and metrics in one local dashboard. | Standalone container. |

## Development

```sh
go test ./...
go vet ./...
go test ./internal/ingest/otlp -run '^$' -fuzz '^FuzzReceiver$' -fuzztime 30s
go test ./internal/storage/sqlite -run '^$' -bench .
```

CI also runs staticcheck, govulncheck, race tests, `go mod tidy -diff`, and a
Docker smoke test with the JSON exporters. CI also builds a release snapshot,
verifies the archive checksums, and runs the Linux amd64 binary. Run
`goreleaser release --snapshot --clean` locally to generate the same archives
in `dist/`. Pushing a `v*` tag publishes checksummed archives for Linux, macOS,
and Windows on amd64 and arm64.

## License

[MIT](LICENSE)
