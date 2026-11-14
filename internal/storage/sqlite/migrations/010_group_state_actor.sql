-- Who last resolved or muted a group: a token name or a proxy user. Empty for
-- changes made before identities were recorded, or without a token.
ALTER TABLE group_states ADD COLUMN changed_by TEXT NOT NULL DEFAULT '';
