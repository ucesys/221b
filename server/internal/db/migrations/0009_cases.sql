-- Cases (docs/data-model.md). Keys are UUIDv7 made by the console unless the
-- model names a natural key; every key is named for its table.

CREATE TABLE services (
    service_id uuid PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    runs_on jsonb,
    contacts text[] NOT NULL DEFAULT '{}'
);
COMMENT ON TABLE services IS 'Service catalogue: name, where it runs, contacts';

CREATE TABLE case_events (
    case_event_id uuid PRIMARY KEY,
    case_id uuid NOT NULL,
    at text NOT NULL DEFAULT '',
    what text NOT NULL DEFAULT '',
    actor text NOT NULL DEFAULT ''
);
COMMENT ON TABLE case_events IS 'History of each case';

CREATE TABLE cases (
    case_id uuid PRIMARY KEY,
    case_number text NOT NULL UNIQUE,
    title text NOT NULL DEFAULT '',
    type text NOT NULL DEFAULT '',
    source text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '',
    object text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT '',
    outcome text NOT NULL DEFAULT '',
    priority text NOT NULL DEFAULT '',
    service_id uuid,
    known_issue_id uuid,
    exception_id uuid,
    assignee text NOT NULL DEFAULT '',
    reporter text NOT NULL DEFAULT '',
    incident text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz
);
COMMENT ON TABLE cases IS 'Cases: number, source, cluster, object, status, priority, owner';

CREATE TABLE case_links (
    case_id uuid NOT NULL,
    linked_case_id uuid NOT NULL,
    kind text NOT NULL DEFAULT '',
    PRIMARY KEY (case_id, linked_case_id)
);
COMMENT ON TABLE case_links IS 'Links between cases: duplicate of, regression of, same as, possible duplicate of';

CREATE TABLE case_messages (
    case_message_id uuid PRIMARY KEY,
    case_id uuid NOT NULL,
    at text NOT NULL DEFAULT '',
    author text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT ''
);
COMMENT ON TABLE case_messages IS 'Messages on a case';

CREATE TABLE case_analyses (
    case_analysis_id uuid PRIMARY KEY,
    case_id uuid NOT NULL,
    job_run_id uuid,
    summary text NOT NULL DEFAULT '',
    cause text NOT NULL DEFAULT '',
    evidence jsonb,
    suggested_fix text NOT NULL DEFAULT '',
    confidence text NOT NULL DEFAULT '',
    analyzed_at timestamptz
);
COMMENT ON TABLE case_analyses IS 'The Troubleshooter''s analysis: cause, evidence, suggested fix';

CREATE TABLE rca_cases (
    rca_id uuid NOT NULL,
    case_number text NOT NULL,
    case_id uuid,
    PRIMARY KEY (rca_id, case_number)
);
COMMENT ON TABLE rca_cases IS 'The cases each RCA covers';

CREATE TABLE rcas (
    rca_id uuid PRIMARY KEY,
    title text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '',
    resource text NOT NULL DEFAULT '',
    priority text NOT NULL DEFAULT '',
    cause text NOT NULL DEFAULT '',
    fix text NOT NULL DEFAULT '',
    prevention text NOT NULL DEFAULT '',
    written_by text NOT NULL DEFAULT '',
    written_at timestamptz
);
COMMENT ON TABLE rcas IS 'Root cause analyses written when cases close';

-- Keys.
ALTER TABLE case_events ADD CONSTRAINT case_events_case_id_fkey
    FOREIGN KEY (case_id) REFERENCES cases (case_id) ON DELETE CASCADE;
CREATE INDEX case_events_case_id_idx ON case_events (case_id);
ALTER TABLE cases ADD CONSTRAINT cases_service_id_fkey
    FOREIGN KEY (service_id) REFERENCES services (service_id) ON DELETE SET NULL;
CREATE INDEX cases_service_id_idx ON cases (service_id);
ALTER TABLE case_links ADD CONSTRAINT case_links_case_id_fkey
    FOREIGN KEY (case_id) REFERENCES cases (case_id) ON DELETE CASCADE;
ALTER TABLE case_links ADD CONSTRAINT case_links_linked_case_id_fkey
    FOREIGN KEY (linked_case_id) REFERENCES cases (case_id) ON DELETE CASCADE;
CREATE INDEX case_links_linked_case_id_idx ON case_links (linked_case_id);
ALTER TABLE case_messages ADD CONSTRAINT case_messages_case_id_fkey
    FOREIGN KEY (case_id) REFERENCES cases (case_id) ON DELETE CASCADE;
CREATE INDEX case_messages_case_id_idx ON case_messages (case_id);
ALTER TABLE case_analyses ADD CONSTRAINT case_analyses_case_id_fkey
    FOREIGN KEY (case_id) REFERENCES cases (case_id) ON DELETE CASCADE;
CREATE INDEX case_analyses_case_id_idx ON case_analyses (case_id);
ALTER TABLE case_analyses ADD CONSTRAINT case_analyses_job_run_id_fkey
    FOREIGN KEY (job_run_id) REFERENCES job_runs (job_run_id) ON DELETE SET NULL;
CREATE INDEX case_analyses_job_run_id_idx ON case_analyses (job_run_id);
ALTER TABLE rca_cases ADD CONSTRAINT rca_cases_rca_id_fkey
    FOREIGN KEY (rca_id) REFERENCES rcas (rca_id) ON DELETE CASCADE;
ALTER TABLE rca_cases ADD CONSTRAINT rca_cases_case_id_fkey
    FOREIGN KEY (case_id) REFERENCES cases (case_id) ON DELETE SET NULL;
CREATE INDEX rca_cases_case_id_idx ON rca_cases (case_id);
