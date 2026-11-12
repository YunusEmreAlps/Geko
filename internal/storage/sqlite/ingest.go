package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

const observeTrace = `
INSERT INTO traces (trace_id, first_observed_ns, last_observed_ns)
VALUES (?, ?, ?)
ON CONFLICT (trace_id) DO UPDATE SET
    first_observed_ns = min(traces.first_observed_ns, excluded.first_observed_ns),
    last_observed_ns = max(traces.last_observed_ns, excluded.last_observed_ns)
`

const insertSpan = `
INSERT INTO spans (
    trace_id, span_id, parent_span_id, service_name, name, kind, start_ns, end_ns,
    status_code, status_description, resource_attributes, scope_name, scope_version,
    scope_attributes, attributes, events
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (trace_id, span_id) DO NOTHING
`

// WriteSpans stores a batch of normalized spans in a single transaction.
func (s *Store) WriteSpans(ctx context.Context, observed time.Time, spans []telemetry.Span) error {
	if len(spans) == 0 {
		return nil
	}
	err := s.checkJournalCapacity(ctx)
	if err == nil {
		err = s.writeSpans(ctx, observed.UnixNano(), spans)
	}
	if errors.Is(err, storage.ErrCapacity) {
		_, countErr := s.db.ExecContext(ctx, "UPDATE storage_counters SET rejected_exports = rejected_exports + 1 WHERE id = 1")
		if countErr != nil {
			return errors.Join(err, fmt.Errorf("record rejected export: %w", countErr))
		}
	}
	return err
}

func (s *Store) writeSpans(ctx context.Context, observedNS int64, spans []telemetry.Span) error {
	_, err := s.writeSpanBatch(ctx, observedNS, spans, "", 0)
	if !errors.Is(err, storage.ErrCapacity) || s.onFull != storage.EvictOldest {
		return err
	}

	ids := make([]string, 0, len(spans))
	for _, span := range spans {
		ids = append(ids, hex.EncodeToString(span.TraceID[:]))
	}
	protected, err := json.Marshal(ids)
	if err != nil {
		return err
	}

	// SQLITE_FULL can roll back the whole transaction. Retry from scratch so
	// eviction and ingestion only commit together, even for an oversized export.
	for limit := int64(1); ; limit *= 2 {
		deleted, err := s.writeSpanBatch(ctx, observedNS, spans, string(protected), limit)
		if !errors.Is(err, storage.ErrCapacity) || deleted < limit {
			return err
		}
	}
}

func (s *Store) writeSpanBatch(ctx context.Context, observedNS int64, spans []telemetry.Span, protected string, limit int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin trace batch: %w", err)
	}
	defer tx.Rollback()

	if err := s.checkWriteCapacity(ctx, tx); err != nil {
		return 0, err
	}
	var deleted int64
	if limit > 0 {
		query := fmt.Sprintf(deleteOldest, "AND lower(hex(trace_id)) NOT IN (SELECT value FROM json_each(?))")
		result, err := tx.ExecContext(ctx, query, observedNS, protected, limit)
		if err != nil {
			return 0, fmt.Errorf("evict traces: %w", writeError(err))
		}
		deleted, err = result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if deleted == 0 {
			return 0, storage.ErrCapacity
		}
		if _, err := tx.ExecContext(ctx, "UPDATE storage_counters SET evicted_traces = evicted_traces + ? WHERE id = 1", deleted); err != nil {
			return 0, fmt.Errorf("record evicted traces: %w", writeError(err))
		}
	}
	if err := insertSpanBatch(ctx, tx, observedNS, spans); err != nil {
		return deleted, err
	}
	return deleted, writeError(tx.Commit())
}

func insertSpanBatch(ctx context.Context, tx *sql.Tx, observedNS int64, spans []telemetry.Span) error {
	traceStmt, err := tx.PrepareContext(ctx, observeTrace)
	if err != nil {
		return fmt.Errorf("prepare trace write: %w", err)
	}
	defer traceStmt.Close()
	spanStmt, err := tx.PrepareContext(ctx, insertSpan)
	if err != nil {
		return fmt.Errorf("prepare span write: %w", err)
	}
	defer spanStmt.Close()

	seen := make(map[telemetry.TraceID]struct{})
	for _, span := range spans {
		if _, ok := seen[span.TraceID]; !ok {
			if _, err := traceStmt.ExecContext(ctx, span.TraceID[:], observedNS, observedNS); err != nil {
				return fmt.Errorf("update trace observation: %w", writeError(err))
			}
			seen[span.TraceID] = struct{}{}
		}

		fields, err := encodeSpanFields(span)
		if err != nil {
			return err
		}
		var parent any
		if span.ParentID != (telemetry.SpanID{}) {
			parent = span.ParentID[:]
		}
		_, err = spanStmt.ExecContext(ctx,
			span.TraceID[:], span.ID[:], parent, span.ServiceName, span.Name, span.Kind,
			span.StartTime.UnixNano(), span.EndTime.UnixNano(), span.StatusCode, span.StatusDescription,
			fields[0], span.Scope.Name, span.Scope.Version, fields[1], fields[2], fields[3],
		)
		if err != nil {
			return fmt.Errorf("insert span: %w", writeError(err))
		}
	}
	return nil
}

func encodeSpanFields(span telemetry.Span) ([4]string, error) {
	var fields [4]string
	values := [...]any{span.ResourceAttributes, span.Scope.Attributes, span.Attributes, span.Events}
	for i, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return fields, fmt.Errorf("encode span metadata: %w", err)
		}
		fields[i] = string(encoded)
	}
	return fields, nil
}
