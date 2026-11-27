package sqlite

import (
	"context"
	"time"

	"github.com/YunusEmreAlps/geko/internal/storage"
)

type capacityKey struct{}

// WithCapacitySource is startup-only; the supplier publishes immutable values.
func (s *Store) WithCapacitySource(source func() storage.CapacityPolicy) *Store {
	s.capacitySource = source
	return s
}
func (s *Store) capacity(ctx context.Context) storage.CapacityPolicy {
	if policy, ok := ctx.Value(capacityKey{}).(storage.CapacityPolicy); ok {
		return policy
	}
	if s.capacitySource != nil {
		return s.capacitySource()
	}
	return storage.CapacityPolicy{MaxBytes: s.maxBytes, OnFull: s.onFull}
}
func (s *Store) captureCapacity(ctx context.Context) context.Context {
	if _, ok := ctx.Value(capacityKey{}).(storage.CapacityPolicy); ok {
		return ctx
	}
	return context.WithValue(ctx, capacityKey{}, s.capacity(ctx))
}

// RetentionPreview is an indexed count of currently eligible whole traces.
// It does not predict future ingest, compaction or ongoing maintenance.
func (s *Store) RetentionPreview(ctx context.Context, cutoff time.Time) (int64, error) {
	var count int64
	err := s.read.QueryRowContext(ctx, "SELECT count(*) FROM traces WHERE last_observed_ns < ? "+unpinned, cutoff.UnixNano()).Scan(&count)
	return count, err
}

func (s *Store) BudgetRetentionPreview(ctx context.Context, cutoff time.Time) (int64, error) {
	var count int64
	err := s.read.QueryRowContext(ctx, "SELECT count(*) FROM budget_checkpoints WHERE window_ns < ?", cutoff.UnixNano()).Scan(&count)
	return count, err
}
