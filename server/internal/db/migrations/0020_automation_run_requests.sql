-- The runs that wait for approval are rows of script_runs too (DB-37), so that one
-- list has every run: the ones that ran, and the ones that wait, were rejected or
-- expired. A run whose own values needed approval and the request that waited for it
-- are one row. Until this step the requests were kept whole in a settings scope.

ALTER TABLE script_runs
    -- A request that is rejected or expires never becomes a run, and has no number.
    ALTER COLUMN run_number DROP NOT NULL,
    -- The request's own number; requests and runs are numbered apart.
    ADD COLUMN request_number integer,
    -- waiting, approved, rejected or expired; none for a run that needed no approval.
    ADD COLUMN request_state text,
    -- The page's name for the request: asked twice, it waits once.
    ADD COLUMN request_client_key text NOT NULL DEFAULT '',
    ADD COLUMN requested_by text NOT NULL DEFAULT '',
    ADD COLUMN requested_by_user text NOT NULL DEFAULT '',
    ADD COLUMN requested_at timestamptz,
    -- How the asker signed in: local, ldap or oidc.
    ADD COLUMN request_method text NOT NULL DEFAULT '',
    -- The nodes the request named, none for the whole fleet; and the nodes it would
    -- run on when it was asked for.
    ADD COLUMN request_nodes text[] NOT NULL DEFAULT '{}',
    ADD COLUMN request_targets text[] NOT NULL DEFAULT '{}',
    -- Who the cluster sees, which the run is started as once it is approved.
    ADD COLUMN person_user text NOT NULL DEFAULT '',
    ADD COLUMN person_groups text[] NOT NULL DEFAULT '{}',
    -- The run's values that differ from what a node inherits.
    ADD COLUMN request_changed text[] NOT NULL DEFAULT '{}',
    -- What could not be weighed: Sherlock was off, or failed.
    ADD COLUMN request_note text NOT NULL DEFAULT '',
    ADD COLUMN decided_by text,
    ADD COLUMN decided_by_user text,
    ADD COLUMN decided_at timestamptz,
    -- The reason given with a rejection, or after how long it expired.
    ADD COLUMN decision_comment text NOT NULL DEFAULT '',
    -- The number of the run the approval started, kept when that run's row is gone.
    ADD COLUMN started_run_number integer;
CREATE UNIQUE INDEX script_runs_request_number_key ON script_runs (request_number);
COMMENT ON TABLE script_runs IS 'Runs, and the requests for a run that wait for approval, were rejected or expired: the script version, profile and fleet each names, its rollout, state, who asked and who started it';
COMMENT ON COLUMN script_run_values.scope IS 'run: a value the run gave itself. host: what the host got. fleet: what a fleet of the run had. request: a value a request asked to run with';

CREATE TABLE script_run_impacts (
    script_run_id uuid NOT NULL REFERENCES script_runs (script_run_id) ON DELETE CASCADE,
    position integer NOT NULL,
    script text NOT NULL DEFAULT '',
    version_number integer NOT NULL DEFAULT 0,
    -- safe, or approval.
    decision text NOT NULL DEFAULT '',
    why text NOT NULL DEFAULT '',
    -- rule, for what the console worked out, or sherlock.
    decided_by text NOT NULL DEFAULT '',
    PRIMARY KEY (script_run_id, position)
);
COMMENT ON TABLE script_run_impacts IS 'What a request''s own values mean for the script it would run, and whether that needs approval';
