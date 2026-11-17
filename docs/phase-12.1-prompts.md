# Phase 12.1: the agents' and jobs' instructions

Nov 12, 2026 · branch `phase-12.1-maint`, from `main` at `4bbe6f5` (0.12.0) · the review the owner asked for, and what was built from it. The requirement rows are PM-C1 to PM-S1 in [requirements.md](requirements.md#phase-121-the-agents-and-jobs-instructions); the sources are recorded in [prompt-sources.md](prompt-sources.md).

## What was asked

Review the agents' and the jobs' prompts, find open-source projects under Apache-2.0 with comparable Kubernetes, node management and AIOps prompts, and propose improvements. The proposals were then asked to be implemented on this branch, without a build.

## What was reviewed

Everything a model is sent: Sherlock's core (`sherlock/prompt.go`, `ai/prompts/sherlock.md`, `sherlockCompact.md`), the rules every agent keeps (`common.md`), the eight other agents' cores, the setup and AIOps skills, the jobs' tasks and output format (`jobs/kinds.go`, `jobs/parse.go`), the case analysis, the RCA's draft, report triage and its check (`casework/analysis.go`, `investigate/investigate.go`), Automation's tasks (`automation/prompts.go`), the wiki sync (`knowledge/sync.go`), the cluster block and the tools' descriptions; and the run loop, where several gaps only show.

## The sources

Eleven projects with comparable prompts and two check catalogues, each read at the commit in [prompt-sources.md](prompt-sources.md) and Apache-2.0 at that commit. Ideas only: no proposed wording copies text from any of them.

| Project | What compares |
|---|---|
| HolmesGPT | A Kubernetes root-cause agent: facts against hypotheses, evidence for each affected object, reading a few replicas, health checks that fail when they cannot verify |
| OpenShift Lightspeed | A console's assistant: an investigation protocol, recent changes compared with the symptom's start, mitigations marked reversible or not |
| kagent | Prompt fragments shared by many agents: a way back, the smallest scope, a check after a change, another agent's answer checked |
| kubectl-ai | A change agent that collects the specifics before it writes a manifest |
| k8sgpt | Steps per kind, and analyzers for autoscalers, disruption budgets, webhooks, Ingress, network policies and storage |
| NeMo Agent Toolkit, alert triage example | Triage of a host's alert: maintenance first, hardware, performance, process and network checks, a verdict on the alert |
| ITBench SRE Agent | An incident as chains of failures, one root cause each |
| Stakpak agent | A DevOps agent on hosts: back up, change, test; versions are not decimals |
| kubernetes-mcp-server | A cluster health prompt that opens with one overall state |
| prometheus-operator runbooks | Runbooks in one shape: meaning, impact, diagnosis, mitigation |
| Headlamp AI assistant | The closest product, an assistant inside a Kubernetes console; nothing to take |
| Polaris, kube-linter | Which workload and RBAC settings deserve an audit rule |

221B was ahead of all of them on five points, which stay as they are: text from the cluster is data, a change is proposed and never run, the diagnosis goes down to the operating system with a section per distribution, a finding may be *expected*, and every prompt has a golden copy.

## The gaps, and where each was closed

| Gap | Closed by |
|---|---|
| No rule for naming an object as affected | PM-C1, PM-A2 |
| A cause was given or left empty: nothing between | PM-C1, PM-J1d |
| What changed before a failure was one clause | PM-A1 |
| Nothing on asking reads together, repeating a read or the round limit | PM-C2, PM-C5 |
| `ask_knowledge` was in every agent's tools and in no agent's steps but Mycroft's | PM-C3 |
| A change plan had no way back and no check | PM-A3 |
| A fix could be verified by an agent that read nothing | PM-J1c |
| critical, warning and info were never defined | PM-J1a |
| A finding was named on a pod, whose name changes | PM-J1b, PM-J2 |
| Prompts sent agents to what their tools could not read | PM-A2, PM-A4, PM-R1, PM-R2 |
| The script rules reached the Automation screen's drafts only | PM-J9 |
| A job's answer without a readable block ended "ok, 0 findings" | PM-C6 |

## What differs from the proposal

- **PM-J1c found a defect, not only a gap.** `"still": null` was read into a Go `bool` as false, so an agent saying it could not read the object would have verified the fix. `Recheck.UnmarshalJSON` now reads null as no answer.
- **PM-C5 is said in both loops**, a chat's answer and an agent's run. The request keeps its tools: a request with tool results and no tools is refused by some providers, so the note asks for the answer instead.
- **PM-J9 keeps the rules in the Automation screen's tasks too.** With the agents off, a draft is sent the task alone, and an administrator may edit a core; so the rules are two constants of package `automation`, in the tasks as before and in the two cores through the marks `{{automation.ScriptRules}}` and `{{automation.ReviewRules}}`. The screen's path sends them twice.
- **PM-J6 adds no field to an RCA.** The timeline, the detection and the impact of a draft fill the RCA's free text, which a person edits like the rest.
- **One rule for a webhook**, `webhook.unreachable`, in both jobs, so that the audit's finding and the alert's join one case.
- **A likely cause's suggested fix is not filed as a proposed change by itself** (PM-J1d); a change the agent itself proposed is still filed, and still waits for a person.

## Validation rules

Alert diagnosis, new: `pod.evicted`, `workload.rollout-stuck`, `quota.exceeded`, `hpa.maxed-out`, `ingress.backend-missing`, `webhook.unreachable`, `pvc.unbound`, `pvc.mount-failed`, `node.pid-pressure`, `node.network-unavailable`.

Nightly audit, new: `rbac.escalation-verbs`, `rbac.pod-create`, `sa.token-automount`, `sa.long-lived-token`, `pod.host-path`, `pod.privilege-escalation`, `pod.no-requests`, `quota.nearly-full`, `workload.single-replica`, `pdb.missing`, `pdb.blocks-drain`, `service.no-pods`, `helm.release-failed`, `webhook.unreachable`, `node.version-skew`.

Nightly audit, reworded to what a tool can show: `rbac.secrets-read`, `rbac.pod-exec`, `rbac.sa-overprivileged`.

## Who does what (November 13, PM-D1 to PM-D5)

After the commit that gave Sherlock the console's records and sent troubleshooting to the Troubleshooter (SR-20), Sherlock's answers got worse. The instructions were read again as a model reads them, whole, and against eight permissively licensed multi-agent projects ([prompt-sources.md](prompt-sources.md); ideas only).

### What was wrong

1. **Two jobs in one prompt.** Sherlock's fixed text was still a site reliability engineer's: a six-layer troubleshooting manual, the recipes of two reports, the checks on a node. The text added after it said to hand all of that over and not to read the cluster for the cause. A model given both does some of each.
2. **Two rules that disagreed.** Every agent was told never to hand over what one or two reads answer; Sherlock was told to hand troubleshooting over even when a read or two would answer it.
3. **The work was done twice.** The knowledge-base rule applied to Sherlock and again to the Troubleshooter, and Sherlock was told to read what filled the gaps in the Troubleshooter's answer.
4. **A thin brief, a thin answer, a long report.** The task was cut at 1,000 bytes; the agent asked saw neither the person's question nor their view and was told to answer briefly; Sherlock then wrote a six-part report from it.
5. **The better manual was on the wrong side.** The Troubleshooter's own instructions were shorter than the manual Sherlock used when it diagnosed by itself.
6. **The buttons asked Sherlock.** The Troubleshoot and Audit buttons' recipes were in Sherlock's text, as its own work.
7. **The specialists overlapped.** The Troubleshooter wrote the commands of a plain fix and handed on to the Host doctor and the Change planner itself; the Auditor kept small fixes.
8. **Staying on the cluster** was worded so that a question about the console's own settings read as off the subject.

### What changed

- **One owner for each kind of work**, said once to every agent (`common.md`, *Working with the other agents*; PM-D2), with what to do where one's work meets another's: stop, name the part and its owner, hand it over if you were asked first, do it yourself only when the owner cannot be asked.
- **Sherlock is the front desk** (PM-D1): lookups, the console's records, the screen, the answer. The manual and the reports moved to their owners (PM-D3) and come back into Sherlock's instructions only for a kind of work whose owner is not in its list. With every agent on, Sherlock's instructions no longer say two things, and are shorter (PM-D6).
- **Hub and spoke** (PM-D4): only the agent asked first hands work over. A handed task says so in its first words and its agent hands nothing on, so a chain of agents retelling each other does not form. The engine's depth and no-repeat limits stay as the guard.
- **Findings are passed on, not retold** (PM-D2): Sherlock adds the sentence that answers the question, the view, the order and what nobody covered.
- **Full briefs** (PM-D5): 4,000 bytes of task, 12,000 of context, and the engine adds the person's question and view.
- Each agent's file ends at its own work: the Troubleshooter at the cause and what must change, in words; the Auditor at the finding; the Change planner plans what it is given and asks for a missing diagnosis; the Host doctor stays on the host; the Script reviewer does not rewrite; the Summarizer does not diagnose.

### What it costs

- **A diagnosis with a fix Watson can run is two hand-overs in a row**, the Troubleshooter and then the Change planner, where Sherlock or the Troubleshooter wrote the commands before. It is slower and uses both agents' budgets. The Troubleshoot button's report waits for both.
- **In a job** (the nightly audit, an alert's diagnosis) the job's agent hands the changes of the run to the Change planner once, where it wrote small fixes itself before.
- **The Troubleshooter's model now decides the quality of a diagnosis.** It should be at least as strong as Sherlock's.
- **An administrator's edited Sherlock core** replaces the front-desk text as before, so it keeps whatever it says about troubleshooting; the own-work parts are still added by the list of agents.

### Shortened the same day (PM-D6)

The first version of this change said the new rules at length, and every agent's instructions grew by half. They were cut back without taking a rule away:

| Agent | Before Phase 12.1 | After the records commit | First version | Now |
|---|---|---|---|---|
| Sherlock | 13,909 | 17,505 | 17,203 | 9,406 |
| Troubleshooter (now holds the manual) | 5,114 | 7,493 | 12,759 | 9,827 |
| Change planner | 4,360 | 6,272 | 9,144 | 6,493 |
| Auditor | 4,158 | 5,765 | 9,091 | 6,030 |
| Host doctor | 4,281 | 6,411 | 9,233 | 6,148 |
| Automation author | 3,986 | 6,626 | 9,302 | 6,217 |
| Script reviewer | 4,052 | 5,753 | 8,431 | 5,346 |
| Mycroft | 4,291 | 5,487 | 8,065 | 4,980 |
| Summarizer | 3,765 | 5,203 | 7,884 | 4,799 |

Bytes of the assembled instructions with every agent on, from the golden copies.

- **One list of agents, not two.** The rules named every owner and its work, and the list of agents after them named every agent and its purpose. The first list went; the rules now say that the list of agents says whose the rest is.
- **Five lines on working together** where there were three sections: stop where your work ends; hand over if you were asked first, do it yourself if the owner cannot be asked, hand nothing on if you were handed the task; what a brief holds; keep findings as given; answer a handed task complete. What the engine already says when it refuses a hand-over is not said again.
- **What Watson runs** is told to the agents that may give its commands, not to the Host doctor, the Automation author, the Script reviewer, Mycroft or the Summarizer.
- **Sherlock**, in two passes: the routing list no longer repeats the owners' work; the report recipe's trigger, said three times, is said once; what Watson runs and how to write commands it accepts are own work, for when no Change planner can be asked; the section on the person's screen no longer repeats what the view tools' own descriptions say (each tab's filters, what `view_aiops` and `view_admin` open) and says each rule about the view once; every sentence was cut to what it tells the model to do.
- **The Troubleshooter and the Auditor** say what they return once, not once as a list and again as a report.
- `TestInstructionsStayShort` holds the sizes. Sherlock's whole request is 12,107 tokens by `TestSherlocksRequestSize` (13,998 after the records commit, limit 14,000).

What remains long is not the instructions: Sherlock's fifty tools' descriptions are 9,888 of the 12,107 tokens. They come from earlier phases and were left alone. The rule that text from the cluster is data, not instructions, was kept word for word.

### Left as it was

The view changes held until the answer is done (SR-18) and opening the page with every answer (SR-16) were not touched. Neither is about who does what; if the screen moving with every answer is part of what feels worse, the agent's view-changes switch turns it off.

## Not done

- **A summary of a long conversation** when it no longer fits, where the engine drops the oldest turns. It matters most for a case's shared Troubleshoot conversation and deserves a design of its own.
- **The owner of an alert's pod in the alert diagnosis's scope.** A run re-checks the waiting fixes inside what it read, and an alert diagnosis reads its alert's object. Since PM-J1b a finding names the workload that owns the pods, so a fix waiting on that workload is outside the scope of a later alert on one of its pods: found again, the problem still joins or reopens the case by its key, but only a person closes it as verified (*Close without validation*). Putting the pod's owner in the scope needs the owner on the alert's row (`views.AlertRow`, `jobs.Alert`).
- **Evaluation cases** in the simulator for the behaviours that changed: pods replaced during a rollout not reported as affected, one finding on the node for three pods failing on it, `"still": null` for a fix the reader may not read, a fix found in the knowledge base named and checked, a change request with no image tag. `hack/sherlock-eval.sh` was not run: it needs a model.

## How it was checked

`go vet ./...` and `go test ./...` in `server` (again on November 13 for PM-D1 to PM-D5), the golden prompts regenerated and read, the browser app's type check and the unit tests of the case drawer and the runs. Nothing was built: no binary, no image, no chart, no CI run, no pull request. The instructions were not run against a live model: how the agents now divide a real question is to be seen in the simulator or on a cluster.
