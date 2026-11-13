package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestGroupStateLifecycle(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "states.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	received := time.Unix(1_700_000_000, 0).UTC()
	if err := store.WriteSpans(ctx, received, []telemetry.Span{failingSpan(1)}); err != nil {
		t.Fatal(err)
	}
	groups := func(state investigation.GroupState) []investigation.FailureGroup {
		t.Helper()
		result, err := store.FailureGroups(ctx, investigation.FailureFilter{Limit: 10, GroupState: state}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return result.Items
	}
	open := groups("")
	if len(open) != 1 || open[0].State != investigation.GroupOpen || open[0].StateChanged != nil {
		t.Fatalf("open groups = %+v", open)
	}
	key := open[0].Key

	resolvedAt := received.Add(time.Minute)
	if err := store.SetGroupState(ctx, key, investigation.GroupResolved, "", resolvedAt); err != nil {
		t.Fatal(err)
	}
	if got := groups(""); len(got) != 0 {
		t.Fatalf("resolved group still open: %+v", got)
	}
	if got := groups(investigation.GroupResolved); len(got) != 1 || got[0].State != investigation.GroupResolved || !got[0].StateChanged.Equal(resolvedAt) {
		t.Fatalf("resolved groups = %+v", got)
	}

	// A matching trace received after resolution reopens the group.
	if err := store.WriteSpans(ctx, resolvedAt.Add(time.Minute), []telemetry.Span{failingSpan(2)}); err != nil {
		t.Fatal(err)
	}
	if got := groups(""); len(got) != 1 || got[0].State != investigation.GroupReopened || got[0].Count != 2 {
		t.Fatalf("reopened groups = %+v", got)
	}
	if got := groups(investigation.GroupResolved); len(got) != 0 {
		t.Fatalf("reopened group listed as resolved: %+v", got)
	}

	if notified, err := store.GroupNotified(ctx, key); err != nil || !notified.IsZero() {
		t.Fatalf("notified = %v, %v", notified, err)
	}
	if err := store.MarkGroupNotified(ctx, key, resolvedAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if notified, err := store.GroupNotified(ctx, key); err != nil || !notified.Equal(resolvedAt.Add(2*time.Minute)) {
		t.Fatalf("notified = %v, %v", notified, err)
	}

	if err := store.SetGroupState(ctx, key, investigation.GroupMuted, "", resolvedAt.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := groups(""); len(got) != 0 {
		t.Fatalf("muted group open: %+v", got)
	}
	if got := groups(investigation.GroupMuted); len(got) != 1 || got[0].State != investigation.GroupMuted {
		t.Fatalf("muted groups = %+v", got)
	}
	if got := groups(investigation.GroupAll); len(got) != 1 {
		t.Fatalf("all groups = %+v", got)
	}

	if err := store.SetGroupState(ctx, key, investigation.GroupOpen, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := groups(""); len(got) != 1 || got[0].State != investigation.GroupOpen {
		t.Fatalf("reopened by user = %+v", got)
	}
	if err := store.SetGroupState(ctx, key, investigation.GroupReopened, "", time.Now()); err == nil {
		t.Fatal("accepted reopened as a stored state")
	}

	// Retention removes states older than the cutoff with the traces.
	if err := store.SetGroupState(ctx, key, investigation.GroupMuted, "", received); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(ctx, received.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var states int
	if err := store.read.QueryRowContext(ctx, "SELECT count(*) FROM group_states").Scan(&states); err != nil || states != 0 {
		t.Fatalf("group states after prune = %d, %v", states, err)
	}
}

func failingSpan(traceID byte) telemetry.Span {
	span := testSpan(traceID, 1, "api", 503)
	span.StatusCode = telemetry.StatusError
	return span
}

func TestResolvedInVersion(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "versions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	versioned := func(traceID byte, version string) []telemetry.Span {
		span := failingSpan(traceID)
		span.ResourceAttributes = telemetry.Attributes{"service.version": version}
		return []telemetry.Span{span}
	}
	received := time.Unix(1_700_000_000, 0).UTC()
	if err := store.WriteSpans(ctx, received, versioned(1, "1.1.0")); err != nil {
		t.Fatal(err)
	}
	groups := func() []investigation.FailureGroup {
		t.Helper()
		result, err := store.FailureGroups(ctx, investigation.FailureFilter{Limit: 10, GroupState: investigation.GroupAll}, nil)
		if err != nil || len(result.Items) != 1 {
			t.Fatalf("groups = %+v, %v", result, err)
		}
		return result.Items
	}
	key := groups()[0].Key
	if err := store.SetGroupState(ctx, key, investigation.GroupMuted, "1.2.0", received); err == nil {
		t.Fatal("accepted a version for a muted group")
	}
	if err := store.SetGroupState(ctx, key, investigation.GroupResolved, "1.2.0", received.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Instances still running the old version don't reopen the group.
	if err := store.WriteSpans(ctx, received.Add(2*time.Minute), versioned(2, "1.1.0")); err != nil {
		t.Fatal(err)
	}
	if got := groups()[0]; got.State != investigation.GroupResolved || got.ResolvedVersion != "1.2.0" || got.Count != 2 {
		t.Fatalf("after an old-version trace = %+v", got)
	}
	if err := store.WriteSpans(ctx, received.Add(3*time.Minute), versioned(3, "1.2.0")); err != nil {
		t.Fatal(err)
	}
	if got := groups()[0]; got.State != investigation.GroupReopened {
		t.Fatalf("after a fixed-version trace = %+v", got)
	}
}
