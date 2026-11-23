-- The models a provider offers its agents (phase-13.1-ai, A9; decided by the owner on
-- November 23, 2026). One row a model, in the order the provider offers them, with
-- its tier for Auto ('' none: Auto does not pick it). Whether a SuperAdmin chose
-- them, marked on the provider's row: a provider none were chosen for (every row
-- already there) offers every model it lists, as before this step, and Auto picks on
-- a provider of kind Anthropic among Claude Haiku, Sonnet and Opus 5.5 as before.

CREATE TABLE ai_provider_models (
    ai_provider_model_id uuid PRIMARY KEY,
    ai_provider_id text NOT NULL REFERENCES ai_providers (ai_provider_id) ON DELETE CASCADE,
    model text NOT NULL,
    tier text NOT NULL DEFAULT '' CHECK (tier IN ('', 'light', 'standard', 'strong')),
    position integer NOT NULL,
    UNIQUE (ai_provider_id, model),
    UNIQUE (ai_provider_id, position)
);
COMMENT ON TABLE ai_provider_models IS 'The models a provider offers its agents, in order, each with its tier for Auto';

ALTER TABLE ai_providers ADD COLUMN is_models_chosen boolean NOT NULL DEFAULT false;
