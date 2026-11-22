-- What the agents spend as rows that are only ever added, one a cost, summed when read
-- (DB-41). The table of step 0007 held no rows until this step.
ALTER TABLE ai_spend
    -- None: no agent's (a cost counted before the agents, or the total carried over).
    ALTER COLUMN agent_id DROP NOT NULL,
    -- The engine counts what a call cost, not its tokens, and knows a provider by its id.
    DROP COLUMN provider_name,
    DROP COLUMN tokens_in,
    DROP COLUMN tokens_out,
    ALTER COLUMN usd DROP DEFAULT,
    ALTER COLUMN usd TYPE double precision USING (CASE WHEN usd = '' THEN 0 ELSE usd::double precision END),
    ALTER COLUMN usd SET DEFAULT 0,
    -- The questions the cost counts as: 1 for the first call of a question, 0 for the
    -- calls that follow it; many in a row carried over.
    ADD COLUMN question_count integer NOT NULL DEFAULT 0,
    -- A share carried over from the sums the ConfigMap, or a backup, held: counted in its
    -- agent's or provider's share and not in the total, which has a row of its own.
    ADD COLUMN is_share_only boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN ai_spend.usd IS 'What the call cost in US dollars, as counted from the token use the API reports or estimated; a sum of estimates, not an invoice';
