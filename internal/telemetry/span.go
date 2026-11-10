// Package telemetry defines normalized, transport-independent trace data.
package telemetry

import "time"

type TraceID [16]byte
type SpanID [8]byte

// Attributes contain only normalized values that have passed privacy filtering.
type Attributes map[string]any

type Scope struct {
	Name       string     `json:"name"`
	Version    string     `json:"version"`
	Attributes Attributes `json:"attributes"`
}

type Event struct {
	Name       string     `json:"name"`
	Time       time.Time  `json:"time"`
	Attributes Attributes `json:"attributes"`
}

type SpanKind uint8

const (
	KindUnspecified SpanKind = iota
	KindInternal
	KindServer
	KindClient
	KindProducer
	KindConsumer
)

type StatusCode uint8

const (
	StatusUnset StatusCode = iota
	StatusOK
	StatusError
)

type Span struct {
	TraceID            TraceID
	ID                 SpanID
	ParentID           SpanID // Zero for a root span.
	ServiceName        string
	Name               string
	Kind               SpanKind
	StartTime          time.Time
	EndTime            time.Time
	StatusCode         StatusCode
	StatusDescription  string
	ResourceAttributes Attributes
	Scope              Scope
	Attributes         Attributes
	Events             []Event
}
