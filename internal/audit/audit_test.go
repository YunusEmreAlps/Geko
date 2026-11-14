package audit

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
)

func TestRecord(t *testing.T) {
	var out bytes.Buffer
	log := New(&out)
	log.now = func() time.Time { return time.Date(2026, 11, 14, 10, 0, 0, 0, time.FixedZone("x", 3600)) }
	r := httptest.NewRequest("POST", "/groups/state", nil)
	r.RemoteAddr = "10.0.0.2:4000"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	ctx := access.WithCaller(r.Context(), access.Principal{Name: "qa", Role: access.RoleTriager, Via: "session"})
	log.Record(ctx, r, Event{Action: "group.state", Target: "g1", Detail: map[string]string{"state": "resolved"}})
	log.Record(r.Context(), nil, Event{Action: "sign_in", Actor: "unknown", Result: "denied"})
	var nilLog *Log
	nilLog.Record(ctx, r, Event{Action: "ignored"})

	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("lines = %q", out.String())
	}
	want := `{"time":"2026-11-14T09:00:00Z","action":"group.state","actor":"qa","role":"triager","via":"session","remote":"10.0.0.2 (forwarded for 198.51.100.7)","target":"g1","detail":{"state":"resolved"},"result":"ok"}`
	if string(lines[0]) != want {
		t.Fatalf("line = %s\nwant %s", lines[0], want)
	}
	var second Event
	if err := json.Unmarshal(lines[1], &second); err != nil || second.Actor != "unknown" || second.Result != "denied" || second.Role != "" {
		t.Fatalf("second = %+v, %v", second, err)
	}
}

func TestRecordWithoutCallerIsLocal(t *testing.T) {
	var out bytes.Buffer
	New(&out).Record(t.Context(), nil, Event{Action: "applications.change"})
	var event Event
	if err := json.Unmarshal(out.Bytes(), &event); err != nil || event.Actor != "local" || event.Role != "admin" {
		t.Fatalf("event = %+v, %v", event, err)
	}
}

func TestOpenAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	for range 2 {
		log, closeLog, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		log.Record(t.Context(), nil, Event{Action: "sign_out"})
		if err := closeLog(); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.Count(data, []byte("\n")) != 2 {
		t.Fatalf("file = %q, %v", data, err)
	}
}
