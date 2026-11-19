-- Keys from one domain into a later one: the AIOps domains key into each other along
-- the flow, and Automation and AI configuration key into Approvals and Inventory.

ALTER TABLE findings ADD CONSTRAINT findings_case_id_fkey
    FOREIGN KEY (case_id) REFERENCES cases (case_id) ON DELETE SET NULL;
CREATE INDEX findings_case_id_idx ON findings (case_id);
ALTER TABLE findings ADD CONSTRAINT findings_known_issue_id_fkey
    FOREIGN KEY (known_issue_id) REFERENCES known_issues (known_issue_id) ON DELETE SET NULL;
CREATE INDEX findings_known_issue_id_idx ON findings (known_issue_id);
ALTER TABLE findings ADD CONSTRAINT findings_exception_id_fkey
    FOREIGN KEY (exception_id) REFERENCES exceptions (exception_id) ON DELETE SET NULL;
CREATE INDEX findings_exception_id_idx ON findings (exception_id);
ALTER TABLE cases ADD CONSTRAINT cases_known_issue_id_fkey
    FOREIGN KEY (known_issue_id) REFERENCES known_issues (known_issue_id) ON DELETE SET NULL;
CREATE INDEX cases_known_issue_id_idx ON cases (known_issue_id);
ALTER TABLE cases ADD CONSTRAINT cases_exception_id_fkey
    FOREIGN KEY (exception_id) REFERENCES exceptions (exception_id) ON DELETE SET NULL;
CREATE INDEX cases_exception_id_idx ON cases (exception_id);
