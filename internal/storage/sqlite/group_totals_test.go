package sqlite

import (
	"fmt"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func assertGroupTotals(t *testing.T, store *Store) {
	t.Helper()
	const query = `SELECT count(*) FROM failure_group_partitions p WHERE
 p.members<>(SELECT count(*) FROM failure_group_members WHERE partition_id=p.id)
 OR p.first_ns<>(SELECT min(first_ns) FROM failure_group_members WHERE partition_id=p.id)
 OR p.last_ns<>(SELECT max(last_ns) FROM failure_group_members WHERE partition_id=p.id)
 OR p.sample<>(SELECT max(printf('%020d',last_ns)||hex(trace_id)||hex(span_id)) FROM failure_group_members WHERE partition_id=p.id)
 OR p.members=0`
	var bad int
	if err := store.read.QueryRowContext(t.Context(), query).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("inconsistent partition totals: %d: %v", bad, err)
	}
	if err := store.read.QueryRowContext(t.Context(), `SELECT count(*) FROM failure_group_keys k
 WHERE NOT EXISTS(SELECT 1 FROM failure_group_partitions WHERE key_id=k.id)`).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("unused group identities: %d: %v", bad, err)
	}
}

func TestHourlyGroupTotalsMatchExactWindows(t *testing.T) {
	for _, base := range []time.Time{time.Unix(1_800_000_000, 0).UTC().Truncate(time.Hour), time.Unix(-7200, 0).UTC()} {
		t.Run(fmt.Sprint(base.Unix()), func(t *testing.T) {
			store := openTestStore(t)
			for i := range 90 {
				span := testSpan(byte(i+1), 1, "api", int64([]int{200, 503, 0}[i%3]))
				span.StatusCode = telemetry.StatusError
				span.ResourceAttributes = telemetry.Attributes{"service.version": fmt.Sprint(i % 2)}
				if i%4 == 0 {
					span.ResourceAttributes["service.namespace"] = "shop"
				}
				at := base.Add(time.Duration(i) * 4 * time.Minute)
				if err := store.WriteSpans(t.Context(), at, []telemetry.Span{span}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.SetServiceApplication(t.Context(), "api", "mapped"); err != nil {
				t.Fatal(err)
			}
			// Move a trace between hours and versions, preserving its first receipt.
			late := testSpan(1, 2, "worker", 503)
			late.ParentID = telemetry.SpanID{1}
			late.Events = []telemetry.Event{{Name: "exception", Attributes: telemetry.Attributes{"exception.type": "LateError"}}}
			if err := store.WriteSpans(t.Context(), base.Add(7*time.Hour), []telemetry.Span{late}); err != nil {
				t.Fatal(err)
			}
			if err := store.SetTracePin(t.Context(), telemetry.TraceID{2}, true, "test", base); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AssignTraceApplication(t.Context(), base.Add(2*time.Hour), base.Add(4*time.Hour), "api", "assigned"); err != nil {
				t.Fatal(err)
			}
			assertGroupTotals(t, store)
			groups, err := store.FailureGroups(t.Context(), investigation.FailureFilter{Limit: 100}, nil)
			if err != nil || len(groups.Items) == 0 {
				t.Fatal(groups, err)
			}
			if err := store.SetGroupState(t.Context(), groups.Items[0].Key, investigation.GroupResolved, "1", "test", base.Add(3*time.Hour)); err != nil {
				t.Fatal(err)
			}
			app := "assigned"
			for _, filter := range []investigation.FailureFilter{{}, {Pinned: true}, {Service: "api"}, {Application: &app}, {Service: "api", Application: &app}, {HTTPStatus: 200}, {GroupState: investigation.GroupAll}, {GroupState: investigation.GroupResolved}} {
				filter.Limit = 2
				assertGroupProjection(t, store, filter, nil)
				fromOnly := base.Add(35 * time.Minute)
				filter.From = &fromOnly
				assertGroupProjection(t, store, filter, nil)
				filter.From = nil
				toOnly := base.Add(4*time.Hour + 17*time.Minute)
				filter.To = &toOnly
				assertGroupProjection(t, store, filter, nil)
				filter.To = nil
				for _, window := range [][2]time.Duration{{0, 6 * time.Hour}, {time.Nanosecond, 6*time.Hour - time.Nanosecond}, {45 * time.Minute, 4*time.Hour + 15*time.Minute}, {time.Hour, 5 * time.Hour}, {2*time.Hour + time.Nanosecond, 2*time.Hour + 55*time.Minute}, {3 * time.Hour, 3 * time.Hour}} {
					from, to := base.Add(window[0]), base.Add(window[1])
					filter.From, filter.To = &from, &to
					assertGroupProjection(t, store, filter, nil)
					assertGroupProjection(t, store, filter, &investigation.GroupCursor{TimeNS: base.Add(3 * time.Hour).UnixNano(), Key: groups.Items[0].Key})
				}
			}
			if err := store.Prune(t.Context(), base.Add(5*time.Hour)); err != nil {
				t.Fatal(err)
			}
			assertGroupTotals(t, store)
			assertGroupProjection(t, store, investigation.FailureFilter{Limit: 100, GroupState: investigation.GroupAll}, nil)
			if err := store.SetTracePin(t.Context(), telemetry.TraceID{2}, false, "test", base); err != nil {
				t.Fatal(err)
			}
			if err := store.Prune(t.Context(), base.Add(10*time.Hour)); err != nil {
				t.Fatal(err)
			}
			assertGroupTotals(t, store)
			var remaining int
			if err := store.read.QueryRowContext(t.Context(), "SELECT count(*) FROM failure_group_keys").Scan(&remaining); err != nil || remaining != 0 {
				t.Fatalf("identities not collected: %d: %v", remaining, err)
			}
		})
	}
}

func TestWholeHourGroupingDoesNotReadNormalMembers(t *testing.T) {
	store := openTestStore(t)
	from := time.Unix(3600, 0)
	query, args, ok := summarizedGroupMatches(investigation.FailureFilter{From: &from, Limit: 10})
	if !ok {
		t.Fatal("expected hourly totals")
	}
	rows, err := store.read.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var step string
		if err := rows.Scan(&id, &parent, &unused, &step); err != nil {
			t.Fatal(err)
		}
		// Only the partial index of exceptional metadata may read members.
		if step == "SCAN m" || step == "SCAN failure_group_members" {
			t.Fatalf("whole-hour query scans trace members: %s", step)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
