-- Automation (docs/data-model.md). Keys are UUIDv7 made by the console unless the
-- model names a natural key; every key is named for its table.

CREATE TABLE script_groups (
    script_group_id uuid PRIMARY KEY,
    -- Its place in the list.
    position integer NOT NULL DEFAULT 0,
    name text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT ''
);

COMMENT ON TABLE script_groups IS 'Named groups of scripts';

CREATE TABLE scripts (
    script_id uuid PRIMARY KEY,
    script_group_id uuid NOT NULL,
    name text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz
);
COMMENT ON TABLE scripts IS 'Script identity: name and group';

CREATE TABLE script_versions (
    script_version_id uuid PRIMARY KEY,
    script_id uuid NOT NULL,
    version_number integer NOT NULL DEFAULT 0,
    hash text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT '',
    header jsonb,
    review jsonb,
    author text NOT NULL DEFAULT '',
    approval_request_id uuid,
    created_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE script_versions IS 'Immutable, hashed versions with their state, header and review';

CREATE TABLE script_version_parameters (
    script_version_id uuid NOT NULL,
    name text NOT NULL,
    is_required boolean NOT NULL DEFAULT false,
    is_secret boolean NOT NULL DEFAULT false,
    description text NOT NULL DEFAULT '',
    PRIMARY KEY (script_version_id, name)
);
COMMENT ON TABLE script_version_parameters IS 'Parameters each version declares in its header';

CREATE TABLE run_profiles (
    -- The id the console gives a profile; the built-in ones have fixed ids that runs name.
    run_profile_id text PRIMARY KEY,
    position integer NOT NULL DEFAULT 0,
    name text NOT NULL DEFAULT '',
    -- ssh or kubernetes.
    kind text NOT NULL DEFAULT '',
    privilege text NOT NULL DEFAULT '',
    namespace text NOT NULL DEFAULT '',
    service_account text NOT NULL DEFAULT '',
    is_built_in boolean NOT NULL DEFAULT false,
    description text NOT NULL DEFAULT ''
);

COMMENT ON TABLE run_profiles IS 'Execution profiles: SSH privilege or Kubernetes identity';

CREATE TABLE run_profile_fleets (
    run_profile_id text NOT NULL,
    -- The fleet by the id it has within a cluster: a profile is not of one cluster, so it
    -- names fleets as runs do and cannot key to one fleet's row.
    fleet_key text NOT NULL,
    PRIMARY KEY (run_profile_id, fleet_key)
);

COMMENT ON TABLE run_profile_fleets IS 'Fleets a profile may run on';

CREATE TABLE variables (
    variable_id uuid PRIMARY KEY,
    scope text NOT NULL DEFAULT '',
    fleet_id uuid,
    host_id uuid,
    name text NOT NULL DEFAULT '',
    value text NOT NULL DEFAULT '',
    is_sensitive boolean NOT NULL DEFAULT false,
    updated_by text NOT NULL DEFAULT '',
    updated_at timestamptz,
    CHECK (num_nonnulls(fleet_id, host_id) = 1)
);
COMMENT ON TABLE variables IS 'Variables scripts read, each on one fleet or one host';

CREATE TABLE variable_changes (
    variable_change_id uuid PRIMARY KEY,
    scope text NOT NULL DEFAULT '',
    fleet_id uuid,
    host_id uuid,
    reason text NOT NULL DEFAULT '',
    impact jsonb,
    state text NOT NULL DEFAULT '',
    approval_request_id uuid NOT NULL,
    requested_by text NOT NULL DEFAULT '',
    requested_at timestamptz
);
COMMENT ON TABLE variable_changes IS 'A proposed change to one fleet''s or host''s variables, with its impact and approval';

CREATE TABLE variable_change_items (
    variable_change_id uuid NOT NULL,
    name text NOT NULL,
    old_value text NOT NULL DEFAULT '',
    new_value text NOT NULL DEFAULT '',
    PRIMARY KEY (variable_change_id, name)
);
COMMENT ON TABLE variable_change_items IS 'Each variable a change adds, edits or removes';

CREATE TABLE script_run_fleets (
    script_run_fleet_id uuid PRIMARY KEY,
    script_run_id uuid NOT NULL,
    fleet_id uuid,
    fleet_name text NOT NULL DEFAULT ''
);
COMMENT ON TABLE script_run_fleets IS 'The fleets a run drew values from, as they were when it started';

CREATE TABLE script_runs (
    script_run_id uuid PRIMARY KEY,
    script_version_id uuid NOT NULL,
    run_profile_id text NOT NULL,
    retry_of_script_run_id uuid,
    cluster_id text NOT NULL DEFAULT '',
    fleet_id uuid,
    fleet_name text NOT NULL DEFAULT '',
    is_dry_run boolean NOT NULL DEFAULT false,
    rollout jsonb,
    hooks jsonb,
    state text NOT NULL DEFAULT '',
    approval_request_id uuid,
    started_by text NOT NULL DEFAULT '',
    heartbeat_at timestamptz,
    started_at timestamptz,
    ended_at timestamptz
);
COMMENT ON TABLE script_runs IS 'Runs: script version, profile, target fleet, rollout, state; a production run with its own values points to their approval';

CREATE TABLE script_run_hosts (
    script_run_host_id uuid PRIMARY KEY,
    script_run_id uuid NOT NULL,
    host_id uuid,
    host_name text NOT NULL DEFAULT '',
    batch integer NOT NULL DEFAULT 0,
    state text NOT NULL DEFAULT '',
    exit_code integer,
    output_tail text NOT NULL DEFAULT '',
    reboot_minutes integer,
    is_cordoned boolean NOT NULL DEFAULT false,
    started_at timestamptz,
    ended_at timestamptz
);
COMMENT ON TABLE script_run_hosts IS 'Result per host of a run: batch, state, exit code, output tail; keeps the host name';

CREATE TABLE script_run_values (
    script_run_value_id uuid PRIMARY KEY,
    script_run_id uuid NOT NULL,
    scope text NOT NULL DEFAULT '',
    script_run_fleet_id uuid,
    script_run_host_id uuid,
    name text NOT NULL DEFAULT '',
    value text NOT NULL DEFAULT ''
);
COMMENT ON TABLE script_run_values IS 'Values a run used, at run, fleet or host level, as they were when it started';

CREATE TABLE script_run_host_fleets (
    script_run_host_id uuid NOT NULL,
    script_run_fleet_id uuid NOT NULL,
    PRIMARY KEY (script_run_host_id, script_run_fleet_id)
);
COMMENT ON TABLE script_run_host_fleets IS 'The fleets each host of a run was in when it started';

-- Keys.
ALTER TABLE scripts ADD CONSTRAINT scripts_script_group_id_fkey
    FOREIGN KEY (script_group_id) REFERENCES script_groups (script_group_id) ON DELETE RESTRICT;
CREATE INDEX scripts_script_group_id_idx ON scripts (script_group_id);
ALTER TABLE script_versions ADD CONSTRAINT script_versions_script_id_fkey
    FOREIGN KEY (script_id) REFERENCES scripts (script_id) ON DELETE CASCADE;
CREATE INDEX script_versions_script_id_idx ON script_versions (script_id);
ALTER TABLE script_versions ADD CONSTRAINT script_versions_approval_request_id_fkey
    FOREIGN KEY (approval_request_id) REFERENCES approval_requests (approval_request_id) ON DELETE SET NULL;
CREATE INDEX script_versions_approval_request_id_idx ON script_versions (approval_request_id);
ALTER TABLE script_version_parameters ADD CONSTRAINT script_version_parameters_script_version_id_fkey
    FOREIGN KEY (script_version_id) REFERENCES script_versions (script_version_id) ON DELETE CASCADE;
ALTER TABLE run_profile_fleets ADD CONSTRAINT run_profile_fleets_run_profile_id_fkey
    FOREIGN KEY (run_profile_id) REFERENCES run_profiles (run_profile_id) ON DELETE CASCADE;
ALTER TABLE variables ADD CONSTRAINT variables_fleet_id_fkey
    FOREIGN KEY (fleet_id) REFERENCES fleets (fleet_id) ON DELETE CASCADE;
CREATE INDEX variables_fleet_id_idx ON variables (fleet_id);
ALTER TABLE variables ADD CONSTRAINT variables_host_id_fkey
    FOREIGN KEY (host_id) REFERENCES hosts (host_id) ON DELETE CASCADE;
CREATE INDEX variables_host_id_idx ON variables (host_id);
ALTER TABLE variable_changes ADD CONSTRAINT variable_changes_fleet_id_fkey
    FOREIGN KEY (fleet_id) REFERENCES fleets (fleet_id) ON DELETE SET NULL;
CREATE INDEX variable_changes_fleet_id_idx ON variable_changes (fleet_id);
ALTER TABLE variable_changes ADD CONSTRAINT variable_changes_host_id_fkey
    FOREIGN KEY (host_id) REFERENCES hosts (host_id) ON DELETE SET NULL;
CREATE INDEX variable_changes_host_id_idx ON variable_changes (host_id);
ALTER TABLE variable_changes ADD CONSTRAINT variable_changes_approval_request_id_fkey
    FOREIGN KEY (approval_request_id) REFERENCES approval_requests (approval_request_id) ON DELETE RESTRICT;
CREATE INDEX variable_changes_approval_request_id_idx ON variable_changes (approval_request_id);
ALTER TABLE variable_change_items ADD CONSTRAINT variable_change_items_variable_change_id_fkey
    FOREIGN KEY (variable_change_id) REFERENCES variable_changes (variable_change_id) ON DELETE CASCADE;
ALTER TABLE script_run_fleets ADD CONSTRAINT script_run_fleets_script_run_id_fkey
    FOREIGN KEY (script_run_id) REFERENCES script_runs (script_run_id) ON DELETE CASCADE;
CREATE INDEX script_run_fleets_script_run_id_idx ON script_run_fleets (script_run_id);
ALTER TABLE script_run_fleets ADD CONSTRAINT script_run_fleets_fleet_id_fkey
    FOREIGN KEY (fleet_id) REFERENCES fleets (fleet_id) ON DELETE SET NULL;
CREATE INDEX script_run_fleets_fleet_id_idx ON script_run_fleets (fleet_id);
ALTER TABLE script_runs ADD CONSTRAINT script_runs_script_version_id_fkey
    FOREIGN KEY (script_version_id) REFERENCES script_versions (script_version_id) ON DELETE RESTRICT;
CREATE INDEX script_runs_script_version_id_idx ON script_runs (script_version_id);
ALTER TABLE script_runs ADD CONSTRAINT script_runs_run_profile_id_fkey
    FOREIGN KEY (run_profile_id) REFERENCES run_profiles (run_profile_id) ON DELETE RESTRICT;
CREATE INDEX script_runs_run_profile_id_idx ON script_runs (run_profile_id);
ALTER TABLE script_runs ADD CONSTRAINT script_runs_retry_of_script_run_id_fkey
    FOREIGN KEY (retry_of_script_run_id) REFERENCES script_runs (script_run_id) ON DELETE SET NULL;
CREATE INDEX script_runs_retry_of_script_run_id_idx ON script_runs (retry_of_script_run_id);
ALTER TABLE script_runs ADD CONSTRAINT script_runs_fleet_id_fkey
    FOREIGN KEY (fleet_id) REFERENCES fleets (fleet_id) ON DELETE SET NULL;
CREATE INDEX script_runs_fleet_id_idx ON script_runs (fleet_id);
ALTER TABLE script_runs ADD CONSTRAINT script_runs_approval_request_id_fkey
    FOREIGN KEY (approval_request_id) REFERENCES approval_requests (approval_request_id) ON DELETE SET NULL;
CREATE INDEX script_runs_approval_request_id_idx ON script_runs (approval_request_id);
ALTER TABLE script_run_hosts ADD CONSTRAINT script_run_hosts_script_run_id_fkey
    FOREIGN KEY (script_run_id) REFERENCES script_runs (script_run_id) ON DELETE CASCADE;
CREATE INDEX script_run_hosts_script_run_id_idx ON script_run_hosts (script_run_id);
ALTER TABLE script_run_hosts ADD CONSTRAINT script_run_hosts_host_id_fkey
    FOREIGN KEY (host_id) REFERENCES hosts (host_id) ON DELETE SET NULL;
CREATE INDEX script_run_hosts_host_id_idx ON script_run_hosts (host_id);
ALTER TABLE script_run_values ADD CONSTRAINT script_run_values_script_run_id_fkey
    FOREIGN KEY (script_run_id) REFERENCES script_runs (script_run_id) ON DELETE CASCADE;
CREATE INDEX script_run_values_script_run_id_idx ON script_run_values (script_run_id);
ALTER TABLE script_run_values ADD CONSTRAINT script_run_values_script_run_fleet_id_fkey
    FOREIGN KEY (script_run_fleet_id) REFERENCES script_run_fleets (script_run_fleet_id) ON DELETE SET NULL;
CREATE INDEX script_run_values_script_run_fleet_id_idx ON script_run_values (script_run_fleet_id);
ALTER TABLE script_run_values ADD CONSTRAINT script_run_values_script_run_host_id_fkey
    FOREIGN KEY (script_run_host_id) REFERENCES script_run_hosts (script_run_host_id) ON DELETE SET NULL;
CREATE INDEX script_run_values_script_run_host_id_idx ON script_run_values (script_run_host_id);
ALTER TABLE script_run_host_fleets ADD CONSTRAINT script_run_host_fleets_script_run_host_id_fkey
    FOREIGN KEY (script_run_host_id) REFERENCES script_run_hosts (script_run_host_id) ON DELETE CASCADE;
ALTER TABLE script_run_host_fleets ADD CONSTRAINT script_run_host_fleets_script_run_fleet_id_fkey
    FOREIGN KEY (script_run_fleet_id) REFERENCES script_run_fleets (script_run_fleet_id) ON DELETE CASCADE;
CREATE INDEX script_run_host_fleets_script_run_fleet_id_idx ON script_run_host_fleets (script_run_fleet_id);
