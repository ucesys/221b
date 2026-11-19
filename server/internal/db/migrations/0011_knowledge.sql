-- Knowledge (docs/data-model.md). Keys are UUIDv7 made by the console unless the
-- model names a natural key; every key is named for its table.

CREATE TABLE wiki_pages (
    path text PRIMARY KEY,
    title text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    is_critical boolean NOT NULL DEFAULT false,
    is_stale boolean NOT NULL DEFAULT false,
    version integer NOT NULL DEFAULT 0,
    updated_at timestamptz,
    retired_at timestamptz,
    retired_reason text NOT NULL DEFAULT ''
);
COMMENT ON TABLE wiki_pages IS 'Manifest of wiki pages, retired ones included; bodies stay in Git or S3';

CREATE TABLE wiki_changes (
    wiki_change_id uuid PRIMARY KEY,
    path text NOT NULL,
    base_version integer NOT NULL DEFAULT 0,
    diff text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT '',
    proposed_by text NOT NULL DEFAULT '',
    approval_request_id uuid,
    created_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE wiki_changes IS 'Proposed page changes awaiting approval, kept as history';

CREATE TABLE knowledge_sources (
    knowledge_source_id uuid PRIMARY KEY,
    repository_id uuid,
    credential_id uuid,
    name text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    url text NOT NULL DEFAULT '',
    branch text NOT NULL DEFAULT '',
    path text NOT NULL DEFAULT '',
    is_enabled boolean NOT NULL DEFAULT false,
    last_read_at timestamptz,
    last_status text NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT ''
);
COMMENT ON TABLE knowledge_sources IS 'Sources Mycroft reads: Git, S3, Confluence, web, uploads; with the last sync''s outcome';

CREATE TABLE fixes (
    fix_id uuid PRIMARY KEY,
    title text NOT NULL DEFAULT '',
    problem_key text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '',
    resource text NOT NULL DEFAULT '',
    symptoms text NOT NULL DEFAULT '',
    cause text NOT NULL DEFAULT '',
    steps text[] NOT NULL DEFAULT '{}',
    commands text[] NOT NULL DEFAULT '{}',
    prevention text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT '',
    duplicate_of_fix_id uuid,
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE fixes IS 'Recorded fixes: problem, cause, steps, the commands that worked';

CREATE TABLE fix_cases (
    fix_id uuid NOT NULL,
    case_number text NOT NULL,
    case_id uuid,
    seen_at timestamptz,
    PRIMARY KEY (fix_id, case_number)
);
COMMENT ON TABLE fix_cases IS 'The cases each fix was seen in';

CREATE TABLE source_exclusions (
    source_exclusion_id uuid PRIMARY KEY,
    knowledge_source_id uuid NOT NULL,
    pattern text NOT NULL DEFAULT '',
    is_item boolean NOT NULL DEFAULT false,
    is_under boolean NOT NULL DEFAULT false,
    title text NOT NULL DEFAULT '',
    reason text NOT NULL DEFAULT '',
    excluded_by text NOT NULL DEFAULT '',
    excluded_at timestamptz
);
COMMENT ON TABLE source_exclusions IS 'Items or patterns a source does not read';

CREATE TABLE exceptions (
    exception_id uuid PRIMARY KEY,
    cluster_id text NOT NULL DEFAULT '',
    resource text NOT NULL DEFAULT '',
    check_name text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    reason text NOT NULL DEFAULT '',
    finding_id uuid,
    job_run_id uuid,
    case_id uuid,
    duplicate_of_exception_id uuid,
    expires_at timestamptz,
    created_by text NOT NULL DEFAULT '',
    removed_at timestamptz
);
COMMENT ON TABLE exceptions IS 'Accepted exceptions: findings on this check, resource and cluster are left out until expiry';

CREATE TABLE known_issues (
    known_issue_id uuid PRIMARY KEY,
    title text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT '',
    symptoms text NOT NULL DEFAULT '',
    workaround text NOT NULL DEFAULT '',
    expected text NOT NULL DEFAULT '',
    time_window jsonb,
    source text NOT NULL DEFAULT '',
    is_draft boolean NOT NULL DEFAULT false,
    link text NOT NULL DEFAULT '',
    owner text NOT NULL DEFAULT '',
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE known_issues IS 'Known issues: symptoms, status, workaround, owner';

CREATE TABLE known_issue_updates (
    known_issue_update_id uuid PRIMARY KEY,
    known_issue_id uuid NOT NULL,
    at text NOT NULL DEFAULT '',
    author text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT ''
);
COMMENT ON TABLE known_issue_updates IS 'Dated updates on a known issue';

CREATE TABLE known_issue_services (
    known_issue_id uuid NOT NULL,
    service_id uuid NOT NULL,
    PRIMARY KEY (known_issue_id, service_id)
);
COMMENT ON TABLE known_issue_services IS 'The services a known issue affects';

-- Keys.
ALTER TABLE wiki_changes ADD CONSTRAINT wiki_changes_path_fkey
    FOREIGN KEY (path) REFERENCES wiki_pages (path) ON DELETE CASCADE;
CREATE INDEX wiki_changes_path_idx ON wiki_changes (path);
ALTER TABLE wiki_changes ADD CONSTRAINT wiki_changes_approval_request_id_fkey
    FOREIGN KEY (approval_request_id) REFERENCES approval_requests (approval_request_id) ON DELETE SET NULL;
CREATE INDEX wiki_changes_approval_request_id_idx ON wiki_changes (approval_request_id);
ALTER TABLE knowledge_sources ADD CONSTRAINT knowledge_sources_repository_id_fkey
    FOREIGN KEY (repository_id) REFERENCES repositories (repository_id) ON DELETE SET NULL;
CREATE INDEX knowledge_sources_repository_id_idx ON knowledge_sources (repository_id);
ALTER TABLE knowledge_sources ADD CONSTRAINT knowledge_sources_credential_id_fkey
    FOREIGN KEY (credential_id) REFERENCES credentials (credential_id) ON DELETE SET NULL;
CREATE INDEX knowledge_sources_credential_id_idx ON knowledge_sources (credential_id);
ALTER TABLE fixes ADD CONSTRAINT fixes_duplicate_of_fix_id_fkey
    FOREIGN KEY (duplicate_of_fix_id) REFERENCES fixes (fix_id) ON DELETE SET NULL;
CREATE INDEX fixes_duplicate_of_fix_id_idx ON fixes (duplicate_of_fix_id);
ALTER TABLE fix_cases ADD CONSTRAINT fix_cases_fix_id_fkey
    FOREIGN KEY (fix_id) REFERENCES fixes (fix_id) ON DELETE CASCADE;
ALTER TABLE fix_cases ADD CONSTRAINT fix_cases_case_id_fkey
    FOREIGN KEY (case_id) REFERENCES cases (case_id) ON DELETE SET NULL;
CREATE INDEX fix_cases_case_id_idx ON fix_cases (case_id);
ALTER TABLE source_exclusions ADD CONSTRAINT source_exclusions_knowledge_source_id_fkey
    FOREIGN KEY (knowledge_source_id) REFERENCES knowledge_sources (knowledge_source_id) ON DELETE CASCADE;
CREATE INDEX source_exclusions_knowledge_source_id_idx ON source_exclusions (knowledge_source_id);
ALTER TABLE exceptions ADD CONSTRAINT exceptions_finding_id_fkey
    FOREIGN KEY (finding_id) REFERENCES findings (finding_id) ON DELETE SET NULL;
CREATE INDEX exceptions_finding_id_idx ON exceptions (finding_id);
ALTER TABLE exceptions ADD CONSTRAINT exceptions_job_run_id_fkey
    FOREIGN KEY (job_run_id) REFERENCES job_runs (job_run_id) ON DELETE SET NULL;
CREATE INDEX exceptions_job_run_id_idx ON exceptions (job_run_id);
ALTER TABLE exceptions ADD CONSTRAINT exceptions_case_id_fkey
    FOREIGN KEY (case_id) REFERENCES cases (case_id) ON DELETE SET NULL;
CREATE INDEX exceptions_case_id_idx ON exceptions (case_id);
ALTER TABLE exceptions ADD CONSTRAINT exceptions_duplicate_of_exception_id_fkey
    FOREIGN KEY (duplicate_of_exception_id) REFERENCES exceptions (exception_id) ON DELETE SET NULL;
CREATE INDEX exceptions_duplicate_of_exception_id_idx ON exceptions (duplicate_of_exception_id);
ALTER TABLE known_issue_updates ADD CONSTRAINT known_issue_updates_known_issue_id_fkey
    FOREIGN KEY (known_issue_id) REFERENCES known_issues (known_issue_id) ON DELETE CASCADE;
CREATE INDEX known_issue_updates_known_issue_id_idx ON known_issue_updates (known_issue_id);
ALTER TABLE known_issue_services ADD CONSTRAINT known_issue_services_known_issue_id_fkey
    FOREIGN KEY (known_issue_id) REFERENCES known_issues (known_issue_id) ON DELETE CASCADE;
ALTER TABLE known_issue_services ADD CONSTRAINT known_issue_services_service_id_fkey
    FOREIGN KEY (service_id) REFERENCES services (service_id) ON DELETE CASCADE;
CREATE INDEX known_issue_services_service_id_idx ON known_issue_services (service_id);
