package web

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

func TestTraceOTelMetadataInspector(t *testing.T) {
	path := filepath.Join(t.TempDir(), "otel.db")
	store, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	span := telemetry.Span{TraceID: telemetry.TraceID{1}, ID: telemetry.SpanID{2}, ServiceName: "api", Name: "operation", StartTime: now, EndTime: now.Add(time.Millisecond)}
	span.OTel = telemetry.Metadata{Present: true, TraceState: "vendor=opaque", Flags: 257, ResourceSchemaURL: "https://schema.test/resource", ScopeSchemaURL: "https://schema.test/scope", DroppedAttributesCount: 11, DroppedEventsCount: 12, DroppedLinksCount: 13, ResourceDroppedAttributesCount: 14, ScopeDroppedAttributesCount: 15, Filtering: telemetry.Filtering{Attributes: 21, TruncatedValues: 22, Links: 23, Metadata: 24}, Links: []telemetry.Link{{TraceID: strings.Repeat("a", 32), SpanID: strings.Repeat("b", 16), TraceState: "other=state", Flags: 259, DroppedAttributesCount: 16, Attributes: telemetry.Attributes{"link.attribute": "<script>alert(1)</script>"}}}}
	span.Events = []telemetry.Event{{Name: "event", Time: now, DroppedAttributesCount: 17}}
	legacy := span
	legacy.TraceID = telemetry.TraceID{3}
	legacy.OTel = telemetry.Metadata{}
	legacy.Events = nil
	if err := store.WriteSpans(t.Context(), now, []telemetry.Span{span, legacy}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := Register(mux, store, Settings{DBPath: path}); err != nil {
		t.Fatal(err)
	}
	get := func(id telemetry.TraceID) string {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/traces/"+hex.EncodeToString(id[:]), nil))
		if w.Code != 200 {
			t.Fatalf("response %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	body := get(span.TraceID)
	for _, fragment := range []string{"Span links (1)", `href="/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?span=bbbbbbbbbbbbbbbb#span-details"`, "may be unavailable", "vendor=opaque", "https://schema.test/resource", "https://schema.test/scope", "SDK-reported drops", "Geko filtering", "Truncated values", "link.attribute", "&lt;script&gt;alert(1)&lt;/script&gt;", "Event attributes dropped by SDK: 17", "other=state"} {
		if !strings.Contains(body, fragment) {
			t.Errorf("missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"<dt>Flags</dt><dd>257</dd>", "<dt>Flags</dt><dd>259</dd>",
		"<dt>Link attributes dropped by SDK</dt><dd>16</dd>",
		"<dt>Span attributes</dt><dd>11</dd>", "<dt>Events</dt><dd>12</dd>",
		"<dt>Links</dt><dd>13</dd>", "<dt>Resource attributes</dt><dd>14</dd>",
		"<dt>Scope attributes</dt><dd>15</dd>", "<dt>Filtered attributes</dt><dd>21</dd>",
		"<dt>Truncated values</dt><dd>22</dd>", "<dt>Filtered links</dt><dd>23</dd>",
		"<dt>Filtered metadata fields</dt><dd>24</dd>",
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("incorrect metadata value: missing %q", fragment)
		}
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("unsafe link attributes")
	}
	if strings.Contains(body, "OTel metadata unavailable") {
		t.Fatal("captured metadata shown unavailable")
	}
	if body := get(legacy.TraceID); !strings.Contains(body, "OTel metadata unavailable") || strings.Contains(body, "SDK-reported drops") {
		t.Fatal("legacy metadata claims known loss counts")
	}
}
