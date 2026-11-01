# Automation: requirements

Automation runs AI-written, human-approved scripts on the cluster's nodes over SSH, with the same safety model as the rest of 221B. Approval happens once, on the code: running or scheduling approved code needs permission, not approval. Designed on October 31, 2026, as the second part of Phase 11 (branch `phase-11-auto`), on top of Phase 11's SSH to nodes (ADR-72). The full design, with every phase, is the doc "221B Automation Design"; the screens are on the canvas "221B Automation design", copied to `docs/design/automation/`; the mapping from Ansible's functions is [automation-mapping.xlsx](automation-mapping.xlsx).

## Scope

In, for this first step (Auto-1 core):

- A top-level **Automation** tab with **Scripts** and **Executions**.
- Scripts with a comment header the console reads, static checks, immutable versions and approval.
- Fleet and node parameters, read by scripts as environment variables.
- Runs on a fleet's paired nodes over Phase 11's SSH: dry run, privilege none or full, batches, a failure limit, a timeout per node, reboot and wait, cancel, per-node results and output.
- A ConfigMap of Automation's own for scripts, parameters and runs.

Later, in this order (the design doc's phases):

- Auto-1, second step: facts collection; the Configurations page; Watson's wording on every audited command.
- Auto-2: Sherlock writing and reviewing scripts; parameter change impact analysis and suspended versions; Kubernetes targets through a Watson proxy; run identities; secrets injected into scripts; Fleet roles.
- Auto-3: node hooks (drain and uncordon, and approved Kubernetes scripts), retry of failed nodes.
- Auto-4: a Schedules area shared with Kubernetes actions, notifications, a script library.

Out: Ansible or another tool as the executor; a module library, playbooks and handlers; a file store or templating in the console; app deployment through scripts (Apps does that); hosts outside the cluster (SSH-08); limited sudo (needs sudoers rules on the node).

## Approach

**A script is the unit of work.** One shell file does one job on one node. Running it on many nodes is the engine's job: order, batches, the failure limit, results. There are no steps, handlers or modules; logic lives in the script.

**Approve code, not runs.** A script version is approved once, like a code review. Running or scheduling it needs a SuperAdmin's permission and the checks below, not another approval. A version run on a cluster with Watson's two-person rule needs two SuperAdmins' approval, once; an SSH session there needs a second person every time, and that difference is deliberate (open question 10 in the design doc).

**Limits outside the AI.** Sherlock (Auto-2) reviews; the console enforces. A script that declares `privilege none`, and every dry run, runs as `nobody` through `runuser`, so it cannot change the node even if it ignores `DRY_RUN`. The fleet's ceiling caps what a run may get. Static checks refuse a `sudo` the header did not declare, a download that is not checked, and a few commands that destroy a node.

**Phase 11 is the transport.** Each node connection is Phase 11's: the pairing's login user and port, a certificate the console signs for that connection (key ID `<person> via 221B automation run <id>`, five minutes), the pinned host keys. A node that is not paired, or paired with another authority, fails with the reason. Automation never pairs a node.

**Its own ConfigMap.** The settings ConfigMap is shared and capped at 1 MiB (phase-9-review.md), so Automation keeps its records in `<release>-automation` (ADR-73).

## The script header

| Line | Declares | Example |
| --- | --- | --- |
| `# 221b:describe` | What the script does, one line or more | `# 221b:describe Report mounts above a threshold` |
| `# 221b:param` | A parameter: name, `required` or `optional`, a description | `# 221b:param THRESHOLD optional percent full, default 85` |
| `# 221b:privilege` | `none` or `full` | `# 221b:privilege none` |
| `# 221b:dry-run` | `yes` when the script honours `DRY_RUN=1` | `# 221b:dry-run yes` |
| `# 221b:facts` | Facts it reads (Auto-1 second step) | `# 221b:facts os.family` |

A script receives its parameters as environment variables, `DRY_RUN=1` on a dry run, and `NODE`, `CLUSTER`, `FLEET` and `RUN_ID`. Exit code 0 is success. The last line of output may be JSON: `{"changed": true, "msg": "…", "reboot": true}`; `reboot` asks the console to reboot the node and wait for it.

## Requirements

| ID | Requirement | Done when |
| --- | --- | --- |
| AU-01 | **The tab.** *Automation* is a top-level tab after Fleets, with *Scripts* and *Executions*. It is offered to SuperAdmins (SSH-06) when `automation.enabled` and `toolbox.ssh.enabled` are on; otherwise the tab says which is off. A read-only or locked connection shows the scripts but refuses runs. | e2e: the tab, its two sections; a Viewer gets 403 from `GET /api/v1/automation`. |
| AU-02 | **Scripts.** A script has a name (`^[a-z0-9][a-z0-9-]{0,62}$`), a group, a description from its header, and versions. Saving changed text makes a new draft version; text over 32 KiB is refused. The list shows name, description, group, current version, approval, privilege, last run, and when it was created and updated, with groups on the left. | Go test: create, a second save makes v2, an unchanged save makes none, too long refused; e2e: create a script and see it listed. |
| AU-03 | **Header.** The console reads `# 221b:` lines before the first command: describe, param, privilege, dry-run, facts. Unknown keys are findings, not errors. | Go test of the parser on a full header, a bad parameter name and a missing privilege. |
| AU-04 | **Static checks**, shown in the drawer and run at every save: errors (no `describe`, no `privilege`; `sudo` with `privilege none`; `dry-run yes` without reading `DRY_RUN`; a `curl` or `wget` download without a `sha256sum -c`; `rm -rf /`, `mkfs`, `dd` onto a disk, a fork bomb, `iptables -F`) and warnings (`helm install`/`upgrade` or `kubectl apply`, which belong in Apps; `reboot` or `shutdown`, which should be `"reboot": true`). A version with errors cannot be submitted. | Go tests for each finding and a clean script. |
| AU-05 | **Versions and approval.** A version is Draft, In review, Approved or Superseded. *Submit* puts a draft in review; *Approve* records the SuperAdmin and the time, and the author may approve; approving a version supersedes the earlier approved one. A run on a cluster with the two-person rule needs a version with two approvals, one of them not the author. Approvals and submissions are audited (`automation.approve`). | Go test: the states, the supersede, the two-person count; a Viewer refused. |
| AU-06 | **Parameters.** Each fleet of each cluster has parameter values, and each node may override them. Names are `^[A-Z_][A-Z0-9_]{0,63}$`, values at most 1 KiB, at most 100 per fleet. A run resolves each node's values: the run's own, then the node's, then the fleet's; a required parameter with no value refuses the run. Saving is audited (`automation.params`). | Go test of the resolution and the limits. |
| AU-07 | **The Run dialog** (*Run…* and *Test (dry run)…* in a script's Tools menu): the cluster's fleets, the fleet's nodes with whether each is paired, parameters with each node's resolved value and its source, a value to override for this run, batch size (all, a number, or 1-then-the-rest), stop after N failed or unreachable nodes, a timeout per node (default 10 minutes, at most 60), and dry run or run. A run uses the newest approved version; a dry run may use any version. | Go test: refusals for no approved version, an unpaired fleet, a privilege over the ceiling; e2e: the dialog sends a dry run. |
| AU-08 | **A node's part.** The console connects through the node's pairing, sends the script on standard input with the parameters and `DRY_RUN`, `NODE`, `CLUSTER`, `FLEET`, `RUN_ID` in the environment, and with `privilege none` or a dry run wraps it in `runuser -u nobody`. It records the exit code, the last line's JSON when there is one, and the output (64 KiB per node, the newest kept). A node's state is waiting, running, no change, changed, failed, unreachable, skipped or cancelled. | Go test with an SSH stand-in: success, changed, a failure, the environment, the `runuser` wrap, a node that cannot be dialled. |
| AU-09 | **Reboot and wait.** On a real run, a node whose result says `"reboot": true` is rebooted (`systemctl reboot`, else `reboot`), and the console waits for SSH to answer again, up to 10 minutes, before the node counts as done. | Go test: the reboot command is sent and the node is dialled again. |
| AU-10 | **Rollout.** Nodes run in batches, each batch's nodes at once, at most 20 at a time. The run stops starting nodes once the failed and unreachable count reaches the limit; the first batch stops on any failure. *Cancel* stops new nodes and closes running connections. | Go test: batches in order, the stop, the cancel. |
| AU-11 | **Executions.** A table of runs, newest first: number, script and version, dry run or run, cluster and fleet, per-node results, who, when, how long. A run's drawer shows each node's state, exit code, message and output, refreshing while the run lasts, with *Cancel*. | e2e: a dry run appears and its drawer shows a node's output. |
| AU-12 | **Storage.** Scripts, parameters and runs are kept in the ConfigMap `<release>-automation` under `scripts.json` (384 KiB at most; superseded versions dropped oldest first), `params.json` (64 KiB) and `runs.json` (the newest 200 runs, 384 KiB). Output stays in memory on the leader, and where `toolbox.transcripts` is `pvc`, in a file per run beside the transcripts. A run left running by a leader that stopped is marked *interrupted* by the next. | Go test: the limits, the pruning, interrupted runs. |
| AU-13 | **Who and audit.** Every Automation route is SuperAdmin-only and runs on the leader. Starting and ending a run is audited (`automation.run`) with the script, version, cluster, fleet, node count and outcome; each node's connection is in sshd's log on the node under the certificate's key ID. | Go test: a Viewer and an Admin get 403; the audit records. |
| AU-14 | **Chart.** `automation.enabled` (true) creates the ConfigMap and grants the console `get`, `update` and `patch` on it by name, and passes `AUTOMATION_CONFIGMAP`. Automation needs `toolbox.ssh.enabled` too. | `hack/check-chart.sh` renders the ConfigMap and the rule, and neither with it off. |

## Design

The screens are in `docs/design/automation/`: the Scripts page with a Tools menu open (`Scripts.html`), the script drawer (`ScriptDrawer.html`), the Run dialog (`RunDialog.html`), Executions (`Executions.html`), a run's live output (`LiveRun.html`), fleet parameters (`FleetParameters.html`), configurations (`Configurations.html`), and the later screens for scheduling (`Schedule.html`), a version's approval (`VersionApproval.html`) and a parameter change (`ParameterChange.html`). The first step builds the first five in the console's own components; the run's output is in the Executions drawer rather than a page of its own.

## As built (Auto-1)

- **Server.** `server/internal/automation`: the header parser and checks (`header.go`, `checks.go`), scripts and versions (`store.go`), fleet parameters (`params.go`), runs and the engine (`runs.go`, `engine.go`). `nodessh.Service.Exec` runs one command with no terminal on a paired node, behind a certificate signed for that run (key ID `<user> via 221B automation run <id>`); `NobodyCommand` wraps privilege `none` and every dry run. The API is `server/internal/api/automation.go`; every route is a SuperAdmin's and is recorded in the audit log (`automation.script`, `automation.approve`, `automation.params`, `automation.run`).
- **Web.** The Automation tab, after Fleets and shown only to a SuperAdmin, has three sections: *Scripts* (by group, with Dry run… and Run…), *Executions* (every run, with a drawer of per-node results and output, polling while the run goes on, and Cancel), and *Fleet parameters* (values, a node's own values, and the privilege ceiling). The script drawer edits, checks as you type, saves new draft versions, submits, approves and deletes; its Versions tab lists each version's approvals and SHA-256.
- **Settings.** `-automation` / `AUTOMATION` (default true) and `-automation-configmap` / `AUTOMATION_CONFIGMAP`; the chart's `automation.enabled` adds the ConfigMap `<release>-automation` and lets the settings Role reach it. Without the ConfigMap, scripts and runs live in memory.
- **Fleets.** A run picks one of the cluster's fleets, or *All nodes* (id `_all`) for every node; only nodes paired for SSH can run, and the dialog shows why the others cannot.
- **Not yet.** Mixed runs, Sherlock's authoring and impact analysis, run identities, secrets, Fleet roles, hooks and schedules stay with Auto-2 to Auto-4.

## Open questions

The design doc lists eleven; these touch this step:

- On a two-person cluster, should a run of approved code need a second person, as an SSH session does?
- Is one SuperAdmin's approval enough off production, and may the author count as one? Built: yes and yes.
- Limited sudo: should pairing add a sudoers drop-in for the login user?
