-- Activity log (docs/data-model.md). Keys are UUIDv7 made by the console unless the
-- model names a natural key; every key is named for its table.

CREATE TABLE audit_events (
    audit_event_id uuid PRIMARY KEY,
    at timestamptz NOT NULL DEFAULT now(),
    -- Who did it: the name shown, and the subject and email the sign-in gave.
    actor text NOT NULL DEFAULT '',
    actor_subject text NOT NULL DEFAULT '',
    actor_email text NOT NULL DEFAULT '',
    action text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '',
    target text NOT NULL DEFAULT '',
    outcome text NOT NULL DEFAULT '',
    -- A sentence, searched by word; never a credential.
    detail text NOT NULL DEFAULT '',
    source_ip text NOT NULL DEFAULT '',
    -- Set on an event that came back from a backup: when the backup was taken.
    restored_at timestamptz
);

COMMENT ON TABLE audit_events IS 'Every recorded action: who, what, cluster, target, outcome';

CREATE TABLE toolbox_sessions (
    toolbox_session_id uuid PRIMARY KEY,
    approval_request_id uuid,
    change_id uuid,
    cluster_id text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    namespace text NOT NULL DEFAULT '',
    pod text NOT NULL DEFAULT '',
    container text NOT NULL DEFAULT '',
    owner text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT '',
    started_at timestamptz,
    ends_at timestamptz,
    ended_at timestamptz,
    exit_code integer,
    transcript_ref text NOT NULL DEFAULT ''
);
COMMENT ON TABLE toolbox_sessions IS 'Shell, port-forward and debug sessions: owner, target, limits, end and exit; a production session points to its approval request';

-- Keys.
ALTER TABLE toolbox_sessions ADD CONSTRAINT toolbox_sessions_approval_request_id_fkey
    FOREIGN KEY (approval_request_id) REFERENCES approval_requests (approval_request_id) ON DELETE SET NULL;
CREATE INDEX toolbox_sessions_approval_request_id_idx ON toolbox_sessions (approval_request_id);
