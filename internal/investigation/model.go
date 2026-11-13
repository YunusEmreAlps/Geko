// Package investigation defines the query model for trace evidence.
package investigation

import (
	"errors"
	"time"

	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

var ErrTraceNotFound = errors.New("trace not found")
var ErrSpanNotFound = errors.New("span not found")
var ErrAssignmentInProgress = errors.New("a historical assignment is already in progress")

type ServiceApplication struct {
	Service     string
	Application string
}

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
	GroupKey        string
	Application     *string // nil means all, empty means ungrouped
	Service         string
	EvidenceService string
	Search          string
	HTTPStatus      int
	From            *time.Time
	To              *time.Time
	After           *Cursor
	Limit           int
}

// FailureGroup counts traces by their selected primary evidence, not exports
// or every failing span. Identity excludes instance IDs and free-text messages.
type FailureGroup struct {
	Key            string          `json:"-"`
	Application    string          `json:"application,omitempty"`
	Namespace      string          `json:"namespace,omitempty"`
	Service        string          `json:"evidence_service"`
	Environment    string          `json:"environment,omitempty"`
	Operation      string          `json:"evidence_operation"`
	Classification string          `json:"classification"`
	ExceptionType  string          `json:"exception_type,omitempty"`
	HTTPStatus     int             `json:"evidence_http_status,omitempty"`
	Count          int64           `json:"trace_count"`
	FirstObserved  time.Time       `json:"first_received"`
	LastObserved   time.Time       `json:"last_received"`
	TraceID        string          `json:"latest_trace_id"`
	SpanID         string          `json:"latest_evidence_span_id"`
	Outcomes       RequestOutcomes `json:"request_outcomes"`
}

type RequestOutcomes struct {
	HTTP2xx     int64 `json:"http_2xx"`
	HTTP5xx     int64 `json:"http_5xx"`
	HTTPOther   int64 `json:"http_other"`
	HTTPUnknown int64 `json:"http_unknown"`
}

type GroupCursor struct {
	TimeNS int64
	Key    string
}
type FailureGroups struct {
	Items []FailureGroup
	Next  *GroupCursor
}

type SpanSummary struct {
	Application string `json:"application,omitempty"`
	SpanID      string `json:"span_id"`
	Service     string `json:"service"`
	Operation   string `json:"operation"`
	HTTPStatus  *int   `json:"http_status"`
	DurationNS  *int64 `json:"duration_ns"`
}

type Evidence struct {
	SpanSummary
	Classification   string `json:"classification"`
	ExceptionMessage string `json:"exception_message,omitempty"`
	ExceptionType    string `json:"exception_type,omitempty"`
	ExceptionHash    string `json:"exception_hash,omitempty"`
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

const MapSpanLimit = 500

type TraceDetailFilter struct {
	After         *Cursor
	Selected      *telemetry.SpanID
	EvidenceAfter *telemetry.SpanID
}

type TraceDetails struct {
	Summary      TraceSummary
	Waterfall    Page[Span]
	After        *Cursor
	Selected     *Span
	MapSpans     []Span
	Evidence     []Evidence
	EvidenceNext string
}

type Overview struct {
	TraceCount   int
	ServiceCount int
	Operations   []OperationCount
}

type OperationCount struct {
	Application string
	Service     string
	Operation   string
	Count       int
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
