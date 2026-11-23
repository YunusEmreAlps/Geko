package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/investigation"
)

// reportLimit bounds the traces a run report lists and saves.
const reportLimit = 25

type runReportStatus struct {
	RunID           string `json:"run_id"`
	TelemetryStatus string `json:"telemetry_status"`
	Failures        *int   `json:"failures"`
	Truncated       bool   `json:"truncated"`
	// Limit is the most traces a report lists; a truncated one lists exactly this many.
	Limit int `json:"limit"`
}

func saveRunReportStatus(dir string, status runReportStatus) error {
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0o644)
}

// writeRunReport saves a Markdown summary of the run's failures, their
// number, and each failing trace as OTLP JSON that any Geko can import.
func writeRunReport(ctx context.Context, base, token, runID, dir string) (reportErr error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Invalidate earlier successful counts and traces before any fallible
	// network work, so a reused directory holds only this run's evidence.
	if err := os.Remove(filepath.Join(dir, "failures")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(filepath.Join(dir, "comparison.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.RemoveAll(filepath.Join(dir, "traces")); err != nil {
		return err
	}
	unavailable := runReportStatus{RunID: runID, TelemetryStatus: "unavailable", Limit: reportLimit}
	defer func() {
		if reportErr != nil {
			_ = saveRunSnapshotFile(dir, investigation.UnavailableRun(runID))
			reportErr = errors.Join(reportErr, saveRunReportStatus(dir, unavailable))
			if err := os.Remove(filepath.Join(dir, "failures")); err != nil && !errors.Is(err, os.ErrNotExist) {
				reportErr = errors.Join(reportErr, err)
			}
			reportErr = errors.Join(reportErr, os.WriteFile(filepath.Join(dir, "report.md"), []byte("### Geko: telemetry report unavailable\n\nThe run's failure evidence could not be collected completely. The failure count is unknown; this is not a zero-failure result. Check the Geko command's diagnostic output.\n"), 0o644))
		}
	}()
	if err := saveRunSnapshotFile(dir, investigation.UnavailableRun(runID)); err != nil {
		return err
	}
	if err := saveRunReportStatus(dir, unavailable); err != nil {
		return err
	}
	query := url.Values{"run_id": {runID}, "limit": {strconv.Itoa(reportLimit)}}
	body, err := gekoGet(ctx, base+"/api/v1/failures?"+query.Encode(), token)
	if err != nil {
		return err
	}
	var page queryPage[investigation.TraceSummary]
	if err := json.Unmarshal(body, &page); err != nil {
		return fmt.Errorf("decode failures: %w", err)
	}
	if page.RunID != runID || page.Items == nil || len(page.Items) > reportLimit ||
		(page.NextCursor != "" && len(page.Items) != reportLimit) {
		return errors.New("invalid or unsupported run-filtered failures response")
	}
	traces := filepath.Join(dir, "traces")
	if err := os.MkdirAll(traces, 0o755); err != nil {
		return err
	}
	for _, trace := range page.Items {
		if _, err := api.ParseTraceID(trace.TraceID); err != nil {
			return errors.New("invalid trace ID in run report response")
		}
		data, err := gekoGet(ctx, base+"/traces/"+trace.TraceID+"/otlp.json", token)
		if err != nil {
			return fmt.Errorf("download trace %s: %w", trace.TraceID, err)
		}
		if !json.Valid(data) {
			return errors.New("invalid trace JSON in run report response")
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
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(runReport(runID, page.Items, page.NextCursor != "")), 0o644); err != nil {
		return err
	}
	known := len(page.Items)
	snapshot, err := loadRunSnapshot(ctx, runID, base, token)
	if err != nil {
		snapshot = investigation.UnavailableRun(runID)
	}
	if err := saveRunSnapshotFile(dir, snapshot); err != nil {
		return err
	}
	return saveRunReportStatus(dir, runReportStatus{RunID: runID, TelemetryStatus: "available", Failures: &known, Truncated: page.NextCursor != "", Limit: reportLimit})
}

func saveRunSnapshotFile(dir string, snapshot investigation.RunSnapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "run.json"), append(data, '\n'), 0o644)
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
