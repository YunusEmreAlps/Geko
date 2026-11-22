package demo

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestSeedLeavesAnInvestigationToFollow(t *testing.T) {
	ctx := t.Context()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "demo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	dataset, err := Seed(ctx, store, now)
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Traces != seededTraces {
		t.Fatalf("seeded %d traces", dataset.Traces)
	}
	groups, err := store.FailureGroups(ctx, investigation.FailureFilter{Limit: investigation.MaxPageSize, GroupState: investigation.GroupAll}, nil)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]investigation.GroupState{}
	for _, group := range groups.Items {
		states[group.Service+" "+group.ExceptionType] = group.State
		if group.Service == payments.name && group.Classification == "exception" && group.IssueURL == "" {
			t.Errorf("payment timeouts carry no note: %+v", group)
		}
	}
	for key, want := range map[string]investigation.GroupState{
		"payments java.net.http.HttpTimeoutException": investigation.GroupOpen,
		"reporting java.lang.NullPointerException":    investigation.GroupReopened,
		"notifications ":                       investigation.GroupMuted,
		"catalog psycopg.errors.QueryCanceled": investigation.GroupOpen,
	} {
		if got := states[key]; got != want {
			t.Errorf("%s is %q, want %q (groups %v)", key, got, want, states)
		}
	}
	pinned, err := store.Failures(ctx, investigation.FailureFilter{Limit: 5, Pinned: true})
	if err != nil || len(pinned.Items) != 1 {
		t.Fatalf("pinned traces = %+v, %v", pinned.Items, err)
	}

	rows, err := store.VersionComparisons(ctx, investigation.RegressionFilter{Baseline: Baseline, Candidate: Candidate, Since: now.Add(-History)})
	if err != nil {
		t.Fatal(err)
	}
	flagged := map[string][]bool{}
	for _, row := range rows {
		flagged[row.Operation] = []bool{row.FailureRegression, row.LatencyRegression}
	}
	if !slices.Equal(flagged["POST /checkout"], []bool{true, true}) || !slices.Equal(flagged["GET /products"], []bool{false, true}) {
		t.Fatalf("regressions = %v", flagged)
	}
}

func TestTracesAreDeterministic(t *testing.T) {
	end := time.Unix(1_800_000_000, 0)
	first, _ := Seed(t.Context(), discard{}, end)
	second, _ := Seed(t.Context(), discard{}, end)
	a, b := first.Trace(end), second.Trace(end)
	if len(a) == 0 || len(a) != len(b) || a[0].TraceID != b[0].TraceID || !a[len(a)-1].EndTime.Equal(b[len(b)-1].EndTime) {
		t.Fatal("the same seed produced different traces")
	}
	for _, span := range a {
		if span.StartTime.After(span.EndTime) || span.EndTime.After(end) {
			t.Fatalf("span %s runs from %v to %v, after the trace end %v", span.Name, span.StartTime, span.EndTime, end)
		}
	}
}

type discard struct{}

// Check batching independently of the filesystem: every trace must survive,
// exports stay bounded, and receipt timestamps must not precede their spans.
func TestSeedBatchesStayBoundedAndPreserveHistory(t *testing.T) {
	store := &seedRecorder{t: t, traces: make(map[telemetry.TraceID]bool)}
	now := time.Unix(1_800_000_000, 0)
	if _, err := Seed(t.Context(), store, now); err != nil {
		t.Fatal(err)
	}
	if len(store.traces) != seededTraces || !store.observed.Equal(now) {
		t.Fatalf("seeded %d traces, last observed %v", len(store.traces), store.observed)
	}
	if store.exports >= 60 {
		t.Fatalf("seed still needs %d durable commits", store.exports)
	}
	t.Logf("seeded %d traces in %d bounded exports", len(store.traces), store.exports)
}

type seedRecorder struct {
	discard
	t        *testing.T
	traces   map[telemetry.TraceID]bool
	observed time.Time
	exports  int
}

func (s *seedRecorder) WriteSpans(_ context.Context, observed time.Time, spans []telemetry.Span) error {
	s.t.Helper()
	if len(spans) == 0 || len(spans) > seedBatchSpans || observed.Before(s.observed) {
		s.t.Fatalf("invalid seed export: %d spans, observed %v after %v", len(spans), observed, s.observed)
	}
	for _, span := range spans {
		if span.EndTime.After(observed) {
			s.t.Fatalf("span ends at %v after receipt %v", span.EndTime, observed)
		}
		s.traces[span.TraceID] = true
	}
	s.exports++
	s.observed = observed
	return nil
}

func (discard) WriteSpans(context.Context, time.Time, []telemetry.Span) error { return nil }
func (discard) FailureGroups(context.Context, investigation.FailureFilter, *investigation.GroupCursor) (investigation.FailureGroups, error) {
	return investigation.FailureGroups{}, nil
}
func (discard) SetGroupState(context.Context, string, investigation.GroupState, string, string, time.Time) error {
	return nil
}
func (discard) SetGroupNote(context.Context, string, string, string, string, time.Time) error {
	return nil
}
func (discard) SetTracePin(context.Context, telemetry.TraceID, bool, string, time.Time) error {
	return nil
}
