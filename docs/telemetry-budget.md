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
