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
whole window.

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
