-- Triage state for error groups. A resolved group reopens when a matching trace
-- is received after changed_ns; with a version, only traces whose evidence
-- service reports that service.version count. Rows expire with trace retention.
CREATE TABLE group_states (
    group_key TEXT PRIMARY KEY,
    state TEXT NOT NULL CHECK (state IN ('resolved', 'muted')),
    changed_ns INTEGER NOT NULL,
    version TEXT NOT NULL DEFAULT '',
    notified_ns INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;
