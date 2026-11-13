package web

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestCompareTraceUsesMatchingCleanSuccess(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "compare.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	write := func(id byte, namespace, environment, operation string, code int64, childError bool, duration time.Duration) string {
		t.Helper()
		traceID := telemetry.TraceID{id}
		root := telemetry.Span{TraceID: traceID, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer,
			ServiceName: "orders", Name: operation, StartTime: now, EndTime: now.Add(duration),
			Attributes:         telemetry.Attributes{"http.response.status_code": code},
			ResourceAttributes: telemetry.Attributes{"service.namespace": namespace, "deployment.environment.name": environment}}
		spans := []telemetry.Span{root}
		if childError {
			child := root
			child.ID, child.ParentID = telemetry.SpanID{2}, root.ID
			child.StatusCode = telemetry.StatusError
			spans = append(spans, child)
		}
		if err := store.WriteSpans(t.Context(), now.Add(time.Duration(id)*time.Second), spans); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(traceID[:])
	}
	source := write(1, "shop", "prod", "GET /orders", 200, true, 300*time.Millisecond)
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{}); err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1"+path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return w.Body.String()
	}
	path := "/traces/" + source
	if !strings.Contains(get(path), "Compare with success") || !strings.Contains(get(path+"/compare"), "No comparison available") {
		t.Fatal("missing comparison link or empty state")
	}
	write(2, "shop", "prod", "GET /orders", 200, false, 50*time.Millisecond)
	want := write(3, "shop", "prod", "GET /orders", 200, false, 100*time.Millisecond)
	wrong := []string{
		write(4, "shop", "staging", "GET /orders", 200, false, 50*time.Millisecond),
		write(5, "other", "prod", "GET /orders", 200, false, 50*time.Millisecond),
		write(6, "shop", "prod", "POST /orders", 200, false, 50*time.Millisecond),
		write(7, "shop", "prod", "GET /orders", 200, true, 50*time.Millisecond),
		write(8, "shop", "prod", "GET /orders", 404, false, 50*time.Millisecond),
	}
	body := get(path + "/compare")
	if !strings.Contains(body, want) || !strings.Contains(body, "200ms slower") {
		t.Fatalf("comparison missing baseline or duration difference: %s", body)
	}
	for _, id := range wrong {
		if strings.Contains(body, id) {
			t.Fatalf("unrelated trace selected: %s", id)
		}
	}
}
