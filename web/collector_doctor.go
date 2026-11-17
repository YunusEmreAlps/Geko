package web

import (
	"context"
	"net/http"
	"time"

	"github.com/YunusEmreAlps/geko/internal/collectordoctor"
)

const collectorSampleInterval = 5 * time.Second

func (d *dashboard) collectorDoctor(w http.ResponseWriter, r *http.Request) {
	options := collectordoctor.Options{Sources: d.settings.CollectorDoctor}
	render := func(ctx context.Context) {
		report := collectordoctor.Inspect(ctx, options)
		d.render(w, r, http.StatusOK, "collector-doctor", "Collector Doctor", struct {
			collectordoctor.Report
			CanSample      bool
			SampleInterval string
		}{report, options.MetricsURL != "", collectorSampleInterval.String()})
	}
	if r.URL.Query().Get("sample") != "true" {
		d.withQuery(w, r, render)
		return
	}
	if options.MetricsURL == "" {
		d.problem(w, r, http.StatusBadRequest, "Collector metrics unavailable", "Set collector_doctor.metrics_url to compare live samples.")
		return
	}
	// A sampling request waits between scrapes. Keep it independent of the
	// slots used by trace queries and refuse overlapping comparisons.
	select {
	case d.collectorSample <- struct{}{}:
		defer func() { <-d.collectorSample }()
	default:
		w.Header().Set("Retry-After", "5")
		d.problem(w, r, http.StatusServiceUnavailable, "Collector comparison in progress", "Try again in a few seconds.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	options.SampleInterval = collectorSampleInterval
	render(ctx)
}
