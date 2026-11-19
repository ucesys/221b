-- AI configuration (docs/data-model.md). Keys are UUIDv7 made by the console unless the
-- model names a natural key; every key is named for its table.

CREATE TABLE ai_providers (
    ai_provider_id uuid PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    endpoint text NOT NULL DEFAULT '',
    model text NOT NULL DEFAULT '',
    price_in_usd numeric(14,6) NOT NULL DEFAULT 0,
    price_out_usd numeric(14,6) NOT NULL DEFAULT 0,
    context_window integer NOT NULL DEFAULT 0,
    secret_ref text NOT NULL DEFAULT '',
    tested_at timestamptz
);
COMMENT ON TABLE ai_providers IS 'Model providers: endpoint, model, prices, context window';

CREATE TABLE agents (
    agent_id text PRIMARY KEY,
    ai_provider_id uuid,
    model text NOT NULL DEFAULT '',
    is_enabled boolean NOT NULL DEFAULT false,
    daily_usd numeric(14,6) NOT NULL DEFAULT 0,
    monthly_usd numeric(14,6) NOT NULL DEFAULT 0,
    cluster_ids text[] NOT NULL DEFAULT '{}',
    tools text[] NOT NULL DEFAULT '{}',
    context_window integer NOT NULL DEFAULT 0,
    compact_mode text NOT NULL DEFAULT ''
);
COMMENT ON TABLE agents IS 'Settings of the nine built-in agents, keyed by catalog id: provider, model, budgets, clusters, tools';

CREATE TABLE ai_spend (
    ai_spend_id uuid PRIMARY KEY,
    agent_id text NOT NULL,
    ai_provider_id uuid,
    provider_name text NOT NULL DEFAULT '',
    spent_at timestamptz NOT NULL DEFAULT now(),
    usd text NOT NULL DEFAULT '',
    tokens_in integer NOT NULL DEFAULT 0,
    tokens_out integer NOT NULL DEFAULT 0
);
COMMENT ON TABLE ai_spend IS 'Insert-only spend rows per call, summed when read';

CREATE TABLE prompt_versions (
    prompt_version_id uuid PRIMARY KEY,
    agent_id text NOT NULL,
    version_number integer NOT NULL DEFAULT 0,
    state text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    notes jsonb,
    pilot jsonb,
    why text NOT NULL DEFAULT '',
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    approval_request_id uuid
);
COMMENT ON TABLE prompt_versions IS 'Prompt versions per agent with a state, from draft to live';

CREATE TABLE test_cases (
    test_case_id uuid PRIMARY KEY,
    agent_id text NOT NULL,
    name text NOT NULL DEFAULT '',
    question text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '',
    recorded jsonb,
    expect jsonb,
    is_built_in boolean NOT NULL DEFAULT false,
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE test_cases IS 'Each agent''s test suite: questions with recorded tool results and the expected behaviour';

CREATE TABLE pilot_entries (
    pilot_entry_id uuid PRIMARY KEY,
    prompt_version_id uuid NOT NULL,
    asked_by text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '',
    question text NOT NULL DEFAULT '',
    answer text NOT NULL DEFAULT '',
    model text NOT NULL DEFAULT '',
    cost_usd numeric(14,6) NOT NULL DEFAULT 0,
    asked_at timestamptz
);
COMMENT ON TABLE pilot_entries IS 'Questions answered by a version in pilot';

CREATE TABLE test_runs (
    test_run_id uuid PRIMARY KEY,
    prompt_version_id uuid NOT NULL,
    state text NOT NULL DEFAULT '',
    case_count integer NOT NULL DEFAULT 0,
    passed_count integer NOT NULL DEFAULT 0,
    estimate_usd numeric(14,6) NOT NULL DEFAULT 0,
    cost_usd numeric(14,6) NOT NULL DEFAULT 0,
    stopped_at_case integer,
    started_by text NOT NULL DEFAULT '',
    started_at timestamptz,
    ended_at timestamptz,
    error text NOT NULL DEFAULT ''
);
COMMENT ON TABLE test_runs IS 'One run of an agent''s whole suite against one prompt version: state, counts, cost';

CREATE TABLE test_case_results (
    test_case_result_id uuid PRIMARY KEY,
    test_run_id uuid NOT NULL,
    test_case_id uuid,
    case_name text NOT NULL DEFAULT '',
    question text NOT NULL DEFAULT '',
    expect jsonb,
    is_passed boolean NOT NULL DEFAULT false,
    answer text NOT NULL DEFAULT '',
    checks jsonb,
    cost_usd numeric(14,6) NOT NULL DEFAULT 0
);
COMMENT ON TABLE test_case_results IS 'Result of each case in a test run, with a snapshot of the case';

CREATE TABLE test_run_replays (
    test_run_replay_id uuid PRIMARY KEY,
    test_run_id uuid NOT NULL,
    cluster_id text NOT NULL DEFAULT '',
    question text NOT NULL DEFAULT '',
    answer_before text NOT NULL DEFAULT '',
    answer_after text NOT NULL DEFAULT '',
    is_same boolean NOT NULL DEFAULT false,
    cost_usd numeric(14,6) NOT NULL DEFAULT 0,
    error text NOT NULL DEFAULT ''
);
COMMENT ON TABLE test_run_replays IS 'Production questions replayed against the version: answer before and after';

-- Keys.
ALTER TABLE agents ADD CONSTRAINT agents_ai_provider_id_fkey
    FOREIGN KEY (ai_provider_id) REFERENCES ai_providers (ai_provider_id) ON DELETE SET NULL;
CREATE INDEX agents_ai_provider_id_idx ON agents (ai_provider_id);
ALTER TABLE ai_spend ADD CONSTRAINT ai_spend_agent_id_fkey
    FOREIGN KEY (agent_id) REFERENCES agents (agent_id) ON DELETE RESTRICT;
CREATE INDEX ai_spend_agent_id_idx ON ai_spend (agent_id);
ALTER TABLE ai_spend ADD CONSTRAINT ai_spend_ai_provider_id_fkey
    FOREIGN KEY (ai_provider_id) REFERENCES ai_providers (ai_provider_id) ON DELETE SET NULL;
CREATE INDEX ai_spend_ai_provider_id_idx ON ai_spend (ai_provider_id);
ALTER TABLE prompt_versions ADD CONSTRAINT prompt_versions_agent_id_fkey
    FOREIGN KEY (agent_id) REFERENCES agents (agent_id) ON DELETE RESTRICT;
CREATE INDEX prompt_versions_agent_id_idx ON prompt_versions (agent_id);
ALTER TABLE prompt_versions ADD CONSTRAINT prompt_versions_approval_request_id_fkey
    FOREIGN KEY (approval_request_id) REFERENCES approval_requests (approval_request_id) ON DELETE SET NULL;
CREATE INDEX prompt_versions_approval_request_id_idx ON prompt_versions (approval_request_id);
ALTER TABLE test_cases ADD CONSTRAINT test_cases_agent_id_fkey
    FOREIGN KEY (agent_id) REFERENCES agents (agent_id) ON DELETE RESTRICT;
CREATE INDEX test_cases_agent_id_idx ON test_cases (agent_id);
ALTER TABLE pilot_entries ADD CONSTRAINT pilot_entries_prompt_version_id_fkey
    FOREIGN KEY (prompt_version_id) REFERENCES prompt_versions (prompt_version_id) ON DELETE CASCADE;
CREATE INDEX pilot_entries_prompt_version_id_idx ON pilot_entries (prompt_version_id);
ALTER TABLE test_runs ADD CONSTRAINT test_runs_prompt_version_id_fkey
    FOREIGN KEY (prompt_version_id) REFERENCES prompt_versions (prompt_version_id) ON DELETE CASCADE;
CREATE INDEX test_runs_prompt_version_id_idx ON test_runs (prompt_version_id);
ALTER TABLE test_case_results ADD CONSTRAINT test_case_results_test_run_id_fkey
    FOREIGN KEY (test_run_id) REFERENCES test_runs (test_run_id) ON DELETE CASCADE;
CREATE INDEX test_case_results_test_run_id_idx ON test_case_results (test_run_id);
ALTER TABLE test_case_results ADD CONSTRAINT test_case_results_test_case_id_fkey
    FOREIGN KEY (test_case_id) REFERENCES test_cases (test_case_id) ON DELETE SET NULL;
CREATE INDEX test_case_results_test_case_id_idx ON test_case_results (test_case_id);
ALTER TABLE test_run_replays ADD CONSTRAINT test_run_replays_test_run_id_fkey
    FOREIGN KEY (test_run_id) REFERENCES test_runs (test_run_id) ON DELETE CASCADE;
CREATE INDEX test_run_replays_test_run_id_idx ON test_run_replays (test_run_id);
