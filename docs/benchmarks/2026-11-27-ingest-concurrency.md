# Ingest concurrency investigation — 2026-11-27

[Machine-readable observations](2026-11-27-ingest-concurrency.json) retain the
workload, binary hashes, raw HTTP samples, refusals and storage measurements.
These are local Windows measurements, independently of the hosted Linux CI run.

## Findings

The previous sweep sent 32 exports per connection: 128 exports at four
exporters and 512 at sixteen. It also used one sample per level. That confounds
concurrency with database growth and host variation. The native benchmark above
that table used three rounds and a different sequence; the two figures were
not interchangeable.

An equal-work experiment reproduced tail waits and refusals. Deterministic tests
then demonstrated that the admission gate allowed a new request to overtake an
existing waiter, including after a live concurrency increase. The old gate
broadcast every released slot to all waiters. FIFO reservations now give the
oldest valid queued caller the slot before waking it. Cancellation and expiry
return reservations; live limit decreases drain active work and increases remain
observable within the existing polling interval. HTTP, gRPC and imports share
this gate. Four active exports, two-second admission waits and durable commit
acknowledgment are preserved.

## Before/after observations

Each sample starts a fresh database, sends 32 warmup exports, then attempts
512 exports of 128 spans each. The admission limit is four. Three exploratory
repeats rotate the 4/16-exporter level order; their within-level variant order
is fixed (4: before/after, 16: after/before). One extra reverse-order pair per
level checks the opposite variant order. Measurements run sequentially.

| Exporters | Before median spans/s | FIFO median spans/s | Before accepted-request p95 | FIFO accepted-request p95 | Before/FIFO refusals |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 4 | 3,522.5 | 3,346.5 | 236.97 ms | 264.20 ms | 0 / 0 |
| 16 | 3,004 | 3,399 | 1,458.77 ms | 938.10 ms | 52 / 0 |

Counts sum four samples (2,048 attempted exports per variant/level). Rates and
p95s are medians of sample results; latency percentiles include successful
requests only. Every refused paired request was classified as `receiver_busy`.
The FIFO samples accepted every export. Equal attempted work does not mean equal
persisted work when the baseline refuses requests.

The local 16-exporter median rate rose about 13% and its accepted-request p95
fell about 36%. At four exporters the median rate fell about 5% and p95 rose
about 11%; rate ranges overlap (before 3,221–3,812, FIFO 3,278–3,892). No
four-exporter speedup or statistically significant general throughput change is
established. Repeat the controlled hosted benchmark before extrapolating these
numbers. The repeatable correctness improvement is preventing admission barging
and its observed refusals, rather than promising arbitrary concurrency scaling.

## Writer profile

A separate Go benchmark bypassed HTTP, normalization and Python. It used the
same fixed size and verified final span/trace counts. Its three samples were
grouped by producer count, so it is supporting profiling evidence rather than
a counterbalanced throughput experiment.

The writer dominated sampled execution. In the 16-producer profile, span
insertion accounted for 39%, group refresh 26%, commit 21% and SQL preparation
about 12.5% of cumulative samples. These shares overlap. Windows file flush
calls contributed materially; this is not a purely CPU-bound workload.

Write-mutex blocking was only 28 microseconds. The 179 seconds waiting for write
slots and 62 seconds for commit results are sums across goroutines, not wall
latency. Raising writer slots or rewriting the mutex is unsupported by this
profile. SQL statement reuse merits a separate controlled experiment, with
transaction, cancellation and failure-isolation tests before adoption. SQLite
and durable storage continue to set the throughput ceiling.

## Reproduce and CI

```sh
go build -o bin/geko ./cmd/geko
python scripts/bench-ingest-concurrency.py --binary bin/geko --connections 1,4,16 --ingest-concurrency 4 --total-exports 512 --rounds 3 --out bench/ingest.json
go test ./internal/storage/sqlite -run '^$' -bench '^BenchmarkConcurrentIngest$' -benchtime=512x -count=3
```

Omitting `--total-exports` retains the historical per-connection workload.
Schema 2 connects all clients before a synchronized timed start and reports
client CPU, request p50/p95, rate spread, raw observations and bounded refusal
categories. `ms_per_accepted_export` remains the amortized wall-time/rate inverse;
the explicit p50/p95 fields measure individual accepted requests.

The nightly job now uses fixed total work and three rotated rounds. Its summary
shows methodology, spread and successful-request p95. The regression guard
checks raw and aggregate refusals, so medians cannot hide an unsuccessful round.
The native baseline was retained. No hosted workflow was dispatched.

Validation: full Windows Go tests, vet, staticcheck, module consistency, seven
harness tests, eleven comparator tests and independent code review passed.
Linux race validation passed for the full OTLP package and 100 repetitions of
admission/cancellation/expiry cases. No commit or push was performed.
