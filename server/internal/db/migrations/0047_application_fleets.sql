-- An application's fleet is a key (DB-92). Since step 0035 an application named the
-- fleet it is deployed to by the fleet's id within its cluster (fleet_key), as text. A
-- fleet keeps its row's id through a save of the fleets (settings/pg.go updates a fleet
-- found by its cluster and its id, and deletes only a fleet that was removed), so the
-- application keys to the row: fleet_id, emptied when the fleet is removed, with
-- fleet_key kept beside it as the history's name, which the deploy page shows.

ALTER TABLE applications ADD COLUMN fleet_id uuid REFERENCES fleets (fleet_id) ON DELETE SET NULL;
CREATE INDEX applications_fleet_id_idx ON applications (fleet_id);

UPDATE applications a SET fleet_id = (
    SELECT f.fleet_id FROM fleets f
    WHERE coalesce(f.cluster_id, '') = a.cluster_id AND f.fleet_key = a.fleet_key
    ORDER BY f.fleet_id LIMIT 1)
WHERE a.fleet_key <> '';
