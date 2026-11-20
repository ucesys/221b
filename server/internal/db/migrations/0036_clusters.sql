-- The cluster connections are rows (DB-64): what the console keeps of a connection
-- beside its credential was the key "spec.json" of the connection's Secret, and is now
-- a row of clusters with every field a column. The credential stays in that Secret
-- (<prefix>-cluster-<cluster_id>), whole: the token, the kubeconfig, the client
-- certificate and key, the OIDC client's secret and refresh token, and beside them what
-- is read only together with them (the CA certificate, a kubeconfig's context, the OIDC
-- issuer, client id and scopes). Nothing of it is in this table.
--
-- The table already holds a row with only its id for each cluster an acknowledged
-- alert, an application, a fleet or a host points at (step 0001, inventory.EnsureCluster,
-- apps/pg_apps.go, settings/pg.go), so it is altered, never dropped: those rows stay,
-- with is_kept false, until the copy of the connections fills them.

ALTER TABLE clusters
    -- The Secret's name follows from the id.
    DROP COLUMN secret_ref,
    -- False for a row made with only its id, for what points at a cluster whose own
    -- connection is not here (or is no longer): the connections are the kept rows.
    ADD COLUMN is_kept boolean NOT NULL DEFAULT false,
    -- How the API server's certificate is checked (pem, system, skip), and what the
    -- saved CA certificate says of itself: its subject and expiry, never the certificate.
    ADD COLUMN ca_mode text NOT NULL DEFAULT '',
    ADD COLUMN ca_info text NOT NULL DEFAULT '',
    -- What the saved credential says about itself, never the credential: who it signs
    -- in as, a kubeconfig's context and cluster, an OIDC issuer and client id, when it
    -- stops working, when it was saved.
    ADD COLUMN cred_who text NOT NULL DEFAULT '',
    ADD COLUMN cred_context text NOT NULL DEFAULT '',
    ADD COLUMN cred_cluster text NOT NULL DEFAULT '',
    ADD COLUMN cred_issuer text NOT NULL DEFAULT '',
    ADD COLUMN cred_client_id text NOT NULL DEFAULT '',
    ADD COLUMN cred_expires_at timestamptz,
    ADD COLUMN cred_set_at timestamptz,
    -- Counts the row's writes, so a replica sees what another one changed.
    ADD COLUMN version integer NOT NULL DEFAULT 0;

COMMENT ON TABLE clusters IS 'Cluster connections, keyed by the console''s cluster id, every field but the credential a column; the credential stays in a Secret. A row with is_kept false holds only the id of a cluster something points at';
