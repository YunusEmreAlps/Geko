package telemetry

// Metadata preserves bounded OTLP context and distinguishes producer-side drops
// from Geko's privacy filtering. Present is false for records predating capture.
type Metadata struct {
	Present                        bool      `json:"present,omitempty"`
	TraceState                     string    `json:"trace_state,omitempty"`
	Flags                          uint32    `json:"flags,omitempty"`
	ResourceSchemaURL              string    `json:"resource_schema_url,omitempty"`
	ScopeSchemaURL                 string    `json:"scope_schema_url,omitempty"`
	DroppedAttributesCount         uint32    `json:"dropped_attributes_count,omitempty"`
	DroppedEventsCount             uint32    `json:"dropped_events_count,omitempty"`
	DroppedLinksCount              uint32    `json:"dropped_links_count,omitempty"`
	ResourceDroppedAttributesCount uint32    `json:"resource_dropped_attributes_count,omitempty"`
	ScopeDroppedAttributesCount    uint32    `json:"scope_dropped_attributes_count,omitempty"`
	Links                          []Link    `json:"links,omitempty"`
	Filtering                      Filtering `json:"filtering"`
}

type Link struct {
	TraceID                string     `json:"trace_id"`
	SpanID                 string     `json:"span_id"`
	TraceState             string     `json:"trace_state,omitempty"`
	Flags                  uint32     `json:"flags,omitempty"`
	Attributes             Attributes `json:"attributes"`
	DroppedAttributesCount uint32     `json:"dropped_attributes_count,omitempty"`
}

// Filtering counts omissions by Geko; it never contains omitted values.
type Filtering struct {
	Attributes      uint64 `json:"attributes,omitempty"`
	TruncatedValues uint64 `json:"truncated_values,omitempty"`
	Links           uint64 `json:"links,omitempty"`
	Metadata        uint64 `json:"metadata,omitempty"`
}
