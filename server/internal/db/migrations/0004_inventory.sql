-- Inventory (docs/data-model.md). Keys are UUIDv7 made by the console unless the
-- model names a natural key; every key is named for its table.

CREATE TABLE fleets (
    fleet_id uuid PRIMARY KEY,
    cluster_id text,
    name text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    labels jsonb,
    taints jsonb,
    deploy_label text NOT NULL DEFAULT '',
    color text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    privilege_ceiling text NOT NULL DEFAULT '',
    reboot_minutes integer
);
COMMENT ON TABLE fleets IS 'Fleets per cluster: name, kind (list or selector), the selector''s labels and taints, the privilege ceiling and reboot timeout';

CREATE TABLE fleet_hosts (
    fleet_id uuid NOT NULL,
    host_id uuid NOT NULL,
    PRIMARY KEY (fleet_id, host_id)
);
COMMENT ON TABLE fleet_hosts IS 'Members of list fleets';

CREATE TABLE hosts (
    host_id uuid PRIMARY KEY,
    cluster_id text,
    data_center_id uuid,
    name text NOT NULL DEFAULT '',
    node_uid text NOT NULL DEFAULT '',
    is_kubernetes_node boolean GENERATED ALWAYS AS (node_uid <> '') STORED,
    address text NOT NULL DEFAULT '',
    port integer,
    state text NOT NULL DEFAULT '',
    rack_name text NOT NULL DEFAULT '',
    privilege_ceiling text NOT NULL DEFAULT '',
    reboot_minutes integer,
    added_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE hosts IS 'Kubernetes nodes and external SSH hosts. Nodes keep identity only (cluster, name, uid); external hosts add address, port, state, data center and rack';

CREATE TABLE data_centers (
    data_center_id uuid PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    site text NOT NULL DEFAULT '',
    time_zone text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT ''
);
COMMENT ON TABLE data_centers IS 'Data centers external hosts stand in: name, site, time zone';

CREATE TABLE host_facts (
    host_id uuid PRIMARY KEY,
    facts jsonb,
    read_at timestamptz,
    error text NOT NULL DEFAULT ''
);
COMMENT ON TABLE host_facts IS 'Latest facts gathered from each host, network ones included: interfaces, bonds, subnets, routes, DNS, time sync, LLDP switch';

CREATE TABLE host_pairings (
    host_id uuid PRIMARY KEY,
    host_keys text[] NOT NULL DEFAULT '{}',
    ca_fingerprint text NOT NULL DEFAULT '',
    trusted_by text NOT NULL DEFAULT '',
    paired_by text NOT NULL DEFAULT '',
    paired_at timestamptz
);
COMMENT ON TABLE host_pairings IS 'SSH trust per host: pinned host keys, authority fingerprint, who trusted and paired it';

CREATE TABLE host_logins (
    host_id uuid NOT NULL,
    login_user text NOT NULL,
    principal text NOT NULL DEFAULT '',
    privilege text NOT NULL DEFAULT '',
    PRIMARY KEY (host_id, login_user)
);
COMMENT ON TABLE host_logins IS 'Login accounts per host and the privilege each serves; a run uses the login that matches its profile';

-- Keys.
ALTER TABLE fleets ADD CONSTRAINT fleets_cluster_id_fkey
    FOREIGN KEY (cluster_id) REFERENCES clusters (cluster_id) ON DELETE SET NULL;
CREATE INDEX fleets_cluster_id_idx ON fleets (cluster_id);
ALTER TABLE fleet_hosts ADD CONSTRAINT fleet_hosts_fleet_id_fkey
    FOREIGN KEY (fleet_id) REFERENCES fleets (fleet_id) ON DELETE CASCADE;
ALTER TABLE fleet_hosts ADD CONSTRAINT fleet_hosts_host_id_fkey
    FOREIGN KEY (host_id) REFERENCES hosts (host_id) ON DELETE CASCADE;
CREATE INDEX fleet_hosts_host_id_idx ON fleet_hosts (host_id);
ALTER TABLE hosts ADD CONSTRAINT hosts_cluster_id_fkey
    FOREIGN KEY (cluster_id) REFERENCES clusters (cluster_id) ON DELETE SET NULL;
CREATE INDEX hosts_cluster_id_idx ON hosts (cluster_id);
ALTER TABLE hosts ADD CONSTRAINT hosts_data_center_id_fkey
    FOREIGN KEY (data_center_id) REFERENCES data_centers (data_center_id) ON DELETE SET NULL;
CREATE INDEX hosts_data_center_id_idx ON hosts (data_center_id);
ALTER TABLE host_facts ADD CONSTRAINT host_facts_host_id_fkey
    FOREIGN KEY (host_id) REFERENCES hosts (host_id) ON DELETE CASCADE;
ALTER TABLE host_pairings ADD CONSTRAINT host_pairings_host_id_fkey
    FOREIGN KEY (host_id) REFERENCES hosts (host_id) ON DELETE CASCADE;
ALTER TABLE host_logins ADD CONSTRAINT host_logins_host_id_fkey
    FOREIGN KEY (host_id) REFERENCES hosts (host_id) ON DELETE CASCADE;
