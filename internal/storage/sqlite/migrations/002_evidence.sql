ALTER TABLE spans ADD COLUMN http_status INTEGER GENERATED ALWAYS AS (
    CASE WHEN json_type(attributes, '$."http.response.status_code"') = 'integer'
        AND json_extract(attributes, '$."http.response.status_code"') BETWEEN 100 AND 599
    THEN json_extract(attributes, '$."http.response.status_code"') END
) VIRTUAL;

ALTER TABLE traces ADD COLUMN has_evidence INTEGER NOT NULL DEFAULT 0 CHECK (has_evidence IN (0, 1));

CREATE INDEX traces_recent_failures ON traces (last_observed_ns DESC, trace_id DESC)
    WHERE has_evidence = 1;

CREATE INDEX spans_server_roots ON spans (trace_id, span_id)
    WHERE kind = 2 AND parent_span_id IS NULL;

CREATE TABLE span_evidence (
    trace_id BLOB NOT NULL,
    span_id BLOB NOT NULL,
    start_ns INTEGER NOT NULL,
    rank INTEGER NOT NULL CHECK (rank BETWEEN 1 AND 3),
    exception_message TEXT,
    PRIMARY KEY (trace_id, span_id),
    FOREIGN KEY (trace_id, span_id) REFERENCES spans (trace_id, span_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE INDEX span_evidence_by_trace ON span_evidence (trace_id, rank, start_ns, span_id);

-- Share extraction between the migration backfill and future span inserts.
CREATE VIEW span_evidence_source AS
SELECT trace_id, span_id, start_ns,
    CASE WHEN exception_event IS NOT NULL THEN 1
         WHEN http_status BETWEEN 500 AND 599 THEN 2
         ELSE 3 END AS rank,
    json_extract(exception_event, '$.attributes."exception.message"') AS exception_message
FROM (
    SELECT trace_id, span_id, start_ns, http_status, status_code,
        (SELECT event.value FROM json_each(spans.events) AS event
         WHERE event.type = 'object' AND json_extract(event.value, '$.name') = 'exception'
         ORDER BY event.key LIMIT 1) AS exception_event
    FROM spans
    WHERE status_code = 2 OR http_status BETWEEN 500 AND 599 OR json_array_length(events) > 0
)
WHERE exception_event IS NOT NULL OR http_status BETWEEN 500 AND 599 OR status_code = 2;

INSERT INTO span_evidence (trace_id, span_id, start_ns, rank, exception_message)
SELECT trace_id, span_id, start_ns, rank, exception_message FROM span_evidence_source;

UPDATE traces SET has_evidence = 1
WHERE EXISTS (SELECT 1 FROM span_evidence WHERE span_evidence.trace_id = traces.trace_id);

CREATE TRIGGER spans_record_evidence AFTER INSERT ON spans BEGIN
    INSERT INTO span_evidence (trace_id, span_id, start_ns, rank, exception_message)
    SELECT trace_id, span_id, start_ns, rank, exception_message
    FROM span_evidence_source WHERE trace_id = NEW.trace_id AND span_id = NEW.span_id;
END;

CREATE TRIGGER evidence_mark_trace AFTER INSERT ON span_evidence BEGIN
    UPDATE traces SET has_evidence = 1 WHERE trace_id = NEW.trace_id AND has_evidence = 0;
END;

CREATE TRIGGER evidence_unmark_trace AFTER DELETE ON span_evidence
WHEN NOT EXISTS (SELECT 1 FROM span_evidence WHERE trace_id = OLD.trace_id) BEGIN
    UPDATE traces SET has_evidence = 0 WHERE trace_id = OLD.trace_id;
END;
