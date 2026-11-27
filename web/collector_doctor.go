package web

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/YunusEmreAlps/geko/internal/collectordoctor"
)

const collectorSampleInterval = 5 * time.Second

func (d *dashboard) collectorDoctor(w http.ResponseWriter, r *http.Request) {
	options := collectordoctor.Options{Sources: d.settings.CollectorDoctor}
	back, refresh, sample := "", "/collector-doctor", "/collector-doctor?sample=true"
	if raw := r.URL.Query().Get("return"); raw != "" {
		back = safeConnectReturn(raw, d.currentSettings())
		query := url.Values{"return": {back}}
		refresh = route("/collector-doctor", query)
		query.Set("sample", "true")
		sample = route("/collector-doctor", query)
	}
	render := func(ctx context.Context) {
		report := collectordoctor.Inspect(ctx, options)
		summary := summarizeCollector(report, time.Now().UTC())
		d.collectorLast.Store(&summary)
		d.render(w, r, http.StatusOK, "collector-doctor", "Collector Doctor", struct {
			collectordoctor.Report
			CanSample                        bool
			SampleInterval                   string
			ReturnURL, RefreshURL, SampleURL string
		}{report, options.MetricsURL != "", collectorSampleInterval.String(), back, refresh, sample})
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

func summarizeCollector(report collectordoctor.Report, at time.Time) collectorSnapshot {
	summary := collectorSnapshot{At: at, ConfigChecked: report.ConfigChecked, MetricsChecked: report.MetricsChecked, Sampled: report.Sampled}
	for _, finding := range report.Findings {
		switch finding.Severity {
		case "error":
			summary.Errors++
		case "warning":
			summary.Warnings++
		}
	}
	return summary
}

// Only local Connect state is carried between tools. Do not reflect arbitrary
// return URLs, secret token values or extra query fields in navigation links.
func safeConnectReturn(raw string, settings Settings) string {
	if len(raw) > 2048 {
		return "/connect"
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Path != "/connect" || u.Opaque != "" {
		return "/connect"
	}
	input, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "/connect"
	}
	query := url.Values{}
	for _, key := range []string{"service", "application", "protocol", "token", "since"} {
		if len(input[key]) > 1 {
			return "/connect"
		}
		value := input.Get(key)
		if value == "" {
			continue
		}
		switch key {
		case "service", "application":
			if !snippetName.MatchString(value) {
				return "/connect"
			}
		case "protocol":
			if value != "http/protobuf" && value != "grpc" {
				return "/connect"
			}
		case "token":
			if !slices.Contains(settings.IngestTokens, value) {
				return "/connect"
			}
		case "since":
			if n, err := strconv.ParseInt(value, 10, 64); err != nil || n <= 0 {
				return "/connect"
			}
		}
		query.Set(key, value)
	}
	query.Set("check", rand.Text())
	return route("/connect", query) + "#delivery-diagnostics"
}
