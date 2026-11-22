-- Changes (docs/data-model.md). Keys are UUIDv7 made by the console unless the
-- model names a natural key; every key is named for its table.

CREATE TABLE change_actions (
    change_action_id uuid PRIMARY KEY,
    change_id uuid NOT NULL,
    position integer NOT NULL DEFAULT 0,
    kind text NOT NULL DEFAULT '',
    args jsonb,
    impact text NOT NULL DEFAULT '',
    risk text NOT NULL DEFAULT '',
    plan_summary text NOT NULL DEFAULT '',
    plan_warning text NOT NULL DEFAULT '',
    planned_at timestamptz,
    is_ok boolean NOT NULL DEFAULT false,
    result_summary text NOT NULL DEFAULT '',
    watson_ref text NOT NULL DEFAULT '',
    ran_at timestamptz
);
COMMENT ON TABLE change_actions IS 'What a change will do, its dry-run plan and its result';

CREATE TABLE changes (
    change_id uuid PRIMARY KEY,
    agent_id text NOT NULL,
    cluster_id text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    reasoning text NOT NULL DEFAULT '',
    evidence jsonb,
    source jsonb,
    state text NOT NULL DEFAULT '',
    is_production boolean NOT NULL DEFAULT false,
    approval_request_id uuid,
    replaces_change_id uuid,
    dedup_key text NOT NULL DEFAULT '',
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz
);
COMMENT ON TABLE changes IS 'Changes the agents propose, from proposal to applied or failed: reasoning, state, expiry';

CREATE TABLE change_events (
    change_event_id uuid PRIMARY KEY,
    change_id uuid NOT NULL,
    at text NOT NULL DEFAULT '',
    what text NOT NULL DEFAULT '',
    actor text NOT NULL DEFAULT ''
);
COMMENT ON TABLE change_events IS 'History of each change';

CREATE TABLE change_chats (
    change_chat_id uuid PRIMARY KEY,
    change_id uuid NOT NULL,
    at text NOT NULL DEFAULT '',
    role text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT ''
);
COMMENT ON TABLE change_chats IS 'The conversation with Sherlock about a waiting change';

CREATE TABLE change_cases (
    change_id uuid NOT NULL,
    case_id uuid NOT NULL,
    linked_by text NOT NULL DEFAULT '',
    PRIMARY KEY (change_id, case_id)
);
COMMENT ON TABLE change_cases IS 'The cases a change resolves';

-- Keys.
ALTER TABLE change_actions ADD CONSTRAINT change_actions_change_id_fkey
    FOREIGN KEY (change_id) REFERENCES changes (change_id) ON DELETE CASCADE;
CREATE INDEX change_actions_change_id_idx ON change_actions (change_id);
ALTER TABLE changes ADD CONSTRAINT changes_agent_id_fkey
    FOREIGN KEY (agent_id) REFERENCES agents (agent_id) ON DELETE RESTRICT;
CREATE INDEX changes_agent_id_idx ON changes (agent_id);
ALTER TABLE changes ADD CONSTRAINT changes_approval_request_id_fkey
    FOREIGN KEY (approval_request_id) REFERENCES approval_requests (approval_request_id) ON DELETE SET NULL;
CREATE INDEX changes_approval_request_id_idx ON changes (approval_request_id);
ALTER TABLE changes ADD CONSTRAINT changes_replaces_change_id_fkey
    FOREIGN KEY (replaces_change_id) REFERENCES changes (change_id) ON DELETE SET NULL;
CREATE INDEX changes_replaces_change_id_idx ON changes (replaces_change_id);
ALTER TABLE change_events ADD CONSTRAINT change_events_change_id_fkey
    FOREIGN KEY (change_id) REFERENCES changes (change_id) ON DELETE CASCADE;
CREATE INDEX change_events_change_id_idx ON change_events (change_id);
ALTER TABLE change_chats ADD CONSTRAINT change_chats_change_id_fkey
    FOREIGN KEY (change_id) REFERENCES changes (change_id) ON DELETE CASCADE;
CREATE INDEX change_chats_change_id_idx ON change_chats (change_id);
ALTER TABLE change_cases ADD CONSTRAINT change_cases_change_id_fkey
    FOREIGN KEY (change_id) REFERENCES changes (change_id) ON DELETE CASCADE;
ALTER TABLE change_cases ADD CONSTRAINT change_cases_case_id_fkey
    FOREIGN KEY (case_id) REFERENCES cases (case_id) ON DELETE CASCADE;
CREATE INDEX change_cases_case_id_idx ON change_cases (case_id);
