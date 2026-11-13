package access

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProtectedRoutes(t *testing.T) {
	secret := strings.Repeat("0123456789abcdef", 2)
	token, err := ParseToken(secret)
	if err != nil {
		t.Fatal(err)
	}
	handler := token.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !Authenticated(r.Context()) {
			t.Error("handler did not receive authenticated context")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		name, method, path, credentials string
		status                          int
	}{
		{"no credentials", "GET", "/settings", "", 401},
		{"wrong token", "GET", "/healthz", "wrong", 401},
		{"browser sign-in", "GET", "/settings", "basic", 204},
		{"browser event stream", "GET", "/events/failures", "basic", 204},
		{"OTLP requires bearer", "POST", "/v1/traces", "basic", 401},
		{"OTLP bearer", "POST", "/v1/traces", "bearer", 204},
		{"cross-origin mutation", "POST", "/traces/import", "cross-site", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://geko.example"+tc.path, nil)
			switch tc.credentials {
			case "basic", "cross-site":
				r.SetBasicAuth("geko", secret)
			case "bearer":
				r.Header.Set("Authorization", "Bearer "+secret)
			case "wrong":
				r.Header.Set("Authorization", "Bearer wrong")
			}
			if tc.credentials == "cross-site" {
				r.Header.Set("Origin", "https://other.example")
				r.Header.Set("Sec-Fetch-Site", "cross-site")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
		})
	}
}
