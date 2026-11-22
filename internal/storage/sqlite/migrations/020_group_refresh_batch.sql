-- Materialize only the affected export before evaluating cache eligibility.
CREATE VIEW failure_group_refresh_input AS
SELECT s.*,coalesce(ta.application,'') AS assigned,ta.trace_id IS NOT NULL AS has_assignment,
 EXISTS(SELECT 1 FROM trace_pins pin WHERE pin.trace_id=s.trace_id) AS pinned,
 (SELECT json_group_array(CASE WHEN length(namespace)<=256 AND length(service_name)<=256
    THEN json_array(namespace,service_name) ELSE json_array(NULL,NULL) END)
  FROM (SELECT namespace,service_name FROM trace_services WHERE trace_id=s.trace_id ORDER BY namespace,service_name LIMIT 129)) AS services,
 s.last_ns/3600000000000 - (s.last_ns%3600000000000 < 0) AS bucket
 FROM failure_group_member_source s LEFT JOIN trace_applications ta ON ta.trace_id=s.trace_id;

-- Refresh an extremum only when its former contributor changed.
DROP TRIGGER failure_group_member_delete;
CREATE TRIGGER failure_group_member_delete AFTER DELETE ON failure_group_members BEGIN
 UPDATE failure_group_partitions SET members=members-1,
 first_ns=CASE WHEN first_ns=OLD.first_ns THEN coalesce((SELECT first_ns FROM failure_group_members WHERE partition_id=OLD.partition_id ORDER BY first_ns LIMIT 1),0) ELSE first_ns END,
 last_ns=CASE WHEN last_ns=OLD.last_ns THEN coalesce((SELECT last_ns FROM failure_group_members WHERE partition_id=OLD.partition_id ORDER BY last_ns DESC,trace_id DESC LIMIT 1),0) ELSE last_ns END,
 sample=CASE WHEN sample=printf('%020d',OLD.last_ns)||hex(OLD.trace_id)||hex(OLD.span_id) THEN coalesce(CASE WHEN bucket<0 THEN
    (SELECT max(printf('%020d',last_ns)||hex(trace_id)||hex(span_id)) FROM failure_group_members WHERE partition_id=OLD.partition_id)
   ELSE (SELECT printf('%020d',last_ns)||hex(trace_id)||hex(span_id) FROM failure_group_members
         WHERE partition_id=OLD.partition_id ORDER BY last_ns DESC,trace_id DESC LIMIT 1) END,'') ELSE sample END
 WHERE id=OLD.partition_id;
 DELETE FROM failure_group_partitions WHERE id=OLD.partition_id AND members=0;
END;

-- Refresh an extremum only when its former contributor changed.
DROP TRIGGER failure_group_member_update;
CREATE TRIGGER failure_group_member_update AFTER UPDATE ON failure_group_members BEGIN
 UPDATE failure_group_partitions SET members=members-1,
 first_ns=CASE WHEN first_ns=OLD.first_ns THEN coalesce((SELECT first_ns FROM failure_group_members WHERE partition_id=OLD.partition_id ORDER BY first_ns LIMIT 1),0) ELSE first_ns END,
 last_ns=CASE WHEN last_ns=OLD.last_ns THEN coalesce((SELECT last_ns FROM failure_group_members WHERE partition_id=OLD.partition_id ORDER BY last_ns DESC,trace_id DESC LIMIT 1),0) ELSE last_ns END,
 sample=CASE WHEN sample=printf('%020d',OLD.last_ns)||hex(OLD.trace_id)||hex(OLD.span_id) THEN coalesce(CASE WHEN bucket<0 THEN
    (SELECT max(printf('%020d',last_ns)||hex(trace_id)||hex(span_id)) FROM failure_group_members WHERE partition_id=OLD.partition_id)
   ELSE (SELECT printf('%020d',last_ns)||hex(trace_id)||hex(span_id) FROM failure_group_members
         WHERE partition_id=OLD.partition_id ORDER BY last_ns DESC,trace_id DESC LIMIT 1) END,'') ELSE sample END
 WHERE id=OLD.partition_id;
 UPDATE failure_group_partitions SET
 first_ns=CASE WHEN members=0 THEN NEW.first_ns ELSE min(first_ns,NEW.first_ns) END,
 last_ns=CASE WHEN members=0 THEN NEW.last_ns ELSE max(last_ns,NEW.last_ns) END,
 sample=max(sample,printf('%020d',NEW.last_ns)||hex(NEW.trace_id)||hex(NEW.span_id)),
 members=members+1 WHERE id=NEW.partition_id;
 DELETE FROM failure_group_partitions WHERE id=OLD.partition_id AND members=0;
END;

