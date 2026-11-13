ALTER TABLE traces ADD COLUMN entry_span_id BLOB;
ALTER TABLE traces ADD COLUMN evidence_span_id BLOB;

UPDATE traces SET entry_span_id = (
    SELECT CASE WHEN count(*) = 1 THEN min(span_id) END
    FROM (SELECT span_id FROM spans WHERE trace_id = traces.trace_id
          AND kind = 2 AND parent_span_id IS NULL LIMIT 2)
), evidence_span_id = (
    SELECT span_id FROM span_evidence WHERE trace_id = traces.trace_id
    ORDER BY rank, start_ns, span_id LIMIT 1
);

CREATE TRIGGER spans_project_root_insert AFTER INSERT ON spans
WHEN NEW.kind = 2 AND NEW.parent_span_id IS NULL BEGIN
    UPDATE traces SET entry_span_id = (
        SELECT CASE WHEN count(*) = 1 THEN min(span_id) END
        FROM (SELECT span_id FROM spans WHERE trace_id = NEW.trace_id
              AND kind = 2 AND parent_span_id IS NULL LIMIT 2)
    ) WHERE trace_id = NEW.trace_id;
END;

CREATE TRIGGER spans_project_root_delete AFTER DELETE ON spans
WHEN OLD.kind = 2 AND OLD.parent_span_id IS NULL BEGIN
    UPDATE traces SET entry_span_id = (
        SELECT CASE WHEN count(*) = 1 THEN min(span_id) END
        FROM (SELECT span_id FROM spans WHERE trace_id = OLD.trace_id
              AND kind = 2 AND parent_span_id IS NULL LIMIT 2)
    ) WHERE trace_id = OLD.trace_id;
END;

CREATE TRIGGER evidence_project_insert AFTER INSERT ON span_evidence BEGIN
    UPDATE traces SET evidence_span_id = (
        SELECT span_id FROM span_evidence WHERE trace_id = NEW.trace_id
        ORDER BY rank, start_ns, span_id LIMIT 1
    ) WHERE trace_id = NEW.trace_id;
END;

CREATE TRIGGER evidence_project_delete AFTER DELETE ON span_evidence
WHEN OLD.span_id = (SELECT evidence_span_id FROM traces WHERE trace_id = OLD.trace_id) BEGIN
    UPDATE traces SET evidence_span_id = (
        SELECT span_id FROM span_evidence WHERE trace_id = OLD.trace_id
        ORDER BY rank, start_ns, span_id LIMIT 1
    ) WHERE trace_id = OLD.trace_id;
END;

CREATE TABLE trace_services (
    trace_id BLOB NOT NULL REFERENCES traces(trace_id) ON DELETE CASCADE,
    namespace TEXT NOT NULL,
    service_name TEXT NOT NULL,
    span_count INTEGER NOT NULL CHECK (span_count > 0),
    PRIMARY KEY (trace_id, namespace, service_name)
) STRICT, WITHOUT ROWID;

INSERT INTO trace_services
SELECT trace_id, service_namespace, service_name, count(*) FROM spans
GROUP BY trace_id, service_namespace, service_name;

CREATE TRIGGER spans_project_service_insert AFTER INSERT ON spans BEGIN
    INSERT INTO trace_services VALUES (NEW.trace_id, NEW.service_namespace, NEW.service_name, 1)
    ON CONFLICT (trace_id, namespace, service_name)
    DO UPDATE SET span_count = span_count + 1;
END;

CREATE TRIGGER spans_project_service_delete AFTER DELETE ON spans BEGIN
    DELETE FROM trace_services WHERE trace_id = OLD.trace_id
        AND namespace = OLD.service_namespace AND service_name = OLD.service_name AND span_count = 1;
    UPDATE trace_services SET span_count = span_count - 1 WHERE trace_id = OLD.trace_id
        AND namespace = OLD.service_namespace AND service_name = OLD.service_name;
END;
