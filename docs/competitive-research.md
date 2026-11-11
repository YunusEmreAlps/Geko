# Competitive research and product focus

Research date: 2026-11-11. Method: official documentation and current Geko source
review. No side-by-side usability study or resource benchmark was performed.
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

## Roadmap effect

Keep M4 examples, packaging and benchmarks. Prioritize reliable storage bounds and
evidence workflow ahead of further decorative UI work or general monitoring.
Defer full logs, metrics, alerting, cloud accounts and automatic root-cause claims.
The existing service graph is a trace-scoped investigation aid, not a complete
production topology or service-health monitor.
