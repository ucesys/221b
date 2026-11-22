-- The list of cases is a query (DB-88): the lists read the rows of the cases that pass
-- their filter and that the person may see, not every case's row. These indexes serve
-- what they filter on: who reported, filed, owns and is involved in a case, its
-- service, known issue, cluster and outcome, and the cases kept, newest first.

CREATE INDEX cases_list_idx ON cases (case_number DESC) WHERE is_case_kept;
CREATE INDEX cases_reporter_key_idx ON cases (reporter_key);
CREATE INDEX cases_filed_by_key_idx ON cases (filed_by_key);
CREATE INDEX cases_owner_key_idx ON cases (owner_key);
CREATE INDEX cases_involved_idx ON cases USING gin (involved);
CREATE INDEX cases_service_key_idx ON cases (service_key);
CREATE INDEX cases_known_issue_key_idx ON cases (known_issue_key);
CREATE INDEX cases_cluster_id_idx ON cases (cluster_id);
CREATE INDEX cases_outcome_idx ON cases (outcome);
CREATE INDEX case_people_watcher_idx ON case_people (person_key, case_id) WHERE role = 'watcher';
