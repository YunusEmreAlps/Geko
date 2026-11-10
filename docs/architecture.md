# Architecture

`cmd/geko` owns command dispatch, local diagnostics, and HTTP lifecycle.
`internal/config` resolves settings and restricts the listener to loopback IPs.
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

## Ingestion boundary

The next milestone will decode OTLP requests, validate them, apply privacy
filtering, and map the result into the normalized model before writing to
storage. Credential and body fields should be excluded from the allowlist.
Exception messages, stack traces, and custom attributes can contain secrets
even when their keys are allowed, so their values also need sanitization.
