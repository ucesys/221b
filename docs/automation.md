# Automation: requirements

Automation runs AI-written, human-approved scripts on the cluster's nodes over SSH, with the same safety model as the rest of 221B. Approval happens once, on the code: running or scheduling approved code needs permission, not approval. Designed on October 31, 2026, as the second part of Phase 11 (branch `phase-11-auto`), on top of Phase 11's SSH to nodes (ADR-72). The full design, with every phase, is the doc "221B Automation Design"; the screens are on the canvas "221B Automation design", copied to `docs/design/automation/`; the mapping from Ansible's functions is [automation-mapping.xlsx](automation-mapping.xlsx).

## Scope

In, for this first step (Auto-1 core):

- A top-level **Automation** area with **Nodes**, **Scripts**, **Executions** and **Parameters**.
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

**Approve code, not runs.** On a production cluster, only an approved version runs. A script version is approved once, like a code review, by someone who did not write or submit it, so every approved version has had two people. On any other cluster a SuperAdmin may run the latest version without approval, and the audit log records its state. Running or scheduling approved code needs a SuperAdmin's permission and the checks below, not another approval. An SSH session on a cluster with Watson's two-person rule needs a second person every time, and that difference is deliberate (open question 10 in the design doc). A console needs two SuperAdmins for anything to be approved.

**Limits outside the AI.** Sherlock (Auto-2) reviews; the console enforces. A script that declares `privilege none`, and every dry run, runs as `nobody` through `runuser`, so it cannot change the node even if it ignores `DRY_RUN`. The fleet's ceiling caps what a run may get. Static checks refuse a `sudo` the header did not declare, a download that is not checked, and a few commands that destroy a node.

**Phase 11 is the transport.** Each node connection is Phase 11's: the pairing's login user and port, a certificate the console signs for that connection (key ID `<person> via 221B automation run <id>`, five minutes), the pinned host keys. A node that is not paired, or paired with another authority, fails with the reason. Automation never pairs a node.

**Its own ConfigMap.** The settings ConfigMap is shared and capped at 1 MiB (phase-9-review.md), so Automation keeps its records in `<release>-automation` (ADR-73).

## The script header

| Line | Declares | Example |
| --- | --- | --- |
| `# 221b:describe` | What the script does, one line or more | `# 221b:describe Report mounts above a threshold` |
| `# 221b:param` | A parameter: name, `required` or `optional`, a description | `# 221b:param THRESHOLD optional percent full, default 85` |
| `# 221b:privilege` | `none` runs as `nobody` and cannot change the node; `full` runs with the login user's rights, root or sudo, up to the fleet's ceiling | `# 221b:privilege none` |
| `# 221b:dry-run` | `yes` when the script honours `DRY_RUN=1` | `# 221b:dry-run yes` |
| `# 221b:facts` | Facts it reads (Auto-1 second step) | `# 221b:facts os.family` |

Spaces after `221b:` and a colon after the key are optional: `# 221b: privilege: none` is the same line as `# 221b:privilege none`.

A script receives its parameters as environment variables, `DRY_RUN=1` on a dry run, and `NODE`, `CLUSTER`, `FLEET` and `RUN_ID`. Exit code 0 is success. The last line of output may be JSON: `{"changed": true, "msg": "…", "reboot": true}`; `reboot` asks the console to reboot the node and wait for it.

## Requirements

| ID | Requirement | Done when |
| --- | --- | --- |
| AU-01 | **The area.** The top bar switches between *Kubernetes* and *Automation*, which has the tabs *Nodes* (the same page as Kubernetes's, staying in the area it was opened from), *Scripts*, *Executions* and *Parameters*. The switch is offered to SuperAdmins (SSH-06); with `automation.enabled` or `toolbox.ssh.enabled` off, the pages say which is off. A read-only or locked connection shows the scripts but refuses runs. | Tests: the tabs of each area, Nodes in both, old `#/automation` links; a Viewer gets 403 from `GET /api/v1/automation`. |
| AU-02 | **Scripts.** A script has a name (`^[a-z0-9][a-z0-9-]{0,62}$`), a group, a description from its header, and versions. Saving changed text updates the draft and keeps its version number; with no draft, because the latest version was submitted, it starts the next version. Text over 32 KiB is refused. The list shows name, description, group, current version, approval, privilege, last run, and when it was created and updated, in one table. Groups are a filter on the left, as fleets are on the Nodes page: a group's name shows it alone and its box adds it to the others. The table also filters by state: pending review, draft, approved, nothing approved. | Go test: create, saves stay in the draft, a save after a submission makes v2, too long refused; a test of the filter. |
| AU-03 | **Header.** The console reads `# 221b:` lines before the first command: describe, param, privilege, dry-run, facts. Unknown keys are findings, not errors. | Go test of the parser on a full header, a bad parameter name and a missing privilege. |
| AU-04 | **Static checks**, shown in the drawer and run at every save: errors (no `describe`, no `privilege`; `sudo` with `privilege none`; `dry-run yes` without reading `DRY_RUN`; a `curl` or `wget` download without a `sha256sum -c`; `rm -rf /`, `mkfs`, `dd` onto a disk, a fork bomb, `iptables -F`) and warnings (`helm install`/`upgrade` or `kubectl apply`, which belong in Apps; `reboot` or `shutdown`, which should be `"reboot": true`). A version with errors cannot be submitted. | Go tests for each finding and a clean script. |
| AU-05 | **Versions and approval.** A version is Draft, In review, Approved or Superseded. *Submit* freezes a draft and puts it in review, superseding an older version still in review; *Approve* records the SuperAdmin and the time. Nobody who wrote, saved or submitted a version may approve it (403), and on a production cluster a run needs an approval from someone else; versions approved earlier stay approved. Approvals, submissions and revocations are audited (`automation.approve`). | Go test: the states, the refusals; a version only its author approved does not run. |
| AU-06 | **Parameters.** Each fleet of each cluster has parameter values, and each node may override them. Names are `^[A-Z_][A-Z0-9_]{0,63}$`, values at most 1 KiB, at most 100 per fleet. A run resolves each node's values: the run's own, then the node's, then the fleet's; a required parameter with no value refuses the run. Saving is audited (`automation.params`). | Go test of the resolution and the limits. |
| AU-07 | **The Run dialog** (*Run…* and *Dry run…* on a script): the version, the cluster's fleets and the nodes of the one chosen, with whether each is paired, in a list that scrolls, so a large cluster fits; *All nodes* lists every node. Choosing a fleet selects exactly its paired nodes, and the unpaired ones are left out, with a note. A run goes to the nodes it names and no others. Changing a node by hand makes the run a *Custom selection*, no fleet's: it takes the values and the ceiling of *All nodes*, and is recorded as a custom selection. A script that does not declare `dry-run yes` has no dry run: the buttons are off and the server refuses (409 or 400). Parameters with each node's resolved value and its source, a value to override for this run, batch size (all, a number, or 1-then-the-rest), stop after N failed or unreachable nodes, a timeout per node (default 10 minutes, at most 60), and dry run or run. A new version does not take an approved one away: approved versions stay approved, and any of them can run, until someone revokes it. so production offers every approved version, newest first, and starts on the newest. Any other cluster offers every version and says when the one chosen is not approved; a real run starts on the newest approved, a dry run on the newest that has one. The rollout's numbers sit to its right and follow the choice. A version with check errors never runs. | Go test: refusals for no approved version, no dry run declared; a run takes only the nodes it names; tests of the dialog: a fleet's nodes, a custom selection; browser test: 40 nodes stay in the list's height. |
| AU-08 | **A node's part.** The console connects through the node's pairing, sends the script on standard input with the parameters and `DRY_RUN`, `NODE`, `CLUSTER`, `FLEET`, `RUN_ID` in the environment, and with `privilege none` or a dry run wraps it in `runuser -u nobody`. It records the exit code, the last line's JSON when there is one, and the output (64 KiB per node, the newest kept). A node's state is waiting, running, no change, changed, failed, unreachable, skipped or cancelled. | Go test with an SSH stand-in: success, changed, a failure, the environment, the `runuser` wrap, a node that cannot be dialled. |
| AU-09 | **Reboot and wait.** On a real run, a node whose result says `"reboot": true` is rebooted (`systemctl reboot`, else `reboot`), and the console waits for SSH to answer again, up to 10 minutes, before the node counts as done. | Go test: the reboot command is sent and the node is dialled again. |
| AU-10 | **Rollout.** *Canary first*: one node alone, then the rest in batches of the size given, or together with size 0. *Batches*: the size given at a time. *All at once*: every node, one batch. A batch's nodes run at once, at most 20 at a time, and each batch finishes before the next starts. The run stops starting nodes once the failed and unreachable count reaches the limit (0 sets none); with more than one batch, any failure in the first stops the run whatever the limit. The dialog lists the modes as radio buttons, one per line, each saying what it does. *Cancel* stops new nodes and closes running connections. | Go test: batches in order, the stop, the cancel. |
| AU-11 | **Executions.** A table of runs, newest first: number, script and version, dry run or run, cluster and fleet, per-node results, who, when, how long. A run's drawer shows each node's state, exit code, message and output, refreshing while the run lasts, with *Cancel*. | e2e: a dry run appears and its drawer shows a node's output. |
| AU-12 | **Storage.** Scripts, parameters and runs are kept in the ConfigMap `<release>-automation` under `scripts.json` (384 KiB at most; superseded versions dropped oldest first), `params.json` (64 KiB) and `runs.json` (the newest 200 runs, 384 KiB). Output stays in memory on the leader, and where `toolbox.transcripts` is `pvc`, in a file per run beside the transcripts. A run left running by a leader that stopped is marked *interrupted* by the next. | Go test: the limits, the pruning, interrupted runs. |
| AU-13 | **Who and audit.** Every Automation route is SuperAdmin-only and runs on the leader. Starting and ending a run is audited (`automation.run`) with the script, version, cluster, fleet, node count and outcome; each node's connection is in sshd's log on the node under the certificate's key ID. | Go test: a Viewer and an Admin get 403; the audit records. |
| AU-14 | **Chart.** `automation.enabled` (true) creates the ConfigMap and grants the console `get`, `update` and `patch` on it by name, and passes `AUTOMATION_CONFIGMAP`. Automation needs `toolbox.ssh.enabled` too. | `hack/check-chart.sh` renders the ConfigMap and the rule, and neither with it off. |
| AU-15 | **Starting points.** *New script* can start from an example or from `reserve-node-resources`, a script kept in the repository, which then goes through the same review as any other. | Go test: it passes the checks; run against a stand-in node. |
| AU-16 | **A script's versions.** The Versions tab is wider: versions on the left, the text of the one chosen on the right, and a diff against every other version: the icon Helm's diff uses stands on each of their rows, and compares the version shown, the one for approval unless another is chosen, with that row. The list beside the text does the same, and a no-diff icon goes back to the text. It opens on the version in review, or the latest. | Tests of the dialog; test of the line diff. |
| AU-17 | **A run's parameters** stand at the top of its drawer, for the node chosen. | Test of the drawer. |
| AU-19 | **Revoking.** Any SuperAdmin may revoke the approval of any approved version, their own or not. It goes back to review, production stops running it, the row names who revoked it, and the audit log records it. | Go test of the store; test of the dialog. |
| AU-18 | **What the Kubernetes agent keeps from pods**, read-only. How much is each node's capacity less its allocatable, read from the node as it is now, so it is right however it was set: on the Fleets page, per fleet and per node, on the Parameters page, in the Fleets pane and the *Reserved* column on Nodes. The node's drawer adds how, from the kubelet's `/configz` through the API server (`get` on `nodes/proxy`, as uptime needs, off with `kubeletStats`): system-reserved, kube-reserved, reserved CPUs, the eviction threshold and what is enforced, and no other field of that configuration. | Go test: only those fields are kept. Tests of the wording; browser test of the drawer. |

## Design

The screens are in `docs/design/automation/`: the Scripts page with a Tools menu open (`Scripts.html`), the script drawer (`ScriptDrawer.html`), the Run dialog (`RunDialog.html`), Executions (`Executions.html`), a run's live output (`LiveRun.html`), fleet parameters (`FleetParameters.html`), configurations (`Configurations.html`), and the later screens for scheduling (`Schedule.html`), a version's approval (`VersionApproval.html`) and a parameter change (`ParameterChange.html`). The first step builds the first five in the console's own components; the run's output is in the Executions drawer rather than a page of its own.

## As built (Auto-1)

- **Server.** `server/internal/automation`: the header parser and checks (`header.go`, `checks.go`), scripts and versions (`store.go`), fleet parameters (`params.go`), runs and the engine (`runs.go`, `engine.go`). `nodessh.Service.Exec` runs one command with no terminal on a paired node, behind a certificate signed for that run (key ID `<user> via 221B automation run <id>`); `NobodyCommand` wraps privilege `none` and every dry run. The API is `server/internal/api/automation.go`; every route is a SuperAdmin's and is recorded in the audit log (`automation.script`, `automation.approve`, `automation.params`, `automation.run`).
- **Web.** The Automation area, shown only to a SuperAdmin, has *Nodes*, *Scripts* (one table, filtered by groups on the left and by state, with Dry run… and Run…), *Executions* (every run, with a drawer of per-node results and output, polling while the run goes on, and Cancel), and *Parameters* (values, a node's own values, and the privilege ceiling). The script drawer edits, checks as you type, saves the draft, submits, approves and deletes; its Versions tab lists each version's approvals and SHA-256.
- **Settings.** `-automation` / `AUTOMATION` (default true) and `-automation-configmap` / `AUTOMATION_CONFIGMAP`; the chart's `automation.enabled` adds the ConfigMap `<release>-automation` and lets the settings Role reach it. Without the ConfigMap, scripts and runs live in memory.
- **Fleets.** A run picks one of the cluster's fleets, or *All nodes* (id `_all`) for every node; only nodes paired for SSH can run, and the dialog shows why the others cannot.
- **Not yet.** Mixed runs, Sherlock's authoring and impact analysis, run identities, secrets, Fleet roles, hooks and schedules stay with Auto-2 to Auto-4.

## Open questions

The design doc lists eleven; these touch this step:

- On a two-person cluster, should a run of approved code need a second person, as an SSH session does?
- Is one SuperAdmin's approval enough, and may the author count as one? Decided November 1, 2026: one approval on production, never the author's; off production a run needs none.
- Limited sudo: should pairing add a sudoers drop-in for the login user?

## Keeping resources for software beside Kubernetes

To run MinIO or anything else on the nodes Kubernetes runs on, tell the kubelet to keep part of the machine from pods. It is agent configuration, so it changes through a script and shows read-only in the console.

1. *Scripts › New script › Start from*: **Reserve resources from Kubernetes (RKE2, k3s)**. Have it reviewed and approved as usual.
2. *Parameters*: set `SYSTEM_RESERVED_CPU` and `SYSTEM_RESERVED_MEMORY` on the fleet, and on a node where it differs; `KUBE_RESERVED_CPU` and `KUBE_RESERVED_MEMORY` are optional. Raise the fleet's ceiling to `full`.
3. *Run…*: a dry run says what would change on each node. Then run it **Canary first** with batches of 1, so one agent restarts at a time and control-plane nodes keep their quorum.
4. *Fleets* shows what each node keeps now. Nodes that differ are listed apart, which is how a run that stopped part way shows.

The script writes `/etc/rancher/rke2/config.yaml.d/50-221b-reserved.yaml` (`k3s` on k3s) with `kubelet-arg+:`, which adds to arguments already set, restarts `rke2-server`, `rke2-agent`, `k3s` or `k3s-agent`, and waits for the kubelet. If the agent does not come back, it puts the file back as it was and restarts it again. The same values twice change nothing and restart nothing.

Three things to know:

- **A shrinking node can lose pods.** On restart the kubelet admits its pods again; one that no longer fits what is allocatable fails with `OutOfmemory` or `OutOfcpu`, and its controller starts it elsewhere. Before you run it, compare *Free to schedule* on Nodes with what you are taking away, or cordon and drain the node first.
- **Memory is a limit on pods, CPU a share.** The kubelet caps all pods together at allocatable memory. For CPU it only lowers their share when the machine is busy, so software beside Kubernetes gets its CPU under contention, not a wall.
- **In use is the whole machine.** metrics-server reads the machine, your other software included, so *In use* is shown as a share of the machine; *Requested* and *Free to schedule* stay shares of what pods may have.
