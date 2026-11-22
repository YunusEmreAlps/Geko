package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

// EvidenceDetails returns bounded full details independently of timeline pages.
// The selected evidence is always first; other evidence follows normal rank.
func (s *Store) EvidenceDetails(ctx context.Context, traceID telemetry.TraceID, selected telemetry.SpanID, limit int) ([]investigation.Span, bool, error) {
	if limit < 1 || limit > investigation.MaxPageSize {
		return nil, false, errors.New("invalid evidence limit")
	}
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	primary, _, err := scanSpan(tx.QueryRowContext(ctx, spanSelect+` AND span_id=?
 AND EXISTS(SELECT 1 FROM span_evidence WHERE trace_id=? AND span_id=?)`, traceID[:], selected[:], traceID[:], selected[:]))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, investigation.ErrSpanNotFound
	}
	if err != nil {
		return nil, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT span_id FROM span_evidence WHERE trace_id=? AND span_id<>?
 ORDER BY rank,start_ns,span_id LIMIT ?`, traceID[:], selected[:], limit)
	if err != nil {
		return nil, false, err
	}
	var ids [][]byte
	for rows.Next() {
		var id []byte
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, false, err
		}
		ids = append(ids, append([]byte(nil), id...))
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, false, err
	}
	more := len(ids) > limit-1
	spans := []investigation.Span{primary}
	for _, id := range ids[:min(len(ids), limit-1)] {
		span, _, err := scanSpan(tx.QueryRowContext(ctx, spanSelect+" AND span_id=?", traceID[:], id))
		if err != nil {
			return nil, false, err
		}
		spans = append(spans, span)
	}
	return spans, more, tx.Commit()
}
