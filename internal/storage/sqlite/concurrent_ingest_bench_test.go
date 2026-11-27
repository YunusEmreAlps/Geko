package sqlite

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

// BenchmarkConcurrentIngest isolates persistence from HTTP admission, decoding,
// normalization, and the Python load generator. Use a fixed iteration count so
// every producer count writes the same total work into a fresh database:
//
// go test ./internal/storage/sqlite -run '^$' -bench '^BenchmarkConcurrentIngest$' -benchtime=512x -count=3
//
// Run one producer count at a time with -cpuprofile, -blockprofile and
// -mutexprofile to distinguish writer execution from callers waiting for slots
// or commit acknowledgement. Profiling runs should not be used for throughput
// comparisons against runs without profiling.
func BenchmarkConcurrentIngest(b *testing.B) {
	for _, producers := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("producers=%d", producers), func(b *testing.B) {
			store, err := Open(b.Context(), filepath.Join(b.TempDir(), "ingest.db"))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := store.Close(); err != nil {
					b.Error(err)
				}
			})
			// Match the HTTP fixed-total harness's 32 sequential warm-up exports.
			const warmup = 32
			observed := time.Unix(1_800_000_000, 0).UTC()
			for export := range warmup {
				if err := store.WriteSpans(b.Context(), observed, concurrentIngestExport(export, b.N+warmup)); err != nil {
					b.Fatal(err)
				}
			}
			exports := make([][]telemetry.Span, b.N)
			for export := range exports {
				exports[export] = concurrentIngestExport(export+warmup, b.N+warmup)
			}
			latencies := make([]time.Duration, b.N)
			start := make(chan struct{})
			errors := make(chan error, producers)
			var ready, finished sync.WaitGroup
			ready.Add(producers)
			finished.Add(producers)
			for producer := range producers {
				go func() {
					defer finished.Done()
					ready.Done()
					<-start
					for export := producer; export < len(exports); export += producers {
						began := time.Now()
						if err := store.WriteSpans(b.Context(), observed, exports[export]); err != nil {
							errors <- err
							return
						}
						latencies[export] = time.Since(began)
					}
				}()
			}
			ready.Wait()
			b.ReportAllocs()
			b.ResetTimer()
			close(start)
			finished.Wait()
			b.StopTimer()
			close(errors)
			for err := range errors {
				b.Fatal(err)
			}
			var spans, traces int
			if err := store.read.QueryRowContext(b.Context(), "SELECT (SELECT count(*) FROM spans),(SELECT count(*) FROM traces)").Scan(&spans, &traces); err != nil {
				b.Fatal(err)
			}
			if spans != (b.N+warmup)*128 || traces != (b.N+warmup)*64 {
				b.Fatalf("stored %d spans/%d traces, want %d/%d", spans, traces, (b.N+warmup)*128, (b.N+warmup)*64)
			}
			slices.Sort(latencies)
			b.ReportMetric(float64(b.N*128)/b.Elapsed().Seconds(), "spans/s")
			b.ReportMetric(float64(latencies[(b.N-1)/2])/float64(time.Millisecond), "p50-export-ms")
			b.ReportMetric(float64(latencies[(b.N*95+99)/100-1])/float64(time.Millisecond), "p95-export-ms")
		})
	}
}

// Match scripts/bench-native.py's normalized fixture: 64 two-span traces per
// export, both spans failing with HTTP 503 and captured OTel metadata. Build it
// before starting the timer so client-side generation cannot limit ingestion.
func concurrentIngestExport(export, totalExports int) []telemetry.Span {
	spans := make([]telemetry.Span, 0, 128)
	end := time.Unix(1_800_000_000, 0).UTC()
	resource := telemetry.Attributes{"service.name": "benchmark", "service.namespace": "otlp-json-demo"}
	for offset := range 64 {
		number := uint64(export*64 + offset + 1)
		root := telemetry.Span{
			TraceID: telemetry.TraceID{0x92, 0x7c, 0x58, 0x3d, 0x26, 0x4b, 0x4c, 0xc4},
			OTel:    telemetry.Metadata{Present: true}, ServiceName: "benchmark",
			Name: "GET /inventory", Kind: telemetry.KindServer,
			StartTime: end.Add(-3 * time.Millisecond), EndTime: end,
			StatusCode:         telemetry.StatusError,
			ResourceAttributes: resource, Scope: telemetry.Scope{Attributes: telemetry.Attributes{}},
			Attributes: telemetry.Attributes{"http.response.status_code": int64(503)},
			Events:     []telemetry.Event{},
		}
		binary.BigEndian.PutUint64(root.TraceID[8:], number)
		binary.BigEndian.PutUint64(root.ID[:], number)
		child := root
		child.ParentID = root.ID
		binary.BigEndian.PutUint64(child.ID[:], number+uint64(totalExports*64))
		child.Name, child.Kind = "inventory.lookup", telemetry.KindInternal
		child.StartTime, child.EndTime = end.Add(-2*time.Millisecond), end.Add(-time.Millisecond)
		spans = append(spans, root, child)
	}
	return spans
}
