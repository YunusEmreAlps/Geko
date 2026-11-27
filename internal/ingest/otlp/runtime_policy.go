package otlp

import (
	"container/list"
	"context"
	"time"
)

type Policy struct {
	Concurrency int
	Strict      bool
}
type policyKey struct{}

// WithPolicySource is installed before sharing the receiver.
func (h *Receiver) WithPolicySource(source func() Policy) *Receiver {
	h.policySource = source
	return h
}
func (h *Receiver) policy() Policy {
	p := Policy{Concurrency: h.concurrency, Strict: h.strict}
	if h.policySource != nil {
		p = h.policySource()
	}
	if p.Concurrency < 1 {
		p.Concurrency = maxConcurrent
	}
	if p.Concurrency > 64 {
		p.Concurrency = 64
	}
	return p
}
func (h *Receiver) capturePolicy(ctx context.Context) context.Context {
	return context.WithValue(ctx, policyKey{}, h.policy())
}
func (h *Receiver) policyFor(ctx context.Context) Policy {
	if p, ok := ctx.Value(policyKey{}).(Policy); ok {
		return p
	}
	return h.policy()
}

// admissionWaiter receives a reservation, rather than racing other requests
// for a shared wakeup. All queue/reservation fields are protected by admissionMu.
type admissionWaiter struct {
	ctx      context.Context
	deadline time.Time
	ready    chan struct{}
	element  *list.Element
	granted  bool
}

// dispatchAdmissions runs under admissionMu. Active includes reservations so a
// newly arriving request cannot steal a slot before an older goroutine resumes.
func (h *Receiver) dispatchAdmissions() {
	limit, now := h.policy().Concurrency, time.Now()
	for h.active < limit && h.admissionWaiters.Len() > 0 {
		element := h.admissionWaiters.Front()
		waiter := element.Value.(*admissionWaiter)
		h.admissionWaiters.Remove(element)
		waiter.element = nil
		if waiter.ctx.Err() == nil && now.Before(waiter.deadline) {
			waiter.granted = true
			h.active++
		}
		close(waiter.ready)
	}
}

func (h *Receiver) tryAcquire() bool {
	h.admissionMu.Lock()
	defer h.admissionMu.Unlock()
	h.dispatchAdmissions()
	if h.admissionWaiters.Len() == 0 && h.active < h.policy().Concurrency {
		h.active++
		return true
	}
	return false
}

func (h *Receiver) release() {
	h.admissionMu.Lock()
	defer h.admissionMu.Unlock()
	h.active--
	h.dispatchAdmissions()
}

// resolveAdmission also returns a canceled/expired reservation, so cancellation
// racing a grant cannot leak capacity or block the next valid waiter.
func (h *Receiver) resolveAdmission(waiter *admissionWaiter, accept bool) bool {
	h.admissionMu.Lock()
	defer h.admissionMu.Unlock()
	if accept && waiter.granted && waiter.ctx.Err() == nil && time.Now().Before(waiter.deadline) {
		return true
	}
	if waiter.element != nil {
		h.admissionWaiters.Remove(waiter.element)
		waiter.element = nil
	}
	if waiter.granted {
		waiter.granted = false
		h.active--
	}
	h.dispatchAdmissions()
	return false
}

func (h *Receiver) acquire(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	if h.tryAcquire() {
		if ctx.Err() != nil {
			h.release()
			return false
		}
		return true
	}
	if h.wait <= 0 {
		return false
	}
	waiter := &admissionWaiter{ctx: ctx, deadline: time.Now().Add(h.wait), ready: make(chan struct{})}
	h.admissionMu.Lock()
	if ctx.Err() != nil {
		h.admissionMu.Unlock()
		return false
	}
	waiter.element = h.admissionWaiters.PushBack(waiter)
	h.dispatchAdmissions()
	h.admissionMu.Unlock()
	timeout := time.NewTimer(time.Until(waiter.deadline))
	defer timeout.Stop()
	// Poll only for runtime limit changes; releases notify granted waiters only.
	retry := time.NewTicker(25 * time.Millisecond)
	defer retry.Stop()
	for {
		select {
		case <-waiter.ready:
			return h.resolveAdmission(waiter, true)
		case <-ctx.Done():
			return h.resolveAdmission(waiter, false)
		case <-timeout.C:
			return h.resolveAdmission(waiter, false)
		case <-retry.C:
			h.admissionMu.Lock()
			h.dispatchAdmissions()
			h.admissionMu.Unlock()
		}
	}
}
