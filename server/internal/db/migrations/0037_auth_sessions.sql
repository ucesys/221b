-- The sign-in sessions are rows (DB-66): a session was a Secret of its own, named for
-- the SHA-256 of the session's id, and is now a row of auth_sessions under that same
-- hash, with every field of its record a column. Nothing in a session's record is
-- secret, and neither the id (the cookie's value) nor the key that signs a session made
-- while the database is away (DB-65) is ever in this table.
--
-- The table holds no rows (nothing wrote to it before this step), so its key is retyped
-- in place. A row put there by hand could not be a session, whose key is a hash, and
-- would stop the step at the check below: there is none after this line.

DELETE FROM auth_sessions;

ALTER TABLE auth_sessions
    -- The hash of the session's id, in hexadecimal: what the Secret's name ended in.
    ALTER COLUMN auth_session_id TYPE text USING auth_session_id::text,
    ADD CONSTRAINT auth_sessions_key_is_a_hash CHECK (auth_session_id ~ '^[0-9a-f]{64}$'),
    -- The format of the record (P7-03): a console reads a row of a later format than
    -- its own and never writes it.
    ADD COLUMN record_version integer NOT NULL DEFAULT 1,
    -- Who the person is, as the sign-in's source gave it.
    ADD COLUMN email text NOT NULL DEFAULT '',
    ADD COLUMN display_name text NOT NULL DEFAULT '',
    ADD COLUMN groups text[] NOT NULL DEFAULT '{}',
    -- The user and the groups the clusters know the person as (RB-15, RB-05).
    ADD COLUMN kube_name text NOT NULL DEFAULT '',
    ADD COLUMN kube_groups text[] NOT NULL DEFAULT '{}',
    -- A SuperAdmin (RB-13).
    ADD COLUMN is_admin boolean NOT NULL DEFAULT false,
    -- A session always has its four times.
    ALTER COLUMN groups_read_at SET NOT NULL,
    ALTER COLUMN last_seen_at SET NOT NULL,
    ALTER COLUMN expires_at SET NOT NULL;

COMMENT ON TABLE auth_sessions IS 'Signed-in browser sessions, shared by all replicas, keyed by the SHA-256 of the session id; never the id itself';
