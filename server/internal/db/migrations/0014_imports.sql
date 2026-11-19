-- What was copied from the ConfigMaps, once each (DB-17): the console looks here before
-- it reads a ConfigMap's key again, so records deleted in the database do not come back.
CREATE TABLE imports (
    import_id text PRIMARY KEY,
    source text NOT NULL DEFAULT '',
    record_count integer NOT NULL DEFAULT 0,
    imported_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE imports IS 'Record sets copied from the ConfigMaps into the database, by the key they were kept under';
