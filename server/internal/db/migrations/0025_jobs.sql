-- The jobs and their runs with every field a column (DB-42). The tables of step 0008
-- held no rows until this step.

-- A job is known by the id the console gives it: its kind's, or one made when it is
-- added. A run and a claim name their job by that id and are kept after the job is gone,
-- and a job nobody changed has no row, so neither is a key.
ALTER TABLE job_runs DROP CONSTRAINT job_runs_job_id_fkey;
ALTER TABLE schedule_claims DROP CONSTRAINT schedule_claims_job_id_fkey;
ALTER TABLE jobs ALTER COLUMN job_id TYPE text USING job_id::text;
ALTER TABLE job_runs ALTER COLUMN job_id TYPE text USING job_id::text;
ALTER TABLE schedule_claims ALTER COLUMN job_id TYPE text USING job_id::text;

ALTER TABLE jobs
    -- False: the job is as its kind makes it, and the row holds only the time it last
    -- handled or what it spent.
    ADD COLUMN is_kept boolean NOT NULL DEFAULT true,
    -- When it runs: on a schedule (has_schedule), on an event, or neither.
    DROP COLUMN schedule,
    ADD COLUMN has_schedule boolean NOT NULL DEFAULT false,
    ADD COLUMN schedule_kind text NOT NULL DEFAULT '',
    ADD COLUMN schedule_at text NOT NULL DEFAULT '',
    ADD COLUMN schedule_days text[] NOT NULL DEFAULT '{}',
    ADD COLUMN schedule_every integer NOT NULL DEFAULT 0,
    ADD COLUMN schedule_cron text NOT NULL DEFAULT '',
    ADD COLUMN schedule_time_zone text NOT NULL DEFAULT '',
    ADD COLUMN event text NOT NULL DEFAULT '',
    ADD COLUMN event_severity text NOT NULL DEFAULT '',
    ADD COLUMN skips_overlap boolean NOT NULL DEFAULT false,
    ADD COLUMN catches_up boolean NOT NULL DEFAULT false,
    -- When its schedule counts from.
    ADD COLUMN since_at timestamptz,
    -- None: the default lowest priority.
    ALTER COLUMN min_priority DROP NOT NULL,
    ALTER COLUMN min_priority DROP DEFAULT,
    -- The newest scheduled time handled: set in the transaction that reads it, so each
    -- time runs once whichever replica gets there first.
    ADD COLUMN handled_at timestamptz,
    -- What it spent on one day (spend_day, UTC), when anything, and why it stopped for its
    -- budget that day.
    ADD COLUMN spend_day text,
    ADD COLUMN spend_usd double precision NOT NULL DEFAULT 0,
    ADD COLUMN spend_stopped text NOT NULL DEFAULT '';

-- A run is known by the id the console gives it (run_key); the newest has the greatest
-- position.
ALTER TABLE job_runs DROP CONSTRAINT job_runs_agent_id_fkey;
ALTER TABLE job_runs
    ADD COLUMN run_key text NOT NULL DEFAULT '',
    ADD COLUMN position bigint NOT NULL DEFAULT 0,
    ADD COLUMN kind text NOT NULL DEFAULT '',
    ADD COLUMN label text NOT NULL DEFAULT '',
    ALTER COLUMN agent_id DROP NOT NULL,
    -- What started it.
    DROP COLUMN trigger,
    ADD COLUMN trigger_kind text NOT NULL DEFAULT '',
    ADD COLUMN trigger_what text NOT NULL DEFAULT '',
    ADD COLUMN trigger_set_by text NOT NULL DEFAULT '',
    ADD COLUMN trigger_by text NOT NULL DEFAULT '',
    ADD COLUMN trigger_slot_at timestamptz,
    ADD COLUMN trigger_event text NOT NULL DEFAULT '',
    ADD COLUMN trigger_key text NOT NULL DEFAULT '',
    ADD COLUMN trigger_is_rebuild boolean NOT NULL DEFAULT false,
    ADD COLUMN repeats integer NOT NULL DEFAULT 0,
    ADD COLUMN last_seen_at timestamptz,
    ADD COLUMN fingerprint text NOT NULL DEFAULT '',
    ADD COLUMN ref text NOT NULL DEFAULT '',
    ADD COLUMN note text NOT NULL DEFAULT '',
    -- Where its findings went, counted.
    DROP COLUMN routed,
    ADD COLUMN routed_opened integer NOT NULL DEFAULT 0,
    ADD COLUMN routed_seen integer NOT NULL DEFAULT 0,
    ADD COLUMN routed_known integer NOT NULL DEFAULT 0,
    ADD COLUMN routed_excepted integer NOT NULL DEFAULT 0,
    ADD COLUMN routed_reopened integer NOT NULL DEFAULT 0,
    ADD COLUMN routed_regression integer NOT NULL DEFAULT 0,
    ADD COLUMN routed_verified integer NOT NULL DEFAULT 0,
    ADD COLUMN routed_capped integer NOT NULL DEFAULT 0,
    ADD COLUMN routed_invalid integer NOT NULL DEFAULT 0,
    ADD COLUMN routed_ignored integer NOT NULL DEFAULT 0,
    ADD COLUMN routed_expected integer NOT NULL DEFAULT 0,
    ADD COLUMN verified_cases text[] NOT NULL DEFAULT '{}',
    ADD COLUMN new_checks text[] NOT NULL DEFAULT '{}',
    ADD COLUMN failed_cluster_ids text[] NOT NULL DEFAULT '{}',
    -- The findings left out of the recheck for its cap, when any were (has_left_out).
    ADD COLUMN has_left_out boolean NOT NULL DEFAULT false,
    ADD COLUMN left_out_count integer NOT NULL DEFAULT 0,
    ADD COLUMN left_out_cap integer NOT NULL DEFAULT 0,
    ADD COLUMN left_out_critical integer NOT NULL DEFAULT 0,
    ADD COLUMN left_out_warning integer NOT NULL DEFAULT 0,
    ADD COLUMN left_out_info integer NOT NULL DEFAULT 0,
    ADD COLUMN not_rechecked integer NOT NULL DEFAULT 0,
    ADD COLUMN tokens_in integer NOT NULL DEFAULT 0,
    ADD COLUMN tokens_out integer NOT NULL DEFAULT 0,
    ADD COLUMN not_routed text NOT NULL DEFAULT '',
    ADD COLUMN is_reroute boolean NOT NULL DEFAULT false,
    ADD COLUMN instance text NOT NULL DEFAULT '',
    ADD COLUMN is_without_findings boolean NOT NULL DEFAULT false,
    DROP COLUMN facts;
ALTER TABLE job_runs ADD CONSTRAINT job_runs_agent_id_fkey
    FOREIGN KEY (agent_id) REFERENCES agents (agent_id) ON DELETE RESTRICT;
CREATE UNIQUE INDEX job_runs_run_key_key ON job_runs (run_key);
CREATE INDEX job_runs_position_idx ON job_runs (position);

CREATE TABLE job_run_facts (
    job_run_id uuid NOT NULL REFERENCES job_runs (job_run_id) ON DELETE CASCADE,
    position integer NOT NULL,
    label text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    href text NOT NULL DEFAULT '',
    tone text NOT NULL DEFAULT '',
    PRIMARY KEY (job_run_id, position)
);
COMMENT ON TABLE job_run_facts IS 'The facts shown in a run''s panel, in order';

CREATE TABLE job_run_proposals (
    job_run_id uuid NOT NULL REFERENCES job_runs (job_run_id) ON DELETE CASCADE,
    position integer NOT NULL,
    -- The proposed change's id, as the Changes area knows it.
    card_key text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    -- The cases it says it resolves.
    resolves text[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (job_run_id, position)
);
COMMENT ON TABLE job_run_proposals IS 'The changes a run proposed, in order';

CREATE TABLE job_run_proposal_cases (
    job_run_id uuid NOT NULL,
    position integer NOT NULL,
    case_key text NOT NULL,
    title text NOT NULL DEFAULT '',
    PRIMARY KEY (job_run_id, position, case_key),
    FOREIGN KEY (job_run_id, position) REFERENCES job_run_proposals (job_run_id, position) ON DELETE CASCADE
);
COMMENT ON TABLE job_run_proposal_cases IS 'The titles, when the run ended, of the cases a proposed change resolves';

-- A finding names its check, case, known issue and exception by their own ids: a check
-- need not be a rule, and the three have not moved yet.
ALTER TABLE findings DROP CONSTRAINT findings_validation_rule_id_fkey;
DROP INDEX findings_validation_rule_id_idx;
ALTER TABLE findings
    DROP COLUMN validation_rule_id,
    DROP COLUMN case_id,
    DROP COLUMN known_issue_id,
    DROP COLUMN exception_id,
    ADD COLUMN position integer NOT NULL DEFAULT 0,
    ADD COLUMN finding_key text NOT NULL DEFAULT '',
    ADD COLUMN detail text NOT NULL DEFAULT '',
    ADD COLUMN evidence text[] NOT NULL DEFAULT '{}',
    -- What the finding is about, when it says (has_key): cluster, object, check.
    ADD COLUMN has_key boolean NOT NULL DEFAULT false,
    ADD COLUMN key_cluster_id text NOT NULL DEFAULT '',
    ADD COLUMN key_object text NOT NULL DEFAULT '',
    ADD COLUMN key_check text NOT NULL DEFAULT '',
    ADD COLUMN invalid_reason text NOT NULL DEFAULT '',
    ADD COLUMN is_unverified boolean NOT NULL DEFAULT false,
    ADD COLUMN case_key text NOT NULL DEFAULT '',
    ADD COLUMN known_issue_key text NOT NULL DEFAULT '',
    ADD COLUMN exception_key text NOT NULL DEFAULT '',
    ADD COLUMN is_new_check boolean NOT NULL DEFAULT false,
    ADD COLUMN expected_why text NOT NULL DEFAULT '',
    ADD COLUMN check_why text NOT NULL DEFAULT '';
