package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage"
)

func serviceLabel(application, service string) string {
	if application == "" {
		return service
	}
	return application + " / " + service
}

func (d *dashboard) applications(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	after := params.Get("after")
	if !validApplicationField(after) {
		d.problem(w, r, http.StatusBadRequest, "Invalid mapping page", "Open Applications again.")
		return
	}
	d.withQuery(w, r, func(ctx context.Context) {
		rules, err := d.store.ServiceApplications(ctx, after)
		if err != nil {
			d.unavailable(w, r, ctx)
			return
		}
		view := struct {
			Rules       []investigation.ServiceApplication
			Next        string
			HasPrevious bool
			Saved       bool
			Changed     *int64
		}{Rules: rules, HasPrevious: after != "", Saved: params.Get("saved") == "1"}
		if len(rules) > 25 {
			view.Rules = rules[:25]
			view.Next = route("/applications", url.Values{"after": {rules[24].Service}})
		}
		if changed, err := strconv.ParseInt(params.Get("changed"), 10, 64); err == nil && changed >= 0 {
			view.Changed = &changed
		}
		d.render(w, http.StatusOK, "applications", "Applications", view)
	})
}

func validApplicationField(value string) bool {
	return len(value) <= 256 && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) == -1
}

func (d *dashboard) changeApplications(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		d.problem(w, r, http.StatusBadRequest, "Invalid application settings", "Check the form and try again.")
		return
	}
	service := strings.TrimSpace(r.PostForm.Get("service"))
	application := strings.TrimSpace(r.PostForm.Get("application"))
	if !validApplicationField(service) || !validApplicationField(application) {
		d.problem(w, r, http.StatusBadRequest, "Invalid name", "Use at most 256 bytes per name without control characters.")
		return
	}
	action := r.PostForm.Get("action")
	switch action {
	case "mapping", "mapping-remove":
		if service == "" {
			d.problem(w, r, http.StatusBadRequest, "Service name required", "Enter the exact service name recorded in the trace.")
			return
		}
		if action == "mapping-remove" {
			application = ""
		} else if application == "" {
			d.problem(w, r, http.StatusBadRequest, "Application name required", "Enter the application to associate with this service.")
			return
		}
		d.withQuery(w, r, func(ctx context.Context) {
			if err := d.store.SetServiceApplication(ctx, service, application); err != nil {
				d.applicationWriteError(w, r, ctx, err)
				return
			}
			http.Redirect(w, r, "/applications?saved=1", http.StatusSeeOther)
		})
	case "assign", "assign-clear":
		if action == "assign-clear" {
			application = ""
		} else if application == "" {
			d.problem(w, r, http.StatusBadRequest, "Application name required", "Enter the application to assign to this time range.")
			return
		}
		const minuteUTC = "2006-01-02T15:04"
		from, fromErr := time.Parse(minuteUTC, r.PostForm.Get("from"))
		to, toErr := time.Parse(minuteUTC, r.PostForm.Get("to"))
		if fromErr != nil || toErr != nil || !from.Before(to) ||
			!from.Equal(time.Unix(0, from.UnixNano())) || !to.Equal(time.Unix(0, to.UnixNano())) {
			d.problem(w, r, http.StatusBadRequest, "Invalid UTC time range", "Choose a start and a later end time.")
			return
		}
		d.withQuery(w, r, func(ctx context.Context) {
			changed, err := d.store.AssignTraceApplication(ctx, from, to, service, application)
			if err != nil {
				d.applicationWriteError(w, r, ctx, err)
				return
			}
			http.Redirect(w, r, route("/applications", url.Values{"changed": {strconv.FormatInt(changed, 10)}}), http.StatusSeeOther)
		})
	case "assign-discard":
		d.withQuery(w, r, func(ctx context.Context) {
			if err := d.store.DiscardAssignmentProgress(ctx); err != nil {
				d.applicationWriteError(w, r, ctx, err)
				return
			}
			http.Redirect(w, r, "/applications", http.StatusSeeOther)
		})
	default:
		d.problem(w, r, http.StatusBadRequest, "Invalid application action", "Open Applications again.")
	}
}

func (d *dashboard) applicationWriteError(w http.ResponseWriter, r *http.Request, ctx context.Context, err error) {
	switch {
	case errors.Is(err, investigation.ErrAssignmentInProgress):
		d.problem(w, r, http.StatusConflict, "Historical assignment in progress", "Retry the same inputs to resume an interrupted update, or discard its progress on the Applications page.")
	case errors.Is(err, context.DeadlineExceeded):
		d.problem(w, r, http.StatusGatewayTimeout, "Historical update timed out", "Submit the same inputs to continue from the last completed batch.")
	case errors.Is(err, storage.ErrCapacity):
		d.problem(w, r, http.StatusServiceUnavailable, "Trace storage capacity reached", "Check storage usage in Settings and try again.")
	default:
		d.unavailable(w, r, ctx)
	}
}
