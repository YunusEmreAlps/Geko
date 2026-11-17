# Collector Doctor

`geko doctor` checks Geko's SQLite database and listener addresses. To inspect
an OpenTelemetry Collector, select its configuration or metrics explicitly.

```sh
geko doctor --collector-config collector.yaml
geko doctor --collector-metrics http://127.0.0.1:8888/metrics --sample 5s
geko doctor --collector-config collector.yaml --metrics-file after.prom --baseline before.prom --json
```

The configuration check follows pipeline references to receivers, processors,
exporters and connectors. It flags missing or repeated components, inactive
components, missing pipeline stages, memory limiter order and explicitly
disabled exporter buffering or retries. Supply a resolved YAML file if your
Collector merges multiple files or substitutes configuration providers. Use
the Collector distribution's own `validate` command for component options and
signal support.

The metrics check accepts Prometheus text. One sample reports cumulative
failures. Two samples compare counters for matching series and skip counters
that reset or change identity. Queue pressure reflects the current sample.
Refused, failed-to-enqueue and failed-send counters describe different stages.
A failed send alone leaves open whether a later retry succeeded.
Queue utilization is flagged at 80% or more when matching size and capacity
series are available. The command exits with a nonzero status if the report
contains an error finding. Warnings and informational findings still appear
in the report.

To enable the report under **Settings → Receiver** in the dashboard, add
either or both sources to `geko.yaml`:

```yaml
collector_doctor:
  config_file: collector.yaml
  metrics_url: http://127.0.0.1:8888/metrics
```

Relative paths resolve from the Geko YAML file's directory. The dashboard
reads only these configured sources. **Refresh** takes one metrics sample.
With a metrics URL configured, **Compare 5s samples** takes two scrapes
and reports the increases. **Current snapshot** returns to cumulative
counters. `geko doctor --collector` uses the same sources. Input is bounded
to 2 MiB per source. Reports contain component IDs and metric counts, with
configuration values and arbitrary metric labels omitted.

See [Collector configuration](https://opentelemetry.io/docs/collector/configuration/)
and [Collector internal telemetry](https://opentelemetry.io/docs/collector/internal-telemetry/)
for Collector-side options.
