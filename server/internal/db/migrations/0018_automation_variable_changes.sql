-- Automation's variable changes as what they alter (DB-34): a change is rows of
-- variable_change_items, one for each value, mark, ceiling or reboot time it changes,
-- and no longer the fleet's variables before and after. The tables of step 0006 held no
-- rows until this step.

-- A change is on a fleet; its items say which nodes' own values it alters. It outlives
-- its fleet, so it names the fleet as runs do, by its key and its name at the time.
ALTER TABLE variable_changes
    DROP COLUMN scope,
    DROP COLUMN host_id,
    DROP COLUMN fleet_id,
    DROP COLUMN impact,
    -- No request in Approvals yet: who decided is kept here until the two are joined.
    ALTER COLUMN approval_request_id DROP NOT NULL,
    -- The number people know it by.
    ADD COLUMN change_number integer NOT NULL,
    ADD COLUMN cluster_id text NOT NULL DEFAULT '',
    ADD COLUMN fleet_key text NOT NULL DEFAULT '',
    ADD COLUMN fleet_name text NOT NULL DEFAULT '',
    ADD COLUMN requested_by_user text NOT NULL DEFAULT '',
    -- What it changes, as the list shows it.
    ADD COLUMN changed text[] NOT NULL DEFAULT '{}',
    -- What could not be weighed: Sherlock was off, or failed.
    ADD COLUMN note text NOT NULL DEFAULT '',
    ADD COLUMN decided_by text,
    ADD COLUMN decided_by_user text,
    ADD COLUMN decided_at timestamptz,
    -- The reason given with a rejection.
    ADD COLUMN comment text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX variable_changes_change_number_key ON variable_changes (change_number);
CREATE INDEX variable_changes_cluster_id_fleet_key_idx ON variable_changes (cluster_id, fleet_key);
COMMENT ON TABLE variable_changes IS 'A change to one fleet''s variables: who asked, why, its state and who decided';

DROP TABLE variable_change_items;
CREATE TABLE variable_change_items (
    variable_change_id uuid NOT NULL REFERENCES variable_changes (variable_change_id) ON DELETE CASCADE,
    position integer NOT NULL,
    -- value: a fleet's value. node_value: a node's own. ceiling: the privilege ceiling.
    -- sensitive: whether the name is marked. reboot_minutes, node_reboot_minutes: the
    -- fleet's reboot time, a node's own.
    kind text NOT NULL CHECK (kind IN ('value', 'node_value', 'ceiling', 'sensitive', 'reboot_minutes', 'node_reboot_minutes')),
    -- The node, by its name: a change is kept after its nodes are gone.
    node text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    -- None: not set before, or unset by the change.
    old_value text,
    new_value text,
    PRIMARY KEY (variable_change_id, position)
);
COMMENT ON TABLE variable_change_items IS 'Each thing a change alters: a value, a node''s own value, a mark, the ceiling or a reboot time, before and after';

CREATE TABLE variable_change_impacts (
    variable_change_id uuid NOT NULL REFERENCES variable_changes (variable_change_id) ON DELETE CASCADE,
    position integer NOT NULL,
    -- The script, by its name, or what the rule is about.
    script text NOT NULL DEFAULT '',
    version_number integer NOT NULL DEFAULT 0,
    -- safe, or approval.
    decision text NOT NULL DEFAULT '',
    why text NOT NULL DEFAULT '',
    -- rule, for what the console worked out, or sherlock.
    decided_by text NOT NULL DEFAULT '',
    PRIMARY KEY (variable_change_id, position)
);
COMMENT ON TABLE variable_change_impacts IS 'The approved script versions a change touches, and whether each needs the change approved';
