package otlp

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"geko/internal/telemetry"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/proto"
)

func TestRejectInvalidExports(t *testing.T) {
	handler := NewHandler(writeFunc(func(context.Context, time.Time, []telemetry.Span) error {
		t.Error("invalid request reached storage")
		return nil
	}))
	valid := fixtureSpan()
	invalid := proto.CloneOf(valid)
	invalid.TraceId = make([]byte, 16)
	tooMany := make([]*trace.Span, maxSpans+1)
	for i := range tooMany {
		tooMany[i] = valid
	}
	big := make([]byte, maxPayloadBytes+1)
	var compressed bytes.Buffer
	zip := gzip.NewWriter(&compressed)
	if _, err := zip.Write(big); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, mediaType, encoding string
		payload                   []byte
		want                      int
	}{
		{"JSON", "application/json", "", []byte("{}"), 415},
		{"unsupported encoding", protobufContentType, "deflate", nil, 415},
		{"malformed protobuf", protobufContentType, "", []byte{0xff}, 400},
		{"malformed gzip", protobufContentType, "gzip", []byte("gzip"), 400},
		{"invalid span in mixed batch", protobufContentType, "", encodeBatch(t, valid, invalid), 400},
		{"wire size", protobufContentType, "", big, 413},
		{"expanded size", protobufContentType, "gzip", compressed.Bytes(), 413},
		{"span limit", protobufContentType, "", encodeBatch(t, tooMany...), 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(tc.payload))
			r.ContentLength = -1 // Exercise limits even without a Content-Length header.
			r.Header.Set("Content-Type", tc.mediaType)
			r.Header.Set("Content-Encoding", tc.encoding)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			var response status.Status
			err := proto.Unmarshal(w.Body.Bytes(), &response)
			if w.Code != tc.want || w.Header().Get("Content-Type") != protobufContentType || err != nil || response.Message == "" {
				t.Fatalf("export response = %d %q %v, want status %d with protobuf error", w.Code, w.Header().Get("Content-Type"), err, tc.want)
			}
		})
	}
	diagnostics := handler.Diagnostics()
	if diagnostics.AcceptedExports != 0 || !diagnostics.LastAccepted.IsZero() ||
		diagnostics.Rejections[unsupportedFormat].Count != 2 ||
		diagnostics.Rejections[invalidPayload].Count != 3 ||
		diagnostics.Rejections[limitExceeded].Count != 3 {
		t.Fatalf("incorrect diagnostics for rejected exports: %+v", diagnostics)
	}
}

func TestFailedWriteIsNotAnAcceptedExport(t *testing.T) {
	handler := NewHandler(writeFunc(func(context.Context, time.Time, []telemetry.Span) error {
		return errors.New("storage fixture error")
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(encodeBatch(t, fixtureSpan())))
	request.Header.Set("Content-Type", protobufContentType)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	diagnostics := handler.Diagnostics()
	if response.Code != http.StatusServiceUnavailable || diagnostics.AcceptedExports != 0 ||
		!diagnostics.LastAccepted.IsZero() || diagnostics.Rejections[storageUnavailable].Count != 1 {
		t.Fatalf("write failure response = %d, diagnostics = %+v", response.Code, diagnostics)
	}
}

func TestBackpressure(t *testing.T) {
	started := make(chan struct{}, maxConcurrent)
	release := make(chan struct{})
	var calls atomic.Int32
	handler := NewHandler(writeFunc(func(ctx context.Context, _ time.Time, _ []telemetry.Span) error {
		if calls.Add(1) > maxConcurrent {
			return nil
		}
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	payload := encodeBatch(t, fixtureSpan())
	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/traces", bytes.NewReader(payload))
		req.Header.Set("Content-Type", protobufContentType)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	results := make(chan int, maxConcurrent)
	for range maxConcurrent {
		go func() { results <- send().Code }()
	}
	defer func() {
		close(release)
		for range maxConcurrent {
			if code := <-results; code != http.StatusOK {
				t.Errorf("admitted export status = %d, want 200", code)
			}
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for range maxConcurrent {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("concurrent requests did not reach storage")
		}
	}
	out := send()
	if out.Code != http.StatusServiceUnavailable || out.Header().Get("Retry-After") == "" {
		t.Fatalf("overload response = %d %v", out.Code, out.Header())
	}
}

func fixtureSpan() *trace.Span {
	return &trace.Span{
		TraceId: bytes.Repeat([]byte{1}, 16), SpanId: bytes.Repeat([]byte{2}, 8),
		Name: "GET /orders", StartTimeUnixNano: 100, EndTimeUnixNano: 200,
	}
}

func encodeBatch(t *testing.T, spans ...*trace.Span) []byte {
	t.Helper()
	body, err := proto.Marshal(&collector.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{{
		ScopeSpans: []*trace.ScopeSpans{{Spans: spans}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

type writeFunc func(context.Context, time.Time, []telemetry.Span) error

func (fn writeFunc) WriteSpans(ctx context.Context, observed time.Time, spans []telemetry.Span) error {
	return fn(ctx, observed, spans)
}
