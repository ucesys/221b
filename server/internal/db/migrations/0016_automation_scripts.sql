-- Automation's scripts with every field a column (DB-32). The tables of step 0006 held
-- no rows until this step: no build wrote to them.

-- A script may be in no group, and is known by its name; so is a group.
ALTER TABLE scripts ALTER COLUMN script_group_id DROP NOT NULL;
CREATE UNIQUE INDEX scripts_name_key ON scripts (name);
CREATE UNIQUE INDEX script_groups_name_key ON script_groups (name);

-- What a version's header declares, who wrote it, and Sherlock's reading of it, each
-- in a column of its own.
ALTER TABLE script_versions
    DROP COLUMN header,
    DROP COLUMN review,
    ADD COLUMN description text NOT NULL DEFAULT '',
    ADD COLUMN privilege text NOT NULL DEFAULT '',
    ADD COLUMN has_dry_run boolean NOT NULL DEFAULT false,
    -- Empty for nodes, or kubernetes for the cluster.
    ADD COLUMN target text NOT NULL DEFAULT '',
    -- A Kubernetes script's default identity: namespace/name of a service account.
    ADD COLUMN runs_as text NOT NULL DEFAULT '',
    -- The facts about a node the script asks for.
    ADD COLUMN facts text[] NOT NULL DEFAULT '{}',
    ADD COLUMN author_user text NOT NULL DEFAULT '',
    -- The rejected version whose text this one has again.
    ADD COLUMN same_as_version integer,
    -- Sherlock's latest reading; no summary, no reading yet.
    ADD COLUMN review_summary text,
    ADD COLUMN reviewed_at timestamptz;
CREATE UNIQUE INDEX script_versions_script_id_version_number_key ON script_versions (script_id, version_number);
COMMENT ON TABLE script_versions IS 'A script''s hashed texts, each with its state, what its header declares, its author and Sherlock''s reading';

-- The order the header declares them in.
ALTER TABLE script_version_parameters ADD COLUMN position integer NOT NULL DEFAULT 0;

CREATE TABLE script_version_kube_actions (
    script_version_id uuid NOT NULL REFERENCES script_versions (script_version_id) ON DELETE CASCADE,
    position integer NOT NULL,
    verb text NOT NULL DEFAULT '',
    resource text NOT NULL DEFAULT '',
    PRIMARY KEY (script_version_id, position)
);
COMMENT ON TABLE script_version_kube_actions IS 'What a Kubernetes script''s header says it may do: a verb on a resource';

CREATE TABLE script_version_findings (
    script_version_id uuid NOT NULL REFERENCES script_versions (script_version_id) ON DELETE CASCADE,
    -- checks: the console's own checks of the text. review: Sherlock's reading.
    source text NOT NULL CHECK (source IN ('checks', 'review')),
    position integer NOT NULL,
    level text NOT NULL DEFAULT '',
    -- The line of the text, from 1; 0 for the text as a whole.
    line integer NOT NULL DEFAULT 0,
    code text NOT NULL DEFAULT '',
    message text NOT NULL DEFAULT '',
    PRIMARY KEY (script_version_id, source, position)
);
COMMENT ON TABLE script_version_findings IS 'What the checks and Sherlock found in a version, on its lines';

CREATE TABLE script_version_actions (
    script_version_id uuid NOT NULL REFERENCES script_versions (script_version_id) ON DELETE CASCADE,
    -- edited: saved the draft without being its author. submitted, approved, rejected: the
    -- review. revoked: took an approval back, and withdrawn: the approval taken back.
    kind text NOT NULL CHECK (kind IN ('edited', 'submitted', 'approved', 'revoked', 'withdrawn', 'rejected')),
    position integer NOT NULL,
    by_name text NOT NULL DEFAULT '',
    by_user text NOT NULL DEFAULT '',
    at timestamptz,
    comment text NOT NULL DEFAULT '',
    PRIMARY KEY (script_version_id, kind, position)
);
COMMENT ON TABLE script_version_actions IS 'What people did with a version: saved its draft, submitted, approved, rejected it, took an approval back';
