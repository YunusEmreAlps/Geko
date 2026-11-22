-- Keep capacity metadata bounded. Overflow counts application memberships,
-- not unique traces, because one trace may touch multiple untracked labels.
ALTER TABLE storage_counters ADD COLUMN application_eviction_overflow INTEGER NOT NULL DEFAULT 0;
UPDATE storage_counters SET application_eviction_overflow=coalesce((
 SELECT sum(evicted_traces) FROM application_evictions WHERE length(CAST(application AS BLOB))>512
 OR application NOT IN (SELECT application FROM application_evictions
  WHERE length(CAST(application AS BLOB))<=512 ORDER BY evicted_traces DESC,application LIMIT 512)
),0);
DELETE FROM application_evictions WHERE length(CAST(application AS BLOB))>512
 OR application NOT IN (SELECT application FROM application_evictions
  WHERE length(CAST(application AS BLOB))<=512 ORDER BY evicted_traces DESC,application LIMIT 512);
