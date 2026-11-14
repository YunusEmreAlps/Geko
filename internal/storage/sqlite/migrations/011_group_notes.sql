-- A team note and an issue tracker link for an error group, with who last
-- changed them. Rows expire with trace retention, like group_states.
CREATE TABLE group_notes (
    group_key TEXT PRIMARY KEY,
    note TEXT NOT NULL DEFAULT '',
    issue_url TEXT NOT NULL DEFAULT '',
    changed_ns INTEGER NOT NULL,
    changed_by TEXT NOT NULL DEFAULT ''
) STRICT, WITHOUT ROWID;
