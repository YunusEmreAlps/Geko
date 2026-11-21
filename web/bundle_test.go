package web

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/demo"
	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
)

func TestFailureBundleMovesAGroupBetweenInstances(t *testing.T) {
	ctx := t.Context()
	open := func(name string) *sqlite.Store {
		t.Helper()
		store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		return store
	}
	source, destination := open("source.db"), open("destination.db")
	if _, err := demo.Seed(ctx, source, time.Now()); err != nil {
		t.Fatal(err)
	}
	groups, err := source.FailureGroups(ctx, investigation.FailureFilter{Limit: 1, Search: "HttpTimeoutException"}, nil)
	if err != nil || len(groups.Items) != 1 {
		t.Fatalf("source groups = %+v, %v", groups, err)
	}
	group := groups.Items[0]
	exporter := http.NewServeMux()
	if err := Register(exporter, source, Settings{Version: "test"}); err != nil {
		t.Fatal(err)
	}
	download := httptest.NewRecorder()
	exporter.ServeHTTP(download, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/groups/bundle?"+url.Values{"group": {investigation.EncodeGroupKey(group.Key)}}.Encode(), nil))
	var bundle failureBundle
	if download.Code != http.StatusOK || json.Unmarshal(download.Body.Bytes(), &bundle) != nil ||
		bundle.Format != bundleFormat || len(bundle.Traces) != bundleTraces || bundle.Group.IssueURL == "" {
		t.Fatalf("download = %d %.300s", download.Code, download.Body.String())
	}

	importer := http.NewServeMux()
	receiver := otlp.NewHandlerWithRedaction(destination, false)
	if err := Register(importer, destination, Settings{RestoreTrace: receiver.RestoreTraceJSON}); err != nil {
		t.Fatal(err)
	}
	upload := func(body []byte) *httptest.ResponseRecorder {
		var form bytes.Buffer
		writer := multipart.NewWriter(&form)
		part, _ := writer.CreateFormFile("bundle_file", "bundle.json")
		part.Write(body)
		writer.Close()
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/groups/bundle", &form)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request.Header.Set("Origin", "http://127.0.0.1")
		response := httptest.NewRecorder()
		importer.ServeHTTP(response, request)
		return response
	}
	response := upload(download.Body.Bytes())
	if response.Code != http.StatusSeeOther || !strings.HasPrefix(response.Header().Get("Location"), "/failures?group=") {
		t.Fatalf("import = %d %s", response.Code, response.Body.String())
	}
	imported, err := destination.FailureGroups(ctx, investigation.FailureFilter{Limit: 5, GroupState: investigation.GroupAll}, nil)
	if err != nil || len(imported.Items) != 1 || imported.Items[0].Count != bundleTraces ||
		imported.Items[0].Note != group.Note || imported.Items[0].IssueURL != group.IssueURL || imported.Items[0].State != investigation.GroupOpen {
		t.Fatalf("imported groups = %+v, %v", imported, err)
	}
	// Importing again restores nothing new and keeps the note.
	if response := upload(download.Body.Bytes()); response.Code != http.StatusSeeOther {
		t.Fatalf("second import = %d %s", response.Code, response.Body.String())
	}
	if again, _ := destination.FailureGroups(ctx, investigation.FailureFilter{Limit: 5, GroupState: investigation.GroupAll}, nil); again.Items[0].Count != bundleTraces {
		t.Fatalf("second import changed the group: %+v", again.Items[0])
	}
	for _, body := range []string{`{}`, `{"format":"other","traces":[{}]}`, `not json`} {
		if response := upload([]byte(body)); response.Code != http.StatusBadRequest {
			t.Errorf("bundle %q = %d", body, response.Code)
		}
	}
}
