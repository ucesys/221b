-- AI configuration's providers and agents with every field a column (DB-39). The tables
-- of step 0007 held no rows until this step.

-- A provider is known by the id the console gives it, which agents and Secrets name.
ALTER TABLE agents DROP CONSTRAINT agents_ai_provider_id_fkey;
ALTER TABLE ai_spend DROP CONSTRAINT ai_spend_ai_provider_id_fkey;
ALTER TABLE ai_providers ALTER COLUMN ai_provider_id TYPE text USING ai_provider_id::text;
ALTER TABLE agents ALTER COLUMN ai_provider_id TYPE text USING ai_provider_id::text;
ALTER TABLE ai_spend ALTER COLUMN ai_provider_id TYPE text USING ai_provider_id::text;
COMMENT ON COLUMN ai_spend.ai_provider_id IS 'The provider''s id; not a key, since the default provider may have no row and what was spent stays counted when a provider goes';
COMMENT ON COLUMN agents.ai_provider_id IS 'The provider''s id as the agent names it, none for the default provider; not a key, since the default provider may have no row';

ALTER TABLE ai_providers
    -- Its place in the list.
    ADD COLUMN position integer NOT NULL DEFAULT 0,
    -- How the console signs its requests, and how a header credential is sent.
    ADD COLUMN auth text NOT NULL DEFAULT '',
    ADD COLUMN auth_header text NOT NULL DEFAULT '',
    ADD COLUMN auth_prefix text NOT NULL DEFAULT '',
    -- Added to every request's address.
    ADD COLUMN query text NOT NULL DEFAULT '',
    ADD COLUMN region text NOT NULL DEFAULT '',
    ADD COLUMN project text NOT NULL DEFAULT '',
    -- A PEM bundle the endpoint's certificate must chain to; empty is the system's.
    ADD COLUMN ca_cert text NOT NULL DEFAULT '',
    -- The last connection test (tested_at): whether it passed, with which model, whether
    -- without a credential, the connection settings it ran with, and what went wrong.
    ADD COLUMN tested_ok boolean NOT NULL DEFAULT false,
    ADD COLUMN tested_model text NOT NULL DEFAULT '',
    ADD COLUMN tested_is_keyless boolean NOT NULL DEFAULT false,
    ADD COLUMN tested_conn text NOT NULL DEFAULT '',
    ADD COLUMN tested_error text NOT NULL DEFAULT '',
    -- What every agent may spend on it together; 0 is no limit.
    ADD COLUMN daily_limit_usd numeric(14,6) NOT NULL DEFAULT 0,
    ADD COLUMN monthly_limit_usd numeric(14,6) NOT NULL DEFAULT 0,
    -- The provider agents that name none use.
    ADD COLUMN is_default boolean NOT NULL DEFAULT false,
    -- Only of the row 'default', the provider Sherlock had before Phase 12, whose
    -- endpoint and model are Sherlock's settings: it was removed.
    ADD COLUMN is_removed boolean NOT NULL DEFAULT false;
COMMENT ON TABLE ai_providers IS 'Model providers: endpoint, how to reach it, model, prices, context window, the last test and the limits';

CREATE TABLE ai_provider_headers (
    ai_provider_id text NOT NULL REFERENCES ai_providers (ai_provider_id) ON DELETE CASCADE,
    name text NOT NULL,
    value text NOT NULL DEFAULT '',
    PRIMARY KEY (ai_provider_id, name)
);
COMMENT ON TABLE ai_provider_headers IS 'Headers sent with every request to a provider; they are not secret';

ALTER TABLE agents
    -- None: the catalog's default.
    ALTER COLUMN is_enabled DROP NOT NULL,
    ALTER COLUMN is_enabled DROP DEFAULT,
    -- The hash of the agent's baseline instructions last tested at start.
    ADD COLUMN baseline_tested text NOT NULL DEFAULT '';
COMMENT ON TABLE agents IS 'The built-in agents, keyed by catalog id, with what an administrator chose for each: provider, model, budgets, clusters, tools';
