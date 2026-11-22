package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func groupRows(ctx context.Context, db *sql.DB, query string, args []any) ([][]any, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var result [][]any
	for rows.Next() {
		values, pointers := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		for i, value := range values {
			if data, ok := value.([]byte); ok {
				values[i] = bytes.Clone(data)
			}
		}
		result = append(result, values)
	}
	return result, rows.Err()
}

func assertGroupProjection(t *testing.T, store *Store, filter investigation.FailureFilter, after *investigation.GroupCursor) {
	t.Helper()
	matching, args := legacyGroupMatches(filter)
	legacy, args, err := assembleGroupsQuery(filter, after, matching, args)
	if err != nil {
		t.Fatal(err)
	}
	want, err := groupRows(t.Context(), store.read, legacy, args)
	if err != nil {
		t.Fatal(err)
	}
	query, args, err := failureGroupsQuery(filter, after)
	if err != nil {
		t.Fatal(err)
	}
	got, err := groupRows(t.Context(), store.read, query, args)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("filter %+v after %+v:\nprojected %v\nlegacy %v\nerror %v", filter, after, got, want, err)
	}
}

func TestGroupProjectionMatchesLegacyAfterChanges(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC()
	for i := range 24 {
		root := testSpan(byte(i+1), 1, "api", int64(200+i%2*303))
		child := testSpan(byte(i+1), 2, "worker", 500)
		child.ParentID = root.ID
		child.ResourceAttributes = telemetry.Attributes{"service.version": fmt.Sprint(i % 2), "deployment.environment.name": "prod"}
		if i%2 == 0 {
			child.ResourceAttributes["service.namespace"] = "shop"
		}
		if i%3 == 0 {
			root.Kind = telemetry.KindClient // no root server yet
		}
		if i == 22 {
			child.Name = strings.Repeat("large-operation-", 200) // exact uncached fallback
		}
		if err := store.WriteSpans(t.Context(), now.Add(time.Duration(i)*time.Second), []telemetry.Span{root, child}); err != nil {
			t.Fatal(err)
		}
	}
	var uncached int
	if err := store.read.QueryRowContext(t.Context(), "SELECT count(*) FROM failure_group_members WHERE partition_id IS NULL").Scan(&uncached); err != nil || uncached != 1 {
		t.Fatalf("large metadata fallback = %d rows: %v", uncached, err)
	}
	if err := store.WriteSpans(t.Context(), now, []telemetry.Span{testSpan(200, 1, "api", 200)}); err != nil {
		t.Fatal(err)
	}
	var members int
	if err := store.read.QueryRowContext(t.Context(), "SELECT count(*) FROM failure_group_members").Scan(&members); err != nil || members != 24 {
		t.Fatalf("healthy trace added to failure projection: %d members: %v", members, err)
	}
	if err := store.SetServiceApplication(t.Context(), "worker", "mapped"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignTraceApplication(t.Context(), now, now.Add(time.Minute), "api", "assigned"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTracePin(t.Context(), telemetry.TraceID{1}, true, "tester", now); err != nil {
		t.Fatal(err)
	}
	// A late exception replaces primary HTTP evidence. A late root changes the
	// request outcome; retransmission updates receipt times but not span data.
	late := testSpan(1, 3, "worker", 200)
	late.ParentID = telemetry.SpanID{1}
	late.ResourceAttributes = telemetry.Attributes{"geko.run.id": "run-1"}
	late.Attributes["geko.hmac.customer"] = strings.Repeat("a", 64)
	late.Events = []telemetry.Event{{Name: "exception", Attributes: telemetry.Attributes{"exception.type": "Timeout"}}}
	lateRoot := testSpan(1, 4, "api", 200)
	for _, spans := range [][]telemetry.Span{{late, lateRoot}, {late}, {testSpan(2, 3, "gateway", 200)}} {
		if err := store.WriteSpans(t.Context(), now.Add(time.Minute), spans); err != nil {
			t.Fatal(err)
		}
	}
	groups, err := store.FailureGroups(t.Context(), investigation.FailureFilter{Limit: 100}, nil)
	if err != nil || len(groups.Items) == 0 {
		t.Fatal(groups, err)
	}
	if err := store.SetGroupState(t.Context(), groups.Items[0].Key, investigation.GroupResolved, "", "tester", now); err != nil {
		t.Fatal(err)
	}
	if err := store.SetGroupNote(t.Context(), groups.Items[0].Key, "Investigating", "", "tester", now); err != nil {
		t.Fatal(err)
	}
	from, to := now.Add(5*time.Second), now.Add(15*time.Second)
	filters := []investigation.FailureFilter{
		{}, {From: &from, To: &to}, {Pinned: true}, {HTTPStatus: 200}, {Service: "api"},
		{GroupState: investigation.GroupAll}, {GroupState: investigation.GroupResolved},
		{GroupState: investigation.GroupMuted}, {GroupKey: groups.Items[0].Key},
		{Search: "Timeout"}, {EvidenceService: "worker"}, {Search: "  "},
		{RunID: "run-1"}, {AttributeDigest: strings.Repeat("a", 64)},
	}
	for _, app := range []string{"shop", "mapped", "assigned", "", "missing"} {
		filters = append(filters, investigation.FailureFilter{Application: &app}, investigation.FailureFilter{Application: &app, Service: "api"})
	}
	for _, filter := range filters {
		filter.Limit = 2
		assertGroupProjection(t, store, filter, nil)
		assertGroupProjection(t, store, filter, &investigation.GroupCursor{TimeNS: groups.Items[0].LastObserved.UnixNano(), Key: groups.Items[0].Key})
	}
	canonical, err := groupRows(t.Context(), store.read, "SELECT * FROM failure_group_refresh_source ORDER BY trace_id", nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := groupRows(t.Context(), store.read, "WITH batch AS MATERIALIZED(SELECT * FROM failure_group_refresh_input) SELECT "+preparedGroupFields+" FROM batch ORDER BY trace_id", nil)
	if err != nil || !reflect.DeepEqual(prepared, canonical) {
		t.Fatalf("batch refresh differs from canonical eligibility: %v", err)
	}
	// Mappings and historical assignment changes must not require rebuilding.
	if err := store.SetServiceApplication(t.Context(), "worker", "remapped"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignTraceApplication(t.Context(), now, now.Add(2*time.Minute), "api", "reassigned"); err != nil {
		t.Fatal(err)
	}
	assertGroupProjection(t, store, investigation.FailureFilter{Limit: 100, GroupState: investigation.GroupAll}, nil)
	if err := store.Prune(t.Context(), now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	assertGroupProjection(t, store, investigation.FailureFilter{Limit: 100, GroupState: investigation.GroupAll}, nil)
	var stale int
	if err := store.read.QueryRowContext(t.Context(), `SELECT count(*) FROM failure_group_members m LEFT JOIN traces t USING(trace_id) WHERE t.trace_id IS NULL`).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("orphaned group members = %d: %v", stale, err)
	}
}

func TestGroupProjectionFailureRollsBackExport(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.ExecContext(t.Context(), `CREATE TRIGGER reject_group_projection
 BEFORE INSERT ON failure_group_members BEGIN SELECT RAISE(ABORT, 'projection failure'); END`); err != nil {
		t.Fatal(err)
	}
	spans := []telemetry.Span{testSpan(1, 1, "api", 503)}
	if err := store.WriteSpans(t.Context(), time.Now(), spans); err == nil {
		t.Fatal("expected projection failure")
	}
	for _, table := range []string{"traces", "spans", "span_evidence", "trace_services", "failure_group_members", "failure_group_keys", "failure_group_partitions"} {
		var count int
		if err := store.read.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s after failed export: %d rows: %v", table, count, err)
		}
	}
	if _, err := store.db.ExecContext(t.Context(), "DROP TRIGGER reject_group_projection"); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSpans(t.Context(), time.Now(), spans); err != nil {
		t.Fatal(err)
	}
	assertGroupProjection(t, store, investigation.FailureFilter{Limit: 10}, nil)
}

func TestGroupProjectionRefreshUsesTraceIndex(t *testing.T) {
	store := openTestStore(t)
	rows, err := store.read.QueryContext(t.Context(), `EXPLAIN QUERY PLAN
 SELECT * FROM failure_group_refresh_input WHERE trace_id IN (?,?)`, make([]byte, 16), make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var steps []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(steps, "\n")
	if !strings.Contains(plan, "SEARCH t USING PRIMARY KEY") || strings.Contains(plan, "SCAN t") || strings.Contains(plan, "SCAN s") {
		t.Fatalf("group refresh must only read affected trace identities:\n%s", plan)
	}
}

func BenchmarkFailureGroupProjection(b *testing.B) {
	store := seededStore(b)
	f := investigation.FailureFilter{Limit: 25}
	legacy, args := legacyGroupMatches(f)
	legacy, legacyArgs, err := assembleGroupsQuery(f, nil, legacy, args)
	if err != nil {
		b.Fatal(err)
	}
	projected, projectedArgs, err := failureGroupsQuery(f, nil)
	if err != nil {
		b.Fatal(err)
	}
	for _, variant := range []struct {
		name, query string
		args        []any
	}{{"legacy", legacy, legacyArgs}, {"projected", projected, projectedArgs}} {
		b.Run(variant.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				rows, err := groupRows(b.Context(), store.read, variant.query, variant.args)
				if err != nil || len(rows) == 0 {
					b.Fatal(rows, err)
				}
			}
		})
	}
}
