package otlp

import (
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
func (h *Receiver) tryAcquire() (bool, <-chan struct{}) {
	h.admissionMu.Lock()
	defer h.admissionMu.Unlock()
	if h.admissionChanged == nil {
		h.admissionChanged = make(chan struct{})
	}
	if h.active < h.policy().Concurrency {
		h.active++
		return true, h.admissionChanged
	}
	return false, h.admissionChanged
}
func (h *Receiver) release() {
	h.admissionMu.Lock()
	defer h.admissionMu.Unlock()
	h.active--
	close(h.admissionChanged)
	h.admissionChanged = make(chan struct{})
}
func (h *Receiver) acquire(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	if ok, _ := h.tryAcquire(); ok {
		return true
	}
	if h.wait <= 0 {
		return false
	}
	timeout := time.NewTimer(h.wait)
	defer timeout.Stop()
	// Observe increased limits without requiring callbacks into a live exporter.
	retry := time.NewTicker(25 * time.Millisecond)
	defer retry.Stop()
	for {
		ok, changed := h.tryAcquire()
		if ok {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-timeout.C:
			return false
		case <-changed:
		case <-retry.C:
		}
	}
}
