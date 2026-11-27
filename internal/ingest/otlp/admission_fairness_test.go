package otlp

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Pause the oldest caller while its wait select is evaluated. This makes a
// released slot/new arrival race deterministic without scheduler sleeps.
type pausedWaitContext struct {
	context.Context
	entered, resume chan struct{}
	enter, unblock  sync.Once
}

func (p *pausedWaitContext) Done() <-chan struct{} {
	p.enter.Do(func() { close(p.entered); <-p.resume })
	return p.Context.Done()
}

func waitingExport(t *testing.T, h *Receiver) (*pausedWaitContext, <-chan bool, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	paused := &pausedWaitContext{Context: ctx, entered: make(chan struct{}), resume: make(chan struct{})}
	result, stopped := make(chan bool, 1), make(chan struct{})
	go func() { defer close(stopped); result <- h.acquire(paused) }()
	t.Cleanup(func() {
		cancel()
		paused.unpause()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Error("waiting admission did not stop")
		}
	})
	select {
	case <-paused.entered:
	case <-time.After(time.Second):
		t.Fatal("export did not reach admission wait")
	}
	return paused, result, cancel
}

func (p *pausedWaitContext) unpause() { p.unblock.Do(func() { close(p.resume) }) }

func TestAdmissionReservesReleasedSlotForOldestWaiter(t *testing.T) {
	h := NewHandler(nil).WithConcurrency(1)
	if !h.acquire(t.Context()) {
		t.Fatal("initial slot")
	}
	paused, result, _ := waitingExport(t, h)
	h.release()
	late := h.tryAcquire()
	if late {
		h.release()
	}
	paused.unpause()
	if !<-result {
		t.Fatal("oldest request was refused")
	}
	h.release()
	if late {
		t.Fatal("new arrival overtook a waiting export")
	}
}

func TestAdmissionLimitIncreasePrioritizesExistingWaiter(t *testing.T) {
	var limit atomic.Int32
	limit.Store(1)
	h := NewHandler(nil).WithPolicySource(func() Policy { return Policy{Concurrency: int(limit.Load())} })
	if !h.acquire(t.Context()) {
		t.Fatal("initial slot")
	}
	paused, result, _ := waitingExport(t, h)
	limit.Store(2)
	late := h.tryAcquire()
	if late {
		h.release()
	}
	paused.unpause()
	if !<-result {
		t.Fatal("waiting request did not observe increase")
	}
	h.release()
	h.release()
	if late {
		t.Fatal("increased capacity was taken ahead of an older request")
	}
}

func TestCanceledAdmissionReturnsReservedSlot(t *testing.T) {
	h := NewHandler(nil).WithConcurrency(1)
	if !h.acquire(t.Context()) {
		t.Fatal("initial slot")
	}
	paused, result, cancel := waitingExport(t, h)
	h.release()
	cancel()
	paused.unpause()
	if <-result {
		h.release()
		t.Fatal("canceled request was admitted")
	}
	if !h.acquire(t.Context()) {
		t.Fatal("canceled reservation leaked capacity")
	}
	h.release()
}

func TestAdmissionLimitIncreaseWakesWithoutARelease(t *testing.T) {
	var limit atomic.Int32
	limit.Store(1)
	h := NewHandler(nil).WithPolicySource(func() Policy { return Policy{Concurrency: int(limit.Load())} })
	if !h.acquire(t.Context()) {
		t.Fatal("initial slot")
	}
	paused, result, _ := waitingExport(t, h)
	limit.Store(2)
	paused.unpause()
	select {
	case admitted := <-result:
		if !admitted {
			t.Fatal("new capacity unavailable")
		}
	case <-time.After(time.Second):
		t.Fatal("limit increase did not wake waiting export")
	}
	h.release()
	h.release()
}

func TestExpiredAdmissionDoesNotReserveAReleasedSlot(t *testing.T) {
	h := NewHandler(nil).WithConcurrency(1)
	h.wait = 10 * time.Millisecond
	if !h.acquire(t.Context()) {
		t.Fatal("initial slot")
	}
	paused, result, _ := waitingExport(t, h)
	time.Sleep(20 * time.Millisecond)
	h.release()
	paused.unpause()
	if <-result {
		h.release()
		t.Fatal("expired export admitted")
	}
	if !h.acquire(t.Context()) {
		t.Fatal("expired waiter leaked capacity")
	}
	h.release()
}

func TestCanceledHeadDoesNotBlockNextWaiter(t *testing.T) {
	h := NewHandler(nil).WithConcurrency(1)
	if !h.acquire(t.Context()) {
		t.Fatal("initial slot")
	}
	first, firstResult, cancel := waitingExport(t, h)
	second, secondResult, _ := waitingExport(t, h)
	cancel()
	first.unpause()
	if <-firstResult {
		t.Fatal("canceled head was admitted")
	}
	h.release()
	second.unpause()
	if !<-secondResult {
		t.Fatal("next waiter blocked behind canceled head")
	}
	h.release()
}
