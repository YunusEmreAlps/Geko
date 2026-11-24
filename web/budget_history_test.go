package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
)

func TestBudgetHistoryAPIAndPage(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	checkpoint := budget.Checkpoint{Session: strings.Repeat("a", 32), Start: hour, Since: hour, Until: hour.Add(time.Hour), Spans: 5, Bytes: 100, Services: []budget.Volume{{Service: "api", Spans: 5, Bytes: 100}}}
	if err := store.SaveBudgetCheckpoints(t.Context(), time.Now(), 7, []budget.Checkpoint{checkpoint}); err != nil {
		t.Fatal(err)
	}
	tracker := budget.New(budget.Config{Enabled: true, HistoryDays: 7}, time.Now())
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{TelemetryBudget: tracker}); err != nil {
		t.Fatal(err)
	}
	query := "?hour=" + url.QueryEscape(hour.Format(time.RFC3339))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1/api/v1/budget"+query, nil))
	var report budget.Report
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &report) != nil || report.Total.Spans != 5 || report.Partial || !report.CardinalitySince.IsZero() {
		t.Fatalf("history API %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1/budget"+query, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Cardinality is unavailable for persisted history") || !strings.Contains(w.Body.String(), "5 spans") {
		t.Fatalf("history page %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1/api/v1/budget?hour=invalid", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatal("invalid hour was silently ignored")
	}
}
