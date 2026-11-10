CREATE TABLE traces (
    trace_id BLOB NOT NULL PRIMARY KEY CHECK (length(trace_id) = 16),
    first_observed_ns INTEGER NOT NULL,
    last_observed_ns INTEGER NOT NULL CHECK (last_observed_ns >= first_observed_ns)
) STRICT, WITHOUT ROWID;

CREATE INDEX traces_last_observed ON traces (last_observed_ns);

CREATE TABLE spans (
    trace_id BLOB NOT NULL REFERENCES traces (trace_id) ON DELETE CASCADE,
    span_id BLOB NOT NULL CHECK (length(span_id) = 8),
    parent_span_id BLOB CHECK (length(parent_span_id) = 8),
    service_name TEXT NOT NULL,
    name TEXT NOT NULL,
    kind INTEGER NOT NULL,
    start_ns INTEGER NOT NULL,
    end_ns INTEGER NOT NULL,
    status_code INTEGER NOT NULL,
    status_description TEXT NOT NULL DEFAULT '',
    resource_attributes TEXT NOT NULL DEFAULT '{}',
    scope_name TEXT NOT NULL DEFAULT '',
    scope_version TEXT NOT NULL DEFAULT '',
    scope_attributes TEXT NOT NULL DEFAULT '{}',
    attributes TEXT NOT NULL DEFAULT '{}',
    events TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (trace_id, span_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX spans_trace_start ON spans (trace_id, start_ns, span_id);
