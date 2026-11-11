# Architecture

`cmd/geko` owns command dispatch, local diagnostics, and HTTP lifecycle.
`internal/config` loads YAML and environment overrides, then validates the
effective settings and restricts the listener to loopback IPs.
`internal/telemetry` defines the normalized span and event model independently
of wire protocols and storage. `internal/storage/sqlite` owns the database
connections and migrations. The ingest and query packages define the storage
interfaces they consume.

## SQLite

The pure-Go SQLite driver lets the CLI link SQLite into the executable without
CGO. Geko uses one writer with foreign keys enabled and a separate read-only
pool for queries. WAL lets readers inspect committed data during writes.
The database is marked with a Geko
application ID. An immediate transaction applies embedded migrations in order,
advancing SQLite's schema version in the same transaction. A newer schema or a
database owned by another application results in an error before journal mode
is changed.

A trace has observation timestamps for retention and capacity eviction. Span identity
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
Strict mode stores only exception type and a SHA-256 message digest on new
exception events, and omits free-text span status descriptions. The evidence
index reads the persisted fields. Changing modes applies to new spans.

Each successful non-empty export is committed in one SQLite transaction. A
span is identified by its trace ID and span ID. The first persisted copy wins
on retransmission. Observation times track the earliest and latest accepted
arrival for a trace, including retries. A child may be stored before its parent
or without one. The receiver preserves recorded timestamps, including skewed
timestamps. Retention uses the latest accepted arrival time. The server prunes
expired traces at startup and every minute, in batches of whole traces. Foreign
key cascades remove their spans and evidence. Expiry makes pages reusable.

Ingestion sets SQLite's page limit on the writer connection. The default
capacity policy retries with the oldest eligible whole traces removed in the
same transaction as the export. A failed retry rolls back both writes and
eviction. The reject policy returns HTTP 503 at capacity. Both counters are
kept in the database. Retention attempts to truncate the WAL after deleting
traces or when the WAL reaches 4 MiB. A reader can defer truncation. A failed
startup retention pass is logged and retried during periodic maintenance.
The main file can retain freed pages until an
explicit `geko compact`. A transaction and sidecar files can use additional
disk space beyond the main file limit.

Historical application assignment commits up to 225 trace IDs per transaction.
The last committed ID and change count are stored with the form inputs so a
retry can resume. Every batch evaluates eligibility against the current
last-received times. Application membership is evaluated from grouped spans
when querying. The application filter benchmark measures that query at
different database sizes and match rates.

## Failure evidence and queries

An exception event, HTTP 5xx response, or explicit error status makes a span
eligible as evidence. A plain 4xx response does not trigger evidence. SQLite
indexes the eligible spans and the affected traces in the same transaction as
ingestion. The second migration backfills existing spans with the same extraction
rules. Startup waits for the migration to finish, and Ctrl+C can cancel it.
Results select one evidence span per trace in priority order (exception,
5xx, span error), then by recorded start time and span ID. Evidence is an observed
signal, and the priority expresses which evidence to show first.

The request summary uses a unique observed parentless server span when available.
Its HTTP outcome is `server_error` for 5xx, `non_5xx` for other recorded HTTP
statuses, and `unknown` when the entry or status is absent or ambiguous. Geko
shows observed evidence immediately and re-evaluates the summary when queried
after late arrivals. A span with reversed timestamps has no computed duration.

`internal/api` serves bounded, cursor-paginated JSON queries. Trace summary and
spans within one response use the same database snapshot. Cursors navigate the
live result set, so a trace updated between requests may move to a newer page
position. The query API restricts the HTTP Host to loopback and marks responses
as non-cacheable.

## Dashboard

`web` renders HTML on the server and embeds its templates, styles, icon, and
clipboard script into the Go executable. It shares the query and validation
paths with the JSON API. Navigation and filters work without JavaScript.
Telemetry fields are escaped by the HTML templating engine, and the loopback
Host check and content security policy apply to all dashboard routes.

Trace pages show up to 50 spans against a shared trace-wide time scale. A link
to an evidence or parent span can open its details even when the span is outside
the initial page. Parent links across pages are preserved. Missing parents and
cycles leave the affected rows visible. Settings reports live SQLite metadata,
disk usage, and cumulative capacity counters.
