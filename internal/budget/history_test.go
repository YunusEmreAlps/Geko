package budget

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/telemetry"
	common "go.opentelemetry.io/proto/otlp/common/v1"
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
	if err != nil || report.Total.Spans != 5 || !report.Partial || !report.CardinalitySince.Equal(hour) || len(report.Services) != 1 {
		t.Fatalf("history %+v, %v", report, err)
	}
	if report.CardinalityCoverage != "historical_session_max_lower_bound" {
		t.Fatalf("coverage: %s", report.CardinalityCoverage)
	}
	last := New(Config{Enabled: true, HistoryDays: 7}, hour.Add(time.Hour))
	previous, err := last.ReportAt(t.Context(), memoryHistory(stored), hour.Add(time.Hour), hour)
	if err != nil || previous.Total.Spans != 5 || !previous.CardinalitySince.Equal(hour) || !previous.Partial {
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

func TestPersistedCardinalityUsesMaximumNotSum(t *testing.T) {
	hour := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	cfg := Config{Enabled: true, HistoryDays: 7, Attributes: []string{"id"}, CardinalityLimit: 1}
	history := memoryHistory{}
	for _, count := range []int{2, 3} {
		tracker := New(cfg, hour)
		for i := 0; i < count; i++ {
			tracker.Record(hour, []telemetry.Span{{ServiceName: "api"}}, []Observation{{Span: &trace.Span{Attributes: []*common.KeyValue{{Key: "id", Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: int64(i)}}}}}}})
		}
		history = append(history, tracker.Checkpoints(hour.Add(time.Hour))[0])
	}
	current := New(cfg, hour.Add(2*time.Hour))
	report, err := current.ReportAt(t.Context(), history, hour.Add(2*time.Hour), hour)
	if err != nil || len(report.Services) != 1 || len(report.Services[0].HighCardinality) != 1 {
		t.Fatalf("missing persisted cardinality: %+v, %v", report, err)
	}
	c := report.Services[0].HighCardinality[0]
	if c.Distinct != 3 || !c.AtLeast {
		t.Fatalf("cross-session union must be a lower bound, not a sum: %+v", c)
	}
}

func TestHistoryLegacyCardinalityCoverageAndBounds(t *testing.T) {
	hour := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	tracker := New(Config{Enabled: true, HistoryDays: 7}, hour.Add(2*time.Hour))
	legacy := Checkpoint{Session: strings.Repeat("a", 32), Start: hour, Since: hour, Until: hour.Add(time.Hour), Spans: 1, Bytes: 1, Services: []Volume{{Service: "api", Spans: 1, Bytes: 1}}}
	report, err := tracker.ReportAt(t.Context(), memoryHistory{legacy}, hour.Add(2*time.Hour), hour)
	if err != nil || report.CardinalityCoverage != "unavailable" || !report.CardinalitySince.IsZero() {
		t.Fatalf("legacy coverage %+v: %v", report, err)
	}
	legacy.CardinalityMeasured = true
	legacy.Services[0].Cardinality = []Cardinality{{Key: "id", Distinct: 257}}
	if legacy.Validate() == nil {
		t.Fatal("unbounded persisted cardinality accepted")
	}
	legacy.Services[0].Cardinality = []Cardinality{{Key: "id", Distinct: 2}, {Key: "id", Distinct: 3}}
	if legacy.Validate() == nil {
		t.Fatal("duplicate persisted attribute accepted")
	}
}

func TestHistoricalCardinalityRemainsBoundedAcrossCollectionChanges(t *testing.T) {
	hour := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	history := memoryHistory{}
	for _, prefix := range []string{"a", "b"} {
		row := Checkpoint{CardinalityMeasured: true, Session: strings.Repeat(prefix, 32), Start: hour, Since: hour, Until: hour.Add(time.Hour), Spans: 2, Bytes: 2, Services: []Volume{{Service: "api", Spans: 2, Bytes: 2}}}
		for _, suffix := range []string{"0", "1", "2", "3", "4", "5", "6", "7"} {
			row.Services[0].Cardinality = append(row.Services[0].Cardinality, Cardinality{Key: prefix + suffix, Distinct: 2})
		}
		history = append(history, row)
	}
	tracker := New(Config{Enabled: true, HistoryDays: 7, CardinalityLimit: 1}, hour.Add(2*time.Hour))
	report, err := tracker.ReportAt(t.Context(), history, hour.Add(2*time.Hour), hour)
	if err != nil || len(report.Services) != 1 || len(report.Services[0].HighCardinality) > 8 || report.CardinalityCoverage != "partial_historical_session_max_lower_bound" {
		t.Fatalf("unbounded/undisclosed historical keys: %+v, %v", report, err)
	}
}
