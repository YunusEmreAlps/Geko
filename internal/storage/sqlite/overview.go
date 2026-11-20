package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

// OverviewSampleLimit bounds the breakdown. Counting the traces is an
// index-only scan, but describing each one costs a span lookup, so a window
// holding more failures than this reports the breakdown of the most recent
// OverviewSampleLimit of them and says so.
const OverviewSampleLimit = 10000

// overviewCountQuery answers from traces_recent_failures without reading a row.
const overviewCountQuery = `SELECT count(*) FROM traces
        WHERE has_evidence = 1 AND last_observed_ns >= ?`

// overviewQuery describes each sampled trace by its entry span, or by its
// evidence span when no unique root exists. Unlike a trace detail it needs
// neither span of the same trace, so it reads one span per trace. The
// application is resolved here, not stored, so a later reassignment applies.
const overviewQuery = `
WITH recent AS (
    SELECT trace_id, entry_span_id, evidence_span_id FROM traces
    WHERE has_evidence = 1 AND last_observed_ns >= ?
    ORDER BY last_observed_ns DESC LIMIT ?
), operations AS MATERIALIZED (
    SELECT coalesce(r.service_name, s.service_name) AS service,
           CASE WHEN r.span_id IS NOT NULL
                THEN coalesce(nullif(r.service_namespace, ''), ta.application, ra.application, '')
                ELSE coalesce(nullif(s.service_namespace, ''), ta.application, sa.application, '') END AS application,
           coalesce(r.name, s.name) AS operation, count(*) AS count
    FROM recent t
    LEFT JOIN spans r ON r.trace_id = t.trace_id AND r.span_id = t.entry_span_id
    LEFT JOIN spans s ON s.trace_id = t.trace_id
        AND s.span_id = CASE WHEN t.entry_span_id IS NULL THEN t.evidence_span_id END
    LEFT JOIN trace_applications ta ON ta.trace_id = t.trace_id
    LEFT JOIN service_applications ra ON ra.service_name = r.service_name
    LEFT JOIN service_applications sa ON sa.service_name = s.service_name
    GROUP BY 1, 2, 3
)
SELECT service, application, operation, count,
       (SELECT count(*) FROM (SELECT DISTINCT application, service FROM operations))
FROM operations ORDER BY count DESC, application, service, operation LIMIT 5`

func (s *Store) Overview(ctx context.Context, since time.Time) (investigation.Overview, error) {
	var result investigation.Overview
	if err := s.read.QueryRowContext(ctx, overviewCountQuery, since.UnixNano()).Scan(&result.TraceCount); err != nil {
		return result, fmt.Errorf("count failing traces: %w", err)
	}
	if result.TraceCount == 0 {
		return result, nil
	}
	if result.TraceCount > OverviewSampleLimit {
		result.SampledTraces = OverviewSampleLimit
	}
	rows, err := s.read.QueryContext(ctx, overviewQuery, since.UnixNano(), OverviewSampleLimit)
	if err != nil {
		return result, fmt.Errorf("query overview: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var operation investigation.OperationCount
		if err := rows.Scan(&operation.Service, &operation.Application, &operation.Operation, &operation.Count, &result.ServiceCount); err != nil {
			return result, fmt.Errorf("read overview: %w", err)
		}
		result.Operations = append(result.Operations, operation)
	}
	return result, rows.Err()
}
