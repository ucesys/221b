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

## Not done

- **A summary of a long conversation** when it no longer fits, where the engine drops the oldest turns. It matters most for a case's shared Troubleshoot conversation and deserves a design of its own.
- **The owner of an alert's pod in the alert diagnosis's scope.** A run re-checks the waiting fixes inside what it read, and an alert diagnosis reads its alert's object. Since PM-J1b a finding names the workload that owns the pods, so a fix waiting on that workload is outside the scope of a later alert on one of its pods: found again, the problem still joins or reopens the case by its key, but only a person closes it as verified (*Close without validation*). Putting the pod's owner in the scope needs the owner on the alert's row (`views.AlertRow`, `jobs.Alert`).
- **Evaluation cases** in the simulator for the behaviours that changed: pods replaced during a rollout not reported as affected, one finding on the node for three pods failing on it, `"still": null` for a fix the reader may not read, a fix found in the knowledge base named and checked, a change request with no image tag. `hack/sherlock-eval.sh` was not run: it needs a model.

## How it was checked

`go vet ./...` and `go test ./...` in `server`, the golden prompts regenerated and read, the browser app's type check and the unit tests of the case drawer and the runs. Nothing was built: no binary, no image, no chart, no CI run, no pull request.
