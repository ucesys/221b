-- What the tables alone do not say: the view of a run's values per host, the ledgers that
-- are only ever added to, and the indexes the lists by time read through.

-- Each host's actual values in a run: the run's own value first, then the host's, then
-- its fleets'.
CREATE VIEW script_run_host_values AS
SELECT DISTINCT ON (h.script_run_host_id, v.name)
       h.script_run_id, h.script_run_host_id, v.name, v.value, v.scope
FROM script_run_hosts h
JOIN script_run_values v ON v.script_run_id = h.script_run_id
 AND (v.scope = 'run'
      OR (v.scope = 'host' AND v.script_run_host_id = h.script_run_host_id)
      OR (v.scope = 'fleet' AND v.script_run_fleet_id IN (
            SELECT f.script_run_fleet_id FROM script_run_host_fleets f
            WHERE f.script_run_host_id = h.script_run_host_id)))
ORDER BY h.script_run_host_id, v.name,
         CASE v.scope WHEN 'run' THEN 0 WHEN 'host' THEN 1 ELSE 2 END, v.script_run_value_id;

-- Ledgers are added to and trimmed by retention, never changed: two replicas adding at
-- once never collide, and a row read once reads the same for good.
CREATE FUNCTION refuse_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'integrity_constraint_violation';
END $$;
CREATE TRIGGER audit_events_append_only BEFORE UPDATE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION refuse_update();
CREATE TRIGGER ai_spend_append_only BEFORE UPDATE ON ai_spend
    FOR EACH ROW EXECUTE FUNCTION refuse_update();

-- Lists by time.
CREATE INDEX audit_events_at_idx ON audit_events (at DESC);
CREATE INDEX toolbox_sessions_started_at_idx ON toolbox_sessions (started_at DESC);
CREATE INDEX ai_spend_spent_at_idx ON ai_spend (spent_at);
CREATE INDEX script_runs_started_at_idx ON script_runs (started_at DESC);
CREATE INDEX job_runs_started_at_idx ON job_runs (started_at DESC);
CREATE INDEX backup_runs_started_at_idx ON backup_runs (started_at DESC);
CREATE INDEX auth_sessions_expires_at_idx ON auth_sessions (expires_at);
CREATE INDEX approval_requests_state_idx ON approval_requests (state, expires_at);
CREATE INDEX cases_status_idx ON cases (status, created_at DESC);
CREATE INDEX changes_state_idx ON changes (state, expires_at);
