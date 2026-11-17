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
