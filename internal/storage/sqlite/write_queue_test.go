package sqlite

import (
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestWriteQueueCommitAndFallback(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		name := "shared commit"
		if invalid {
			name = "failed export isolated"
		}
		t.Run(name, func(t *testing.T) {
			store := openTestStore(t)
			observed := time.Now()
			root := testSpan(1, 1, "gateway", 200)
			child := testSpan(1, 2, "payments", 503)
			child.ParentID = root.ID
			if invalid {
				child.Attributes["invalid"] = make(chan int)
			}
			// Preload both requests so the test does not depend on scheduler timing.
			first, second := make(chan error, 1), make(chan error, 1)
			store.pending = []spanWrite{
				{t.Context(), observed.UnixNano(), []telemetry.Span{root}, first},
				{t.Context(), observed.Add(time.Second).UnixNano(), []telemetry.Span{child}, second},
			}
			store.writer.Add(1)
			store.drainWrites()
			if err := <-first; err != nil {
				t.Fatal(err)
			}
			if err := <-second; (err != nil) != invalid {
				t.Fatalf("second write = %v", err)
			}
			if store.Revision() != 1 {
				t.Fatalf("revision = %d, want 1", store.Revision())
			}
			summary, page, err := store.Trace(t.Context(), root.TraceID, nil, 10)
			if err != nil || summary.EntrySpan == nil || summary.EntrySpan.Service != "gateway" {
				t.Fatalf("trace summary = %+v, %v", summary, err)
			}
			if invalid {
				if len(page.Items) != 1 || summary.Evidence != nil || !summary.LastObserved.Equal(observed) {
					t.Fatalf("failed export affected the trace: %+v, %+v", summary, page)
				}
			} else if len(page.Items) != 2 || summary.Evidence == nil || summary.Evidence.Service != "payments" ||
				!summary.FirstObserved.Equal(observed) || !summary.LastObserved.Equal(observed.Add(time.Second)) {
				t.Fatalf("shared commit lost span data: %+v, %+v", summary, page)
			}
			if err := store.SetServiceApplication(t.Context(), "payments", "shop"); err != nil {
				t.Fatal(err)
			}
			app := "shop"
			result, err := store.Traces(t.Context(), investigation.FailureFilter{Application: &app, Limit: 10})
			if err != nil || (len(result.Items) == 1) == invalid {
				t.Fatalf("projected service membership = %+v, %v", result, err)
			}
		})
	}
}
