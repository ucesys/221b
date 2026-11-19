-- A record set that only grows (the audit log, the Toolbox's session records) is copied
-- again from where the last copy stopped: what a console rolled back to a release without
-- a database wrote to the ConfigMap comes over at the next start with one (DB-17).
ALTER TABLE imports ADD COLUMN newest_at timestamptz;
COMMENT ON COLUMN imports.newest_at IS 'The time of the newest record copied, for a record set that only grows; a later copy takes what is newer';
