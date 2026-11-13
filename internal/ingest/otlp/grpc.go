package otlp

import (
	"context"
	"errors"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/storage"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// guardedExport retains validation errors for the method handler to translate
// into OTLP status codes. gRPC otherwise wraps codec errors as Internal.
type guardedExport struct {
	request collector.ExportTraceServiceRequest
	err     error
}

type exportCodec struct{}

func (exportCodec) Name() string                      { return "proto" }
func (exportCodec) Marshal(value any) ([]byte, error) { return proto.Marshal(value.(proto.Message)) }
func (exportCodec) Unmarshal(data []byte, value any) error {
	export := value.(*guardedExport)
	export.err = checkWireBatch(data)
	if export.err == nil {
		export.err = (proto.UnmarshalOptions{DiscardUnknown: true, RecursionLimit: maxWireDepth}).Unmarshal(data, &export.request)
	}
	return nil
}

// NewGRPCServer serves only OTLP traces. Its method adapter takes the shared
// admission slot BEFORE invoking the decoder, bounding allocations across HTTP
// and all gRPC connections. The wire codec validates before protobuf allocation.
func (h *Receiver) NewGRPCServer(token access.Token) *grpc.Server {
	server := grpc.NewServer(
		grpc.ForceServerCodec(exportCodec{}), grpc.MaxRecvMsgSize(maxPayloadBytes),
		grpc.MaxConcurrentStreams(maxConcurrent), grpc.MaxHeaderListSize(16<<10),
		grpc.ConnectionTimeout(5*time.Second),
		grpc.WaitForHandlers(true),
		grpc.KeepaliveParams(keepalive.ServerParameters{MaxConnectionIdle: time.Minute}),
	)
	descriptor := collector.TraceService_ServiceDesc
	descriptor.HandlerType = nil
	descriptor.Methods = []grpc.MethodDesc{{MethodName: "Export", Handler: func(_ any, ctx context.Context, decode func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		authority := md.Get(":authority")
		if token.Enabled() {
			headers := md.Get("authorization")
			if len(headers) != 1 || !token.Bearer(headers[0]) {
				return nil, status.Error(codes.Unauthenticated, "Bearer token required")
			}
		} else if len(authority) != 1 || !api.LocalHost(authority[0]) {
			h.reject(forbiddenHost)
			return nil, status.Error(codes.PermissionDenied, "trace receiver requires a loopback authority")
		}
		select {
		case h.slots <- struct{}{}:
			defer func() { <-h.slots }()
		default:
			h.reject(receiverBusy)
			return nil, retryStatus("trace receiver is busy", time.Second)
		}
		var export guardedExport
		if err := decode(&export); err != nil {
			if status.Code(err) == codes.ResourceExhausted {
				h.reject(limitExceeded)
			} else {
				h.reject(invalidPayload)
			}
			return nil, err
		}
		if export.err != nil {
			if errors.Is(export.err, errBatchLimit) {
				h.reject(limitExceeded)
				return nil, status.Error(codes.ResourceExhausted, "trace batch exceeds decoding limits")
			}
			h.reject(invalidPayload)
			return nil, status.Error(codes.InvalidArgument, "invalid OTLP trace payload")
		}
		response, err := h.export(ctx, &export.request)
		switch {
		case errors.Is(err, storage.ErrCapacity):
			return nil, retryStatus("trace storage capacity reached", 30*time.Second)
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, status.FromContextError(err).Err()
		case errors.Is(err, errStorage):
			return nil, retryStatus("trace storage unavailable", time.Second)
		case errors.Is(err, errBatchLimit):
			return nil, status.Error(codes.ResourceExhausted, err.Error())
		case err != nil:
			return nil, status.Error(codes.InvalidArgument, err.Error())
		default:
			return response, nil
		}
	}}}
	// A nil implementation is intentional: the transport adapter owns decoding.
	server.RegisterService(&descriptor, nil)
	return server
}

func retryStatus(message string, delay time.Duration) error {
	result := status.New(codes.Unavailable, message)
	detailed, err := result.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(delay)})
	if err != nil {
		return result.Err()
	}
	return detailed.Err()
}
