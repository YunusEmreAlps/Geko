package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
)

// A full database refuses a note with a capacity explanation, not an
// unexplained server error.
func TestGroupNoteAtTheStorageLimitExplainsCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "full.db")
	initial, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.OpenWithPolicy(t.Context(), path, info.Size()+64<<10, storage.Reject)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{}); err != nil {
		t.Fatal(err)
	}
	note := strings.Repeat("n", investigation.MaxGroupNote)
	for i := range 1000 {
		group := investigation.EncodeGroupKey(fmt.Sprintf(`["a","b","c","d","e","f","g","%d"]`, i))
		form := url.Values{"group": {group}, "note": {note}}
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/groups/note", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		switch w.Code {
		case http.StatusSeeOther:
			continue
		case http.StatusServiceUnavailable:
			if !strings.Contains(w.Body.String(), "capacity") {
				t.Fatalf("capacity response = %s", w.Body.String())
			}
			return
		default:
			t.Fatalf("note %d = %d: %s", i, w.Code, w.Body.String())
		}
	}
	t.Fatal("notes never reached the storage limit")
}
