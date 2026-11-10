package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRejectInvalidQueries(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, nil)
	for _, tc := range []struct {
		path, host string
		status     int
	}{
		{"/api/v1/failures", "attacker.example:4318", http.StatusForbidden},
		{"/api/v1/failures?limit=101", "127.0.0.1:4318", http.StatusBadRequest},
		{"/api/v1/failures?status=700", "localhost", http.StatusBadRequest},
		{"/api/v1/failures?cursor=bogus", "[::1]:4318", http.StatusBadRequest},
		{"/api/v1/failures?from=3000-01-01T00:00:00Z", "localhost", http.StatusBadRequest},
		{"/api/v1/failures?from=2026-10-10T00:00:00Z&to=2026-10-01T00:00:00Z", "localhost", http.StatusBadRequest},
		{"/api/v1/traces/00000000000000000000000000000000", "localhost", http.StatusBadRequest},
		{"/api/v1/traces/not-hex", "localhost", http.StatusBadRequest},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.Host = tc.host
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("query response = %d, %v, want %d with JSON and no-store", w.Code, w.Header(), tc.status)
			}
		})
	}
}
