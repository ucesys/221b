-- One case for one failure (CF-117 to CF-119; decided by the owner on November 23,
-- 2026). The alerts attached to a case as its evidence, each by its cluster and key (an
-- alert has no row of its own: the key is the console's, "pod:shop/web-1"), with its
-- name and object as they were, when, by whom and why it was attached. The key a
-- case groups the alerts of one failure under ("node:<cluster>/<node>"), which the
-- safety net looks open cases up by. The case a case was merged into, keyed while that
-- case has a row, its number kept beside the key as the page shows it after. What the
-- case analysis says another case shares the cause with, and which case it names the
-- root, as people read them ("C-13"). The rows already there have none of these.

CREATE TABLE case_signals (
    case_signal_id uuid PRIMARY KEY,
    case_id uuid NOT NULL REFERENCES cases (case_id) ON DELETE CASCADE,
    position integer NOT NULL,
    cluster_id text NOT NULL DEFAULT '',
    alert_key text NOT NULL,
    alert_name text NOT NULL DEFAULT '',
    object text NOT NULL DEFAULT '',
    attached_at timestamptz NOT NULL,
    -- Who attached it: the console's alert safety net or a person, by key and name.
    attached_by_key text NOT NULL DEFAULT '',
    attached_by_name text NOT NULL DEFAULT '',
    reason text NOT NULL DEFAULT '',
    UNIQUE (case_id, position)
);
CREATE INDEX case_signals_alert_idx ON case_signals (cluster_id, alert_key);
COMMENT ON TABLE case_signals IS 'The alerts attached to a case as its evidence: same node, merged from another case';

ALTER TABLE cases ADD COLUMN correlation_key text NOT NULL DEFAULT '';
CREATE INDEX cases_correlation_key_idx ON cases (correlation_key) WHERE correlation_key <> '';

ALTER TABLE cases ADD COLUMN merged_into_case_number integer NOT NULL DEFAULT 0;
ALTER TABLE cases ADD COLUMN merged_into_case_id uuid REFERENCES cases (case_id) ON DELETE SET NULL;
CREATE INDEX cases_merged_into_case_id_idx ON cases (merged_into_case_id);

ALTER TABLE case_analyses ADD COLUMN same_cause_as_case_keys text[] NOT NULL DEFAULT '{}';
ALTER TABLE case_analyses ADD COLUMN root_case_key text NOT NULL DEFAULT '';
