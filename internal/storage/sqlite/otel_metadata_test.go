package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestOTelMetadataRoundTrip(t *testing.T) {
	s := openTestStore(t)
	span := testSpan(1, 2, "api", 500)
	span.OTel = telemetry.Metadata{Present: true, TraceState: "vendor=opaque", Flags: 257, ResourceSchemaURL: "https://example.test/resource", ScopeSchemaURL: "https://example.test/scope", DroppedAttributesCount: 1, DroppedEventsCount: 2, DroppedLinksCount: 3, ResourceDroppedAttributesCount: 4, ScopeDroppedAttributesCount: 5, Filtering: telemetry.Filtering{Attributes: 6, TruncatedValues: 7, Links: 8, Metadata: 9}, Links: []telemetry.Link{{TraceID: "01010101010101010101010101010101", SpanID: "0202020202020202", TraceState: "other=value", Flags: 259, DroppedAttributesCount: 10, Attributes: telemetry.Attributes{"large": json.Number("9223372036854775807"), "nested": []any{json.Number("9007199254740993")}}}}}
	span.Events = []telemetry.Event{{Name: "event", Time: span.StartTime, DroppedAttributesCount: 11}}
	if err := s.WriteSpans(t.Context(), time.Now(), []telemetry.Span{span}); err != nil {
		t.Fatal(err)
	}
	check := func(got investigation.Span) {
		t.Helper()
		if !reflect.DeepEqual(got.OTel, span.OTel) {
			t.Fatalf("metadata lost: got %#v want %#v", got.OTel, span.OTel)
		}
		if len(got.Events) != 1 || got.Events[0].DroppedAttributesCount != 11 {
			t.Fatalf("event drops lost: %+v", got.Events)
		}
	}
	got, err := s.Span(t.Context(), span.TraceID, span.ID)
	if err != nil {
		t.Fatal(err)
	}
	check(got)
	_, page, err := s.Trace(t.Context(), span.TraceID, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	check(page.Items[0])
	details, err := s.TraceDetails(t.Context(), span.TraceID, investigation.TraceDetailFilter{})
	if err != nil {
		t.Fatal(err)
	}
	check(*details.Selected)
	if err := s.VisitTraceSpans(t.Context(), span.TraceID, func(got investigation.Span) error { check(got); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestSchema21UpgradeLeavesOTelMetadataUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:21] {
		if _, err := old.ExecContext(t.Context(), migration); err != nil {
			t.Fatal(err)
		}
	}
	traceID := telemetry.TraceID{1}
	spanID := telemetry.SpanID{2}
	if _, err := old.ExecContext(t.Context(), `INSERT INTO traces(trace_id,first_observed_ns,last_observed_ns) VALUES(?,1,2);`, traceID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(t.Context(), `INSERT INTO spans(trace_id,span_id,service_name,name,kind,start_ns,end_ns,status_code) VALUES(?,?,'api','legacy',2,1,2,0)`, traceID[:], spanID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(t.Context(), fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=21", applicationID)); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var raw string
	if err := s.read.QueryRowContext(t.Context(), "SELECT otel_metadata FROM spans").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "{}" {
		t.Fatalf("legacy default %q", raw)
	}
	got, err := s.Span(t.Context(), traceID, spanID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OTel.Present {
		t.Fatal("legacy metadata reported present")
	}
	span := testSpan(2, 3, "api", 200)
	if err := s.WriteSpans(t.Context(), time.Now(), []telemetry.Span{span}); err != nil {
		t.Fatal(err)
	}
	if err := s.read.QueryRowContext(t.Context(), "SELECT otel_metadata FROM spans WHERE trace_id=?", span.TraceID[:]).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "{}" {
		t.Fatalf("zero metadata encoded %q", raw)
	}
}
