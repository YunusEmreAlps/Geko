CREATE TABLE storage_counters (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    evicted_traces INTEGER NOT NULL DEFAULT 0,
    rejected_exports INTEGER NOT NULL DEFAULT 0
) STRICT;

INSERT INTO storage_counters (id) VALUES (1);

CREATE TABLE assignment_progress (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    from_ns INTEGER NOT NULL,
    to_ns INTEGER NOT NULL,
    service TEXT NOT NULL,
    application TEXT NOT NULL,
    last_trace_id BLOB NOT NULL,
    changed INTEGER NOT NULL
) STRICT;
