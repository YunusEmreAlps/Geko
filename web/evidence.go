package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

// baseURL is the dashboard's address for links pasted into other tools.
func (d *dashboard) baseURL(r *http.Request) string {
	if d.settings.PublicURL != "" {
		return d.settings.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// traceBugEvidence describes a failing trace as Markdown for an issue tracker.
func traceBugEvidence(base string, trace investigation.TraceSummary) string {
	evidence := trace.Evidence
	if evidence == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**%s on %s in %s**\n", failureTitle(evidence.ExceptionType, evidence.Classification),
		inlineCode(evidence.Operation), serviceLabel(evidence.Application, evidence.Service))
	if entry := trace.EntrySpan; entry != nil {
		fmt.Fprintf(&b, "- Request: %s in %s, %s, %s\n", inlineCode(entry.Operation), serviceLabel(entry.Application, entry.Service),
			outcome(trace), formatDuration(entry.DurationNS))
	}
	if detail := oneLine(evidence.ExceptionMessage); detail != "" {
		if evidence.ExceptionType != "" {
			detail = inlineCode(evidence.ExceptionType) + ": " + detail
		}
		fmt.Fprintf(&b, "- Exception: %s\n", detail)
	}
	fmt.Fprintf(&b, "- Received: %s\n", formatTimestamp(trace.LastObserved))
	fmt.Fprintf(&b, "- Trace: %s/traces/%s?span=%s\n", base, trace.TraceID, evidence.SpanID)
	return b.String()
}

// groupBugEvidence describes an error group as Markdown for an issue tracker.
// Groups hold no exception messages, so neither does this.
func groupBugEvidence(base string, group investigation.FailureGroup) string {
	var b strings.Builder
	where := serviceLabel(group.Application, group.Service)
	if group.Environment != "" {
		where += " (" + group.Environment + ")"
	}
	fmt.Fprintf(&b, "**%s on %s in %s**\n", failureTitle(group.ExceptionType, group.Classification), inlineCode(group.Operation), where)
	traces := "traces"
	if group.Count == 1 {
		traces = "trace"
	}
	fmt.Fprintf(&b, "- %d %s, first received %s, last received %s\n", group.Count, traces,
		formatTimestamp(group.FirstObserved), formatTimestamp(group.LastObserved))
	o := group.Outcomes
	fmt.Fprintf(&b, "- Request outcomes: %d 2xx, %d 5xx, %d other HTTP, %d unknown\n", o.HTTP2xx, o.HTTP5xx, o.HTTPOther, o.HTTPUnknown)
	if group.IssueURL != "" {
		fmt.Fprintf(&b, "- Issue: %s\n", group.IssueURL)
	}
	fmt.Fprintf(&b, "- Group: %s\n", route(base+"/failures", url.Values{"group": {investigation.EncodeGroupKey(group.Key)}}))
	fmt.Fprintf(&b, "- Latest example: %s/traces/%s?span=%s\n", base, group.TraceID, group.SpanID)
	return b.String()
}

// inlineCode wraps a value from telemetry in a Markdown code span.
func inlineCode(value string) string {
	return "`" + strings.ReplaceAll(oneLine(value), "`", "'") + "`"
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// traceLink fills the trace ID into a link template such as a Grafana Explore URL.
func traceLink(template, traceID string) string {
	if template == "" {
		return ""
	}
	return strings.NewReplacer("{trace_id}", traceID, "%7Btrace_id%7D", traceID, "%7btrace_id%7d", traceID).Replace(template)
}

// issueLabel shortens an issue link for display, such as jira.example.com/browse/PAY-12.
func issueLabel(link string) string {
	parsed, err := url.Parse(link)
	if err != nil {
		return link
	}
	return parsed.Host + strings.TrimSuffix(parsed.EscapedPath(), "/")
}
