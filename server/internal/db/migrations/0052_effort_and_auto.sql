-- The agents' effort and Auto (phase-13.1-ai, part A; decided by the owner on
-- November 22, 2026). An agent's effort ('' its model's default, a level, or
-- 'auto') and the lowest effort and model Auto may pick for it ('' none). The
-- provider the classifier asks, marked on its row (none marked: no classifier).
-- What a job's run ran with and how it was picked, and a model moved up in it;
-- the same of a kept conversation's answer. The rows already there ran with
-- their model's default effort and were picked by nobody: '' everywhere.
ALTER TABLE agents ADD COLUMN effort text NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN floor_effort text NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN floor_model text NOT NULL DEFAULT '';
ALTER TABLE ai_providers ADD COLUMN is_classifier boolean NOT NULL DEFAULT false;
ALTER TABLE job_runs ADD COLUMN ai_provider_name text NOT NULL DEFAULT '';
ALTER TABLE job_runs ADD COLUMN effort text NOT NULL DEFAULT '';
ALTER TABLE job_runs ADD COLUMN pick_method text NOT NULL DEFAULT '';
ALTER TABLE job_runs ADD COLUMN pick_why text NOT NULL DEFAULT '';
ALTER TABLE job_runs ADD COLUMN moved_up_step integer NOT NULL DEFAULT 0;
ALTER TABLE job_runs ADD COLUMN moved_up_from_model text NOT NULL DEFAULT '';
ALTER TABLE job_runs ADD COLUMN moved_up_why text NOT NULL DEFAULT '';
ALTER TABLE chat_messages ADD COLUMN effort text NOT NULL DEFAULT '';
ALTER TABLE chat_messages ADD COLUMN pick_method text NOT NULL DEFAULT '';
ALTER TABLE chat_messages ADD COLUMN pick_why text NOT NULL DEFAULT '';
ALTER TABLE chat_messages ADD COLUMN pick_cost_usd numeric(14,6) NOT NULL DEFAULT 0;
ALTER TABLE chat_messages ADD COLUMN moved_up_from_model text NOT NULL DEFAULT '';
