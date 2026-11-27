package otlp

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestLiveAdmissionDrainsAndPrivacySnapshot(t *testing.T) {
	var limit atomic.Int32
	limit.Store(2)
	var strict atomic.Bool
	h := NewHandler(nil).WithPolicySource(func() Policy { return Policy{Concurrency: int(limit.Load()), Strict: strict.Load()} })
	h.wait = 0
	for range 2 {
		if !h.acquire(context.Background()) {
			t.Fatal("initial admission")
		}
	}
	limit.Store(1)
	if h.acquire(context.Background()) {
		t.Fatal("limit decrease admitted an export")
	}
	h.release()
	if h.acquire(context.Background()) {
		t.Fatal("did not drain to new limit")
	}
	h.release()
	if !h.acquire(context.Background()) {
		t.Fatal("new limit not applied")
	}
	h.release()
	ctx := h.capturePolicy(context.Background())
	strict.Store(true)
	if h.policyFor(ctx).Strict || !h.policyFor(context.Background()).Strict {
		t.Fatal("in-flight privacy changed")
	}
}
