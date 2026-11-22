-- The proposed changes with every field a column (DB-45), and the conversations Sherlock
-- keeps for another record (DB-46). The tables of step 0010 held no rows until this step.

DROP TABLE change_actions, change_events, change_chats, change_cases, changes;

CREATE TABLE changes (
    change_id uuid PRIMARY KEY,
    -- The id the console gives a change, "PC-12": cases, runs, the audit log and Watson
    -- name a change by it.
    change_key text NOT NULL UNIQUE,
    -- Its place in the list: a newer change has a greater one.
    position bigint NOT NULL DEFAULT 0,
    -- None: a change with no agent named.
    agent_id text REFERENCES agents (agent_id) ON DELETE RESTRICT,
    -- The agent's model when it proposed.
    model text NOT NULL DEFAULT '',
    -- What started the agent: a job, an alert, a report, a knowledge source, a chat, a case.
    source_kind text NOT NULL DEFAULT '',
    source_key text NOT NULL DEFAULT '',
    source_label text NOT NULL DEFAULT '',
    source_set_by text NOT NULL DEFAULT '',
    source_job text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    reasoning text NOT NULL DEFAULT '',
    -- What must still hold when it is approved; the arguments are whatever the kind takes.
    condition_kind text NOT NULL DEFAULT '',
    condition_args jsonb,
    dedup_key text NOT NULL DEFAULT '',
    expires_at timestamptz,
    state text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    -- The person who asked for it in a chat; none for an agent on its own.
    requester_key text,
    requester_name text NOT NULL DEFAULT '',
    requester_kind text NOT NULL DEFAULT '',
    is_production boolean NOT NULL DEFAULT false,
    needed_count integer NOT NULL DEFAULT 0,
    -- The last check that it still holds; none when it was never checked.
    rechecked_at timestamptz,
    recheck_is_ok boolean NOT NULL DEFAULT false,
    recheck_why text NOT NULL DEFAULT '',
    recheck_checked text[] NOT NULL DEFAULT '{}',
    recheck_warnings text[] NOT NULL DEFAULT '{}',
    -- Why it expired or failed.
    reason text NOT NULL DEFAULT '',
    -- The changes that replaced it and that it replaces, by their ids: either may be gone.
    replaced_by_change_key text NOT NULL DEFAULT '',
    replaces_change_key text NOT NULL DEFAULT '',
    -- The standing approval that applied it, by its id: it may be gone.
    approval_policy_id text NOT NULL DEFAULT '',
    -- A chat change's command in the Watson panel.
    watson_item text NOT NULL DEFAULT '',
    -- Who proposed it and who changed it last; none where the change does not say.
    proposed_by_name text,
    proposed_by_kind text NOT NULL DEFAULT '',
    last_by_name text,
    last_by_kind text NOT NULL DEFAULT '',
    last_at timestamptz,
    approval_request_id uuid REFERENCES approval_requests (approval_request_id) ON DELETE SET NULL
);
CREATE INDEX changes_agent_id_idx ON changes (agent_id);
CREATE INDEX changes_approval_request_id_idx ON changes (approval_request_id);
CREATE INDEX changes_state_idx ON changes (state, expires_at);
CREATE INDEX changes_position_idx ON changes (position);
COMMENT ON TABLE changes IS 'Changes the agents propose, from proposal to applied or failed: what started the agent, the reasoning, the condition, the state, who asked and who changed it last';

CREATE TABLE change_evidence (
    change_id uuid NOT NULL REFERENCES changes (change_id) ON DELETE CASCADE,
    position integer NOT NULL,
    kind text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    ref text NOT NULL DEFAULT '',
    -- When it was true; none where the agent did not say.
    at timestamptz,
    PRIMARY KEY (change_id, position)
);
COMMENT ON TABLE change_evidence IS 'What the agent read before it proposed a change: a log line, an event, a fact, a wiki page, a finding';

CREATE TABLE change_actions (
    change_id uuid NOT NULL REFERENCES changes (change_id) ON DELETE CASCADE,
    position integer NOT NULL,
    kind text NOT NULL DEFAULT '',
    -- Whatever the action's kind takes: one JSON value, by decision.
    args jsonb,
    impact text NOT NULL DEFAULT '',
    risk text NOT NULL DEFAULT '',
    PRIMARY KEY (change_id, position)
);
COMMENT ON TABLE change_actions IS 'What a change will do, in order, with the agent''s word on impact and risk';

CREATE TABLE change_plans (
    change_id uuid NOT NULL REFERENCES changes (change_id) ON DELETE CASCADE,
    -- The action it is of.
    position integer NOT NULL,
    summary text NOT NULL DEFAULT '',
    targets text[] NOT NULL DEFAULT '{}',
    impact text[] NOT NULL DEFAULT '{}',
    -- What the check before approval compares.
    fingerprint text NOT NULL DEFAULT '',
    -- Why Watson could not read the target then.
    unread text NOT NULL DEFAULT '',
    warning text NOT NULL DEFAULT '',
    PRIMARY KEY (change_id, position)
);
COMMENT ON TABLE change_plans IS 'Watson''s reading of each action when the change was proposed';

CREATE TABLE change_results (
    change_id uuid NOT NULL REFERENCES changes (change_id) ON DELETE CASCADE,
    position integer NOT NULL,
    -- The action it is of, counted from 0.
    action_number integer NOT NULL DEFAULT 0,
    is_ok boolean NOT NULL DEFAULT false,
    summary text NOT NULL DEFAULT '',
    detail text NOT NULL DEFAULT '',
    -- The command's card in the approver's Watson panel.
    ref text NOT NULL DEFAULT '',
    at timestamptz NOT NULL,
    PRIMARY KEY (change_id, position)
);
COMMENT ON TABLE change_results IS 'How each action of an approved change went; never its output';

CREATE TABLE change_decisions (
    change_id uuid NOT NULL REFERENCES changes (change_id) ON DELETE CASCADE,
    verdict text NOT NULL CHECK (verdict IN ('approved', 'rejected')),
    position integer NOT NULL,
    by_key text NOT NULL DEFAULT '',
    by_name text NOT NULL DEFAULT '',
    by_kind text NOT NULL DEFAULT '',
    at timestamptz NOT NULL,
    comment text NOT NULL DEFAULT '',
    -- The standing approval that gave it, by its id.
    approval_policy_id text NOT NULL DEFAULT '',
    PRIMARY KEY (change_id, verdict, position)
);
COMMENT ON TABLE change_decisions IS 'The approvals a change has, in order, and its rejection';

CREATE TABLE change_events (
    change_id uuid NOT NULL REFERENCES changes (change_id) ON DELETE CASCADE,
    position integer NOT NULL,
    at timestamptz NOT NULL,
    what text NOT NULL DEFAULT '',
    by_name text NOT NULL DEFAULT '',
    -- Who, with their kind (person, agent, system); none on an event from before that was kept.
    actor_name text,
    actor_kind text NOT NULL DEFAULT '',
    PRIMARY KEY (change_id, position)
);
COMMENT ON TABLE change_events IS 'History of each change';

CREATE TABLE change_commands (
    change_id uuid NOT NULL REFERENCES changes (change_id) ON DELETE CASCADE,
    position integer NOT NULL,
    watson_item text NOT NULL DEFAULT '',
    line text NOT NULL DEFAULT '',
    by_name text NOT NULL DEFAULT '',
    at timestamptz NOT NULL,
    is_change boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT '',
    exit_code integer,
    approved_by text NOT NULL DEFAULT '',
    finished_at timestamptz,
    PRIMARY KEY (change_id, position)
);
COMMENT ON TABLE change_commands IS 'The commands run from a change''s drawer and its own actions once approved; never their output';

CREATE TABLE change_resolves (
    change_id uuid NOT NULL REFERENCES changes (change_id) ON DELETE CASCADE,
    position integer NOT NULL,
    -- A case ("C-12") or a known issue ("KI-3"), by the id people read: either may be gone.
    resolved_key text NOT NULL,
    -- False: only who linked it is kept, and the change no longer lists it.
    is_listed boolean NOT NULL DEFAULT true,
    -- The agent, or the person's name; none where nobody is named.
    linked_by text,
    PRIMARY KEY (change_id, position)
);
CREATE INDEX change_resolves_resolved_key_idx ON change_resolves (resolved_key);
COMMENT ON TABLE change_resolves IS 'The cases and known issues a change resolves, and who linked each';

-- A conversation Sherlock keeps for another record, while that record wants it: a waiting
-- change's ("proposal:PC-12"), a case's ("case:C-12"). The session is Sherlock's name for
-- it, so no key points at the record, which is in another domain.
CREATE TABLE chats (
    chat_id uuid PRIMARY KEY,
    session text NOT NULL UNIQUE,
    cluster_id text NOT NULL DEFAULT '',
    updated_at timestamptz
);
COMMENT ON TABLE chats IS 'The conversations with Sherlock kept for a waiting change or a case, by Sherlock''s session';

CREATE TABLE chat_messages (
    chat_id uuid NOT NULL REFERENCES chats (chat_id) ON DELETE CASCADE,
    position integer NOT NULL,
    -- The number the conversation gave it.
    message_number integer NOT NULL DEFAULT 0,
    role text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    -- What the model wrote before reading once more, while the answer is not done.
    note text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    asked_by text NOT NULL DEFAULT '',
    -- Who answered, when not Sherlock; a name, since a message outlives what it names.
    agent_id text NOT NULL DEFAULT '',
    model text NOT NULL DEFAULT '',
    at timestamptz NOT NULL,
    first_at timestamptz,
    done_at timestamptz,
    cost_usd double precision NOT NULL DEFAULT 0,
    -- None: no use was reported for the message.
    tokens_in integer,
    tokens_out integer NOT NULL DEFAULT 0,
    tokens_cache_write integer NOT NULL DEFAULT 0,
    tokens_cache_read integer NOT NULL DEFAULT 0,
    -- The steps, references, view changes, things shown and hand-overs: one JSON value, by
    -- decision. A chat is read and written whole and never asked for by its parts. It is
    -- json and not jsonb so that it reads back as it was written: a thing shown carries the
    -- input the model gave, key for key.
    parts json,
    PRIMARY KEY (chat_id, position)
);
COMMENT ON TABLE chat_messages IS 'A conversation''s messages, in order: the text and what is asked of a message as columns, its parts as one JSON value';
