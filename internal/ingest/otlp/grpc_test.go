package otlp

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/budget"
	"github.com/YunusEmreAlps/geko/internal/privacy"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/telemetry"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

func grpcClient(t *testing.T, receiver *Receiver, authority string) *grpc.ClientConn {
	t.Helper()
	return grpcClientWithToken(t, receiver, authority, access.Token{})
}

func grpcClientWithToken(t *testing.T, receiver *Receiver, authority string, token access.Token) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := receiver.NewGRPCServer(token)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///127.0.0.1:4317", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithAuthority(authority), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); server.Stop(); listener.Close(); <-done })
	return conn
}

func TestGRPCTokenWithRemoteAuthority(t *testing.T) {
	secret := strings.Repeat("0123456789abcdef", 2)
	token, err := access.ParseToken(secret)
	if err != nil {
		t.Fatal(err)
	}
	ingest := "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	token, err = token.WithIngestTokens("storefront=" + ingest)
	if err != nil {
		t.Fatal(err)
	}
	receiver := NewHandler(nil)
	client := collector.NewTraceServiceClient(grpcClientWithToken(t, receiver, "geko.example", token))
	req := &collector.ExportTraceServiceRequest{}
	if _, err := client.Export(t.Context(), req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing credentials: %v", err)
	}
	for _, value := range []string{secret, ingest} {
		ctx := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer "+value)
		if _, err := client.Export(ctx, req); err != nil {
			t.Fatalf("authenticated remote authority: %v", err)
		}
	}
}

func TestBoundIngestToken(t *testing.T) {
	token, err := access.ParseToken(strings.Repeat("0123456789abcdef", 2))
	if err != nil {
		t.Fatal(err)
	}
	const bound = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	if token, err = token.WithIngestTokens("storefront=" + bound); err == nil {
		token, err = token.WithIngestApplications("storefront=Storefront")
	}
	if err != nil {
		t.Fatal(err)
	}
	var stored []telemetry.Span
	tracker := budget.New(budget.Config{Enabled: true}, time.Now())
	receiver := NewHandler(writeFunc(func(_ context.Context, _ time.Time, spans []telemetry.Span) error {
		stored = spans
		return nil
	})).WithBudget(tracker)
	request := func(namespace string) *collector.ExportTraceServiceRequest {
		resource := &resourcepb.Resource{}
		if namespace != "" {
			resource.Attributes = []*common.KeyValue{{Key: "service.namespace", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: namespace}}}}
		}
		return &collector.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{
			{Resource: resource, ScopeSpans: []*trace.ScopeSpans{{Spans: []*trace.Span{fixtureSpan()}}}},
		}}
	}
	namespace := func() string {
		if len(stored) != 1 {
			t.Fatalf("stored %d spans", len(stored))
		}
		value, _ := stored[0].ResourceAttributes["service.namespace"].(string)
		return value
	}

	client := collector.NewTraceServiceClient(grpcClientWithToken(t, receiver, "geko.example", token))
	ctx := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer "+bound)
	for _, given := range []string{"", "Storefront"} {
		stored = nil
		if _, err := client.Export(ctx, request(given)); err != nil || namespace() != "Storefront" {
			t.Fatalf("gRPC export with namespace %q: %v", given, err)
		}
	}
	stored = nil
	if _, err := client.Export(ctx, request("Reporting")); status.Code(err) != codes.PermissionDenied || stored != nil {
		t.Fatalf("gRPC export into another application: %v, stored %d", err, len(stored))
	}

	handler := token.Protect(receiver)
	post := func(namespace string) int {
		t.Helper()
		body, err := proto.Marshal(request(namespace))
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "http://geko.example/v1/traces", bytes.NewReader(body))
		r.Header.Set("Content-Type", protobufContentType)
		r.Header.Set("Authorization", "Bearer "+bound)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	stored = nil
	if code := post(""); code != http.StatusOK || namespace() != "Storefront" {
		t.Fatalf("HTTP export = %d", code)
	}
	stored = nil
	if code := post("Reporting"); code != http.StatusForbidden || stored != nil {
		t.Fatalf("HTTP export into another application = %d, stored %d", code, len(stored))
	}
	for _, rejection := range receiver.Diagnostics().Rejections {
		if rejection.Reason == "Application not allowed for the ingest token" && rejection.Count != 2 {
			t.Fatalf("rejections = %+v", receiver.Diagnostics().Rejections)
		}
	}
	report := tracker.Snapshot(time.Now(), false)
	if report.Total.Spans != 3 || report.Total.Bytes != 3*int64(proto.Size(fixtureSpan())) ||
		len(report.Services) != 1 || report.Services[0].Namespace != "Storefront" {
		t.Fatalf("HTTP and gRPC budget attribution = %+v", report)
	}
}

func TestGRPCExportAndPartialSuccess(t *testing.T) {
	var stored []telemetry.Span
	receiver := NewHandler(writeFunc(func(_ context.Context, _ time.Time, spans []telemetry.Span) error { stored = spans; return nil }))
	client := collector.NewTraceServiceClient(grpcClient(t, receiver, "127.0.0.1:4317"))
	good, bad := fixtureSpan(), fixtureSpan()
	bad.TraceId = nil
	request := &collector.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{{ScopeSpans: []*trace.ScopeSpans{{Spans: []*trace.Span{good, bad}}}}}}
	response, err := client.Export(t.Context(), request, grpc.UseCompressor("gzip"))
	if err != nil || response.GetPartialSuccess().GetRejectedSpans() != 1 || len(stored) != 1 {
		t.Fatalf("export=%v, %v; stored=%d", response, err, len(stored))
	}
	diagnostics := receiver.Diagnostics()
	if diagnostics.AcceptedExports != 1 || diagnostics.RejectedSpans != 1 {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
}

type rawCodec struct{}

func (rawCodec) Name() string                      { return "proto" }
func (rawCodec) Marshal(value any) ([]byte, error) { return value.([]byte), nil }
func (rawCodec) Unmarshal(data []byte, value any) error {
	return proto.Unmarshal(data, value.(proto.Message))
}

func TestGRPCRejectsBeforeStorage(t *testing.T) {
	receiver := NewHandler(writeFunc(func(context.Context, time.Time, []telemetry.Span) error {
		t.Error("invalid export reached storage")
		return nil
	}))
	conn := grpcClient(t, receiver, "127.0.0.1:4317")
	for _, tc := range []struct {
		name string
		wire []byte
		code codes.Code
		gzip bool
	}{
		{"malformed", []byte{0xff}, codes.InvalidArgument, false},
		{"resource count", bytes.Repeat([]byte{10, 0}, maxSpans+1), codes.ResourceExhausted, false},
		{"nested attributes", envelope(1, envelope(1, bytes.Repeat(envelope(1, nil), maxDecodedMessages+1))), codes.ResourceExhausted, false},
		{"wire bytes", []byte(strings.Repeat("x", maxPayloadBytes+1)), codes.ResourceExhausted, false},
		{"gzip expansion", []byte(strings.Repeat("x", maxPayloadBytes+1)), codes.ResourceExhausted, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := []grpc.CallOption{grpc.ForceCodec(rawCodec{})}
			if tc.gzip {
				options = append(options, grpc.UseCompressor("gzip"))
			}
			err := conn.Invoke(t.Context(), collector.TraceService_Export_FullMethodName, tc.wire, &collector.ExportTraceServiceResponse{}, options...)
			if status.Code(err) != tc.code {
				t.Fatalf("got %v, want %v", err, tc.code)
			}
		})
	}
	blocked := collector.NewTraceServiceClient(grpcClient(t, receiver, "rebind.example"))
	if _, err := blocked.Export(t.Context(), &collector.ExportTraceServiceRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("authority=%v", err)
	}
	if receiver.Diagnostics().Rejections[forbiddenHost].Count != 1 {
		t.Fatal("missing Host rejection")
	}
}

func TestGRPCBackpressure(t *testing.T) {
	receiver := NewHandler(writeFunc(func(context.Context, time.Time, []telemetry.Span) error { return storage.ErrCapacity }))
	client := collector.NewTraceServiceClient(grpcClient(t, receiver, "127.0.0.1:4317"))
	var request collector.ExportTraceServiceRequest
	if err := proto.Unmarshal(encodeBatch(t, fixtureSpan()), &request); err != nil {
		t.Fatal(err)
	}
	_, err := client.Export(t.Context(), &request)
	check := func(err error, delay time.Duration) {
		t.Helper()
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("backpressure=%v", err)
		}
		details := status.Convert(err).Details()
		if len(details) != 1 {
			t.Fatalf("retry details=%v", details)
		}
		retry, ok := details[0].(*errdetails.RetryInfo)
		if !ok || retry.RetryDelay.AsDuration() != delay {
			t.Fatalf("retry=%v", details)
		}
	}
	check(err, 30*time.Second)
	for range maxConcurrent {
		receiver.slots <- struct{}{}
	}
	defer func() {
		for range maxConcurrent {
			<-receiver.slots
		}
	}()
	_, err = client.Export(t.Context(), &request)
	check(err, time.Second)
}

func TestGRPCStrictRedaction(t *testing.T) {
	var stored []telemetry.Span
	receiver := NewHandlerWithRedaction(writeFunc(func(_ context.Context, _ time.Time, spans []telemetry.Span) error { stored = spans; return nil }), true)
	client := collector.NewTraceServiceClient(grpcClient(t, receiver, "127.0.0.1:4317"))
	span := fixtureSpan()
	span.Events = []*trace.Span_Event{{Name: "exception", TimeUnixNano: 150, Attributes: []*common.KeyValue{
		{Key: "exception.message", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: "private customer detail"}}},
		{Key: "exception.type", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: "TimeoutError"}}},
	}}}
	var request collector.ExportTraceServiceRequest
	if err := proto.Unmarshal(encodeBatch(t, span), &request); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Export(t.Context(), &request); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || len(stored[0].Events) != 1 {
		t.Fatalf("stored=%+v", stored)
	}
	attributes := stored[0].Events[0].Attributes
	if _, ok := attributes["exception.message"]; ok {
		t.Fatal("strict mode retained message")
	}
	if attributes["exception.message_hash"] != privacy.ExceptionMessageHash("private customer detail") || attributes["exception.type"] != "TimeoutError" {
		t.Fatalf("strict evidence=%+v", attributes)
	}
}

func TestGRPCAdmissionPrecedesDecoding(t *testing.T) {
	receiver := NewHandler(nil)
	receiver.wait = 0
	conn := grpcClient(t, receiver, "127.0.0.1:4317")
	for range maxConcurrent {
		receiver.slots <- struct{}{}
	}
	defer func() {
		for range maxConcurrent {
			<-receiver.slots
		}
	}()
	err := conn.Invoke(t.Context(), collector.TraceService_Export_FullMethodName, []byte{0xff}, &collector.ExportTraceServiceResponse{}, grpc.ForceCodec(rawCodec{}))
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("request decoded before admission: %v", err)
	}
	if receiver.Diagnostics().Rejections[invalidPayload].Count != 0 {
		t.Fatal("busy receiver decoded payload")
	}
}
