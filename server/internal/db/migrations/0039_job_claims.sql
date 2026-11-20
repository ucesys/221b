-- A job's scheduled time is claimed through schedule_claims (DB-80): the replica whose
-- insert of (job, time) goes in runs it, and the newest of a job's claims is the time it
-- last handled. jobs.handled_at held that time until this step: each is a claim now, and
-- the column goes. A row of jobs that held only that time holds nothing, and goes too.
INSERT INTO schedule_claims (job_id, slot_at, claimed_by)
    SELECT job_id, handled_at, '' FROM jobs WHERE handled_at IS NOT NULL
    ON CONFLICT DO NOTHING;
DELETE FROM jobs WHERE NOT is_kept AND spend_day IS NULL;
ALTER TABLE jobs DROP COLUMN handled_at;
COMMENT ON TABLE schedule_claims IS 'One row a scheduled time a job claimed, so each time runs once; the newest is the time the job last handled';
