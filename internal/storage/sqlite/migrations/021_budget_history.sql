CREATE TABLE budget_checkpoints (
    session_id TEXT NOT NULL,
    window_ns INTEGER NOT NULL,
    saved_ns INTEGER NOT NULL,
    payload TEXT NOT NULL CHECK(length(payload) <= 262144),
    PRIMARY KEY(session_id,window_ns)
) STRICT, WITHOUT ROWID;
CREATE INDEX budget_checkpoints_window ON budget_checkpoints(window_ns,session_id);
CREATE TABLE budget_history_meta (
    id INTEGER PRIMARY KEY CHECK(id=1),
    discarded_checkpoints INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO budget_history_meta(id) VALUES(1);
