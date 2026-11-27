// Package web serves the embedded Geko dashboard.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/audit"
	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/collectordoctor"
	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/ownership"
	"github.com/YunusEmreAlps/geko/internal/privacy"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

//go:embed templates/*.html assets/*
var files embed.FS

type Reader interface {
	VersionComparisons(context.Context, investigation.RegressionFilter) ([]investigation.VersionComparison, error)
	CompareTrace(context.Context, telemetry.TraceID) (investigation.TraceSummary, *investigation.TraceSummary, error)
	VisitTraceSpans(context.Context, telemetry.TraceID, func(investigation.Span) error) error
	Traces(context.Context, investigation.FailureFilter) (investigation.Page[investigation.TraceSummary], error)
	Revision() uint64
	FailureGroups(context.Context, investigation.FailureFilter, *investigation.GroupCursor) (investigation.FailureGroups, error)
	SetGroupState(context.Context, string, investigation.GroupState, string, string, time.Time) error
	SetGroupNote(context.Context, string, string, string, string, time.Time) error
	SetTracePin(context.Context, telemetry.TraceID, bool, string, time.Time) error
	api.Reader
	HasTraces(context.Context) (bool, error)
	Overview(context.Context, time.Time) (investigation.Overview, error)
	TraceDetails(context.Context, telemetry.TraceID, investigation.TraceDetailFilter) (investigation.TraceDetails, error)
	Status(context.Context) (storage.Status, error)
	ApplicationNames(context.Context) ([]string, error)
	ServiceApplications(context.Context, string) ([]investigation.ServiceApplication, error)
	SetServiceApplication(context.Context, string, string) error
	AssignTraceApplication(context.Context, time.Time, time.Time, string, string) (int64, error)
	DiscardAssignmentProgress(context.Context) error
	StorageByApplication(context.Context) (investigation.StorageUsage, error)
}

type Settings struct {
	Runtime                 *config.Runtime
	TelemetryBudget         *budget.Tracker
	CollectorDoctor         collectordoctor.Sources
	AttributeSearch         privacy.AttributeSearch
	AttributeRetention      privacy.AttributeRetention
	RestoreTrace            func(context.Context, io.Reader, func(context.Context, telemetry.TraceID) (bool, error)) (telemetry.TraceID, error)
	Shutdown                <-chan struct{}
	DashboardAddr, GRPCAddr string
	ConfigSource            config.Source
	ListenAddr              string
	PublicURL               string
	// TraceLinkURL opens a trace in another tool; {trace_id} is replaced.
	TraceLinkURL, TraceLinkName string
	AccessToken                 bool
	IngestTokens                []string
	// IngestApplications maps bound ingest token names to their applications.
	IngestApplications map[string]string
	ReadTokens         []string
	TriageTokens       []string
	// ProxyHeader names the header an authenticating proxy sets, if trusted.
	ProxyHeader string
	// Audit records changes and sign-ins; nil discards them.
	Audit *audit.Log
	// Tokens, when set, supplies current token names after a reload.
	Tokens            access.Token
	IngestConcurrency int
	Notifications     *Notifications
	// SignIn and SignOut manage browser sessions when a token is configured.
	SignIn              func(http.ResponseWriter, *http.Request, string) (access.Principal, bool)
	SignOut             func(http.ResponseWriter, *http.Request) (access.Principal, bool)
	DBPath              string
	Version             string
	ReceiverDiagnostics func() otlp.Diagnostics
	ReceiverConnections func() otlp.ConnectionSnapshot
	RetentionDays       int
	MaxDBBytes          int64
	OnFull              storage.FullPolicy
	Redaction           string
	// ReadOnly shows every caller what a viewer sees; the server refuses changes.
	ReadOnly bool
	// Owners names the team owning each group.
	Owners ownership.Owners
}

// Notifications describes the configured webhooks without their secret URLs.
// Format and Host are empty when only application webhooks are configured.
type Notifications struct {
	Format, Host string
	// Digest is the weekly digest's schedule, or empty when it is off.
	Digest       string
	Applications []ApplicationWebhook
	Test         func(context.Context) error
}

// ApplicationWebhook receives one application's groups instead of the shared webhook.
type ApplicationWebhook struct {
	Application, Format, Host string
}

type dashboard struct {
	streams         chan struct{}
	store           Reader
	settings        Settings
	templates       *template.Template
	slots           chan struct{}
	collectorSample chan struct{}
	collectorLast   atomic.Pointer[collectorSnapshot]
	// usage caches the storage breakdown, which reads every stored trace.
	usage struct {
		sync.Mutex
		value investigation.StorageUsage
		at    time.Time
	}
}

type screen struct {
	Page, Title string
	Data        any
	SignOut     bool
	ReadOnly    bool
	// Caller is who is signed in, when a token is configured.
	Caller *access.Principal
}

func Register(mux *http.ServeMux, store Reader, settings Settings) error {
	if settings.Runtime != nil {
		settings.Owners = ownership.Dynamic(settings.Runtime.Owners)
	}
	t, err := template.New("dashboard").Funcs(template.FuncMap{
		"withReturn": withReturn, "failureTable": failureTable, "groupTable": groupTable,
		"sourceLinks": sourceLinks,
		"duration":    formatDuration, "timestamp": formatTimestamp, "age": func(t time.Time) string { return formatAge(t, time.Now()) }, "classification": classification,
		"size": formatSize, "evidenceLink": evidenceLink, "signal": signal,
		"percent":    func(share float64) string { return strconv.FormatFloat(100*share, 'f', 1, 64) + "%" },
		"portion":    func(total int64, share float64) int64 { return int64(float64(total) * share) },
		"ownerLabel": ownerLabel,
		"owner":      settings.Owners.For,
		"outcome":    outcome, "outcomeClass": outcomeClass,
		"serviceLabel": serviceLabel, "failureTitle": failureTitle, "issueLabel": issueLabel,
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
	d := &dashboard{store: store, settings: settings, templates: t, slots: make(chan struct{}, 4), streams: make(chan struct{}, 16)}
	mux.HandleFunc("GET /events/failures", d.local(d.failureEvents))
	mux.HandleFunc("GET /{$}", d.local(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/failures", http.StatusSeeOther)
	}))
	mux.HandleFunc("GET /failures", d.local(d.failures))
	mux.HandleFunc("GET /overview", d.local(d.overview))
	mux.HandleFunc("GET /regressions", d.local(d.regressions))
	mux.HandleFunc("GET /runs/compare", d.local(d.compareRuns))
	mux.HandleFunc("GET /traces", d.local(func(w http.ResponseWriter, r *http.Request) {
		d.traceList(w, r, true)
	}))
	mux.HandleFunc("GET /traces/{traceID}", d.local(d.trace))
	mux.HandleFunc("GET /traces/{traceID}/compare", d.local(d.compareTrace))
	mux.HandleFunc("GET /traces/{traceID}/otlp.json", d.local(d.downloadTrace))
	mux.HandleFunc("GET /groups/bundle", d.local(d.downloadBundle))
	mux.HandleFunc("GET /settings", d.local(d.status))
	mux.HandleFunc("GET /settings/storage", d.local(d.storageUsage))
	if settings.CollectorDoctor.Enabled() {
		d.collectorSample = make(chan struct{}, 1)
		mux.HandleFunc("GET /collector-doctor", d.local(d.collectorDoctor))
	}
	if settings.TelemetryBudget != nil {
		mux.HandleFunc("GET /budget", d.local(d.budgetPage))
		mux.HandleFunc("GET /api/v1/budget", d.local(d.budgetAPI))
	}
	mux.HandleFunc("GET /applications", d.local(d.applications))
	mux.HandleFunc("GET /connect", d.local(d.connect))
	mux.HandleFunc("GET /metrics", d.local(d.metrics))
	protect := http.NewCrossOriginProtection()
	if settings.Runtime != nil {
		mux.Handle("POST /settings/owners", protect.Handler(d.local(d.updateRuntimeOwners)))
		mux.HandleFunc("GET /api/v1/settings/owners", d.local(d.runtimeOwnersAPI))
		mux.Handle("POST /settings/operations/preview", protect.Handler(d.local(d.previewRuntimeOperations)))
		mux.Handle("POST /settings/operations", protect.Handler(d.local(d.updateRuntimeOperations)))
		mux.HandleFunc("GET /api/v1/settings/operations", d.local(d.runtimeOperationsAPI))
		mux.Handle("POST /api/v1/settings/operations/preview", protect.Handler(d.local(d.previewRuntimeOperations)))
		mux.Handle("POST /api/v1/settings/operations", protect.Handler(d.local(d.updateRuntimeOperations)))
		mux.Handle("POST /settings/budget", protect.Handler(d.local(d.updateRuntimeBudget)))
		mux.HandleFunc("GET /api/v1/settings/budget", d.local(d.runtimeBudgetAPI))
		mux.Handle("POST /api/v1/settings/budget", protect.Handler(d.local(d.updateRuntimeBudget)))
		mux.Handle("POST /api/v1/settings/owners", protect.Handler(d.local(d.updateRuntimeOwners)))
		mux.Handle("POST /settings/runtime", protect.Handler(d.local(d.updateRuntimeSettings)))
		mux.HandleFunc("GET /api/v1/settings/runtime", d.local(d.runtimeSettingsAPI))
		mux.Handle("POST /api/v1/settings/runtime", protect.Handler(d.local(d.updateRuntimeSettings)))
	}
	mux.Handle("POST /traces/attribute-search", protect.Handler(d.local(d.searchAttribute)))
	if settings.RestoreTrace != nil {
		mux.Handle("POST /traces/import", protect.Handler(d.local(d.restoreTrace)))
		mux.Handle("POST /groups/bundle", protect.Handler(d.local(d.importBundle)))
	}
	mux.Handle("POST /applications", protect.Handler(d.local(d.changeApplications)))
	mux.Handle("POST /groups/state", protect.Handler(d.local(d.changeGroupState)))
	mux.Handle("POST /groups/note", protect.Handler(d.local(d.changeGroupNote)))
	mux.Handle("POST /traces/{traceID}/pin", protect.Handler(d.local(d.changeTracePin)))
	if settings.SignIn != nil && settings.SignOut != nil {
		mux.HandleFunc("GET /login", d.local(d.login))
		mux.Handle("POST /login", protect.Handler(d.local(d.login)))
		mux.Handle("POST /logout", protect.Handler(d.local(d.logout)))
	}
	if settings.Notifications != nil {
		mux.Handle("POST /settings/notifications/test", protect.Handler(d.local(d.testNotification)))
	}
	for _, name := range []string{"dashboard.css", "dashboard.js", "geko.svg", "theme.js", "live.js", "workspace.mjs"} {
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
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		if !api.LocalHost(r.Host) && !access.Authenticated(r.Context()) && !access.Public(r.Context()) {
			http.Error(w, "Dashboard requires a loopback Host", http.StatusForbidden)
			return
		}
		handler(w, r)
	}
}

func (d *dashboard) render(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
	view := screen{Page: page, Title: title, Data: data, SignOut: d.settings.SignOut != nil && access.SessionAuthenticated(r), ReadOnly: d.settings.ReadOnly}
	if caller, ok := access.Caller(r.Context()); ok {
		view.Caller = &caller
	}
	var body bytes.Buffer
	if err := d.templates.ExecuteTemplate(&body, "layout", view); err != nil {
		log.Printf("render dashboard %s: %v", page, err)
		http.Error(w, "Dashboard rendering failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = body.WriteTo(w)
}

func (d *dashboard) problem(w http.ResponseWriter, r *http.Request, status int, title, message string) {
	d.render(w, r, status, "error", title, struct {
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

// writeFailed explains a failed triage change, which the size limit can refuse
// like an export.
func (d *dashboard) writeFailed(w http.ResponseWriter, r *http.Request, ctx context.Context, err error) {
	if errors.Is(err, storage.ErrCapacity) {
		d.problem(w, r, http.StatusServiceUnavailable, "Trace storage capacity reached", "Check storage usage in Settings, free space or raise the limit, and try again.")
		return
	}
	d.unavailable(w, r, ctx)
}

// caller is who made the request: a token, session or proxy user, or Local
// without a token.
func (d *dashboard) caller(r *http.Request) access.Principal {
	principal := access.Local
	if d.settings.Tokens.Enabled() {
		principal, _ = access.Caller(r.Context())
	}
	if d.settings.ReadOnly && principal.Role > access.RoleViewer {
		principal.Role = access.RoleViewer
	}
	return principal
}

// readOnly reports whether the caller lacks the admin role, so pages hide
// actions that would be refused.
func (d *dashboard) readOnly(r *http.Request) bool {
	return d.caller(r).Role != access.RoleAdmin
}

// changedBy names the caller for stored changes; nobody is named without a token.
func (d *dashboard) changedBy(r *http.Request) string {
	if !d.settings.Tokens.Enabled() {
		return ""
	}
	return d.caller(r).Name
}

type failureView struct {
	ReturnURL                         string
	CanRestore, CanTriage             bool
	AllTraces                         bool
	Path, AllTracesURL, Reset         string
	Groups                            []failureGroupView
	GroupSummary                      *investigation.FailureGroup
	GroupEvidence                     string
	Mode, Group, GroupsURL, TracesURL string
	GroupState                        string
	StateURLs                         map[string]string
	Live                              bool
	Items                             []investigation.TraceSummary
	Search, Service, Status, Range    string
	Pinned                            bool
	RunID, AttributeDigest            string
	SearchAttributes                  []string
	Refresh, First, Next              string
	Filtered                          bool
	HasTraces                         bool
	Endpoint                          string
	EvidenceService                   string
	Application                       string
	Applications                      []string
}

func (d *dashboard) failures(w http.ResponseWriter, r *http.Request) {
	d.traceList(w, r, false)
}

func (d *dashboard) traceList(w http.ResponseWriter, r *http.Request, allTraces bool) {
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid filters", "Reset the filters and try again.")
		return
	}
	path, pageName, title := "/failures", "failures", "Failures"
	if allTraces {
		if params.Has("trace_id") {
			d.findTrace(w, r)
			return
		}
		path, pageName, title = "/traces", "traces", "All traces"
		if params.Get("view") != "" || params.Get("group") != "" || params.Get("group_cursor") != "" {
			d.problem(w, r, http.StatusBadRequest, "Invalid trace filters", "Open error groups from the Failures page.")
			return
		}
	}
	view := failureView{
		ReturnURL:  localReturnURL(r.URL.RequestURI()),
		CanRestore: allTraces && d.settings.RestoreTrace != nil && !d.readOnly(r),
		CanTriage:  d.caller(r).Role >= access.RoleTriager,
		AllTraces:  allTraces, Path: path, Reset: path,
		Mode: params.Get("view"), Group: params.Get("group"), GroupState: params.Get("state"),
		Search: params.Get("q"), Service: params.Get("service"), Status: params.Get("status"), Range: params.Get("range"),
		EvidenceService: params.Get("evidence_service"),
		Application:     params.Get("application"),
		Pinned:          params.Get("pinned") == "true",
		RunID:           params.Get("run_id"),
		AttributeDigest: params.Get("attribute_digest"),
	}
	if allTraces {
		view.SearchAttributes = d.settings.AttributeSearch.Keys()
	}
	if view.Mode != "" && view.Mode != "groups" {
		d.problem(w, r, http.StatusBadRequest, "Invalid view", "Choose traces or error groups.")
		return
	}
	if view.Mode == "groups" && params.Get("cursor") != "" {
		d.problem(w, r, http.StatusBadRequest, "Invalid cursor", "Reset group pagination.")
		return
	}
	groupAfter, err := investigation.DecodeGroupCursor(params.Get("group_cursor"))
	if err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid cursor", err.Error())
		return
	}
	if view.Mode != "groups" && groupAfter != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid cursor", "Reset trace pagination.")
		return
	}
	toggle := maps.Clone(params)
	toggle.Del("cursor")
	toggle.Del("group_cursor")
	toggle.Del("group")
	toggle.Del("view")
	toggle.Del("state")
	view.TracesURL = route("/failures", toggle)
	view.AllTracesURL = route("/traces", toggle)
	toggle.Set("view", "groups")
	view.GroupsURL = route("/failures", toggle)
	view.Live = params.Get("cursor") == "" && groupAfter == nil
	params.Del("limit")
	refresh := maps.Clone(params)
	refresh.Del("cursor")
	refresh.Del("group_cursor")
	view.Refresh = route(path, refresh)
	if view.Mode == "groups" {
		view.Reset = "/failures?view=groups"
		view.StateURLs = make(map[string]string)
		for _, state := range []string{"open", "resolved", "muted"} {
			links := maps.Clone(refresh)
			links.Del("state")
			if state != "open" {
				links.Set("state", state)
			}
			view.StateURLs[state] = route("/failures", links)
		}
	} else if view.GroupState != "" {
		d.problem(w, r, http.StatusBadRequest, "Invalid filters", "Group states apply to error groups only.")
		return
	}
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
	view.Filtered = filter.GroupKey != "" || filter.Application != nil || filter.Search != "" || filter.Service != "" || filter.EvidenceService != "" || filter.HTTPStatus != 0 || filter.From != nil || filter.To != nil || filter.Pinned || filter.RunID != "" || filter.AttributeDigest != ""
	d.withQuery(w, r, func(ctx context.Context) {
		applications, err := d.store.ApplicationNames(ctx)
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		view.Applications = applications
		if filter.Application != nil && *filter.Application != "" && !slices.Contains(applications, *filter.Application) {
			view.Applications = append(view.Applications, *filter.Application)
		}
		var page investigation.Page[investigation.TraceSummary]
		if view.Mode == "groups" {
			groups, groupErr := d.store.FailureGroups(ctx, filter, groupAfter)
			if groupErr != nil {
				d.unavailable(w, r, ctx)
				return
			}
			for _, group := range groups.Items {
				links := maps.Clone(params)
				links.Del("view")
				links.Del("group_cursor")
				links.Del("limit")
				links.Del("state")
				links.Set("group", investigation.EncodeGroupKey(group.Key))
				view.Groups = append(view.Groups, failureGroupView{group, route("/failures", links)})
			}
			links := maps.Clone(params)
			links.Del("group_cursor")
			links.Del("limit")
			if groupAfter != nil {
				view.First = route("/failures", links)
			}
			if groups.Next != nil {
				links.Set("group_cursor", investigation.EncodeGroupCursor(groups.Next))
				view.Next = route("/failures", links)
			}
		} else if allTraces {
			page, err = d.store.Traces(ctx, filter)
		} else {
			page, err = d.store.Failures(ctx, filter)
			if err == nil && filter.GroupKey != "" && len(page.Items) > 0 {
				summaryFilter := filter
				summaryFilter.After, summaryFilter.Limit = nil, 1
				summaryFilter.GroupState = investigation.GroupAll
				var groups investigation.FailureGroups
				groups, err = d.store.FailureGroups(ctx, summaryFilter, nil)
				if err == nil && len(groups.Items) > 0 {
					view.GroupSummary = &groups.Items[0]
					view.GroupEvidence = groupBugEvidence(d.baseURL(r), groups.Items[0])
				}
			}
		}
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		view.Items = page.Items
		if len(view.Items) == 0 && len(view.Groups) == 0 {
			view.HasTraces, err = d.store.HasTraces(ctx)
			if err != nil {
				d.unavailable(w, r, ctx)
				return
			}
			view.Endpoint = d.traceEndpoint(r.Host)
		}
		params.Del("limit")
		if filter.After != nil {
			params.Del("cursor")
			view.First = route(path, params)
		}
		if page.Next != nil {
			params.Set("cursor", api.EncodeCursor(page.Next))
			view.Next = route(path, params)
		}
		d.render(w, r, http.StatusOK, pageName, title, view)
	})
}

func (d *dashboard) searchAttribute(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	if err := r.ParseForm(); err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid attribute search", "Choose an attribute and enter its exact value.")
		return
	}
	digest, ok := d.settings.AttributeSearch.Digest(r.PostForm.Get("attribute"), r.PostForm.Get("value"))
	if !ok {
		d.problem(w, r, http.StatusBadRequest, "Invalid attribute search", "Choose a configured attribute and enter a value up to 4,096 bytes.")
		return
	}
	http.Redirect(w, r, route("/traces", url.Values{"attribute_digest": {digest}}), http.StatusSeeOther)
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
		d.render(w, r, http.StatusOK, "overview", "Overview", struct {
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
		var receiver *otlp.Diagnostics
		if snapshot := d.settings.ReceiverDiagnostics; snapshot != nil {
			value := snapshot()
			receiver = &value
		}
		d.render(w, r, http.StatusOK, "settings", "Settings", struct {
			Settings
			Storage       storage.Status
			Receiver      *otlp.Diagnostics
			TraceEndpoint string
			Notification  string
			ReadOnly      bool
			RuntimeState  *config.RuntimeState
			OwnersState   *config.OwnersState
			BudgetState   *config.BudgetState
			Operations    *operationsSettings
		}{d.currentSettings(), status, receiver, d.traceEndpoint(r.Host), r.URL.Query().Get("notification"), d.readOnly(r), d.runtimeSettingsState(r), d.runtimeOwnersState(r), d.runtimeBudgetState(r), d.runtimeOperationsState(r)})
	})
}

// ownerLabel describes an eviction owner: a namespace, or a service name for
// spans without one.
func ownerLabel(owner string) string {
	if namespace, ok := strings.CutPrefix(owner, "n:"); ok {
		return "namespace " + namespace
	}
	return "service " + strings.TrimPrefix(owner, "s:")
}

// usageCache is how long a storage breakdown is reused.
const usageCache = time.Minute

func (d *dashboard) storageUsage(w http.ResponseWriter, r *http.Request) {
	d.withQuery(w, r, func(ctx context.Context) {
		d.usage.Lock()
		defer d.usage.Unlock()
		if d.usage.at.IsZero() || time.Since(d.usage.at) > usageCache {
			usage, err := d.store.StorageByApplication(ctx)
			if err != nil {
				d.unavailable(w, r, ctx)
				return
			}
			d.usage.value, d.usage.at = usage, time.Now()
		}
		settings := d.currentSettings()
		d.render(w, r, http.StatusOK, "storage", "Storage by application", struct {
			investigation.StorageUsage
			Computed   time.Time
			OnFull     storage.FullPolicy
			MaxDBBytes int64
		}{d.usage.value, d.usage.at, settings.OnFull, settings.MaxDBBytes})
	})
}

// currentSettings refreshes token names, which a token file reload can change.
func (d *dashboard) currentSettings() Settings {
	settings := d.settings
	if settings.Runtime != nil {
		snapshot := settings.Runtime.Snapshot()
		settings.TraceLinkURL, settings.TraceLinkName = snapshot.URL, snapshot.Name
		settings.ConfigSource.Owners = settings.Runtime.OwnersSource()
		operation := settings.Runtime.OperationsState()
		p := operation.Policy
		settings.RetentionDays, settings.MaxDBBytes, settings.IngestConcurrency, settings.OnFull, settings.Redaction = p.RetentionDays, int64(p.MaxDBMiB)<<20, p.IngestConcurrency, p.OnFull, p.Redaction
		settings.ConfigSource.RetentionDays = operation.Sources["retention_days"]
		settings.ConfigSource.MaxDBMiB = operation.Sources["max_db_mib"]
		settings.ConfigSource.IngestSlots = operation.Sources["ingest_concurrency"]
		settings.ConfigSource.OnFull = operation.Sources["on_full"]
		settings.ConfigSource.Redaction = operation.Sources["redaction"]
		if settings.Notifications != nil {
			notifications := *settings.Notifications
			notifications.Digest = ""
			if p.Digest != "" {
				notifications.Digest = p.Digest + " UTC"
			}
			settings.Notifications = &notifications
		}

	}
	if settings.Tokens.Enabled() {
		settings.IngestTokens = settings.Tokens.Names(access.ScopeIngest)
		settings.IngestApplications = settings.Tokens.IngestApplications()
		settings.ReadTokens = settings.Tokens.Names(access.ScopeRead)
		settings.TriageTokens = settings.Tokens.Names(access.ScopeTriage)
	}
	return settings
}

func (d *dashboard) traceEndpoint(requestHost string) string {
	if d.settings.PublicURL != "" {
		return d.settings.PublicURL + "/v1/traces"
	}
	return traceEndpoint(d.settings.ListenAddr, requestHost)
}

// traceEndpoint returns a URL exporters can use. A wildcard listener such as
// 0.0.0.0 or [::] is reached through the host name the dashboard was opened with.
func traceEndpoint(listenAddr, requestHost string) string {
	address, err := netip.ParseAddrPort(listenAddr)
	if err != nil || !address.Addr().IsUnspecified() {
		return "http://" + listenAddr + "/v1/traces"
	}
	host := requestHost
	if name, _, err := net.SplitHostPort(requestHost); err == nil {
		host = name
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(int(address.Port()))) + "/v1/traces"
}

func route(path string, params url.Values) string {
	if query := params.Encode(); query != "" {
		return path + "?" + query
	}
	return path
}
