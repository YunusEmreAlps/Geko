package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"github.com/YunusEmreAlps/geko/internal/privacy"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
)

func TestProtectedAttributeSearch(t *testing.T) {
	policy, err := privacy.NewAttributeSearch("order.id,tenant.id", strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "protected.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	receiver := otlp.NewHandlerWithRedaction(store, true).WithAttributeSearch(policy)
	mux.Handle("POST /v1/traces", receiver)
	if err := Register(mux, store, Settings{AttributeSearch: policy}); err != nil {
		t.Fatal(err)
	}
	const admin = "admin-attribute-search-token-012345"
	const viewer = "viewer-attribute-search-token-789012"
	token, err := access.ParseToken(admin)
	if err != nil {
		t.Fatal(err)
	}
	token, err = token.WithReadTokens("viewer=" + viewer)
	if err != nil {
		t.Fatal(err)
	}
	handler := token.Protect(mux)
	request := func(method, path, body, mediaType string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", mediaType)
		key := viewer
		if path == "/v1/traces" {
			key = admin
		}
		r.Header.Set("Authorization", "Bearer "+key)
		handler.ServeHTTP(w, r)
		return w
	}
	const raw = "purchase-48230"
	const id = "0b000000000000000000000000000001"
	payload := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"orders"}}]},
"scopeSpans":[{"spans":[{"traceId":"` + id + `","spanId":"0b00000000000001","name":"POST /buy","kind":2,
"startTimeUnixNano":"1000","endTimeUnixNano":"2000","attributes":[{"key":"order.id","value":{"stringValue":"` + raw + `"}}]}]}]}]}`
	if response := request(http.MethodPost, "/v1/traces", payload, "application/json"); response.Code != http.StatusOK {
		t.Fatalf("ingest: %d %s", response.Code, response.Body)
	}
	for _, field := range []string{"order.id", "tenant.id"} {
		form := url.Values{"attribute": {field}, "value": {raw}}
		response := request(http.MethodPost, "/traces/attribute-search", form.Encode(), "application/x-www-form-urlencoded")
		location := response.Header().Get("Location")
		if response.Code != http.StatusSeeOther || strings.Contains(location, raw) {
			t.Fatalf("redirect: %d %s", response.Code, location)
		}
		results := request(http.MethodGet, location, "", "")
		if results.Code != http.StatusOK || strings.Contains(results.Body.String(), "/traces/"+id) != (field == "order.id") {
			t.Fatalf("results for %s: %d %s", field, results.Code, results.Body)
		}
	}
	detail := request(http.MethodGet, "/traces/"+id, "", "")
	export := request(http.MethodGet, "/traces/"+id+"/otlp.json", "", "")
	if detail.Code != http.StatusOK || export.Code != http.StatusOK || strings.Contains(detail.Body.String(), raw) ||
		strings.Contains(export.Body.String(), raw) || strings.Contains(export.Body.String(), privacy.AttributeDigestPrefix) {
		t.Fatal("protected value leaked from trace details or export")
	}
}
