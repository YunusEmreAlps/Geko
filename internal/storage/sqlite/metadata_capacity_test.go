package sqlite

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/storage"
)

// Triage metadata grows the file like an export does, so it must stop at the
// same limit, including before the first export and after reopening.
func TestGroupMetadataRespectsTheStorageLimit(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "metadata.db")
	initial, err := Open(ctx, path)
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
	limit := info.Size() + 256<<10
	note := strings.Repeat("n", investigation.MaxGroupNote)
	key := func(i int) string { return fmt.Sprintf(`["a","b","c","d","e","f","g","%d"]`, i) }
	for _, reopen := range []bool{false, true} {
		db, err := OpenWithPolicy(ctx, path, limit, storage.Reject)
		if err != nil {
			t.Fatal(err)
		}
		writes := []func(int) error{
			func(i int) error { return db.SetGroupNote(ctx, key(i), note, "", "tester", time.Now()) },
			func(i int) error {
				return db.SetGroupState(ctx, key(i), investigation.GroupResolved, strings.Repeat("v", 256), strings.Repeat("b", 256), time.Now())
			},
		}
		full := false
		for i := 0; i < 2000 && !full; i++ {
			for _, write := range writes {
				if err := write(i); errors.Is(err, storage.ErrCapacity) {
					full = true
					break
				} else if err != nil {
					t.Fatal(err)
				}
			}
		}
		if !full {
			t.Fatalf("reopen=%v: metadata never reached the limit", reopen)
		}
		if info, err := os.Stat(path); err != nil || info.Size() > limit {
			t.Fatalf("reopen=%v: main file = %v bytes above the %d-byte limit (%v)", reopen, info.Size(), limit, err)
		}
		// Removing metadata never needs more space.
		if err := db.SetGroupNote(ctx, key(0), "", "", "tester", time.Now()); err != nil {
			t.Fatalf("removing a note at the limit: %v", err)
		}
		if err := db.SetGroupState(ctx, key(0), investigation.GroupOpen, "", "tester", time.Now()); err != nil {
			t.Fatalf("reopening a group at the limit: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
