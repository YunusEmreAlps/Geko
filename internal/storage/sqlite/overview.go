package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

// Overview uses evidence only as a fallback when no unique root exists.
// Unlike a trace detail, it does not need both spans for the same trace.
const overviewJoins = traceSummaryJoinStart + evidenceForTrace +
	" AND r.span_id IS NULL ORDER BY " + evidenceOrder + traceSummaryEnd

func (s *Store) Overview(ctx context.Context, since time.Time) (investigation.Overview, error) {
	var result investigation.Overview
	rows, err := s.read.QueryContext(ctx, `
WITH operations AS MATERIALIZED (
    SELECT coalesce(r.service_name, s.service_name) AS service,
           `+rootOrEvidenceApplication+` AS application,
           coalesce(r.name, s.name) AS operation, count(*) AS count
    `+overviewJoins+`
    WHERE t.has_evidence = 1 AND t.last_observed_ns >= ?
    GROUP BY 1, 2, 3
)
SELECT service, application, operation, count, (SELECT sum(count) FROM operations),
       (SELECT count(*) FROM (SELECT DISTINCT application, service FROM operations))
FROM operations ORDER BY count DESC, application, service, operation LIMIT 5`, since.UnixNano())
	if err != nil {
		return result, fmt.Errorf("query overview: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var operation investigation.OperationCount
		if err := rows.Scan(&operation.Service, &operation.Application, &operation.Operation, &operation.Count, &result.TraceCount, &result.ServiceCount); err != nil {
			return result, fmt.Errorf("read overview: %w", err)
		}
		result.Operations = append(result.Operations, operation)
	}
	return result, rows.Err()
}
