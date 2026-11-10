# Architecture

`cmd/geko` owns command dispatch, local diagnostics, and HTTP lifecycle.
`internal/config` loads YAML and environment overrides, then validates the
effective settings and restricts the listener to loopback IPs.
`internal/telemetry` defines the normalized span and event model independently
of wire protocols and storage. `internal/storage/sqlite` owns the database
connection and migrations. Query and ingest interfaces can be defined at their
call sites when those consumers exist.

## SQLite

The pure-Go SQLite driver lets the CLI link SQLite into the executable without
CGO. Geko uses one pooled connection, enables foreign keys on each connection,
and uses WAL on local filesystems. The database is marked with a Geko
application ID. An immediate transaction applies embedded migrations in order,
advancing SQLite's schema version in the same transaction. A newer schema or a
database owned by another application results in an error before journal mode
is changed.

A trace has observation timestamps for future retention queries. Span identity
is scoped to its trace. The timeline index supports ordered reads by trace and
span start time. Parent IDs have no foreign key to another span, allowing a
child to arrive before its parent or remain orphaned. Span timestamps preserve
the recorded values so the investigation layer can account for clock skew.

## OTLP ingestion

`internal/ingest/otlp` decodes OTLP/HTTP protobuf using the official generated
OpenTelemetry types. The handler accepts at most four concurrent requests,
limits wire and decompressed bodies to 4 MiB, and caps each request at 2,048 spans
and each span at 32 events. Overload returns HTTP 503 with `Retry-After`. A batch
with invalid span IDs or timestamps returns HTTP 400 before storage. A batch
that exceeds a size or count limit returns HTTP 413.

The receiver allows a defined set of service, scope, HTTP, and exception
attributes and canonicalizes legacy HTTP method and status keys. Headers,
cookies, body fields, raw URLs, and other attributes outside the allowlists
are discarded. Text is bounded and redacted when it matches recognizable
credential or payload markers. Arbitrary exception messages, stack traces, and
values of allowed attributes can still contain sensitive data without recognizable
markers. Instrumentation should keep secrets out of telemetry.

Each successful non-empty export is committed in one SQLite transaction. A
span is identified by its trace ID and span ID. The first persisted copy wins
on retransmission. Observation times track the earliest and latest accepted
arrival for a trace, including retries. A child may be stored before its parent
or without one. The receiver preserves recorded timestamps, including skewed
timestamps. Automatic retention and disk quotas are planned for a later
milestone.
