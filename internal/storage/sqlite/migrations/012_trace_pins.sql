-- Pinned traces outlive retention and are never evicted at capacity. A pin
-- goes with its trace, which can only expire after it is unpinned.
CREATE TABLE trace_pins (
    trace_id BLOB NOT NULL PRIMARY KEY REFERENCES traces (trace_id) ON DELETE CASCADE,
    pinned_ns INTEGER NOT NULL,
    pinned_by TEXT NOT NULL DEFAULT ''
) STRICT, WITHOUT ROWID;
