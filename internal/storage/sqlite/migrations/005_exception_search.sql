ALTER TABLE span_evidence ADD COLUMN exception_type TEXT;
ALTER TABLE span_evidence ADD COLUMN exception_hash TEXT;

DROP TRIGGER spans_record_evidence;
DROP VIEW span_evidence_source;

CREATE VIEW span_evidence_source AS
SELECT trace_id, span_id, start_ns,
    CASE WHEN exception_event IS NOT NULL THEN 1
         WHEN http_status BETWEEN 500 AND 599 THEN 2
         ELSE 3 END AS rank,
    json_extract(exception_event, '$.attributes."exception.message"') AS exception_message,
    json_extract(exception_event, '$.attributes."exception.type"') AS exception_type,
    json_extract(exception_event, '$.attributes."exception.message_hash"') AS exception_hash
FROM (
    SELECT trace_id, span_id, start_ns, http_status, status_code,
        (SELECT event.value FROM json_each(spans.events) AS event
         WHERE event.type = 'object' AND json_extract(event.value, '$.name') = 'exception'
         ORDER BY event.key LIMIT 1) AS exception_event
    FROM spans
    WHERE status_code = 2 OR http_status BETWEEN 500 AND 599 OR json_array_length(events) > 0
)
WHERE exception_event IS NOT NULL OR http_status BETWEEN 500 AND 599 OR status_code = 2;

UPDATE span_evidence AS evidence
SET exception_type = source.exception_type, exception_hash = source.exception_hash
FROM span_evidence_source AS source
WHERE evidence.rank = 1 AND source.trace_id = evidence.trace_id AND source.span_id = evidence.span_id;

CREATE TRIGGER spans_record_evidence AFTER INSERT ON spans BEGIN
    INSERT INTO span_evidence (trace_id, span_id, start_ns, rank, exception_message, exception_type, exception_hash)
    SELECT trace_id, span_id, start_ns, rank, exception_message, exception_type, exception_hash
    FROM span_evidence_source WHERE trace_id = NEW.trace_id AND span_id = NEW.span_id;
END;
