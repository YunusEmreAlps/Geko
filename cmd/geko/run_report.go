package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

// reportLimit bounds the traces a run report lists and saves.
const reportLimit = 25

// writeRunReport saves a Markdown summary of the run's failures, their
// number, and each failing trace as OTLP JSON that any Geko can import.
func writeRunReport(ctx context.Context, base, token, runID, dir string) error {
	query := url.Values{"run_id": {runID}, "limit": {strconv.Itoa(reportLimit)}}
	body, err := gekoGet(ctx, base+"/api/v1/failures?"+query.Encode(), token)
	if err != nil {
		return err
	}
	var page queryPage[investigation.TraceSummary]
	if err := json.Unmarshal(body, &page); err != nil {
		return fmt.Errorf("decode failures: %w", err)
	}
	traces := filepath.Join(dir, "traces")
	if err := os.MkdirAll(traces, 0o755); err != nil {
		return err
	}
	for _, trace := range page.Items {
		data, err := gekoGet(ctx, base+"/traces/"+trace.TraceID+"/otlp.json", token)
		if err != nil {
			return fmt.Errorf("download trace %s: %w", trace.TraceID, err)
		}
		if err := os.WriteFile(filepath.Join(traces, trace.TraceID+".json"), data, 0o644); err != nil {
			return err
		}
	}
	count := strconv.Itoa(len(page.Items))
	if page.NextCursor != "" {
		count += "+"
	}
	if err := os.WriteFile(filepath.Join(dir, "failures"), []byte(count+"\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(runReport(runID, page.Items, page.NextCursor != "")), 0o644)
}

// runReport describes the failures as Markdown for a pull request or a job
// summary. Every telemetry value goes into a code span, so a planted mention,
// link or image in an exception message renders as plain text.
func runReport(runID string, items []investigation.TraceSummary, more bool) string {
	var report strings.Builder
	if len(items) == 0 {
		fmt.Fprintf(&report, "### Geko: no failure evidence in this run\n\nNo span exported during the run recorded an exception, an HTTP 5xx or an error status.\n\n<sub>Run %s</sub>\n", runID)
		return report.String()
	}
	count := strconv.Itoa(len(items))
	if more {
		count = "More than " + count
	}
	fmt.Fprintf(&report, "### Geko: %s failing %s in this run\n\n", count, plural(len(items), "trace", "traces"))
	report.WriteString("| Request | Evidence | Trace |\n| --- | --- | --- |\n")
	for _, trace := range items {
		request := "unknown request"
		if trace.EntrySpan != nil {
			request = code(trace.EntrySpan.Operation) + " in " + code(trace.EntrySpan.Service)
			if trace.EntrySpan.HTTPStatus != nil {
				request += " · HTTP " + strconv.Itoa(*trace.EntrySpan.HTTPStatus)
			}
		}
		evidence := "none recorded"
		if e := trace.Evidence; e != nil {
			evidence = map[string]string{"exception": "Exception", "http_5xx": "HTTP 5xx", "span_error": "Error status"}[e.Classification]
			if e.ExceptionType != "" {
				evidence += " " + code(e.ExceptionType)
			}
			evidence += " on " + code(e.Operation) + " in " + code(e.Service)
			if e.ExceptionMessage != "" {
				evidence += ": " + code(e.ExceptionMessage)
			}
		}
		fmt.Fprintf(&report, "| %s | %s | `%s` |\n", request, evidence, trace.TraceID)
	}
	report.WriteString("\nEach trace is attached as OTLP JSON. To open one, start `geko serve`, then open **Traces → All traces → Restore a trace** and choose the file.\n\n")
	fmt.Fprintf(&report, "<sub>Run %s, recorded by Geko</sub>\n", runID)
	return report.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// code renders telemetry text as a Markdown code span inside a table cell.
func code(value string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case r == '`':
			return '\''
		case r < ' ':
			return ' '
		}
		return r
	}, value)
	if utf8.RuneCountInString(value) > 120 {
		value = string([]rune(value)[:119]) + "…"
	}
	return "`" + strings.ReplaceAll(strings.TrimSpace(value), "|", `\|`) + "`"
}
