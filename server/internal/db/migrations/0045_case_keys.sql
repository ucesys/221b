-- Keys in place of ids as text where the rows they name keep their ids now (DB-90): a
-- case, a change, a known issue and an exception each keep their row through every save.
-- Each key is emptied when what it names goes, and the id people read stays beside it,
-- since the page shows it after. A finding, a job's run and a job are not keyed: a run is
-- written again whole with a new row at each save, its findings with it, and a job has a
-- row only once it is changed.

-- A finding's case, known issue and exception.
ALTER TABLE findings
    ADD COLUMN case_id uuid REFERENCES cases (case_id) ON DELETE SET NULL,
    ADD COLUMN known_issue_id uuid REFERENCES known_issues (known_issue_id) ON DELETE SET NULL,
    ADD COLUMN exception_id uuid REFERENCES exceptions (exception_id) ON DELETE SET NULL;
UPDATE findings f SET
    case_id = (SELECT t.case_id FROM cases t WHERE f.case_key LIKE 'C-%' AND t.object_name = substr(f.case_key, 3)),
    known_issue_id = (SELECT k.known_issue_id FROM known_issues k WHERE k.known_issue_key = f.known_issue_key),
    exception_id = (SELECT e.exception_id FROM exceptions e WHERE e.exception_key = f.exception_key)
WHERE f.case_key <> '' OR f.known_issue_key <> '' OR f.exception_key <> '';
CREATE INDEX findings_case_id_idx ON findings (case_id);
CREATE INDEX findings_known_issue_id_idx ON findings (known_issue_id);
CREATE INDEX findings_exception_id_idx ON findings (exception_id);
-- What names a case, a known issue or an exception before it has a row takes its key
-- when it is made: these find them.
CREATE INDEX findings_case_key_idx ON findings (case_key) WHERE case_id IS NULL AND case_key <> '';
CREATE INDEX findings_known_issue_key_idx ON findings (known_issue_key) WHERE known_issue_id IS NULL AND known_issue_key <> '';
CREATE INDEX findings_exception_key_idx ON findings (exception_key) WHERE exception_id IS NULL AND exception_key <> '';

-- A change's resolved cases and known issues.
ALTER TABLE change_resolves
    ADD COLUMN resolved_case_id uuid REFERENCES cases (case_id) ON DELETE SET NULL,
    ADD COLUMN resolved_known_issue_id uuid REFERENCES known_issues (known_issue_id) ON DELETE SET NULL;
UPDATE change_resolves r SET
    resolved_case_id = (SELECT t.case_id FROM cases t WHERE r.resolved_key LIKE 'C-%' AND t.object_name = substr(r.resolved_key, 3)),
    resolved_known_issue_id = (SELECT k.known_issue_id FROM known_issues k WHERE k.known_issue_key = r.resolved_key);
CREATE INDEX change_resolves_resolved_case_id_idx ON change_resolves (resolved_case_id);
CREATE INDEX change_resolves_resolved_known_issue_id_idx ON change_resolves (resolved_known_issue_id);

-- An exception's case.
ALTER TABLE exceptions ADD COLUMN case_id uuid REFERENCES cases (case_id) ON DELETE SET NULL;
UPDATE exceptions e SET case_id = (SELECT t.case_id FROM cases t WHERE e.case_key LIKE 'C-%' AND t.object_name = substr(e.case_key, 3)) WHERE e.case_key <> '';
CREATE INDEX exceptions_case_id_idx ON exceptions (case_id);
CREATE INDEX exceptions_case_key_idx ON exceptions (case_key) WHERE case_id IS NULL AND case_key <> '';

-- A case's service, beside the service's id (service_key), which the case keeps when
-- the service goes.
ALTER TABLE cases ADD COLUMN service_id text REFERENCES services (service_id) ON DELETE SET NULL;
UPDATE cases c SET service_id = (SELECT s.service_id FROM services s WHERE s.service_id = c.service_key) WHERE c.service_key <> '';
CREATE INDEX cases_service_id_idx ON cases (service_id);

-- What the code looks a row up by is unique. A finding's id: a duplicate (which the
-- console never makes: an id is its run's and its place in it) keeps its row, its id
-- marked "~2", "~3" after the oldest's. A case's number: a duplicate (two objects that
-- hold one number, which the console never makes either) is 0 but on the case whose
-- object is named by the number; 0 is not held unique, as an object that holds only the
-- work on a case may have it. An RCA's id is not looked up, and is empty on every RCA
-- written since wave 6e, so it is not held unique.
UPDATE findings f SET finding_key = f.finding_key || '~' || d.n
FROM (SELECT finding_id, row_number() OVER (PARTITION BY finding_key ORDER BY finding_id) AS n FROM findings WHERE finding_key <> '') d
WHERE d.finding_id = f.finding_id AND d.n > 1;
CREATE UNIQUE INDEX findings_finding_key_key ON findings (finding_key) WHERE finding_key <> '';
UPDATE cases c SET case_number = 0
FROM (SELECT case_id, row_number() OVER (PARTITION BY case_number ORDER BY (object_name = case_number::text) DESC, case_id) AS n FROM cases WHERE case_number > 0) d
WHERE d.case_id = c.case_id AND d.n > 1;
DROP INDEX cases_case_number_idx;
CREATE UNIQUE INDEX cases_case_number_key ON cases (case_number) WHERE case_number > 0;

-- Names that said something else. A test case's id in the agents' test runs was
-- case_key, the name of a case's id elsewhere; case_work repeated its case's number.
ALTER TABLE test_case_results RENAME COLUMN case_key TO test_case_id;
ALTER TABLE test_run_cases RENAME COLUMN case_key TO test_case_id;
ALTER TABLE case_work DROP COLUMN case_number;
