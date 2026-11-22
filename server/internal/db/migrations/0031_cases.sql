-- The cases with the work on them, the known issues and the service catalogue, with every
-- field a column (DB-52 to DB-54). The tables of steps 0009 and 0011 named here held no
-- rows until this step.

ALTER TABLE cases DROP CONSTRAINT cases_known_issue_id_fkey;
ALTER TABLE cases DROP CONSTRAINT cases_exception_id_fkey;
DROP TABLE case_events, case_links, case_messages, case_analyses, rca_cases, rcas, cases, known_issue_updates, known_issue_services, known_issues, services;

-- A service is known by the id the console gives it: cases and known issues name it.
CREATE TABLE services (
    service_id text PRIMARY KEY,
    position integer NOT NULL DEFAULT 0,
    name text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    contacts text[] NOT NULL DEFAULT '{}',
    aliases text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
COMMENT ON TABLE services IS 'Service catalogue: name, what people call it, contacts';

CREATE TABLE service_runs_on (
    service_id text NOT NULL REFERENCES services (service_id) ON DELETE CASCADE,
    position integer NOT NULL,
    kind text NOT NULL DEFAULT '',
    value text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '',
    PRIMARY KEY (service_id, position)
);
COMMENT ON TABLE service_runs_on IS 'Where a service runs: a cluster, a namespace, a workload, a host, a fleet, an address';

-- A known issue is known by the id the console gives it ("KI-3").
CREATE TABLE known_issues (
    known_issue_id uuid PRIMARY KEY,
    known_issue_key text NOT NULL UNIQUE,
    position bigint NOT NULL DEFAULT 0,
    title text NOT NULL DEFAULT '',
    symptoms text[] NOT NULL DEFAULT '{}',
    -- The services it affects, by their ids: a service may be gone.
    services text[] NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT '',
    workaround text NOT NULL DEFAULT '',
    expected text NOT NULL DEFAULT '',
    -- A person is kept as the id, the name, the email and whether it is the console
    -- itself; none where there is no such person.
    owner_key text,
    owner_name text NOT NULL DEFAULT '',
    owner_email text NOT NULL DEFAULT '',
    owner_is_system boolean NOT NULL DEFAULT false,
    kind text NOT NULL DEFAULT '',
    -- A maintenance window; none for an issue that is not one.
    window_start_at timestamptz,
    window_end_at timestamptz,
    source text NOT NULL DEFAULT '',
    is_draft boolean NOT NULL DEFAULT false,
    link text NOT NULL DEFAULT '',
    created_by_key text,
    created_by_name text NOT NULL DEFAULT '',
    created_by_email text NOT NULL DEFAULT '',
    created_by_is_system boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    resolved_at timestamptz,
    -- The runs that still found it after it was resolved; none when none did.
    still_present_count integer,
    still_present_last_at timestamptz,
    still_present_run_key text NOT NULL DEFAULT '',
    verify_jobs text[] NOT NULL DEFAULT '{}',
    is_pending boolean NOT NULL DEFAULT false,
    -- Set aside by the housekeeping as a repeat of another, named by its id.
    is_inactive boolean NOT NULL DEFAULT false,
    duplicate_of_known_issue_key text NOT NULL DEFAULT ''
);
COMMENT ON TABLE known_issues IS 'Known issues: symptoms, status, workaround, owner, and what the runs still find of them';

CREATE TABLE known_issue_updates (
    known_issue_id uuid NOT NULL REFERENCES known_issues (known_issue_id) ON DELETE CASCADE,
    position integer NOT NULL,
    at timestamptz NOT NULL,
    by_key text NOT NULL DEFAULT '',
    by_name text NOT NULL DEFAULT '',
    by_email text NOT NULL DEFAULT '',
    by_is_system boolean NOT NULL DEFAULT false,
    body text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT '',
    PRIMARY KEY (known_issue_id, position)
);
COMMENT ON TABLE known_issue_updates IS 'Dated updates on a known issue';

CREATE TABLE known_issue_followers (
    known_issue_id uuid NOT NULL REFERENCES known_issues (known_issue_id) ON DELETE CASCADE,
    position integer NOT NULL,
    person_key text NOT NULL DEFAULT '',
    person_name text NOT NULL DEFAULT '',
    person_email text NOT NULL DEFAULT '',
    person_is_system boolean NOT NULL DEFAULT false,
    PRIMARY KEY (known_issue_id, position)
);
COMMENT ON TABLE known_issue_followers IS 'Who follows a known issue';

CREATE TABLE known_issue_keys (
    known_issue_id uuid NOT NULL REFERENCES known_issues (known_issue_id) ON DELETE CASCADE,
    position integer NOT NULL,
    cluster_id text NOT NULL DEFAULT '',
    object text NOT NULL DEFAULT '',
    check_name text NOT NULL DEFAULT '',
    job_id text NOT NULL DEFAULT '',
    PRIMARY KEY (known_issue_id, position)
);
CREATE INDEX known_issue_keys_idx ON known_issue_keys (cluster_id, object, check_name);
COMMENT ON TABLE known_issue_keys IS 'The findings a known issue covers: cluster, object and check, with the job that found each';

-- A case is one object with one version: the case itself, the work on it and its
-- conversation with Sherlock are read and saved over that version. object_name is the
-- name the console gives the object, the case's number.
CREATE TABLE cases (
    case_id uuid PRIMARY KEY,
    object_name text NOT NULL UNIQUE,
    version bigint NOT NULL DEFAULT 0,
    -- False: only the work or the conversation is kept of it so far.
    is_case_kept boolean NOT NULL DEFAULT false,
    case_number integer NOT NULL DEFAULT 0,
    title text NOT NULL DEFAULT '',
    -- The title its line in the list shows, and who is involved in it: worked out when
    -- the case is saved, so that the list is read without the text and the messages.
    line_title text NOT NULL DEFAULT '',
    involved text[] NOT NULL DEFAULT '{}',
    source text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '',
    object text NOT NULL DEFAULT '',
    opened_by_key text,
    opened_by_name text NOT NULL DEFAULT '',
    opened_by_email text NOT NULL DEFAULT '',
    opened_by_is_system boolean NOT NULL DEFAULT false,
    -- The finding's key; none for a case no finding opened.
    key_cluster_id text,
    key_object text NOT NULL DEFAULT '',
    key_check text NOT NULL DEFAULT '',
    job_id text NOT NULL DEFAULT '',
    run_key text NOT NULL DEFAULT '',
    finding_key text NOT NULL DEFAULT '',
    severity text NOT NULL DEFAULT '',
    -- How often the runs saw it; none for a case they never saw.
    seen_count integer,
    seen_first_at timestamptz,
    seen_last_at timestamptz,
    seen_run_key text NOT NULL DEFAULT '',
    seen_finding_key text NOT NULL DEFAULT '',
    seen_as text NOT NULL DEFAULT '',
    -- Other cases, by their numbers: each may be gone.
    regression_of_number integer NOT NULL DEFAULT 0,
    same_as_number integer NOT NULL DEFAULT 0,
    possible_duplicate_of_number integer NOT NULL DEFAULT 0,
    duplicate_of_number integer NOT NULL DEFAULT 0,
    similar_numbers integer[] NOT NULL DEFAULT '{}',
    -- How the case it is a regression of was closed; none otherwise.
    previous_case_number integer,
    previous_outcome text NOT NULL DEFAULT '',
    previous_explanation text NOT NULL DEFAULT '',
    previous_by text NOT NULL DEFAULT '',
    previous_at timestamptz,
    exception_key text NOT NULL DEFAULT '',
    rca_key text NOT NULL DEFAULT '',
    rca_cause text NOT NULL DEFAULT '',
    is_verify_pending boolean NOT NULL DEFAULT false,
    verified_at timestamptz,
    -- The run that verified the fix; none while none did.
    verified_run_key text,
    verified_job_id text NOT NULL DEFAULT '',
    verified_run_at timestamptz,
    verified_evidence text NOT NULL DEFAULT '',
    unchecked_count integer NOT NULL DEFAULT 0,
    unchecked_why text NOT NULL DEFAULT '',
    unchecked_run_key text NOT NULL DEFAULT '',
    sla_from_at timestamptz,
    closed_by_key text,
    closed_by_name text NOT NULL DEFAULT '',
    closed_by_email text NOT NULL DEFAULT '',
    closed_by_is_system boolean NOT NULL DEFAULT false,
    is_fix_proposed boolean NOT NULL DEFAULT false,
    -- The change that was applied for it; none while none was.
    applied_change_key text,
    applied_title text NOT NULL DEFAULT '',
    applied_by text NOT NULL DEFAULT '',
    applied_at timestamptz,
    verification text NOT NULL DEFAULT '',
    type text NOT NULL DEFAULT '',
    incident text NOT NULL DEFAULT '',
    created_by_name text,
    created_by_kind text NOT NULL DEFAULT '',
    last_by_name text,
    last_by_kind text NOT NULL DEFAULT '',
    last_at timestamptz,
    analysis_queued_at timestamptz,
    body text NOT NULL DEFAULT '',
    is_scrubbed boolean NOT NULL DEFAULT false,
    is_pruned boolean NOT NULL DEFAULT false,
    reporter_key text NOT NULL DEFAULT '',
    reporter_name text NOT NULL DEFAULT '',
    reporter_email text NOT NULL DEFAULT '',
    reporter_is_system boolean NOT NULL DEFAULT false,
    filed_by_key text,
    filed_by_name text NOT NULL DEFAULT '',
    filed_by_email text NOT NULL DEFAULT '',
    filed_by_is_system boolean NOT NULL DEFAULT false,
    created_at timestamptz,
    updated_at timestamptz,
    status text NOT NULL DEFAULT '',
    status_at timestamptz,
    outcome text NOT NULL DEFAULT '',
    explanation text NOT NULL DEFAULT '',
    priority integer NOT NULL DEFAULT 0,
    priority_by text NOT NULL DEFAULT '',
    owner_key text,
    owner_name text NOT NULL DEFAULT '',
    owner_email text NOT NULL DEFAULT '',
    owner_is_system boolean NOT NULL DEFAULT false,
    -- The service and the known issue, by their ids: either may be gone.
    service_key text NOT NULL DEFAULT '',
    service_confidence integer NOT NULL DEFAULT 0,
    service_by text NOT NULL DEFAULT '',
    known_issue_key text NOT NULL DEFAULT '',
    match_confidence integer NOT NULL DEFAULT 0,
    rejected text[] NOT NULL DEFAULT '{}',
    waiting_at timestamptz,
    technical text NOT NULL DEFAULT '',
    plain text NOT NULL DEFAULT '',
    cause text NOT NULL DEFAULT '',
    evidence text[] NOT NULL DEFAULT '{}',
    acknowledged_at timestamptz,
    first_response_at timestamptz,
    resolved_at timestamptz,
    closed_at timestamptz,
    last_reporter_at timestamptz,
    reopened_count integer NOT NULL DEFAULT 0
);
CREATE INDEX cases_case_number_idx ON cases (case_number);
CREATE INDEX cases_status_idx ON cases (status);
COMMENT ON TABLE cases IS 'Cases: number, source, cluster, object, status, outcome, priority, the people on it, what it links to, and its times';

CREATE TABLE case_messages (
    case_id uuid NOT NULL REFERENCES cases (case_id) ON DELETE CASCADE,
    position integer NOT NULL,
    message_number integer NOT NULL DEFAULT 0,
    at timestamptz NOT NULL,
    kind text NOT NULL DEFAULT '',
    from_key text NOT NULL DEFAULT '',
    from_name text NOT NULL DEFAULT '',
    from_email text NOT NULL DEFAULT '',
    from_is_system boolean NOT NULL DEFAULT false,
    body text NOT NULL DEFAULT '',
    is_draft boolean NOT NULL DEFAULT false,
    draft_by text NOT NULL DEFAULT '',
    is_internal boolean NOT NULL DEFAULT false,
    sent_by_key text,
    sent_by_name text NOT NULL DEFAULT '',
    sent_by_email text NOT NULL DEFAULT '',
    sent_by_is_system boolean NOT NULL DEFAULT false,
    PRIMARY KEY (case_id, position)
);
COMMENT ON TABLE case_messages IS 'Messages on a case';

-- The people of a case: its watchers and the people who may approve its changes.
CREATE TABLE case_people (
    case_id uuid NOT NULL REFERENCES cases (case_id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('watcher', 'approver')),
    position integer NOT NULL,
    person_key text NOT NULL DEFAULT '',
    person_name text NOT NULL DEFAULT '',
    person_email text NOT NULL DEFAULT '',
    person_is_system boolean NOT NULL DEFAULT false,
    PRIMARY KEY (case_id, role, position)
);
CREATE INDEX case_people_person_key_idx ON case_people (person_key);
COMMENT ON TABLE case_people IS 'Who watches a case and who may approve its changes';

-- The work on a case: what 221B did about it. None for a case nothing was done about.
CREATE TABLE case_work (
    case_id uuid PRIMARY KEY REFERENCES cases (case_id) ON DELETE CASCADE,
    case_number integer NOT NULL DEFAULT 0,
    -- Where the case came from.
    source_kind text NOT NULL DEFAULT '',
    finding_key text NOT NULL DEFAULT '',
    run_key text NOT NULL DEFAULT '',
    job_id text NOT NULL DEFAULT '',
    check_name text NOT NULL DEFAULT '',
    severity text NOT NULL DEFAULT '',
    alert text NOT NULL DEFAULT '',
    alert_key text NOT NULL DEFAULT '',
    -- The runs that analyzed it, by their ids.
    runs text[] NOT NULL DEFAULT '{}',
    fix text NOT NULL DEFAULT '',
    -- The note that the fix was applied; none while it was not.
    fix_applied_at timestamptz,
    fix_applied_body text NOT NULL DEFAULT '',
    fix_applied_by text NOT NULL DEFAULT '',
    fix_applied_change_key text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL
);
COMMENT ON TABLE case_work IS 'The work on a case: where it came from, the runs that analyzed it, the fix';

CREATE TABLE case_analyses (
    case_id uuid PRIMARY KEY REFERENCES cases (case_id) ON DELETE CASCADE,
    run_key text NOT NULL DEFAULT '',
    analyzed_at timestamptz NOT NULL,
    summary text NOT NULL DEFAULT '',
    cause text NOT NULL DEFAULT '',
    evidence text[] NOT NULL DEFAULT '{}',
    fix_summary text NOT NULL DEFAULT '',
    fix_commands text[] NOT NULL DEFAULT '{}',
    confidence text NOT NULL DEFAULT '',
    ruled_out text[] NOT NULL DEFAULT '{}',
    is_expected boolean NOT NULL DEFAULT false,
    expected_why text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    left_out text[] NOT NULL DEFAULT '{}'
);
COMMENT ON TABLE case_analyses IS 'The Troubleshooter''s analysis of a case: cause, evidence, suggested fix';

CREATE TABLE rcas (
    case_id uuid PRIMARY KEY REFERENCES cases (case_id) ON DELETE CASCADE,
    rca_key text NOT NULL DEFAULT '',
    -- The case it was written for and the cases it covers, by the ids people read.
    case_key text NOT NULL DEFAULT '',
    cases text[] NOT NULL DEFAULT '{}',
    title text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '',
    resource text NOT NULL DEFAULT '',
    priority integer NOT NULL DEFAULT 0,
    cause text NOT NULL DEFAULT '',
    fix text NOT NULL DEFAULT '',
    prevention text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    written_by text NOT NULL DEFAULT '',
    written_at timestamptz NOT NULL
);
COMMENT ON TABLE rcas IS 'Root cause analyses written when cases close';

CREATE TABLE case_events (
    case_id uuid NOT NULL REFERENCES cases (case_id) ON DELETE CASCADE,
    position integer NOT NULL,
    at timestamptz NOT NULL,
    what text NOT NULL DEFAULT '',
    by_name text NOT NULL DEFAULT '',
    by_kind text NOT NULL DEFAULT '',
    PRIMARY KEY (case_id, position)
);
COMMENT ON TABLE case_events IS 'History of the work on each case';

CREATE TABLE case_proposals (
    case_id uuid NOT NULL REFERENCES cases (case_id) ON DELETE CASCADE,
    position integer NOT NULL,
    -- The proposed change, by the id people read ("PC-12"): it may be gone.
    change_key text NOT NULL,
    -- False: the case no longer lists it, and only who linked it or how it was handled is kept.
    is_listed boolean NOT NULL DEFAULT true,
    linked_by text,
    handled text,
    PRIMARY KEY (case_id, position)
);
CREATE INDEX case_proposals_change_key_idx ON case_proposals (change_key);
COMMENT ON TABLE case_proposals IS 'The changes proposed for a case, who linked each and how the case handled each';

-- What people are writing on a case and have not sent: the case's own draft
-- (is_shared), and each person's.
CREATE TABLE case_drafts (
    case_id uuid NOT NULL REFERENCES cases (case_id) ON DELETE CASCADE,
    is_shared boolean NOT NULL,
    owner_key text NOT NULL DEFAULT '',
    reply text NOT NULL DEFAULT '',
    fix text NOT NULL DEFAULT '',
    -- The closing being written; none when none is.
    close_outcome text,
    close_explanation text NOT NULL DEFAULT '',
    close_original text NOT NULL DEFAULT '',
    -- The RCA being written; none when none is.
    rca_cause text,
    rca_fix text NOT NULL DEFAULT '',
    rca_prevention text NOT NULL DEFAULT '',
    rca_body text NOT NULL DEFAULT '',
    by_name text NOT NULL DEFAULT '',
    updated_at timestamptz,
    at timestamptz,
    PRIMARY KEY (case_id, is_shared, owner_key)
);
COMMENT ON TABLE case_drafts IS 'What is being written on a case and not sent yet: a reply, a fix, a closing, an RCA';

CREATE TABLE case_draft_drafters (
    case_id uuid NOT NULL,
    is_shared boolean NOT NULL,
    owner_key text NOT NULL,
    -- The part of the draft, and who wrote in it, in order.
    part text NOT NULL,
    position integer NOT NULL,
    drafter text NOT NULL DEFAULT '',
    PRIMARY KEY (case_id, is_shared, owner_key, part, position),
    FOREIGN KEY (case_id, is_shared, owner_key) REFERENCES case_drafts (case_id, is_shared, owner_key) ON DELETE CASCADE
);
COMMENT ON TABLE case_draft_drafters IS 'Who wrote in each part of a draft';

CREATE TABLE case_commands (
    case_id uuid NOT NULL REFERENCES cases (case_id) ON DELETE CASCADE,
    position integer NOT NULL,
    watson_item text NOT NULL DEFAULT '',
    line text NOT NULL DEFAULT '',
    by_name text NOT NULL DEFAULT '',
    at timestamptz NOT NULL,
    is_change boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT '',
    exit_code integer,
    approved_by text NOT NULL DEFAULT '',
    finished_at timestamptz,
    PRIMARY KEY (case_id, position)
);
COMMENT ON TABLE case_commands IS 'The commands run from a case through Watson; never their output';
