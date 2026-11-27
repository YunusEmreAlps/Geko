package budget

import (
	"fmt"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/telemetry"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestLivePolicyPreservesMeasurements(t *testing.T) {
	now := time.Now().UTC()
	tracker := New(Config{Enabled: true, Defaults: Limits{Spans: 10}}, now)
	initial, err := NewPolicy(ReportingLimits{Defaults: Limits{Spans: 10}})
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[Policy]
	current.Store(&initial)
	tracker.WithPolicySource(func() Policy { return *current.Load() })
	tracker.Record(now, []telemetry.Span{{ServiceName: "api"}, {ServiceName: "api"}}, []Observation{{Span: &trace.Span{}}, {Span: &trace.Span{}}})
	before := tracker.Checkpoints(now)
	session := tracker.Session()
	next, err := NewPolicy(ReportingLimits{Defaults: Limits{Spans: 1}, CardinalityLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	current.Store(&next)
	report := tracker.Snapshot(now, false)
	if report.Total.Spans != 2 || len(report.Services) != 1 || report.Services[0].SpanOverage != 1 || report.CardinalityLimit != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !reflect.DeepEqual(before, tracker.Checkpoints(now)) || tracker.Session() != session {
		t.Fatal("policy reset measurements")
	}
}

func TestLivePoliciesKeepServiceTrackingBounded(t *testing.T) {
	now := time.Now()
	tracker := New(Config{Enabled: true}, now)
	var current Policy
	tracker.WithPolicySource(func() Policy { return current })
	for i := 0; i < 400; i++ {
		name := fmt.Sprintf("service-%d", i)
		var err error
		current, err = NewPolicy(ReportingLimits{Services: []ServiceLimit{{Service: name, Limits: Limits{Spans: 1}}}})
		if err != nil {
			t.Fatal(err)
		}
		tracker.Record(now, []telemetry.Span{{ServiceName: name}}, []Observation{{Span: &trace.Span{}}})
	}
	report := tracker.Snapshot(now, false)
	if len(report.Services) > MaxHistoryServices || report.Total.Spans != 400 || report.Other.Spans != 400-int64(len(report.Services)) {
		t.Fatalf("unbounded or lost volume: %+v", report)
	}
	for _, c := range tracker.Checkpoints(now) {
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReportingLimitsAreImmutableAndValidated(t *testing.T) {
	input := ReportingLimits{Services: []ServiceLimit{{Service: "api", Limits: Limits{Spans: 5}}}}
	policy, err := NewPolicy(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Services[0].Spans = 99
	copy := policy.Limits()
	copy.Services[0].Spans = 88
	if policy.Limits().Services[0].Spans != 5 {
		t.Fatal("mutable policy")
	}
	if _, err := NewPolicy(ReportingLimits{Defaults: Limits{Spans: -1}}); err == nil {
		t.Fatal("negative limit accepted")
	}
}

func TestLivePolicyKeepsCardinalityAndHistoricalVolume(t *testing.T) {
	hour := time.Now().UTC().Truncate(time.Hour)
	initial, _ := NewPolicy(ReportingLimits{CardinalityLimit: 10})
	var current atomic.Pointer[Policy]
	current.Store(&initial)
	tracker := New(Config{Enabled: true, HistoryDays: 7, Attributes: []string{"http.route"}}, hour).WithPolicySource(func() Policy { return *current.Load() })
	for _, route := range []string{"/a", "/b"} {
		raw := &trace.Span{Attributes: []*common.KeyValue{{Key: "http.route", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: route}}}}}
		tracker.Record(hour, []telemetry.Span{{ServiceName: "api"}}, []Observation{{Span: raw}})
	}
	old := New(Config{Enabled: true, HistoryDays: 7}, hour)
	recordVolume(old, hour, 3)
	history := memoryHistory(old.Checkpoints(hour.Add(30 * time.Minute)))
	before, err := tracker.HistoryReport(t.Context(), history, hour.Add(30*time.Minute), hour)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := NewPolicy(ReportingLimits{Defaults: Limits{Spans: 1}, CardinalityLimit: 1})
	current.Store(&next)
	after, err := tracker.HistoryReport(t.Context(), history, hour.Add(30*time.Minute), hour)
	if err != nil {
		t.Fatal(err)
	}
	if after.Total != before.Total || after.Total.Spans != 5 || len(after.Services) != 2 || after.CardinalityLimit != 1 {
		t.Fatalf("history reset: %+v", after)
	}
	var found bool
	for _, service := range after.Services {
		if service.Namespace == "" {
			found = len(service.HighCardinality) == 1 && service.HighCardinality[0].Distinct == 2 && service.SpanOverage == 1
		}
	}
	if !found {
		t.Fatalf("cardinality was reset: %+v", after.Services)
	}
}

func TestConcurrentPolicyRecordAndHistory(t *testing.T) {
	now := time.Now().UTC()
	var current atomic.Pointer[Policy]
	current.Store(&Policy{})
	tracker := New(Config{Enabled: true, HistoryDays: 7}, now).WithPolicySource(func() Policy { return *current.Load() })
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 200 {
			p, err := NewPolicy(ReportingLimits{Defaults: Limits{Spans: int64(i)}, CardinalityLimit: i % 255})
			if err != nil {
				t.Error(err)
				return
			}
			current.Store(&p)
		}
	})
	wg.Go(func() {
		for range 200 {
			recordVolume(tracker, now, 1)
		}
	})
	wg.Go(func() {
		for range 200 {
			tracker.Snapshot(now, false)
			if _, err := tracker.HistoryReport(t.Context(), memoryHistory{}, now, now.Truncate(time.Hour)); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Wait()
	if tracker.Snapshot(now, false).Total.Spans != 200 {
		t.Fatal("lost concurrent volume")
	}
}

func TestLiveHistoryPolicyPreservesSessionCounters(t *testing.T) {
	now := time.Now()
	policy, _ := NewPolicy(ReportingLimits{})
	tracker := New(Config{Enabled: true}, now).WithPolicySource(func() Policy { return policy })
	recordVolume(tracker, now, 2)
	before := tracker.Checkpoints(now)
	policy = policy.WithHistoryDays(7)
	if tracker.HistoryDays() != 7 || tracker.Snapshot(now, false).HistoryDays != 7 || !reflect.DeepEqual(before, tracker.Checkpoints(now)) {
		t.Fatal("history update lost counters")
	}
	policy = policy.WithHistoryDays(0)
	if tracker.HistoryDays() != 0 {
		t.Fatal("history not disabled")
	}
}

func TestReportAtCapturesOneHistoryPolicy(t *testing.T) {
	now := time.Now()
	calls := 0
	tracker := New(Config{Enabled: true}, now).WithPolicySource(func() Policy { calls++; return Policy{} })
	_, err := tracker.ReportAt(t.Context(), memoryHistory{}, now, now.UTC().Truncate(time.Hour))
	if err != nil || calls != 1 {
		t.Fatalf("mixed policies: %d %v", calls, err)
	}
}
