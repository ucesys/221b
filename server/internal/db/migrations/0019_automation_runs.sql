-- Automation's runs with every field a column (DB-35). The tables of step 0006 held no
-- rows until this step.

-- A run is kept after its script's version, its profile, its fleet and the run it
-- retries are gone: it names each as its document did, and keys to the row while the
-- row is there.
ALTER TABLE script_runs
    DROP CONSTRAINT script_runs_script_version_id_fkey,
    DROP CONSTRAINT script_runs_run_profile_id_fkey,
    ALTER COLUMN script_version_id DROP NOT NULL,
    ALTER COLUMN run_profile_id DROP NOT NULL,
    DROP COLUMN rollout,
    DROP COLUMN hooks,
    -- The number people know it by.
    ADD COLUMN run_number integer NOT NULL,
    ADD COLUMN script_name text NOT NULL DEFAULT '',
    ADD COLUMN version_number integer NOT NULL DEFAULT 0,
    ADD COLUMN version_hash text NOT NULL DEFAULT '',
    ADD COLUMN cluster_name text NOT NULL DEFAULT '',
    ADD COLUMN fleet_key text NOT NULL DEFAULT '',
    ADD COLUMN retry_of_number integer,
    ADD COLUMN privilege text NOT NULL DEFAULT '',
    -- Empty for nodes, or kubernetes for the cluster.
    ADD COLUMN target text NOT NULL DEFAULT '',
    -- The service account Kubernetes commands ran as; empty for the person.
    ADD COLUMN identity text NOT NULL DEFAULT '',
    -- Why this version could run: approved code, or not approved, off production.
    ADD COLUMN basis text NOT NULL DEFAULT '',
    -- Why the run stopped early or failed to start a node.
    ADD COLUMN note text NOT NULL DEFAULT '',
    -- The page's name for the start: asked twice, one run.
    ADD COLUMN request_key text NOT NULL DEFAULT '',
    ADD COLUMN is_cancel_asked boolean NOT NULL DEFAULT false,
    -- all, size or canary; the batch size; failures that stop it; minutes a node may take.
    ADD COLUMN rollout_mode text NOT NULL DEFAULT '',
    ADD COLUMN rollout_size integer NOT NULL DEFAULT 0,
    ADD COLUMN rollout_stop_after integer NOT NULL DEFAULT 0,
    ADD COLUMN rollout_timeout_minutes integer NOT NULL DEFAULT 0,
    -- What ran around each node, by name; and the scripts asked for before and after.
    ADD COLUMN hook_names text[] NOT NULL DEFAULT '{}',
    ADD COLUMN hooks_before text[] NOT NULL DEFAULT '{}',
    ADD COLUMN hooks_after text[] NOT NULL DEFAULT '{}',
    ADD COLUMN started_by_user text NOT NULL DEFAULT '',
    -- The approval the run's own values waited for on production: the request's number,
    -- who approved it and when.
    ADD COLUMN values_request_number integer,
    ADD COLUMN values_approved_by text,
    ADD COLUMN values_approved_by_user text,
    ADD COLUMN values_approved_at timestamptz;
ALTER TABLE script_runs ADD CONSTRAINT script_runs_script_version_id_fkey
    FOREIGN KEY (script_version_id) REFERENCES script_versions (script_version_id) ON DELETE SET NULL;
CREATE UNIQUE INDEX script_runs_run_number_key ON script_runs (run_number);
COMMENT ON TABLE script_runs IS 'Runs: the script version, profile and fleet each names, its rollout, state and who started it';
COMMENT ON COLUMN script_runs.run_profile_id IS 'The profile''s id as the run had it; not a key, since a run outlives its profile';

ALTER TABLE script_run_hosts
    -- The node's place in the run.
    ADD COLUMN position integer NOT NULL DEFAULT 0,
    ADD COLUMN message text NOT NULL DEFAULT '',
    ADD COLUMN error text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX script_run_hosts_script_run_id_host_name_key ON script_run_hosts (script_run_id, host_name);

-- A host's values go with its row.
ALTER TABLE script_run_values
    DROP CONSTRAINT script_run_values_script_run_host_id_fkey;
ALTER TABLE script_run_values ADD CONSTRAINT script_run_values_script_run_host_id_fkey
    FOREIGN KEY (script_run_host_id) REFERENCES script_run_hosts (script_run_host_id) ON DELETE CASCADE;
COMMENT ON COLUMN script_run_values.scope IS 'run: a value the run gave itself. host: what the host got. fleet: what a fleet of the run had';

CREATE TABLE script_run_approvers (
    script_run_id uuid NOT NULL REFERENCES script_runs (script_run_id) ON DELETE CASCADE,
    position integer NOT NULL,
    name text NOT NULL DEFAULT '',
    user_name text NOT NULL DEFAULT '',
    PRIMARY KEY (script_run_id, position)
);
COMMENT ON TABLE script_run_approvers IS 'Who had approved the script''s version when the run started';
