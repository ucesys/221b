-- Mycroft's sources, the syncs' state and the page changes that wait for review, with
-- every field a column (DB-48 to DB-50). The tables of step 0011 named here held no rows
-- until this step.

DROP TABLE source_exclusions, knowledge_sources, wiki_changes;

-- A source is known by the id the console gives it ("console", "added", or the one a
-- SuperAdmin chose): the wiki's manifest, the syncs' state and the audit log name a source
-- by it.
CREATE TABLE knowledge_sources (
    knowledge_source_id text PRIMARY KEY,
    position integer NOT NULL DEFAULT 0,
    name text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    -- False: not read; its pages stay and grow stale.
    is_enabled boolean NOT NULL DEFAULT true,
    -- A Confluence space.
    base_url text NOT NULL DEFAULT '',
    flavor text NOT NULL DEFAULT '',
    email text NOT NULL DEFAULT '',
    space text NOT NULL DEFAULT '',
    -- A git repository, or a web site's start address.
    url text NOT NULL DEFAULT '',
    branch text NOT NULL DEFAULT '',
    path text NOT NULL DEFAULT '',
    username text NOT NULL DEFAULT '',
    -- The repository of Administration > Settings > Repositories it reads, by its name
    -- there: it may be gone.
    repository_name text NOT NULL DEFAULT '',
    -- A bucket of documents.
    bucket text NOT NULL DEFAULT '',
    prefix text NOT NULL DEFAULT '',
    region text NOT NULL DEFAULT '',
    endpoint text NOT NULL DEFAULT '',
    is_path_style boolean NOT NULL DEFAULT false,
    -- keys (in the Secret) or identity (the pod's).
    credentials_kind text NOT NULL DEFAULT '',
    -- How a live source or a web site signs in, and how far a web site's read goes.
    auth text NOT NULL DEFAULT '',
    depth integer NOT NULL DEFAULT 0,
    page_limit integer NOT NULL DEFAULT 0,
    -- The web pages of Added in Mycroft a web site was made from at the upgrade.
    migrated_from text[] NOT NULL DEFAULT '{}',
    ca_pem text NOT NULL DEFAULT '',
    -- The certificate a SuperAdmin trusted; none when none is pinned.
    pinned_cert_sha256 text,
    pinned_cert_subject text NOT NULL DEFAULT '',
    pinned_cert_issuer text NOT NULL DEFAULT '',
    pinned_cert_not_after timestamptz,
    pinned_by text NOT NULL DEFAULT '',
    pinned_at timestamptz,
    added_at timestamptz,
    added_by text NOT NULL DEFAULT '',
    -- The credential of Administration > Credentials it signs in with, by its id: the
    -- credentials' own records are still in a Secret.
    credential_id text NOT NULL DEFAULT '',
    is_credential_migrated boolean NOT NULL DEFAULT false
);
COMMENT ON TABLE knowledge_sources IS 'Sources Mycroft reads: Confluence, Git, S3, web sites, live addresses, uploads, the console and the fixes';

CREATE TABLE knowledge_source_addresses (
    knowledge_source_id text NOT NULL REFERENCES knowledge_sources (knowledge_source_id) ON DELETE CASCADE,
    position integer NOT NULL,
    kind text NOT NULL DEFAULT '',
    url text NOT NULL DEFAULT '',
    space text NOT NULL DEFAULT '',
    PRIMARY KEY (knowledge_source_id, position)
);
COMMENT ON TABLE knowledge_source_addresses IS 'The addresses of a live source: never copied, read when an agent looks them up';

CREATE TABLE source_exclusions (
    knowledge_source_id text NOT NULL REFERENCES knowledge_sources (knowledge_source_id) ON DELETE CASCADE,
    position integer NOT NULL,
    pattern text NOT NULL DEFAULT '',
    is_under boolean NOT NULL DEFAULT false,
    is_item boolean NOT NULL DEFAULT false,
    title text NOT NULL DEFAULT '',
    reason text NOT NULL DEFAULT '',
    excluded_by text NOT NULL DEFAULT '',
    excluded_at timestamptz NOT NULL,
    PRIMARY KEY (knowledge_source_id, position)
);
COMMENT ON TABLE source_exclusions IS 'Items or patterns a source does not read';

-- What was last read of each source. It names a source by its id and not by a key: the
-- state of a source is kept whether or not the settings still list it.
CREATE TABLE knowledge_source_reads (
    knowledge_source_id text PRIMARY KEY,
    last_read_at timestamptz,
    item_count integer NOT NULL DEFAULT 0,
    error text NOT NULL DEFAULT '',
    reported_count integer NOT NULL DEFAULT 0,
    skipped_count integer NOT NULL DEFAULT 0,
    skipped_why text NOT NULL DEFAULT '',
    -- The last read of every item.
    last_full_at timestamptz
);
COMMENT ON TABLE knowledge_source_reads IS 'The last read of each source: when, how many items, what it skipped, why it failed';

-- The syncs: the last one whole (slot last) and the newest few in short (slot history).
CREATE TABLE knowledge_syncs (
    knowledge_sync_id uuid PRIMARY KEY,
    slot text NOT NULL CHECK (slot IN ('last', 'history')),
    position integer NOT NULL DEFAULT 0,
    -- The run of page changes it made, by its id.
    run_key text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    started_by text NOT NULL DEFAULT '',
    is_full boolean NOT NULL DEFAULT false,
    is_rebuild boolean NOT NULL DEFAULT false,
    read_count integer NOT NULL DEFAULT 0,
    unchanged_count integer NOT NULL DEFAULT 0,
    gone_count integer NOT NULL DEFAULT 0,
    page_count integer NOT NULL DEFAULT 0,
    proposal_count integer NOT NULL DEFAULT 0,
    reported text[] NOT NULL DEFAULT '{}',
    errors text[] NOT NULL DEFAULT '{}',
    not_proposed text[] NOT NULL DEFAULT '{}',
    summary text NOT NULL DEFAULT '',
    -- The one source it read, when it read one, by its id, with its name then.
    knowledge_source_id text NOT NULL DEFAULT '',
    source_name text NOT NULL DEFAULT '',
    skipped_count integer NOT NULL DEFAULT 0,
    skipped_why text NOT NULL DEFAULT '',
    unsent_count integer NOT NULL DEFAULT 0,
    model text NOT NULL DEFAULT '',
    tokens_in integer NOT NULL DEFAULT 0,
    tokens_out integer NOT NULL DEFAULT 0,
    cost_usd double precision NOT NULL DEFAULT 0,
    fixes text[] NOT NULL DEFAULT '{}',
    contradictions text[] NOT NULL DEFAULT '{}',
    UNIQUE (slot, position)
);
COMMENT ON TABLE knowledge_syncs IS 'The syncs of Mycroft''s sources: the last one, and the newest few in short';

CREATE TABLE knowledge_sync_batches (
    knowledge_sync_id uuid NOT NULL REFERENCES knowledge_syncs (knowledge_sync_id) ON DELETE CASCADE,
    position integer NOT NULL,
    items text[] NOT NULL DEFAULT '{}',
    outcome text NOT NULL DEFAULT '',
    page_count integer NOT NULL DEFAULT 0,
    note text NOT NULL DEFAULT '',
    PRIMARY KEY (knowledge_sync_id, position)
);
COMMENT ON TABLE knowledge_sync_batches IS 'How each batch of items given to Mycroft in a sync went';

-- A run is one sync's page changes, known by the id the console gives it.
CREATE TABLE wiki_runs (
    wiki_run_id uuid PRIMARY KEY,
    run_key text NOT NULL UNIQUE,
    position integer NOT NULL DEFAULT 0,
    started_at timestamptz NOT NULL,
    -- What started it, and the person who did, if one did.
    started_by text NOT NULL DEFAULT '',
    started_by_name text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT '',
    -- Where the wiki was kept then.
    store_kind text NOT NULL DEFAULT '',
    reported text[] NOT NULL DEFAULT '{}',
    decided_at timestamptz
);
COMMENT ON TABLE wiki_runs IS 'Mycroft''s runs of page changes waiting for review, and the newest decided ones';

CREATE TABLE wiki_changes (
    wiki_run_id uuid NOT NULL REFERENCES wiki_runs (wiki_run_id) ON DELETE CASCADE,
    position integer NOT NULL,
    path text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    is_critical boolean NOT NULL DEFAULT false,
    is_new boolean NOT NULL DEFAULT false,
    -- The version of the page the change is over.
    base_version text NOT NULL DEFAULT '',
    diff text NOT NULL DEFAULT '',
    added_count integer NOT NULL DEFAULT 0,
    removed_count integer NOT NULL DEFAULT 0,
    is_clipped boolean NOT NULL DEFAULT false,
    state text NOT NULL DEFAULT '',
    reason text NOT NULL DEFAULT '',
    approval_request_id uuid REFERENCES approval_requests (approval_request_id) ON DELETE SET NULL,
    PRIMARY KEY (wiki_run_id, position)
);
CREATE INDEX wiki_changes_path_idx ON wiki_changes (path);
COMMENT ON TABLE wiki_changes IS 'The page changes of a run: the diff, its state and why';

CREATE TABLE wiki_change_sources (
    wiki_run_id uuid NOT NULL,
    change_position integer NOT NULL,
    position integer NOT NULL,
    source_key text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    version text NOT NULL DEFAULT '',
    dated_at timestamptz,
    url text NOT NULL DEFAULT '',
    PRIMARY KEY (wiki_run_id, change_position, position),
    FOREIGN KEY (wiki_run_id, change_position) REFERENCES wiki_changes (wiki_run_id, position) ON DELETE CASCADE
);
COMMENT ON TABLE wiki_change_sources IS 'The source items a page change draws on';

CREATE TABLE wiki_change_approvals (
    wiki_run_id uuid NOT NULL,
    change_position integer NOT NULL,
    position integer NOT NULL,
    approved_by text NOT NULL DEFAULT '',
    approved_by_name text NOT NULL DEFAULT '',
    approved_at timestamptz NOT NULL,
    PRIMARY KEY (wiki_run_id, change_position, position),
    FOREIGN KEY (wiki_run_id, change_position) REFERENCES wiki_changes (wiki_run_id, position) ON DELETE CASCADE
);
COMMENT ON TABLE wiki_change_approvals IS 'Who approved a page change so far';

CREATE TABLE wiki_change_contradictions (
    wiki_run_id uuid NOT NULL,
    change_position integer NOT NULL,
    position integer NOT NULL,
    -- The fix, by the id the console gives it.
    fix_key text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    case_count integer NOT NULL DEFAULT 0,
    why text NOT NULL DEFAULT '',
    PRIMARY KEY (wiki_run_id, change_position, position),
    FOREIGN KEY (wiki_run_id, change_position) REFERENCES wiki_changes (wiki_run_id, position) ON DELETE CASCADE
);
COMMENT ON TABLE wiki_change_contradictions IS 'The fixes a page change contradicts';

CREATE TABLE wiki_run_decisions (
    wiki_run_id uuid NOT NULL REFERENCES wiki_runs (wiki_run_id) ON DELETE CASCADE,
    position integer NOT NULL,
    decided_by text NOT NULL DEFAULT '',
    decided_by_name text NOT NULL DEFAULT '',
    decided_at timestamptz NOT NULL,
    action text NOT NULL DEFAULT '',
    comment text NOT NULL DEFAULT '',
    pages text[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (wiki_run_id, position)
);
COMMENT ON TABLE wiki_run_decisions IS 'What people decided on a run: who, when, which pages';

CREATE TABLE wiki_page_requests (
    wiki_page_request_id uuid PRIMARY KEY,
    request_key text NOT NULL UNIQUE,
    position integer NOT NULL DEFAULT 0,
    action text NOT NULL DEFAULT '',
    path text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    reason text NOT NULL DEFAULT '',
    requested_by text NOT NULL DEFAULT '',
    requested_by_name text NOT NULL DEFAULT '',
    requested_at timestamptz NOT NULL,
    state text NOT NULL DEFAULT '',
    decided_by text NOT NULL DEFAULT '',
    decided_by_name text NOT NULL DEFAULT '',
    decided_at timestamptz,
    comment text NOT NULL DEFAULT ''
);
COMMENT ON TABLE wiki_page_requests IS 'Requests to retire a wiki page or make it active again, which a second person approves';

CREATE TABLE wiki_corrections (
    wiki_correction_id uuid PRIMARY KEY,
    correction_key text NOT NULL UNIQUE,
    position integer NOT NULL DEFAULT 0,
    path text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    sent_by text NOT NULL DEFAULT '',
    sent_by_name text NOT NULL DEFAULT '',
    sent_at timestamptz NOT NULL,
    state text NOT NULL DEFAULT '',
    -- The run that rewrote the page for it, by its id.
    run_key text NOT NULL DEFAULT '',
    note text NOT NULL DEFAULT ''
);
COMMENT ON TABLE wiki_corrections IS 'The corrections people sent with This is outdated, and what became of each';
