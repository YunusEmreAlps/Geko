package sqlite

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

const evidenceEnvironment = `coalesce(nullif(json_extract(s.resource_attributes, '$."deployment.environment.name"'), ''), json_extract(s.resource_attributes, '$."deployment.environment"'), '')`

const failureGroupKey = `json_array(` + evidenceApplication + `, s.service_namespace, s.service_name, ` + evidenceEnvironment + `, s.name, e.rank, coalesce(e.exception_type,''), coalesce(s.http_status,0))`

func (s *Store) FailureGroups(ctx context.Context, f investigation.FailureFilter, after *investigation.GroupCursor) (investigation.FailureGroups, error) {
	result := investigation.FailureGroups{}
	if f.Limit < 1 || f.Limit > investigation.MaxPageSize || f.After != nil {
		return result, errors.New("invalid group page")
	}
	base, args := traceListQuery(f, true)
	query := `WITH matching AS (
 SELECT ` + failureGroupKey + ` AS group_key, ` + evidenceApplication + ` AS application,
 s.service_namespace AS namespace,s.service_name AS service,` + evidenceEnvironment + ` AS environment,
 s.name AS operation,e.rank,coalesce(e.exception_type,'') AS exception_type,coalesce(s.http_status,0) AS http_status,
 t.first_observed_ns AS first_ns,t.last_observed_ns AS last_ns,r.http_status AS request_status,
 printf('%020d',t.last_observed_ns)||hex(t.trace_id)||hex(s.span_id) AS sample
 ` + base + `)
 SELECT group_key,application,namespace,service,environment,operation,rank,exception_type,http_status,
 count(*),min(first_ns),max(last_ns),substr(max(sample),21,32),substr(max(sample),53,16),
 count(*) FILTER (WHERE request_status BETWEEN 200 AND 299),
 count(*) FILTER (WHERE request_status BETWEEN 500 AND 599),
 count(*) FILTER (WHERE request_status BETWEEN 100 AND 199 OR request_status BETWEEN 300 AND 499),
 count(*) FILTER (WHERE request_status IS NULL OR request_status NOT BETWEEN 100 AND 599)
 FROM matching GROUP BY group_key`
	if after != nil {
		query += ` HAVING max(last_ns) < ? OR (max(last_ns) = ? AND group_key > ?)`
		args = append(args, after.TimeNS, after.TimeNS, after.Key)
	}
	query += ` ORDER BY max(last_ns) DESC,group_key LIMIT ?`
	args = append(args, f.Limit+1)
	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(result.Items) == f.Limit {
			last := result.Items[len(result.Items)-1]
			result.Next = &investigation.GroupCursor{TimeNS: last.LastObserved.UnixNano(), Key: last.Key}
			break
		}
		var group investigation.FailureGroup
		var first, last int64
		var rank int
		if err := rows.Scan(&group.Key, &group.Application, &group.Namespace, &group.Service, &group.Environment,
			&group.Operation, &rank, &group.ExceptionType, &group.HTTPStatus, &group.Count, &first, &last, &group.TraceID, &group.SpanID,
			&group.Outcomes.HTTP2xx, &group.Outcomes.HTTP5xx, &group.Outcomes.HTTPOther, &group.Outcomes.HTTPUnknown,
		); err != nil {
			return result, err
		}
		group.FirstObserved, group.LastObserved = time.Unix(0, first).UTC(), time.Unix(0, last).UTC()
		group.TraceID, group.SpanID = strings.ToLower(group.TraceID), strings.ToLower(group.SpanID)
		group.Classification = map[int]string{1: "exception", 2: "http_5xx", 3: "span_error"}[rank]
		result.Items = append(result.Items, group)
	}
	return result, rows.Err()
}
