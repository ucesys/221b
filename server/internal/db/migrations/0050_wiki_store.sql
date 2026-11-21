-- The wiki's store in the database (DB-96, decided by the owner on November 21, 2026):
-- where Mycroft's wiki is kept while Administration › AI › Knowledge names no Git
-- repository or S3 bucket, in place of the temporary ConfigMap store <release>-wiki
-- (CF-57), which the first start copies in once and then deletes. A file is a path
-- with its body and the version a change is published over; a run's page changes that
-- wait for review are staged apart, by the run, until they are published or discarded.
CREATE TABLE wiki_files (
    wiki_file_id uuid PRIMARY KEY,
    -- The file's path in the wiki: a page, the manifest or an excerpt.
    path text NOT NULL UNIQUE,
    body bytea NOT NULL,
    -- New at every write (a UUIDv7 as text), so a change made over an older one is
    -- a conflict; a file copied from the ConfigMap store keeps the version it had
    -- there, so the runs staged over it publish as they would have.
    version text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE wiki_stages (
    wiki_stage_id uuid PRIMARY KEY,
    -- The run whose page changes these are, by the id the console gives it
    -- (wiki_runs.run_key): a run may be staged before its row is saved, and
    -- its row may go first.
    run_key text NOT NULL UNIQUE,
    staged_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE wiki_staged_files (
    wiki_stage_id uuid NOT NULL REFERENCES wiki_stages (wiki_stage_id) ON DELETE CASCADE,
    path text NOT NULL,
    body bytea NOT NULL,
    PRIMARY KEY (wiki_stage_id, path)
);
