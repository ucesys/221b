-- Automation's variables as the model has them (DB-33): each on one fleet or on one host.
-- A node's own values are the node's, whatever fleets it is in; the privilege ceiling and
-- the reboot time are columns of fleets and hosts. The table held no rows until this step.

-- No value: the fleet only marks the name as sensitive. An empty string is a value.
ALTER TABLE variables ALTER COLUMN value DROP NOT NULL, ALTER COLUMN value DROP DEFAULT;
CREATE UNIQUE INDEX variables_fleet_id_name_key ON variables (fleet_id, name);
CREATE UNIQUE INDEX variables_host_id_name_key ON variables (host_id, name);
COMMENT ON COLUMN variables.scope IS 'fleet or host: which of fleet_id and host_id is set';
COMMENT ON COLUMN variables.value IS 'The value; none where a fleet only marks the name as sensitive';

-- Two more kinds of fleet, which the settings'' list of fleets leaves out. all: every node
-- of the cluster, the fleet under every other; it holds All nodes'' variables, ceiling and
-- reboot time. unlisted: a fleet that variables name and the list does not have (not
-- copied yet, or removed before its variables were); it goes when nothing is set on it.
COMMENT ON COLUMN fleets.kind IS 'selector: the nodes that have its labels and taints. list: the hosts listed. all: every node of the cluster. unlisted: named by variables only';
COMMENT ON TABLE fleets IS 'Fleets per cluster: name, kind, the selector''s labels and taints, the privilege ceiling and reboot timeout';
