# Architecture

`cmd/geko` owns command dispatch, local diagnostics, and HTTP/gRPC lifecycle.
`internal/config` loads YAML and environment overrides, then validates the
effective settings and requires an access token for non-loopback listeners.
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

`internal/ingest/otlp` decodes OTLP/HTTP protobuf and JSON using the official
generated OpenTelemetry types. The receiver checks the Host header before
processing telemetry. The handler accepts at most four concurrent requests,
limits wire and decompressed bodies to 4 MiB, and caps each request at 2,048
resources, scopes, and spans before decoding. Protobuf preflight traverses the
message schema and caps all nested messages at 131,072 (64 per span at the span
limit), including resource/scope attributes, span attributes, events, links and
nested AnyValue messages. JSON uses a streaming token preflight before allocating
arrays, with at most 131,072 array entries and 1,048,576 values across the
request, including unknown fields.
Both preflights cap nesting at 32. Each span can carry up
to 32 events. Overload returns HTTP 503 with `Retry-After`.
A mixed batch stores its valid spans and returns HTTP 200 with OTLP
`partial_success` and a rejected-span count. A batch with no valid spans
returns an error. Invalid fields produce HTTP 400 and size or count
violations produce HTTP 413.

An optional `grpc_addr` enables OTLP/gRPC with protobuf and gzip. A private
method adapter acquires the same admission slot as HTTP before calling gRPC's
request decoder; its codec runs the protobuf wire preflight before allocating
generated messages. This prevents separate connections from each receiving an
independent decoding budget. The receiver validates loopback `:authority` in
local mode and Bearer credentials in token mode. It shares normalization,
persistence, partial-success responses and counters
with HTTP. Invalid batches return `InvalidArgument` or `ResourceExhausted`;
temporary backpressure returns `Unavailable` with `RetryInfo` (30 seconds at
capacity, one second when busy), following the
[OTLP specification](https://opentelemetry.io/docs/specs/otlp/).

By default the dashboard and query API share `listen_addr`. An optional
`dashboard_addr` moves both to a separate listener. Health checks are available
on both HTTP listeners. Startup binds
all configured listeners before announcing readiness. Any bind failure unwinds
the previously opened listeners. A serve failure or shutdown stops all
transports together, with a five-second grace period. `doctor` probes every
configured port. With `GEKO_ACCESS_TOKEN` set, authentication covers all HTTP
routes and OTLP/gRPC. HTTP mutation requests also pass a cross-origin check.
Token mode supports Docker bridge networking and a TLS-terminating proxy.

The receiver allows a defined set of service, scope, HTTP, and exception
attributes and canonicalizes legacy HTTP method and status keys. Headers,
cookies, body fields, raw URLs, and other attributes outside the allowlists
are discarded. The default allowlist includes database and RPC operation
metadata and URL paths. Stack traces are limited to 32 KiB. Text is bounded
and redacted when it matches recognizable
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
last-received times. Application membership is evaluated from indexed trace/service rows
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

Migration 006 replaces the insert trigger with a materialized one-row exception
lookup. Each new span runs `json_each` once; existing evidence is preserved.

Overview skips evidence lookup when a unique root already supplies its operation
and service. `BenchmarkOverview200KFailures` tracks this path (about 2.28 s before
and 1.62 s after on a Windows i7-8750H development machine; not a latency guarantee).
Migration 007 backfills the unique entry span, primary evidence span and
indexed trace/service membership. Triggers maintain the entry and evidence
spans in the same transaction as the underlying spans. Migration 008 removes the
per-span membership insert trigger: ingest adds one count per trace, namespace
and service in each export, reading the namespace from the stored span's
generated column, and skips retransmitted spans. A delete trigger still
decrements the count per span. Application labels resolve at
read time, so changes to assignments apply immediately. Searches can still
select matching secondary evidence. Overview and grouping still scale with
the number of matching traces.

The request summary uses a unique observed parentless server span when available.
Its HTTP outcome is `server_error` for 5xx, `non_5xx` for other recorded HTTP
statuses, and `unknown` when the entry or status is absent or ambiguous. Geko
updates the summary when late spans arrive. A span with reversed timestamps has
no computed duration.

Concurrent exports can share a SQLite transaction. Up to four writes can be
admitted. The writer starts as soon as a request arrives; requests that arrive
while a commit is in progress form the next shared batch. Acknowledgements
follow the commit. A panic while writing fails only the exports in that batch
and keeps the queue running. If a shared transaction fails, the writer
retries eligible exports individually with the original capacity policy.

`internal/api` serves bounded, cursor-paginated JSON queries. Trace summary and
spans within one response use the same database snapshot. Cursors navigate the
live result set, so a trace updated between requests may move to a newer page
position. The query API requires a loopback Host in local mode and marks responses
as non-cacheable.

## Dashboard

`web` renders HTML on the server and embeds its templates, styles, icon, and
clipboard script into the Go executable. It shares the query and validation
paths with the JSON API. Navigation and filters work without JavaScript.
Telemetry fields are escaped by the HTML templating engine, and the loopback
Host check and content security policy apply to all dashboard routes.

Each trace page reads the header, waterfall, map, evidence, and focused span
in a single read transaction. Trace pages show up to 50 spans against a shared trace-wide time scale. A link
to an evidence or parent span can open its details even when the span is outside
the initial page. Parent links across pages are preserved. Missing parents and
cycles leave the affected rows visible. Settings reports live SQLite metadata,
disk usage, and cumulative capacity counters.

### Error groups and live updates

`/failures?view=groups` aggregates the same filtered trace set as the trace list.
Each trace contributes its selected primary evidence once. Group identity is the
evidence span's effective application, namespace, service, environment, operation,
classification, exception type and HTTP status. Messages, strict-mode hashes and
instance IDs do not split groups. Missing exception types therefore produce a
coarser group. Counts represent retained matching traces, not exports or all
exception events; receipt timestamps come from those traces. Reassignment, late
evidence and retention are reflected at query time. Groups use last receipt/key
pagination and link to both matching traces and a deterministic latest example.
The aggregate still scans matching traces; it shares the three-second query
timeout, so narrower time ranges can be necessary on large databases.

`/events/failures` is an SSE invalidation feed, limited to 16 streams
outside the four dashboard query slots. Requests whose `Sec-Fetch-Site` is
present and neither `same-origin` nor `none` are refused, so other web pages
cannot occupy the streams. It samples an atomic revision every
second, coalesces writes, and sends a ten-second heartbeat with bounded write
deadlines. The feed contains no telemetry. Commits through this Store instance
(ingest, historical assignment, service mapping, retention) advance the revision;
external database writers are not monitored. The revision is not a persistent
counter. Reconnection always requests a fresh snapshot.

On the first page, the client coalesces invalidations over three seconds and
replaces only server-rendered results. Filters remain untouched. Focused results
and text selections defer replacement to an explicit refresh button. Users can
pause updates; hidden tabs disconnect, and older pages remain stable. JavaScript
is optional for manual navigation. Shutdown closes SSE streams before HTTP
graceful shutdown completes.
