-- Every column is written, and what the code looks rows up by is unique (DB-93).
--
-- Columns nothing wrote. A Toolbox session's card is a Watson card, not a proposed
-- change: change_id goes, and card_key keeps the card's id, which the console has for
-- every session. ends_at is filled from now on with the session's hard limit as it
-- ended. transcript_ref goes: a transcript's file is named by the session's id, and
-- is_recorded says whether there is one. A node's row is its cluster and name: node_uid
-- goes. A host is reached as one login, and the privilege is the host's ceiling:
-- host_logins.privilege goes. data_centers, hosts.data_center_id and hosts.rack_name stay
-- for the page that fills them (the owner, November 19, 2026).
ALTER TABLE toolbox_sessions DROP COLUMN change_id, DROP COLUMN transcript_ref,
    ADD COLUMN card_key text NOT NULL DEFAULT '';
ALTER TABLE hosts DROP COLUMN node_uid;
ALTER TABLE host_logins DROP COLUMN privilege;

-- Unique. A database may hold rows a race made twice before these were declared; none is
-- deleted that holds what people set. A second fleet of one key in a cluster, and a
-- second host of one name, keep their rows under the key or name with "~" and the first
-- eight digits of their id, so the step never fails and nothing is lost: the fleet the
-- settings list (selector or list) is kept under its key before a row made for its
-- variables, else the oldest. A second acknowledgement of one alert is deleted, the
-- newest kept. A second request of one number is given a number after the greatest, and
-- the counter the numbers come from is moved past it.
WITH twice AS (
    SELECT fleet_id, row_number() OVER (PARTITION BY coalesce(cluster_id, ''), fleet_key
        ORDER BY kind IN ('selector', 'list') DESC, fleet_id) AS n FROM fleets)
UPDATE fleets f SET fleet_key = f.fleet_key || '~' || left(f.fleet_id::text, 8)
    FROM twice WHERE twice.fleet_id = f.fleet_id AND twice.n > 1;
CREATE UNIQUE INDEX fleets_cluster_id_fleet_key_key ON fleets ((coalesce(cluster_id, '')), fleet_key);

WITH twice AS (
    SELECT host_id, row_number() OVER (PARTITION BY coalesce(cluster_id, ''), name ORDER BY created_at, host_id) AS n FROM hosts)
UPDATE hosts h SET name = h.name || '~' || left(h.host_id::text, 8)
    FROM twice WHERE twice.host_id = h.host_id AND twice.n > 1;
CREATE UNIQUE INDEX hosts_cluster_id_name_key ON hosts ((coalesce(cluster_id, '')), name);

DELETE FROM alert_acks a USING (
    SELECT alert_ack_id, row_number() OVER (PARTITION BY cluster_id, alert_key ORDER BY acked_at DESC, alert_ack_id DESC) AS n FROM alert_acks) twice
    WHERE twice.alert_ack_id = a.alert_ack_id AND twice.n > 1;
ALTER TABLE alert_acks ADD CONSTRAINT alert_acks_cluster_id_alert_key_key UNIQUE (cluster_id, alert_key);

WITH top AS (SELECT coalesce(max(request_number), 0) AS n FROM approval_requests),
twice AS (
    SELECT approval_request_id, row_number() OVER (PARTITION BY request_number ORDER BY created_at, approval_request_id) AS n FROM approval_requests),
renumbered AS (
    SELECT approval_request_id, row_number() OVER (ORDER BY approval_request_id) AS k FROM twice WHERE n > 1)
UPDATE approval_requests r SET request_number = top.n + renumbered.k
    FROM top, renumbered WHERE renumbered.approval_request_id = r.approval_request_id;
UPDATE settings SET value = jsonb_build_object('next', greatest(coalesce((value->>'next')::integer, 0), (SELECT max(request_number) FROM approval_requests)))
    WHERE scope = 'approvals' AND coalesce((value->>'next')::integer, 0) < (SELECT coalesce(max(request_number), 0) FROM approval_requests);
INSERT INTO settings (scope, value, version)
    SELECT 'approvals', jsonb_build_object('next', max(request_number)), 1 FROM approval_requests
    HAVING max(request_number) > 0 AND NOT EXISTS (SELECT 1 FROM settings WHERE scope = 'approvals');
ALTER TABLE approval_requests ADD CONSTRAINT approval_requests_request_number_key UNIQUE (request_number);
