-- Jobs (docs/data-model.md). Keys are UUIDv7 made by the console unless the
-- model names a natural key; every key is named for its table.

CREATE TABLE jobs (
    job_id uuid PRIMARY KEY,
    kind text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    task text NOT NULL DEFAULT '',
    schedule jsonb,
    cluster_ids text[] NOT NULL DEFAULT '{}',
    run_usd numeric(14,6) NOT NULL DEFAULT 0,
    day_usd numeric(14,6) NOT NULL DEFAULT 0,
    min_priority integer NOT NULL DEFAULT 0,
    is_enabled boolean NOT NULL DEFAULT false,
    is_added boolean NOT NULL DEFAULT false,
    set_by text NOT NULL DEFAULT '',
    set_at timestamptz
);
COMMENT ON TABLE jobs IS 'Job definitions: kind, task, schedule, clusters, budget';

CREATE TABLE job_runs (
    job_run_id uuid PRIMARY KEY,
    job_id uuid NOT NULL,
    agent_id text NOT NULL,
    model text NOT NULL DEFAULT '',
    trigger jsonb,
    subject text NOT NULL DEFAULT '',
    severity text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT '',
    cluster_ids text[] NOT NULL DEFAULT '{}',
    started_at timestamptz,
    ended_at timestamptz,
    cost_usd numeric(14,6) NOT NULL DEFAULT 0,
    report text NOT NULL DEFAULT '',
    facts jsonb,
    routed jsonb
);
COMMENT ON TABLE job_runs IS 'Runs: trigger, agent, model, status, report, cost, and the facts shown in the run''s panel';

CREATE TABLE findings (
    finding_id uuid PRIMARY KEY,
    job_run_id uuid NOT NULL,
    validation_rule_id text,
    check_name text NOT NULL DEFAULT '',
    severity text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '',
    resource text NOT NULL DEFAULT '',
    routing text NOT NULL DEFAULT '',
    case_id uuid,
    known_issue_id uuid,
    exception_id uuid,
    is_expected boolean NOT NULL DEFAULT false,
    confidence text NOT NULL DEFAULT ''
);
COMMENT ON TABLE findings IS 'Findings of a run: check, severity, resource, and the case, known issue or exception it went to';

CREATE TABLE schedule_claims (
    job_id uuid NOT NULL,
    slot_at timestamptz NOT NULL,
    claimed_by text NOT NULL DEFAULT '',
    claimed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, slot_at)
);
COMMENT ON TABLE schedule_claims IS 'One row per scheduled time, so each time runs once';

CREATE TABLE cluster_checks (
    cluster_id text NOT NULL,
    check_name text NOT NULL,
    first_seen_at timestamptz,
    last_seen_at timestamptz,
    finding_count integer NOT NULL DEFAULT 0,
    sample_title text NOT NULL DEFAULT '',
    PRIMARY KEY (cluster_id, check_name)
);
COMMENT ON TABLE cluster_checks IS 'Checks seen per cluster, to tell a new one and to suggest rules';

CREATE TABLE validation_rules (
    validation_rule_id text PRIMARY KEY,
    says text NOT NULL DEFAULT '',
    case_type text NOT NULL DEFAULT '',
    job_kinds text[] NOT NULL DEFAULT '{}',
    control text NOT NULL DEFAULT '',
    is_built_in boolean NOT NULL DEFAULT false,
    is_edited boolean NOT NULL DEFAULT false,
    is_auto boolean NOT NULL DEFAULT false,
    state text NOT NULL DEFAULT '',
    replaced_by_validation_rule_id text,
    updated_by text NOT NULL DEFAULT '',
    updated_at timestamptz
);
COMMENT ON TABLE validation_rules IS 'Validation rules, keyed by check id, offered to job kinds';

-- Keys.
ALTER TABLE job_runs ADD CONSTRAINT job_runs_job_id_fkey
    FOREIGN KEY (job_id) REFERENCES jobs (job_id) ON DELETE CASCADE;
CREATE INDEX job_runs_job_id_idx ON job_runs (job_id);
ALTER TABLE job_runs ADD CONSTRAINT job_runs_agent_id_fkey
    FOREIGN KEY (agent_id) REFERENCES agents (agent_id) ON DELETE RESTRICT;
CREATE INDEX job_runs_agent_id_idx ON job_runs (agent_id);
ALTER TABLE findings ADD CONSTRAINT findings_job_run_id_fkey
    FOREIGN KEY (job_run_id) REFERENCES job_runs (job_run_id) ON DELETE CASCADE;
CREATE INDEX findings_job_run_id_idx ON findings (job_run_id);
ALTER TABLE findings ADD CONSTRAINT findings_validation_rule_id_fkey
    FOREIGN KEY (validation_rule_id) REFERENCES validation_rules (validation_rule_id) ON DELETE SET NULL;
CREATE INDEX findings_validation_rule_id_idx ON findings (validation_rule_id);
ALTER TABLE schedule_claims ADD CONSTRAINT schedule_claims_job_id_fkey
    FOREIGN KEY (job_id) REFERENCES jobs (job_id) ON DELETE CASCADE;
ALTER TABLE validation_rules ADD CONSTRAINT validation_rules_replaced_by_validation_rule_id_fkey
    FOREIGN KEY (replaced_by_validation_rule_id) REFERENCES validation_rules (validation_rule_id) ON DELETE SET NULL;
CREATE INDEX validation_rules_replaced_by_validation_rule_id_idx ON validation_rules (replaced_by_validation_rule_id);
