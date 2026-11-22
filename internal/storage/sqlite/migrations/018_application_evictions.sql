-- Historical global losses cannot be attributed after their spans are gone.
ALTER TABLE storage_counters ADD COLUMN application_eviction_baseline INTEGER NOT NULL DEFAULT 0;
UPDATE storage_counters SET application_eviction_baseline=evicted_traces;

CREATE TABLE application_evictions (
 application TEXT PRIMARY KEY,
 evicted_traces INTEGER NOT NULL CHECK(evicted_traces>0)
) STRICT, WITHOUT ROWID;
