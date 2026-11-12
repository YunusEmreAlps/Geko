package sqlite

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestTraceDetailsMissingAndCanceled(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.TraceDetails(t.Context(), telemetry.TraceID{1}, investigation.TraceDetailFilter{}); !errors.Is(err, investigation.ErrTraceNotFound) {
		t.Fatalf("missing trace: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.TraceDetails(ctx, telemetry.TraceID{1}, investigation.TraceDetailFilter{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
}

func TestTraceDetailsPagingAndSelection(t *testing.T) {
	store := openTestStore(t)
	spans := make([]telemetry.Span, investigation.MapSpanLimit+2)
	for i := range spans {
		spans[i] = testSpan(1, 1, "api", 200)
		binary.BigEndian.PutUint64(spans[i].ID[:], uint64(i+1))
		spans[i].StartTime = time.Unix(0, int64(i+1))
		spans[i].EndTime = spans[i].StartTime.Add(time.Nanosecond)
	}
	spans[len(spans)-1].StatusCode = telemetry.StatusError
	if err := store.WriteSpans(t.Context(), time.Now(), spans); err != nil {
		t.Fatal(err)
	}
	id := spans[0].TraceID
	first, err := store.TraceDetails(t.Context(), id, investigation.TraceDetailFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.MapSpans) != investigation.MapSpanLimit || len(first.Waterfall.Items) != investigation.DefaultPageSize || first.Waterfall.Next == nil || first.Selected == nil || len(first.Evidence) != 1 {
		t.Fatalf("unexpected details: map=%d waterfall=%d evidence=%d", len(first.MapSpans), len(first.Waterfall.Items), len(first.Evidence))
	}
	second, err := store.TraceDetails(t.Context(), id, investigation.TraceDetailFilter{After: first.Waterfall.Next})
	if err != nil || len(second.Waterfall.Items) == 0 || second.Waterfall.Items[0].SpanID == first.Waterfall.Items[0].SpanID {
		t.Fatalf("next page: %+v, %v", second.Waterfall, err)
	}
	selected := spans[len(spans)-1].ID
	last, err := store.TraceDetails(t.Context(), id, investigation.TraceDetailFilter{Selected: &selected})
	if err != nil || last.Selected == nil || len(last.Waterfall.Items) != 1 || last.Selected.SpanID != last.Waterfall.Items[0].SpanID {
		t.Fatalf("selection outside map: %+v, %v", last.Selected, err)
	}
	missing := telemetry.SpanID{255}
	absent, err := store.TraceDetails(t.Context(), id, investigation.TraceDetailFilter{Selected: &missing})
	if err != nil || absent.Selected != nil {
		t.Fatalf("absent span: %+v, %v", absent.Selected, err)
	}
}
