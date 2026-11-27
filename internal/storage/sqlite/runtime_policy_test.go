package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/storage"
)

func TestCapacityPolicyCapturedForWork(t *testing.T) {
	policy := storage.CapacityPolicy{MaxBytes: 8 << 20, OnFull: storage.Reject}
	s := Store{capacitySource: func() storage.CapacityPolicy { return policy }}
	ctx := s.captureCapacity(context.Background())
	policy = storage.CapacityPolicy{MaxBytes: 16 << 20, OnFull: storage.EvictFair}
	if got := s.capacity(ctx); got.MaxBytes != 8<<20 || got.OnFull != storage.Reject {
		t.Fatal("captured write policy changed")
	}
	if s.capacity(context.Background()).OnFull != storage.EvictFair {
		t.Fatal("new work did not use live policy")
	}
}

func TestLargerLiveCapacityIgnoresStaleFullHint(t *testing.T) {
	s, err := OpenWithPolicy(t.Context(), filepath.Join(t.TempDir(), "hint.db"), 8<<20, storage.EvictOldest)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.WithCapacitySource(func() storage.CapacityPolicy {
		return storage.CapacityPolicy{MaxBytes: 16 << 20, OnFull: storage.EvictOldest}
	})
	s.full.Store(true)
	if s.mustEvict(s.captureCapacity(t.Context()), 1) {
		t.Fatal("increased limit evicted using an old full hint")
	}
}
