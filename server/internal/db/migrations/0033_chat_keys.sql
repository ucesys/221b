-- A conversation Sherlock keeps is joined to the change or the case it is of by a key
-- (DB-57), in place of the session's name of step 0028 ("proposal:PC-12", "case:C-12").
-- The key goes with its row: a change or a case that is deleted takes its conversation.
-- The session is not kept any more. Sherlock still names a conversation by it, and it is
-- worked out from the key: 'proposal:' || changes.change_key, 'case:C-' || cases.case_number.

ALTER TABLE chats
    ADD COLUMN change_id uuid REFERENCES changes (change_id) ON DELETE CASCADE,
    ADD COLUMN case_id uuid REFERENCES cases (case_id) ON DELETE CASCADE;

-- The conversations there are get their key from their session's name, matched whole:
-- "case:C-1" is case 1's and not case 12's.
UPDATE chats c SET change_id = g.change_id FROM changes g WHERE c.session = 'proposal:' || g.change_key;
UPDATE chats c SET case_id = (SELECT k.case_id FROM cases k WHERE 'case:C-' || k.case_number = c.session ORDER BY k.object_name LIMIT 1)
    WHERE c.change_id IS NULL;

-- A conversation whose change or case is not there is of nothing, and goes with its messages.
DELETE FROM chats WHERE change_id IS NULL AND case_id IS NULL;

ALTER TABLE chats
    DROP COLUMN session,
    -- A conversation is of one record: a change or a case, and each has at most one.
    ADD CONSTRAINT chats_of_one_record_check CHECK (num_nonnulls(change_id, case_id) = 1),
    ADD CONSTRAINT chats_change_id_key UNIQUE (change_id),
    ADD CONSTRAINT chats_case_id_key UNIQUE (case_id);
COMMENT ON TABLE chats IS 'The conversations with Sherlock kept for a waiting change or a case, each joined to that change or case by its key';
