package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

// CompareTrace reads the source and its latest matching successful request
// from a single snapshot. A nil baseline means no comparable trace was found.
func (s *Store) CompareTrace(ctx context.Context, id telemetry.TraceID) (investigation.TraceSummary, *investigation.TraceSummary, error) {
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return investigation.TraceSummary{}, nil, err
	}
	defer tx.Rollback()
	source, baseline, err := compareTrace(ctx, tx, id)
	if err != nil {
		return source, baseline, err
	}
	return source, baseline, tx.Commit()
}

func compareTrace(ctx context.Context, tx *sql.Tx, id telemetry.TraceID) (investigation.TraceSummary, *investigation.TraceSummary, error) {
	source, err := traceSummary(ctx, tx, id)
	if err != nil {
		return source, nil, err
	}
	if source.Evidence == nil || source.EntrySpan == nil {
		return source, nil, nil
	}

	var baselineID telemetry.TraceID
	var rawID []byte
	args := []any{id[:], source.EntrySpan.Service, source.EntrySpan.Operation, source.EntrySpan.Application}
	// A match among the newest successes is the newest match overall. Without
	// one, look the operation up instead of reading every older success.
	err = tx.QueryRowContext(ctx, recentBaselineQuery, append([]any{recentBaselineTraces}, args...)...).Scan(&rawID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, operationBaselineQuery, args...).Scan(&rawID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return source, nil, nil
	}
	if err != nil {
		return source, nil, err
	}
	copy(baselineID[:], rawID)
	baseline, err := traceSummary(ctx, tx, baselineID)
	if err != nil {
		return source, nil, err
	}
	return source, &baseline, nil
}

// TraceComparison collects both summaries and bounded span facts in one WAL
// read snapshot. Existing CLI/API CompareTrace callers keep their lighter read.
func (s *Store) TraceComparison(ctx context.Context, id telemetry.TraceID) (investigation.TraceComparison, error) {
	return s.traceComparison(ctx, id, nil)
}

// TraceComparisonWithBaseline validates a point-selected reference in the same
// read snapshot as both summaries and bounded span facts. No automatic fallback.
func (s *Store) TraceComparisonWithBaseline(ctx context.Context, id, baseline telemetry.TraceID) (investigation.TraceComparison, error) {
	return s.traceComparison(ctx, id, &baseline)
}
func (s *Store) traceComparison(ctx context.Context, id telemetry.TraceID, selected *telemetry.TraceID) (investigation.TraceComparison, error) {
	var result investigation.TraceComparison
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if selected == nil {
		result.Source, result.Baseline, err = compareTrace(ctx, tx, id)
	} else {
		result.Source, err = traceSummary(ctx, tx, id)
		if err == nil {
			if result.Source.Evidence == nil || result.Source.EntrySpan == nil {
				return result, investigation.ErrComparisonSource
			}
			baseline, readErr := traceSummary(ctx, tx, *selected)
			if errors.Is(readErr, investigation.ErrTraceNotFound) {
				return result, investigation.ErrBaselineNotFound
			}
			if readErr != nil {
				return result, readErr
			}
			var rawID []byte
			entry := result.Source.EntrySpan
			readErr = tx.QueryRowContext(ctx, selectedBaselineQuery, id[:], entry.Service, entry.Operation, entry.Application, selected[:]).Scan(&rawID)
			if errors.Is(readErr, sql.ErrNoRows) {
				return result, investigation.ErrBaselineIneligible
			}
			if readErr != nil {
				return result, readErr
			}
			result.Baseline = &baseline
		}
	}
	if err != nil {
		return result, err
	}
	if result.Baseline == nil {
		return result, tx.Commit()
	}
	result.SourceSpans, err = comparisonSpans(ctx, tx, id)
	if err != nil {
		return result, err
	}
	var other telemetry.TraceID
	// The baseline ID came from this database, in canonical hexadecimal form.
	if _, err = hex.Decode(other[:], []byte(result.Baseline.TraceID)); err != nil {
		return result, err
	}
	result.BaselineSpans, err = comparisonSpans(ctx, tx, other)
	if err != nil {
		return result, err
	}
	result.Truncated = result.Source.Window != nil && result.Source.Window.SpanCount > len(result.SourceSpans) ||
		result.Baseline.Window != nil && result.Baseline.Window.SpanCount > len(result.BaselineSpans)
	return result, tx.Commit()
}

func comparisonSpans(ctx context.Context, tx *sql.Tx, id telemetry.TraceID) ([]investigation.ComparisonSpan, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.span_id,s.service_name,s.name,s.http_status,s.start_ns,s.end_ns,`+evidenceApplication+`,
 s.service_namespace,`+spanEnvironment("s")+`,s.kind,
 EXISTS(SELECT 1 FROM span_evidence e WHERE e.trace_id=s.trace_id AND e.span_id=s.span_id)
 FROM spans s LEFT JOIN trace_applications ta ON ta.trace_id=s.trace_id
 LEFT JOIN service_applications sa ON sa.service_name=s.service_name
 WHERE s.trace_id=? ORDER BY s.start_ns,s.span_id LIMIT ?`, id[:], investigation.MapSpanLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var spans []investigation.ComparisonSpan
	for rows.Next() {
		var summary spanSummaryRow
		var span investigation.ComparisonSpan
		fields := append(summary.fields(), &span.Namespace, &span.Environment, &span.Kind, &span.HasEvidence)
		if err := rows.Scan(fields...); err != nil {
			return nil, err
		}
		span.SpanSummary = *summary.summary()
		spans = append(spans, span)
	}
	return spans, rows.Err()
}

// recentBaselineTraces bounds the newest-first scan. It finds a recent
// success of a busy operation within a few pages.
const recentBaselineTraces = 1000

var baselineConditions = `
JOIN traces original ON original.trace_id = ?
JOIN spans original_root ON original_root.trace_id = original.trace_id AND original_root.span_id = original.entry_span_id
LEFT JOIN trace_applications ta ON ta.trace_id = t.trace_id
LEFT JOIN service_applications ra ON ra.service_name = r.service_name
WHERE t.has_evidence = 0 AND r.kind = 2 AND r.parent_span_id IS NULL AND r.http_status BETWEEN 200 AND 299
  AND r.service_name = ? AND r.name = ? AND ` + rootApplication + ` = ?
  AND r.service_namespace = original_root.service_namespace
  AND ` + spanEnvironment("r") + ` = ` + spanEnvironment("original_root") + `
`

var baselineMatch = baselineConditions + `ORDER BY t.last_observed_ns DESC,t.trace_id DESC LIMIT 1`

// recentBaselineQuery scans the newest successful traces. CROSS JOIN keeps
// that scan as the outer loop, so it stops at the first match instead of
// sorting every candidate span.
var recentBaselineQuery = `
SELECT t.trace_id FROM (SELECT trace_id, entry_span_id, last_observed_ns, has_evidence FROM traces
    WHERE has_evidence = 0 ORDER BY last_observed_ns DESC, trace_id DESC LIMIT ?) t
CROSS JOIN spans r ON r.trace_id = t.trace_id AND r.span_id = t.entry_span_id` + baselineMatch

// operationBaselineQuery reads only the entry spans of the operation.
var operationBaselineQuery = `
SELECT t.trace_id FROM spans r INDEXED BY spans_entry_operations
JOIN traces t ON t.trace_id = r.trace_id AND t.entry_span_id = r.span_id` + baselineMatch

func spanEnvironment(alias string) string {
	attrs := alias + ".resource_attributes"
	return `coalesce(nullif(json_extract(` + attrs + `, '$."deployment.environment.name"'), ''),
        json_extract(` + attrs + `, '$."deployment.environment"'), '')`
}

// Primary-key lookup; eligibility is shared with automatic matching.
var selectedBaselineQuery = `SELECT t.trace_id FROM traces t
 JOIN spans r ON r.trace_id=t.trace_id AND r.span_id=t.entry_span_id` + baselineConditions + ` AND t.trace_id=?`
