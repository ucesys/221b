-- Identity (docs/data-model.md). Keys are UUIDv7 made by the console unless the
-- model names a natural key; every key is named for its table.

CREATE TABLE auth_sessions (
    auth_session_id uuid PRIMARY KEY,
    subject text NOT NULL DEFAULT '',
    username text NOT NULL DEFAULT '',
    method text NOT NULL DEFAULT '',
    directory_ref text NOT NULL DEFAULT '',
    roles text[] NOT NULL DEFAULT '{}',
    super_roles text[] NOT NULL DEFAULT '{}',
    groups_read_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz,
    expires_at timestamptz
);
COMMENT ON TABLE auth_sessions IS 'Signed-in browser sessions, shared by all replicas, keyed by a hash of the session id';
