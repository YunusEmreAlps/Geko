package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

// versionedStore holds 100,000 traces, the first half from version 1.0.0 and
// the rest from 1.1.0, a quarter of each failing.
func versionedStore(b *testing.B) *Store {
	b.Helper()
	store, err := Open(b.Context(), filepath.Join(b.TempDir(), "versions.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	now := time.Now()
	for batch := range 200 {
		spans := benchExport(batch*500, 500)
		if batch >= 100 {
			for i := range spans {
				spans[i].ResourceAttributes["service.version"] = "1.1.0"
			}
		}
		if err := store.WriteSpans(b.Context(), now, spans); err != nil {
			b.Fatal(err)
		}
	}
	return store
}

func BenchmarkVersionComparisons100K(b *testing.B) {
	store := versionedStore(b)
	filter := investigation.RegressionFilter{Baseline: "1.0.0", Candidate: "1.1.0", Since: time.Now().Add(-time.Hour)}
	b.ReportAllocs()
	for b.Loop() {
		rows, err := store.VersionComparisons(b.Context(), filter)
		if err != nil || len(rows) == 0 {
			b.Fatal(rows, err)
		}
	}
}

// BenchmarkCompareTraceWithoutBaseline compares a failure of an operation that
// never succeeded, so no stored trace matches it.
func BenchmarkCompareTraceWithoutBaseline(b *testing.B) {
	store := versionedStore(b)
	spans := benchExport(1_000_000, 1)
	for i := range spans {
		spans[i].Name = "never-succeeds"
		spans[i].StatusCode = telemetry.StatusError
	}
	if err := store.WriteSpans(b.Context(), time.Now(), spans); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		source, baseline, err := store.CompareTrace(b.Context(), spans[0].TraceID)
		if err != nil || source.EntrySpan == nil || baseline != nil {
			b.Fatal(source, baseline, err)
		}
	}
}

// BenchmarkWriteSpansAtCapacity writes 100-span exports into a database
// already at its size limit, so every export evicts.
func BenchmarkWriteSpansAtCapacity(b *testing.B) {
	for _, policy := range []storage.FullPolicy{storage.EvictOldest, storage.EvictFair} {
		b.Run(string(policy), func(b *testing.B) {
			store, err := OpenWithPolicy(b.Context(), filepath.Join(b.TempDir(), "full.db"), 16<<20, policy)
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			now := time.Now()
			next := 0
			for status, err := store.Status(b.Context()); err == nil && status.EvictedTraces == 0; status, err = store.Status(b.Context()) {
				if err := store.WriteSpans(b.Context(), now, benchExport(next, 100)); err != nil {
					b.Fatal(err)
				}
				next += 100
				now = now.Add(time.Millisecond)
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := store.WriteSpans(b.Context(), now, benchExport(next, 20)); err != nil {
					b.Fatal(err)
				}
				next += 20
				now = now.Add(time.Millisecond)
			}
		})
	}
}

// BenchmarkCompareTraceWithRecentBaseline compares a failure of an operation
// that succeeds often, the common case.
func BenchmarkCompareTraceWithRecentBaseline(b *testing.B) {
	store := versionedStore(b)
	var failing telemetry.TraceID
	failing[15] = 1 // the first trace of benchExport fails
	b.ReportAllocs()
	for b.Loop() {
		source, baseline, err := store.CompareTrace(b.Context(), failing)
		if err != nil || source.Evidence == nil || baseline == nil {
			b.Fatal(source, baseline, err)
		}
	}
}

func BenchmarkStorageByApplication100K(b *testing.B) {
	store := versionedStore(b)
	b.ReportAllocs()
	for b.Loop() {
		usage, err := store.StorageByApplication(b.Context())
		if err != nil || len(usage.Applications) != 1 || usage.FairOwner != "n:Storefront" {
			b.Fatal(usage, err)
		}
	}
}
