-- The rule suggestions dismissed and the runs counted as rows, one an id (DB-43, decided
-- with the owner on November 19, 2026). Step 0026 kept both lists as JSON in settings
-- scopes; what a database made since holds there is carried over.
CREATE TABLE dismissed_checks (
    check_name text PRIMARY KEY,
    -- The order they were dismissed in: the oldest go first when the list is full.
    position integer NOT NULL DEFAULT 0
);
COMMENT ON TABLE dismissed_checks IS 'Check ids a person dismissed as rule suggestions, not suggested again';

CREATE TABLE counted_runs (
    run_key text PRIMARY KEY,
    position integer NOT NULL DEFAULT 0
);
COMMENT ON TABLE counted_runs IS 'The runs whose findings are counted in check_found, so none is counted twice; a run may be gone from job_runs';

INSERT INTO dismissed_checks (check_name, position)
    SELECT e.id, min(e.n)::integer FROM settings s, jsonb_array_elements_text(s.value -> 'dismissed') WITH ORDINALITY AS e(id, n)
    WHERE s.scope = 'case-rules' AND jsonb_typeof(s.value -> 'dismissed') = 'array' GROUP BY e.id;
DELETE FROM settings WHERE scope = 'case-rules';

INSERT INTO counted_runs (run_key, position)
    SELECT e.id, min(e.n)::integer FROM settings s, jsonb_array_elements_text(s.value -> 'counted') WITH ORDINALITY AS e(id, n)
    WHERE s.scope = 'case-found' AND jsonb_typeof(s.value -> 'counted') = 'array' GROUP BY e.id;
UPDATE settings SET value = value - 'counted' WHERE scope = 'case-found';
