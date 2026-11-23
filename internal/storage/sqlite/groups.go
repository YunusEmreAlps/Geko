package sqlite

import (
	"context"
	"database/sql"
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
	if f.Application != nil {
		known, err := s.knownApplication(ctx, *f.Application)
		if err != nil {
			return result, err
		}
		if !known {
			return result, nil
		}
	}
	query, args, err := failureGroupsQuery(f, after)
	if err != nil {
		return result, err
	}
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
		var first, last, changed, noted int64
		var rank int
		if err := rows.Scan(&group.Key, &group.Application, &group.Namespace, &group.Service, &group.Environment,
			&group.Operation, &rank, &group.ExceptionType, &group.HTTPStatus, &group.Count, &first, &last, &group.TraceID, &group.SpanID,
			&group.Outcomes.HTTP2xx, &group.Outcomes.HTTP5xx, &group.Outcomes.HTTPOther, &group.Outcomes.HTTPUnknown,
			&group.State, &changed, &group.ResolvedVersion, &group.ChangedBy,
			&group.Note, &group.IssueURL, &noted, &group.NoteBy,
		); err != nil {
			return result, err
		}
		group.FirstObserved, group.LastObserved = time.Unix(0, first).UTC(), time.Unix(0, last).UTC()
		group.TraceID, group.SpanID = strings.ToLower(group.TraceID), strings.ToLower(group.SpanID)
		group.Classification = map[int]string{1: "exception", 2: "http_5xx", 3: "span_error"}[rank]
		if group.State != investigation.GroupOpen {
			at := time.Unix(0, changed).UTC()
			group.StateChanged = &at
		}
		if noted != 0 {
			at := time.Unix(0, noted).UTC()
			group.NoteChanged = &at
		}
		result.Items = append(result.Items, group)
	}
	return result, rows.Err()
}

func failureGroupsQuery(f investigation.FailureFilter, after *investigation.GroupCursor) (string, []any, error) {
	matching, args, ok := summarizedGroupMatches(f)
	if !ok {
		matching, args, ok = projectedGroupMatches(f)
	}
	if !ok {
		matching, args = legacyGroupMatches(f)
	}
	return assembleGroupsQuery(f, after, matching, args)
}

func legacyGroupMatches(f investigation.FailureFilter) (string, []any) {
	base, args := traceListQuery(f, true, true)
	return `SELECT ` + failureGroupKey + ` AS group_key,
 t.first_observed_ns AS first_ns,t.last_observed_ns AS last_ns,r.http_status AS request_status,
 printf('%020d',t.last_observed_ns)||hex(t.trace_id)||hex(s.span_id) AS sample,
 coalesce(json_extract(s.resource_attributes, '$."service.version"'),'') AS version, 1 AS weight
 ` + base, args
}

func assembleGroupsQuery(f investigation.FailureFilter, after *investigation.GroupCursor, matching string, args []any) (string, []any, error) {
	if _, ok := groupStateCondition[f.GroupState]; !ok {
		return "", nil, errors.New("invalid group state")
	}
	// One pass over the matching traces both groups them and finds the ones
	// that reopen a resolved group.
	query := `WITH matching AS (` + matching + `), grouped AS (
 SELECT m.group_key,
 json_extract(m.group_key,'$[0]') AS application,json_extract(m.group_key,'$[1]') AS namespace,
 json_extract(m.group_key,'$[2]') AS service,json_extract(m.group_key,'$[3]') AS environment,
 json_extract(m.group_key,'$[4]') AS operation,json_extract(m.group_key,'$[5]') AS rank,
 json_extract(m.group_key,'$[6]') AS exception_type,json_extract(m.group_key,'$[7]') AS http_status,
 sum(weight) AS traces,min(first_ns) AS first_ns,max(last_ns) AS last_ns,substr(max(sample),21,32) AS trace_id,substr(max(sample),53,16) AS span_id,
 coalesce(sum(weight) FILTER (WHERE request_status BETWEEN 200 AND 299),0) AS ok,
 coalesce(sum(weight) FILTER (WHERE request_status BETWEEN 500 AND 599),0) AS server_error,
 coalesce(sum(weight) FILTER (WHERE request_status BETWEEN 100 AND 199 OR request_status BETWEEN 300 AND 499),0) AS other,
 coalesce(sum(weight) FILTER (WHERE request_status IS NULL OR request_status NOT BETWEEN 100 AND 599),0) AS unknown,
 max(last_ns) FILTER (WHERE gs.state = 'resolved' AND last_ns > gs.changed_ns AND (gs.version = '' OR m.version = gs.version)) AS recurred_ns
 FROM matching m LEFT JOIN group_states gs ON gs.group_key = m.group_key GROUP BY m.group_key)
 SELECT * FROM (SELECT g.group_key,g.application,g.namespace,g.service,g.environment,g.operation,g.rank,g.exception_type,g.http_status,
 g.traces,g.first_ns,g.last_ns,g.trace_id,g.span_id,g.ok,g.server_error,g.other,g.unknown,
 CASE WHEN gs.state IS NULL THEN 'open' WHEN gs.state = 'muted' THEN 'muted'
 WHEN g.recurred_ns IS NOT NULL THEN 'reopened' ELSE 'resolved' END AS state,
 coalesce(gs.changed_ns,0) AS changed_ns,coalesce(gs.version,'') AS resolved_version,coalesce(gs.changed_by,'') AS changed_by,
 coalesce(gn.note,'') AS note,coalesce(gn.issue_url,'') AS issue_url,coalesce(gn.changed_ns,0) AS note_ns,coalesce(gn.changed_by,'') AS note_by
 FROM grouped g LEFT JOIN group_states gs USING (group_key) LEFT JOIN group_notes gn USING (group_key))
 WHERE ` + groupStateCondition[f.GroupState]
	if after != nil {
		query += ` AND (last_ns < ? OR (last_ns = ? AND group_key > ?))`
		args = append(args, after.TimeNS, after.TimeNS, after.Key)
	}
	query += ` ORDER BY last_ns DESC,group_key LIMIT ?`
	args = append(args, f.Limit+1)
	return query, args, nil
}

var groupStateCondition = map[investigation.GroupState]string{
	"":                          `state IN ('open', 'reopened')`,
	investigation.GroupResolved: `state = 'resolved'`,
	investigation.GroupMuted:    `state = 'muted'`,
	investigation.GroupAll:      `1`,
}

// SetGroupState resolves or mutes a group, or reopens it when state is open.
// A resolved group with a version reopens only for traces from that version.
// by names who made the change.
func (s *Store) SetGroupState(ctx context.Context, key string, state investigation.GroupState, version, by string, now time.Time) error {
	if _, err := investigation.DecodeGroupKey(investigation.EncodeGroupKey(key)); err != nil {
		return err
	}
	if len(version) > 256 || version != "" && state != investigation.GroupResolved {
		return errors.New("a version of up to 256 bytes applies to resolved groups only")
	}
	if len(by) > 256 {
		return errors.New("the name of who changed a group is limited to 256 bytes")
	}
	switch state {
	case investigation.GroupOpen:
		_, err := s.db.ExecContext(ctx, "DELETE FROM group_states WHERE group_key = ?", key)
		return s.changed(writeError(err))
	case investigation.GroupResolved, investigation.GroupMuted:
		return s.changed(s.withinCapacity(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO group_states (group_key, state, changed_ns, version, changed_by) VALUES (?, ?, ?, ?, ?)
 ON CONFLICT (group_key) DO UPDATE SET state = excluded.state, changed_ns = excluded.changed_ns, version = excluded.version,
 changed_by = excluded.changed_by`, key, string(state), now.UnixNano(), version, by)
			return err
		}))
	default:
		return errors.New("state must be open, resolved or muted")
	}
}

// SetGroupNote stores a group's note and issue link, replacing earlier ones.
// Both empty removes them. by names who made the change.
func (s *Store) SetGroupNote(ctx context.Context, key, note, issueURL, by string, now time.Time) error {
	if _, err := investigation.DecodeGroupKey(investigation.EncodeGroupKey(key)); err != nil {
		return err
	}
	if err := investigation.ValidateGroupNote(note, issueURL); err != nil {
		return err
	}
	if len(by) > 256 {
		return errors.New("the name of who changed a group is limited to 256 bytes")
	}
	if note == "" && issueURL == "" {
		_, err := s.db.ExecContext(ctx, "DELETE FROM group_notes WHERE group_key = ?", key)
		return s.changed(writeError(err))
	}
	return s.changed(s.withinCapacity(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO group_notes (group_key, note, issue_url, changed_ns, changed_by) VALUES (?, ?, ?, ?, ?)
 ON CONFLICT (group_key) DO UPDATE SET note = excluded.note, issue_url = excluded.issue_url, changed_ns = excluded.changed_ns,
 changed_by = excluded.changed_by`, key, note, issueURL, now.UnixNano(), by)
		return err
	}))
}

// MarkGroupNotified records that a reopened group's notification was sent.
func (s *Store) MarkGroupNotified(ctx context.Context, key string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE group_states SET notified_ns = ? WHERE group_key = ?", now.UnixNano(), key)
	return writeError(err)
}

// GroupNotified reports when a group's last notification was sent, or zero.
func (s *Store) GroupNotified(ctx context.Context, key string) (time.Time, error) {
	var notified int64
	err := s.read.QueryRowContext(ctx, "SELECT notified_ns FROM group_states WHERE group_key = ?", key).Scan(&notified)
	if errors.Is(err, sql.ErrNoRows) || notified == 0 {
		return time.Time{}, nil
	}
	return time.Unix(0, notified).UTC(), err
}

// groupVersionEdge is how many of the oldest and of the newest version pairs
// GroupVersions returns; the ones between them are counted but omitted.
const groupVersionEdge = 10

// GroupVersions reports the versions a group's traces came from, oldest first,
// so a reader can see which release it started in and which is the latest.
// With more than twice groupVersionEdge pairs, it returns the oldest and the
// newest ones and total counts them all. It examines every failing trace,
// like a group view without a time range.
func (s *Store) GroupVersions(ctx context.Context, key string) (versions []investigation.GroupVersion, total int, err error) {
	base, args := traceListQuery(investigation.FailureFilter{GroupKey: key}, true, true)
	rows, err := s.read.QueryContext(ctx, `WITH pairs AS (SELECT coalesce(t.entry_version, '') AS entry,
 coalesce(json_extract(s.resource_attributes, '$."service.version"'), '') AS evidence,
 count(*) AS traces, min(t.first_observed_ns) AS first, max(t.last_observed_ns) AS last `+base+`
 GROUP BY 1, 2),
ranked AS (SELECT *, row_number() OVER (ORDER BY first, entry, evidence) AS position, count(*) OVER () AS total FROM pairs)
SELECT entry, evidence, traces, first, last, total FROM ranked
 WHERE position <= ? OR position > total - ? ORDER BY position`, append(args, groupVersionEdge, groupVersionEdge)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	versions = []investigation.GroupVersion{}
	for rows.Next() {
		var version investigation.GroupVersion
		var first, last int64
		if err := rows.Scan(&version.EntryVersion, &version.EvidenceVersion, &version.Count, &first, &last, &total); err != nil {
			return nil, 0, err
		}
		version.FirstObserved, version.LastObserved = time.Unix(0, first).UTC(), time.Unix(0, last).UTC()
		versions = append(versions, version)
	}
	return versions, total, rows.Err()
}
