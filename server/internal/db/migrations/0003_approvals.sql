-- Approvals (docs/data-model.md). Keys are UUIDv7 made by the console unless the
-- model names a natural key; every key is named for its table.

CREATE TABLE approval_requests (
    approval_request_id uuid PRIMARY KEY,
    -- The number people read, in the order the requests were made.
    request_number integer NOT NULL DEFAULT 0,
    kind text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '',
    -- What was asked, as its kind has it: read whole.
    summary jsonb,
    requested_by text NOT NULL DEFAULT '',
    -- The requester's key, which the one who decides must not share.
    requested_by_key text NOT NULL DEFAULT '',
    required_count integer NOT NULL DEFAULT 0,
    state text NOT NULL DEFAULT '',
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz
);

COMMENT ON TABLE approval_requests IS 'What was asked, by whom, on which cluster, how many approvers are required, state, expiry';

CREATE TABLE approval_decisions (
    approval_decision_id uuid PRIMARY KEY,
    approval_request_id uuid NOT NULL,
    approval_policy_id text,
    decided_by text NOT NULL DEFAULT '',
    decided_by_key text NOT NULL DEFAULT '',
    -- approved, rejected or withdrawn.
    verdict text NOT NULL DEFAULT '',
    comment text NOT NULL DEFAULT '',
    decided_at timestamptz
);

COMMENT ON TABLE approval_decisions IS 'One row per approve, reject, withdraw or revoke: who, when, comment';

CREATE TABLE approval_policies (
    -- The id the console gave the standing approval; the audit log carries it.
    approval_policy_id text PRIMARY KEY,
    -- Its place in the list, which people order.
    position integer NOT NULL DEFAULT 0,
    name text NOT NULL DEFAULT '',
    is_enabled boolean NOT NULL DEFAULT false,
    agent_id text NOT NULL DEFAULT '',
    cluster_ids text[] NOT NULL DEFAULT '{}',
    actions text[] NOT NULL DEFAULT '{}',
    namespaces text[] NOT NULL DEFAULT '{}',
    condition text NOT NULL DEFAULT '',
    per_hour integer NOT NULL DEFAULT 0,
    -- Who the changes it approves run as.
    run_as_user text NOT NULL DEFAULT '',
    run_as_groups text[] NOT NULL DEFAULT '{}',
    updated_by text NOT NULL DEFAULT '',
    updated_by_key text NOT NULL DEFAULT '',
    updated_by_kind text NOT NULL DEFAULT '',
    created_at timestamptz,
    updated_at timestamptz,
    -- When it last approved, for its limit an hour.
    applied_at timestamptz[] NOT NULL DEFAULT '{}'
);

COMMENT ON TABLE approval_policies IS 'Rules that approve automatically (today''s standing approvals)';

CREATE TABLE notification_channels (
    -- The id the console gave the channel; its address is under it in the Secret.
    notification_channel_id text PRIMARY KEY,
    position integer NOT NULL DEFAULT 0,
    kind text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    is_enabled boolean NOT NULL DEFAULT false,
    secret_ref text NOT NULL DEFAULT ''
);

COMMENT ON TABLE notification_channels IS 'Channels that tell approvers a request is waiting; their addresses stay in a Secret';

-- Keys.
ALTER TABLE approval_decisions ADD CONSTRAINT approval_decisions_approval_request_id_fkey
    FOREIGN KEY (approval_request_id) REFERENCES approval_requests (approval_request_id) ON DELETE CASCADE;
CREATE INDEX approval_decisions_approval_request_id_idx ON approval_decisions (approval_request_id);
ALTER TABLE approval_decisions ADD CONSTRAINT approval_decisions_approval_policy_id_fkey
    FOREIGN KEY (approval_policy_id) REFERENCES approval_policies (approval_policy_id) ON DELETE SET NULL;
CREATE INDEX approval_decisions_approval_policy_id_idx ON approval_decisions (approval_policy_id);
