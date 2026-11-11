package sqlite

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"geko/internal/ingest/otlp"
	"geko/internal/investigation"
	"geko/internal/telemetry"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestStrictExceptionExportAndSearch(t *testing.T) {
	db := openTestStore(t)
	const message = "private customer reference 638a9"
	const stack = "private-stack-marker"
	const description = "private-status-marker"
	const kind = "PaymentTimeout"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(message)))
	attribute := func(key, value string) *common.KeyValue {
		return &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: value}}}
	}
	traceID := telemetry.TraceID{1}
	spanID := telemetry.SpanID{2}
	batch := &collector.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{{
		Resource: &resource.Resource{Attributes: []*common.KeyValue{attribute("service.instance.id", "payments-1")}},
		ScopeSpans: []*trace.ScopeSpans{{Spans: []*trace.Span{{
			TraceId: traceID[:], SpanId: spanID[:], Name: "authorize", StartTimeUnixNano: 100, EndTimeUnixNano: 120,
			Status: &trace.Status{Code: trace.Status_STATUS_CODE_ERROR, Message: description},
			Events: []*trace.Span_Event{{Name: "exception", TimeUnixNano: 110, Attributes: []*common.KeyValue{
				attribute("exception.type", kind), attribute("exception.message", message), attribute("exception.stacktrace", stack),
			}}},
		}}}},
	}}}
	payload, err := proto.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/x-protobuf")
	response := httptest.NewRecorder()
	otlp.NewHandlerWithRedaction(db, true).ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("export status %d", response.Code)
	}
	for _, query := range []struct {
		search string
		count  int
	}{{kind, 1}, {strings.ToUpper(hash), 1}, {hash[:12], 0}, {message, 0}} {
		page, err := db.Failures(t.Context(), investigation.FailureFilter{Search: query.search, Limit: 10})
		if err != nil || len(page.Items) != query.count {
			t.Fatalf("search %q = %d results, %v", query.search, len(page.Items), err)
		}
		if query.count > 0 {
			evidence := page.Items[0].Evidence
			if evidence == nil || evidence.ExceptionType != kind || evidence.ExceptionHash != hash || evidence.ExceptionMessage != "" {
				t.Fatalf("unexpected evidence: %+v", evidence)
			}
		}
	}
	span, err := db.Span(t.Context(), traceID, spanID)
	if err != nil || span.ResourceAttributes["service.instance.id"] != "payments-1" {
		t.Fatalf("instance ID was not retained: %+v, %v", span, err)
	}
	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(db.path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{message, stack, description} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("strict exception data reached database file %s", suffix)
			}
		}
	}
}

func TestOfficialExporter(t *testing.T) {
	db := openTestStore(t)
	server := httptest.NewServer(otlp.NewHandler(db))
	defer server.Close()

	exporter, err := otlptracehttp.New(t.Context(),
		otlptracehttp.WithEndpointURL(server.URL+"/v1/traces"),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}),
	)
	if err != nil {
		t.Fatal(err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter))
	defer func() {
		if err := provider.Shutdown(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	_, span := provider.Tracer("test-exporter").Start(t.Context(), "GET /orders")
	span.SetAttributes(attribute.Int("http.response.status_code", 502))
	span.End()
	if err := provider.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}

	var name, scope string
	var code int
	err = db.db.QueryRow(`SELECT name, scope_name, json_extract(attributes, '$."http.response.status_code"') FROM spans`).
		Scan(&name, &scope, &code)
	if err != nil || name != "GET /orders" || scope != "test-exporter" || code != 502 {
		t.Fatalf("exported span = %q, %q, %d, %v", name, scope, code, err)
	}
}

func TestOTLPPersistsOutOfOrderSpansWithoutSecrets(t *testing.T) {
	db := openTestStore(t)
	handler := otlp.NewHandler(db)
	traceID := bytes.Repeat([]byte{1}, 16)
	parentID := bytes.Repeat([]byte{2}, 8)
	childID := bytes.Repeat([]byte{3}, 8)
	const secret = "TEST_SECRET_VALUE"
	kv := func(key, value string) *common.KeyValue {
		return &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: value}}}
	}
	batch := func(service string, spans ...*trace.Span) *collector.ExportTraceServiceRequest {
		return &collector.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{{
			Resource: &resource.Resource{Attributes: []*common.KeyValue{kv("service.name", service), kv("service.version", "1.2"), kv("deployment.secret", secret)}},
			ScopeSpans: []*trace.ScopeSpans{{
				Scope: &common.InstrumentationScope{Name: "instrumentation", Version: "2", Attributes: []*common.KeyValue{kv("code.namespace", "app"), kv("api.key", secret)}},
				Spans: spans,
			}},
		}}}
	}
	child := &trace.Span{
		TraceId: traceID, SpanId: childID, ParentSpanId: parentID, Name: "charge",
		Kind: trace.Span_SPAN_KIND_SERVER, StartTimeUnixNano: 100, EndTimeUnixNano: 90,
		Status: &trace.Status{Code: trace.Status_STATUS_CODE_ERROR, Message: "Authorization: Bearer " + secret},
		Attributes: []*common.KeyValue{
			kv("http.method", "POST"), kv("http.route", "/charge"),
			{Key: "http.status_code", Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: 503}}},
			kv("http.request.header.authorization", secret), kv("http.request.header.cookie", secret),
			kv("http.response.header.set-cookie", secret), kv("http.request.body", secret),
			kv("http.response.body", secret), kv("url.full", "https://example.org/?customer="+secret),
		},
		Events: []*trace.Span_Event{{
			Name: "exception", TimeUnixNano: 95,
			Attributes: []*common.KeyValue{
				kv("exception.type", "TimeoutError"), kv("exception.message", "password=\""+secret+"\""),
				kv("exception.stacktrace", "app.charge\n\tat payments.go:19"), kv("session.cookie", secret),
			},
		}},
	}
	parent := &trace.Span{TraceId: traceID, SpanId: parentID, Name: "POST /orders", StartTimeUnixNano: 110, EndTimeUnixNano: 120}
	send := func(message *collector.ExportTraceServiceRequest) int {
		t.Helper()
		body, err := proto.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/x-protobuf")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}

	if got := send(batch("payments", child)); got != http.StatusOK {
		t.Fatalf("child export status = %d", got)
	}
	var first, last int64
	if err := db.db.QueryRow("SELECT first_observed_ns, last_observed_ns FROM traces WHERE trace_id = ?", traceID).Scan(&first, &last); err != nil || first != last || first == 0 {
		t.Fatalf("initial observation = %d, %d, %v", first, last, err)
	}
	if got := send(batch("checkout", parent)); got != http.StatusOK {
		t.Fatalf("parent export status = %d", got)
	}
	child.Name = "retransmitted charge"
	if got := send(batch("payments", child)); got != http.StatusOK {
		t.Fatalf("duplicate export status = %d", got)
	}
	var firstAfter, lastAfter int64
	if err := db.db.QueryRow("SELECT first_observed_ns, last_observed_ns FROM traces WHERE trace_id = ?", traceID).Scan(&firstAfter, &lastAfter); err != nil || firstAfter != first || lastAfter < last {
		t.Fatalf("trace observation after additional batches = %d, %d, %v", firstAfter, lastAfter, err)
	}
	var count int
	if err := db.db.QueryRow("SELECT count(*) FROM spans WHERE trace_id = ?", traceID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("persisted span count = %d, %v", count, err)
	}
	var storedParent []byte
	var service, name, statusDescription, resourceJSON, scopeJSON, attrsJSON, eventsJSON string
	var start, end, status int64
	err := db.db.QueryRow(`SELECT parent_span_id, service_name, name, start_ns, end_ns, status_code, status_description,
		resource_attributes, scope_attributes, attributes, events FROM spans WHERE trace_id = ? AND span_id = ?`, traceID, childID).
		Scan(&storedParent, &service, &name, &start, &end, &status, &statusDescription, &resourceJSON, &scopeJSON, &attrsJSON, &eventsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedParent, parentID) || service != "payments" || name != "charge" || start != 100 || end != 90 || status != int64(telemetry.StatusError) {
		t.Fatalf("unexpected stored span: parent=%x service=%q name=%q time=%d-%d status=%d", storedParent, service, name, start, end, status)
	}
	if statusDescription != "[redacted]" || resourceJSON != `{"service.name":"payments","service.version":"1.2"}` ||
		scopeJSON != `{"code.namespace":"app"}` || attrsJSON != `{"http.request.method":"POST","http.response.status_code":503,"http.route":"/charge"}` ||
		!strings.Contains(eventsJSON, `"exception.type":"TimeoutError"`) || !strings.Contains(eventsJSON, `"exception.message":"[redacted]"`) ||
		!strings.Contains(eventsJSON, "payments.go:19") {
		t.Fatalf("unexpected stored metadata: %q %s %s %s %s", statusDescription, resourceJSON, scopeJSON, attrsJSON, eventsJSON)
	}
	for _, suffix := range []string{"", "-wal"} {
		onDisk, err := os.ReadFile(db.path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(onDisk, []byte(secret)) {
			t.Fatalf("secret reached the database file %q", db.path+suffix)
		}
	}
}

func TestWriteSpansRollsBackBatch(t *testing.T) {
	db := openTestStore(t)
	first := telemetry.Span{TraceID: telemetry.TraceID{1}, ID: telemetry.SpanID{1}, StartTime: time.Unix(0, 100), EndTime: time.Unix(0, 101)}
	bad := first
	bad.TraceID = telemetry.TraceID{2}
	bad.Attributes = telemetry.Attributes{"invalid": make(chan int)}
	if err := db.WriteSpans(t.Context(), time.Now(), []telemetry.Span{first, bad}); err == nil {
		t.Fatal("expected an encoding error")
	}
	var count int
	if err := db.db.QueryRow("SELECT count(*) FROM traces").Scan(&count); err != nil || count != 0 {
		t.Fatalf("trace count after rollback = %d, %v", count, err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(t.Context(), filepath.Join(t.TempDir(), "geko.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}
