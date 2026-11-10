package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"geko/internal/telemetry"
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin trace batch: %w", err)
	}
	defer tx.Rollback()

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

	observedNS := observed.UnixNano()
	seen := make(map[telemetry.TraceID]struct{})
	for _, span := range spans {
		if _, ok := seen[span.TraceID]; !ok {
			if _, err := traceStmt.ExecContext(ctx, span.TraceID[:], observedNS, observedNS); err != nil {
				return fmt.Errorf("update trace observation: %w", err)
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
			return fmt.Errorf("insert span: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit trace batch: %w", err)
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
