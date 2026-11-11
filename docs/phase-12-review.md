# Phase 12 code review and hand-over

A review of 221B on `phase-12.0-ai`, read at commit `d255427` on November 9, 2026 and checked again at `52fc821` on November 10 (the one commit between them touches none of the findings). It covers the architecture, the consistency of the code, the names Sherlock is given against the application, the case workflow end to end, every agent's and job's prompt, and the console's labels and texts. Where a document and the code differ, the code was taken as truth.

A **second review** on November 10, after the fixes, ran the tests and the console for the first time and is at the end: [Second review](#second-review). What is still open after it, written for whoever fixes it, is in [Hand-over: what is open after the second review](#hand-over-what-is-open-after-the-second-review).

This file is also the hand-over: the owner accepted every finding, fix and improvement on November 10, 2026, and they are to be made on `phase-12.0-ai`. Each finding says where it is, what is wrong, what to do and how to tell it is done. The documentation fixes that could be made without code changes are already on the branch (see [Documentation](#documentation)).

## Summary

The architecture is sound: one composition root, small packages that do not import each other in circles, and every change behind a person's approval that is enforced in code and not in a prompt. Two groups of problems need work.

1. **A fix can be marked verified, and its case closed, without any run having looked at it** (W-1, W-2, W-3, with W-4 and P-2). Verification is inferred from a run *not* reporting a problem, and the run's scope is taken to be its whole cluster.
2. **What Sherlock is told about the console has drifted from the console** (S-1, S-2, S-3). The tool names are right; the hand-written lists of Watson's commands, of buttons and of filters are not.

| Severity | Count | Findings |
| --- | --- | --- |
| High | 3 | W-1, W-2, W-3 |
| Medium | 9 | W-4, W-5, W-6, W-7, S-1, S-2, S-3, P-1 (with P-2), A-1 |
| Low | 43 | W-8 to W-11, S-4 to S-8, P-3 to P-9, A-2 to A-7, U-1 to U-16, I-1 to I-4, D-9 |

Severity: **High** is a wrong outcome recorded with no person involved; **Medium** is wrong or misleading behaviour a person can catch; **Low** is consistency, text or structure.

## How the review was done

- Read: `server/internal/ai` (catalog, instruction layers, every file in `prompts/`), `sherlock` (core prompt, view control, hand-over), `jobs`, `casework`, `cards`, the case parts of `issues`, the task prompts in `investigate`, `knowledge/sync.go` and `api/automation_more.go`, `watson/parse.go`, `api/sherlock_tools.go`, and in the browser app `lib/router.ts`, `lib/uiControl.ts`, `lib/cases.ts`, the top bar, Administration, Settings and `components/cases`.
- Ran: `gofmt -l` (clean), `tsc --noEmit` (clean), `eslint` (clean), `vitest` (all pass), and a script over the internal imports (acyclic; `api` imports 36 packages and nothing imports `api`).
- Compared by script: the 27 tool names in `ai/catalog.go` against `api/sherlock_tools.go`; every console address the server and the prompts emit against the router; every button label in the browser app for case and wording.
- **Not run:** `go build`, `go vet` and the Go tests (the review environment had Go 1.24 and `go.mod` needs 1.26), the kind and simulator end-to-end tests, and the chart checks.
- **Not seen:** the running console. The test install could not be reached from the review's browser, so the UI findings U-1 to U-9 come from the source. On November 10 the Screenshots workflow retook `docs/screenshots` from the simulated cluster, with six new AIOps pages; U-10 to U-16 were found on those. The section [To check on a running console](#to-check-on-a-running-console) lists what is still open.
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
| 5 | U-1 to U-16, I-1 to I-4 | Labels, texts and the UI improvements |
| 6 | W-8 to W-11, A-2 to A-4, A-6, P-3 | The remaining Low findings |
| 7 | D-9 and the documentation left open | Documents and screenshots, once the names have settled |

---

## High

### W-1. An alert diagnosis verifies fixes it never looked at *fixed* (0.12.0)

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

### W-2. A cluster the audit failed to read still counts as audited *fixed* (0.12.0)

**Where:** `server/internal/jobs/kinds.go:148-153` (`runAudit`), `server/internal/jobs/run.go:381`.

**What is wrong:** In an audit of several clusters, a cluster whose request fails is noted and skipped (`env.Note(...)`, `continue`), the function returns no error, the run ends OK, and `RouteInput.Clusters` holds every cluster the run was given. Every fix waiting on the skipped cluster is then verified.

**Do:** Return, with the result, the clusters that answered, and build the run's scope (W-1) from those only. Show the clusters that failed on the run (I-3): a run with a failed cluster is *Done, with errors*, not plain *Done*.

**Done when:** a Go test with two clusters, one of which fails, shows no case on the failed cluster verified, and the run says which cluster failed.

### W-3. Findings past the cap are dropped and then count as not seen *fixed* (0.12.0)

**Where:** `server/internal/jobs/store.go:34` (`maxFindings = 40`), `:214` (`tidy`).

**What is wrong:** A run keeps at most 40 findings over all its clusters. `tidy` drops the rest in the order they came, so in a three-cluster audit the last cluster's findings go first, a critical one as easily as an info one. Dropped findings never reach the router, so their keys count as not seen.

**Do:** With W-1, a dropped finding no longer verifies anything. Still: sort by severity before cutting so that the least severe are the ones dropped, mark the run as truncated, and say on the run how many were left out and of which severity. A truncated run should not be the reason an open agent-found case is called "not seen again" either.

**Done when:** a Go test with 45 findings, the critical ones last, keeps every critical finding, and the run carries the note.

---

## Medium

### W-4. Cases closed as Fixed and waiting for verification are not in the agent's keys *fixed* (0.12.0)

**Where:** `server/internal/casework/route.go:545` (`l.AgentFound && l.Open()`).

**What is wrong:** `KeysInUse` lists open cases only. A case a person closed as Fixed is closed, so the next run is not told its key. If the agent names the same problem with another rule id or another object, a new and unrelated case opens, and the original's fix is verified because its key was not seen.

**Do:** Include the cases that wait for verification, said as such ("fix applied, re-check: C-12"). They are the list W-1 step 2 needs.

**Done when:** `TestKeysInUse…` covers a closed case with `VerifyPending`.

### W-5. An agent can file findings and changes for a cluster it was not asked about *fixed* (0.12.0)

**Where:** `server/internal/jobs/kinds.go:107` (`merge`: the cluster is filled in only when the agent left it empty), `server/internal/jobs/parse.go` (`Changes`: `cl := c.Cluster`), `server/internal/jobs/store.go:214` (`tidy` does not check the cluster).

**What is wrong:** Each request is about one cluster, but the cluster on a finding or a proposal is whatever the agent wrote. An audit of cluster A can open a case or file a change on cluster B, one the job does not even cover. Text planted in cluster A (an annotation, a log line) could steer that. A person still has to approve the change, which is why this is Medium.

**Do:** For a request about one cluster, set the cluster on every finding and proposal to that cluster and drop, with a note, anything that named another. `casework/analysis.go:542` already does this for the case analysis.

**Done when:** a Go test whose agent answers with another cluster's id shows nothing filed there and a note on the run.

*Fixed* (0.12.0, CF-97, ADR-147): `merge` and `Changes` hold every request to the cluster its agent was asked on (named by id or name, or not at all); the rest is left out with a note. The digest, asked on its first cluster, is held to that one. `tidy` is left as it was: it gets findings already set to the cluster. `TestNothingIsFiledForAnotherClusterCF97`.

### W-6. A change is filed with its refused commands silently removed *fixed* (0.12.0)

**Where:** `server/internal/jobs/parse.go:141-153` (`Changes`); `server/internal/casework/analysis.go:521-530` (a case's suggested commands).

**What is wrong:** When Watson would refuse one command of a proposal, that command is taken out and the rest is filed. Cordon, drain, a refused step, uncordon becomes a different change from the one the agent planned. The note is on the run; the card and the case show the shortened list as if it were whole.

**Do:** Drop the whole proposal (or the whole suggested fix) when any command is refused, and say on the run and on the case's analysis which command was refused and why. Where dropping loses too much, the alternative is to keep it but mark the card *Incomplete* with the refused lines shown and approval disabled (I-2); pick one and use it in both places.

**Done when:** tests for both paths; no card is ever filed with fewer commands than the agent gave without saying so on the card.

*Fixed* (0.12.0, CF-98, ADR-147): the whole proposal, or the whole suggested fix, is dropped; the run's note and the analysis's `leftOut` (shown under *Fix* in the case's drawer) name the command and why. A line that is not kubectl or helm, and a fix of more than five commands, are dropped the same way. No *Incomplete* card, so I-2 is not needed. `TestAChangeIsFiledWholeOrNotAtAllCF98`, `TestAnAnalysisKeepsAFixWholeOrNotAtAllCF98`.

### W-7. A failed read of the exceptions is ignored *fixed* (0.12.0)

**Where:** `server/internal/casework/route.go:87`, `:562`; `server/internal/casework/actions.go:276` (`list, _ := s.o.Lessons.Exceptions(ctx)`).

**What is wrong:** If the exceptions cannot be read, the list is empty and every finding an exception covers opens a case, with the notifications and the analysis that follow.

**Do:** In `Route`, return the error so the findings stay unrouted (and are retried, W-8). In `KeysInUse` and `actions.go`, return the error to the caller.

**Done when:** a Go test with a failing lessons store shows no case opened.

*Fixed* (0.12.0, CF-99): `Route` returns the findings unrouted with why (`RouteResult.NotRouted`), retried as W-8 says; `KeysInUse` returns `([]KeyInUse, error)`, and a run whose keys cannot be read says so and goes on without them; `SimilarTo` returns the error. (`KeysInUse` still lists the cases and known issues it could read when those reads fail, as before; only the exceptions fail closed.) `TestAFailedReadOfTheExceptionsOpensNoCaseCF99`.

### S-1. Sherlock's list of what Watson runs is out of date — *fixed* (0.12.0)

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

### S-2. Names in Sherlock's Cases skill that the console does not use — *fixed* (0.12.0)

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

### S-3. The view tools describe filters and tabs differently from what the browser accepts — *fixed* (0.12.0)

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

Done with tests on both sides against a shared fixture, `server/internal/sherlock/testdata/ui_tabs.json`: `uiControl.test.ts` holds `TAB_SPECS` to it exactly, and `ui_test.go` holds the descriptions of `view_open` and `view_filter` to it. The descriptions stay written for the model, with what each value means, which a sentence generated from the table could not say.

### P-1. The Troubleshooter proposes Watson commands without knowing what Watson runs — *fixed* (0.12.0)

**Where:** `server/internal/ai/catalog.go` (`Troubleshooter`: no `check_command`; only `ChangePlanner` has it), `server/internal/ai/prompts/troubleshooter.md`, `common.md`.

**What is wrong:** The Troubleshooter writes the commands for the case analysis, the alert diagnosis and the report triage. Only Sherlock's core says what Watson runs, and the Troubleshooter cannot check a command. What Watson would refuse is taken out afterwards (W-6).

**Do:** Add `check_command` to the Troubleshooter's tools (it changes nothing; see `sherlock/agents.go`, `ToolCheckCommand`), and the generated list of S-1 to `common.md`'s place. **Prompt:** one sentence in `troubleshooter.md` to check each command before giving it, as `changePlanner.md` has.

**Done when:** the S-1 test covers the Troubleshooter, and a test in `ai/ai_test.go` expects the tool among the Troubleshooter's.

### P-2. No task asks the agent what it re-checked *fixed* (0.12.0)

Part of W-1 (steps 2 and 3). Listed separately because it is the prompt half: `jobs/keys.go:387` tells the agent to reuse a key for the same problem, and nothing asks it to look at each listed key again and say what it found.

### A-1. "Cases" names six different things *fixed* (0.12.0)

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

*Fixed* (CF-104, ADR-152), as the table says. The router reads `#/cases/…` as the same page under `#/aiops/…` and `AIOpsPage` replaces the address; `lib/casesItem.ts` became `lib/aiopsItem.ts` and `CasesPage` `AIOpsPage` with the area. The server emits the drawer's own address where it had a query (`#/aiops/inbox/proposal/PC-n`, and `proposalHref` in the browser app), and a known issue at `#/aiops/mycroft/known/KI-n`. The copy of `<release>-cases` into `<release>-proposals` runs before the cards open, copies only keys the new object lacks, the cards last, and is logged; the chart keeps making `<release>-cases` so that `helm upgrade` does not delete it first (`TestTheProposedChangesOfBeforeAreCopiedOnceCF104`). Sherlock's view says `"view":"aiops"`, and the server still reads `"cases"` from a page loaded before. Also kept: the Secret `<release>-cases` (the channels' addresses, not a ConfigMap), the API's route names (`cases-proposals`…), the test ids (`cases-page`, `area-cases`…), `MaxCases` and `sherlock.Question.Cases` (the flag that turns the skill on).

---

## Low

### Case workflow

- **W-8. Routing that fails is not retried.** *fixed* (0.12.0) `casework/route.go:222` logs a warning and returns the findings unrouted. Keep the run marked as *not routed* and route it on the next tick; show it on the run.
  *Fixed* (CF-99): the run keeps `notRouted` (why) and `reroute`; the leader's tick routes it again, at most once a minute, oldest first, with `OK: false` so a late routing verifies no fix; it lets a run go, with a note, when its job is gone, a later run of the job was routed since, or a day after it ended. The run's drawer says it. The proposed changes of a run not routed that fix its findings by position resolve nothing and are not filed (noted, as before); the next run proposes them again. `TestARunNotRoutedIsRoutedLaterCF99`, `TestARunNotRoutedIsLetGoAfterALaterOneCF99`.
- **W-9. A case and its work record are saved in two writes.** *fixed* (0.12.0) A failure of the second is only logged (`casework/changes.go:261`, `actions.go` after `apply`). Have Housekeeping reconcile: a closed case whose work record is open, a card applied whose case does not name it.
  *Fixed* (CF-100, `casework/reconcile.go`): "open" is read as what a close lets go of after its first write (`closed`: the conversation, the commands other than the changes that finished done, the waiting changes that resolve only the case); Housekeeping does that for the cases closed in the last 7 days, and acts again (`onCard`) for an applied change of the last 7 days whose case's `appliedBy` does not name it. Each in the case's history and audited `case.reconcile`. What a close writes to the work itself (*Closed as fixed: …*, the RCA) cannot be rebuilt from the case and is not. `TestHousekeepingPutsACaseRightWithItsWorkCF100`.
- **W-10. A proposal's dedup key is its title.** *fixed* (0.12.0) `jobs/run.go:505` and `casework/analysis.go:547` key on the lower-cased title, which the model writes; a reworded title files the card again. Key on the cluster, the sorted cases it resolves and a hash of the commands.
  *Fixed* (CF-101, ADR-148): `cardid.Key`, set in `jobs.file` once what a proposal resolves is known, for the runs, the case analysis and the case triage (`investigate`, which keyed on the title too). A card waiting under a key of before is replaced by the same change (`cards.keyedBefore`), so the change of form files nothing twice. `TestKey`, `TestAProposalIsKeyedOnWhatItIsAboutCF101`, `TestACardKeyedBeforeIsReplacedByTheSameChangeCF101`.
- **W-11. A leader change during an apply can flip a card's state twice.** *fixed* (0.12.0) The new leader's sweep fails an Approved card after five minutes (`cards/store.go:1225`) while the old leader may still be applying it, and the late result then overwrites Failed. Have `apply`'s last write keep a Failed state set by the sweep and add its results as a note, or stop the apply when the lease is lost.
  *Fixed* (CF-100, ADR-149): the first, simpler one. `TestALateApplyKeepsTheSweepsFailedCF100`.

### Sherlock's names

- **S-4. The core prompt's list of tabs is short.** — *fixed* (0.12.0). `sherlock/prompt.go`, "Your place in the console": add Storage, and say the Automation area's tabs (Scripts, Run history, Variables, Run profiles); "Admin" should read Administration; the Pods statuses should include `notready`. **Prompt**, exact names only.
- **S-5. Two id sets for the Policies sections.** — *fixed* (0.12.0). `list_policies` takes `priority`, `drain` and `limits` (`api/sherlock_tools.go:1279-1287`); the Policies tab's path takes `prio` and has no `drain` or `limits` (`web/src/lib/uiControl.ts`, `policies`). Make the tab accept the tool's ids (keep `prio` as an alias), so Sherlock can show what it just read.
- **S-6. Two step labels show raw ids.** — *fixed* (0.12.0). `sherlock/ui.go:270` (`uiLabel`) has no case for `open_tool_dialog`; `sherlock/agents.go:673` returns `"Ask " + in.Agent`, which reads "Ask changePlanner". Label them "Opened a tool dialog" and "Asked the Change planner" (the agent's `Name`).
- **S-7. Two limits on view changes.** — *fixed* (0.12.0). The prompt says at most four changes for one question; `sherlock/ui.go:79` allows ten actions, reads included. Keep ten as the hard cap, and build the prompt's number from a constant.
- **S-8. Three formats for an object.** *fixed* (0.12.0, CF-106): an evidence's `ref` is `namespace/Kind/name`, as a finding's resource; the link form in answers stays, being an address. `221b://Kind/namespace/name` in answers, `namespace/Kind/name` for a finding's resource, `namespace/kind/name` for evidence (`jobs/parse.go`, `OutputFormat`). `NormalizeObject` accepts either order. Left for the prompt review; no code change.

### Prompts

- **P-3. Prompt text is in about twelve places.** — *fixed* (0.12.0); the move of the Automation prompts with A-2. `ai/prompts/*.md`, `sherlock/prompt.go`, `sherlock/agents.go` (tool descriptions), `sherlock/ui_actions.json`, `jobs/kinds.go`, `jobs/parse.go`, `jobs/keys.go`, `casework/analysis.go`, `casework/trouble.go`, `investigate/investigate.go`, `knowledge/sync.go`, `api/automation_more.go`. Add one Go test that renders every prompt and task with sample data into a golden directory (`server/internal/ai/testdata/prompts/`), so a review reads one place and a change shows in a diff. Move the three Automation prompts out of `api` into `automation` (A-2).
- **P-4.** *fixed* (0.12.0, CF-106) The audit task opens "This is the nightly audit" whatever started it (`jobs/kinds.go:140`). *Prompt review.*
- **P-5.** *fixed* (0.12.0, CF-106) "Check" and "validation rule" name one thing inside one prompt (`jobs/keys.go:380`, `OutputFormat`). *Prompt review.*
- **P-6.** *fixed* (0.12.0, CF-106) Spelling is mixed in prompts ("Analyse it", "analysed", "catalogue" against "Analyze"). American, with U-1. *Prompt review* for the prompts; U-1 for the console.
- **P-7.** *fixed* (0.12.0, CF-106) The script header is written `# 221b: k8s verb resource` in the draft prompt and `# 221b:k8s` in the review prompt and the parser's messages (`api/automation_more.go:312-340`, `automation/header.go`). The parser accepts both; the form without the space is the one to teach. *Prompt review.*
- **P-8. A test for the names in prompts.** — *fixed* (0.12.0). A Go test (or a vitest that reads the prompt files) that takes the console names a prompt quotes and fails when one is not a label in `web/src`. Start with the AIOps skill; the list of names can be a small file beside the prompt. `ai/casesskill_test.go` checks a few phrases of the skill today, but not that the console has them. It would have caught S-2.
- **P-9.** *fixed* (0.12.0, CF-106) Alerts are error or warning; findings are critical, warning or info; `jobs/store.go:223` maps high and error to critical. Say the mapping in the alert task. *Prompt review.*

### Architecture and code

- **A-2. `api` holds logic beside HTTP.** *fixed* (0.12.0) About 18,000 lines without tests. Move the Automation prompts (`api/automation_more.go:312-340`) to `automation`, and Sherlock's toolbox (`api/sherlock_tools.go`, 1,519 lines) to a package of its own that `api` wires. No change in behaviour.
  *Fixed:* the three prompts are `automation.ImpactPrompt`, `DraftPrompt` and `ReviewPrompt` (`automation/prompts.go`), with their golden test. The toolbox, `review_cluster` with it, is package `sherlocktools`: `api` gives it the console's API (called in-process as before) and the two reads that do not go through it, the home page's summary and the cached events the person may list (`api/sherlock_tools.go`). `TestSherlockToolsOnlyRead`, `TestEveryAgentToolExists` and the toolbox's golden test moved with it; the tests that need a running console stay in `api`. The golden prompts are unchanged byte for byte.
- **A-3. `cmd/221b/main.go` is 1,365 lines of wiring.** *fixed* (0.12.0) Finish the pattern of the files beside it (`ai.go`, `cards.go`, `casework.go`): one `wireX` function per feature.
  *Fixed:* `main.go` (about 360 lines) keeps the flags, the order of the wiring and the server's start and stop; each feature is a `wireX` method of `wiring`, the console as it is wired, in a file of its own (`auth.go`, `leader.go`, `clusters.go`, `settings.go`, `sherlock.go`, `watson.go`, `ssh.go`, `automation.go`, `sessions.go`, `api.go` and `metrics.go`, and beside the existing `openX` in `helmrepos.go`, `jobs.go`, `issues.go`, `knowledge.go`, `cards.go`, `casework.go` and `backupparts.go`). The code moved as it was: the same steps in the same order, the same flags and logs.
- **A-4. Two code styles in the browser app.** 25 files have semicolons and double quotes (`lib/router.ts`, `TopBar.tsx` among them), 325 have neither, and no formatter is configured. Add Prettier with the majority style to `web/package.json` and `make lint-web`, and format once in a commit of its own. *fixed* (0.12.0): Prettier 3.9.10 pinned in `web/package.json`, `npm run format` / `format:check`, in `make lint-web`; no semicolons, single quotes, trailing commas, and a print width of 240, the width the code was written to (160 would have grown the code by 16%, 240 by 3%); the code formatted once.
- **A-5. The server emits addresses that only work through redirects.** *fixed* (0.12.0, with A-1) `confluence/publisher.go:1148` and `:1307` (`#/cases/known…`), `cards/notify.go:333` (`#/cases/inbox?proposal=`). Emit the current addresses; with A-1, the new ones.
- **A-6. A name is cut at 60 bytes.** *fixed* (0.12.0) `ai/cases.go:115` (`c.Name[:60]`) can split a multi-byte character. Cut on a rune boundary, as `viewBlock` in `sherlock/ui.go` does.
- **A-7. One thing, several words.** *fixed* (0.12.0) Proposal, proposed change, card and change (PC-n); check and validation rule; reject and refuse. In everything a person reads: *proposed change*, *validation rule*, *reject* (U-3). In code the package names stay. *Refuse* stays where the system declines something (Watson refusing a command, the server refusing a request); every decision a person makes on a proposed change already read *reject*.

### Labels and texts

| ID | Where | Now | Change to |
| --- | --- | --- | --- |
| U-1 *fixed* (0.12.0) | `CaseDrawer.tsx:956` and `:911`, `BulkBar.tsx:174` and `:188`, `inboxRows.tsx:523` | Analyse, Analyse again, Analyse now | Analyze, Analyze again, Analyze now. Look for other British spellings in strings a person reads (`grep -rniE "analys|behaviour|colour|catalogue|organis|recognis" web/src`); comments may stay. |
| U-2 *fixed* (0.12.0) | `NewReport.tsx:22`, `AlertReports.tsx:81`, `CasesSettings.tsx:22` | New Case; "New Case looks back" | New case; "New case looks back" |
| U-2 *fixed* (0.12.0) | `web/src/lib/router.ts:57`, `:62` | Run Profiles, Run History | Run profiles, Run history |
| U-3 *fixed* (0.12.0) | `server/internal/knowledge/jobs.go:305-307` and other run facts | "Proposals", "proposal waits", "proposals wait" | "Proposed changes", "proposed change waits", "proposed changes wait" |
| U-4 *fixed* (0.12.0) | `HostDialog.tsx:601` | Check connection | Test connection, as the cluster editor and the S3 backup say |
| U-5 *fixed* (0.12.0) | `AdminPage.tsx:28` | "SuperAdmin only: refresh rate, Sherlock and Watson" | "SuperAdmin only: general, AI, changes and AIOps settings" |
| U-6 *fixed* (0.12.0) | `investigate/investigate.go:69`; `SettingsPage.tsx:56` | The job *Report triage*; the hint "Report page, timers, API tokens" | The job's label becomes *Case triage* (its id `report-triage` stays, so settings kept for it still apply). The Issues hint stays: the report page is still called that. |
| U-7 (with S-6) | Steps under Sherlock's answers | `open_tool_dialog`, "Ask changePlanner" | See S-6. |
| U-8 *fixed* (0.12.0) (with D-9) | Documents | The area is *Cases* in older requirement rows and changelog lines | See [Documentation](#documentation). |
| U-9 *fixed* (0.12.0) | Script header help and messages | `# 221b:describe` and `# 221b: describe:` | The form without the space everywhere a person reads it (P-7). |

Each label change needs its tests and end-to-end specs changed with it (`grep -rn` the old text under `web/src` and `web/e2e`).

Notes on the fixes (0.12.0): U-1 also covers *analyzes*, *analyzing*, *analyzed*, *labeled*, *color* and *catalog* in what the console and the server show (case history, errors, a proposed change's impact, the audit log, the case analysis job's description); the task texts and `ai/prompts` are left to the prompt review. U-3 and A-7 also cover the server's errors, notifications, history lines and audit details about proposed changes (`cards`, `api/cards.go`, `api/proposaltrouble.go`, Backup's part *Agents' proposed changes*); the stored history line *replaced by the newer proposal PC-n* stays, as the store reads it back. U-6: the triage task's own text still says *report triage* (prompt review). U-7 is done with S-6 and U-8 with D-9. U-9: the new-script template, the drawer's hint and the bundled `reserve-node-resources.sh` use `# 221b:describe`; the draft prompt's header is P-7's.


### Seen on the retaken screenshots

Found on November 10 on the screenshots the workflow took from the simulated cluster (`docs/screenshots/aiops-*.png`, `admin-settings.png`).

*Fixed* (0.12.0, CF-105), all seven. U-10: `.tb-sub` was styled only inside a table, so in a case's drawer its second lines ran on; `styles/cases.css` now gives them a line of their own in a case's rows and sections (the SLA line too). U-11 and U-12: `aiops/runNames.ts` reads the agents' and clusters' names from the jobs' list for the Runs table and a run's drawer; a case's rule row and Troubleshoot pane use the case's cluster name. U-14 also covers a fix's status tag. U-15: `.cs-obj-clip`. U-16: the shared `Drawer` takes `prose` for a title in words, and `.rk-sel select` uses the page's face. Tests: `aiops/runNames.test.tsx`.

| ID | Where | What is wrong | Change to |
| --- | --- | --- | --- |
| U-10 | `CaseDrawer.tsx:781-793` (Validation rule), `:797-800` (Seen), `SourceText` (Opened from) | Adjacent parts have no space between them: "netpol.missinga namespace has no network policyon Namespace/analytics · local", "last 2026-11-09 6:48pm UTCfirst 2026-11-09 …", "an agent's finding in run-3nightly-audit" | A space or a separator between the parts (the spans sit side by side with none). Add a vitest that reads the row's text. |
| U-11 | `CaseDrawer.tsx:791` (`c.key.cluster`), `CaseTroubleshoot.tsx:123`, `ProposalTroubleshoot.tsx:113`, `RunsTab.tsx` (the Job cell) | The cluster's id is shown where its name belongs: "· local" and "Ask Sherlock about C-6 on local" on a cluster the top bar calls *simulated*; Runs says "auditor · local" | The cluster's name (`clusterName`), as the Inbox and the rules page show it |
| U-12 | `RunsTab.tsx:145` | The agent's id, "auditor", where Schedule shows its name, "Auditor" | The agent's name |
| U-13 | `RulesView.tsx:372` | The page's lead does not parse: "Agents use these rules for a portion of a problem identifiers - each finding becomes a case identified by cluster, object and rule." | "Agents name each finding with one of these rules. A finding becomes a case identified by its cluster, object and rule. When no rule fits, …" |
| U-14 | `RulesView.tsx:84` (`STATE_TEXT`) | State tags in lower case ("active", "retired") where every other tag is capitalised ("Paused", "Identified", "New") | Active, Retired, Replaced, Suggested |
| U-15 | The Inbox's table, Object column | A long object is cut with no ellipsis ("shop/Deployment/checl") | An ellipsis and the full name in the title, as the Title column does |
| U-16 | The case drawer's header and every `select` | The case's title and the values of select boxes are in the monospace face ("analytics has no network policy", "P2: Service degradation", "Nobody"), where the same words are proportional in the Inbox and in text inputs | The proportional face for words; monospace only for ids, object names and commands |

Also seen, and already covered: Watson's own help line in the dock lists `helm install`, `helm upgrade` and `helm uninstall` among its changes, which Sherlock's prompt does not (S-1); the case drawer's button reads *Analyse* (U-1); at 1440 px the Kubernetes area's tabs already need *More* (see [To check on a running console](#to-check-on-a-running-console)).

### UI improvements

- **I-1. Show what a verification rested on.** On a case whose fix is verified, name the run and what it re-checked ("Verified by the Nightly audit of Nov 12: read the Deployment, 3 of 3 ready"), from the `rechecked` evidence of W-1. On a case still waiting, say since when and how many runs have passed. *fixed* (0.12.0)
- **I-2. Mark an incomplete change on its card**, if W-6 keeps such cards at all. *Not needed:* W-6 drops such changes (0.12.0).
- **I-3. Say on a run what it could not be used for.** In AIOps › Jobs › Runs: the clusters that failed (W-2), findings left out by the cap (W-3), fixes not re-checked (W-1), routing that did not happen (W-8). *fixed* (0.12.0) for the clusters that failed, the findings left out and the fixes not re-checked; routing that did not happen goes with W-8.
  *The part of W-8 fixed* (0.12.0, CF-99): the run's drawer says "Not routed into cases: <why>", and whether the console tries again.
- **I-4. One dialog for Reject… and Withdraw….** *fixed* (0.12.0) Four places build their own: `InboxApprovals.tsx:99`, `WikiPageActions.tsx:225`, `ProposalDrawer.tsx:218`, `ScriptDrawer.tsx:997`. One shared component keeps the wording and the required reason the same. Three of the four already used `automation/DecideDialog.tsx`; the wiki page's request had a field in its card, and a console change's Withdraw… opened a dialog that said *Reject*. Now all four use `DecideDialog`, which says *Withdraw* to whoever asked (CF-102). The reason stays required where it was (a console change, a proposed change, a script's version) and optional for a wiki page's request, as it was.
- **I-5. Show the agent on each job's row** *fixed* (0.12.0) ("Nightly audit · Auditor"), so it is plain that several jobs share one agent and its settings. The agent was on the row's second line (*Auditor · all clusters*); it is now beside the name, with a hover on its shared settings (CF-103).

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

**D-9.** *fixed* (0.12.0), but for the screenshots themselves, which the integrator takes with `make screenshots`. It was left open:

- **Screenshots.** Retaken on November 10 by the Screenshots workflow: all nineteen, plus six of AIOps (Inbox, a case, Jobs, Runs, Validation rules, known issues), which are in the README's table. They show the labels as they were that day. After the label and name changes, run the workflow again by hand (Actions › Screenshots › Run workflow on the branch) or change `web/screenshots/readme.spec.ts`; the spec's addresses (`#/cases/…`) need the new ones after A-1. Still missing: the Automation area (the simulator has no sample scripts, so the spec would have to make some) and the Troubleshoot view of a case with Sherlock on. *fixed* (0.12.0) for the addresses: the spec uses `#/aiops/…`, and all twenty-five were retaken after the label and name changes; the two shots still missing stay open.
- **The 0.12.0 changelog reads as a diary.** Early lines describe the Cases area, a Reports tab and *New report*; later lines replace them. Before the release (`docs/releasing.md`), rewrite the 0.12.0 section as what ships.
- **After A-1:** `docs/architecture.md` (the area's row, the API and ConfigMap names, ADR-88's note), `docs/states.md`, `docs/issues.md`, `docs/upgrading.md` (the renamed ConfigMap and its one-time copy), `docs/prompt-sources.md` (`aiops.md`), `README.md`.
- **After W-1:** `docs/states.md`, the paragraphs on verification.

*Fixed* (0.12.0): `web/screenshots/readme.spec.ts` takes `aiops-inbox.png`, `aiops-case.png` and `aiops-jobs.png` (two cases made through the API first) and retakes `admin-settings.png` on Settings › Agents, and the README's table lists them. CHANGELOG.md's 0.12.0 section is rewritten as what ships: *Upgrading* (the records, the copy of `<release>-cases` into `<release>-proposals`, the API path), then *Added* by area, *Changed* for what differs from 0.11.1 and *Fixed* for what was wrong in it; the review's fixes are in the lines of the features they mend, with their ids. After A-1, `docs/architecture.md` (the area's row and ADR-88's note, the ConfigMap and API names), `docs/upgrading.md` (the copy) and `docs/prompt-sources.md` (`aiops.md`) were already right; the work-items row no longer gives an RCA to Knowledge (CF-44), and `docs/issues.md`, `docs/upgrading.md` and the README no longer name a Reports tab, *New report* in Cases, *Report triage* or a Cases page. `docs/states.md` says the W-1 rule (verified only on what a run re-checked inside its scope) and a proposed change is replaced by the same change for the same cases (CF-101). U-8: CF-91 says once that the rows before it name the area *Cases*, its addresses `#/cases/…` and its API `/api/v1/cases/…`, and the changelog's introduction says it too.

## Left for the prompt review

*Done* on November 10, 2026 (CF-106): P-4 to P-7, P-9 and S-8 as marked above; the AIOps skill rewritten in short sections (and corrected: it still said a run that no longer sees a problem verifies the fix, which W-1 ended); the re-check rule's wording made plain. `knowledge/sync.go`'s `syncPrompt` was read again and left as it is. The golden copies were changed by hand with their sources, since the review environment cannot run the Go tests; the pull request's CI is their check. What was left then:

- The wording of `ai/prompts/*.md` beyond the exact name corrections of S-2 and S-4, the sentence of P-1 and the generated list of S-1. The AIOps skill is about 1,600 words in ten long bullets and should be restructured then.
- P-4, P-5, P-6 (in prompts), P-7, P-9 and S-8.
- The wording around the new `rechecked` field: put in a plain first version (W-1 step 3); it will be reviewed with the first real runs.
- Anything in `knowledge/sync.go`'s `syncPrompt`.

## To check on a running console

*Answered* by the second review, on the simulated console: see [Seen on the running console](#seen-on-the-running-console). The list as the first review left it:

The review did not see the console run. With `make dev-sim` or the test install:

- The top bar at 1280 px with three areas, the bell and the tabs of the Kubernetes area. At 1440 px two tabs are already behind *More* (`docs/screenshots/admin-settings.png`); check that the tab in use is never the hidden one.
- A case's drawer with *Troubleshoot* open: the case on the left, Sherlock and Watson on the right.
- Select boxes do show their value in a monospace face (confirmed on the retaken screenshots; U-16).
- Every label changed in U-1 to U-6, in place, and that no truncation follows from the longer ones.
- An old address of each kind (`#/cases/inbox/case/1`, `#/cases/known/KI-1`, `#/cases/inbox?proposal=PC-1`) opens its new place after A-1.

## Open question: answered

*Decided* (the owner, November 10, 2026): **one clean run is enough.** Below P1, a run of the job that found the case, which re-checked its object (W-1) and found the problem gone, closes it as Fixed; at P0 and P1 it marks the fix verified and the case waits for a person's RCA. No case waits for a second run, and `docs/states.md` says so.

With the decision came CF-107: a case says what it waits for, one stage beside its status (*Waiting for analysis*, *Waiting for approval*, *Waiting for a validation run*, *Waiting for closure* and the rest; the table is in `docs/states.md`).

## Second review

A second review on November 10, 2026, after the fixes: of `9148164` (*Phase 12 review: fix all findings*), of what followed it (`97c07f7`, `1e9e2ba`, the status work of CF-107) and, for the first time, of the console running. Findings are numbered R2-1 and on. Those marked *fixed* or *decided* are on the branch with their tests (CF-107 to CF-110); the others are open.

### What ran

The first review could build nothing. This one built Go 1.26 from its source and fetched the modules from their repositories (checked against `go.sum`), so everything below ran here, on the branch's tip.

| Check | Result |
| --- | --- |
| `go build ./...`, `gofmt -l cmd internal`, `go vet ./...` | Clean. |
| `go test ./...` | Every package passes on the tip. On the way: `TestJobInstructionsAreEditable` (`jobs`) failed (R2-4), and two tests of `knowledge` failed: one only where `HTTPS_PROXY` is set, as it was on this machine, and one at random, which a look on November 11 showed (R2-13). |
| `go test -race ./...` (`make test-go`) | 48 of the 50 packages with tests pass. `api` and `nodessh` failed on data races, all three in the tests' own code (R2-17); with those fixed both pass. |
| `hack/check-prompt-sources.sh` | Passes, once it could be run (R2-3). |
| Browser app: `tsc`, `eslint`, `prettier --check`, `vitest` | Clean; 848 tests pass. |
| `make e2e-sim`, Chromium | 270 tests: 264 pass and 6 are skipped on the simulator. The first full run had my change to the dock in it (R2-8), which failed `inbox.spec.ts:257` and kept the two tests after it from running; with the change taken back the file passes. One test depends on what ran before it (R2-14). |
| `make screenshots` | Every shot is taken (28 files), here and in the Screenshots workflow. |

Not run: the chart's checks (`hack/check-chart.sh` needs Helm), `shellcheck`, the end-to-end tests on kind, Firefox and WebKit, and anything on a real cluster.

**CI has not run on this branch.** `ci.yml` runs on pull requests and on `main`; the last run is of November 4. Every result above is from this review's machine. Open a pull request for `phase-12.0-ai` (a draft is enough) so that CI runs the same checks, the chart's and kind's among them, before more is built on the branch.

### The fixes of the first review

Read again with their tests: W-1 to W-4 (verification on what a run re-checked inside its scope, a failed cluster left out, the cap by severity, the list of fixes to re-check) do what the hand-over asked, and `recheck_test.go` in `jobs` and `casework` covers each case `docs/states.md` lists. A-1 is complete: the old addresses lead to the new ones (seen on the running console, below), and the old ConfigMap is kept by the chart and readable by the console's Role until its records are copied. S-1 and S-2 are held by tests that fail when the console's names move. `sherlocktools` is `api/sherlock_tools.go` moved behind an interface, with no change in what the tools do. The defects below are in what the fixes did not reach; R2-4 is mine.

### Findings

| Severity | Count | Findings |
| --- | --- | --- |
| Medium | 7 | R2-1 to R2-6, R2-17 |
| Low | 10 | R2-7 to R2-16 |

#### R2-1. A re-check that names another cluster was taken at its word. *fixed* (0.12.0, CF-108)

**Where:** `jobs/recheck.go` `withCluster`, called from `runAudit` and `runAlert` (`jobs/kinds.go`).

**What was wrong:** A finding that names another cluster than the one its answer was asked about is left out, and one that names the cluster by its name gets its id (`merge`, ADR-147). A re-check got neither: `withCluster` only filled an empty cluster. In an audit over several clusters the scope is every cluster that answered, so an answer about cluster A that said `"cluster": "B", "still": false` for a fix waiting on B verified it, and below P1 closed its case, though the call never read B. The same entry with `still: true` reopened B's case. A re-check that named the cluster by its name ("simulated", as the task's first line gives it beside the id) matched no key and counted as not re-checked.

**Done:** a re-check is its answer's cluster's, by id, when it names none, the id or the name; one that names another cluster is left out and the run's note says how many.

#### R2-2. At P0 and P1 a verified fix stayed verified when the problem came back. *fixed* (0.12.0, CF-108)

**Where:** `casework/route.go` `by`.

**What was wrong:** At P0 and P1 a run that re-checks a fix marks it verified and the case waits, In progress, for a person's RCA. A later run that found the same problem again only counted it (seen ×N): the case went on saying *Verified: add the RCA to close*, and nothing re-checks a case that is no longer waiting. A person could close a P1 as Fixed, verified, on a problem the last run still saw.

**Done:** found again by a run of its job, such a case says the fix did not hold, with a note; `TestAVerifiedFixFoundAgainNoLongerReadsAsVerified` failed before the change.

#### R2-3. `hack/check-prompt-sources.sh` could not be run. *fixed*

**Where:** the file's mode in git (100644), since `50dd5d2`; `Makefile` `lint-go`, `ci.yml` *Prompt sources*.

**What was wrong:** Both call it as `hack/check-prompt-sources.sh`, which fails with *Permission denied*. `make lint` stopped there, and the CI step would have failed at the first pull request.

**Done:** the file is executable.

#### R2-4. A Go test failed since the prompt review. *fixed*

**Where:** `jobs/round2_test.go` `TestJobInstructionsAreEditable`.

**What was wrong:** My change of CF-106 gave the audit's task a new first line; this test read the old one. I had checked the string literals of the tests by script and missed a partial one, and could not run the tests. It failed from `1e9e2ba` until this review.

**Done:** the test reads the new line. All of CF-106's golden files were written by hand; `go test -update` now agrees with every one of them.

#### R2-5. A change proposed again discards an approval already given. *fixed* (0.12.0, CF-110, ADR-153)

*Decided* (the owner, November 10, 2026): keep the card that waits, with its approvals, and say in its history that it was proposed again. It is replaced only when the change is no longer the same (what it acts on changed since, other steps, another case). The second part of **Do**, telling the agent which cases have a change waiting, was not taken: a change proposed with other commands still waits beside the first.

**Where:** `cards/store.go` `propose` (CF-101, ADR-148).

**What is wrong:** A change with the same cluster, cases and commands as one that waits replaces it: the card that waited becomes *Replaced* and a new one starts with no approvals. On a production cluster a change needs two people. When the first has approved and the same change is proposed again, the approval is gone, and the case goes from *Waiting for a second approval* back to *Waiting for approval*. The nightly audit is not told that a change already waits for a case, so it may propose it again every night: the second approver has until the next run. Checked with a test: after one approval and the same proposal, the new card has 0 of 2 approvals.

Proposed with other commands, the change waits beside the first, one more card each night until the cap of waiting changes.

**Do:** when the same change waits already, keep that card: plan it again as *Check again* does, add "proposed again by <run>" to its history and move its expiry, and keep its approvals when the plan did not change. Replace only when it did. And tell the agent which cases have a change waiting (the keys in use can say "change PC-4 waits"), so it does not propose for them again.

**Done when:** a test approves once, proposes the same change again and finds one card with one approval; another finds the task names the waiting change.

#### R2-6. A case can wait for a run that will not come. *decided* (0.12.0, CF-109)

*Decided* (the owner, November 10, 2026): a person may close the case without validation. *Close without validation* on the Fixed form, and *Skip validation…* on a case already closed and waiting, end the wait; the stage's hint and Sherlock's skill name them. What follows under **Do** was not taken: a whole-cluster run does not re-check other jobs' fixes, and a case does not say that its job is off.

**Where:** `casework/route.go` (a fix is verified only by a run of the job that found the case), `jobs/kinds.go` `runAlert`, `casework/queue.go` `drain`; the texts of CF-107.

**What is wrong:** Since W-1 a fix is verified only by a run of the job that found the case, with the case's object in its scope. That is right for the nightly audit. For a case the **Alert diagnosis** found, the scope of a run is the object of the alert that started it, and once the fix works the alert no longer fires: no run re-checks it. An open case with an applied change stays *Waiting for a validation run* (and at P0 and P1 never reaches *Waiting for closure*); one a person closed as Fixed waits unseen. The same holds for any case whose job was turned off or no longer covers its cluster, and a case queued for the Case analysis job stays *Waiting for analysis* while that job is paused. The case says "the next run of <job> re-checks it" in each of these.

**Do:** (1) Let a run that read a cluster whole re-check every fix waiting on it, whichever job found it: list them in the audit's task and verify on its answer, the case naming the run that did. (2) Where no run can come (the job is off, paused or does not cover the cluster), say so on the case in place of the stage's hint: "Its job is off: nothing will re-check it. Close it once you have checked."

**Done when:** a test fixes a case the Alert diagnosis found and an audit of its cluster verifies it; the drawer of a case whose job is off says so.

#### R2-17. `make test-go` fails on data races in three tests. *fixed*

**Where:** `api/casetrouble_test.go:155`, `api/proposaltrouble_test.go:189` (the audit buffer of `multiSetupWith`), `nodessh/hosts_test.go:493` (`TestAFailedPairingSaysWhyAndLeaksNothing`). All three since `50dd5d2`.

**What was wrong:** `make test-go` runs with the race detector, which found the two API tests reading the audit log's plain `bytes.Buffer` while a request's goroutine still wrote to it, and the SSH test changing its fake host's settings while the host served. The console's own code is not involved. Whether they fail depends on timing: they did on this machine, so they can in CI.

**Done:** the shared setup of the API tests hands out a buffer with a lock (the `lockedBuffer` the package already had), and the fake host's settings change under its lock.

#### Low

| ID | Where | What is wrong | Do |
| --- | --- | --- | --- |
| R2-7 | `casework/route.go`, the queue for analyses | A case reopened by a re-check alone (still there, no finding) was not queued for its analysis unless the same run also had a finding on that cluster. | *fixed* (CF-108) |
| R2-8 | The case drawer with *Troubleshoot*, `styles/design.css` | With the dock open, a case's drawer keeps the dock under it: the drawer loses its lower third, and a second Sherlock and Watson show under the case's own. Only the dock's round button is hidden. | Put the dock away while a drawer shows its own Sherlock. *Tried and taken back:* hiding it under every split drawer broke `inbox.spec.ts:257` (IB-10 to IB-13), where a proposed change's drawer is open and the person asks in the dock. So hide it only where the drawer's own conversation can be asked in |
| R2-9 | The Inbox, Status column | 150 px wide: a status with its stage was cut with no ellipsis ("Waiting for analys"). Found on the first screenshot. | *fixed*: 250 px, an ellipsis, the whole text in the hint |
| R2-10 | `casework/route.go`, verification | A finding of a waiting fix's key that opens no case (below the job's lowest priority, marked expected, or covered by an exception) counts as found again, so the fix is neither verified nor said not to hold, and the run does not count it as not re-checked. The case waits with nothing said. | Count it with the fixes not re-checked, and say on the case why |
| R2-11 | `jobs/reroute.go`, `jobs/run.go` | A routing that succeeds while the run cannot be saved after it is done again on the next look: seen counts and notes twice. | Record on the cases which run was routed, or save the run first |
| R2-12 | `casework/route.go` `KeysInUse` | It returns an error when the exceptions cannot be read (W-7) but goes on without the cases or the known issues when their index cannot. | *fixed* (CF-111): an error for the cases and the known issues, as for the exceptions |
| R2-13 | `knowledge/store_test.go`, `knowledge/sourcetest_test.go` | Two tests, two causes. `TestMovingTheWikiCopiesEveryFile` fails at random (23 of 40 runs alone): saving the store starts a read of the wiki that nothing waits for, and the test ends while it writes into the test's folder. `TestATestSaysWhatIsWrongKN26` fails when `HTTPS_PROXY` is set: the code rightly checks nothing behind a proxy. | *fixed*: the read after a new store and the sync after a push are counted in the service's background work, which the tests wait for; the service has its proxy lookup beside `dial` |
| R2-14 | `web/e2e/watson.spec.ts:424` | *requests do not pile up* counts every request left waiting, so it fails when an earlier spec leaves one. It failed here when run right after `sherlock.spec.ts`; it passes alone and in the suite's own order. | *fixed*: the test discards what still waits before it starts |
| R2-15 | `CaseDrawer.tsx`, the first line and *Opened from* | The job's id shows where its name belongs: "found by Auditor (nightly-audit)", "nightly-audit" under the run. U-11 and U-12 put names where the cluster's and the agent's ids stood. | *fixed* (CF-112): the job's label, its id as the hint |
| R2-16 | AIOps › Jobs, Schedule | One row gives its times in two zones: *When* in the console's ("Every day at 02:00 UTC"), *Last run* in the browser's ("7:36pm PST"). | One zone on the row, the console's, or the zone named on both |
| R2-18 | `jobs/jobs_test.go:319` `TestBudgetStopsForTheDay` | Found on November 11 by the first `make test` of the hand-over, before any change: it failed once in the whole run with "the job's last run is still going" at the run of the next day, right after `w.s.Wait()`; run alone 40 times with the race detector it passed every time. A run seems to be still marked as going after `Wait` returns, under load. | Find what `Wait` does not wait for (the run's end being saved, or its *running* mark let go), and wait for it in `Wait` or in the test; not fixed with the eight, as the hand-over asked |

### Seen on the running console

The simulated console (`make dev-sim`'s setup), in Chrome, at 1440 and 1280 px. The list [To check on a running console](#to-check-on-a-running-console) is answered there.

- **The old addresses.** `#/cases/inbox/case/1`, `#/cases/known/KI-1`, `#/cases/inbox?proposal=PC-1`, `#/cases/jobs/runs`, `#/cases/mycroft/known` and `#/cases` each open their place under `#/aiops`.
- **The top bar at 1280 px.** The three areas, the bell and the Kubernetes area's tabs fit; the tabs that do not are behind *More*, and the tab in use is always shown (Storage, Policies, Helm, Access and Networks were tried).
- **A case's drawer with Troubleshoot.** Now a screenshot of the README (`aiops-troubleshoot.png`): the case on the left, Sherlock's answer with *Run in Watson* on the right. With the dock open it stays under the drawer (R2-8).
- **The Inbox at 1280 px.** Its table is wider than the page and scrolls inside its card, as before: the room for the Status column came from Title, Type, Incident, Object and Last change, so the table's least width is what it was (1,356 px).
- **The labels of U-1 to U-6 and U-10 to U-16.** In place on the pages seen (the Inbox, a case, Jobs, Runs, Validation rules, Known issues, Scripts, a script, Settings › Agents); none is cut.
- **The simulator's sample case C-6** reads *Waiting for analysis* while the Case analysis job is paused there, as every job is: R2-6 on the README's own screenshot.

The test install at `rke2cp1:8081` was still not reachable, so nothing here was seen on a real cluster.

### What a case says now (CF-107)

Asked for with this review: a case says what it waits for, beside its status. The stages and what ends each are in `docs/states.md`. With one validation run chosen there is no stage for a second run. Two things came with it: a case closed as Fixed that waits for its run says so in the Inbox (it showed nothing), and one a run reopened says *Fix did not hold* (it said so only in its history).

## Hand-over: what is open after the second review

Eight findings of the second review are open: R2-8 and R2-10 to R2-16, all Low. This section is for whoever makes the changes (a person or Claude Code) and says for each where it is, what is wrong, what to do and how to tell it is done. Everything else of both reviews is done or decided.

### How to work

1. Work on `phase-12.0-ai`, from its tip. Take the findings in the order below; each builds and tests on its own, so commit after each (`git commit -s`).
2. Follow `CONTRIBUTING.md` and steps 2, 5 and 6 of [How to work through this](#how-to-work-through-this): a requirement row for each change in behaviour (the next free id is **CF-111**), an ADR only for a decision (the next is **ADR-154**; none of these needs one except perhaps R2-10), a line under 0.12.0 in `CHANGELOG.md` for what a user would notice, tests with the change. A change to tests only (R2-13, R2-14) needs no row and no changelog line.
3. Before each commit: `make lint` and `make test`. Before the last: `make e2e-sim`. Run `make test` **first**, before changing anything; on November 11 the Go tests, the race detector and the browser tests all passed on the tip, apart from the flaky test of R2-13.
4. Do not open a pull request and do not start CI: the owner has not asked for it yet.
5. When a finding is done, write *fixed* and the commit in its row of the [Low table](#low-1) and under its heading here. If one turns out not to be a defect, say so there (*not a defect:* and why) and change nothing.

### Order

| # | Finding | Kind | Size |
| --- | --- | --- | --- |
| 1 | R2-13 | A test that fails at random, and one that fails behind a proxy | Small; do it first, it can fail any run of `make test` |
| 2 | R2-12 | Server, error handling | Small |
| 3 | R2-14 | Browser test | Small |
| 4 | R2-15 | Browser app, a label | Small |
| 5 | R2-16 | Browser app, a label | Small |
| 6 | R2-8 | Browser app, layout | Medium; an earlier attempt broke a test |
| 7 | R2-10 | Server, case flow | Medium |
| 8 | R2-11 | Server, case flow | Medium |

### R2-13. One knowledge test fails at random, another behind a proxy

*fixed*: `SaveStore`'s read of the wiki and `Hook`'s sync after a push are counted in `s.bg`; `newFixture` registers `t.Cleanup(f.s.Wait)` after `t.TempDir()` and sets a proxy lookup that returns none; `reach` takes `s.proxy` (`http.ProxyFromEnvironment` when nil), and `TestATestSaysWhatIsWrongKN26` has a case with a proxy, where nothing is dialed. The Confluence check started by hand (`http.go`) writes no file into the folder and is left as it was; the jobs' ticker in `jobs.go` runs until its context ends and is not background work to wait for.

**Where:** `server/internal/knowledge`: `storeapi.go` (the goroutine started after the store is saved, near line 184), `helpers_test.go` `newFixture`, `store_test.go` `TestMovingTheWikiCopiesEveryFile`; `sourcetest.go` `reach` (line 219), `sourcetest_test.go` `TestATestSaysWhatIsWrongKN26`.

**What is wrong:** Two separate things; the review's table first put both down to the proxy.

1. `TestMovingTheWikiCopiesEveryFile` fails at random with *TempDir RemoveAll cleanup: directory not empty*. Run alone 40 times it failed 23 times without a proxy and 5 times with one; in the whole package it usually passes, which is why the full runs did not show it. Saving the wiki's store starts a goroutine that reads the wiki again (`s.Refresh`) and writes its local copy into the service's folder, which in tests is `t.TempDir()`. The goroutine is not counted in `s.bg`, so nothing can wait for it, and the test ends while it still writes.
2. `TestATestSaysWhatIsWrongKN26` fails wherever `HTTPS_PROXY` is set. `reach` asks `http.ProxyFromEnvironment` and, behind a proxy, rightly checks nothing; the test expects the check. `ProxyFromEnvironment` reads the environment once for the whole process, so `t.Setenv` in the test is not a dependable fix.

**Do:**

1. Count that goroutine in `s.bg` (`s.bg.Add(1)` before it, `defer s.bg.Done()` in it), and have `newFixture` wait for the background work when a test ends: `t.Cleanup(f.s.Wait)`, registered after `t.TempDir()` is called so that it runs before the folder is removed. Tried on November 11: with both, 80 runs of 80 pass with and without a proxy, and the package passes. Look at the other goroutines of the package that write into the folder (`go func` in `storeapi.go`, `http.go`, `jobs.go`) and count those that a test can leave running.
2. Give the service the proxy lookup as a field beside `dial` (for example `proxy func(*http.Request) (*url.URL, error)`, `http.ProxyFromEnvironment` when nil), and have the fixture set one that returns no proxy. Add one case to the test: with a proxy, `reach` checks nothing and the test of the source goes on.

**Done when:** `go test ./internal/knowledge/ -run 'TestMovingTheWikiCopiesEveryFile$' -count=100` passes, and `HTTPS_PROXY=http://127.0.0.1:9 go test ./internal/knowledge/` passes.

### R2-12. The keys in use are given without the cases when the cases cannot be read

*fixed* (CF-111): `KeysInUse` returns "the cases could not be read: …" or "the known issues could not be read: …"; the caller's note was already there. Tests: `TestAFailedReadOfTheCasesGivesNoKeysR212` (casework, the issue store down) and `TestKeysThatCannotBeReadAreSaidOnTheRunR212` (jobs, the run's note). The two reads go through one load of the issue store, so a test cannot fail the known issues alone; the error for them is the same code path.

**Where:** `server/internal/casework/route.go` `KeysInUse` (line 658); its caller `server/internal/jobs/keys.go` (line 407); the test `TestAFailedReadOfTheExceptionsOpensNoCaseCF99` in `casework/review_test.go`.

**What is wrong:** `KeysInUse` returns an error when the exceptions cannot be read (W-7), but when the cases' index (`s.o.Issues.Index`) or the known issues (`s.o.Issues.Known`) cannot, it goes on without them: `if err == nil { … }`. The agent then gets a list with no open cases and no fixes to re-check, as if there were none, and the run says nothing. It names problems anew and re-checks nothing.

**Do:** Return the error in both places, worded like the one for the exceptions ("the cases could not be read: …", "the known issues could not be read: …"). The caller already handles an error: it notes on the run that the agent was not given the keys, and the agent works without them. Nothing else changes.

**Done when:** a test with a cases store that cannot be read gets an error from `KeysInUse`, and the run's note says the keys could not be read. Extend the test above or add one beside it.

### R2-14. A Watson browser test counts requests another test left

*fixed*: `discardWaiting` reads the requests still waiting on the local cluster from Watson's stream (`/api/v1/watson/events`, its first `items` event) and discards each (`POST …/watson/commands/{id}/discard`) before the test starts; `POST /api/v1/watson/clear` removes only finished commands, so it could not. The test then checks that nothing waits, and its assertions are as they were.

**Where:** `web/e2e/watson.spec.ts`, *requests do not pile up: the newest is the card, the earlier one folds into a line and still waits (CF-65)* (line 424).

**What is wrong:** It expects "1 earlier request left waiting" and "2 waiting", which holds only when nothing waits at its start. Run after `sherlock.spec.ts` in one session it found a request that spec had left, and failed with "2 earlier requests". It passes alone and in the suite's own order, so it fails only when the order changes or an earlier test leaves more behind.

**Do:** At the start of the test, discard every request that still waits, so the counts are its own: through the pane (open the *earlier requests* line and press *Discard* on each card) or through the API if one discards a waiting request (`POST /api/v1/watson/clear` is used in this file's setup; check whether it covers requests that wait). Do not weaken the assertions to "at least".

**Done when:** `npx playwright test sherlock.spec watson.spec` (the two files in that order, one session) passes, and so does `watson.spec` alone.

### R2-15. A case shows its job's id where the job's name belongs

*fixed* (CF-112): `useRunNames` gains `job(id)` from the jobs' list it already reads (kinds and jobs, a job's own label last), the id until the list comes; the drawer uses it on the line under the title and under *Opened from*, with the id as the element's `title`. Tests: `CaseDrawer.test.tsx` (the label in both places and no `nightly-audit` in the drawer's text; the id when the jobs cannot be read), `runNames.test.tsx` (`job`).

**Where:** `web/src/components/aiops/CaseDrawer.tsx`: the first line under the title (lines 414 and 415, "found by Auditor (nightly-audit)"), the job under the source (line 588), and *Opened from* (near line 849). `web/src/components/aiops/runNames.ts` already gives names for agents and clusters.

**What is wrong:** The drawer shows `c.source.job`, the job's id (`nightly-audit`, `alert-diagnosis`), where every other place shows its name (*Nightly audit*). U-11 and U-12 put names where the cluster's and the agent's ids stood; the job was left.

**Do:** Add `job(id)` to `useRunNames` (the jobs' list it already reads has each job's `id` and `label`), falling back to the id, and use it in those three places. Keep the id as the element's `title`, as U-11 did for the cluster. The server could send the label with the case instead (`verifyJob` already carries `{id, label}`); either is fine, pick the smaller change.

**Done when:** a drawer test finds "found by Auditor (Nightly audit)" and no "nightly-audit" in the drawer's text, and `runNames.test.tsx` covers `job`.

### R2-16. A job's row gives its times in two time zones

**Where:** `web/src/components/aiops/JobsTab.tsx`: `WhenCell` (line 62) and the *Last run* and *Next run* cells (lines 81 to 88, 184); `describeJobSchedule` in `web/src/api/jobs.ts`.

**What is wrong:** *When* is in the console's zone ("Every day at 02:00", "UTC" under it), because that is how the schedule is set. *Last run* and *Next run* are in the browser's ("2026-11-09 7:36pm PST"), as every time in the console is. On one row, a reader has to convert to see that 02:00 UTC is the run of 6:00 pm.

**Do** (the owner's choice, November 11, 2026): keep both as they are and add the browser's time to *When* where the two zones differ: under the schedule, beside the zone, "UTC · 6:00pm PST". Where they are the same, show nothing more. *Last run* and *Next run* stay in the browser's zone, as every other time in the console.

**Done when:** a test of `WhenCell` with a console zone other than the browser's finds both times, and one with the same zone finds one.

### R2-8. With the dock open, a case's drawer loses its lower third

**Where:** `web/src/styles/design.css`, the rules after "Over a split drawer the dock's round buttons are hidden" (`.rk:has([data-split='true']) .rk-fab`); `web/src/components/Drawer.tsx` (`data-split`); `web/src/components/aiops/CaseTroubleshoot.tsx` and `ProposalTroubleshoot.tsx` (the drawer's own Sherlock); `web/e2e/inbox.spec.ts:257` (*Sherlock is in the dock over Cases and is told the page and the item open*, IB-10 to IB-13).

**What is wrong:** A case's drawer with *Troubleshoot* has its own Sherlock and Watson on the right. When the dock at the bottom is open, it stays open under the drawer: the drawer ends above it and loses about a third of its height, and a second Sherlock and Watson show under the case's own. Only the dock's round button is hidden over a split drawer.

**What was tried:** Hiding the open dock under every split drawer (`.rk:has([data-split='true']) .rk-dock:not([data-mode='max']) { display: none }` and `--dock-space: 0px`). It fixed the case's drawer and broke `inbox.spec.ts:257`: there a proposed change's drawer is open, its own conversation cannot be asked in, and the person asks in the dock, which is what IB-10 to IB-13 require. The change was taken back.

**Do:** Put the dock away only while a drawer shows a Sherlock of its own that the person can ask in. Mark that on the drawer: for example `data-own-chat="true"` on the element that has `data-split`, set by `CaseTroubleshoot` and `ProposalTroubleshoot` when their pane takes questions (not when it is read-only or absent). Then the two rules above with `[data-split='true'][data-own-chat='true']`. The dock comes back as it was when the drawer closes; a maximized dock is left alone.

**Done when:** an end-to-end test opens the dock, opens an agent-found case and finds the dock gone and the drawer as tall as the page allows, then closes the drawer and finds the dock back; `inbox.spec.ts`, `troubleshoot.spec.ts`, `proposal-trouble.spec.ts`, `sherlock-ui` and `watson.spec.ts` pass. Take the README shot `aiops-troubleshoot` again (the Screenshots workflow does on a push that touches `web/screenshots/`).

### R2-10. A finding that opens no case leaves a waiting fix neither verified nor counted

**Where:** `server/internal/casework/route.go` `Route`: `seenKeys` (lines 111 to 115) and the verification loop (`if seenKeys[k] || leftOut[k] { continue }`, line 217); the routings `RoutedExcepted`, `RoutedExpected`, `RoutedIgnored` (`jobs/jobs.go`); `verifyWait` and `VerifyWait` in `casework/service.go`; `waitText` in `web/src/api/casework.ts`.

**What is wrong:** `seenKeys` holds the key of every finding of the run. The loop that verifies waiting fixes skips a case whose key is in it, on the ground that the finding was "routed above (reopened, or the fix did not hold)". That is true only of a finding that reached the case. One that an exception covers, that the agent marked expected, or that is below the job's lowest priority opens and joins nothing, so the case is neither reopened nor verified, and the run does not count it among the fixes it did not re-check. The case waits with nothing said, run after run, for as long as the finding is reported that way.

**Do:** Keep the skip for the findings that did reach a case. For the other three routings, count the case as not re-checked by this run (`Unchecked++`, and the run's count) and say why on the case: a short text kept with the wait ("run-12 found it again and opened no case: below the job's lowest priority" / "…: the agent marked it expected" / "…: exception E-3 covers it"), given in `verifyWait` and shown where the wait is shown (`waitText`). Clear it when the wait ends. Do not reopen the case and do not say the fix did not hold: whether such a finding means that is not decided, and the person now sees why it waits and can use *Skip validation* (CF-109) or reopen it.

**Done when:** three tests, one per routing, close an agent-found case as Fixed, route a finding of its key that way, and find the case still closed and waiting, its `Unchecked` one higher, the reason on its wait, and the run's count of fixes not re-checked one higher; a drawer test shows the reason.

### R2-11. A routing done twice counts twice

**Where:** `server/internal/jobs/reroute.go` (the loop that routes a run again, lines 70 to 100) and `jobs/run.go` (line 383, the first routing); `server/internal/casework/route.go` `by` (the seen count, line 396) and the known issues' and exceptions' counts (`StillPresent.Count++`, `hits[e.ID]++`); `issues.Seen` and `issues.StillPresent` in `issues/issues.go`.

**What is wrong:** A run whose findings could not be routed is routed again on a later look (W-8). When that routing succeeds and the run then cannot be saved (`updateRuns` fails), the run still says *to route*, and the next look routes it once more. `Route` is not safe to repeat: every finding that joins a case adds one to its *seen ×N*, a known issue's *still present* count and an exception's hits go up again, and a case the first routing opened is counted as seen by the same run. It needs two failures in a row, so it is rare, and nothing is lost; the numbers are wrong.

**Do:** Make a second routing of the same run change no count. Remember the run that counted last where a count is kept (`Seen.Run`, `StillPresent.Run`; a case already has `Run`, the run that opened it) and, in `by` and the known-issue branch, skip the count when it is `in.Run`. Count an exception's hit only for a finding whose routing was not already counted for this run, or leave the hits to the first routing. The findings of the result must still name their cases, so that the run can be saved with them.

**Done when:** a test routes one `RouteInput` twice and finds the same cases, seen counts, known issue's count, exception hits and notes after the second as after the first, and the same `RouteResult.Findings`.

### Found while fixing them

- **R2-18.** `TestBudgetStopsForTheDay` (`jobs`) failed once in the first `make test` of November 11, before any change, and passed 40 runs of 40 alone. See its row in the [Low table](#low-1). Left as it is: it is not one of the eight.

