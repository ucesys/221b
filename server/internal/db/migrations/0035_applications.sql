-- The applications are rows (DB-62): what the console keeps of a Helm application was
-- the key "spec.json" of the application's own Secret, and is now a row of applications
-- with every field a column, and its deploy commands rows of application_commands. The
-- secret part of each stays in that Secret, under the key "set.json": the values file,
-- the parameters and each command's parameters, which hold what people type into them,
-- passwords among them. Nothing of them is in these tables: the model's values column
-- is dropped, never having been filled.
--
-- The table of step 0001 held no rows, so it is made again as it is kept from now on.

DROP TABLE applications;

CREATE TABLE applications (
    application_id uuid PRIMARY KEY,
    cluster_id text NOT NULL REFERENCES clusters (cluster_id) ON DELETE CASCADE,
    namespace text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    -- The source: git (or ''), helm or oci; its address, the chart's directory in a git
    -- repository, a repository's chart by name, and the branch, tag or version deployed.
    source_kind text NOT NULL DEFAULT '',
    source_url text NOT NULL DEFAULT '',
    source_path text NOT NULL DEFAULT '',
    chart text NOT NULL DEFAULT '',
    ref text NOT NULL DEFAULT '',
    ref_kind text NOT NULL DEFAULT '',
    -- The repository of Administration › Settings › Repositories the source names: the key
    -- while the repository is listed, which follows it through a new name, beside the
    -- name as it was last saved, which is read once the repository is gone.
    repository_id uuid REFERENCES repositories (repository_id) ON DELETE SET NULL,
    repository_name text NOT NULL DEFAULT '',
    -- The credential the git repository is fetched with; null for none.
    credential_id text REFERENCES credentials (credential_id) ON DELETE SET NULL,
    -- The record names its credential: a token of before 0.12.1, which stays in the
    -- Secret under "token", is no longer read.
    is_credential_migrated boolean NOT NULL DEFAULT false,
    -- The commit last installed or upgraded from, and the chart's version there.
    commit text NOT NULL DEFAULT '',
    commit_at timestamptz,
    commit_subject text NOT NULL DEFAULT '',
    chart_version text NOT NULL DEFAULT '',
    -- A second values file in the chart's directory, by name: never its contents.
    values_file text NOT NULL DEFAULT '',
    is_namespace_created boolean NOT NULL DEFAULT false,
    is_defaults_ignored boolean NOT NULL DEFAULT false,
    -- The fleet it is deployed to, by the fleet's id within its cluster, and the fleet's
    -- deploy label when it was saved.
    fleet_key text NOT NULL DEFAULT '',
    fleet_label text NOT NULL DEFAULT '',
    -- The deployed revision the source was found to render exactly, how it rendered
    -- differently when it did (a count and a commit), and the revision of a release the
    -- console adopted.
    matched_revision integer NOT NULL DEFAULT 0,
    source_diff text NOT NULL DEFAULT '',
    adopted_revision integer NOT NULL DEFAULT 0,
    -- The namespaces whoever created it could reach then.
    creator_namespaces text[] NOT NULL DEFAULT '{}',
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz,
    updated_by text NOT NULL DEFAULT '',
    updated_at timestamptz,
    -- What a save is held to: a record is saved only over the version that was read.
    version integer NOT NULL DEFAULT 0,
    UNIQUE (cluster_id, namespace, name)
);
CREATE INDEX applications_repository_id_idx ON applications (repository_id);
CREATE INDEX applications_credential_id_idx ON applications (credential_id);
COMMENT ON TABLE applications IS 'Helm apps deployed to a cluster: the source, the commit deployed and who set it, every field but the values a column; the values file, the parameters and the commands'' parameters stay in the application''s Secret';

-- An application's own deploy commands, in the menu's order. A command's parameters stay
-- in the application's Secret, beside the command's name.
CREATE TABLE application_commands (
    application_id uuid NOT NULL REFERENCES applications (application_id) ON DELETE CASCADE,
    position integer NOT NULL,
    name text NOT NULL DEFAULT '',
    note text NOT NULL DEFAULT '',
    created_at timestamptz,
    created_by text NOT NULL DEFAULT '',
    updated_at timestamptz,
    updated_by text NOT NULL DEFAULT '',
    PRIMARY KEY (application_id, position)
);
COMMENT ON TABLE application_commands IS 'An application''s named redeploys: name, note and who set them; their parameters stay in the application''s Secret';
