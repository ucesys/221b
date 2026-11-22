-- A case's links, an RCA's cases and a known issue's services are tables with keys
-- (DB-89), in place of four number columns and an array of numbers on cases, an array of
-- case ids on rcas and an array of service ids on known_issues. Each row keys to what it
-- names while that has a row, and is emptied when it goes; the number or the id people
-- read is kept beside the key, since the page shows it after its case or service is gone.

-- The other cases a case names: the original of a duplicate, the case it is a regression
-- of, the case it is the same as, the case it may be a duplicate of, and the similar ones.
CREATE TABLE case_links (
    case_id uuid NOT NULL REFERENCES cases (case_id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('duplicate_of', 'regression_of', 'same_as', 'possible_duplicate_of', 'similar')),
    position integer NOT NULL,
    linked_case_id uuid REFERENCES cases (case_id) ON DELETE SET NULL,
    linked_case_number integer NOT NULL,
    PRIMARY KEY (case_id, kind, position)
);
CREATE INDEX case_links_linked_case_id_idx ON case_links (linked_case_id);
CREATE INDEX case_links_linked_case_number_idx ON case_links (linked_case_number) WHERE linked_case_id IS NULL;
COMMENT ON TABLE case_links IS 'The other cases a case names: duplicate of, regression of, same as, possible duplicate of, similar';

INSERT INTO case_links (case_id, kind, position, linked_case_id, linked_case_number)
SELECT c.case_id, l.kind, 0, (SELECT t.case_id FROM cases t WHERE t.object_name = l.n::text), l.n
FROM cases c CROSS JOIN LATERAL (VALUES ('duplicate_of', c.duplicate_of_number), ('regression_of', c.regression_of_number), ('same_as', c.same_as_number),
    ('possible_duplicate_of', c.possible_duplicate_of_number)) AS l (kind, n)
WHERE l.n <> 0;

INSERT INTO case_links (case_id, kind, position, linked_case_id, linked_case_number)
SELECT c.case_id, 'similar', s.ord - 1, (SELECT t.case_id FROM cases t WHERE t.object_name = s.n::text), s.n
FROM cases c CROSS JOIN LATERAL unnest(c.similar_numbers) WITH ORDINALITY AS s (n, ord);

ALTER TABLE cases DROP COLUMN regression_of_number, DROP COLUMN same_as_number, DROP COLUMN possible_duplicate_of_number,
    DROP COLUMN duplicate_of_number, DROP COLUMN similar_numbers;

-- The other cases an RCA covers, closed as fixed with its case at once.
CREATE TABLE rca_cases (
    case_id uuid NOT NULL REFERENCES rcas (case_id) ON DELETE CASCADE,
    position integer NOT NULL,
    linked_case_id uuid REFERENCES cases (case_id) ON DELETE SET NULL,
    -- The case as people read it ("C-12").
    linked_case_key text NOT NULL,
    PRIMARY KEY (case_id, position)
);
CREATE INDEX rca_cases_linked_case_id_idx ON rca_cases (linked_case_id);
COMMENT ON TABLE rca_cases IS 'The other cases an RCA covers';

INSERT INTO rca_cases (case_id, position, linked_case_id, linked_case_key)
SELECT r.case_id, x.ord - 1, (SELECT t.case_id FROM cases t WHERE x.id LIKE 'C-%' AND t.object_name = substr(x.id, 3)), x.id
FROM rcas r CROSS JOIN LATERAL unnest(r.cases) WITH ORDINALITY AS x (id, ord);

ALTER TABLE rcas DROP COLUMN cases;

-- The services a known issue affects.
CREATE TABLE known_issue_services (
    known_issue_id uuid NOT NULL REFERENCES known_issues (known_issue_id) ON DELETE CASCADE,
    position integer NOT NULL,
    service_id text REFERENCES services (service_id) ON DELETE SET NULL,
    -- The service's id, kept when the service goes.
    service_key text NOT NULL,
    PRIMARY KEY (known_issue_id, position)
);
CREATE INDEX known_issue_services_service_id_idx ON known_issue_services (service_id);
COMMENT ON TABLE known_issue_services IS 'The services a known issue affects';

INSERT INTO known_issue_services (known_issue_id, position, service_id, service_key)
SELECT k.known_issue_id, s.ord - 1, (SELECT v.service_id FROM services v WHERE v.service_id = s.id), s.id
FROM known_issues k CROSS JOIN LATERAL unnest(k.services) WITH ORDINALITY AS s (id, ord);

ALTER TABLE known_issues DROP COLUMN services;
