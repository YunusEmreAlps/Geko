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

## Return to a connection check

In source builds after v0.2.0, opening Collector Doctor from **Connect → Delivery
diagnostics** keeps a validated local return context through Refresh and sample
comparison. **Back to connection check** restores the service/application,
protocol, configured token name and observation start. An old observation can
expire under the existing check-window rules; start a fresh one if needed.
Connect shows only the last requested report's time, source coverage and
error/warning counts, never its raw findings or source credentials. It does not
trigger a Collector request itself. See [delivery evidence](connect.md#follow-delivery-evidence)
for each source's scope and limitations.

## Validation coverage

JSON reports include `coverage.config` (`not_requested`, `unavailable`,
`static_wiring`, or `partial_unresolved`), `coverage.metrics`, and an explicit
list of excluded checks. The dashboard shows the same source coverage. A
successful parse checks wiring, not component schemas, installed distribution
components, signal compatibility or end-to-end delivery. The metrics status
covers only supported diagnostic series, not all Collector telemetry.

Unresolved `${...}` provider/environment references and `$NAME` substitutions
are flagged without showing variable names, provider paths or values. By default, Geko
does not resolve environment variables, fetch file/remote providers, execute
Collector commands or merge multiple configuration files. Resolve and merge
using your trusted Collector workflow, then inspect that single YAML mapping.
Multiple YAML documents are rejected. A failed send counter records failed
attempts; retries may succeed. Refusals and queue failures also require upstream
retry evidence before concluding permanent loss.

## Optional validation with your Collector binary

```sh
geko doctor --collector-config collector.yaml --collector-binary /path/to/otelcol --json
```

`--collector-binary` explicitly opts the CLI into running that trusted local
executable as `validate --config <absolute selected file>`. Geko never searches
PATH, uses a shell, downloads a binary or starts this step from the dashboard.
The binary inherits the caller's environment and may resolve its configured
providers; select only a binary and configuration you trust. Remote UNC binary
paths are rejected. This validates the selected distribution's configuration,
including supported component options, rather than starting its pipelines.

The process has a five-second timeout; stdout/stderr are discarded and never
included in reports because validation errors may contain credentials. Only
exit status zero produces `coverage.config: runtime_validated`. Failure or
timeout produces an error finding and `runtime_validation_failed`; use your
Collector's own validate command locally for sensitive error details. Even a
successful validation does not check running pipeline behavior, destination
connectivity or prove end-to-end delivery/loss. Configuration files remain
bounded to regular files up to 2 MiB.
