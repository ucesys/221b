-- A sensitive variable's value is never in the database (DB-71). It is kept in a
-- Kubernetes Secret, under a keyed digest of it, and the rows keep the digest: the name,
-- the scope and the mark stay where they were. This step only shapes the tables. It
-- moves no value and reads none: the console's start that follows moves each value
-- into the Secret and puts its digest in its place.

-- A variable's value is in value, or its digest is in value_digest, never both.
ALTER TABLE variables
    ADD COLUMN value_digest text,
    ADD CONSTRAINT variables_value_digest_check CHECK (value IS NULL OR value_digest IS NULL);
-- Nothing writes a value on a row marked sensitive from now on. The rows that hold one
-- today are left for the start to move, so the constraint is not checked on them.
ALTER TABLE variables
    ADD CONSTRAINT variables_is_sensitive_check CHECK (NOT is_sensitive OR value IS NULL) NOT VALID;
COMMENT ON COLUMN variables.value IS 'The value; none where a fleet only marks the name as sensitive, and none where the value is sensitive';
COMMENT ON COLUMN variables.value_digest IS 'For a sensitive value: the keyed digest the Secret keeps the value under, never the value';
COMMENT ON TABLE variables IS 'Variable values on a fleet or a host; a sensitive value is in a Secret, and its row keeps a digest';

-- What a change alters: a sensitive value before and after is its digest.
ALTER TABLE variable_change_items
    ADD COLUMN old_digest text,
    ADD COLUMN new_digest text,
    ADD CONSTRAINT variable_change_items_old_digest_check CHECK (old_value IS NULL OR old_digest IS NULL),
    ADD CONSTRAINT variable_change_items_new_digest_check CHECK (new_value IS NULL OR new_digest IS NULL);
COMMENT ON COLUMN variable_change_items.old_digest IS 'For a sensitive value: its keyed digest, never the value';
COMMENT ON COLUMN variable_change_items.new_digest IS 'For a sensitive value: its keyed digest, never the value; the value a waiting change would set is in the Secret until it is decided';
COMMENT ON TABLE variable_change_items IS 'Each thing a change alters: a value, a node''s own value, a mark, the ceiling or a reboot time, before and after; a sensitive value as its digest';

-- What a run used: a sensitive value is its digest.
ALTER TABLE script_run_values
    ADD COLUMN value_digest text,
    ADD CONSTRAINT script_run_values_value_digest_check CHECK (value_digest IS NULL OR value = '');
COMMENT ON COLUMN script_run_values.value_digest IS 'For a sensitive value: its keyed digest, never the value';
COMMENT ON TABLE script_run_values IS 'Values a run used, at run, fleet or host level, as they were when it started; a sensitive value as its digest';

-- Each host's values as the run gave them: a sensitive one as its digest, beside the empty value.
CREATE OR REPLACE VIEW script_run_host_values AS
SELECT DISTINCT ON (h.script_run_host_id, v.name)
       h.script_run_id, h.script_run_host_id, v.name, v.value, v.scope, v.value_digest
FROM script_run_hosts h
JOIN script_run_values v ON v.script_run_id = h.script_run_id
 AND (v.scope = 'run'
      OR (v.scope = 'host' AND v.script_run_host_id = h.script_run_host_id)
      OR (v.scope = 'fleet' AND v.script_run_fleet_id IN (
            SELECT f.script_run_fleet_id FROM script_run_host_fleets f
            WHERE f.script_run_host_id = h.script_run_host_id)))
ORDER BY h.script_run_host_id, v.name,
         CASE v.scope WHEN 'run' THEN 0 WHEN 'host' THEN 1 ELSE 2 END, v.script_run_value_id;
