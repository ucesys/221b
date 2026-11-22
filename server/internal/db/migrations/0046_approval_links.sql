-- Where an approval is asked (DB-91). approval_requests holds the console's own
-- two-person actions: an environment change, a SuperAdmin made, and an agent's draft
-- instructions going live. Of the seven records that had a key into it, only the last
-- is approved by such a request: the draft's row of prompt_versions keeps the key of
-- the request that asks for it, beside request_number, the number people read, which
-- stays when the request is pruned. The other six keep their approval in rows of their
-- own (a script version's review, a run request's approvers, a variable change's
-- decision, a change's change_decisions, a wiki page change's state, a Toolbox session's
-- card and approver), and their key, which nothing ever filled, goes.
--
-- A standing approval decides changes, not these requests, and a request does not
-- expire: approval_decisions.approval_policy_id and approval_requests.expires_at go.

ALTER TABLE script_versions DROP COLUMN approval_request_id;
ALTER TABLE script_runs DROP COLUMN approval_request_id;
ALTER TABLE variable_changes DROP COLUMN approval_request_id;
ALTER TABLE changes DROP COLUMN approval_request_id;
ALTER TABLE wiki_changes DROP COLUMN approval_request_id;
ALTER TABLE toolbox_sessions DROP COLUMN approval_request_id;
ALTER TABLE approval_decisions DROP COLUMN approval_policy_id;

DROP INDEX IF EXISTS approval_requests_state_idx;
ALTER TABLE approval_requests DROP COLUMN expires_at;
CREATE INDEX approval_requests_state_idx ON approval_requests (state);
COMMENT ON TABLE approval_requests IS 'The console''s two-person requests: what was asked, by whom, on which cluster, how many approvers are required, state';

-- A draft that waits names its request by number: fill the key from it.
UPDATE prompt_versions p SET approval_request_id = r.approval_request_id
    FROM approval_requests r
    WHERE r.kind = 'prompt' AND r.request_number = p.request_number AND p.request_number > 0;
