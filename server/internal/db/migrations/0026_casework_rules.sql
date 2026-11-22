-- The checks seen on each cluster, the validation rules with their suggestions, and what
-- the runs found under each check, with every field a column (DB-43). The tables of step
-- 0008 held no rows until this step.

-- The checks seen on a cluster are a list; when each was first and last found, and how
-- often, is kept by check and not by cluster (check_found below).
ALTER TABLE cluster_checks
    DROP COLUMN first_seen_at,
    DROP COLUMN last_seen_at,
    DROP COLUMN finding_count,
    DROP COLUMN sample_title,
    ADD COLUMN position integer NOT NULL DEFAULT 0;

CREATE TABLE checked_clusters (
    cluster_id text PRIMARY KEY,
    -- The cluster has a list of checks seen, which may be empty.
    has_checks boolean NOT NULL DEFAULT false,
    -- The first run that looked at it: none of what that run found is a new check.
    first_run_key text
);
COMMENT ON TABLE checked_clusters IS 'The clusters the jobs have looked at, each with its first run';

-- A stored rule is a built-in one's changes or a rule added; the rule that replaces a
-- retired one may be built in and have no row, so it is named and not a key.
ALTER TABLE validation_rules DROP CONSTRAINT validation_rules_replaced_by_validation_rule_id_fkey;
ALTER TABLE validation_rules
    DROP COLUMN is_edited,
    -- None: the job kinds are the built-in rule's own.
    ALTER COLUMN job_kinds DROP NOT NULL,
    ALTER COLUMN job_kinds DROP DEFAULT;
COMMENT ON COLUMN validation_rules.state IS 'active or retired';

CREATE TABLE rule_suggestions (
    rule_suggestion_id uuid PRIMARY KEY,
    position integer NOT NULL DEFAULT 0,
    -- The check id suggested as a rule.
    suggestion_key text NOT NULL DEFAULT '',
    says text NOT NULL DEFAULT '',
    case_type text NOT NULL DEFAULT '',
    job_kinds text[],
    -- The ids agents made up that it would stand for.
    from_checks text[],
    run_count integer NOT NULL DEFAULT 0,
    cluster_count integer NOT NULL DEFAULT 0,
    created_at timestamptz,
    why text NOT NULL DEFAULT ''
);
COMMENT ON TABLE rule_suggestions IS 'Rules suggested from what the runs found, waiting for someone to add or dismiss them';

CREATE TABLE rule_suggestion_descriptions (
    rule_suggestion_id uuid NOT NULL REFERENCES rule_suggestions (rule_suggestion_id) ON DELETE CASCADE,
    position integer NOT NULL,
    description_key text NOT NULL DEFAULT '',
    job_id text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    run_key text NOT NULL DEFAULT '',
    described_at timestamptz,
    PRIMARY KEY (rule_suggestion_id, position)
);
COMMENT ON TABLE rule_suggestion_descriptions IS 'How agents described the ids a suggestion stands for';

CREATE TABLE check_found (
    check_name text PRIMARY KEY,
    finding_count integer NOT NULL DEFAULT 0,
    run_count integer NOT NULL DEFAULT 0,
    cluster_ids text[] NOT NULL DEFAULT '{}',
    job_ids text[] NOT NULL DEFAULT '{}',
    first_found_at timestamptz,
    last_found_at timestamptz,
    -- The newest runs that found it.
    run_keys text[] NOT NULL DEFAULT '{}'
);
COMMENT ON TABLE check_found IS 'What the runs found under each check id: how often, where, and when first and last';

CREATE TABLE check_found_titles (
    check_name text NOT NULL REFERENCES check_found (check_name) ON DELETE CASCADE,
    position integer NOT NULL,
    title text NOT NULL DEFAULT '',
    title_count integer NOT NULL DEFAULT 0,
    PRIMARY KEY (check_name, position)
);
COMMENT ON TABLE check_found_titles IS 'The titles findings of a check had, counted';

CREATE TABLE check_found_kinds (
    check_name text NOT NULL REFERENCES check_found (check_name) ON DELETE CASCADE,
    kind text NOT NULL,
    kind_count integer NOT NULL DEFAULT 0,
    PRIMARY KEY (check_name, kind)
);
COMMENT ON TABLE check_found_kinds IS 'The kinds of object findings of a check were about, counted';

CREATE TABLE check_found_findings (
    check_name text NOT NULL REFERENCES check_found (check_name) ON DELETE CASCADE,
    position integer NOT NULL,
    run_key text NOT NULL DEFAULT '',
    finding_key text NOT NULL DEFAULT '',
    found_at timestamptz,
    -- The check as the finding named it.
    found_check text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '',
    object text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    severity text NOT NULL DEFAULT '',
    routing text NOT NULL DEFAULT '',
    case_key text NOT NULL DEFAULT '',
    known_issue_key text NOT NULL DEFAULT '',
    exception_key text NOT NULL DEFAULT '',
    PRIMARY KEY (check_name, position)
);
COMMENT ON TABLE check_found_findings IS 'The newest findings of a check, for its drawer';

CREATE TABLE check_found_descriptions (
    check_name text NOT NULL REFERENCES check_found (check_name) ON DELETE CASCADE,
    position integer NOT NULL,
    description_key text NOT NULL DEFAULT '',
    job_id text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    run_key text NOT NULL DEFAULT '',
    described_at timestamptz,
    PRIMARY KEY (check_name, position)
);
COMMENT ON TABLE check_found_descriptions IS 'How agents described an id they made up, the newest';
