# The data model

What 221B keeps in PostgreSQL (Phase 13): 73 tables in eleven domains and three tiers. The schema itself is the SQL in [`server/internal/db/migrations`](../server/internal/db/migrations), one file per domain; this page says what each table holds and the rules every table follows. [database.md](database.md) says how the database is run, and which domains have moved to it so far.

Each table's columns are listed with their marks: **PK** is the key, **FK** a key into another table, **FK?** one that may be empty, and ↗ a key into another domain.

## The rules

- **Keys point down the tiers or stay inside one.** The AIOps flow may key into the engines and the foundation, the engines into the foundation, and the four AIOps domains into each other along the flow. A foundation table never keys up: where it records something higher (an acknowledgement's case, a Toolbox session's change, a policy's agent), it keeps the id as a plain column.
- **Every key is named for its table.** A primary key is `<entity>_id` (`fleet_id`, `script_run_id`); no table has a bare `id`. A foreign key has the name of the key it points to; a second key into the same table adds a role (`retry_of_script_run_id`, `linked_case_id`). A join table's key is its pair of keys.
- **Natural keys only where the code already has a stable id:** `settings.scope`, `clusters.cluster_id`, `agents.agent_id`, `validation_rules.validation_rule_id` and `wiki_pages.path`. Every other key is a UUIDv7 the console makes (`db.NewID`), never a sequence; a number people read, such as `case_number`, is a column of its own.
- **Names say what a column holds.** Flags start with `is_`, times end in `_at`, people in `_by`, money in `_usd`, lists of ids in `_ids`.
- **History outlives what it points to.** A run, result or record that outlives its target holds a key that is emptied when the target is deleted, beside the target's name as text (`host_name`, `fleet_name`, `case_number`).
- **A cluster is `cluster_id` everywhere.** Only Platform and Inventory hold a real key to `clusters`. Inventory's is emptied when a cluster is removed, so list fleets and hosts outside Kubernetes outlive it; the other domains keep the id as a plain column, so history survives a removed connection.
- **Sign-in never depends on the database.** Local accounts stay in a Kubernetes Secret, and during a database outage a local user gets a short-lived signed session.
- **Kubernetes state is never stored.** A node's row holds its identity only (cluster, name, uid); labels, taints, status and topology are read live.
- **Observed host state is a fact, not a column.** What the facts run reports, network details included, is JSON in `host_facts`; the tables hold only what people set, such as a host's data center and rack.
- **A record is one row for its whole life.** Approval changes its state; there are no draft tables.
- **Versioned things keep their history in a versions table:** scripts and prompts.
- **JSON only for what is read whole:** facts, checks, rollout settings, evidence. Anything joined, filtered or pointed to has a table.
- **Ledgers are only added to.** `audit_events` and `ai_spend` are inserted and read, never updated (a trigger refuses it), so writers at once never collide. Retention deletes.
- **True singletons are JSON rows of `settings`,** read whole and written rarely, each over its version.
- **Secrets stay in Kubernetes.** A table holds only the name of a credential's, a token's, a provider key's or the SSH authority's Secret; a secret script parameter keeps only its Secret's name.
- **Bodies stay where they are.** Wiki pages and transcripts remain in git, S3 or a volume.

## Where the schema differs from the names above

- `check` and `window` are reserved words in SQL: the columns are `check_name` (in `findings`, `cluster_checks` and `exceptions`) and `time_window` (in `known_issues`).
- `audit_events` has `actor`, `actor_subject` and `actor_email` for who, `detail` as text (it is a sentence, searched by word), and `restored_at` for an event that came back from a backup.
- `toolbox_sessions.toolbox_session_id` is the Toolbox's own id, sixteen hex digits, not a UUID: the audit log and the transcripts' file names carry it. The table also has the session's `summary`, `node`, `owner_user`, `approved_by`, `reason`, `created_at`, `ended_by`, `end_note`, `is_recorded`, `byte_count`, `connection_count` and `watched_by`.
- `approval_policies.approval_policy_id` and `notification_channels.notification_channel_id` are the ids the console already gives them (`s…`, `n…`), not UUIDs: the audit log and the channels' Secret carry them. `approval_requests` has `request_number`, the number people read, and `requested_by_key`; a decision has `decided_by_key`; both lists keep a `position`. The next request's number and the notifications' link are scopes of `settings` (`approvals`, `notifications`).
- `hosts.is_kubernetes_node` is a plain flag, set by what made the row, since a node's uid is not known where its row is made; `hosts` also has `is_auto_named`. `host_pairings` has `fingerprint`, `trusted_at`, `offered_key` and `mismatch_key` for a host outside Kubernetes. `fleets` has `fleet_key`, the id a fleet has within its cluster, and `position`; `labels` and `taints` are text arrays of the requirements as written; `color` is the palette's number. `fleet_hosts` has `position`.
- `alert_acks.job_run_id`, `case_id` and `exception_id` are text: the ids those records carry today.
- `credentials.credential_id`, and the columns that point at it, are text: the id the console gives a credential. `repositories` has `position`, `username`, `is_secret_set`, `is_credential_migrated`, the pinned certificate's parts as `pinned_cert_*`, `pinned_by`, `pinned_at` and `test_result`.
- `clusters` and `credentials` hold, for now, a row with only its id for each cluster or credential another table points at; their own records are still in Secrets.
- Two tables are the database's own and not part of the model: `schema_migrations`, the steps applied, and `imports`, the record sets copied from the ConfigMaps.
- The columns of a domain that has not moved yet are the model's first cut: they are settled, and may change in place, when the domain's repository is written. Once a release ships a step, it is never edited.

## Foundation tier

### Platform

Clusters own their apps and acks; credentials sign in for sources. Schema: `0001_platform.sql`.

| Table | Holds | Columns |
| --- | --- | --- |
| `clusters` | Connection metadata per cluster, keyed by the console's cluster id; credentials stay in Secrets | `cluster_id` PK, `name`, `environment`, `description`, `server_url`, `auth_method`, `mode`, `is_local`, `secret_ref`, `created_at`, `updated_at` |
| `alert_acks` | Acknowledged alerts per cluster: who, when, why, and the run, case or exception that acknowledged them | `alert_ack_id` PK, `cluster_id` FK, `alert_key`, `fingerprint`, `is_expected`, `reason`, `job_run_id`, `case_id`, `exception_id`, `acked_by`, `acked_at` |
| `backup_runs` | Scheduled and manual backup runs and their results | `backup_run_id` PK, `slot_at`, `started_by`, `started_at`, `ended_at`, `is_ok`, `is_missed`, `object_key`, `size_bytes`, `pruned_count`, `error` |
| `settings` | One JSONB row per scope: general, Sherlock, Watson, email, backup, cases, getting started | `scope` PK, `value` JSON, `version`, `updated_by`, `updated_at` |
| `applications` | Helm apps deployed to a cluster: source kind and address, chart, ref, commit, values; may name a repository and a credential | `application_id` PK, `cluster_id` FK, `repository_id` FK?, `credential_id` FK?, `namespace`, `name`, `source_kind`, `source_url`, `chart`, `ref`, `commit`, `values` JSON, `updated_by` |
| `repositories` | Registered chart and runbook sources: Git repositories, Helm repositories and OCI registries | `repository_id` PK, `credential_id` FK?, `name`, `kind`, `url`, `default_branch`, `auth_method`, `pinned_cert`, `is_test_ok`, `tested_at`, `updated_by`, `updated_at` |
| `credentials` | Credentials of Administration › Credentials: name, kind, endpoint and test result; the secret itself stays in a Secret | `credential_id` PK, `name`, `kind`, `endpoint`, `description`, `username`, `region`, `pinned_cert`, `expires_at`, `secret_ref`, `last_test` JSON, `created_by`, `updated_at` |

An ack keeps the run, case and exception that acknowledged it as plain ids, since a foundation table never keys into a higher tier.

### Identity

Sessions live in the database; local accounts stay in a Secret. Schema: `0002_identity.sql`.

| Table | Holds | Columns |
| --- | --- | --- |
| `auth_sessions` | Signed-in browser sessions, shared by all replicas, keyed by a hash of the session id | `auth_session_id` PK, `subject`, `username`, `method`, `directory_ref`, `roles`, `super_roles`, `groups_read_at`, `created_at`, `last_seen_at`, `expires_at` |

Local users, local groups and memberships stay in a Kubernetes Secret, so a database outage never blocks them. During an outage a local user gets a short-lived signed session that any replica checks without the database. Sessions have no key to users: directory users from LDAP or OIDC are never stored.

### Approvals

Every request collects decisions from people or policies. Schema: `0003_approvals.sql`.

| Table | Holds | Columns |
| --- | --- | --- |
| `approval_requests` | What was asked, by whom, on which cluster, how many approvers are required, state, expiry | `approval_request_id` PK, `kind`, `cluster_id`, `summary` JSON, `requested_by`, `required_count`, `state`, `expires_at`, `created_at`, `decided_at` |
| `approval_decisions` | One row per approve, reject, withdraw or revoke: who, when, comment | `approval_decision_id` PK, `approval_request_id` FK, `approval_policy_id` FK?, `decided_by`, `verdict`, `comment`, `decided_at` |
| `approval_policies` | Rules that approve automatically (today's standing approvals) | `approval_policy_id` PK, `name`, `is_enabled`, `agent_id`, `cluster_ids`, `actions`, `namespaces`, `condition`, `per_hour`, `run_as`, `updated_by`, `updated_at` |
| `notification_channels` | Channels that tell approvers a request is waiting; their addresses stay in a Secret | `notification_channel_id` PK, `kind`, `name`, `is_enabled`, `secret_ref` |

Changes, script versions, runs, variable changes, prompt versions, wiki changes and Toolbox sessions carry an approval_request_id. A person's decision leaves approval_policy_id empty.

### Inventory

Kubernetes nodes and external hosts share one hosts table. Schema: `0004_inventory.sql`.

| Table | Holds | Columns |
| --- | --- | --- |
| `fleets` | Fleets per cluster: name, kind (list or selector), the selector's labels and taints, the privilege ceiling and reboot timeout | `fleet_id` PK, `cluster_id` FK? ↗, `name`, `kind`, `labels`, `taints`, `deploy_label`, `color`, `description`, `privilege_ceiling`, `reboot_minutes` |
| `fleet_hosts` | Members of list fleets | `fleet_id` PK FK, `host_id` PK FK |
| `hosts` | Kubernetes nodes and external SSH hosts. Nodes keep identity only (cluster, name, uid); external hosts add address, port, state, data center and rack | `host_id` PK, `cluster_id` FK? ↗, `data_center_id` FK?, `name`, `node_uid`, `is_kubernetes_node`, `address`, `port`, `state`, `rack_name`, `privilege_ceiling`, `reboot_minutes`, `added_by`, `created_at` |
| `data_centers` | Data centers external hosts stand in: name, site, time zone | `data_center_id` PK, `name`, `site`, `time_zone`, `description` |
| `host_facts` | Latest facts gathered from each host, network ones included: interfaces, bonds, subnets, routes, DNS, time sync, LLDP switch | `host_id` PK FK, `facts` JSON, `read_at`, `error` |
| `host_pairings` | SSH trust per host: pinned host keys, authority fingerprint, who trusted and paired it | `host_id` PK FK, `host_keys`, `ca_fingerprint`, `trusted_by`, `paired_by`, `paired_at` |
| `host_logins` | Login accounts per host and the privilege each serves; a run uses the login that matches its profile | `host_id` PK FK, `login_user` PK, `principal`, `privilege` |

node_uid is set only for Kubernetes nodes, and is_kubernetes_node is computed from it. address, port, state, data center and rack are set only for external hosts. A node gets a row on first use and loses it, with its facts, pairing, logins and fleet membership, when it leaves. Removing a cluster clears cluster_id on list fleets and external hosts, which then show under every cluster.

### Activity log

Two append-only tables. Schema: `0005_activity.sql`.

| Table | Holds | Columns |
| --- | --- | --- |
| `audit_events` | Every recorded action: who, what, cluster, target, outcome | `audit_event_id` PK, `at`, `actor`, `action`, `cluster_id`, `target`, `outcome`, `detail` JSON, `source_ip` |
| `toolbox_sessions` | Shell, port-forward and debug sessions: owner, target, limits, end and exit; a production session points to its approval request | `toolbox_session_id` PK, `approval_request_id` FK? ↗, `change_id`, `cluster_id`, `kind`, `namespace`, `pod`, `container`, `owner`, `state`, `started_at`, `ends_at`, `ended_at`, `exit_code`, `transcript_ref` |

Both tables are indexed by time and trimmed by retention, never updated in place. A session started from a change keeps the change_id as a plain id, since Activity log is a foundation domain.

## Engines tier

### Automation: scripts and variables

Scripts are versioned and approved; variables sit on fleets and hosts. Schema: `0006_automation.sql`.

| Table | Holds | Columns |
| --- | --- | --- |
| `script_groups` | Named groups of scripts | `script_group_id` PK, `name`, `description` |
| `scripts` | Script identity: name and group | `script_id` PK, `script_group_id` FK, `name`, `created_at`, `updated_at` |
| `script_versions` | Immutable, hashed versions with their state, header and review | `script_version_id` PK, `script_id` FK, `version_number`, `hash`, `body`, `state`, `header` JSON, `review` JSON, `author`, `approval_request_id` FK? ↗, `created_at` |
| `script_version_parameters` | Parameters each version declares in its header | `script_version_id` PK FK, `name` PK, `is_required`, `is_secret`, `description` |
| `run_profiles` | Execution profiles: SSH privilege or Kubernetes identity | `run_profile_id` PK, `name`, `kind`, `privilege`, `namespace`, `service_account`, `is_built_in`, `description` |
| `run_profile_fleets` | Fleets a profile may run on | `run_profile_id` PK FK, `fleet_id` PK FK ↗ |
| `variables` | Variables scripts read, each on one fleet or one host | `variable_id` PK, `scope`, `fleet_id` FK? ↗, `host_id` FK? ↗, `name`, `value`, `is_sensitive`, `updated_by`, `updated_at` |
| `variable_changes` | A proposed change to one fleet's or host's variables, with its impact and approval | `variable_change_id` PK, `scope`, `fleet_id` FK? ↗, `host_id` FK? ↗, `reason`, `impact` JSON, `state`, `approval_request_id` FK ↗, `requested_by`, `requested_at` |
| `variable_change_items` | Each variable a change adds, edits or removes | `variable_change_id` PK FK, `name` PK, `old_value`, `new_value` |

A variable or change sets exactly one of fleet_id and host_id. A run takes its own value first, then the host's, then the fleet's.

### Automation: runs

Each run records its fleets, hosts and the values each host got. Schema: `0006_automation.sql`.

| Table | Holds | Columns |
| --- | --- | --- |
| `script_run_fleets` | The fleets a run drew values from, as they were when it started | `script_run_fleet_id` PK, `script_run_id` FK, `fleet_id` FK? ↗, `fleet_name` |
| `script_runs` | Runs: script version, profile, target fleet, rollout, state; a production run with its own values points to their approval | `script_run_id` PK, `script_version_id` FK, `run_profile_id` FK, `retry_of_script_run_id` FK?, `cluster_id`, `fleet_id` FK? ↗, `fleet_name`, `is_dry_run`, `rollout` JSON, `hooks` JSON, `state`, `approval_request_id` FK? ↗, `started_by`, `heartbeat_at`, `started_at`, `ended_at` |
| `script_run_hosts` | Result per host of a run: batch, state, exit code, output tail; keeps the host name | `script_run_host_id` PK, `script_run_id` FK, `host_id` FK? ↗, `host_name`, `batch`, `state`, `exit_code`, `output_tail`, `reboot_minutes`, `is_cordoned`, `started_at`, `ended_at` |
| `script_run_values` | Values a run used, at run, fleet or host level, as they were when it started | `script_run_value_id` PK, `script_run_id` FK, `scope`, `script_run_fleet_id` FK?, `script_run_host_id` FK?, `name`, `value` |
| `script_run_host_fleets` | The fleets each host of a run was in when it started | `script_run_host_id` PK FK, `script_run_fleet_id` PK FK |

script_runs also keys to script_versions and run_profiles, drawn above, and to the run it retries. The view script_run_host_values gives each host's actual values: run, else host, else fleet. A secret parameter keeps only the name of its Kubernetes Secret, never the value.

### AI configuration

Prompts are versioned per agent, then tested and piloted. Schema: `0007_ai.sql`.

| Table | Holds | Columns |
| --- | --- | --- |
| `ai_providers` | Model providers: endpoint, model, prices, context window | `ai_provider_id` PK, `name`, `kind`, `endpoint`, `model`, `price_in_usd`, `price_out_usd`, `context_window`, `secret_ref`, `tested_at` |
| `agents` | Settings of the nine built-in agents, keyed by catalog id: provider, model, budgets, clusters, tools | `agent_id` PK, `ai_provider_id` FK?, `model`, `is_enabled`, `daily_usd`, `monthly_usd`, `cluster_ids`, `tools`, `context_window`, `compact_mode` |
| `ai_spend` | Insert-only spend rows per call, summed when read | `ai_spend_id` PK, `agent_id` FK, `ai_provider_id` FK?, `provider_name`, `spent_at`, `usd`, `tokens_in`, `tokens_out` |
| `prompt_versions` | Prompt versions per agent with a state, from draft to live | `prompt_version_id` PK, `agent_id` FK, `version_number`, `state`, `body`, `notes` JSON, `pilot` JSON, `why`, `created_by`, `created_at`, `approval_request_id` FK? ↗ |
| `test_cases` | Each agent's test suite: questions with recorded tool results and the expected behaviour | `test_case_id` PK, `agent_id` FK, `name`, `question`, `cluster_id`, `recorded` JSON, `expect` JSON, `is_built_in`, `created_by`, `created_at` |
| `pilot_entries` | Questions answered by a version in pilot | `pilot_entry_id` PK, `prompt_version_id` FK, `asked_by`, `cluster_id`, `question`, `answer`, `model`, `cost_usd`, `asked_at` |
| `test_runs` | One run of an agent's whole suite against one prompt version: state, counts, cost | `test_run_id` PK, `prompt_version_id` FK, `state`, `case_count`, `passed_count`, `estimate_usd`, `cost_usd`, `stopped_at_case`, `started_by`, `started_at`, `ended_at`, `error` |
| `test_case_results` | Result of each case in a test run, with a snapshot of the case | `test_case_result_id` PK, `test_run_id` FK, `test_case_id` FK?, `case_name`, `question`, `expect` JSON, `is_passed`, `answer`, `checks` JSON, `cost_usd` |
| `test_run_replays` | Production questions replayed against the version: answer before and after | `test_run_replay_id` PK, `test_run_id` FK, `cluster_id`, `question`, `answer_before`, `answer_after`, `is_same`, `cost_usd`, `error` |

Agents ship with the console and are never deleted, so other domains key to agent_id.

## AIOps flow tier

### Jobs

Each run records findings, checked against validation rules. Schema: `0008_jobs.sql`.

| Table | Holds | Columns |
| --- | --- | --- |
| `jobs` | Job definitions: kind, task, schedule, clusters, budget | `job_id` PK, `kind`, `name`, `task`, `schedule` JSON, `cluster_ids`, `run_usd`, `day_usd`, `min_priority`, `is_enabled`, `is_added`, `set_by`, `set_at` |
| `job_runs` | Runs: trigger, agent, model, status, report, cost, and the facts shown in the run's panel | `job_run_id` PK, `job_id` FK, `agent_id` FK ↗, `model`, `trigger` JSON, `subject`, `severity`, `status`, `cluster_ids`, `started_at`, `ended_at`, `cost_usd`, `report`, `facts` JSON, `routed` JSON |
| `findings` | Findings of a run: check, severity, resource, and the case, known issue or exception it went to | `finding_id` PK, `job_run_id` FK, `validation_rule_id` FK?, `check_name`, `severity`, `title`, `cluster_id`, `resource`, `routing`, `case_id` FK? ↗, `known_issue_id` FK? ↗, `exception_id` FK? ↗, `is_expected`, `confidence` |
| `schedule_claims` | One row per scheduled time, so each time runs once | `job_id` PK FK, `slot_at` PK, `claimed_by`, `claimed_at` |
| `cluster_checks` | Checks seen per cluster, to tell a new one and to suggest rules | `cluster_id` PK, `check_name` PK, `first_seen_at`, `last_seen_at`, `finding_count`, `sample_title` |
| `validation_rules` | Validation rules, keyed by check id, offered to job kinds | `validation_rule_id` PK, `says`, `case_type`, `job_kinds`, `control`, `is_built_in`, `is_edited`, `is_auto`, `state`, `replaced_by_validation_rule_id` FK?, `updated_by`, `updated_at` |

A finding with no rule is a check the agent proposed. A rule replaced by another names it.

### Cases

A case gathers its history and analysis; RCAs can cover several cases. Schema: `0009_cases.sql`.

| Table | Holds | Columns |
| --- | --- | --- |
| `services` | Service catalogue: name, where it runs, contacts | `service_id` PK, `name`, `description`, `runs_on` JSON, `contacts` |
| `case_events` | History of each case | `case_event_id` PK, `case_id` FK, `at`, `what`, `actor` |
| `cases` | Cases: number, source, cluster, object, status, priority, owner | `case_id` PK, `case_number` unique, `title`, `type`, `source`, `cluster_id`, `object`, `status`, `outcome`, `priority`, `service_id` FK?, `known_issue_id` FK? ↗, `exception_id` FK? ↗, `assignee`, `reporter`, `incident`, `created_at`, `closed_at` |
| `case_links` | Links between cases: duplicate of, regression of, same as, possible duplicate of | `case_id` PK FK, `linked_case_id` PK FK, `kind` |
| `case_messages` | Messages on a case | `case_message_id` PK, `case_id` FK, `at`, `author`, `body` |
| `case_analyses` | The Troubleshooter's analysis: cause, evidence, suggested fix | `case_analysis_id` PK, `case_id` FK, `job_run_id` FK? ↗, `summary`, `cause`, `evidence` JSON, `suggested_fix`, `confidence`, `analyzed_at` |
| `rca_cases` | The cases each RCA covers | `rca_id` PK FK, `case_number` PK, `case_id` FK? |
| `rcas` | Root cause analyses written when cases close | `rca_id` PK, `title`, `cluster_id`, `resource`, `priority`, `cause`, `fix`, `prevention`, `written_by`, `written_at` |

case_links keys to cases twice, through case_id and linked_case_id. RCAs outlive their cases, so rca_cases keeps the case number.

### Changes

A change holds its actions, history and chat, and resolves cases. Schema: `0010_changes.sql`.

| Table | Holds | Columns |
| --- | --- | --- |
| `change_actions` | What a change will do, its dry-run plan and its result | `change_action_id` PK, `change_id` FK, `position`, `kind`, `args` JSON, `impact`, `risk`, `plan_summary`, `plan_warning`, `planned_at`, `is_ok`, `result_summary`, `watson_ref`, `ran_at` |
| `changes` | Changes the agents propose, from proposal to applied or failed: reasoning, state, expiry | `change_id` PK, `agent_id` FK ↗, `cluster_id`, `title`, `reasoning`, `evidence` JSON, `source` JSON, `state`, `is_production`, `approval_request_id` FK? ↗, `replaces_change_id` FK?, `dedup_key`, `expires_at`, `created_at`, `updated_at` |
| `change_events` | History of each change | `change_event_id` PK, `change_id` FK, `at`, `what`, `actor` |
| `change_chats` | The conversation with Sherlock about a waiting change | `change_chat_id` PK, `change_id` FK, `at`, `role`, `body` |
| `change_cases` | The cases a change resolves | `change_id` PK FK, `case_id` PK FK ↗, `linked_by` |

The console calls a change a card. A change that replaces another names it in replaces_change_id.

### Knowledge

Fixes, exceptions and known issues outlive the cases they came from. Schema: `0011_knowledge.sql`.

| Table | Holds | Columns |
| --- | --- | --- |
| `wiki_pages` | Manifest of wiki pages, retired ones included; bodies stay in Git or S3 | `path` PK, `title`, `kind`, `is_critical`, `is_stale`, `version`, `updated_at`, `retired_at`, `retired_reason` |
| `wiki_changes` | Proposed page changes awaiting approval, kept as history | `wiki_change_id` PK, `path` FK, `base_version`, `diff`, `state`, `proposed_by`, `approval_request_id` FK? ↗, `created_at` |
| `knowledge_sources` | Sources Mycroft reads: Git, S3, Confluence, web, uploads; with the last sync's outcome | `knowledge_source_id` PK, `repository_id` FK? ↗, `credential_id` FK? ↗, `name`, `kind`, `url`, `branch`, `path`, `is_enabled`, `last_read_at`, `last_status`, `last_error` |
| `fixes` | Recorded fixes: problem, cause, steps, the commands that worked | `fix_id` PK, `title`, `problem_key`, `cluster_id`, `resource`, `symptoms`, `cause`, `steps`, `commands`, `prevention`, `status`, `duplicate_of_fix_id` FK?, `created_by`, `created_at` |
| `fix_cases` | The cases each fix was seen in | `fix_id` PK FK, `case_number` PK, `case_id` FK? ↗, `seen_at` |
| `source_exclusions` | Items or patterns a source does not read | `source_exclusion_id` PK, `knowledge_source_id` FK, `pattern`, `is_item`, `is_under`, `title`, `reason`, `excluded_by`, `excluded_at` |
| `exceptions` | Accepted exceptions: findings on this check, resource and cluster are left out until expiry | `exception_id` PK, `cluster_id`, `resource`, `check_name`, `title`, `reason`, `finding_id` FK? ↗, `job_run_id` FK? ↗, `case_id` FK? ↗, `duplicate_of_exception_id` FK?, `expires_at`, `created_by`, `removed_at` |
| `known_issues` | Known issues: symptoms, status, workaround, owner | `known_issue_id` PK, `title`, `kind`, `status`, `symptoms`, `workaround`, `expected`, `time_window` JSON, `source`, `is_draft`, `link`, `owner`, `created_by`, `created_at` |
| `known_issue_updates` | Dated updates on a known issue | `known_issue_update_id` PK, `known_issue_id` FK, `at`, `author`, `status`, `body` |
| `known_issue_services` | The services a known issue affects | `known_issue_id` PK FK, `service_id` PK FK ↗ |

A duplicate fix or exception names the one it repeats. fix_cases keeps the case number, since cases go after their retention.

## Decisions

| Question | Decision |
| --- | --- |
| Should job runs and case analyses record the prompt version that produced them? | Not now: the code does not record it, so it would be a new feature. |
| What happens to fleets and hosts when a cluster connection is removed? | Selector fleets are deleted. List fleets and hosts outside Kubernetes stay, with no cluster, and show under every cluster. The administrator chooses for each node whether to delete it or keep it as a host. |
| Do RCAs belong in Cases or Knowledge? | Cases: they are written when cases close, although like fixes they outlive them. |
| Do validation rules belong in Jobs or Cases? | Jobs: they are offered to job kinds and shown under the Jobs tab. |
| Do variables belong in Automation or Inventory? | Automation: only script runs read them, although they key to fleets and hosts. |
| Does `clusters` hold connection metadata, or does everything stay in Secrets? | A table for the metadata, the credentials in Secrets. |
| Do local users and sign-in sessions move to the database? | Local accounts stay in a Secret, so a database outage never blocks them. Sessions move; during an outage a local user gets a short-lived signed session. |
| Does one rejection close an approval request for good? | Yes, as the code does today; a new request is needed to try again. |
| Will any request need more than two approvers? | No flow needs it today; the required count allows any number. |
