package web

import (
	"bytes"
	"encoding/hex"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"github.com/YunusEmreAlps/geko/internal/privacy"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestDownloadAndRestoreTrace(t *testing.T) {
	open := func(name string) *sqlite.Store {
		t.Helper()
		store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		return store
	}
	source, destination := open("source.db"), open("destination.db")
	at := time.Now().UTC()
	id := telemetry.TraceID{1}
	root := telemetry.Span{TraceID: id, ID: telemetry.SpanID{2}, Kind: telemetry.KindServer,
		Name: "GET /orders", ServiceName: "gateway", StartTime: at, EndTime: at.Add(time.Second),
		Attributes: telemetry.Attributes{"http.response.status_code": int64(200)}}
	message := "private customer information"
	child := telemetry.Span{TraceID: id, ID: telemetry.SpanID{3}, ParentID: root.ID,
		Name: "query", ServiceName: "database", StartTime: at.Add(time.Millisecond), EndTime: at.Add(time.Second),
		Scope: telemetry.Scope{Name: "db.scope"}, StatusCode: telemetry.StatusError,
		Events: []telemetry.Event{{Name: "exception", Time: at, Attributes: telemetry.Attributes{
			"exception.type": "Timeout", "exception.message": message,
		}}}}
	if err := source.WriteSpans(t.Context(), at, []telemetry.Span{root, child}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := Register(mux, source, Settings{Redaction: "strict"}); err != nil {
		t.Fatal(err)
	}
	path := "/traces/" + hex.EncodeToString(id[:])
	download := httptest.NewRecorder()
	mux.ServeHTTP(download, httptest.NewRequest("GET", "http://127.0.0.1"+path+"/otlp.json", nil))
	body := download.Body.String()
	if download.Code != http.StatusOK || !strings.Contains(download.Header().Get("Content-Disposition"), ".otlp.json") ||
		strings.Contains(body, message) || !strings.Contains(body, privacy.ExceptionMessageHash(message)) ||
		!strings.Contains(body, hex.EncodeToString(id[:])) {
		t.Fatalf("download = %d %s", download.Code, body)
	}
	restore := http.NewServeMux()
	receiver := otlp.NewHandlerWithRedaction(destination, true)
	if err := Register(restore, destination, Settings{RestoreTrace: receiver.RestoreTraceJSON}); err != nil {
		t.Fatal(err)
	}
	upload := func(payload, origin string) *httptest.ResponseRecorder {
		t.Helper()
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		part, err := writer.CreateFormFile("trace_file", "trace.otlp.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "http://127.0.0.1/traces/import", &buffer)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		result := httptest.NewRecorder()
		restore.ServeHTTP(result, req)
		return result
	}
	if got := upload(body, "https://example.com"); got.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", got.Code)
	}
	mixed := strings.Replace(body, hex.EncodeToString(id[:]), strings.Repeat("b", 32), 1)
	if got := upload(mixed, ""); got.Code != http.StatusBadRequest {
		t.Fatalf("mixed trace status = %d", got.Code)
	}
	if has, err := destination.HasTraces(t.Context()); err != nil || has {
		t.Fatalf("rejected upload wrote data: %v, %v", has, err)
	}
	if got := upload(body, ""); got.Code != http.StatusSeeOther || got.Header().Get("Location") != path {
		t.Fatalf("restore = %d %s", got.Code, got.Body)
	}
	// A second restore would merge the file's spans into the stored trace.
	if got := upload(body, ""); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), path) {
		t.Fatalf("repeated restore = %d %s", got.Code, got.Body)
	}
	summary, spans, err := destination.Trace(t.Context(), id, nil, 10)
	if err != nil || len(spans.Items) != 2 || summary.EntrySpan == nil || summary.EntrySpan.HTTPStatus == nil ||
		*summary.EntrySpan.HTTPStatus != 200 || summary.Evidence == nil ||
		summary.Evidence.ExceptionHash != privacy.ExceptionMessageHash(message) ||
		spans.Items[1].ParentID != hex.EncodeToString(root.ID[:]) ||
		spans.Items[1].Service != "database" || spans.Items[1].Scope.Name != "db.scope" {
		t.Fatalf("restored trace = %+v %+v, %v", summary, spans, err)
	}
}
