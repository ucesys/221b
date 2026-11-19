-- The agents' instructions, tests and pilot log with every field a column (DB-40). The
-- tables of step 0007 held no rows until this step.

-- An agent's instructions are rows of prompt_versions, each in one state: 'in_force' (one
-- for an agent: what is in force now), 'version' (one that went live), 'draft' (the one on
-- its way to live) and 'revision' (a draft as one save left it, since the last went live).
ALTER TABLE prompt_versions
    DROP COLUMN notes,
    DROP COLUMN pilot,
    -- Its place among the agent's rows of the same state.
    ADD COLUMN position integer NOT NULL DEFAULT 0,
    -- None, for the row in force: the chart's baseline.
    ALTER COLUMN body DROP NOT NULL,
    ALTER COLUMN body DROP DEFAULT,
    -- None for the row in force, which nobody made at one time.
    ALTER COLUMN created_at DROP NOT NULL,
    ALTER COLUMN created_at DROP DEFAULT,
    -- The draft's revision: of a draft or a revision its own, of a version the draft it
    -- was, of the row in force the last one given.
    ADD COLUMN revision_number integer NOT NULL DEFAULT 0,
    -- In force: the baseline the instructions were edited over, and its hash.
    ADD COLUMN base_hash text NOT NULL DEFAULT '',
    ADD COLUMN base_body text NOT NULL DEFAULT '',
    -- A draft: its stage (draft, test, pilot), the approval waiting for it to go live, and
    -- the version it restores.
    ADD COLUMN stage text NOT NULL DEFAULT '',
    ADD COLUMN request_number integer NOT NULL DEFAULT 0,
    ADD COLUMN restored_version integer NOT NULL DEFAULT 0,
    -- A draft's pilot, when it has one (pilot_started_at): who gets the draft, on which
    -- clusters, until when, and who started it.
    ADD COLUMN pilot_started_at timestamptz,
    ADD COLUMN pilot_until timestamptz,
    ADD COLUMN pilot_by text NOT NULL DEFAULT '',
    ADD COLUMN pilot_people text[] NOT NULL DEFAULT '{}',
    ADD COLUMN pilot_cluster_ids text[] NOT NULL DEFAULT '{}',
    -- A revision: the draft was discarded.
    ADD COLUMN is_discarded boolean NOT NULL DEFAULT false,
    -- A version or a revision: how the draft's test ended, when it was tested (tested_at).
    ADD COLUMN tested_at timestamptz,
    ADD COLUMN tested_cases integer NOT NULL DEFAULT 0,
    ADD COLUMN tested_passed integer NOT NULL DEFAULT 0,
    ADD COLUMN tested_state text NOT NULL DEFAULT '';
COMMENT ON COLUMN prompt_versions.version_number IS 'Of a version its number; of the row in force the version in force, 0 for the baseline';
COMMENT ON COLUMN prompt_versions.why IS 'How a version came to be (made live, the baseline taken, restored), or a revision when not by a save';
CREATE INDEX prompt_versions_agent_state_idx ON prompt_versions (agent_id, state, position);

CREATE TABLE prompt_version_notes (
    prompt_version_id uuid NOT NULL REFERENCES prompt_versions (prompt_version_id) ON DELETE CASCADE,
    cluster_id text NOT NULL,
    body text NOT NULL DEFAULT '',
    PRIMARY KEY (prompt_version_id, cluster_id)
);
COMMENT ON TABLE prompt_version_notes IS 'The notes for one cluster, or for every cluster, that go with an agent''s instructions';

CREATE TABLE prompt_version_parts (
    prompt_version_id uuid NOT NULL REFERENCES prompt_versions (prompt_version_id) ON DELETE CASCADE,
    part text NOT NULL,
    -- None: only the hash is kept.
    body text,
    -- In force: the hash of the built-in text the edit was made over.
    base_hash text,
    PRIMARY KEY (prompt_version_id, part)
);
COMMENT ON TABLE prompt_version_parts IS 'Edits of an agent''s built-in text, by part, that go with its instructions';

-- A test run is a draft's, or the one made at start with a new baseline, which is the row
-- in force's.
ALTER TABLE test_runs
    DROP COLUMN ended_at,
    ALTER COLUMN stopped_at_case SET DEFAULT 0,
    ADD COLUMN revision_number integer NOT NULL DEFAULT 0,
    ADD COLUMN same_count integer NOT NULL DEFAULT 0,
    ADD COLUMN replays_planned integer NOT NULL DEFAULT 0;
UPDATE test_runs SET stopped_at_case = 0 WHERE stopped_at_case IS NULL;
ALTER TABLE test_runs ALTER COLUMN stopped_at_case SET NOT NULL;

CREATE TABLE test_run_cases (
    test_run_id uuid NOT NULL REFERENCES test_runs (test_run_id) ON DELETE CASCADE,
    position integer NOT NULL,
    case_key text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    is_built_in boolean NOT NULL DEFAULT false,
    state text NOT NULL DEFAULT '',
    PRIMARY KEY (test_run_id, position)
);
COMMENT ON TABLE test_run_cases IS 'Every case a test run runs, in order, with where it stands';

-- A result names its case by the case's own id: a built-in case has no row, and a result
-- outlives its case.
ALTER TABLE test_case_results DROP CONSTRAINT test_case_results_test_case_id_fkey;
DROP INDEX test_case_results_test_case_id_idx;
ALTER TABLE test_case_results
    DROP COLUMN test_case_id,
    DROP COLUMN expect,
    DROP COLUMN checks,
    ADD COLUMN position integer NOT NULL DEFAULT 0,
    ADD COLUMN case_key text NOT NULL DEFAULT '',
    ADD COLUMN is_built_in boolean NOT NULL DEFAULT false,
    ADD COLUMN why text[] NOT NULL DEFAULT '{}',
    ADD COLUMN tools text[] NOT NULL DEFAULT '{}',
    ADD COLUMN hand_overs text[] NOT NULL DEFAULT '{}',
    -- What the case expected when it ran, when the result keeps it: the planted secrets
    -- are counted, never kept.
    ADD COLUMN has_expect boolean NOT NULL DEFAULT false,
    ADD COLUMN expect_tools text[] NOT NULL DEFAULT '{}',
    ADD COLUMN expect_hand_overs text[] NOT NULL DEFAULT '{}',
    ADD COLUMN expect_is_refusal boolean NOT NULL DEFAULT false,
    ADD COLUMN expect_points text[] NOT NULL DEFAULT '{}',
    ADD COLUMN expect_secret_count integer NOT NULL DEFAULT 0;

CREATE TABLE test_case_result_checks (
    test_case_result_id uuid NOT NULL REFERENCES test_case_results (test_case_result_id) ON DELETE CASCADE,
    position integer NOT NULL,
    what text NOT NULL DEFAULT '',
    is_ok boolean NOT NULL DEFAULT false,
    PRIMARY KEY (test_case_result_id, position)
);
COMMENT ON TABLE test_case_result_checks IS 'Every line a case expected, in order, and whether the answer met it';

ALTER TABLE test_run_replays ADD COLUMN position integer NOT NULL DEFAULT 0;

-- A test case is known by the id the console gives it (t1, t2, ...).
ALTER TABLE test_cases
    ALTER COLUMN test_case_id TYPE text USING test_case_id::text,
    DROP COLUMN recorded,
    DROP COLUMN expect,
    ADD COLUMN position integer NOT NULL DEFAULT 0,
    ALTER COLUMN created_at DROP NOT NULL,
    ALTER COLUMN created_at DROP DEFAULT,
    ADD COLUMN expect_tools text[] NOT NULL DEFAULT '{}',
    ADD COLUMN expect_hand_overs text[] NOT NULL DEFAULT '{}',
    ADD COLUMN expect_is_refusal boolean NOT NULL DEFAULT false,
    ADD COLUMN expect_points text[] NOT NULL DEFAULT '{}',
    -- Made-up secrets planted in the recorded results, which the answer must not repeat.
    ADD COLUMN expect_secrets text[] NOT NULL DEFAULT '{}';

CREATE TABLE test_case_recordings (
    test_case_id text NOT NULL REFERENCES test_cases (test_case_id) ON DELETE CASCADE,
    position integer NOT NULL,
    tool text NOT NULL DEFAULT '',
    -- What the agent gave the tool, as it was recorded; none when nothing.
    input text,
    result text NOT NULL DEFAULT '',
    is_error boolean NOT NULL DEFAULT false,
    PRIMARY KEY (test_case_id, position)
);
COMMENT ON TABLE test_case_recordings IS 'The tool results a test case answers from, in order, recorded once';

-- A pilot's entry names its agent and the version that answered as the answer did; it
-- outlives the draft.
ALTER TABLE pilot_entries DROP CONSTRAINT pilot_entries_prompt_version_id_fkey;
DROP INDEX pilot_entries_prompt_version_id_idx;
ALTER TABLE pilot_entries
    DROP COLUMN prompt_version_id,
    ADD COLUMN entry_number integer NOT NULL DEFAULT 0,
    ADD COLUMN agent_id text NOT NULL DEFAULT '',
    ADD COLUMN version text NOT NULL DEFAULT '';
CREATE INDEX pilot_entries_entry_number_idx ON pilot_entries (entry_number);
COMMENT ON TABLE pilot_entries IS 'Questions answered by a draft in pilot, the newest kept';
