-- A run records the fleets its nodes were in when it started, and the values each
-- fleet had then for the script's parameters (DB-38, the code to-do list's item 11).
ALTER TABLE script_run_fleets
    -- The fleet as the run named it; fleet_id is its row while it exists.
    ADD COLUMN fleet_key text NOT NULL DEFAULT '',
    ADD COLUMN position integer NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX script_run_fleets_script_run_id_fleet_key_key ON script_run_fleets (script_run_id, fleet_key);

-- A fleet's values go with its row.
ALTER TABLE script_run_values
    DROP CONSTRAINT script_run_values_script_run_fleet_id_fkey;
ALTER TABLE script_run_values ADD CONSTRAINT script_run_values_script_run_fleet_id_fkey
    FOREIGN KEY (script_run_fleet_id) REFERENCES script_run_fleets (script_run_fleet_id) ON DELETE CASCADE;

-- A node's fleets in the order its values were looked for: its own fleets, then All nodes.
ALTER TABLE script_run_host_fleets ADD COLUMN position integer NOT NULL DEFAULT 0;
