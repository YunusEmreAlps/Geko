-- Compact facts for the primary evidence of each failing trace. Application
-- overrides and service mappings stay query-time decisions.
CREATE TABLE failure_group_members (
    trace_id BLOB PRIMARY KEY REFERENCES traces(trace_id) ON DELETE CASCADE,
    span_id BLOB NOT NULL,
    namespace TEXT NOT NULL,
    service_name TEXT NOT NULL,
    group_key TEXT,
    version TEXT NOT NULL,
    first_ns INTEGER NOT NULL,
    last_ns INTEGER NOT NULL,
    request_status INTEGER,
    request_service TEXT,
    request_namespace TEXT
) STRICT, WITHOUT ROWID;

CREATE INDEX failure_group_members_recent ON failure_group_members(last_ns DESC, trace_id DESC);
CREATE INDEX failure_group_members_uncached ON failure_group_members(trace_id) WHERE group_key IS NULL;

-- Use a view for the migration backfill and subsequent export refreshes alike.
CREATE VIEW failure_group_member_source AS
SELECT t.trace_id, s.span_id,
    CASE WHEN length(s.service_namespace) <= 256 THEN s.service_namespace ELSE '' END AS namespace,
    CASE WHEN length(s.service_name) <= 256 THEN s.service_name ELSE '' END AS service_name,
    CASE WHEN length(s.name) <= 1024 AND length(s.service_name) <= 256
          AND length(s.service_namespace) <= 256 AND length(s.resource_attributes) <= 4096
          AND coalesce(length(e.exception_type),0) <= 256
          AND coalesce(length(r.service_name),0) <= 256
          AND coalesce(length(r.service_namespace),0) <= 256
    THEN json_array(s.service_namespace, s.service_namespace, s.service_name,
        coalesce(nullif(json_extract(s.resource_attributes, '$."deployment.environment.name"'), ''),
                 json_extract(s.resource_attributes, '$."deployment.environment"'), ''),
        s.name, e.rank, coalesce(e.exception_type,''), coalesce(s.http_status,0)) END AS group_key,
    CASE WHEN length(s.resource_attributes) <= 4096
         THEN coalesce(json_extract(s.resource_attributes, '$."service.version"'),'') ELSE '' END AS version,
    t.first_observed_ns AS first_ns, t.last_observed_ns AS last_ns,
    r.http_status AS request_status,
    CASE WHEN length(r.service_name) <= 256 THEN r.service_name END AS request_service,
    CASE WHEN length(r.service_namespace) <= 256 THEN r.service_namespace END AS request_namespace
FROM traces t
JOIN span_evidence e ON e.trace_id = t.trace_id AND e.span_id = t.evidence_span_id
JOIN spans s ON s.trace_id = e.trace_id AND s.span_id = e.span_id
LEFT JOIN spans r ON r.trace_id = t.trace_id AND r.span_id = t.entry_span_id
WHERE t.has_evidence = 1;

INSERT INTO failure_group_members SELECT * FROM failure_group_member_source;
