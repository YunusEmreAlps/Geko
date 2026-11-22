package sqlite

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestEvidenceDetailsSelectionAndBounds(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	spans := []telemetry.Span{testSpan(1, 1, "api", 200), testSpan(1, 2, "api", 503), testSpan(1, 3, "api", 502)}
	if err := store.WriteSpans(ctx, time.Now(), spans); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{1, 2, 3} {
		got, more, err := store.EvidenceDetails(ctx, spans[0].TraceID, spans[2].ID, limit)
		if err != nil || len(got) != min(limit, 2) || got[0].SpanID != "0300000000000000" || more != (limit == 1) {
			t.Fatalf("limit %d: %+v, more %v, err %v", limit, got, more, err)
		}
	}
	for _, limit := range []int{0, investigation.MaxPageSize + 1} {
		if _, _, err := store.EvidenceDetails(ctx, spans[0].TraceID, spans[2].ID, limit); err == nil {
			t.Fatalf("accepted limit %d", limit)
		}
	}
	for _, id := range []telemetry.SpanID{spans[0].ID, {99}} {
		if _, _, err := store.EvidenceDetails(ctx, spans[0].TraceID, id, 2); !errors.Is(err, investigation.ErrSpanNotFound) {
			t.Fatalf("non-evidence %x: %v", id, err)
		}
	}
	if _, _, err := store.EvidenceDetails(ctx, telemetry.TraceID{2}, spans[2].ID, 2); !errors.Is(err, investigation.ErrSpanNotFound) {
		t.Fatalf("other trace: %v", err)
	}
}
