package web

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"github.com/YunusEmreAlps/geko/internal/storage"
)

// Shared delivery summaries contain only coverage, counters and next steps.
// Payloads, Collector findings, paths and endpoint credentials stay out.
type deliveryReport struct{ Stages []deliveryStage }
type deliveryStage struct{ Title, State, Scope, Evidence, Action, URL, Link string }
type collectorSnapshot struct {
	At                                     time.Time
	ConfigChecked, MetricsChecked, Sampled bool
	Errors, Warnings                       int
}

func buildDelivery(view connectView, settings Settings, receiver *otlp.Diagnostics, status *storage.Status, collector *collectorSnapshot, now time.Time) deliveryReport {
	observation := deliveryStage{Title: "Service observation", State: "Not started", Scope: "Selected service and namespace in the 60-second observation window; not proof of which credential or protocol exported it.", Evidence: "No observation window has been started.", Action: "Start a check, generate a request, then allow for the SDK batch delay.", URL: view.Check.StartURL, Link: "Start a new check"}
	switch {
	case len(view.Check.Samples) > 0:
		observation.State = "Observed"
		observation.Evidence = "A recent committed OTLP export matched this service and namespace in the observation window."
		observation.Action = "Open the observed trace, or browse retained service traces."
		observation.URL = view.TracesURL
		observation.Link = "Browse service traces"
	case !view.Check.Available:
		observation.State = "Unavailable"
		observation.URL = ""
		observation.Action = "Use retained service traces and receiver settings. This instance does not publish recent-export observations."
		observation.Evidence = "Recent export observations are unavailable in this instance."
	case view.Check.Started && view.Check.Expired:
		observation.State = "No matching sample"
		observation.Evidence = "The observation window ended without a matching sample still available."
		observation.Action = "Check the stages below, SDK endpoint/TLS and sampling, then start another check. Absence does not prove instrumentation is missing."
	case view.Check.Started:
		observation.State = "Waiting"
		observation.Evidence = "No matching sample is available yet."
		observation.Action = "Generate a request and use Check again. An idle app, batching or sampling may export nothing."
	}
	ingest := deliveryStage{Title: "Receiver", State: "Unavailable", Scope: "All applications, cumulative since this receiver started; not scoped to this service or observation window.", Evidence: "Receiver counters are unavailable.", Action: "Check the SDK endpoint, HTTP versus gRPC, and HTTP versus HTTPS. Authentication failures happen before these counters: inspect Geko logs for 401/403 responses.", URL: "/settings#receiver", Link: "Receiver settings"}
	if receiver != nil {
		ingest.State = "Counters available"
		ingest.Evidence = fmt.Sprintf("Accepted nonempty exports: %d; rejected spans in partial exports: %d.", receiver.AcceptedExports, receiver.RejectedSpans)
		if !receiver.LastAccepted.IsZero() {
			ingest.Evidence += " Last accepted: " + receiver.LastAccepted.UTC().Format(time.RFC3339) + "."
		}
		if receiver.RejectedSpans > 0 {
			ingest.State = "Past rejections"
		}
		for _, rejection := range receiver.Rejections {
			if rejection.Count > 0 {
				ingest.State = "Past rejections"
				ingest.Evidence += fmt.Sprintf(" %s: %d.", rejection.Reason, rejection.Count)
			}
		}
		if ingest.State == "Past rejections" {
			ingest.Action = "Past rejections may be unrelated to this check. Match response codes in exporter logs with receiver settings: 400/415 payload or protocol, 413 batch size, 503 admission or storage. For authentication failures check credentials; receiver 403 responses can also reflect namespace bindings or Host restrictions."
		}
	}
	if settings.ReadOnly {
		ingest.State = "Read-only"
		ingest.Action = "This instance refuses new telemetry. Connect the exporter to a writable Geko instance."
	}
	disk := deliveryStage{Title: "Storage", State: "Unavailable", Scope: "Current metadata and counters from the database lifetime, across all applications. This is not an integrity check.", Evidence: "Storage metadata could not be read within the dashboard query budget.", Action: "Run geko doctor and check the database and disk. Existing exporter settings remain available above.", URL: "/settings#storage", Link: "Storage settings"}
	if status != nil {
		disk.State = "Metadata available"
		disk.Evidence = fmt.Sprintf("Main file: %d bytes; WAL: %d bytes; reusable pages: %d bytes. Evicted traces: %d; rejected exports at capacity: %d; pinned traces: %d.", status.MainBytes, status.WALBytes, status.FreeBytes, status.EvictedTraces, status.RejectedExports, status.PinnedTraces)
		disk.Action = "A metadata read does not prove delivery or database integrity. For a missing older trace, review retention, eviction and pins; these counters cannot identify which trace was removed."
		if settings.MaxDBBytes > 0 {
			policy := settings.OnFull
			if policy == "" {
				policy = storage.EvictOldest
			}
			disk.Evidence += fmt.Sprintf(" Main-file limit: %d bytes; on_full: %s.", settings.MaxDBBytes, policy)
			if status.MainBytes >= settings.MaxDBBytes {
				disk.Action += " The file is at or above its limit, but reusable pages may still accept exports. Review the capacity policy and pins; a large file alone does not prove rejection."
			}
		}
	}
	external := deliveryStage{Title: "Collector", State: "Not configured", Scope: "Only the configured Collector sources, not the selected service or end-to-end delivery.", Evidence: "No Collector configuration or metrics source is configured.", Action: "If the SDK exports directly to Geko, no Collector check is needed. Otherwise configure Collector Doctor sources in YAML.", URL: "/settings#configuration", Link: "Configuration"}
	if settings.CollectorDoctor.Enabled() {
		external.State = "Not checked"
		external.Evidence = "No completed Collector check is available in this server session."
		external.Action = "Inspect Collector configuration and metrics explicitly. Opening Connect does not scrape the Collector."
		external.URL = "/collector-doctor"
		external.Link = "Inspect Collector"
		if collector != nil {
			external.State = "Last check available"
			if now.Sub(collector.At) > 5*time.Minute {
				external.State = "Previous check is stale"
			}
			external.Evidence = fmt.Sprintf("Last completed check: %s; configuration checked: %t; metrics checked: %t; compared samples: %t; errors: %d; warnings: %d.", collector.At.UTC().Format(time.RFC3339), collector.ConfigChecked, collector.MetricsChecked, collector.Sampled, collector.Errors, collector.Warnings)
			external.Action = "Open the full report to inspect causes, evidence and suggested actions. A previous check cannot establish the Collector's current state or delivery for this service."
		}
	}
	return deliveryReport{Stages: []deliveryStage{observation, ingest, disk, external}}
}

// Reuse dashboard admission, but leave the setup page usable when diagnostics
// are busy or storage is unavailable. No detached queries or polling workers.
func (d *dashboard) deliveryStatus(r *http.Request) *storage.Status {
	if d.store == nil || r.Context().Err() != nil {
		return nil
	}
	select {
	case d.slots <- struct{}{}:
		defer func() { <-d.slots }()
	default:
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()
	status, err := d.store.Status(ctx)
	if err != nil || ctx.Err() != nil {
		return nil
	}
	return &status
}
