# Competitive research and product focus

Initial review on 2026-11-11 used official documentation and the Geko source.
The resource measurement below was run separately. The usability study is pending.
A missing documentation entry is not evidence that a competitor lacks a feature.

## Verified observations

- Jaeger already offers a single binary, embedded Badger storage, OTLP and service
  graphs. Simple local deployment and graph rendering alone are not differentiators.
  https://www.jaegertracing.io/download/
  https://www.jaegertracing.io/docs/2.23/features/
- Zipkin supports local Java/Docker startup, trace search and dependency diagrams.
  Its documented native persistent storage choices include Cassandra,
  Elasticsearch and MySQL. This is a deployment tradeoff, not an inability to run locally.
  https://zipkin.io/pages/quickstart.html
  https://zipkin.io/pages/architecture.html
- Tempo supports powerful TraceQL investigations. Its metrics-generator service
  graphs use a Prometheus-compatible metrics backend. This adds components to that
  workflow; it does not imply every Tempo query needs Prometheus.
  https://grafana.com/docs/tempo/latest/metrics-from-traces/
- SigNoz already groups exceptions and connects them to traces. Its documentation
  explicitly warns about variable exception messages fragmenting groups, and
  explains that its Exceptions view consumes span events rather than log-only
  exceptions. Self-hosted users can change grouping configuration.
  https://signoz.io/docs/userguide/exceptions/
  https://signoz.io/docs/install/docker/
- Sentry is an adjacent error-investigation competitor with mature grouping.
  Its documented self-hosted installation has substantially more infrastructure
  than a local SQLite application. Do not claim Geko resource superiority until measured.
  https://github.com/getsentry/develop/blob/master/src/docs/self-hosted/index.mdx

## Focus hypothesis

Target backend developers investigating a reproduced failure in a local or
isolated development environment. Present request outcome, observed error
signals and missing telemetry separately. The competitive hypothesis is fewer
steps and fewer misinterpretations, not exclusive ownership of these features.

## Priorities

### P0: trustworthy investigation

- Preserve the current distinction between entry HTTP outcome and child evidence.
- Show all observed evidence candidates, not only the current ranked candidate.
- Provide an evidence path using observed parent links, never label it proven causality.
- Mark missing parents, ambiguous roots, late arrivals and truncated graph scope.
- Do not label HTTP 200 + child exception as a confirmed retry recovery without
  explicit supporting instrumentation. Say "successful request with error evidence".
- Explain why each span appears: exception event, ERROR status, or HTTP 5xx.

Acceptance: fixtures for HTTP 500, HTTP 200 with child errors, multiple independent
errors, missing parents and late spans; no invented root cause or completion claim.
The evidence outside the first 500 spans must still be reachable.

### P0: predictable local operation

Implement default seven-day retention, configurable storage bounds and receiver
health diagnostics. Show last received span and accepted/rejected counts without
logging payloads. Empty state must distinguish no received data from no matching
failure evidence. Received telemetry remains untrusted; do not promise detection
of failures absent from the collected spans.

### P1: explainable issue grouping

Group recurring evidence with a versioned fingerprint based on service, operation,
exception type and appropriate sanitized frames. Treat message normalization as a
controlled rule, not unrestricted removal of all numbers. Show grouping reason,
occurrences, first/last seen, and representative traces. Test distinct problems
with similar messages as well as one problem with different IDs. Sentry already
provides sophisticated grouping; our opportunity is the local workflow combination.

### P1: privacy visibility

Expose aggregate counts of dropped or scrubbed fields and documented policy.
Never retain raw values for a "before/after" preview. Arbitrary exception text is
not guaranteed to be secret-free. Safe sharing/export is a later separately
reviewed feature, not part of this implementation commitment.

## Validation before positioning claims

Use the same instrumented Go and Spring Boot scenarios across tools. Include
500-span traces, incomplete traces, repeated message IDs and successful requests
with failed child operations. Measure setup time, time to relevant evidence,
incorrect conclusions, steps taken, memory and disk growth. Suggested initial
usability target: at least four of five developers reach the right evidence in
under 60 seconds on the agreed fixtures. This is a target, not an achieved result.

### Resource comparison

Run the containers on the same Docker host with no other test workloads
running. Build Geko from the revision you want to measure, then run:

```sh
docker build -t geko:local-comparison .
docker pull cr.jaegertracing.io/jaegertracing/jaeger:2.23.0
python scripts/compare-resources.py
```

Add persistent Jaeger and Tempo profiles with:

```sh
docker pull grafana/tempo:3.0.0
python scripts/compare-resources.py --batches 128 --rounds 3 --jaeger-badger --tempo grafana/tempo:3.0.0
```

Each round uses fresh containers and rotates the backend order. Each
backend receives the same sequential OTLP/HTTP JSON exports, with 64
synthetic two-span traces per request. The fixture records HTTP 503 so
Geko also indexes failure evidence. The script verifies both spans of the last
trace and of 16 evenly spaced trace IDs. It records export and trace
lookup latency, Docker's idle and post-ingest memory readings, and the
change in the writable layer between those snapshots.

It also times 16 searches for 20 failing traces with each backend's own
query: Geko's `/api/v1/failures`, Jaeger's `/api/v3/traces` with
`error=true`, and Tempo's TraceQL `{status=error}`. Every returned trace
must be one the workload made fail, and the search must return 20 of them,
or the run stops. The responses differ: Geko returns trace summaries,
Jaeger full traces and Tempo search metadata. By default every trace fails;
`--failure-every N` makes only every Nth trace fail with HTTP 503 and the
rest succeed with HTTP 200, so the search has to select.

The profiles use SQLite, Jaeger in-memory storage, [Jaeger Badger
storage](https://www.jaegertracing.io/docs/2.23/storage/badger/), and
[Tempo monolithic local storage](https://grafana.com/docs/tempo/latest/set-up-for-tracing/setup-tempo/deploy/locally/linux/).
Tempo is measured as a standalone backend. Memory readings are snapshots.
Writable-layer growth measures file footprint at that point in the run.
Batching and persistence semantics affect the meaning of export
acknowledgement time across profiles.

#### Three-round run on 2026-11-20

The same host and method as the 2026-11-17 run below, repeated against the
working tree after the ingest and application-filter fixes. Docker Desktop for
Windows, Engine 28.5.1. Each profile received 8,192 traces (16,384 spans) per
round over 128 exports, followed by 16 timed trace lookups. Medians across
three rounds. [Raw measurements and image digests](benchmarks/2026-11-20-otlp-http.json)

| Profile | Idle memory | Memory after ingest | Writable layer growth | Export median | Export p95 | Trace lookup median |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Geko, SQLite | 11.99 MiB | 23.12 MiB | 9.61 MiB | 34.29 ms | 59.59 ms | 3.28 ms |
| Jaeger 2.23.0, in memory | 18.80 MiB | 40.98 MiB | 0 bytes | 3.33 ms | 24.56 ms | 2.93 ms |
| Jaeger 2.23.0, Badger | 20.24 MiB | 127.80 MiB | 0 bytes | 3.68 ms | 26.07 ms | 3.38 ms |
| Tempo 3.0.0, local | 30.73 MiB | 47.35 MiB | 4 KiB | 4.03 ms | 24.74 ms | 7.10 ms |

The picture is the one the earlier run showed, with three days of changes in
between and no drift: Geko holds the smallest memory snapshot after ingest, at
56% of Jaeger in memory, 18% of Jaeger on Badger and 49% of Tempo, and it
acknowledges an export roughly nine times slower, because it returns after the
SQLite commit that also indexes the failure evidence. Trace lookups are within
noise of both Jaeger profiles and about half of Tempo's.

The two profiles that report no writable-layer growth keep their data outside
that layer, so the 9.61 MiB Geko reports for 16,384 spans is not evidence that
it stores more. The comparable figure is Geko's own 589 bytes per span,
measured below.

#### Three-round run on 2026-11-17

Docker Desktop for Windows, Engine 28.5.1, with 12 logical CPUs and 15.55 GiB
available to Docker. Each profile received 8,192 traces (16,384 spans) per
round. Geko was built from the working tree with its optional Budget
tracker disabled. The following figures are medians across three rounds.
For each round, the script measured 128 exports and 16 timed trace lookups.

| Profile | Idle memory | Memory after ingest | Writable layer growth | Export median | Export p95 | Trace lookup median |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Geko, SQLite | 11.86 MiB | 22.52 MiB | 9.61 MiB | 36.21 ms | 59.29 ms | 4.46 ms |
| Jaeger 2.23.0, in memory | 21.15 MiB | 42.88 MiB | 0 bytes | 3.74 ms | 19.70 ms | 3.00 ms |
| Jaeger 2.23.0, Badger | 18.66 MiB | 127.90 MiB | 0 bytes | 4.49 ms | 27.18 ms | 2.94 ms |
| Tempo 3.0.0, local | 30.62 MiB | 47.50 MiB | 4 KiB | 4.52 ms | 22.39 ms | 5.56 ms |

Geko had the lowest memory snapshot and the longest export response time
in this workload. Its response follows the SQLite write and indexing of
failure evidence. The other profiles acknowledged exports at different
points in their storage pipelines. Neither the short-run writable-layer
growth nor the trace lookups establish the final on-disk footprint after
buffer flush or recovery after restart. Geko's per-round median lookup
ranged from 3.63 to 14.64 ms on this host.

[Raw measurements and image digests](benchmarks/2026-11-17-otlp-http.json)

#### SQLite ingest optimization

The Windows `BenchmarkWriteSpans` CPU profile attributed 58% of samples to
batch insertion and 24% to commit. The optimized image batches trace and
service projection updates and bypasses exception lookup on spans without
events. The receiver still waits for commit before acknowledging each export.

Three paired Docker rounds on the same host used 128 exports and 16,384
spans per image per round. The order alternated between baseline and
optimized images.

| Metric | Baseline | Optimized |
| --- | ---: | ---: |
| Export median | 32.40 ms | 30.20 ms |
| Export p95 | 55.00 ms | 50.24 ms |
| Trace lookup median | 3.17 ms | 3.54 ms |
| Memory after ingest | 21.05 MiB | 23.18 MiB |

The Windows storage microbenchmark went from 6,512 to 5,399 allocations per
100-span export (median of three runs). [Raw paired measurements](benchmarks/2026-11-17-ingest-optimization.json)

#### Earlier baseline

An earlier baseline on Docker Desktop for Windows (Engine 28.5.1) on 2026-11-17
accepted 4,096 spans in both tools and retrieved the last trace from each.

| Configuration | Idle memory | Memory after ingest | Writable layer growth | Median export response |
| --- | ---: | ---: | ---: | ---: |
| Geko, SQLite | 11.93 MiB | 18.59 MiB | 4,481,024 bytes | 34.47 ms |
| Jaeger 2.23.0, in memory | 20.73 MiB | 25.36 MiB | 0 bytes | 4.01 ms |

The local Geko image ID started with `sha256:aea24dbe21d4` and the Jaeger
image ID with `sha256:3823829974cc`. These readings describe one run
with the two storage configurations above.

### Single-process measurement

The Docker comparison above answers how Geko compares. This section answers
what one Geko process does on its own, without Docker and without another
backend, so a regression in Geko shows up on its own.

```sh
go build -o bin/geko ./cmd/geko
python scripts/bench-native.py --binary bin/geko --rounds 4 --out run.json
python scripts/bench-ingest-concurrency.py --binary bin/geko --out concurrency.json
```

#### Four-round run on 2026-11-20

Windows 11, Intel Core i7-8750H, 12 logical CPUs, no other test workload. Each
round started a fresh server with an empty database and ingested 34,816 spans,
every trace recording HTTP 503 and an error status. Query figures are the mean
of 100 requests over one reused connection, after a discarded first request, so
they exclude connection setup. Figures below are medians across four rounds.
[Raw measurements](benchmarks/2026-11-20-native-e2e.json)

| Measurement | Median of four rounds |
| --- | ---: |
| Resident memory, idle | 20.28 MiB |
| Resident memory, after 34,816 spans | 34.20 MiB |
| Database growth per span, including WAL | 586 bytes |
| Export acknowledgement, one connection | 33.43 ms |
| Export acknowledgement, p95 | 51.55 ms |
| Export acknowledgement, new connection each time | 38.25 ms |
| Accepted spans per second, one connection | 3,708 |
| Accepted spans per second, four connections | 3,482 |
| Exports refused with HTTP 503 | 0 |
| `GET /api/v1/failures?limit=50` | 3.18 ms |
| `GET /api/v1/failures?q=inventory` | 3.37 ms |
| `GET /api/v1/traces/{id}` | 1.72 ms |
| Failures page at 17,408 failing traces | 3.50 ms |
| Overview page at 17,408 failing traces | 88.91 ms |

Export acknowledgement follows a durable SQLite commit, which is why it stays
an order of magnitude above the backends that acknowledge earlier in their
pipelines, and it is the one figure here that a change has yet to address.

This host's run-to-run spread reaches 30% on identical code, so a figure here
is not comparable with one from another run on a different day. Comparing two
builds needs the paired run below.

#### Paired run of the Failures page fix on 2026-11-20

One round per invocation, alternating the binary built at `51d64c4` with the
working tree, twice each, so both builds saw the same host state. Medians of
the two rounds per build.
[Raw measurements](benchmarks/2026-11-20-failures-page-fix.json)

| Measurement | Before | After |
| --- | ---: | ---: |
| Failures page at 17,408 failing traces | 247.75 ms | 2.52 ms |
| Failures page, per round | 70.76 and 424.75 ms | 2.26 and 2.78 ms |
| Export acknowledgement, one connection | 25.30 ms | 25.05 ms |
| Accepted spans per second, four connections | 5,028 | 5,040 |
| `GET /api/v1/failures?limit=50` | 1.58 ms | 1.96 ms |
| Overview page | 98.96 ms | 102.60 ms |
| Database growth per span | 587 bytes | 590 bytes |

The Failures page no longer reads every stored span to list the applications
its filter offers, so it is 98 times faster here and no longer varies by a
factor of six between rounds. Every other figure is inside this host's spread:
the change touches the application queries and the admission path, not ingest
or trace reads.

#### Paired run of the Overview bound on 2026-11-20

The same alternating method, against the build before and after Overview stopped
describing every failing trace in the window. This fixture holds 17,408 failing
traces, above the 10,000-trace breakdown sample, so the page reports that it
sampled. [Raw measurements](benchmarks/2026-11-20-overview-bound.json)

| Measurement | Before | After |
| --- | ---: | ---: |
| Overview page at 17,408 failing traces | 123.08 ms | 89.38 ms |
| Overview page, per round | 123.18 and 122.97 ms | 92.61 and 86.16 ms |
| `BenchmarkOverview200KFailures` | 941.28 ms | 84.00 ms |
| Failures page | 3.01 ms | 3.58 ms |
| Database growth per span | 587 bytes | 586 bytes |

The dashboard window gains less than the benchmark because 17,408 traces is
only 74% above the sample, while 200,000 is twenty times it. The trace count
stays exact in both: it comes from `traces_recent_failures` without reading a
row, which costs 25 ms over 200,000 failing traces.

#### Storage microbenchmarks on 2026-11-20

The same host, `go test -run '^$' -bench . -benchmem -benchtime 5x -count 3
./internal/storage/sqlite ./internal/ingest/otlp`. Medians of three runs.
[Raw output](benchmarks/2026-11-20-storage-microbenchmarks.txt)

| Benchmark | Median | Note |
| --- | ---: | --- |
| `WriteSpans` | 6.87 ms | 100 spans per export, 14,555 spans per second in process |
| `Failures` | 4.79 ms | 10,000 traces, 2,500 failing |
| `FailureGroups` | 102.48 ms | the same store, grouped |
| `TraceSummary200KSpans` | 4.69 ms | one trace out of 200,000 spans |
| `Overview200KFailures` | 78.87 ms | 941.28 ms before the breakdown was bounded |
| `ApplicationNames` | 3.76 ms | the filter list over 50,000 spans, setup included |
| `ApplicationFailureFilter/100000/Storefront` | 2.17 ms | a matching application |
| `ApplicationFailureFilter/100000/Reporting` | 26.07 ms | an application on 1% of traces |
| `ApplicationFailureFilter/100000/Missing` | 0.16 ms | 340.60 ms before the indexed lookup |
| `RejectAmplifiedPayload/json` | 2.17 ms | a 10 MiB payload refused |
| `RejectAmplifiedPayload/protobuf` | 5.55 ms | the same payload as protobuf |

In-process writes reach 14,555 spans per second against roughly 5,000 over
OTLP/HTTP in the paired runs above, so most of the cost of an export is HTTP
handling, JSON decoding and redaction rather than storage.

Three rows replace earlier figures, and two of the three come from a change
rather than from the host: Overview no longer describes every failing trace in
the window, and an application with no traces is answered by an indexed lookup.
The filter list behind `ApplicationNames`, measured as a matched pair at
`-benchtime 50x -count 3` over the same 50,000-span store so that setup is
amortized, went from 64.76 ms and 3,236 allocations per call to 0.43 ms and 28:
a factor of 151. `FailureGroups` is the remaining query whose cost follows the
whole window; the paired run of 2026-11-21 below halves it.

#### Ingest concurrency on 2026-11-20

The same host, one fresh server per level, 32 exports per connection after 32
warm-up exports. The receiver keeps `ingest_concurrency` exports in flight and
answers the rest with HTTP 503 and `Retry-After: 1`, which an OTLP exporter
retries. [Raw measurements](benchmarks/2026-11-20-ingest-concurrency.json)

| Exporters | Accepted spans/s | Exports accepted | Refused with 503, before | Refused with 503, after |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 3,511 | 32 | 0 | 0 |
| 2 | 4,895 | 64 | 0 | 0 |
| 4 | 5,499 | 128 | 0 | 0 |
| 8 | 4,838 | 256 | 128 | 0 |
| 16 | 4,765 | 512 | 397 | 0 |

Accepted throughput is flat between one and sixteen exporters, so the limit is
the single SQLite writer committing once per export, not the number of ingest
slots. The four default slots were refusing exports they did not need to
refuse: eight exporters lost half of their 256 exports and sixteen lost 397 of
512, while raising `GEKO_INGEST_CONCURRENCY` to sixteen refused nothing at the
same throughput. The receiver now waits up to two seconds for a slot before it
refuses, and the same sweep accepts every export at every level. An export that
waits holds no payload, so the receiver's memory bound is unchanged.

#### Paired run of the older query paths on 2026-11-21

Paths that predate the fixes above, measured against the commit before they
changed (`188caf3`) with before and after runs alternating, two rounds each
and three more for writes at the size limit. Means of the rounds.
[Raw output](benchmarks/2026-11-21-legacy-paths.txt)

| Path | Fixture | Before | After | Change |
| --- | --- | ---: | ---: | ---: |
| Version comparison | 100,000 traces in two versions | 854.04 ms | 125.94 ms | 6.8x |
| Trace comparison, no successful match | the same store | 450.84 ms | 6.76 ms | 67x |
| Trace comparison, a recent match | the same store | 2.07 ms | 2.33 ms | unchanged |
| Error groups | 10,000 traces, 2,500 failing | 86.90 ms | 39.14 ms | 2.2x |
| Overview | 200,000 failing traces | 72.30 ms | 54.29 ms | 1.3x |
| Export of 100 spans at the size limit, `evict_oldest` | 16 MiB limit | 57 ms median | 9.9 ms median | 5.8x |
| The same, `evict_fair` | 16 MiB limit | 161 ms median | 12.7 ms median | 13x |
| Webhook check with nothing new | 20,000 failing traces | 196–205 ms | 2.4–2.9 ms | 70x |
| `WriteSpans` | 100 spans per export, in one binary | 7.26 ms | 6.96 ms | unchanged |

What each change does:

- **Readers map the database.** Each read connection maps up to 256 MiB of the
  main file, so pages come from the OS cache without a read call each; on
  Windows those calls were a third of the cost of grouping. The pool also keeps
  its four connections instead of closing two after each burst. Windows cannot
  shrink a mapped file and SQLite then skips the truncation silently, so
  `geko compact` releases the maps first and reports another process holding
  the file instead of leaving it unshrunk.
- **Version comparison reads the newest 10,000 traces of each version.** The
  entry span's `service.version` is projected onto the trace and indexed with
  the receipt time, so the query reads two bounded index ranges instead of
  every trace in a window that can reach seven days. Per-row cost does not
  change: a narrower table and a covering index were both measured and neither
  helped, so the gain comes from reading fewer rows, and the page says when a
  version was sampled.
- **Trace comparison looks the operation up.** It still scans the newest
  successful traces first, but only the newest 1,000; a match there is the
  newest match overall. Without one it reads the entry spans of that one
  operation through a new partial index, instead of every older success.
- **Error grouping skips joins it never used.** Grouping no longer joins the
  pin table or the entry span's application mapping unless a filter needs
  them, and finds reopened groups in the same pass that groups the traces.
- **A full database evicts once per export.** An export at the size limit
  first failed a write it could not fit, then evicted 1, 2, 4, 8 traces,
  rewriting the whole export after each. It now starts with as many traces as
  the export holds and skips the doomed first write while the free pages are
  too few. `evict_fair` also looked up the largest owner with a scan of every
  row of `trace_services` on each attempt; it now reuses the answer for up to a
  second, and looks it up again as soon as that owner has nothing left to
  evict.
- **Webhook checks group only new failures.** A check grouped every stored
  failure every 30 seconds to find each group's first receipt. It now groups
  the failures since the previous check, and groups everything only when one of
  them belongs to a group no full check has seen, or reopens a group
  unannounced; otherwise no event is possible.

Ingest cost was measured inside one test binary, against a store with the new
index and trigger removed, because the host's spread between runs (6 to 15 ms
for the same build) exceeded any difference: medians of six rounds were
7.26 ms without and 6.96 ms with them. Error grouping still reads every failing
trace in its window, at about 16 microseconds each. The two schema 15 indexes do
cost disk: the main file of the four-round end-to-end fixture, two spans per
trace, grew from 17.7 to 19.7 MiB, about 55 bytes per span; traces with more
spans per entry span pay proportionally less.

#### Primary-evidence projection on 2026-11-21

Schema 16 stores compact primary-evidence facts for each failing trace within
its export's transaction. Group queries use those facts and decode the group
identity once per group. Application assignments and service mappings remain
query-time decisions. Search and alternate-evidence filters use the original
span query; oversized metadata uses a partial-indexed fallback.

Three alternating paired rounds compared `55d61fe` with the working tree on
the same Windows host. Each binary received 34,816 spans in a fresh database;
every trace failed. Page timings are means of 100 requests per round, and
the table reports medians of three rounds. No Go tests ran alongside them.
[Raw end-to-end measurements](benchmarks/2026-11-21-group-projection.json)

| Measurement | Before | After |
| --- | ---: | ---: |
| Error groups page, 17,408 failing traces | 272.51 ms | 54.07 ms |
| Error groups page, three rounds | 268.60 / 272.51 / 292.16 ms | 54.07 / 52.77 / 62.85 ms |
| Export median | 16.71 ms | 18.31 ms |
| Export p95 | 29.81 ms | 31.19 ms |
| Accepted spans/s, one connection | 6,580 | 5,425 |
| Accepted spans/s, four connections | 10,267 | 9,290 |
| Exports refused with 503 | 0 | 0 |
| Resident memory after ingest | 33.19 MiB | 34.07 MiB |
| Main database after ingest | 18.73 MiB | 22.53 MiB |
| Database growth per span, including WAL/SHM | 684 bytes | 773 bytes |

The page takes 80% less time here. The cache trades writes and disk space for
reads: this all-failure fixture adds 3.8 MiB to the main file, and the median
export takes another 1.6 ms. Throughput also falls in these rounds, so this is
not an ingest speedup. The fixture creates a cache row for every trace; do not
assume the same overhead for another failure rate or span distribution. WAL
checkpoint timing contributes to the spread in total database growth.

An in-process comparison on the same 50,000-span database, containing 10,000
traces and 2,500 failing traces, takes 50.04 ms through span joins and 7.40 ms
through the projection (medians of three runs, 20 iterations each).
[Raw microbenchmark output](benchmarks/2026-11-21-group-projection-micro.txt)

```sh
go test ./internal/storage/sqlite -run '^$' -bench '^BenchmarkFailureGroupProjection$' -benchtime=20x -count=3
```

For an end-to-end comparison, build `55d61fe` and the working tree as separate
binaries and alternate one-round invocations of `scripts/bench-native.py
--binary PATH --rounds 1 --out RESULT.json`, three times per binary. The
harness now measures `/failures?view=groups`; the nightly comparator includes
`mean_groups_page_ms` once the CI baseline contains it.

Counts, receipt bounds, request outcomes, group identity, reopening and cursor
ordering remain exact. Differential tests cover mapping/reassignment, late
evidence and roots, retention, large metadata and the search/run/attribute
fallbacks. A failed cache write rolls back the export. Aggregation still
visits every matching member, so fully bounded per-group/time aggregation
remains on the roadmap. The earlier million-span soak below used schema 15;
the revalidation section below repeats it for schema 16.

#### One million spans at the size limit on 2026-11-21

[`scripts/soak.py`](../scripts/soak.py) sent 1,000,064 spans, 64 two-span
failing traces per export over four connections, into a database limited to
64 MiB, and restarted the server after half of them.
[Raw measurements](benchmarks/2026-11-21-soak-1m.json)

| Spans sent | Spans/s | Median export | p95 export | Main file | WAL | Memory | Evicted traces |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 100,096 | 11,994 | 34.37 ms | 51.01 ms | 52.9 MiB | 2.7 MiB | 35.0 MiB | 0 |
| 200,192 | 5,019 | 74.94 ms | 122.77 ms | 64.0 MiB | 0.4 MiB | 34.3 MiB | 40,704 |
| 500,480 | 4,924 | 74.84 ms | 105.50 ms | 64.0 MiB | 4.3 MiB | 34.8 MiB | 190,592 |
| 600,576 (after restart) | 4,774 | 77.05 ms | 262.90 ms | 64.0 MiB | 0.8 MiB | 34.0 MiB | 240,832 |
| 1,000,064 | 4,468 | 86.19 ms | 124.40 ms | 64.0 MiB | 4.2 MiB | 35.7 MiB | 440,832 |

- The main file never passed the limit and no export was refused; the WAL
  stayed under 4.3 MiB and memory between 34 and 36 MiB.
- The server answered `/readyz` 0.56 seconds after its first start and 0.51
  seconds after the restart on the full database.
- At the limit the database kept 59,200 traces, 567 bytes of main file per
  span including indexes and free pages.
- Reaching the limit halves throughput, from 12,000 to about 4,800 spans per
  second, because every export now also deletes as many traces as it adds.
  Median export latency then drifts from 75 to 86 ms over the next 800,000
  spans.
- On the full database the failures API took 1.58 ms, the Failures page
  2.21 ms and Overview 49.87 ms, but **Error groups took 892 ms**: it groups
  every failing trace, and this fixture keeps 59,200 of them. That is the
  measured case for bounding error grouping.

#### Historical benchmark revalidation on 2026-11-21

The earlier records are retained. Docker export timings, native Windows
timings and the capacity soak describe different environments or workloads;
they must not be merged into one latency series. The November 20 native
33.43 ms export figure predates subsequent code changes. The paired baseline
at `55d61fe` already takes about 16.5 ms, so the fall from 33 ms to 18 ms cannot
be attributed to schema 16's group projection.

Rebuilt the working tree and repeated three alternating pairs against the
same baseline binary. Both binary hashes match the previous paired run.
The fixture is unchanged since `7dbfb2d`. All rounds used fresh databases,
34,816 spans per process and 100 requests per query, with no simultaneous
builds, other benchmarks or Docker startup.
[Raw paired repeat](benchmarks/2026-11-21-revalidation-paired.json)

| Native measurement | Previous candidate run | Candidate repeat | Baseline repeat |
| --- | ---: | ---: | ---: |
| Export median | 18.31 ms | 18.36 ms | 16.50 ms |
| Export p95 | 31.19 ms | 30.55 ms | 26.94 ms |
| Groups page, 17,408 failures | 54.07 ms | 56.29 ms | 285.22 ms |
| Trace API | 0.95 ms | 0.94 ms | 0.91 ms |
| Four-connection accepted spans/s | 9,290 | 9,287 | 10,666 |
| Memory after ingest | 34.07 MiB | 34.44 MiB | 34.40 MiB |
| Bytes/span, including WAL/SHM snapshot | 773 | 826 | 648 |

The repeat confirms the roughly fivefold groups-page improvement and the
extra ingest work. It also shows why snapshot file growth must be treated
carefully: even the identical candidate binary varies by 7% in total
bytes/span, with checkpoint timing and page allocation contributing to it.
The fixed-capacity soak below measures how much history actually fits.

The in-process repeat takes 52.58 ms through span joins and 7.99 ms through
the projection, versus 50.04 and 7.40 ms previously.
[Repeated microbenchmark](benchmarks/2026-11-21-revalidation-micro.txt)

The exporter-count sweep accepts every export at all five levels, using the
default four ingest slots. These are throughput figures, not individual
request latency. One run per level, fresh process/database, 32 warm-up exports
and 32 measured exports per connection.
[Raw concurrency repeat](benchmarks/2026-11-21-revalidation-concurrency.json)

| Exporters | Accepted spans/s | Exports refused with 503 |
| ---: | ---: | ---: |
| 1 | 5,988 | 0 |
| 2 | 7,291 | 0 |
| 4 | 9,619 | 0 |
| 8 | 8,499 | 0 |
| 16 | 6,360 | 0 |

The million-span soak uses the same harness and 64 MiB limit as the earlier
schema 15 run, with a restart halfway. Every export was acknowledged; the
oldest traces were evicted to make room. It is one capacity run per version,
not a fresh alternating-pair comparison.
[Raw schema 16 soak](benchmarks/2026-11-21-revalidation-soak-1m.json)

| Capacity measurement | Earlier schema 15 | Schema 16 repeat |
| --- | ---: | ---: |
| Spans sent | 1,000,064 | 1,000,064 |
| Maximum main file | 64.0 MiB | 64.0 MiB |
| Rejected exports | 0 | 0 |
| Retained traces at completion | 59,200 | 47,808 |
| Main bytes per retained span | 567 | 702 |
| Peak sampled memory | 35.66 MiB | 36.26 MiB |
| Restart to ready | 0.507 s | 0.511 s |
| Final-segment export median | 86.19 ms | 99.35 ms |
| Final-segment accepted spans/s | 4,468 | 3,906 |
| Groups page on the final store | 891.59 ms | 149.90 ms |

The projection's space cost matters: the same byte limit retains 19% fewer
traces. The final groups-page figures therefore cover different trace counts;
use the fixed-count paired benchmark to isolate the query improvement.
Capacity throughput is also lower in the repeat. Before adding further
per-group aggregates, measure sharing repeated group identities rather than
copying their JSON into every member row. Preserve exact membership, filters,
retention and atomic eviction while reducing that footprint.

The Docker profiles were also repeated in three rotated rounds, with 128
exports and 16,384 spans per backend per round. Docker Engine remained 28.5.1
with 12 CPUs and 15.55 GiB available. The existing Jaeger 2.23.0 and Tempo
3.0.0 images were reused; Geko was rebuilt as `geko:revalidation-benchmark`.
[Raw Docker repeat](benchmarks/2026-11-21-revalidation-otlp-http.json)

| Docker profile | Nov 17 export median | Nov 20 export median | Loaded repeat export median | Repeat p95 | Repeat memory after ingest |
| --- | ---: | ---: | ---: | ---: | ---: |
| Geko, SQLite | 36.21 ms | 34.29 ms | 44.94 ms | 67.07 ms | 22.59 MiB |
| Jaeger, in memory | 3.74 ms | 3.33 ms | 4.59 ms | 25.11 ms | 37.73 MiB |
| Jaeger, Badger | 4.49 ms | 3.68 ms | 5.17 ms | 28.00 ms | 125.40 MiB |
| Tempo, local | 4.52 ms | 4.03 ms | 4.87 ms | 24.70 ms | 44.50 MiB |

This Docker repeat is a mixed-load result, not an isolated regression test.
Starting Docker also started nine existing containers. They were left
running; sampled aggregate background CPU was 294% before and 393% after
the comparison (roughly three and four of the twelve cores). All four
profiles got slower than the old records. Their order was rotated, but
background contention remained uncontrolled, so do not attribute the changes
from November 20 to code alone. The native paired repeat completed before
Docker started and is the stronger evidence for the group-query change.
Export acknowledgement guarantees also differ across profiles, so these
response times alone do not establish equal durability.

Docker memory here is container-accounted usage; native memory above is a
Windows working-set snapshot. They are not interchangeable measurements.
Writable-layer growth also excludes mounted/anonymous volumes and must not
be treated as the complete disk cost of the Badger or Tempo profiles.

Reproduce the repeat, running each command after the previous one finishes:

```sh
python scripts/bench-paired.py --baseline bin/before --binary bin/after --rounds 3 --out paired.json
python scripts/bench-ingest-concurrency.py --binary bin/after --connections 1,2,4,8,16 --out concurrency.json
go test ./internal/storage/sqlite -run '^$' -bench '^BenchmarkFailureGroupProjection$' -benchtime=20x -count=3
python scripts/soak.py --binary bin/after --spans 1000000 --limit-mib 64 --out soak.json
python scripts/bench-compare.py --current paired.json --baseline previous-paired.json --ingest concurrency.json
docker build -t geko:revalidation-benchmark .
python scripts/compare-resources.py --geko geko:revalidation-benchmark --batches 128 --rounds 3 --jaeger-badger --tempo grafana/tempo:3.0.0
```

`bench-paired.py` records binary, fixture and harness hashes and writes
`complete: false` until every round finishes. The comparator accepts paired
candidate medians and refuses incomplete runs. These Windows measurements
are not a replacement for the nightly Linux CI baseline.

#### Shared identities and exact hourly totals — schema 20

The next implementation shares group identities and filter partitions instead
of copying identities into each member. Complete hours use exact weighted
totals, with individual members for at most two partial boundary hours. A
window with no complete hour uses the member time index. Versions, request
outcomes, topology, assignment and pin state remain separate partition
dimensions; mappings are resolved on reads. Search/run/alternate-evidence and
oversized metadata keep exact fallback paths.

The initial schema18/19 paired trials are retained in `shared-groups-paired.json`
and `shared-groups-final-paired.json`. Schema20 prepares only the affected
export's input and avoids refreshing unchanged extrema. Use the following
three-pair schema16→20 result for the final code, not a cross-run comparison
between intermediate trials. Each process stores 34,816 spans; no other Go
builds, tests or benchmarks ran alongside the paired measurement. Docker
Desktop and its existing workloads were already running.
[Final paired artifact](benchmarks/2026-11-21-shared-groups-schema20-paired.json)

| Measurement | Schema 16 reference | Schema 20 |
| --- | ---: | ---: |
| Groups page, 17,408 failures | 58.65 ms | 2.01 ms |
| Export median | 18.14 ms | 18.81 ms |
| Export p95 | 29.67 ms | 28.67 ms |
| Accepted spans/s, one connection | 6,113 | 6,256 |
| Accepted spans/s, four connections | 6,185 | 5,993 |
| Refused exports | 0 | 0 |
| Memory after ingest | 34.07 MiB | 36.21 MiB |
| Main file after ingest | 23.12 MiB | 21.41 MiB |
| Snapshot bytes/span, including WAL/SHM | 825 | 748 |

The dense fixture now has one identity and one partition for 17,408 cached
members. That is favorable to aggregation and sharing; it is not a claim that
all group queries take 2 ms. The exporter medians remain close in this paired
run; total snapshot bytes still vary with checkpoint/page-allocation timing.

`BenchmarkGroupingProfiles` validates equality with the legacy span query
before timing three 5,000-trace/25,000-span datasets spread across six hours.
Mixed and unique-version cases contain about 26% failing traces; the rare case
contains 1%. Root services/topology and versions vary. These are single
10-iteration in-process measurements, not end-to-end p95 estimates.
[Profile output](benchmarks/2026-11-21-group-window-profiles.txt)

| Profile/window | Legacy query | Optimized query | Partitions/member |
| --- | ---: | ---: | ---: |
| Mixed, whole hours | 20.19 ms | 3.66 ms | 0.463 |
| Mixed, 15 minutes | 1.86 ms | 1.27 ms | 0.463 |
| Mixed, two edge hours | 13.80 ms | 4.51 ms | 0.463 |
| Rare, whole hours | 1.99 ms | 1.15 ms | 1.000 |
| Rare, 15 minutes | 1.26 ms | 1.07 ms | 1.000 |
| Rare, two edge hours | 1.67 ms | 1.30 ms | 1.000 |
| Unique versions, whole hours | 20.19 ms | 4.34 ms | 1.000 |
| Unique versions, 15 minutes | 1.85 ms | 1.25 ms | 1.000 |
| Unique versions, two edge hours | 14.13 ms | 4.05 ms | 1.000 |

Partitions can equal members under version/topology diversity or sparse
failures. Sharing is not guaranteed to compress every workload. Scanning
boundary hours or fallback evidence can still scale with their member count.

The final schema20 capacity run repeated 1,000,064 spans, a 64 MiB limit and
restart halfway. The earlier schema16 run occurred before Docker startup;
the final run had existing background workloads. Treat throughput differences
as contextual single-run observations, not an isolated paired-capacity test.
[Final soak artifact](benchmarks/2026-11-21-shared-groups-schema20-soak.json)

| Capacity result | Schema 16 repeat | Schema 20 |
| --- | ---: | ---: |
| Maximum main file | 64.0 MiB | 64.0 MiB |
| Rejected exports | 0 | 0 |
| Retained traces | 47,808 | 49,664 |
| Main bytes/retained span | 702 | 676 |
| Peak sampled memory | 36.26 MiB | 36.40 MiB |
| Restart to ready | 0.511 s | 0.511 s |
| Final-segment export median | 99.35 ms | 131.76 ms |
| Final-segment accepted spans/s | 3,906 | 3,354 |
| Groups page on final store | 149.90 ms | 1.85 ms |

The fixed byte limit retained about 4% more history than schema16, not a full
return to schema15's 59,200 traces. The final store has 49,664 members, one
partition and one identity. Its groups-page time therefore does not establish
performance for high-diversity stores. Full-capacity write amplification remains
a measurement/optimization priority; acknowledgement still follows commit.
Application-loss atomicity, overflow budgeting and restart coverage are tested
separately, including cross-application traces and retention exclusion.

Reproduce with the scripts from the preceding section and add:

```sh
go test ./internal/storage/sqlite -run '^$' -bench '^BenchmarkGroupingProfiles$' -benchtime=10x -count=1
```

The [agent review and next plan](product-review-2026-11-21.md) distinguishes
completed code from unresolved product findings, operational validation and
release work.

### Facilitated study

Recruit five backend developers. Use the preassigned order in the
[results sheet](usability-results.csv). Three participants start in Geko and
two in Jaeger. Each uses both tools and sees a different scenario on their
second attempt. Record familiarity with each tool before starting.

Build and pull the images using the [resource comparison setup](#resource-comparison).
For every attempt, start the scenario assigned in the sheet:

```sh
python scripts/usability-study.py A
python scripts/usability-study.py B --verify-only
```

The first command starts fresh containers on loopback, loads the same synthetic
traces into both tools, and prints their URLs. Press Enter in that terminal
after the attempt to stop both containers. The second command checks the data in
both tools and exits. The script generates new trace IDs on every invocation.
Keep the terminal private because it displays the answer and Geko's temporary
sign-in token.

Open the assigned tool at its printed URL. Sign in to Geko before timing starts.
Use a fresh browser profile for each participant and keep the viewport the same
across attempts. Allow up to 120 seconds and read the assigned prompt as written.

- **A:** “The storefront checkout reported completion but stock wasn't
  reserved. Find the span that recorded the failure and report the HTTP status
  returned by the checkout request.”
- **B:** “The booking reservation reported completion but the room wasn't held.
  Find the span that recorded the failure and report the HTTP status returned
  by the reservation request.”

Start timing after the prompt. Log the span and HTTP status as stated, elapsed
seconds, navigation actions and whether a hint was needed. Record setup time
separately. Count an attempt as successful when the developer identifies both
the exact child span and HTTP 200 without help. Report the count under 60
seconds for each tool out of five, alongside the median time among successful
attempts and the number of timeouts. Keep participant names out of the
results sheet.

#### Browser pilot

Both scenarios passed a Chrome pilot on 2026-11-17. In Geko, the **Failures**
row showed the request's HTTP 200 and opened the evidence span directly.
In Jaeger, selecting the service and operation returned three matching
traces. The **Errors** column identified the trace to open. The timeline
showed HTTP 200 on the root span, and the child span's **Events** showed the
exception type. The pilot checked the paths and answers. The results sheet
is reserved for participant timings and outcomes.

## Roadmap effect

Keep M4 examples, packaging and benchmarks. Prioritize reliable storage bounds and
evidence workflow ahead of further decorative UI work or general monitoring.
Defer full logs, metrics, alerting, cloud accounts and automatic root-cause claims.
The existing service graph is a trace-scoped investigation aid, not a complete
production topology or service-health monitor.
