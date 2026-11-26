package sqlite

import (
	"bytes"
	"context"
	"math"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

// ComparisonChoices bounds work before reading JSON fields. It uses the
// existing newest-trace index and per-trace span index, never a global DISTINCT.
func (s *Store) ComparisonChoices(ctx context.Context, since time.Time, service string) (investigation.ComparisonChoices, error) {
	var result investigation.ComparisonChoices
	minObserved := int64(math.MinInt64)
	if !since.IsZero() {
		minObserved = since.UnixNano()
	}
	rows, err := s.read.QueryContext(ctx, comparisonChoicesQuery,
		minObserved, investigation.ChoiceTraceLimit+1, investigation.ChoiceSpanLimit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	versions, runs, services := map[string]bool{}, map[string]bool{}, map[string]bool{}
	traces := 0
	var previous []byte
	// The outer LIMIT bounds scans even if one trace contains many spans.
	count := 0
	for rows.Next() {
		var option investigation.ComparisonChoice
		var version, run string
		var observed int64
		var traceID []byte
		if err := rows.Scan(&option.Service, &option.Namespace, &option.Environment, &version, &run, &observed, &traceID); err != nil {
			return result, err
		}
		if count >= investigation.ChoiceSpanLimit {
			result.Truncated = true
			break
		}
		count++
		if count == 1 || !bytes.Equal(traceID, previous) {
			traces++
			previous = traceID
		}
		if traces > investigation.ChoiceTraceLimit {
			result.Truncated = true
			break
		}
		option.ObservedAt = time.Unix(0, observed).UTC()
		if !services[option.Service] {
			services[option.Service] = true
			if len(result.Services) < investigation.ChoiceLimit {
				result.Services = append(result.Services, option.Service)
			} else {
				result.Truncated = true
			}
		}
		if service != "" && option.Service != service {
			continue
		}
		if version != "" && !versions[version] {
			versions[version] = true
			if len(result.Versions) < investigation.ChoiceLimit {
				option.Value = version
				result.Versions = append(result.Versions, option)
			} else {
				result.Truncated = true
			}
		}
		if investigation.ValidRunID(run) && !runs[run] {
			runs[run] = true
			if len(result.Runs) < investigation.ChoiceLimit {
				option.Value = run
				result.Runs = append(result.Runs, option)
			} else {
				result.Truncated = true
			}
		}
	}
	return result, rows.Err()
}

var comparisonChoicesQuery = `WITH recent AS MATERIALIZED (
 SELECT trace_id,last_observed_ns FROM traces WHERE last_observed_ns>=?
 ORDER BY last_observed_ns DESC,trace_id DESC LIMIT ?
) SELECT s.service_name,s.service_namespace,` + spanEnvironment("s") + `,
 coalesce(json_extract(s.resource_attributes,'$."service.version"'),''),
 coalesce(json_extract(s.resource_attributes,'$."geko.run.id"'),''),recent.last_observed_ns,recent.trace_id
 FROM recent CROSS JOIN spans s ON s.trace_id=recent.trace_id
 LIMIT ?`
