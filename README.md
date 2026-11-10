# Geko

Find the failure. Follow the trace.

Geko is a local-first tool for investigating backend failures from OpenTelemetry
traces. The M0 foundation provides the CLI, a loopback HTTP service, and local
SQLite storage.

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

| Environment variable | Default | Description |
| --- | --- | --- |
| `GEKO_LISTEN_ADDR` | `127.0.0.1:4318` | A loopback IP and port. IPv6 loopback is accepted as `[::1]:4318`. |
| `GEKO_DB_PATH` | `geko/geko.db` under the OS user configuration directory | Path to a database file on local storage. Relative paths resolve from the working directory. |

```sh
go test ./...
go vet ./...
```

[Architecture and storage notes](docs/architecture.md)
