package sqlite

import (
	"context"
	"database/sql"
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
	source, err := traceSummary(ctx, tx, id)
	if err != nil {
		return source, nil, err
	}
	if source.Evidence == nil || source.EntrySpan == nil {
		return source, nil, tx.Commit()
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
		return source, nil, tx.Commit()
	}
	if err != nil {
		return source, nil, err
	}
	copy(baselineID[:], rawID)
	baseline, err := traceSummary(ctx, tx, baselineID)
	if err != nil {
		return source, nil, err
	}
	return source, &baseline, tx.Commit()
}

// recentBaselineTraces bounds the newest-first scan. It finds a recent
// success of a busy operation within a few pages.
const recentBaselineTraces = 1000

var baselineMatch = `
JOIN traces original ON original.trace_id = ?
JOIN spans original_root ON original_root.trace_id = original.trace_id AND original_root.span_id = original.entry_span_id
LEFT JOIN trace_applications ta ON ta.trace_id = t.trace_id
LEFT JOIN service_applications ra ON ra.service_name = r.service_name
WHERE t.has_evidence = 0 AND r.kind = 2 AND r.parent_span_id IS NULL AND r.http_status BETWEEN 200 AND 299
  AND r.service_name = ? AND r.name = ? AND ` + rootApplication + ` = ?
  AND r.service_namespace = original_root.service_namespace
  AND ` + spanEnvironment("r") + ` = ` + spanEnvironment("original_root") + `
ORDER BY t.last_observed_ns DESC, t.trace_id DESC LIMIT 1`

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
