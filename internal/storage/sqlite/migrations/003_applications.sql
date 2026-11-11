ALTER TABLE spans ADD COLUMN service_namespace TEXT GENERATED ALWAYS AS (
    coalesce(nullif(trim(json_extract(resource_attributes, '$."service.namespace"')), ''), '')
) VIRTUAL;

CREATE INDEX spans_namespace ON spans(service_namespace) WHERE service_namespace <> '';

CREATE TABLE service_applications (
    service_name TEXT PRIMARY KEY,
    application TEXT NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE trace_applications (
    trace_id BLOB PRIMARY KEY REFERENCES traces(trace_id) ON DELETE CASCADE,
    application TEXT NOT NULL
) STRICT, WITHOUT ROWID;

CREATE VIEW grouped_spans AS
SELECT spans.*, coalesce(nullif(spans.service_namespace, ''), ta.application, sa.application, '') AS application
FROM spans
LEFT JOIN trace_applications ta USING (trace_id)
LEFT JOIN service_applications sa USING (service_name);
