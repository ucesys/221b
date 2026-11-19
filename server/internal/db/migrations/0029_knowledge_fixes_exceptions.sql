-- The fixes and the accepted exceptions with every field a column (DB-47). The tables of
-- step 0011 held no rows until this step.

-- A fix is known by the id the console gives it ("FX-12"): cases, the wiki and the audit
-- log name a fix by it. The fix a duplicate repeats is named by that id too: it may be gone.
ALTER TABLE fixes DROP CONSTRAINT fixes_duplicate_of_fix_id_fkey;
ALTER TABLE fixes
    ADD COLUMN fix_key text NOT NULL,
    -- Its place in the list: a newer fix has a greater one.
    ADD COLUMN position bigint NOT NULL DEFAULT 0,
    -- The steps are one text, as people write them.
    ALTER COLUMN steps DROP DEFAULT,
    ALTER COLUMN steps TYPE text USING '',
    ALTER COLUMN steps SET DEFAULT '',
    -- Why it is pending or inactive: a duplicate, a contradiction, a person.
    ADD COLUMN reason text NOT NULL DEFAULT '',
    DROP COLUMN duplicate_of_fix_id,
    ADD COLUMN duplicate_of_fix_key text NOT NULL DEFAULT '',
    -- The fixes it contradicts, and those a person accepted beside it, by their ids.
    ADD COLUMN contradicts text[] NOT NULL DEFAULT '{}',
    ADD COLUMN accepted text[] NOT NULL DEFAULT '{}',
    ALTER COLUMN created_at DROP DEFAULT,
    ADD COLUMN updated_at timestamptz NOT NULL,
    ADD COLUMN updated_by text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX fixes_fix_key_key ON fixes (fix_key);
CREATE INDEX fixes_problem_key_idx ON fixes (problem_key);
COMMENT ON TABLE fixes IS 'Recorded fixes: the problem, the cause, the steps, the commands that worked, and whether each is active';

DROP TABLE fix_cases;
CREATE TABLE fix_cases (
    fix_id uuid NOT NULL REFERENCES fixes (fix_id) ON DELETE CASCADE,
    position integer NOT NULL,
    -- The case, by the id people read ("C-12"): cases go after their retention.
    case_key text NOT NULL DEFAULT '',
    seen_at timestamptz NOT NULL,
    cluster_id text NOT NULL DEFAULT '',
    seen_by text NOT NULL DEFAULT '',
    PRIMARY KEY (fix_id, position)
);
CREATE INDEX fix_cases_case_key_idx ON fix_cases (case_key);
COMMENT ON TABLE fix_cases IS 'The cases each fix was seen in, with when, on which cluster and who closed it';

-- An exception is known by the id the console gives it ("EX-12"): findings, cases and
-- acknowledged alerts name an exception by it. Where it came from (the finding, the run,
-- the job, the case) and the exception a duplicate repeats are named by their own ids:
-- each may be gone, and Cases have not moved.
ALTER TABLE exceptions DROP CONSTRAINT exceptions_finding_id_fkey;
ALTER TABLE exceptions DROP CONSTRAINT exceptions_job_run_id_fkey;
ALTER TABLE exceptions DROP CONSTRAINT exceptions_case_id_fkey;
ALTER TABLE exceptions DROP CONSTRAINT exceptions_duplicate_of_exception_id_fkey;
ALTER TABLE exceptions
    ADD COLUMN exception_key text NOT NULL,
    -- Its place in the list: the newest is first there, and has the greatest.
    ADD COLUMN position bigint NOT NULL DEFAULT 0,
    ADD COLUMN detail text NOT NULL DEFAULT '',
    DROP COLUMN finding_id,
    DROP COLUMN job_run_id,
    DROP COLUMN case_id,
    DROP COLUMN duplicate_of_exception_id,
    ADD COLUMN finding_key text NOT NULL DEFAULT '',
    ADD COLUMN run_key text NOT NULL DEFAULT '',
    ADD COLUMN job_id text NOT NULL DEFAULT '',
    ADD COLUMN case_key text NOT NULL DEFAULT '',
    ADD COLUMN duplicate_of_exception_key text NOT NULL DEFAULT '',
    ADD COLUMN created_at timestamptz NOT NULL,
    ADD COLUMN removed_by text NOT NULL DEFAULT '',
    -- Its page in the wiki, once written, or why it could not be.
    ADD COLUMN wiki_path text NOT NULL DEFAULT '',
    ADD COLUMN wiki_error text NOT NULL DEFAULT '',
    -- The findings it left out since, the newest, and the run whose findings it left out last.
    ADD COLUMN match_count integer NOT NULL DEFAULT 0,
    ADD COLUMN last_matched_at timestamptz,
    ADD COLUMN last_run_key text NOT NULL DEFAULT '',
    ADD COLUMN updated_at timestamptz;
CREATE UNIQUE INDEX exceptions_exception_key_key ON exceptions (exception_key);
CREATE INDEX exceptions_cover_idx ON exceptions (cluster_id, resource, check_name);
COMMENT ON TABLE exceptions IS 'Accepted exceptions: findings on this check, resource and cluster are left out until expiry or removal';
