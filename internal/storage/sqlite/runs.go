package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

func (s *Store) RunSnapshot(ctx context.Context, id string) (investigation.RunSnapshot, error) {
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return investigation.RunSnapshot{}, err
	}
	defer tx.Rollback()
	result, err := runSnapshot(ctx, tx, id)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Store) CompareRuns(ctx context.Context, baseline, candidate string) (investigation.RunComparison, error) {
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return investigation.RunComparison{}, err
	}
	defer tx.Rollback()
	left, err := runSnapshot(ctx, tx, baseline)
	if err != nil {
		return investigation.RunComparison{}, err
	}
	right, err := runSnapshot(ctx, tx, candidate)
	if err != nil {
		return investigation.RunComparison{}, err
	}
	result, err := investigation.CompareRunSnapshots(left, right)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func runSnapshot(ctx context.Context, tx *sql.Tx, id string) (investigation.RunSnapshot, error) {
	result := investigation.UnavailableRun(id)
	if !investigation.ValidRunID(id) {
		return result, errors.New("run IDs must be 32 lowercase hexadecimal characters and nonzero")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*),count(DISTINCT trace_id) FROM spans WHERE json_extract(resource_attributes,'$."geko.run.id"')=?`, id).Scan(&result.TaggedSpans, &result.Traces); err != nil {
		return result, err
	}
	base, args := traceListQuery(investigation.FailureFilter{RunID: id}, false, false)
	contextQuery := `SELECT ` + rootApplication + `,coalesce(r.service_namespace,''),coalesce(r.service_name,''),` +
		strings.ReplaceAll(evidenceEnvironment, "s.resource_attributes", "r.resource_attributes") + `,coalesce(json_extract(r.resource_attributes,'$."service.version"'),''),count(*) ` + base + ` GROUP BY 1,2,3,4,5 ORDER BY 1,2,3,4,5 LIMIT 101`
	rows, err := tx.QueryContext(ctx, contextQuery, args...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var c investigation.RunContext
		if err := rows.Scan(&c.Application, &c.Namespace, &c.Service, &c.Environment, &c.Version, &c.Traces); err != nil {
			rows.Close()
			return result, err
		}
		if len(result.Contexts) == investigation.RunSnapshotLimit {
			result.Truncated = true
			break
		}
		result.Contexts = append(result.Contexts, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return result, err
	}
	matching, args := legacyGroupMatches(investigation.FailureFilter{RunID: id})
	query := `WITH matching AS (` + matching + `) SELECT group_key,sum(weight),substr(max(sample),21,32),substr(max(sample),53,16),sum(sum(weight)) OVER () FROM matching GROUP BY group_key ORDER BY group_key LIMIT 101`
	rows, err = tx.QueryContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var key string
		var g investigation.RunGroup
		if err := rows.Scan(&key, &g.Count, &g.TraceID, &g.SpanID, &result.FailingTraces); err != nil {
			rows.Close()
			return result, err
		}
		if len(result.Groups) == investigation.RunSnapshotLimit {
			result.Truncated = true
			break
		}
		var parts []json.RawMessage
		if err := json.Unmarshal([]byte(key), &parts); err != nil {
			rows.Close()
			return result, err
		}
		if len(parts) != 8 {
			rows.Close()
			return result, errors.New("invalid stored group identity")
		}
		var rank int
		for i, dest := range []any{&g.Application, &g.Namespace, &g.Service, &g.Environment, &g.Operation, &rank, &g.ExceptionType, &g.HTTPStatus} {
			if err := json.Unmarshal(parts[i], dest); err != nil {
				rows.Close()
				return result, err
			}
		}
		g.ID = investigation.EncodeGroupKey(key)
		g.Classification = map[int]string{1: "exception", 2: "http_5xx", 3: "span_error"}[rank]
		g.TraceID, g.SpanID = strings.ToLower(g.TraceID), strings.ToLower(g.SpanID)
		result.Groups = append(result.Groups, g)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return result, err
	}
	result.CapturedAt = time.Now().UTC()
	result.CollectionStatus = "available"
	return result, investigation.ValidateRunSnapshot(result)
}
