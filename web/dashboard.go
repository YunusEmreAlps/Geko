// Package web serves the embedded Geko dashboard.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log"
	"maps"
	"net/http"
	"net/url"
	"time"

	"geko/internal/api"
	"geko/internal/investigation"
	"geko/internal/storage"
	"geko/internal/telemetry"
)

//go:embed templates/*.html assets/*
var files embed.FS

type Reader interface {
	api.Reader
	Overview(context.Context, time.Time) (investigation.Overview, error)
	Span(context.Context, telemetry.TraceID, telemetry.SpanID) (investigation.Span, error)
	Status(context.Context) (storage.Status, error)
}

type Settings struct {
	ListenAddr string
	DBPath     string
	Version    string
}

type dashboard struct {
	store     Reader
	settings  Settings
	templates *template.Template
	slots     chan struct{}
}

type screen struct {
	Page, Title string
	Data        any
}

func Register(mux *http.ServeMux, store Reader, settings Settings) error {
	t, err := template.New("dashboard").Funcs(template.FuncMap{
		"duration": formatDuration, "timestamp": formatTimestamp, "classification": classification,
		"size": formatSize, "evidenceLink": evidenceLink, "signal": signal,
		"outcome": outcome, "outcomeClass": outcomeClass,
		"attributes": func(label string, values telemetry.Attributes) any {
			return struct {
				Label  string
				Values telemetry.Attributes
			}{label, values}
		},
	}).ParseFS(files, "templates/*.html")
	if err != nil {
		return fmt.Errorf("load dashboard templates: %w", err)
	}
	d := &dashboard{store: store, settings: settings, templates: t, slots: make(chan struct{}, 4)}
	mux.HandleFunc("GET /{$}", d.local(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/failures", http.StatusSeeOther)
	}))
	mux.HandleFunc("GET /failures", d.local(d.failures))
	mux.HandleFunc("GET /overview", d.local(d.overview))
	mux.HandleFunc("GET /traces/{traceID}", d.local(d.trace))
	mux.HandleFunc("GET /settings", d.local(d.status))
	for _, name := range []string{"dashboard.css", "dashboard.js", "geko.svg"} {
		mux.HandleFunc("GET /assets/"+name, d.local(func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, files, "assets/"+name)
		}))
	}
	return nil
}

func (d *dashboard) local(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		if !api.LocalHost(r.Host) {
			http.Error(w, "Dashboard requires a loopback Host", http.StatusForbidden)
			return
		}
		handler(w, r)
	}
}

func (d *dashboard) render(w http.ResponseWriter, status int, page, title string, data any) {
	var body bytes.Buffer
	if err := d.templates.ExecuteTemplate(&body, "layout", screen{Page: page, Title: title, Data: data}); err != nil {
		log.Printf("render dashboard %s: %v", page, err)
		http.Error(w, "Dashboard rendering failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = body.WriteTo(w)
}

func (d *dashboard) problem(w http.ResponseWriter, r *http.Request, status int, title, message string) {
	d.render(w, status, "error", title, struct {
		Title, Message, Retry string
	}{title, message, r.URL.RequestURI()})
}

func (d *dashboard) withQuery(w http.ResponseWriter, r *http.Request, handler func(context.Context)) {
	select {
	case d.slots <- struct{}{}:
		defer func() { <-d.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		d.problem(w, r, http.StatusServiceUnavailable, "Dashboard is busy", "Try loading the page again in a moment.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	handler(ctx)
}

func (d *dashboard) unavailable(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		d.problem(w, r, http.StatusGatewayTimeout, "The query timed out", "Try a narrower time range or reload the page.")
		return
	}
	d.problem(w, r, http.StatusInternalServerError, "Could not load telemetry", "Check the local database with geko doctor and try again.")
}

type failureView struct {
	Items                          []investigation.TraceSummary
	Search, Service, Status, Range string
	Refresh, First, Next           string
	Filtered                       bool
}

func (d *dashboard) failures(w http.ResponseWriter, r *http.Request) {
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid filters", "Reset the filters and try again.")
		return
	}
	view := failureView{
		Search: params.Get("q"), Service: params.Get("service"), Status: params.Get("status"), Range: params.Get("range"),
	}
	params.Del("limit")
	refresh := maps.Clone(params)
	refresh.Del("cursor")
	view.Refresh = route("/failures", refresh)
	if view.Range != "" {
		period, ok := map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}[view.Range]
		if !ok {
			d.problem(w, r, http.StatusBadRequest, "Invalid time range", "Choose an available time range from the failures page.")
			return
		}
		if params.Get("from") == "" {
			params.Set("from", time.Now().UTC().Add(-period).Format(time.RFC3339Nano))
		}
	}
	params.Set("limit", "25")
	filter, err := api.ParseFailureFilter(params)
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid filters", err.Error())
		return
	}
	view.Filtered = filter.Search != "" || filter.Service != "" || filter.HTTPStatus != 0 || filter.From != nil || filter.To != nil
	d.withQuery(w, r, func(ctx context.Context) {
		page, err := d.store.Failures(ctx, filter)
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		view.Items = page.Items
		params.Del("limit")
		if filter.After != nil {
			params.Del("cursor")
			view.First = route("/failures", params)
		}
		if page.Next != nil {
			params.Set("cursor", api.EncodeCursor(page.Next))
			view.Next = route("/failures", params)
		}
		d.render(w, http.StatusOK, "failures", "Failures", view)
	})
}

func (d *dashboard) overview(w http.ResponseWriter, r *http.Request) {
	d.withQuery(w, r, func(ctx context.Context) {
		since := time.Now().UTC().Add(-24 * time.Hour)
		overview, err := d.store.Overview(ctx, since)
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		recent, err := d.store.Failures(ctx, investigation.FailureFilter{From: &since, Limit: 8})
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		d.render(w, http.StatusOK, "overview", "Overview", struct {
			investigation.Overview
			Items []investigation.TraceSummary
		}{overview, recent.Items})
	})
}

func (d *dashboard) status(w http.ResponseWriter, r *http.Request) {
	d.withQuery(w, r, func(ctx context.Context) {
		status, err := d.store.Status(ctx)
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		d.render(w, http.StatusOK, "settings", "Settings", struct {
			Settings
			Storage storage.Status
		}{d.settings, status})
	})
}

func route(path string, params url.Values) string {
	if query := params.Encode(); query != "" {
		return path + "?" + query
	}
	return path
}
