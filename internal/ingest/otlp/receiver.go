// Package otlp receives OpenTelemetry trace exports over HTTP protobuf.
package otlp

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"geko/internal/storage"
	"geko/internal/telemetry"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

const (
	protobufContentType = "application/x-protobuf"
	maxPayloadBytes     = 4 << 20
	maxConcurrent       = 4
	writeTimeout        = 8 * time.Second
)

type spanWriter interface {
	WriteSpans(ctx context.Context, observed time.Time, spans []telemetry.Span) error
}

type Receiver struct {
	store  spanWriter
	slots  chan struct{}
	strict bool

	mu       sync.Mutex
	accepted uint64
	last     time.Time
	rejected [rejectionCount]uint64
}

const (
	unsupportedFormat = iota
	invalidPayload
	limitExceeded
	receiverBusy
	storageUnavailable
	rejectionCount
)

type Rejection struct {
	Reason string
	Count  uint64
}

// Diagnostics describes nonempty successful exports and rejected requests
// since this receiver started. It contains no data from request payloads.
type Diagnostics struct {
	AcceptedExports uint64
	LastAccepted    time.Time
	Rejections      []Rejection
}

func NewHandler(store spanWriter) *Receiver {
	return NewHandlerWithRedaction(store, false)
}

func NewHandlerWithRedaction(store spanWriter, strict bool) *Receiver {
	return &Receiver{store: store, slots: make(chan struct{}, maxConcurrent), strict: strict}
}

func (h *Receiver) Diagnostics() Diagnostics {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Diagnostics{
		AcceptedExports: h.accepted,
		LastAccepted:    h.last,
		Rejections: []Rejection{
			{"Unsupported format", h.rejected[unsupportedFormat]},
			{"Invalid payload", h.rejected[invalidPayload]},
			{"Payload or batch limit exceeded", h.rejected[limitExceeded]},
			{"Receiver busy", h.rejected[receiverBusy]},
			{"Storage unavailable", h.rejected[storageUnavailable]},
		},
	}
}

func (h *Receiver) reject(reason int) {
	h.mu.Lock()
	h.rejected[reason]++
	h.mu.Unlock()
}

func (h *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if contentType != protobufContentType {
		h.reject(unsupportedFormat)
		fail(w, http.StatusUnsupportedMediaType, codes.InvalidArgument, "expected application/x-protobuf")
		return
	}
	encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	if encoding != "" && encoding != "identity" && encoding != "gzip" {
		h.reject(unsupportedFormat)
		fail(w, http.StatusUnsupportedMediaType, codes.InvalidArgument, "expected identity or gzip content encoding")
		return
	}

	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		h.reject(receiverBusy)
		unavailable(w, "trace receiver is busy", "1")
		return
	}

	payload, err := readPayload(w, r, encoding)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.reject(limitExceeded)
			fail(w, http.StatusRequestEntityTooLarge, codes.ResourceExhausted, "trace payload exceeds 4 MiB")
		} else {
			h.reject(invalidPayload)
			fail(w, http.StatusBadRequest, codes.InvalidArgument, "cannot read trace payload")
		}
		return
	}
	var req collector.ExportTraceServiceRequest
	if err := (proto.UnmarshalOptions{DiscardUnknown: true, RecursionLimit: 32}).Unmarshal(payload, &req); err != nil {
		h.reject(invalidPayload)
		fail(w, http.StatusBadRequest, codes.InvalidArgument, "invalid OTLP trace protobuf")
		return
	}
	spans, err := normalize(&req, h.strict)
	switch {
	case errors.Is(err, errBatchLimit):
		h.reject(limitExceeded)
		fail(w, http.StatusRequestEntityTooLarge, codes.ResourceExhausted, err.Error())
		return
	case err != nil:
		h.reject(invalidPayload)
		fail(w, http.StatusBadRequest, codes.InvalidArgument, err.Error())
		return
	}

	if len(spans) != 0 {
		ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
		defer cancel()
		if err := h.store.WriteSpans(ctx, time.Now(), spans); err != nil {
			if errors.Is(err, storage.ErrCapacity) {
				unavailable(w, "trace storage capacity reached", "30")
				return
			}
			h.reject(storageUnavailable)
			unavailable(w, "trace storage unavailable", "1")
			return
		}
		h.mu.Lock()
		h.accepted++
		h.last = time.Now()
		h.mu.Unlock()
	}
	respond(w, http.StatusOK, &collector.ExportTraceServiceResponse{})
}

func readPayload(w http.ResponseWriter, r *http.Request, encoding string) ([]byte, error) {
	if r.ContentLength > maxPayloadBytes {
		return nil, &http.MaxBytesError{Limit: maxPayloadBytes}
	}
	wire := http.MaxBytesReader(w, r.Body, maxPayloadBytes)
	defer wire.Close()
	if encoding != "gzip" {
		return io.ReadAll(wire)
	}

	decoded, err := gzip.NewReader(wire)
	if err != nil {
		return nil, err
	}
	defer decoded.Close()
	payload, err := io.ReadAll(io.LimitReader(decoded, maxPayloadBytes+1))
	if len(payload) > maxPayloadBytes {
		return nil, &http.MaxBytesError{Limit: maxPayloadBytes}
	}
	return payload, err
}

func unavailable(w http.ResponseWriter, message, retryAfter string) {
	w.Header().Set("Retry-After", retryAfter)
	fail(w, http.StatusServiceUnavailable, codes.Unavailable, message)
}

func fail(w http.ResponseWriter, httpCode int, code codes.Code, message string) {
	respond(w, httpCode, &status.Status{Code: int32(code), Message: message})
}

func respond(w http.ResponseWriter, httpCode int, msg proto.Message) {
	body, err := proto.Marshal(msg)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", protobufContentType)
	w.WriteHeader(httpCode)
	_, _ = w.Write(body)
}
