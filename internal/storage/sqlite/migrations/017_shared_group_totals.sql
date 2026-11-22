-- Share identities and hourly filter partitions; keep exact trace membership.
DROP INDEX failure_group_members_recent;
DROP INDEX failure_group_members_uncached;
ALTER TABLE failure_group_members RENAME TO failure_group_members_v16;

CREATE TABLE failure_group_keys (
 id INTEGER PRIMARY KEY,
 group_key TEXT NOT NULL UNIQUE,
 namespace TEXT NOT NULL,
 service_name TEXT NOT NULL
) STRICT;

CREATE TABLE failure_group_partitions (
 id INTEGER PRIMARY KEY,
 key_id INTEGER NOT NULL REFERENCES failure_group_keys(id),
 assigned TEXT NOT NULL,
 has_assignment INTEGER NOT NULL CHECK(has_assignment IN (0,1)),
 version TEXT NOT NULL,
 request_status INTEGER NOT NULL,
 request_service TEXT NOT NULL,
 request_namespace TEXT NOT NULL,
 has_root INTEGER NOT NULL CHECK(has_root IN (0,1)),
 pinned INTEGER NOT NULL CHECK(pinned IN (0,1)),
 services TEXT NOT NULL,
 bucket INTEGER NOT NULL,
 members INTEGER NOT NULL DEFAULT 0 CHECK(members >= 0),
 first_ns INTEGER NOT NULL DEFAULT 0,
 last_ns INTEGER NOT NULL DEFAULT 0,
 sample TEXT NOT NULL DEFAULT '',
 UNIQUE(key_id,assigned,has_assignment,version,request_status,request_service,request_namespace,has_root,pinned,services,bucket)
) STRICT;
CREATE INDEX failure_group_partitions_time ON failure_group_partitions(bucket);

CREATE TABLE failure_group_members (
 trace_id BLOB PRIMARY KEY REFERENCES traces(trace_id) ON DELETE CASCADE,
 span_id BLOB NOT NULL,
 partition_id INTEGER REFERENCES failure_group_partitions(id),
 first_ns INTEGER NOT NULL,
 last_ns INTEGER NOT NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX failure_group_members_recent ON failure_group_members(last_ns DESC,trace_id DESC);
CREATE INDEX failure_group_members_uncached ON failure_group_members(trace_id) WHERE partition_id IS NULL;
CREATE INDEX failure_group_members_first ON failure_group_members(partition_id,first_ns);
CREATE INDEX failure_group_members_partition_recent ON failure_group_members(partition_id,last_ns DESC,trace_id DESC);

-- Bound unusual historical topology/assignment metadata without changing its
-- semantics: a null key routes that trace through the existing exact query.
CREATE VIEW failure_group_refresh_source AS
SELECT trace_id,span_id,namespace,service_name,
 CASE WHEN length(assigned)<=256 AND length(version)<=256 AND length(services)<=4096
           AND json_array_length(services)<=128
           AND NOT EXISTS(SELECT 1 FROM json_each(services) WHERE json_type(value,'$[0]')='null')
      THEN group_key END AS group_key,
 version,first_ns,last_ns,request_status,request_service,request_namespace,
 assigned,has_assignment,pinned,services,bucket
FROM (
 SELECT s.*,coalesce(ta.application,'') AS assigned,ta.trace_id IS NOT NULL AS has_assignment,
 EXISTS(SELECT 1 FROM trace_pins pin WHERE pin.trace_id=s.trace_id) AS pinned,
 (SELECT json_group_array(CASE WHEN length(namespace)<=256 AND length(service_name)<=256
    THEN json_array(namespace,service_name) ELSE json_array(NULL,NULL) END)
  FROM (SELECT namespace,service_name FROM trace_services WHERE trace_id=s.trace_id ORDER BY namespace,service_name LIMIT 129)) AS services,
 s.last_ns/3600000000000 - (s.last_ns%3600000000000 < 0) AS bucket
 FROM failure_group_member_source s LEFT JOIN trace_applications ta ON ta.trace_id=s.trace_id
);

CREATE VIEW failure_group_refresh AS SELECT * FROM failure_group_refresh_source WHERE 0;

CREATE TRIGGER failure_group_refresh_insert INSTEAD OF INSERT ON failure_group_refresh BEGIN
 INSERT INTO failure_group_keys(group_key,namespace,service_name)
 SELECT NEW.group_key,NEW.namespace,NEW.service_name WHERE NEW.group_key IS NOT NULL
 ON CONFLICT(group_key) DO NOTHING;
 INSERT INTO failure_group_partitions(key_id,assigned,has_assignment,version,request_status,request_service,request_namespace,has_root,pinned,services,bucket)
 SELECT k.id,NEW.assigned,NEW.has_assignment,NEW.version,coalesce(NEW.request_status,0),coalesce(NEW.request_service,''),coalesce(NEW.request_namespace,''),(NEW.request_service IS NOT NULL),NEW.pinned,NEW.services,NEW.bucket FROM failure_group_keys k WHERE k.group_key=NEW.group_key
 ON CONFLICT DO NOTHING;
 INSERT INTO failure_group_members(trace_id,span_id,partition_id,first_ns,last_ns)
 VALUES(NEW.trace_id,NEW.span_id,
  (SELECT p.id FROM failure_group_partitions p JOIN failure_group_keys k ON k.id=p.key_id
   WHERE k.group_key=NEW.group_key AND p.assigned=NEW.assigned AND p.has_assignment=NEW.has_assignment AND p.version=NEW.version AND p.request_status=coalesce(NEW.request_status,0) AND p.request_service=coalesce(NEW.request_service,'') AND p.request_namespace=coalesce(NEW.request_namespace,'') AND p.has_root=(NEW.request_service IS NOT NULL) AND p.pinned=NEW.pinned AND p.services=NEW.services AND p.bucket=NEW.bucket),NEW.first_ns,NEW.last_ns)
 ON CONFLICT(trace_id) DO UPDATE SET span_id=excluded.span_id,partition_id=excluded.partition_id,
 first_ns=excluded.first_ns,last_ns=excluded.last_ns
 WHERE span_id IS NOT excluded.span_id OR partition_id IS NOT excluded.partition_id
    OR first_ns IS NOT excluded.first_ns OR last_ns IS NOT excluded.last_ns;
END;

CREATE TRIGGER failure_group_member_insert AFTER INSERT ON failure_group_members BEGIN
 UPDATE failure_group_partitions SET
 first_ns=CASE WHEN members=0 THEN NEW.first_ns ELSE min(first_ns,NEW.first_ns) END,
 last_ns=CASE WHEN members=0 THEN NEW.last_ns ELSE max(last_ns,NEW.last_ns) END,
 sample=max(sample,printf('%020d',NEW.last_ns)||hex(NEW.trace_id)||hex(NEW.span_id)),
 members=members+1 WHERE id=NEW.partition_id;
END;
CREATE TRIGGER failure_group_member_delete AFTER DELETE ON failure_group_members BEGIN
 UPDATE failure_group_partitions SET members=members-1,
 first_ns=coalesce((SELECT first_ns FROM failure_group_members WHERE partition_id=OLD.partition_id ORDER BY first_ns LIMIT 1),0),
 last_ns=coalesce((SELECT last_ns FROM failure_group_members WHERE partition_id=OLD.partition_id ORDER BY last_ns DESC,trace_id DESC LIMIT 1),0),
 sample=coalesce(CASE WHEN bucket<0 THEN
    (SELECT max(printf('%020d',last_ns)||hex(trace_id)||hex(span_id)) FROM failure_group_members WHERE partition_id=OLD.partition_id)
   ELSE (SELECT printf('%020d',last_ns)||hex(trace_id)||hex(span_id) FROM failure_group_members
         WHERE partition_id=OLD.partition_id ORDER BY last_ns DESC,trace_id DESC LIMIT 1) END,'')
 WHERE id=OLD.partition_id;
 DELETE FROM failure_group_partitions WHERE id=OLD.partition_id AND members=0;
END;
CREATE TRIGGER failure_group_member_update AFTER UPDATE ON failure_group_members BEGIN
 UPDATE failure_group_partitions SET members=members-1,
 first_ns=coalesce((SELECT first_ns FROM failure_group_members WHERE partition_id=OLD.partition_id ORDER BY first_ns LIMIT 1),0),
 last_ns=coalesce((SELECT last_ns FROM failure_group_members WHERE partition_id=OLD.partition_id ORDER BY last_ns DESC,trace_id DESC LIMIT 1),0),
 sample=coalesce(CASE WHEN bucket<0 THEN
    (SELECT max(printf('%020d',last_ns)||hex(trace_id)||hex(span_id)) FROM failure_group_members WHERE partition_id=OLD.partition_id)
   ELSE (SELECT printf('%020d',last_ns)||hex(trace_id)||hex(span_id) FROM failure_group_members
         WHERE partition_id=OLD.partition_id ORDER BY last_ns DESC,trace_id DESC LIMIT 1) END,'')
 WHERE id=OLD.partition_id;
 UPDATE failure_group_partitions SET
 first_ns=CASE WHEN members=0 THEN NEW.first_ns ELSE min(first_ns,NEW.first_ns) END,
 last_ns=CASE WHEN members=0 THEN NEW.last_ns ELSE max(last_ns,NEW.last_ns) END,
 sample=max(sample,printf('%020d',NEW.last_ns)||hex(NEW.trace_id)||hex(NEW.span_id)),
 members=members+1 WHERE id=NEW.partition_id;
 DELETE FROM failure_group_partitions WHERE id=OLD.partition_id AND members=0;
END;
CREATE TRIGGER failure_group_partition_delete AFTER DELETE ON failure_group_partitions BEGIN
 DELETE FROM failure_group_keys WHERE id=OLD.key_id
 AND NOT EXISTS(SELECT 1 FROM failure_group_partitions WHERE key_id=OLD.key_id);
END;
CREATE TRIGGER trace_applications_refresh_groups_insert AFTER INSERT ON trace_applications BEGIN
 INSERT INTO failure_group_refresh SELECT * FROM failure_group_refresh_source WHERE trace_id=NEW.trace_id;
END;
CREATE TRIGGER trace_applications_refresh_groups_update AFTER UPDATE ON trace_applications BEGIN
 INSERT INTO failure_group_refresh SELECT * FROM failure_group_refresh_source WHERE trace_id=NEW.trace_id;
END;
CREATE TRIGGER trace_applications_refresh_groups_delete AFTER DELETE ON trace_applications BEGIN
 INSERT INTO failure_group_refresh SELECT * FROM failure_group_refresh_source WHERE trace_id=OLD.trace_id;
END;
CREATE TRIGGER trace_pins_refresh_groups_insert AFTER INSERT ON trace_pins BEGIN
 INSERT INTO failure_group_refresh SELECT * FROM failure_group_refresh_source WHERE trace_id=NEW.trace_id;
END;
CREATE TRIGGER trace_pins_refresh_groups_update AFTER UPDATE ON trace_pins BEGIN
 INSERT INTO failure_group_refresh SELECT * FROM failure_group_refresh_source WHERE trace_id=NEW.trace_id;
END;
CREATE TRIGGER trace_pins_refresh_groups_delete AFTER DELETE ON trace_pins BEGIN
 INSERT INTO failure_group_refresh SELECT * FROM failure_group_refresh_source WHERE trace_id=OLD.trace_id;
END;

INSERT INTO failure_group_refresh SELECT * FROM failure_group_refresh_source;
DROP TABLE failure_group_members_v16;

CREATE VIEW failure_group_member_facts AS
SELECT m.trace_id,m.span_id,coalesce(k.namespace,'') AS namespace,
 coalesce(k.service_name,'') AS service_name,k.group_key,coalesce(p.version,'') AS version,
 m.first_ns,m.last_ns,nullif(p.request_status,0) AS request_status,
 CASE WHEN p.has_root THEN p.request_service END AS request_service,
 CASE WHEN p.has_root THEN p.request_namespace END AS request_namespace
FROM failure_group_members m LEFT JOIN failure_group_partitions p ON p.id=m.partition_id
LEFT JOIN failure_group_keys k ON k.id=p.key_id;
