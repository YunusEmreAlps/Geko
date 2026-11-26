package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

func (d *dashboard) traceMissing(w http.ResponseWriter, r *http.Request, id string) {
	d.render(w, r, http.StatusNotFound, "trace-missing", "Trace not found", struct{ ID, Retry, BackURL string }{
		id, r.URL.RequestURI(), localReturnURL(r.URL.Query().Get("return")),
	})
}

// Only local list destinations can be carried between investigation pages.
func localReturnURL(raw string) string {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\r\n\\") {
		return "/failures"
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.RawPath != "" {
		return "/failures"
	}
	if u.Path != "/failures" && u.Path != "/traces" && u.Path != "/overview" {
		return "/failures"
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "/failures"
	}
	query.Del("return")
	query.Del("trace_id")
	u.RawQuery, u.Fragment = query.Encode(), ""
	return u.String()
}

func withReturn(href, back string) string {
	if back == "" {
		return href
	}
	u, err := url.Parse(href)
	if err != nil || u.IsAbs() || u.Host != "" {
		return href
	}
	q := u.Query()
	q.Set("return", localReturnURL(back))
	u.RawQuery = q.Encode()
	return u.String()
}

func failureTable(items []investigation.TraceSummary, back string) any {
	return struct {
		Items     []investigation.TraceSummary
		ReturnURL string
	}{items, localReturnURL(back)}
}

func groupTable(items []failureGroupView, back string) any {
	return struct {
		Items     []failureGroupView
		ReturnURL string
	}{items, localReturnURL(back)}
}

func (view *traceView) preserveReturn(back string) {
	view.BackURL = localReturnURL(back)
	if back == "" {
		return
	}
	view.CompareURL = withReturn(view.CompareURL, back)
	view.mapTraceLinks(func(href string) string { return withReturn(href, back) })
}

func (view *traceView) preserveReading() {
	view.mapTraceLinks(func(href string) string {
		u, err := url.Parse(href)
		if err != nil {
			return href
		}
		q := u.Query()
		q.Set("reading", "wide")
		u.RawQuery = q.Encode()
		return u.String()
	})
}

func (view *traceView) mapTraceLinks(convert func(string) string) {
	for _, item := range []*string{&view.Parent, &view.First, &view.Next, &view.EvidenceFirst, &view.EvidenceNext, &view.ServiceMap.EnvironmentToggle} {
		if *item != "" {
			*item = convert(*item)
		}
	}
	for i := range view.Rows {
		view.Rows[i].URL = convert(view.Rows[i].URL)
	}
	for i := range view.Evidence {
		view.Evidence[i].URL = convert(view.Evidence[i].URL)
	}
	if view.FeaturedEvidence != nil {
		view.FeaturedEvidence.URL = convert(view.FeaturedEvidence.URL)
	}
	for i := range view.OtherEvidence {
		view.OtherEvidence[i].URL = convert(view.OtherEvidence[i].URL)
	}
	for i := range view.ServiceMap.Nodes {
		view.ServiceMap.Nodes[i].URL = convert(view.ServiceMap.Nodes[i].URL)
	}
}
