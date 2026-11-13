package sqlite

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

// benchExport returns one export of traces with five spans each: an entry span,
// three children and, in every fourth trace, a failing child with an exception.
func benchExport(first, traces int) []telemetry.Span {
	spans := make([]telemetry.Span, 0, traces*5)
	start := time.Unix(1_700_000_000, 0).UTC()
	for i := range traces {
		var trace telemetry.TraceID
		binary.BigEndian.PutUint64(trace[8:], uint64(first+i+1))
		services := []string{"gateway", "orders", "inventory", "payments", "orders"}
		for s := range 5 {
			span := telemetry.Span{
				TraceID: trace, ID: telemetry.SpanID{byte(s + 1)}, Kind: telemetry.KindServer,
				ServiceName: services[s], Name: fmt.Sprintf("op-%d", s), StartTime: start, EndTime: start.Add(time.Duration(s+1) * time.Millisecond),
				Attributes:         telemetry.Attributes{"http.response.status_code": 200},
				ResourceAttributes: telemetry.Attributes{"service.namespace": "Storefront", "service.version": "1.0.0"},
			}
			if s > 0 {
				span.ParentID = telemetry.SpanID{1}
				span.Kind = telemetry.KindClient
			}
			if s == 4 && i%4 == 0 {
				span.StatusCode = telemetry.StatusError
				span.Attributes = telemetry.Attributes{"http.response.status_code": 503}
				span.Events = []telemetry.Event{{Name: "exception", Time: start, Attributes: telemetry.Attributes{
					"exception.type": fmt.Sprintf("Timeout%d", i%7), "exception.message": "upstream timed out",
				}}}
			}
			spans = append(spans, span)
		}
	}
	return spans
}

func BenchmarkWriteSpans(b *testing.B) {
	store, err := Open(b.Context(), filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	const traces = 20
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		if err := store.WriteSpans(b.Context(), time.Now(), benchExport(i*traces, traces)); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N*traces*5)/b.Elapsed().Seconds(), "spans/s")
}

// seededStore holds 10,000 traces, 2,500 of them failing.
func seededStore(b *testing.B) *Store {
	b.Helper()
	store, err := Open(b.Context(), filepath.Join(b.TempDir(), "query.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	now := time.Now()
	for batch := range 100 {
		if err := store.WriteSpans(b.Context(), now.Add(time.Duration(batch)*time.Second), benchExport(batch*100, 100)); err != nil {
			b.Fatal(err)
		}
	}
	return store
}

func BenchmarkFailures(b *testing.B) {
	store := seededStore(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := store.Failures(b.Context(), investigation.FailureFilter{Limit: 25}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFailureGroups(b *testing.B) {
	store := seededStore(b)
	b.ReportAllocs()
	for b.Loop() {
		groups, err := store.FailureGroups(b.Context(), investigation.FailureFilter{Limit: 25}, nil)
		if err != nil || len(groups.Items) == 0 {
			b.Fatal(groups, err)
		}
	}
}
