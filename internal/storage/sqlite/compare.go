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
	err = tx.QueryRowContext(ctx, compareBaselineQuery,
		id[:], source.EntrySpan.Service, source.EntrySpan.Operation, source.EntrySpan.Application).Scan(&rawID)
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

// CROSS JOIN keeps the newest-first scan of traces as the outer loop, so the
// query stops at the first match instead of sorting every candidate span.
var compareBaselineQuery = `
SELECT t.trace_id FROM traces t
CROSS JOIN spans r ON r.trace_id = t.trace_id AND r.span_id = t.entry_span_id
JOIN traces original ON original.trace_id = ?
JOIN spans original_root ON original_root.trace_id = original.trace_id AND original_root.span_id = original.entry_span_id
LEFT JOIN trace_applications ta ON ta.trace_id = t.trace_id
LEFT JOIN service_applications ra ON ra.service_name = r.service_name
WHERE t.has_evidence = 0 AND r.http_status BETWEEN 200 AND 299
  AND r.service_name = ? AND r.name = ? AND ` + rootApplication + ` = ?
  AND r.service_namespace = original_root.service_namespace
  AND ` + spanEnvironment("r") + ` = ` + spanEnvironment("original_root") + `
ORDER BY t.last_observed_ns DESC, t.trace_id DESC LIMIT 1`

func spanEnvironment(alias string) string {
	attrs := alias + ".resource_attributes"
	return `coalesce(nullif(json_extract(` + attrs + `, '$."deployment.environment.name"'), ''),
        json_extract(` + attrs + `, '$."deployment.environment"'), '')`
}
