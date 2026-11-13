package sqlite

import (
	"context"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

const versionAttribute = `json_extract(r.resource_attributes, '$."service.version"')`

// VersionComparisons compares request cohorts with matching entry-span identities.
func (s *Store) VersionComparisons(ctx context.Context, f investigation.RegressionFilter) ([]investigation.VersionComparison, error) {
	rows, err := s.read.QueryContext(ctx, `
WITH samples AS (
    SELECT `+rootApplication+` AS application, r.service_namespace AS namespace,
           r.service_name AS service, `+spanEnvironment("r")+` AS environment,
           r.name AS operation, `+versionAttribute+` AS version, t.has_evidence,
           CASE WHEN r.end_ns >= r.start_ns AND typeof(r.end_ns - r.start_ns) = 'integer'
                THEN r.end_ns - r.start_ns END AS duration
    FROM traces t
    JOIN spans r ON r.trace_id = t.trace_id AND r.span_id = t.entry_span_id
    LEFT JOIN trace_applications ta ON ta.trace_id = t.trace_id
    LEFT JOIN service_applications ra ON ra.service_name = r.service_name
    WHERE t.last_observed_ns >= ? AND `+versionAttribute+` IN (?, ?)
      AND (? = '' OR r.service_name = ?)
), cohorts AS MATERIALIZED (
    SELECT application, namespace, service, environment, operation, version,
           count(*) AS total, sum(has_evidence) AS failed,
           count(duration) AS timed, avg(duration) AS mean_ns
    FROM samples GROUP BY 1, 2, 3, 4, 5, 6
)
SELECT b.application, b.namespace, b.service, b.environment, b.operation,
       b.total, b.failed, b.timed, CAST(b.mean_ns AS INTEGER),
       c.total, c.failed, c.timed, CAST(c.mean_ns AS INTEGER),
       b.total >= ? AND c.total >= ? AND
          100.0 * c.failed / c.total - 100.0 * b.failed / b.total >= 5 AS failure_regression,
       coalesce(b.timed >= ? AND c.timed >= ? AND b.mean_ns > 0 AND c.mean_ns >= b.mean_ns * 1.2, 0) AS latency_regression
FROM cohorts b JOIN cohorts c USING (application, namespace, service, environment, operation)
WHERE b.version = ? AND c.version = ?
ORDER BY failure_regression DESC, latency_regression DESC, c.total DESC,
         b.application, b.namespace, b.service, b.environment, b.operation
LIMIT 51`, f.Since.UnixNano(), f.Baseline, f.Candidate, f.Service, f.Service,
		investigation.RegressionMinSamples, investigation.RegressionMinSamples,
		investigation.RegressionMinSamples, investigation.RegressionMinSamples, f.Baseline, f.Candidate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var comparisons []investigation.VersionComparison
	for rows.Next() {
		var row investigation.VersionComparison
		if err := rows.Scan(&row.Application, &row.Namespace, &row.Service, &row.Environment, &row.Operation,
			&row.Baseline.Traces, &row.Baseline.Failures, &row.Baseline.Timed, &row.Baseline.MeanNS,
			&row.Candidate.Traces, &row.Candidate.Failures, &row.Candidate.Timed, &row.Candidate.MeanNS,
			&row.FailureRegression, &row.LatencyRegression); err != nil {
			return nil, err
		}
		comparisons = append(comparisons, row)
	}
	return comparisons, rows.Err()
}
