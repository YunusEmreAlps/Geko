package sqlite

import (
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Stress checkpoints far more often than the production one-minute worker.
// This isolates the write-stage cost; it is not a network or Linux CI baseline.
func BenchmarkBudgetHistoryWriteStage(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "memory-only"
		if enabled {
			name = "checkpoint-every-32-exports"
		}
		b.Run(name, func(b *testing.B) {
			db, err := Open(b.Context(), filepath.Join(b.TempDir(), "budget-bench.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			tracker := budget.New(budget.Config{Enabled: true, HistoryDays: 7, Attributes: []string{}}, time.Now())
			spans := make([]telemetry.Span, 64)
			observations := make([]budget.Observation, 64)
			for i := range spans {
				spans[i] = testSpan(1, byte(i+1), "api", 200)
				observations[i] = budget.Observation{Span: &trace.Span{Name: "request"}}
			}
			var exports, saved int64
			b.ResetTimer()
			for b.Loop() {
				exports++
				for i := range spans {
					binary.LittleEndian.PutUint64(spans[i].TraceID[:8], uint64(exports*64+int64(i)+1))
				}
				now := time.Now()
				if err := db.WriteSpans(b.Context(), now, spans); err != nil {
					b.Fatal(err)
				}
				tracker.Record(now, spans, observations)
				if enabled && exports%32 == 0 {
					if err := db.SaveBudgetCheckpoints(b.Context(), now, 7, tracker.Checkpoints(now)); err == nil {
						saved++
					}
				}
			}
			b.ReportMetric(float64(saved), "checkpoints")
		})
	}
}
