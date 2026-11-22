-- A provider's credential is in the Secret its id names (DB-81): nothing ever wrote
-- ai_providers.secret_ref, and it goes.
ALTER TABLE ai_providers DROP COLUMN secret_ref;
