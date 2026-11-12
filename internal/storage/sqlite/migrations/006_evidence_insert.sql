-- Materialize only the new span's first exception. SQLite otherwise inlines
-- the view expression separately for each projected field and predicate.
DROP TRIGGER spans_record_evidence;
CREATE TRIGGER spans_record_evidence AFTER INSERT ON spans BEGIN
    INSERT INTO span_evidence (trace_id, span_id, start_ns, rank, exception_message, exception_type, exception_hash)
    WITH exception AS MATERIALIZED (
        SELECT (
            SELECT event.value FROM json_each(NEW.events) AS event
            WHERE event.type = 'object' AND json_extract(event.value, '$.name') = 'exception'
            ORDER BY event.key LIMIT 1
        ) AS value
    )
    SELECT NEW.trace_id, NEW.span_id, NEW.start_ns,
        CASE WHEN value IS NOT NULL THEN 1
             WHEN NEW.http_status BETWEEN 500 AND 599 THEN 2 ELSE 3 END,
        json_extract(value, '$.attributes."exception.message"'),
        json_extract(value, '$.attributes."exception.type"'),
        json_extract(value, '$.attributes."exception.message_hash"')
    FROM exception
    WHERE value IS NOT NULL OR NEW.http_status BETWEEN 500 AND 599 OR NEW.status_code = 2;
END;
