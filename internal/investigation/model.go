// Package investigation defines the query model for trace evidence.
package investigation

import (
	"errors"
	"time"

	"geko/internal/telemetry"
)

var ErrTraceNotFound = errors.New("trace not found")
var ErrSpanNotFound = errors.New("span not found")

const (
	DefaultPageSize = 50
	MaxPageSize     = 100
)

// Cursor identifies a row in timestamp and ID order. ID holds decoded bytes.
type Cursor struct {
	TimeNS int64
	ID     []byte
}

type Page[T any] struct {
	Items []T
	Next  *Cursor
}

type FailureFilter struct {
	Service    string
	Search     string
	HTTPStatus int
	From       *time.Time
	To         *time.Time
	After      *Cursor
	Limit      int
}

type SpanSummary struct {
	SpanID     string `json:"span_id"`
	Service    string `json:"service"`
	Operation  string `json:"operation"`
	HTTPStatus *int   `json:"http_status"`
	DurationNS *int64 `json:"duration_ns"`
}

type Evidence struct {
	SpanSummary
	Classification   string `json:"classification"`
	ExceptionMessage string `json:"exception_message,omitempty"`
}

type TraceSummary struct {
	TraceID       string       `json:"trace_id"`
	FirstObserved time.Time    `json:"first_observed"`
	LastObserved  time.Time    `json:"last_observed"`
	HTTPOutcome   string       `json:"http_outcome"`
	EntrySpan     *SpanSummary `json:"entry_span"`
	Evidence      *Evidence    `json:"evidence"`
	Window        *TraceWindow `json:"window,omitempty"`
}

type TraceWindow struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	SpanCount int       `json:"span_count"`
}

type Overview struct {
	TraceCount   int
	ServiceCount int
	Operations   []OperationCount
}

type OperationCount struct {
	Service   string
	Operation string
	Count     int
}

type Span struct {
	SpanSummary
	ParentID           string               `json:"parent_span_id,omitempty"`
	Kind               telemetry.SpanKind   `json:"kind"`
	Start              time.Time            `json:"start"`
	End                time.Time            `json:"end"`
	StatusCode         telemetry.StatusCode `json:"status_code"`
	StatusDescription  string               `json:"status_description,omitempty"`
	ResourceAttributes telemetry.Attributes `json:"resource_attributes"`
	Scope              telemetry.Scope      `json:"scope"`
	Attributes         telemetry.Attributes `json:"attributes"`
	Events             []telemetry.Event    `json:"events"`
}

// DurationNS is absent for reversed timestamps, including clock skew.
func DurationNS(start, end int64) *int64 {
	if end < start {
		return nil
	}
	duration := end - start
	if duration < 0 {
		return nil // The span duration exceeds int64.
	}
	return &duration
}

func HTTPOutcome(entry *SpanSummary) string {
	if entry == nil || entry.HTTPStatus == nil {
		return "unknown"
	}
	if *entry.HTTPStatus >= 500 {
		return "server_error"
	}
	return "non_5xx"
}
