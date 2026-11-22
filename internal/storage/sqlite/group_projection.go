package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

// Same eligibility and column order as migration 017's refresh source. The
// affected rows are materialized first so correlated topology reads run once
// per trace rather than once for each reference to the services expression.
const preparedGroupFields = `trace_id,span_id,namespace,service_name,
 CASE WHEN length(assigned)<=256 AND length(version)<=256 AND length(services)<=4096
           AND json_array_length(services)<=128
           AND NOT EXISTS(SELECT 1 FROM json_each(services) WHERE json_type(value,'$[0]')='null')
      THEN group_key END AS group_key,
 version,first_ns,last_ns,request_status,request_service,request_namespace,
 assigned,has_assignment,pinned,services,bucket`

// Refresh once per affected trace, after root and evidence selection and before
// commit. Late spans and retransmissions can change evidence or receipt times.
func refreshGroupMembers(ctx context.Context, tx *sql.Tx, spans []telemetry.Span) error {
	seen := make(map[telemetry.TraceID]bool)
	ids := make([]telemetry.TraceID, 0, len(spans))
	for i := range spans {
		id := spans[i].TraceID
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for chunk := range slices.Chunk(ids, writeChunkSize) {
		args := make([]any, 0, len(chunk))
		for i := range chunk {
			args = append(args, chunk[i][:])
		}
		query := `WITH batch AS MATERIALIZED(SELECT * FROM failure_group_refresh_input WHERE trace_id IN (?` + strings.Repeat(",?", len(chunk)-1) + `))
 INSERT INTO failure_group_refresh SELECT ` + preparedGroupFields + ` FROM batch`
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("refresh failure group members: %w", writeError(err))
		}
	}
	return nil
}

// Search and evidence-service filters may select another evidence span, so they
// retain the original query. The projection always describes primary evidence.
func projectedGroupMatches(f investigation.FailureFilter) (string, []any, bool) {
	if f.Search != "" || f.EvidenceService != "" || f.RunID != "" || f.AttributeDigest != "" {
		return "", nil, false
	}
	key := `CASE WHEN m.namespace <> '' THEN m.group_key
 ELSE json_set(m.group_key, '$[0]', coalesce(ta.application, sa.application, '')) END`
	query := `SELECT ` + key + ` AS group_key,
 m.first_ns, m.last_ns, m.request_status,
 printf('%020d',m.last_ns)||hex(m.trace_id)||hex(m.span_id) AS sample, m.version, 1 AS weight
 FROM failure_group_member_facts m
 LEFT JOIN trace_applications ta ON ta.trace_id = m.trace_id
 LEFT JOIN service_applications sa ON sa.service_name = m.service_name`
	if f.Service != "" && f.Application != nil {
		query += ` LEFT JOIN service_applications ra ON ra.service_name = m.request_service`
	}
	query += " WHERE m.group_key IS NOT NULL"
	var args []any
	if f.Pinned {
		query += ` AND EXISTS (SELECT 1 FROM trace_pins p WHERE p.trace_id = m.trace_id)`
	}
	if f.Application != nil {
		query += ` AND EXISTS (SELECT 1 FROM trace_services member
 LEFT JOIN service_applications ma ON ma.service_name = member.service_name
 WHERE member.trace_id = m.trace_id AND coalesce(nullif(member.namespace,''),ta.application,ma.application,'') = ?)`
		args = append(args, *f.Application)
	}
	if f.Service != "" {
		query += ` AND coalesce(m.request_service,m.service_name) = ?`
		args = append(args, f.Service)
		if f.Application != nil {
			query += ` AND CASE WHEN m.request_service IS NOT NULL
 THEN coalesce(nullif(m.request_namespace,''),ta.application,ra.application,'')
 ELSE coalesce(nullif(m.namespace,''),ta.application,sa.application,'') END = ?`
			args = append(args, *f.Application)
		}
	}
	if f.HTTPStatus != 0 {
		query += ` AND m.request_status = ?`
		args = append(args, f.HTTPStatus)
	}
	if f.From != nil {
		query += ` AND m.last_ns >= ?`
		args = append(args, f.From.UnixNano())
	}
	if f.To != nil {
		query += ` AND m.last_ns < ?`
		args = append(args, f.To.UnixNano())
	}
	if f.GroupKey != "" {
		query += " AND " + key + " = ?"
		args = append(args, f.GroupKey)
	}
	// Unusually large historical metadata is not duplicated into the cache.
	// The partial index restricts this exact fallback to those exceptional rows.
	legacy, legacyArgs := uncachedGroupMatches(f)
	query += " UNION ALL " + legacy
	args = append(args, legacyArgs...)
	return query, args, true
}

func uncachedGroupMatches(f investigation.FailureFilter) (string, []any) {
	legacy, args := legacyGroupMatches(f)
	legacy = strings.Replace(legacy, "FROM traces t", `FROM failure_group_members uncached INDEXED BY failure_group_members_uncached
 JOIN traces t ON t.trace_id = uncached.trace_id`, 1)
	return legacy + " AND uncached.partition_id IS NULL", args
}
