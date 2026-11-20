-- The credentials' list is rows (DB-60): what Administration › Credentials keeps of a
-- credential beside its secret was the key "index" of the credentials' Secret, and is
-- now a row of credentials with every field a column. The secret part of each stays in
-- that Secret, under secret-<credential_id>, and nothing of it is in this table.
--
-- The table already holds a row with only its id for each credential a repository
-- points at (step 0001, apps/pg.go), so it is altered, never dropped: those rows stay,
-- with is_kept false, until the copy of the list fills them.

ALTER TABLE credentials
    -- The Secret's key follows from the id; the pinned certificate and the last test
    -- are columns below.
    DROP COLUMN secret_ref,
    DROP COLUMN pinned_cert,
    DROP COLUMN last_test,
    -- False for a row made with only its id, for what points at a credential whose own
    -- record is not here: the list shows the kept rows.
    ADD COLUMN is_kept boolean NOT NULL DEFAULT false,
    -- A private CA trusted for the endpoint's TLS.
    ADD COLUMN ca_pem text NOT NULL DEFAULT '',
    -- The one certificate trusted for the endpoint when no CA signed it; '' for none.
    ADD COLUMN pinned_cert_sha256 text NOT NULL DEFAULT '',
    ADD COLUMN pinned_cert_subject text NOT NULL DEFAULT '',
    ADD COLUMN pinned_cert_issuer text NOT NULL DEFAULT '',
    ADD COLUMN pinned_cert_not_after timestamptz,
    ADD COLUMN pinned_by text NOT NULL DEFAULT '',
    ADD COLUMN pinned_at timestamptz,
    -- How a token goes on a request: never its value.
    ADD COLUMN send_method text NOT NULL DEFAULT '',
    ADD COLUMN send_username text NOT NULL DEFAULT '',
    ADD COLUMN send_header text NOT NULL DEFAULT '',
    ADD COLUMN send_prefix text NOT NULL DEFAULT '',
    ADD COLUMN send_param text NOT NULL DEFAULT '',
    ADD COLUMN send_cookie text NOT NULL DEFAULT '',
    -- The parts of a kind that are not secret: an SSH server's public key, a GitHub
    -- App's ids, an OAuth2 client's token URL, id and scope, a client certificate.
    ADD COLUMN host_key text NOT NULL DEFAULT '',
    ADD COLUMN app_id text NOT NULL DEFAULT '',
    ADD COLUMN installation_id text NOT NULL DEFAULT '',
    ADD COLUMN token_url text NOT NULL DEFAULT '',
    ADD COLUMN client_id text NOT NULL DEFAULT '',
    ADD COLUMN scope text NOT NULL DEFAULT '',
    ADD COLUMN cert_pem text NOT NULL DEFAULT '',
    -- The names of the secret parts kept in the Secret, never their values.
    ADD COLUMN secret_parts text[] NOT NULL DEFAULT '{}',
    -- The last test; tested_at is null for a credential never tested.
    ADD COLUMN tested_at timestamptz,
    ADD COLUMN is_test_ok boolean NOT NULL DEFAULT false,
    ADD COLUMN test_target text NOT NULL DEFAULT '',
    ADD COLUMN test_method text NOT NULL DEFAULT '',
    ADD COLUMN test_status integer NOT NULL DEFAULT 0,
    ADD COLUMN test_text text NOT NULL DEFAULT '',
    ADD COLUMN test_suggest text NOT NULL DEFAULT '',
    ADD COLUMN tested_by text NOT NULL DEFAULT '',
    -- The certificate the address presented at that test; test_cert_sha256 is '' when
    -- the test kept none.
    ADD COLUMN test_cert_sha256 text NOT NULL DEFAULT '',
    ADD COLUMN test_cert_subject text NOT NULL DEFAULT '',
    ADD COLUMN test_cert_issuer text NOT NULL DEFAULT '',
    ADD COLUMN test_cert_not_before timestamptz,
    ADD COLUMN test_cert_not_after timestamptz,
    ADD COLUMN test_cert_names text[] NOT NULL DEFAULT '{}',
    ADD COLUMN is_test_cert_self_signed boolean NOT NULL DEFAULT false,
    ADD COLUMN is_test_cert_expired boolean NOT NULL DEFAULT false,
    ADD COLUMN is_test_cert_trusted boolean NOT NULL DEFAULT false,
    ADD COLUMN is_test_cert_pinned boolean NOT NULL DEFAULT false,
    ADD COLUMN test_cert_pinned_was text NOT NULL DEFAULT '',
    ADD COLUMN is_test_cert_trustable boolean NOT NULL DEFAULT false,
    ADD COLUMN test_cert_problem text NOT NULL DEFAULT '',
    ADD COLUMN created_at timestamptz,
    ADD COLUMN updated_by text NOT NULL DEFAULT '',
    -- What the upgrade to 0.12.1 made it from.
    ADD COLUMN migrated_from text[] NOT NULL DEFAULT '{}';

-- No two credentials of the list have one name, whatever its case.
CREATE UNIQUE INDEX credentials_name_key ON credentials (lower(name)) WHERE is_kept;
COMMENT ON TABLE credentials IS 'Credentials of Administration › Credentials, every field but the secret a column; the secret itself stays in a Secret. A row with is_kept false holds only the id of a credential something points at';

-- A source's credential is a key (the handover's Changes put off, 10), as a
-- repository's is: none is null, and one whose own record is not here gets a row with
-- only its id. A credential that is used is not removed (CR-06), so the key is emptied
-- only by a row deleted by hand.
ALTER TABLE knowledge_sources
    ALTER COLUMN credential_id DROP DEFAULT,
    ALTER COLUMN credential_id DROP NOT NULL;
UPDATE knowledge_sources SET credential_id = NULL WHERE credential_id = '';
INSERT INTO credentials (credential_id)
    SELECT DISTINCT credential_id FROM knowledge_sources WHERE credential_id IS NOT NULL
    ON CONFLICT (credential_id) DO NOTHING;
ALTER TABLE knowledge_sources ADD CONSTRAINT knowledge_sources_credential_id_fkey
    FOREIGN KEY (credential_id) REFERENCES credentials (credential_id) ON DELETE SET NULL;
CREATE INDEX knowledge_sources_credential_id_idx ON knowledge_sources (credential_id);
