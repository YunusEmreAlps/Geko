package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestPinTrace(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "pin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{}); err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC().Add(-time.Hour)
	for _, id := range []byte{1, 2} {
		if err := store.WriteSpans(t.Context(), observed, []telemetry.Span{{
			TraceID: telemetry.TraceID{id}, ID: telemetry.SpanID{1}, Kind: telemetry.KindServer,
			ServiceName: "checkout", Name: "POST /pay", StatusCode: telemetry.StatusError,
			StartTime: observed, EndTime: observed.Add(time.Millisecond),
		}}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(path string, status int) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil))
		if w.Code != status {
			t.Fatalf("%s returned %d: %s", path, w.Code, w.Body)
		}
		return w.Body.String()
	}
	post := func(path, pinned string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+path, strings.NewReader(url.Values{"pinned": {pinned}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	trace := "/traces/01" + strings.Repeat("0", 30)
	missing := "/traces/09" + strings.Repeat("0", 30)

	if w := post(trace+"/pin", "yes"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid pin value = %d", w.Code)
	}
	if w := post(missing+"/pin", "true"); w.Code != http.StatusNotFound {
		t.Fatalf("pin missing trace = %d", w.Code)
	}
	if w := post(trace+"/pin", "true"); w.Code != http.StatusSeeOther || w.Header().Get("Location") != trace {
		t.Fatalf("pin = %d, Location %q", w.Code, w.Header().Get("Location"))
	}
	if page := get(trace, http.StatusOK); !strings.Contains(page, ">Unpin</button>") || !strings.Contains(page, "<dt>Pinned</dt>") {
		t.Fatal("pinned trace page lacks the unpin action or pin fact")
	}
	list := get("/traces?pinned=true", http.StatusOK)
	if strings.Count(list, ">Pinned</span>") != 1 || !strings.Contains(list, `name="pinned" type="checkbox" value="true" checked`) {
		t.Fatalf("pinned filter lists other traces or loses its state: %s", list)
	}
	get("/traces?pinned=maybe", http.StatusBadRequest)
	if settings := get("/settings", http.StatusOK); !strings.Contains(settings, "<dt>Pinned traces</dt><dd>1</dd>") {
		t.Fatal("settings do not count pinned traces")
	}

	if w := post(trace+"/pin", "false"); w.Code != http.StatusSeeOther {
		t.Fatalf("unpin = %d", w.Code)
	}
	if page := get(trace, http.StatusOK); !strings.Contains(page, ">Pin trace</button>") {
		t.Fatal("unpinned trace lacks the pin action")
	}
	if list := get("/traces?pinned=true", http.StatusOK); !strings.Contains(list, "No matching traces") {
		t.Fatal("unpinned trace still listed as pinned")
	}
}
