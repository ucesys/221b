-- Platform (docs/data-model.md). Keys are UUIDv7 made by the console unless the
-- model names a natural key; every key is named for its table.

CREATE TABLE clusters (
    cluster_id text PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    environment text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    server_url text NOT NULL DEFAULT '',
    auth_method text NOT NULL DEFAULT '',
    mode text NOT NULL DEFAULT '',
    is_local boolean NOT NULL DEFAULT false,
    secret_ref text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz
);
COMMENT ON TABLE clusters IS 'Connection metadata per cluster, keyed by the console''s cluster id; credentials stay in Secrets';

CREATE TABLE alert_acks (
    alert_ack_id uuid PRIMARY KEY,
    cluster_id text NOT NULL,
    alert_key text NOT NULL DEFAULT '',
    fingerprint text NOT NULL DEFAULT '',
    is_expected boolean NOT NULL DEFAULT false,
    reason text NOT NULL DEFAULT '',
    job_run_id uuid,
    case_id uuid,
    exception_id uuid,
    acked_by text NOT NULL DEFAULT '',
    acked_at timestamptz
);
COMMENT ON TABLE alert_acks IS 'Acknowledged alerts per cluster: who, when, why, and the run, case or exception that acknowledged them';

CREATE TABLE backup_runs (
    backup_run_id uuid PRIMARY KEY,
    slot_at timestamptz,
    started_by text NOT NULL DEFAULT '',
    started_at timestamptz,
    ended_at timestamptz,
    is_ok boolean NOT NULL DEFAULT false,
    is_missed boolean NOT NULL DEFAULT false,
    object_key text NOT NULL DEFAULT '',
    size_bytes bigint NOT NULL DEFAULT 0,
    pruned_count integer NOT NULL DEFAULT 0,
    error text NOT NULL DEFAULT ''
);
COMMENT ON TABLE backup_runs IS 'Scheduled and manual backup runs and their results';

CREATE TABLE settings (
    scope text PRIMARY KEY,
    value jsonb,
    version integer NOT NULL DEFAULT 0,
    updated_by text NOT NULL DEFAULT '',
    updated_at timestamptz
);
COMMENT ON TABLE settings IS 'One JSONB row per scope: general, Sherlock, Watson, email, backup, cases, getting started';

CREATE TABLE applications (
    application_id uuid PRIMARY KEY,
    cluster_id text NOT NULL,
    repository_id uuid,
    credential_id uuid,
    namespace text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    source_kind text NOT NULL DEFAULT '',
    source_url text NOT NULL DEFAULT '',
    chart text NOT NULL DEFAULT '',
    ref text NOT NULL DEFAULT '',
    commit text NOT NULL DEFAULT '',
    values jsonb,
    updated_by text NOT NULL DEFAULT ''
);
COMMENT ON TABLE applications IS 'Helm apps deployed to a cluster: source kind and address, chart, ref, commit, values; may name a repository and a credential';

CREATE TABLE repositories (
    repository_id uuid PRIMARY KEY,
    credential_id uuid,
    name text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    url text NOT NULL DEFAULT '',
    default_branch text NOT NULL DEFAULT '',
    auth_method text NOT NULL DEFAULT '',
    pinned_cert text NOT NULL DEFAULT '',
    is_test_ok boolean NOT NULL DEFAULT false,
    tested_at timestamptz,
    updated_by text NOT NULL DEFAULT '',
    updated_at timestamptz
);
COMMENT ON TABLE repositories IS 'Registered chart and runbook sources: Git repositories, Helm repositories and OCI registries';

CREATE TABLE credentials (
    credential_id uuid PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    endpoint text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    username text NOT NULL DEFAULT '',
    region text NOT NULL DEFAULT '',
    pinned_cert text NOT NULL DEFAULT '',
    expires_at timestamptz,
    secret_ref text NOT NULL DEFAULT '',
    last_test jsonb,
    created_by text NOT NULL DEFAULT '',
    updated_at timestamptz
);
COMMENT ON TABLE credentials IS 'Credentials of Administration › Credentials: name, kind, endpoint and test result; the secret itself stays in a Secret';

-- Keys.
ALTER TABLE alert_acks ADD CONSTRAINT alert_acks_cluster_id_fkey
    FOREIGN KEY (cluster_id) REFERENCES clusters (cluster_id) ON DELETE CASCADE;
CREATE INDEX alert_acks_cluster_id_idx ON alert_acks (cluster_id);
ALTER TABLE applications ADD CONSTRAINT applications_cluster_id_fkey
    FOREIGN KEY (cluster_id) REFERENCES clusters (cluster_id) ON DELETE CASCADE;
CREATE INDEX applications_cluster_id_idx ON applications (cluster_id);
ALTER TABLE applications ADD CONSTRAINT applications_repository_id_fkey
    FOREIGN KEY (repository_id) REFERENCES repositories (repository_id) ON DELETE SET NULL;
CREATE INDEX applications_repository_id_idx ON applications (repository_id);
ALTER TABLE applications ADD CONSTRAINT applications_credential_id_fkey
    FOREIGN KEY (credential_id) REFERENCES credentials (credential_id) ON DELETE SET NULL;
CREATE INDEX applications_credential_id_idx ON applications (credential_id);
ALTER TABLE repositories ADD CONSTRAINT repositories_credential_id_fkey
    FOREIGN KEY (credential_id) REFERENCES credentials (credential_id) ON DELETE SET NULL;
CREATE INDEX repositories_credential_id_idx ON repositories (credential_id);
