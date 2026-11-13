package sqlite

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestSchema6UpgradePreservesHistoricalFailureGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema6.db")
	ctx := t.Context()
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	for _, migration := range migrations[:6] {
		if _, err := legacy.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	root := testSpan(1, 1, "gateway", 200)
	child := testSpan(1, 2, "inventory", 503)
	if _, err := legacy.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d; PRAGMA user_version = 6;", applicationID)); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, "INSERT INTO traces (trace_id, first_observed_ns, last_observed_ns) VALUES (?, 100, 200)", root.TraceID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO spans (trace_id, span_id, service_name, name, kind, start_ns, end_ns, status_code, attributes)
VALUES (?, ?, 'gateway', 'GET /orders', 2, 10, 50, 0, '{"http.response.status_code":200}')
`, root.TraceID[:], root.ID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO spans (trace_id, span_id, parent_span_id, service_name, name, kind, start_ns, end_ns, status_code, attributes, events)
VALUES (?, ?, ?, 'inventory', 'reserve', 2, 20, 40, 2, '{"http.response.status_code":503}',
        '[{"name":"exception","attributes":{"exception.type":"Timeout","exception.message":"upstream timed out"}}]')
`, child.TraceID[:], child.ID[:], root.ID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, "INSERT INTO trace_applications VALUES (?, 'commerce')", root.TraceID[:]); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	app := "commerce"
	filter := investigation.FailureFilter{Application: &app, Service: "gateway", EvidenceService: "inventory", HTTPStatus: 200, Limit: 10}
	groups, err := store.FailureGroups(ctx, filter, nil)
	if err != nil || len(groups.Items) != 1 {
		t.Fatalf("historical groups = %+v, %v", groups, err)
	}
	group := groups.Items[0]
	if group.Count != 1 || group.Outcomes.HTTP2xx != 1 || group.HTTPStatus != 503 ||
		group.Application != app || group.ExceptionType != "Timeout" ||
		group.FirstObserved.UnixNano() != 100 || group.LastObserved.UnixNano() != 200 ||
		group.TraceID != hex.EncodeToString(root.TraceID[:]) || group.SpanID != hex.EncodeToString(child.ID[:]) {
		t.Fatalf("historical group = %+v", group)
	}

	filter.GroupKey = group.Key
	page, err := store.Failures(ctx, filter)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("historical drilldown = %+v, %v", page, err)
	}
	evidence := page.Items[0].Evidence
	if evidence == nil || evidence.ExceptionMessage != "upstream timed out" || evidence.SpanID != group.SpanID {
		t.Fatalf("historical evidence = %+v", evidence)
	}
}

func TestFailureGroupsIdentityAndDrilldown(t *testing.T) {
	store := openTestStore(t)
	first := time.Unix(100, 0)
	makeSpan := func(id byte, app, env, instance, kind string) telemetry.Span {
		span := testSpan(id, 1, "api", 500)
		span.ResourceAttributes = telemetry.Attributes{"service.namespace": app, "deployment.environment.name": env, "service.instance.id": instance}
		span.Events = []telemetry.Event{{Name: "exception", Time: span.StartTime, Attributes: telemetry.Attributes{"exception.type": kind, "exception.message": "message varies"}}}
		return span
	}
	spans := []telemetry.Span{
		makeSpan(1, "store", "prod", "pod-a", "Timeout"), makeSpan(2, "store", "prod", "pod-b", "Timeout"),
		makeSpan(3, "store", "staging", "pod-a", "Timeout"), makeSpan(4, "reporting", "prod", "pod-a", "Timeout"),
		makeSpan(5, "store", "prod", "pod-c", "ParseError"),
	}
	spans[1].Events[0].Attributes = telemetry.Attributes{"exception.type": "Timeout", "exception.message_hash": "strict-only-hash"}
	for i, span := range spans {
		observed := first.Add(time.Duration(min(i, 2)) * time.Second)
		if err := store.WriteSpans(t.Context(), observed, []telemetry.Span{span}); err != nil {
			t.Fatal(err)
		}
	}
	groups, err := store.FailureGroups(t.Context(), investigation.FailureFilter{Limit: 10}, nil)
	if err != nil || len(groups.Items) != 4 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	found := false
	for _, group := range groups.Items {
		if group.Count != 2 {
			continue
		}
		found = true
		if !group.FirstObserved.Equal(first) || !group.LastObserved.Equal(first.Add(time.Second)) || group.Environment != "prod" || group.ExceptionType != "Timeout" {
			t.Fatalf("group=%+v", group)
		}
		traces, err := store.Failures(t.Context(), investigation.FailureFilter{GroupKey: group.Key, Limit: 10})
		if err != nil || len(traces.Items) != 2 || traces.Items[0].TraceID != group.TraceID {
			t.Fatalf("drilldown=%+v, %v", traces, err)
		}
	}
	if !found {
		t.Fatal("instance IDs or message/hash split the group")
	}
	app := "store"
	filtered, err := store.FailureGroups(t.Context(), investigation.FailureFilter{Application: &app, Search: "Timeout", Limit: 10}, nil)
	if err != nil || len(filtered.Items) != 2 {
		t.Fatalf("filter=%+v, %v", filtered, err)
	}
	// The cursor order must not duplicate or omit groups, including tied times.
	var after *investigation.GroupCursor
	seen := map[string]bool{}
	for {
		page, err := store.FailureGroups(t.Context(), investigation.FailureFilter{Limit: 1}, after)
		if err != nil {
			t.Fatal(err)
		}
		for _, group := range page.Items {
			if seen[group.Key] {
				t.Fatal("duplicate group")
			}
			seen[group.Key] = true
		}
		if page.Next == nil {
			break
		}
		after = page.Next
	}
	if len(seen) != 4 {
		t.Fatalf("paged groups=%d", len(seen))
	}
	if err := store.Prune(t.Context(), first.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	groups, err = store.FailureGroups(t.Context(), investigation.FailureFilter{Limit: 10}, nil)
	if err != nil || len(groups.Items) != 3 {
		t.Fatalf("retained groups=%+v, %v", groups, err)
	}
}

func TestFailureGroupCountsRequestOutcomesSeparately(t *testing.T) {
	store := openTestStore(t)
	now := time.Unix(100, 0)
	var spans []telemetry.Span
	for i, status := range []int64{200, 503, 404, 0} {
		id := byte(i + 1)
		root := testSpan(id, 1, "gateway", status)
		child := testSpan(id, 2, "orders", 500)
		child.ParentID = root.ID
		child.Events = []telemetry.Event{{
			Name: "exception", Time: child.StartTime,
			Attributes: telemetry.Attributes{"exception.type": "Timeout"},
		}}
		spans = append(spans, root, child)
	}
	if err := store.WriteSpans(t.Context(), now, spans); err != nil {
		t.Fatal(err)
	}
	groups, err := store.FailureGroups(t.Context(), investigation.FailureFilter{Limit: 1}, nil)
	if err != nil || len(groups.Items) != 1 {
		t.Fatalf("groups=%+v, %v", groups, err)
	}
	group := groups.Items[0]
	want := investigation.RequestOutcomes{HTTP2xx: 1, HTTP5xx: 1, HTTPOther: 1, HTTPUnknown: 1}
	if group.Count != 4 || group.Outcomes != want || group.HTTPStatus != 500 || group.SpanID != hex.EncodeToString(spans[7].ID[:]) {
		t.Fatalf("group=%+v, want outcomes %+v and latest child span", group, want)
	}
	filter := investigation.FailureFilter{GroupKey: group.Key, HTTPStatus: 200, Limit: 1}
	filtered, err := store.FailureGroups(t.Context(), filter, nil)
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Count != 1 ||
		filtered.Items[0].Outcomes.HTTP2xx != 1 || filtered.Items[0].TraceID != hex.EncodeToString(spans[0].TraceID[:]) {
		t.Fatalf("filtered=%+v, %v", filtered, err)
	}
}

func TestFailureGroupsLateEvidenceAndAssignment(t *testing.T) {
	store := openTestStore(t)
	span := testSpan(1, 1, "api", 500)
	if err := store.WriteSpans(t.Context(), time.Unix(1, 0), []telemetry.Span{span}); err != nil {
		t.Fatal(err)
	}
	revision := store.Revision()
	if err := store.SetServiceApplication(t.Context(), "api", "checkout"); err != nil {
		t.Fatal(err)
	}
	if store.Revision() <= revision {
		t.Fatal("assignment did not invalidate views")
	}
	group, err := store.FailureGroups(t.Context(), investigation.FailureFilter{Limit: 10}, nil)
	if err != nil || group.Items[0].Application != "checkout" {
		t.Fatalf("assignment=%+v, %v", group, err)
	}
	child := testSpan(1, 2, "payments", 0)
	child.ParentID = span.ID
	child.Events = []telemetry.Event{{Name: "exception", Time: child.StartTime, Attributes: telemetry.Attributes{"exception.type": "Timeout"}}}
	if err := store.WriteSpans(t.Context(), time.Unix(2, 0), []telemetry.Span{child}); err != nil {
		t.Fatal(err)
	}
	group, err = store.FailureGroups(t.Context(), investigation.FailureFilter{Limit: 10}, nil)
	if err != nil || len(group.Items) != 1 || group.Items[0].Service != "payments" || group.Items[0].Count != 1 {
		t.Fatalf("late primary evidence=%+v, %v", group, err)
	}
}
