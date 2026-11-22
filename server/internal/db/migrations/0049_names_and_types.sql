-- Names that say what a column holds, and one type for one kind of value (DB-93).
--
-- An acknowledgement names its run, case or exception by the id people read, as text,
-- never a key: the columns end in _key. A proposal of a job's run names a proposed change:
-- change_key. A flag starts with is_, a time ends in _at, a list of other records' ids
-- ends in _ids, and a column named for a time but holding something else says what it
-- holds; in a group of columns the group's name comes first (tested_is_ok). The lists on
-- the cases' own tables keep their names here (verify_jobs, rejected, similar_numbers):
-- they go with the cases' tables.
ALTER TABLE alert_acks RENAME COLUMN job_run_id TO job_run_key;
ALTER TABLE alert_acks RENAME COLUMN case_id TO case_key;
ALTER TABLE alert_acks RENAME COLUMN exception_id TO exception_key;
ALTER TABLE job_run_proposals RENAME COLUMN card_key TO change_key;
ALTER TABLE job_run_proposals RENAME COLUMN resolves TO resolved_case_ids;
ALTER TABLE job_runs RENAME COLUMN verified_cases TO verified_case_ids;
ALTER TABLE ai_providers RENAME COLUMN tested_ok TO tested_is_ok;
ALTER TABLE jobs RENAME COLUMN skips_overlap TO is_overlap_skipped;
ALTER TABLE jobs RENAME COLUMN catches_up TO is_catching_up;
-- A time of day, "02:00", in the schedule's time zone.
ALTER TABLE jobs RENAME COLUMN schedule_at TO schedule_time_of_day;
ALTER TABLE prompt_versions RENAME COLUMN pilot_until TO pilot_ends_at;
-- When a standing approval approved, the newest last, for its limit an hour.
ALTER TABLE approval_policies RENAME COLUMN applied_at TO applied_times;
ALTER TABLE fixes RENAME COLUMN contradicts TO contradicted_fix_ids;
ALTER TABLE fixes RENAME COLUMN accepted TO accepted_fix_ids;

-- Money is numeric(14,6), rounded to a millionth of a dollar, as a test's and a run's
-- cost are. The spend ledger (ai_spend.usd) and a job's day spend stay floats, as the
-- service adds them (decided in Phase 13).
ALTER TABLE chat_messages ALTER COLUMN cost_usd TYPE numeric(14,6);
ALTER TABLE knowledge_syncs ALTER COLUMN cost_usd TYPE numeric(14,6);

-- No pin is '', as in credentials and repositories.
UPDATE knowledge_sources SET pinned_cert_sha256 = '' WHERE pinned_cert_sha256 IS NULL;
ALTER TABLE knowledge_sources ALTER COLUMN pinned_cert_sha256 SET DEFAULT '', ALTER COLUMN pinned_cert_sha256 SET NOT NULL;
