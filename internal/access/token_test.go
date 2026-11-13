package access

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const ingestSecret = "a1b2c3d4e5f60718293a4b5c6d7e8f90"

func TestProtectedRoutes(t *testing.T) {
	secret := strings.Repeat("0123456789abcdef", 2)
	token, err := ParseToken(secret)
	if err != nil {
		t.Fatal(err)
	}
	token, err = token.WithIngestTokens("storefront=" + ingestSecret)
	if err != nil {
		t.Fatal(err)
	}
	handler := token.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !Authenticated(r.Context()) && r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
			t.Error("handler did not receive authenticated context")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		name, method, path, credentials string
		status                          int
	}{
		{"no credentials", "GET", "/settings", "", 401},
		{"wrong token", "GET", "/settings", "wrong", 401},
		{"liveness probe", "GET", "/healthz", "", 204},
		{"readiness probe", "GET", "/readyz", "", 204},
		{"probe path is GET only", "POST", "/healthz", "", 401},
		{"browser sign-in", "GET", "/settings", "basic", 204},
		{"browser event stream", "GET", "/events/failures", "basic", 204},
		{"OTLP requires bearer", "POST", "/v1/traces", "basic", 401},
		{"OTLP bearer", "POST", "/v1/traces", "bearer", 204},
		{"OTLP ingest token", "POST", "/v1/traces", "ingest", 204},
		{"ingest token cannot read", "GET", "/settings", "ingest", 401},
		{"ingest token cannot query API", "GET", "/api/v1/failures", "ingest", 401},
		{"ingest token cannot import", "POST", "/traces/import", "ingest", 401},
		{"ingest token is not a password", "GET", "/settings", "ingest-basic", 401},
		{"cross-origin mutation", "POST", "/traces/import", "cross-site", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://geko.example"+tc.path, nil)
			switch tc.credentials {
			case "basic", "cross-site":
				r.SetBasicAuth("geko", secret)
			case "bearer":
				r.Header.Set("Authorization", "Bearer "+secret)
			case "ingest":
				r.Header.Set("Authorization", "Bearer "+ingestSecret)
			case "ingest-basic":
				r.SetBasicAuth("geko", ingestSecret)
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

func TestIngestTokens(t *testing.T) {
	admin, err := ParseToken(strings.Repeat("0123456789abcdef", 2))
	if err != nil {
		t.Fatal(err)
	}
	other := "f0e1d2c3b4a5968778695a4b3c2d1e0f"
	token, err := admin.WithIngestTokens(" storefront=" + ingestSecret + ", reporting.api=" + other + " ")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(token.Names(ScopeIngest), ","); got != "storefront,reporting.api" {
		t.Fatalf("names = %q", got)
	}
	if !token.IngestBearer("Bearer "+other) || !token.IngestBearer("bearer "+strings.Repeat("0123456789abcdef", 2)) {
		t.Fatal("configured tokens must authorize exports")
	}
	if token.Bearer("Bearer "+ingestSecret) || token.IngestBearer("Bearer wrong") || token.IngestBearer(ingestSecret) {
		t.Fatal("ingest token accepted as access token, or malformed header accepted")
	}
	if _, err := (Token{}).WithIngestTokens("storefront=" + ingestSecret); err == nil {
		t.Fatal("ingest tokens accepted without an access token")
	}
	for _, spec := range []string{
		"storefront",
		"=" + ingestSecret,
		"store front=" + ingestSecret,
		"storefront=short",
		"storefront=" + strings.Repeat("ab", 16),
		"storefront=" + ingestSecret + ",storefront=" + other,
		"storefront=" + ingestSecret + ",reporting=" + ingestSecret,
		"storefront=" + strings.Repeat("0123456789abcdef", 2),
		"storefront=" + ingestSecret + ",",
	} {
		if _, err := admin.WithIngestTokens(spec); err == nil {
			t.Errorf("accepted %q", spec)
		}
	}
}

func TestReadTokens(t *testing.T) {
	secret := strings.Repeat("0123456789abcdef", 2)
	read := "f0e1d2c3b4a5968778695a4b3c2d1e0f"
	token, err := ParseToken(secret)
	if err == nil {
		token, err = token.WithIngestTokens("storefront=" + ingestSecret)
	}
	if err == nil {
		token, err = token.WithReadTokens("agent=" + read)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := token.WithReadTokens("agent2=" + ingestSecret); err == nil {
		t.Fatal("read token reused an ingest token")
	}
	if _, err := token.WithReadTokens("agent=" + strings.Repeat("9876543210fedcba", 2)); err == nil {
		t.Fatal("read token name reused")
	}
	handler := token.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		method, path, credentials string
		status                    int
	}{
		{"GET", "/settings", "bearer", 204},
		{"GET", "/api/v1/failures", "bearer", 204},
		{"GET", "/events/failures", "basic", 204},
		{"POST", "/mcp", "bearer", 204},
		{"POST", "/traces/import", "bearer", 401},
		{"POST", "/applications", "basic", 401},
		{"POST", "/v1/traces", "bearer", 401},
	} {
		r := httptest.NewRequest(tc.method, "http://geko.example"+tc.path, nil)
		if tc.credentials == "basic" {
			r.SetBasicAuth("geko", read)
		} else {
			r.Header.Set("Authorization", "Bearer "+read)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %s with read token = %d, want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
	if got := strings.Join(token.Names(ScopeRead), ","); got != "agent" {
		t.Fatalf("read names = %q", got)
	}
}
