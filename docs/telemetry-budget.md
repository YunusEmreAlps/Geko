# Telemetry Budget

Telemetry Budget measures OTLP traces accepted by Geko over HTTP or gRPC.
Enable it in `geko.yaml`:

```yaml
telemetry_budget:
  enabled: true
  default:
    spans_per_hour: 10000
    bytes_per_hour: 10485760
  services:
    - namespace: storefront
      service: checkout
      spans_per_hour: 15000
      bytes_per_hour: 15728640
  attributes: [http.route, url.path, db.query.summary, order.id]
  cardinality_limit: 100
```

Limits apply per `service.namespace` and `service.name` pair. A service entry
replaces both default limits. A zero limit is unlimited. Each report lists
the services by volume, their overages and suggestions for sampling,
filtering and reducing high-cardinality dimensions. Exceeding a budget
does not affect trace ingestion.

Open **Settings → Receiver → Open Telemetry Budget**, or query the instance:

```sh
geko budget
geko budget --previous --json
geko budget --server https://geko.example.com --json
```

Use `GEKO_TOKEN` for a read token on a protected server. Reports cover the
current and previous UTC clock hours and start counting when Geko starts.
Counters reset on restart. The observed start time marks a partial hour.
This is the default behavior when `history_days` is zero (or omitted).
Scripts can read the same report with `GET /api/v1/budget` on the dashboard
listener. Pass `?previous=true` for the prior hour. The `available` field
indicates whether the requested window was tracked.

Bytes measure the protobuf size of accepted spans before masking, regardless
of the exporter's encoding or compression. Resource and scope wrappers,
HTTP headers, imported traces and rejected spans are excluded. Retransmitted
spans that the receiver accepts count again because they consume ingest volume.

Cardinality measures distinct scalar values for selected span and resource
attributes. The default keys are `http.route`, `url.path`,
`db.query.summary` and `service.instance.id`. Set `attributes: []` to turn
off cardinality tracking. Geko keeps keyed fingerprints in memory and returns
only attribute names and counts. After 256 distinct fingerprints for one
attribute, the count becomes a lower bound. The number of tracked service rows
is bounded. Additional services are aggregated in **Other services**, with
dedicated rows reserved for configured service limits.

## Optional volume history

**Unreleased source feature:** persistent history and `--hour` require a build
newer than v0.2.0. Enable them explicitly in YAML:

```yaml
telemetry_budget:
  enabled: true
  history_days: 7  # 0 disables history; 1–30 retains hourly checkpoints
```

The Budget page can select a UTC hour. The CLI/API expose the same window:

```sh
geko budget --hour 2026-11-24T08:00:00Z --json
geko budget --server https://geko.example.com --hour 2026-11-24T08:00:00Z
# GET /api/v1/budget?hour=2026-11-24T08:00:00Z
```

Checkpointed totals from previous sessions are combined with live counters,
without restoring old counters into ingestion or counting the current session
twice. Cardinality sets/fingerprints are never persisted; current/previous-hour
cardinality shows its separate process-local observation start. Older history
has no cardinality result. Limits on historical rows use the current config.

Changes from the previous hour appear only when both observed intervals cover
their full UTC hours without gaps or ambiguous overlapping sessions. A running hour, downtime or missing
checkpoint does not become zero traffic. Per-service changes are withheld when
an overflow row prevents complete attribution. Source data may still be shown
for partial windows, clearly marked as partial.

### Durability, storage and contention

- The receiver's existing acknowledgement contract is unchanged: accepted
  trace spans still wait for their SQLite transaction to commit. History is
  **best-effort reporting**, not an additional ingest transaction or exact
  delivery ledger.
- A one-minute worker checkpoints cumulative session/hour totals, with a final
  attempt after graceful HTTP/gRPC shutdown. A crash may lose volume since the
  last successful checkpoint; busy/full storage can extend this gap beyond a
  minute. Observation intervals disclose saved coverage instead of claiming
  complete hours across missing intervals.
- Updates are idempotent for the same session/hour. They use the existing
  serialized writer, a 50 ms context budget and yield when ingestion is busy.
  Brief SQLite write contention remains possible; a failed checkpoint leaves
  live counters in memory. It never evicts traces, and checkpoint errors are
  not returned to exporters. Ingestion retains its existing capacity policy.
- Checkpoints share `max_db_mib`. Estimated writes are skipped near 90% used
  space or WAL pressure to reserve headroom for traces. History still consumes some of the
  shared storage budget. There are at most 1,024 session/hour rows, each bounded
  to 256 service rows and 256 KiB. Older rows discarded at this cap are counted
  and disclosed; excess service identities stay in **Other services**.
- Retention runs on successful checkpoints. Disabling history stops writes and
  does not erase previously stored rows. Read-only mode serves history without
  starting the checkpoint worker. SQLite file space reclamation continues to
  use the existing compact policy.

Raw values, exception data, cardinality fingerprints, tokens and historical
limit configuration are not in checkpoint payloads. Only normalized service
identity, volume counters and observation intervals are persisted.

The local write-stage stress comparison uses one checkpoint attempt per 32
exports, much more frequent than the production one-minute worker. Raw runs
are in `docs/benchmarks/2026-11-24-budget-history-write-stage.txt`; alternating
A/B rounds are recorded separately. These Windows measurements isolate the
write stage and are not the Linux/network benchmark baseline or evidence of
a throughput improvement. Inter-round disk/host variation remains substantial.

The final alternating, fixed-128-export rounds recorded 14.47 ms/export without
checkpoints and 11.92 ms/export with a checkpoint attempt every 32 exports
(medians of the round means). Every history round saved actual checkpoints.
Host/disk variability prevents interpreting this as an ingestion speedup or a
precise overhead bound. The record includes
command/order, binary and source hashes:
[`2026-11-24-budget-history-paired.json`](benchmarks/2026-11-24-budget-history-paired.json).
Reproduce the write-stage modes with:

```sh
go test ./internal/storage/sqlite -run '^$' -bench '^BenchmarkBudgetHistoryWriteStage$' -benchtime=128x -count=3
```

The production worker runs once per minute; the stress cadence is not a claim
about the exact latency cost in a real workload.
