package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

// TraceDetails reads every section of the dashboard trace from one snapshot.
func (s *Store) TraceDetails(ctx context.Context, id telemetry.TraceID, filter investigation.TraceDetailFilter) (investigation.TraceDetails, error) {
	var details investigation.TraceDetails
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return details, err
	}
	defer tx.Rollback()

	details.Summary, err = traceSummary(ctx, tx, id)
	if err != nil {
		return details, err
	}
	topology, err := traceSpans(ctx, tx, id, nil, investigation.MapSpanLimit)
	if err != nil {
		return details, err
	}
	details.MapSpans = topology.Items
	if err := traceWaterfall(ctx, tx, id, filter, &details); err != nil {
		return details, err
	}
	if details.Summary.Evidence != nil {
		details.Evidence, details.EvidenceNext, err = traceEvidence(ctx, tx, id, filter.EvidenceAfter, 10)
		if err != nil {
			return details, err
		}
	}
	return details, tx.Commit()
}

func traceWaterfall(ctx context.Context, tx *sql.Tx, id telemetry.TraceID, filter investigation.TraceDetailFilter, details *investigation.TraceDetails) error {
	const limit = investigation.DefaultPageSize
	details.After = filter.After
	first := details.MapSpans[:min(len(details.MapSpans), limit)]
	if filter.Selected != nil {
		selectedID := hex.EncodeToString(filter.Selected[:])
		inFirstPage := false
		for i := range details.MapSpans {
			if details.MapSpans[i].SpanID == selectedID {
				details.Selected = &details.MapSpans[i]
				inFirstPage = i < len(first)
				break
			}
		}
		if details.Selected == nil {
			span, _, err := scanSpan(tx.QueryRowContext(ctx, spanSelect+" AND span_id = ?", id[:], filter.Selected[:]))
			switch {
			case err == nil:
				details.Selected = &span
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
		}
		if details.After == nil && details.Selected != nil && !inFirstPage {
			details.After = cursorBefore(*details.Selected, *filter.Selected)
		}
	}
	if details.After != nil {
		page, err := traceSpans(ctx, tx, id, details.After, limit)
		if err != nil {
			return err
		}
		details.Waterfall = page
	} else {
		details.Waterfall.Items = first
		if len(details.MapSpans) > limit {
			last := first[len(first)-1]
			spanID, err := hex.DecodeString(last.SpanID)
			if err != nil {
				return err
			}
			details.Waterfall.Next = &investigation.Cursor{TimeNS: last.Start.UnixNano(), ID: spanID}
		}
	}
	if filter.Selected == nil && len(details.Waterfall.Items) != 0 {
		details.Selected = &details.Waterfall.Items[0]
	}
	return nil
}

// Span IDs sort bytewise, so their predecessor makes the selected span the
// first row returned by the exclusive cursor at the same timestamp.
func cursorBefore(span investigation.Span, id telemetry.SpanID) *investigation.Cursor {
	for i := len(id) - 1; i >= 0; i-- {
		id[i]--
		if id[i] != 255 {
			break
		}
	}
	return &investigation.Cursor{TimeNS: span.Start.UnixNano(), ID: id[:]}
}
