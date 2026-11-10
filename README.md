# Geko

Find the failure. Follow the trace.

Geko is a local-first tool for investigating backend failures from OpenTelemetry
traces. Geko accepts OTLP/HTTP trace exports on loopback and stores sanitized
spans in local SQLite.

## Build and run

Use Go 1.26 or later. Build from the project root.

```sh
go build -o bin/geko ./cmd/geko
./bin/geko version
./bin/geko doctor
./bin/geko serve
```

On Windows, build with `go build -o bin/geko.exe ./cmd/geko` and run
`.\bin\geko.exe` from PowerShell. Use `geko --help` for command help.

`serve` opens the database and exposes `GET /healthz` on the configured listener.
The endpoint returns HTTP 204 after successful startup. Press Ctrl+C to shut down.
`doctor` creates or migrates the database if needed, runs SQLite's quick integrity
check, reports file usage including SQLite sidecars, and tests whether the
listener can be bound. Run it before starting `serve` to check the same port.

## Configuration

Copy [`geko.example.yaml`](geko.example.yaml) to `geko.yaml` in the working
directory. To choose another file, set `GEKO_CONFIG` to its path. An explicitly
selected file must exist. `serve` and `doctor` both use these settings.

Nonempty environment variables override YAML values. If neither source supplies
a nonempty value, Geko uses the defaults below. Unknown YAML keys are rejected.

| YAML key | Environment override | Default |
| --- | --- | --- |
| `listen_addr` | `GEKO_LISTEN_ADDR` | `127.0.0.1:4318`. Use a loopback IP and port, such as `[::1]:4318` for IPv6. |
| `db_path` | `GEKO_DB_PATH` | `geko/geko.db` under the OS user configuration directory. |

A relative `db_path` is resolved from the YAML file's directory. A relative
`GEKO_DB_PATH` is resolved from the working directory.

## Send traces

For an instrumented application with an OpenTelemetry OTLP exporter, set these
environment variables before starting the application.

```sh
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces
export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf
```

In PowerShell, assign these variables with `$env:NAME = "value"`. Geko accepts
both uncompressed and gzip-compressed protobuf requests. The current milestone
stores spans for future query and investigation features.

```sh
go test ./...
go vet ./...
```

[Architecture and storage notes](docs/architecture.md)
