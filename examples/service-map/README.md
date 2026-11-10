# Synthetic service map demo

This example exports a single **synthetic** trace with five services. It does
not run five network services or measure real network latency. Service names
start with `demo-`, and timing is explicit fixture data for inspecting the UI.

Start Geko, then run from the repository root in PowerShell:

```powershell
$env:OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = 'http://127.0.0.1:4318/v1/traces'
go run ./examples/service-map
```

Open Failures and select `POST /checkout` from `demo-frontend`. Its service map
shows frontend → checkout → catalog, inventory and payments. Select payments
to inspect the recorded timeout. Expand **Span waterfall** to inspect timings.
The printed trace ID can also be pasted into the failure search.

The graph shows only this trace's observed parent-child relationships, not an
aggregate service dependency map. Nodes count spans, not requests. Error counts
mean spans with failure evidence, not a service health assessment. Same-service
relationships stay within a node. Missing parents do not create guessed links.
Maps read at most 500 spans and explicitly mark truncated results. Concurrent
late arrivals may become visible on the next page refresh.
