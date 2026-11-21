-- Entry spans by operation, so a comparison finds the requests of one
-- operation without reading every stored trace.
CREATE INDEX spans_entry_operations ON spans (service_name, name)
    WHERE kind = 2 AND parent_span_id IS NULL;

-- The entry span's service.version, so a version comparison reads the newest
-- traces of each version instead of every trace in its window.
ALTER TABLE traces ADD COLUMN entry_version TEXT;

UPDATE traces SET entry_version = (
    SELECT json_extract(spans.resource_attributes, '$."service.version"') FROM spans
    WHERE spans.trace_id = traces.trace_id AND spans.span_id = traces.entry_span_id
) WHERE entry_span_id IS NOT NULL;

CREATE INDEX traces_entry_versions ON traces (entry_version, last_observed_ns DESC, trace_id DESC)
    WHERE entry_version IS NOT NULL;

-- A trace has an entry span only when exactly one root server span exists,
-- and after an insert that one is the new span, so its version needs no read.
DROP TRIGGER spans_project_root_insert;
CREATE TRIGGER spans_project_root_insert AFTER INSERT ON spans
WHEN NEW.kind = 2 AND NEW.parent_span_id IS NULL BEGIN
    UPDATE traces SET (entry_span_id, entry_version) = (
        SELECT CASE WHEN count(*) = 1 THEN min(span_id) END,
               CASE WHEN count(*) = 1 THEN json_extract(NEW.resource_attributes, '$."service.version"') END
        FROM (SELECT span_id FROM spans WHERE trace_id = NEW.trace_id
              AND kind = 2 AND parent_span_id IS NULL LIMIT 2)
    ) WHERE trace_id = NEW.trace_id;
END;

DROP TRIGGER spans_project_root_delete;
CREATE TRIGGER spans_project_root_delete AFTER DELETE ON spans
WHEN OLD.kind = 2 AND OLD.parent_span_id IS NULL BEGIN
    UPDATE traces SET (entry_span_id, entry_version) = (
        SELECT CASE WHEN count(*) = 1 THEN min(span_id) END,
               CASE WHEN count(*) = 1 THEN json_extract(min(resource_attributes), '$."service.version"') END
        FROM (SELECT span_id, resource_attributes FROM spans WHERE trace_id = OLD.trace_id
              AND kind = 2 AND parent_span_id IS NULL LIMIT 2)
    ) WHERE trace_id = OLD.trace_id;
END;
