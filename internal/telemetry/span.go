// Package telemetry defines normalized, transport-independent trace data.
package telemetry

import "time"

type TraceID [16]byte
type SpanID [8]byte

// Attributes contain only normalized values that have passed privacy filtering.
type Attributes map[string]any

type Scope struct {
	Name       string
	Version    string
	Attributes Attributes
}

type Event struct {
	Name       string
	Time       time.Time
	Attributes Attributes
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
