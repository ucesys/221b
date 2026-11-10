# Phase 12 code review and hand-over

A review of 221B on `phase-12.0-ai`, read at commit `d255427` on November 9, 2026 and checked again at `52fc821` on November 10 (the one commit between them touches none of the findings). It covers the architecture, the consistency of the code, the names Sherlock is given against the application, the case workflow end to end, every agent's and job's prompt, and the console's labels and texts. Where a document and the code differ, the code was taken as truth.

This file is also the hand-over: the owner accepted every finding, fix and improvement on November 10, 2026, and they are to be made on `phase-12.0-ai`. Each finding says where it is, what is wrong, what to do and how to tell it is done. The documentation fixes that could be made without code changes are already on the branch (see [Documentation](#documentation)).

## Summary

The architecture is sound: one composition root, small packages that do not import each other in circles, and every change behind a person's approval that is enforced in code and not in a prompt. Two groups of problems need work.

1. **A fix can be marked verified, and its case closed, without any run having looked at it** (W-1, W-2, W-3, with W-4 and P-2). Verification is inferred from a run *not* reporting a problem, and the run's scope is taken to be its whole cluster.
2. **What Sherlock is told about the console has drifted from the console** (S-1, S-2, S-3). The tool names are right; the hand-written lists of Watson's commands, of buttons and of filters are not.

| Severity | Count | Findings |
| --- | --- | --- |
| High | 3 | W-1, W-2, W-3 |
| Medium | 9 | W-4, W-5, W-6, W-7, S-1, S-2, S-3, P-1 (with P-2), A-1 |
| Low | 37 | W-8 to W-11, S-4 to S-8, P-3 to P-9, A-2 to A-7, U-1 to U-9, I-1 to I-5, D-9 |

Severity: **High** is a wrong outcome recorded with no person involved; **Medium** is wrong or misleading behaviour a person can catch; **Low** is consistency, text or structure.

## How the review was done

- Read: `server/internal/ai` (catalog, instruction layers, every file in `prompts/`), `sherlock` (core prompt, view control, hand-over), `jobs`, `casework`, `cards`, the case parts of `issues`, the task prompts in `investigate`, `knowledge/sync.go` and `api/automation_more.go`, `watson/parse.go`, `api/sherlock_tools.go`, and in the browser app `lib/router.ts`, `lib/uiControl.ts`, `lib/cases.ts`, the top bar, Administration, Settings and `components/cases`.
- Ran: `gofmt -l` (clean), `tsc --noEmit` (clean), `eslint` (clean), `vitest` (all pass), and a script over the internal imports (acyclic; `api` imports 36 packages and nothing imports `api`).
- Compared by script: the 27 tool names in `ai/catalog.go` against `api/sherlock_tools.go`; every console address the server and the prompts emit against the router; every button label in the browser app for case and wording.
- **Not run:** `go build`, `go vet` and the Go tests (the review environment had Go 1.24 and `go.mod` needs 1.26), the kind and simulator end-to-end tests, and the chart checks.
- **Not seen:** the running console. The test install could not be reached from the review's browser, so the UI findings come from the source and from `docs/screenshots` (taken October 26). The section [To check on a running console](#to-check-on-a-running-console) lists what that leaves open.
- Read at outline level only: `knowledge` apart from `sync.go` and `jobs.go`, `confluence`, `auth`, `automation`, `nodessh`, `apps`, `backup`, the chart's templates.

## How to work through this

For whoever makes the changes (a person or Claude Code):

1. Work on `phase-12.0-ai`. Take the [work order](#work-order) top to bottom; each step builds and tests on its own, so commit after each.
2. Follow `CONTRIBUTING.md`: a requirement row in `docs/requirements.md` for each change in behaviour (the next free ids at the time of writing are CF-94 and on), an ADR in `docs/architecture.md` for each decision (ADR-145 and on), a line under 0.12.0 in `CHANGELOG.md` for what a user would notice, tests with the change, and `git commit -s`.
3. Before each commit: `make lint` and `make test`. Before the last one: `make chart-lint` and `make e2e-sim`. The review could not run the Go tests, so run them **first**, before changing anything, and note any failure that is already there.
4. Prompts: make the prompt edits a mechanism needs (they are marked **prompt** below) and the exact name corrections listed in S-1 to S-4. Leave every other wording change to the prompt review that follows this work ([Left for the prompt review](#left-for-the-prompt-review)).
5. If a finding turns out to be wrong when you read the code, say so in this file under the finding (*not a defect:* and why) instead of changing code to fit it.
6. When a finding is done, add *fixed* after its heading, as `docs/phase-9-review.md` does, with the commit.

### Decisions already made

- All findings and suggested fixes are accepted, the naming proposal A-1 included.
- **Spelling:** American in everything a person or a model reads (*Analyze*, not *Analyse*). Identifiers and the API already use it.
- **Buttons and tabs:** sentence case.
- **One clean run still closes an agent-found case below P1**, as `docs/states.md` says, but the run must have re-checked that case's object (W-1). Whether two clean runs should be needed is an [open question](#open-question) and is not part of this work.
- Jobs stay Jobs and agents stay Agents (considered and declined on November 10).

## Work order

| Step | Findings | What it is |
| --- | --- | --- |
| 1 | W-1, W-2, W-3, W-4, P-2 | Verification rests on what a run re-checked |
| 2 | W-5, W-6, W-7, P-1 | The cluster an agent may name, whole changes only, exceptions fail closed, `check_command` for the Troubleshooter |
| 3 | S-1, S-3, S-4, S-5, S-6, S-7, then S-2 with P-8 | Sherlock's lists built from the code, and a test that keeps prompt names true |
| 4 | A-1, with A-5 | Names |
| 5 | U-1 to U-9, I-1 to I-5 | Labels, texts and the UI improvements |
| 6 | W-8 to W-11, A-2 to A-4, A-6, P-3 | The remaining Low findings |
| 7 | D-9 and the documentation left open | Documents and screenshots, once the names have settled |

---

## High

### W-1. An alert diagnosis verifies fixes it never looked at

**Where:** `server/internal/casework/route.go:173-196` (verification), `:437-449` (`verifyApplied`); `server/internal/jobs/service.go:348-351` (`covered`); `server/internal/jobs/kinds.go` (`runAlert`); `server/internal/jobs/run.go:381` (the `RouteInput`).

**What is wrong:** After routing a run's findings, the router goes through every case whose fix waits for verification and verifies it when three things hold: the case was found by this run's job, the case's cluster is one the run covered, and the case's key is not among the run's findings. For a case below P1 with an applied change, verifying closes the case as Fixed, by the console.

An alert-triggered run of the Alert diagnosis job covers the whole cluster of its alert (`covered` returns `[ev.Cluster]`) but reads one alert's object. So a diagnosis of an alert on pod X verifies the pending fix on pod Y, which that job found on another day, and closes Y's case. *Run now* diagnoses up to three alerts and covers every cluster the job has, with the same effect. The Daily digest has `Checks` too (`kinds.go:84`), so its findings are routed and a digest run verifies the digest's own earlier cases the same way.

**Do:** Verify a fix only on positive evidence that the run looked at that case's object. This one change also closes W-2 and W-3.

1. Give a run a *scope*, set by the job kind and passed in `RouteInput`:
   - Nightly audit: each cluster that answered (W-2), whole cluster.
   - Alert diagnosis: the objects of the alerts this run diagnosed, nothing else.
   - Daily digest and every other kind: no scope; such a run never verifies.
2. Before the request, give the agent the keys to re-check: the open cases of this job in scope and the cases of this job whose fix waits for verification (W-4). `KeysInUse` (`route.go:536-580`) already builds the first half.
3. **Prompt:** add one field to the output format (`jobs/parse.go`, `OutputFormat`) and one sentence that asks for an entry for every key listed for re-checking:

   ```
   "rechecked": [{"check": "<rule id>", "resource": "<object>", "still": true, "evidence": "what you read"}]
   ```

4. In the router, verify a waiting fix only when its key is inside the run's scope **and** the run's `rechecked` has it with `still: false`. `still: true`, or the key among the findings, reopens as today. A key in scope that the agent did not list is neither: the case keeps waiting, and the run gets one note with the count ("3 fixes were not re-checked").
5. A case that has waited for verification through several runs should say so in its drawer (I-1).

**Done when:** a Go test shows that (a) an alert run on object X leaves a waiting fix on object Y of the same cluster and job untouched; (b) a digest run verifies nothing; (c) an audit run verifies a waiting fix only with `rechecked` … `still: false`; (d) an audit run that lists nothing for a waiting key leaves the case waiting and notes it. `docs/states.md` says the new rule. Requirement row and ADR added (this replaces part of CF-06 and CF-30).

### W-2. A cluster the audit failed to read still counts as audited

**Where:** `server/internal/jobs/kinds.go:148-153` (`runAudit`), `server/internal/jobs/run.go:381`.

**What is wrong:** In an audit of several clusters, a cluster whose request fails is noted and skipped (`env.Note(...)`, `continue`), the function returns no error, the run ends OK, and `RouteInput.Clusters` holds every cluster the run was given. Every fix waiting on the skipped cluster is then verified.

**Do:** Return, with the result, the clusters that answered, and build the run's scope (W-1) from those only. Show the clusters that failed on the run (I-3): a run with a failed cluster is *Done, with errors*, not plain *Done*.

**Done when:** a Go test with two clusters, one of which fails, shows no case on the failed cluster verified, and the run says which cluster failed.

### W-3. Findings past the cap are dropped and then count as not seen

**Where:** `server/internal/jobs/store.go:34` (`maxFindings = 40`), `:214` (`tidy`).

**What is wrong:** A run keeps at most 40 findings over all its clusters. `tidy` drops the rest in the order they came, so in a three-cluster audit the last cluster's findings go first, a critical one as easily as an info one. Dropped findings never reach the router, so their keys count as not seen.

**Do:** With W-1, a dropped finding no longer verifies anything. Still: sort by severity before cutting so that the least severe are the ones dropped, mark the run as truncated, and say on the run how many were left out and of which severity. A truncated run should not be the reason an open agent-found case is called "not seen again" either.

**Done when:** a Go test with 45 findings, the critical ones last, keeps every critical finding, and the run carries the note.

---

## Medium

### W-4. Cases closed as Fixed and waiting for verification are not in the agent's keys

**Where:** `server/internal/casework/route.go:545` (`l.AgentFound && l.Open()`).

**What is wrong:** `KeysInUse` lists open cases only. A case a person closed as Fixed is closed, so the next run is not told its key. If the agent names the same problem with another rule id or another object, a new and unrelated case opens, and the original's fix is verified because its key was not seen.

**Do:** Include the cases that wait for verification, said as such ("fix applied, re-check: C-12"). They are the list W-1 step 2 needs.

**Done when:** `TestKeysInUse…` covers a closed case with `VerifyPending`.

### W-5. An agent can file findings and changes for a cluster it was not asked about

**Where:** `server/internal/jobs/kinds.go:107` (`merge`: the cluster is filled in only when the agent left it empty), `server/internal/jobs/parse.go` (`Changes`: `cl := c.Cluster`), `server/internal/jobs/store.go:214` (`tidy` does not check the cluster).

**What is wrong:** Each request is about one cluster, but the cluster on a finding or a proposal is whatever the agent wrote. An audit of cluster A can open a case or file a change on cluster B, one the job does not even cover. Text planted in cluster A (an annotation, a log line) could steer that. A person still has to approve the change, which is why this is Medium.

**Do:** For a request about one cluster, set the cluster on every finding and proposal to that cluster and drop, with a note, anything that named another. `casework/analysis.go:542` already does this for the case analysis.

**Done when:** a Go test whose agent answers with another cluster's id shows nothing filed there and a note on the run.

### W-6. A change is filed with its refused commands silently removed

**Where:** `server/internal/jobs/parse.go:141-153` (`Changes`); `server/internal/casework/analysis.go:521-530` (a case's suggested commands).

**What is wrong:** When Watson would refuse one command of a proposal, that command is taken out and the rest is filed. Cordon, drain, a refused step, uncordon becomes a different change from the one the agent planned. The note is on the run; the card and the case show the shortened list as if it were whole.

**Do:** Drop the whole proposal (or the whole suggested fix) when any command is refused, and say on the run and on the case's analysis which command was refused and why. Where dropping loses too much, the alternative is to keep it but mark the card *Incomplete* with the refused lines shown and approval disabled (I-2); pick one and use it in both places.

**Done when:** tests for both paths; no card is ever filed with fewer commands than the agent gave without saying so on the card.

### W-7. A failed read of the exceptions is ignored

**Where:** `server/internal/casework/route.go:87`, `:562`; `server/internal/casework/actions.go:276` (`list, _ := s.o.Lessons.Exceptions(ctx)`).

**What is wrong:** If the exceptions cannot be read, the list is empty and every finding an exception covers opens a case, with the notifications and the analysis that follow.

**Do:** In `Route`, return the error so the findings stay unrouted (and are retried, W-8). In `KeysInUse` and `actions.go`, return the error to the caller.

**Done when:** a Go test with a failing lessons store shows no case opened.

### S-1. Sherlock's list of what Watson runs is out of date

**Where:** `server/internal/sherlock/prompt.go` (the paragraph "Watson runs kubectl get, describe, …"), `server/internal/ai/prompts/sherlockCompact.md`; the truth is `server/internal/watson/parse.go:126-203` and `Supported()` at `:209`, which `api/watson.go:227` already serves.

**What is wrong:**

| Command Watson runs | Core prompt | Compact prompt |
| --- | --- | --- |
| `helm install`, `helm upgrade`, `helm uninstall` | missing | missing |
| `kubectl delete secret`, `kubectl delete namespace` | missing | missing |
| `kubectl auth can-i` | listed | missing |
| `kubectl patch pvc` (size), `kubectl delete pvc` | listed | missing |

**Do (prompt):** Build that sentence from `watson.Supported()` when the prompt is assembled (`sherlock` may import `watson`; today only `api` does, so check the import graph stays acyclic, or pass the text in through `Options`). Put the same generated sentence in `common.md`'s place for the other agents (P-1).

**Done when:** a Go test fails if a command in `watson`'s list is missing from the assembled prompt of Sherlock, the compact form and the Troubleshooter.

### S-2. Names in Sherlock's Cases skill that the console does not use

**Where:** `server/internal/ai/prompts/cases.md`.

| The skill says | The console shows | Where |
| --- | --- | --- |
| Analyze | Analyse, Analyse again, Analyse now | `CaseDrawer.tsx:956`, `BulkBar.tsx:188` |
| Refuse… | Reject… | `ProposalDrawer.tsx:218` |
| Open in Watson | Run in Watson (no "Open in Watson" label exists) | `CaseDrawer.tsx:977`, `Sherlock.tsx:113` |
| "suggest an exception" | Accept as exception… | `CaseDrawer.tsx:736` |
| Wiki: pages, drafts, sources, Add | Pages, drafts and sources; Add is in Settings › Knowledge | `KnowledgeTab.tsx:50-51` |
| New Case | *New Case* on the button, *New case* on the dialog | `NewReport.tsx:22`, `NewCase.tsx:132` |

**Do (prompt):** After U-1 and U-2 the console says *Analyze* and *New case*; change the skill to *Reject…*, *Run in Watson*, *Accept as exception…*, *New case*, and take *Add* out of the Wiki's list. Then add the test of P-8 so it cannot drift again.

**Done when:** the P-8 test passes.

### S-3. The view tools describe filters and tabs differently from what the browser accepts

**Where:** `server/internal/sherlock/ui_actions.json` (the `description` of `view_filter` and `view_open`) against `web/src/lib/uiControl.ts` (`TAB_SPECS`).

| Tab | The tool's description | The browser accepts |
| --- | --- | --- |
| Helm `tab` | pods, workloads, storage, network, other, values, notes, history | also overview, roles, images |
| Policies | not described | from, to, port |
| Access | kind, verb, res, wns, tbl, sys, defaults | also id, scope |
| Networks | not described | route |
| `view_open` | the sentence of tabs omits storage | storage is in the enum |

**Do:** Keep one table. Either generate the per-tab sentences of `ui_actions.json` from a JSON file that `uiControl.ts` also reads, or add a test on each side against a shared fixture (`web/src/lib/uiControl.test.ts` and a new Go test in `sherlock`) that fails when a tab, a filter or a value is on one side only.

**Done when:** that test exists and the five rows above agree.

### P-1. The Troubleshooter proposes Watson commands without knowing what Watson runs

**Where:** `server/internal/ai/catalog.go` (`Troubleshooter`: no `check_command`; only `ChangePlanner` has it), `server/internal/ai/prompts/troubleshooter.md`, `common.md`.

**What is wrong:** The Troubleshooter writes the commands for the case analysis, the alert diagnosis and the report triage. Only Sherlock's core says what Watson runs, and the Troubleshooter cannot check a command. What Watson would refuse is taken out afterwards (W-6).

**Do:** Add `check_command` to the Troubleshooter's tools (it changes nothing; see `sherlock/agents.go`, `ToolCheckCommand`), and the generated list of S-1 to `common.md`'s place. **Prompt:** one sentence in `troubleshooter.md` to check each command before giving it, as `changePlanner.md` has.

**Done when:** the S-1 test covers the Troubleshooter, and a test in `ai/ai_test.go` expects the tool among the Troubleshooter's.

### P-2. No task asks the agent what it re-checked

Part of W-1 (steps 2 and 3). Listed separately because it is the prompt half: `jobs/keys.go:387` tells the agent to reuse a key for the same problem, and nothing asks it to look at each listed key again and say what it found.

### A-1. "Cases" names six different things

**Where and what:**

| Where | Name today | What it is |
| --- | --- | --- |
| Top bar, Settings group, prompts | AIOps | The area (CF-91) |
| Router, `web/src/components/cases`, `/api/v1/cases/` | cases | The same area |
| `issues.Report` | Report | The case record itself |
| `cards`, ConfigMap `<release>-cases`, `CASES_CONFIGMAP` | cases | Watson's cards, which the console calls proposed changes |
| `casework`, `/api/v1/cases/cases` | cases | 221B's work on a case |
| `ai/cases.go`, `ai/cases/base.json` | Cases | The tests of an agent's instructions |
| `ai/prompts/cases.md` | cases | Sherlock's skill for the area |

**Do:** the accepted names. None of these is in a released version (the cases API, addresses and ConfigMaps are new in 0.12.0; `main` has none of them), so no alias is owed to a release; the redirects below are for links already sent and for the test installs.

| Today | Becomes | Notes |
| --- | --- | --- |
| Addresses `#/cases/…` | `#/aiops/…` | The router sends every old `#/cases/…` address to its new place with `replace`, as it already does for the tabs that moved (`lib/router.ts:202-215`). Links in notifications and on Confluence pages already sent must still open. |
| Area id `cases` in the browser app (`Area`, `view: "cases"`, `CASES_TABS`, `casesHref`, `currentCasesItem`) | `aiops` | 57 files name the address or `casesHref`. |
| `web/src/components/cases/` | `web/src/components/aiops/` | 31 files import from it. `web/src/lib/cases.ts` keeps its name: it is about cases. |
| API `/api/v1/cases/…` | `/api/v1/aiops/…` | So `/api/v1/aiops/cases`, `/api/v1/aiops/proposals`, `/api/v1/aiops/exceptions`, and so on. 19 Go files. Only the browser app calls it (`221b-cli` does not). |
| ConfigMap `<release>-cases`, env `CASES_CONFIGMAP` | `<release>-proposals`, `PROPOSALS_CONFIGMAP` | At start, when the new object is empty and the old one exists, copy the old one's records over once and log it, so a test install keeps its cards. Chart: `templates/configmap.yaml:136`, `deployment.yaml:192`; RBAC rules that name the object. |
| Go type `issues.Report` | `issues.Case` | 13 files. The JSON and `/api/issues/v1/reports` stay as they are: a person's report is still what the intake takes. |
| `ai/cases.go`, `ai/cases/base.json`, `Cases`, `BaseCases`, `CasesOf`, `SaveCase`, `DeleteCase`, `CaseResult` | `ai/tests.go`, `ai/tests/base.json`, `Tests`, `BaseTests`, `TestsOf`, `SaveTest`, `DeleteTest`, `TestResult` | The types are already `TestCase` and `TestRun`. Mind the `//go:embed` line. |
| `ai/prompts/cases.md` (the Cases skill) | `ai/prompts/aiops.md` (the AIOps skill) | And `CasesSkill()`, `ai/casesskill_test.go`, and its mentions in `docs/prompt-sources.md` and `ai/prompts.go`. |

Keep as they are: the packages `cards`, `casework` and `issues`; the chart key `cases:` and Settings › Cases (they configure cases); the ids `C-n` and `PC-n`; `CASEWORK_CONFIGMAP` and `CASE_CONFIGMAPS`.

This replaces the last sentence of CF-91 ("Addresses and the API keep their names"): add a requirement row and an ADR that say so, and correct the CHANGELOG's CF-91 line.

**Done when:** `grep -rn '#/cases' web/src server` finds only the redirect table and its tests; `make e2e-sim` passes; an upgrade test (`hack/upgrade-kind.sh` or a Go test of the copy) shows a card filed before the rename still there after it; the old addresses in `cards/notify.go` and `confluence/publisher.go` are the new ones (A-5).

---

## Low

### Case workflow

- **W-8. Routing that fails is not retried.** `casework/route.go:222` logs a warning and returns the findings unrouted. Keep the run marked as *not routed* and route it on the next tick; show it on the run.
- **W-9. A case and its work record are saved in two writes.** A failure of the second is only logged (`casework/changes.go:261`, `actions.go` after `apply`). Have Housekeeping reconcile: a closed case whose work record is open, a card applied whose case does not name it.
- **W-10. A proposal's dedup key is its title.** `jobs/run.go:505` and `casework/analysis.go:547` key on the lower-cased title, which the model writes; a reworded title files the card again. Key on the cluster, the sorted cases it resolves and a hash of the commands.
- **W-11. A leader change during an apply can flip a card's state twice.** The new leader's sweep fails an Approved card after five minutes (`cards/store.go:1225`) while the old leader may still be applying it, and the late result then overwrites Failed. Have `apply`'s last write keep a Failed state set by the sweep and add its results as a note, or stop the apply when the lease is lost.

### Sherlock's names

- **S-4. The core prompt's list of tabs is short.** `sherlock/prompt.go`, "Your place in the console": add Storage, and say the Automation area's tabs (Scripts, Run history, Variables, Run profiles); "Admin" should read Administration; the Pods statuses should include `notready`. **Prompt**, exact names only.
- **S-5. Two id sets for the Policies sections.** `list_policies` takes `priority`, `drain` and `limits` (`api/sherlock_tools.go:1279-1287`); the Policies tab's path takes `prio` and has no `drain` or `limits` (`web/src/lib/uiControl.ts`, `policies`). Make the tab accept the tool's ids (keep `prio` as an alias), so Sherlock can show what it just read.
- **S-6. Two step labels show raw ids.** `sherlock/ui.go:270` (`uiLabel`) has no case for `open_tool_dialog`; `sherlock/agents.go:673` returns `"Ask " + in.Agent`, which reads "Ask changePlanner". Label them "Opened a tool dialog" and "Asked the Change planner" (the agent's `Name`).
- **S-7. Two limits on view changes.** The prompt says at most four changes for one question; `sherlock/ui.go:79` allows ten actions, reads included. Keep ten as the hard cap, and build the prompt's number from a constant.
- **S-8. Three formats for an object.** `221b://Kind/namespace/name` in answers, `namespace/Kind/name` for a finding's resource, `namespace/kind/name` for evidence (`jobs/parse.go`, `OutputFormat`). `NormalizeObject` accepts either order. Left for the prompt review; no code change.

### Prompts

- **P-3. Prompt text is in about twelve places.** `ai/prompts/*.md`, `sherlock/prompt.go`, `sherlock/agents.go` (tool descriptions), `sherlock/ui_actions.json`, `jobs/kinds.go`, `jobs/parse.go`, `jobs/keys.go`, `casework/analysis.go`, `casework/trouble.go`, `investigate/investigate.go`, `knowledge/sync.go`, `api/automation_more.go`. Add one Go test that renders every prompt and task with sample data into a golden directory (`server/internal/ai/testdata/prompts/`), so a review reads one place and a change shows in a diff. Move the three Automation prompts out of `api` into `automation` (A-2).
- **P-4.** The audit task opens "This is the nightly audit" whatever started it (`jobs/kinds.go:140`). *Prompt review.*
- **P-5.** "Check" and "validation rule" name one thing inside one prompt (`jobs/keys.go:380`, `OutputFormat`). *Prompt review.*
- **P-6.** Spelling is mixed in prompts ("Analyse it", "analysed", "catalogue" against "Analyze"). American, with U-1. *Prompt review* for the prompts; U-1 for the console.
- **P-7.** The script header is written `# 221b: k8s verb resource` in the draft prompt and `# 221b:k8s` in the review prompt and the parser's messages (`api/automation_more.go:312-340`, `automation/header.go`). The parser accepts both; the form without the space is the one to teach. *Prompt review.*
- **P-8. A test for the names in prompts.** A Go test (or a vitest that reads the prompt files) that takes the console names a prompt quotes and fails when one is not a label in `web/src`. Start with the AIOps skill; the list of names can be a small file beside the prompt. `ai/casesskill_test.go` checks a few phrases of the skill today, but not that the console has them. It would have caught S-2.
- **P-9.** Alerts are error or warning; findings are critical, warning or info; `jobs/store.go:223` maps high and error to critical. Say the mapping in the alert task. *Prompt review.*

### Architecture and code

- **A-2. `api` holds logic beside HTTP.** About 18,000 lines without tests. Move the Automation prompts (`api/automation_more.go:312-340`) to `automation`, and Sherlock's toolbox (`api/sherlock_tools.go`, 1,519 lines) to a package of its own that `api` wires. No change in behaviour.
- **A-3. `cmd/221b/main.go` is 1,365 lines of wiring.** Finish the pattern of the files beside it (`ai.go`, `cards.go`, `casework.go`): one `wireX` function per feature.
- **A-4. Two code styles in the browser app.** 25 files have semicolons and double quotes (`lib/router.ts`, `TopBar.tsx` among them), 325 have neither, and no formatter is configured. Add Prettier with the majority style to `web/package.json` and `make lint-web`, and format once in a commit of its own.
- **A-5. The server emits addresses that only work through redirects.** `confluence/publisher.go:1148` and `:1307` (`#/cases/known…`), `cards/notify.go:333` (`#/cases/inbox?proposal=`). Emit the current addresses; with A-1, the new ones.
- **A-6. A name is cut at 60 bytes.** `ai/cases.go:115` (`c.Name[:60]`) can split a multi-byte character. Cut on a rune boundary, as `viewBlock` in `sherlock/ui.go` does.
- **A-7. One thing, several words.** Proposal, proposed change, card and change (PC-n); check and validation rule; reject and refuse. In everything a person reads: *proposed change*, *validation rule*, *reject* (U-3). In code the package names stay.

### Labels and texts

| ID | Where | Now | Change to |
| --- | --- | --- | --- |
| U-1 | `CaseDrawer.tsx:956` and `:911`, `BulkBar.tsx:174` and `:188`, `inboxRows.tsx:523` | Analyse, Analyse again, Analyse now | Analyze, Analyze again, Analyze now. Look for other British spellings in strings a person reads (`grep -rniE "analys|behaviour|colour|catalogue|organis|recognis" web/src`); comments may stay. |
| U-2 | `NewReport.tsx:22`, `AlertReports.tsx:81`, `CasesSettings.tsx:22` | New Case; "New Case looks back" | New case; "New case looks back" |
| U-2 | `web/src/lib/router.ts:57`, `:62` | Run Profiles, Run History | Run profiles, Run history |
| U-3 | `server/internal/knowledge/jobs.go:305-307` and other run facts | "Proposals", "proposal waits", "proposals wait" | "Proposed changes", "proposed change waits", "proposed changes wait" |
| U-4 | `HostDialog.tsx:601` | Check connection | Test connection, as the cluster editor and the S3 backup say |
| U-5 | `AdminPage.tsx:28` | "SuperAdmin only: refresh rate, Sherlock and Watson" | "SuperAdmin only: general, AI, changes and AIOps settings" |
| U-6 | `investigate/investigate.go:69`; `SettingsPage.tsx:56` | The job *Report triage*; the hint "Report page, timers, API tokens" | The job's label becomes *Case triage* (its id `report-triage` stays, so settings kept for it still apply). The Issues hint stays: the report page is still called that. |
| U-7 | Steps under Sherlock's answers | `open_tool_dialog`, "Ask changePlanner" | See S-6. |
| U-8 | Documents | The area is *Cases* in older requirement rows and changelog lines | See [Documentation](#documentation). |
| U-9 | Script header help and messages | `# 221b:describe` and `# 221b: describe:` | The form without the space everywhere a person reads it (P-7). |

Each label change needs its tests and end-to-end specs changed with it (`grep -rn` the old text under `web/src` and `web/e2e`).

### UI improvements

- **I-1. Show what a verification rested on.** On a case whose fix is verified, name the run and what it re-checked ("Verified by the Nightly audit of Nov 12: read the Deployment, 3 of 3 ready"), from the `rechecked` evidence of W-1. On a case still waiting, say since when and how many runs have passed.
- **I-2. Mark an incomplete change on its card**, if W-6 keeps such cards at all.
- **I-3. Say on a run what it could not be used for.** In AIOps › Jobs › Runs: the clusters that failed (W-2), findings left out by the cap (W-3), fixes not re-checked (W-1), routing that did not happen (W-8).
- **I-4. One dialog for Reject… and Withdraw….** Four places build their own: `InboxApprovals.tsx:99`, `WikiPageActions.tsx:225`, `ProposalDrawer.tsx:218`, `ScriptDrawer.tsx:997`. One shared component keeps the wording and the required reason the same.
- **I-5. Show the agent on each job's row** ("Nightly audit · Auditor"), so it is plain that several jobs share one agent and its settings.

---

## Documentation

**Fixed on `phase-12.0-ai`** (commit `0404522`, November 10, 2026):

| ID | Document | Was | Is |
| --- | --- | --- | --- |
| D-1 | `docs/architecture.md:3` | "as built through Phase 6" | Phase 12 |
| D-2 | `docs/architecture.md:41` | Sherlock has 22 reads | 27 |
| D-3 | `docs/architecture.md:50`, ADR-88 | The area's tabs: Inbox, Runs, Reports, Known issues, Jobs, Knowledge | Inbox, Jobs, Mycroft, with what each holds; ADR-88's consequences say what changed since |
| D-5 | `CHANGELOG.md` | The Inbox bell is left of *Live* | Right of *Live* (CF-80, `TopBarOrder.test.tsx`) |
| D-7 | `docs/states.md` | "the Cases area" | "the AIOps area" |
| D-8 | `README.md` | Sherlock is turned on in Administration → Settings → Sherlock | Settings → Providers, then Settings → Agents; the Settings screenshot's caption says it is from an earlier release |

**Not a defect:** D-6. `docs/prompt-sources.md` lists every source's commit as *to be recorded*; the file itself explains that this is allowed until text is copied, and none is.

**Left open (D-9):**

- **Screenshots.** `docs/screenshots` dates from October 26 and shows Settings with three sections, and nothing of AIOps or Automation. After the label changes: `make screenshots`, add shots of AIOps › Inbox, a case's drawer, AIOps › Jobs and Settings, and put them in the README's table. The spec is `web/screenshots/`.
- **The 0.12.0 changelog reads as a diary.** Early lines describe the Cases area, a Reports tab and *New report*; later lines replace them. Before the release (`docs/releasing.md`), rewrite the 0.12.0 section as what ships.
- **After A-1:** `docs/architecture.md` (the area's row, the API and ConfigMap names, ADR-88's note), `docs/states.md`, `docs/issues.md`, `docs/upgrading.md` (the renamed ConfigMap and its one-time copy), `docs/prompt-sources.md` (`aiops.md`), `README.md`.
- **After W-1:** `docs/states.md`, the paragraphs on verification.

## Left for the prompt review

The owner will ask for a second review once this work is done, to update the agents' and jobs' prompts. Do not rewrite these now:

- The wording of `ai/prompts/*.md` beyond the exact name corrections of S-2 and S-4, the sentence of P-1 and the generated list of S-1. The AIOps skill is about 1,600 words in ten long bullets and should be restructured then.
- P-4, P-5, P-6 (in prompts), P-7, P-9 and S-8.
- The wording around the new `rechecked` field: put in a plain first version (W-1 step 3); it will be reviewed with the first real runs.
- Anything in `knowledge/sync.go`'s `syncPrompt`.

## To check on a running console

The review did not see the console run. With `make dev-sim` or the test install:

- The top bar at 1280 px with three areas, the bell and every tab of the Kubernetes area: nothing in use hidden behind *More*.
- A case's drawer with *Troubleshoot* open: the case on the left, Sherlock and Watson on the right.
- Whether select boxes still show their value in a monospace face while text inputs do not, as in `docs/screenshots/admin-settings.png`.
- Every label changed in U-1 to U-6, in place, and that no truncation follows from the longer ones.
- An old address of each kind (`#/cases/inbox/case/1`, `#/cases/known/KI-1`, `#/cases/inbox?proposal=PC-1`) opens its new place after A-1.

## Open question

Below P1, one clean run closes an agent-found case as Fixed. With W-1 that run must have re-checked the object, which removes the false closures found here. Whether a fix should need two clean runs in a row before the console closes the case is a product decision that this review leaves to the owner; the code and `docs/states.md` say one.
