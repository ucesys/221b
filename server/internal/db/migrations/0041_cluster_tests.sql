-- A connection's last test is kept with the connection (DB-64): until now it was kept
-- in the memory of the replica that ran it, and a restart lost it. The test's outcome,
-- what it found and who ran it are columns of clusters; its steps are rows of
-- cluster_test_steps, in the order the page shows them. Nothing of a credential is in
-- either: a step says what it found in words that never repeat a secret (P3-06).

ALTER TABLE clusters
    -- When the last test ran; null for a connection never tested.
    ADD COLUMN tested_at timestamptz,
    -- Who ran it, as the API names a person (an email, else a name, else a subject).
    ADD COLUMN tested_by text NOT NULL DEFAULT '',
    -- Its outcome: ok, warn or err, the sentence that sums it up, and the first failing
    -- step's text.
    ADD COLUMN test_tone text NOT NULL DEFAULT '',
    ADD COLUMN test_summary text NOT NULL DEFAULT '',
    ADD COLUMN test_error text NOT NULL DEFAULT '',
    -- What it found: the Kubernetes version, the nodes counted (and whether there were
    -- more), who the credential signs in as and whether it may impersonate people.
    ADD COLUMN test_version text NOT NULL DEFAULT '',
    ADD COLUMN test_nodes integer NOT NULL DEFAULT 0,
    ADD COLUMN is_test_nodes_more boolean NOT NULL DEFAULT false,
    ADD COLUMN test_who text NOT NULL DEFAULT '',
    ADD COLUMN is_test_impersonate boolean NOT NULL DEFAULT false;

CREATE TABLE cluster_test_steps (
    cluster_id text NOT NULL REFERENCES clusters (cluster_id) ON DELETE CASCADE,
    position integer NOT NULL,
    -- Which check: address, tls, auth, kube or perms.
    step_key text NOT NULL DEFAULT '',
    -- ok, warn, err or muted (not reached).
    tone text NOT NULL DEFAULT '',
    text text NOT NULL DEFAULT '',
    PRIMARY KEY (cluster_id, position)
);

COMMENT ON TABLE cluster_test_steps IS 'The steps of a connection''s last test, in order';
