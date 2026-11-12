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

	"github.com/YunusEmreAlps/geko/internal/api"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/telemetry"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	protobufContentType = "application/x-protobuf"
	jsonContentType     = "application/json"
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

	mu            sync.Mutex
	accepted      uint64
	rejectedSpans uint64
	last          time.Time
	rejected      [rejectionCount]uint64
}

const (
	unsupportedFormat = iota
	invalidPayload
	limitExceeded
	receiverBusy
	storageUnavailable
	forbiddenHost
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
	RejectedSpans   uint64
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
		RejectedSpans:   h.rejectedSpans,
		LastAccepted:    h.last,
		Rejections: []Rejection{
			{"Unsupported format", h.rejected[unsupportedFormat]},
			{"Invalid payload", h.rejected[invalidPayload]},
			{"Payload or batch limit exceeded", h.rejected[limitExceeded]},
			{"Receiver busy", h.rejected[receiverBusy]},
			{"Storage unavailable", h.rejected[storageUnavailable]},
			{"Non-loopback Host", h.rejected[forbiddenHost]},
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
	responseType := protobufContentType
	if contentType == jsonContentType {
		responseType = jsonContentType
	}
	w.Header().Set("Content-Type", responseType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	if !api.LocalHost(r.Host) {
		h.reject(forbiddenHost)
		fail(w, http.StatusForbidden, codes.PermissionDenied, "trace receiver requires a loopback Host")
		return
	}
	if contentType != protobufContentType && contentType != jsonContentType {
		h.reject(unsupportedFormat)
		fail(w, http.StatusUnsupportedMediaType, codes.InvalidArgument, "expected application/x-protobuf or application/json")
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
	if contentType == jsonContentType {
		err = decodeJSON(payload, &req)
	} else if err = checkWireBatch(payload); err == nil {
		err = (proto.UnmarshalOptions{DiscardUnknown: true, RecursionLimit: 32}).Unmarshal(payload, &req)
	}
	if errors.Is(err, errBatchLimit) {
		h.reject(limitExceeded)
		fail(w, http.StatusRequestEntityTooLarge, codes.ResourceExhausted, err.Error())
		return
	}
	if err != nil {
		h.reject(invalidPayload)
		fail(w, http.StatusBadRequest, codes.InvalidArgument, "invalid OTLP trace payload")
		return
	}
	spans, rejected, err := normalize(&req, h.strict)
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
		h.rejectedSpans += uint64(rejected)
		h.last = time.Now()
		h.mu.Unlock()
	}
	response := &collector.ExportTraceServiceResponse{}
	if rejected > 0 {
		response.PartialSuccess = &collector.ExportTracePartialSuccess{
			RejectedSpans: rejected,
			ErrorMessage:  "invalid span fields or per-span limits",
		}
	}
	respond(w, http.StatusOK, response)
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
	var body []byte
	var err error
	if w.Header().Get("Content-Type") == jsonContentType {
		body, err = protojson.Marshal(msg)
	} else {
		body, err = proto.Marshal(msg)
		w.Header().Set("Content-Type", protobufContentType)
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(httpCode)
	_, _ = w.Write(body)
}
