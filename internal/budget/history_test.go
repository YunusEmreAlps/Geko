package budget

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/telemetry"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
)

type memoryHistory []Checkpoint

func (m memoryHistory) ReadBudgetHistory(_ context.Context, hour time.Time, exclude string, visit func(Checkpoint) error) (int64, error) {
	for _, c := range m {
		if c.Start.Equal(hour) && c.Session != exclude {
			if err := visit(c); err != nil {
				return 0, err
			}
		}
	}
	return 0, nil
}
func recordVolume(t *Tracker, at time.Time, n int) {
	for range n {
		t.Record(at, []telemetry.Span{{ServiceName: "api", ResourceAttributes: telemetry.Attributes{"service.namespace": "Shop"}}}, []Observation{{Span: &trace.Span{Name: "request"}}})
	}
}

func TestHistoryCombinesSessionsWithoutDoubleCounting(t *testing.T) {
	hour := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	old := New(Config{Enabled: true, HistoryDays: 7}, hour)
	recordVolume(old, hour, 3)
	stored := old.Checkpoints(hour.Add(30 * time.Minute))
	current := New(Config{Enabled: true, HistoryDays: 7}, hour.Add(35*time.Minute))
	recordVolume(current, hour.Add(36*time.Minute), 2)
	stored = append(stored, current.Checkpoints(hour.Add(40*time.Minute))...)
	report, err := current.ReportAt(t.Context(), memoryHistory(stored), hour.Add(45*time.Minute), hour)
	if err != nil || report.Total.Spans != 5 || !report.Partial || !report.CardinalitySince.Equal(current.started) || len(report.Services) != 1 {
		t.Fatalf("history %+v, %v", report, err)
	}
	if report.CardinalitySince.Equal(report.ObservedSince) {
		t.Fatal("cardinality incorrectly includes the prior process")
	}
	last := New(Config{Enabled: true, HistoryDays: 7}, hour.Add(time.Hour))
	previous, err := last.ReportAt(t.Context(), memoryHistory(stored), hour.Add(time.Hour), hour)
	if err != nil || previous.Total.Spans != 5 || !previous.CardinalitySince.IsZero() || !previous.Partial {
		t.Fatalf("after restart %+v, %v", previous, err)
	}
}

func TestHistoryCompleteWindowsAndServiceChanges(t *testing.T) {
	hour := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	tracker := New(Config{Enabled: true, HistoryDays: 7}, hour)
	recordVolume(tracker, hour, 3)
	first := tracker.Checkpoints(hour.Add(time.Hour))[0]
	recordVolume(tracker, hour.Add(time.Hour), 5)
	second := tracker.Checkpoints(hour.Add(2 * time.Hour))[0]
	current := New(Config{Enabled: true, HistoryDays: 7}, hour.Add(2*time.Hour))
	report, err := current.ReportAt(t.Context(), memoryHistory{first, second}, hour.Add(2*time.Hour), hour.Add(time.Hour))
	if err != nil || report.Partial || report.SpanDelta == nil || *report.SpanDelta != 2 || report.Services[0].SpanDelta == nil || *report.Services[0].SpanDelta != 2 {
		t.Fatalf("comparable history %+v, %v", report, err)
	}
}

func TestHistoryConfigAndMissingWindows(t *testing.T) {
	for _, cfg := range []Config{{Enabled: true, HistoryDays: 31}, {Enabled: true, HistoryDays: -1}, {HistoryDays: 1}} {
		if cfg.Validate() == nil {
			t.Fatal("invalid history configuration accepted")
		}
	}
	hour := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	tracker := New(Config{Enabled: true, HistoryDays: 1}, hour)
	report, err := tracker.ReportAt(t.Context(), memoryHistory{}, hour, hour.Add(-2*time.Hour))
	if err != nil || report.Available || report.HistoryDays != 1 {
		t.Fatalf("missing history %+v, %v", report, err)
	}
	data := tracker.Checkpoints(hour)[0]
	data.Services = []Volume{{Service: strings.Repeat("x", 257)}}
	if data.Validate() == nil {
		t.Fatal("oversized service identity accepted")
	}
}

func TestHistoryIncludesServicesThatStoppedProducing(t *testing.T) {
	hour := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	tracker := New(Config{Enabled: true, HistoryDays: 7}, hour)
	recordVolume(tracker, hour, 3)
	first := tracker.Checkpoints(hour.Add(time.Hour))[0]
	second := tracker.Checkpoints(hour.Add(2 * time.Hour))[0]
	current := New(Config{Enabled: true, HistoryDays: 7}, hour.Add(2*time.Hour))
	report, err := current.ReportAt(t.Context(), memoryHistory{first, second}, hour.Add(2*time.Hour), hour.Add(time.Hour))
	if err != nil || len(report.Services) != 1 || report.Services[0].Spans != 0 || report.Services[0].SpanDelta == nil || *report.Services[0].SpanDelta != -3 {
		t.Fatalf("missing stopped service: %+v, %v", report, err)
	}
}
