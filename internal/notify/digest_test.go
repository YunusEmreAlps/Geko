package notify

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/demo"
	"github.com/YunusEmreAlps/geko/internal/ownership"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
)

func TestParseDigestAndNext(t *testing.T) {
	digest, err := ParseDigest("Monday 09:00")
	if err != nil || digest.String() != "Monday 09:00 UTC" {
		t.Fatalf("digest = %v, %v", digest, err)
	}
	wednesday := time.Date(2026, time.November, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ from, want time.Time }{
		{wednesday, time.Date(2026, time.November, 23, 9, 0, 0, 0, time.UTC)},
		{time.Date(2026, time.November, 23, 8, 59, 0, 0, time.UTC), time.Date(2026, time.November, 23, 9, 0, 0, 0, time.UTC)},
		{time.Date(2026, time.November, 23, 9, 0, 0, 0, time.UTC), time.Date(2026, time.November, 30, 9, 0, 0, 0, time.UTC)},
	} {
		if got := digest.Next(tc.from); !got.Equal(tc.want) {
			t.Errorf("Next(%v) = %v, want %v", tc.from, got, tc.want)
		}
	}
	for _, spec := range []string{"monday", "funday 09:00", "monday 9:30", "monday 25:00"} {
		if _, err := ParseDigest(spec); err == nil {
			t.Errorf("ParseDigest(%q) accepted it", spec)
		}
	}
}

func TestDigestPerWebhook(t *testing.T) {
	ctx := t.Context()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "digest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	if _, err := demo.Seed(ctx, store, now); err != nil {
		t.Fatal(err)
	}
	owners, err := ownership.Parse(demo.Owners)
	if err != nil {
		t.Fatal(err)
	}
	shared, sharedMessages := webhook(t, http.StatusOK)
	backOffice, backOfficeMessages := webhook(t, http.StatusOK)
	n := New(shared.URL, JSON, "https://geko.example.com", store, now).
		WithApplications(map[string]Webhook{"back-office": {backOffice.URL, Slack}}).WithOwners(owners)
	if err := n.SendDigest(ctx, now); err != nil {
		t.Fatal(err)
	}
	got := sharedMessages()
	if len(got) != 1 {
		t.Fatalf("shared messages = %+v", got)
	}
	var payload struct {
		Event, Text, URL string
		Digest           digestSummary
	}
	if err := json.Unmarshal([]byte(got[0].body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Event != "digest" || payload.Digest.Active != 3 || len(payload.Digest.New) != 3 || len(payload.Digest.Reopened) != 0 ||
		payload.Digest.Largest[0].Service != "payments" || !strings.Contains(payload.URL, "range=7d") {
		t.Fatalf("shared digest = %+v", payload)
	}
	if strings.Contains(payload.Text, "Largest:") {
		t.Errorf("the largest groups repeat the new ones:\n%s", payload.Text)
	}
	for _, want := range []string{"applications without their own webhook", "3 active groups, 3 new", "New: java.net.http.HttpTimeoutException in POST · 73 traces", "payments-team"} {
		if !strings.Contains(payload.Text, want) {
			t.Errorf("shared digest text missing %q:\n%s", want, payload.Text)
		}
	}
	// The muted notification group stays out; the reopened report is listed.
	got = backOfficeMessages()
	if len(got) != 1 || !strings.Contains(got[0].body, "Reopened: java.lang.NullPointerException") ||
		!strings.Contains(got[0].body, "1 active group, 0 new, 1 reopened") || strings.Contains(got[0].body, "notification") {
		t.Fatalf("back-office digest = %+v", got)
	}
}
