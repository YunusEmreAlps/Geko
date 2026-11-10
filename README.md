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

```sh
go run ./cmd/geko serve
```

`serve` opens the database and exposes `GET /healthz` on the configured listener.
The endpoint returns HTTP 204 after successful startup. Press Ctrl+C to shut down.
`doctor` creates or migrates the database if needed, runs SQLite's quick integrity
check, reports file usage including SQLite sidecars, and tests whether the
listener can be bound. Run it before starting `serve` to check the same port.

## Open the dashboard

After starting `geko serve`, open [http://127.0.0.1:4318](http://127.0.0.1:4318)
or use the configured loopback address. The dashboard opens on **Failures**.
Filter the list and select an operation to open the span that contains the
observed evidence. **Overview** summarizes the last 24 hours, and **Settings**
shows the effective receiver and storage status.

Trace details include a service map built from observed parent-child spans.
Select a service to inspect its evidence, or expand the waterfall for timing.
The map covers the current trace, with an explicit 500-span limit, rather than
an aggregate view of all services. Try the
[synthetic multi-service demo](examples/service-map/README.md) to explore it.

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
both uncompressed and gzip-compressed protobuf requests.

To try the investigation flow with generated telemetry, follow the
[Go checkout example](examples/go/README.md). It demonstrates an HTTP 500 and
a failed payment attempt followed by an HTTP 200 recovery in the same trace.

## Query traces

On the same loopback listener, `GET /api/v1/failures` returns traces with an
observed failure signal. `GET /api/v1/traces/{trace_id}` returns the trace summary
and a page of sanitized spans. Pass the returned `next_cursor` as `cursor` to
continue either query. Both endpoints accept `limit` from 1 to 100 (default 50).

The failure list also accepts these filters:

| Parameter | Meaning |
| --- | --- |
| `service` | Exact entry span service, or evidence span service when there is no unique entry span. |
| `status` | HTTP status observed on the entry span. |
| `q` | Trace ID prefix or substring in the entry/evidence service, operation, or exception message. ASCII matching ignores case. |
| `from`, `to` | RFC3339 range for the trace's last observation, inclusive at the start and exclusive at the end. |

The entry HTTP outcome and the selected evidence span are separate fields.
For example, a request can return HTTP 200 while its trace contains a retry
that recorded an exception. A trace can change as more spans arrive.

```sh
go test ./...
go vet ./...
```

[Architecture and storage notes](docs/architecture.md)
