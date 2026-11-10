# Issue handling: requirements

Nov 5, 2026 · Mikhail Stolpner · **Draft 1, built on branch `phase-12.0-ai` (0.12.0): the core, the AI investigation and publishing known issues to Confluence. Decided in ADR-83 to ADR-87; the investigation in ADR-93, the publisher in ADR-97.**

People who use what the clusters run, and are not Kubernetes people, report problems in their own words: "checkout hangs after I press Pay". 221B's issue service takes the report, matches it against the problems the operators already know about, has an investigator look into the rest, and keeps the reporter told until it is resolved. It is built as a component of its own, with its own records, storage and versioned API, so that it can later ship apart from the console; 221B is its first integration. The requirement IDs start with IS.

**Since Phase 12's wave 5, a report is a case** (requirements, *Phase 12: Cases replace reports*, CU-01 to CU-14; ADR-113 to ADR-115), **and since wave 6 every problem is one case, with one flow** (*Phase 12: one flow for findings and cases*, CF-01 to CF-24). Every case is a record of this service, whatever opened it: a person's report (source `report`), a case opened in the console by hand or from an alert (`manual`, `alert`), or a finding of a job's run (`finding`, *agent-found*: each finding becomes a case or joins one, by its key, cluster + object + check). The console shows each as `C-<n>`, the number below; the API keeps its paths and keeps calling it a report. A case is `new` while nobody has taken it, `inProgress` once someone has (an owner, an analysis, a fix being applied), and `closed` with exactly one outcome: `fix` (what was done; an RCA, required at P0 and P1, kept by the console), `duplicate` (the same problem as an open case, `duplicateOf`, the end of its duplicate chain), `noAction` (no action needed: not a problem, or it went away; any case; since CF-90 it replaces `notAProblem` and `noFixNeeded`, which are still taken as it), `knownIssue` (`knownIssue` names it) or `exception` (agent-found only, accepted in the console). Any closed case may be reopened with a reason. Priorities are 0 to 4: P0 outage, P1 blocking, P2 service degradation (the default), P3 preemptive, P4 question; the SLA targets are keyed `"0"` to `"4"` (Settings › SLA). A case a person opened has an SLA clock at every priority; an agent-found one only at P0 and P1, from the moment it is raised there (`slaFrom`; none: no SLA). A record gains `title`, `source`, `cluster`, `object` (what it is about, in any words), `openedBy`, `closedBy`, `explanation` (the close's), and for an agent-found case `key`, `job`, `run`, `finding`, `severity`, `seen` (how many runs found it while open) and `verifyPending`/`verified` (a fix the next run of its job checks); for operators, `appliedBy` (the console's proposed change, a Watson card, whose applying fixed it: `card`, `title`, `by`, `at`) and `verification` (`waiting`, `verified` or `notHeld`: how the next run of the job that found it saw it since; wave 6b, CF-29 to CF-31), never shown to a reporter; links `regressionOf`, `sameAs`, `possibleDuplicateOf` and `previous` (how the case it repeats was closed). Since wave 6c (*the owner's feedback on Cases*, CF-32 to CF-41) a record also has a `type` (`security`, `outage`, `performance`, `availability`, `configuration`, `capacity`, `compliance`, `question` or `other`, the default; filter `?type=`), for operators an `incident` (another system's number, 1 to 64 printable characters, matched by `q`, never shown to a reporter), and who opened and last changed it, `createdBy` and `lastBy` (`{name, kind}`, kind `person`, `agent` or `system`) with `lastAt`; a submission takes `incident` and `type` from operators and tokens only (403 for a reporter), and `PATCH reports/{n}` changes both (`incident: ""` clears it). Each case is a ConfigMap of its own, `<release>-case-<n>`, beside the service's index of them (IS-17). What 221B adds to a case (its analysis, suggested fix, proposed changes, RCA's text, history, drafts) is package `casework`'s, kept in the same ConfigMap. With `issues.enabled: false` the report page and this API are off, and the console keeps the cases here all the same.

## Scope

In this document, and built:

- **Reports**: plain text, scrubbed, numbered, with a reporter (or a system account), support filing for someone, statuses and outcomes, priority and its targets, an owner, a guessed service, a known issue, duplicates, watchers, a conversation with drafts no one sends but an operator, summaries an investigator fills, the service-level times, and the timers that reopen and close.
- **Known issues** and **services**, written by operators, with the matching that links reports to them.
- **The Reporter role**, a fifth role outside Kubernetes, and a **Report** permission for the four.
- **The API** under `/api/issues/v1/` ([issues-api.yaml](issues-api.yaml)), with **API tokens** for systems that file reports.
- **The report page** at `/report`, a page of its own outside the console's tabs.
- **Settings**, **notifications** (on the page and a webhook), the **audit** records and the **backup** functions.

Elsewhere, and built too: the operators' screens inside 221B (AIOps › Inbox, Reports, Known issues) and the links from the console's pages into `/report` are in the requirements' *Phase 12: Cases* (CS-01 to CS-22); the AI investigation, where the Troubleshooter is the `Investigator` (`server/internal/investigate`, run as the report triage job), in *Phase 12: Jobs* (JB-19); publishing known issues to Confluence in *Phase 12: Knowledge* (KN-13 to KN-18). The API those screens use is here.

## Approach

| Decision | Choice | Why |
| --- | --- | --- |
| Where it lives | `server/internal/issues`: records, matching, storage, timers and its own HTTP handler; `internal/api/issues.go` mounts it and says who is calling | A boundary that can become a product of its own: the package knows nothing of sessions, roles or clusters (ADR-83). |
| Where the records are | A ConfigMap of its own, `<release>-issues`, three keys | The console's other records are kept the same way (ADR-61, ADR-71, ADR-73); no database to run (ADR-84). |
| How a report is matched | Words weighed by rarity (BM25's idf), trigram likeness for misspellings, addresses the services run on | Explainable, cheap, with no model and no dependency; the AI investigates what words cannot (ADR-85). |
| Who reports | Anyone holding a role listed in `issues.reportRoles`, and the Reporter role from `issues.reporterGroups` | Keeps the four roles of ADR-59 fixed; the fifth grants nothing in Kubernetes (ADR-86). |
| How a system reports | API tokens tied to an account, with scopes, hashed | Monitoring and help desks file reports without a person's session (ADR-87). |

## Records

### Report

| ID | Requirement | Done when |
| --- | --- | --- |
| IS-01 | A report is plain text of at most 4,000 characters. Before it is kept, anything that looks like a credential is taken out (`internal/scrub`, as for the audit log), and the reporter is told: "We removed something that looked like a password or a token". An empty report is refused. | A report with a password and a GitHub token is kept with `[REDACTED]` in their place and says so; the page shows the note. |
| IS-02 | Reports are numbered #1, #2… in order, one numbering with every case (the console shows C-1, C-2…). A number is never given twice, also after a report is archived and when two replicas save at once. | Six reports through two services on one store are #1 to #6. |
| IS-03 | A report names its reporter (the person's user name, or `system:<account>` for a token) and, when support files it for someone (`for`), the person whose problem it is as reporter and support as `filedBy`. Only operators, and tokens with `reports:onBehalf`, may file for someone. | An operator files for Bob: Bob sees it as his, the operator as filed by them; a reporter cannot. |
| IS-04 | Statuses: New, In progress, Closed (wave 6, CF-01). A closed case has exactly one outcome (fix, duplicate, noAction, knownIssue, exception) and its explanation, always required; since CF-90 noAction replaces notAProblem and noFixNeeded, which this API still takes as noAction, and a case closed with one before is answered as noAction. Through this API an operator takes a case in progress, reopens a closed one with a reason (status `new`), closes one as fix (below P1; a P0-P1 fix needs its RCA, in the console) or noAction with `explanation`, as a duplicate with `duplicateOf` (refused, 409, when the original at the end of its chain is closed), or as a known issue with `knownIssue`; an exception is accepted in the console only. An agent-found case never closes as a duplicate unless it is marked a possible duplicate. | Closing without an outcome or an explanation is refused; a reporter cannot set a status. |
| IS-05 | Priority 0 to 4, P2 by default (wave 6, CF-02): P0 outage, P1 blocking, P2 service degradation, P3 preemptive, P4 question. Each has a first-human-response target and a resolution target, keyed `"0"` to `"4"`: 15 m and 4 h, 1 h and 1 d, 4 h and 5 d, 1 d and none, 2 d and none, changed in Settings › SLA (CU-06). A case a person opened has its clock from when it was opened; an agent-found one only at P0-P1, from when it was raised there, and lowering it stops it. Operators see each case's due times and whether it is late; one without a clock carries no `sla`. | The operator's view carries `sla` with the due times, or none. |
| IS-06 | The service-level times: acknowledged (the first handling by anyone, a link included), first human response (the first reply a reporter sees), resolved (fixed or closed). | Sending a reply sets the first response once. |
| IS-07 | A report has an owner (an operator) and a service, guessed from its text with a confidence, or "service unknown" below 50. An operator's choice wins, and an investigator's guess replaces only a guess. | A report naming `shop.example.com` gets the Shop service. |

### Known issues and services

| ID | Requirement | Done when |
| --- | --- | --- |
| IS-08 | A known issue (KI-n) has a title, symptoms in users' words, affected services, a status (Investigating, Identified, Fix in progress, Resolved), a workaround, an expected fix, an owner, a dated update log, a kind (normal, or planned maintenance with its window), a source (operator, alert, planned change, or a proposal by an agent, which is a draft until an operator accepts it) and a link to its full page. The cases closed as it are worked out from the index (`linked`, `cases`). Since wave 6 it also holds the keys agents found that its cases were about (`keys`) and `stillPresent` (how many times runs found them since, and when last). Resolving it reopens none of its cases: each is told in its conversation; and the next run of each job that found its keys checks it: still seen, it reopens (investigating, an update saying so) and is `pending` for its owner until they change it. | Resolving KI-1 adds its last update to its cases' conversations and leaves them closed. |
| IS-09 | A service has a name, a description, what it runs on (clusters, namespaces, workloads, hosts, fleets, URLs, hostnames), contacts (user names) and aliases. | — |

### Matching, duplicates and priority

| ID | Requirement | Done when |
| --- | --- | --- |
| IS-10 | A report is matched against the open, accepted known issues and the services: words weighed by rarity, misspellings by trigrams, and the addresses the services run on, to a confidence of 0 to 100 (ADR-85). At or above the threshold (80 by default, in the settings) the report is linked and the reporter is told the known issue's status, workaround, expected fix and last update as the operators wrote them. Operators preview what a text matches (`POST match`). | The mockup's report links to "Checkout times out in the EU store" at 100; "checkout is slow" is a candidate at 30; an unrelated report matches nothing. |
| IS-11 | "No, mine is something else" unlinks the report, keeps the known issue from matching it again, and sends it to investigation. | The report is Investigating and the investigator is called. |
| IS-12 | Open reports on the same service sent within the last hours (6 by default) whose titles read alike are noted as similar, for the operators; marking one a duplicate closes it as such, its reporter following the original from then on. | Two basket reports on the Shop within an hour are similar; one seven hours later is not. |
| IS-13 | A report matched to a known issue at the threshold is closed as that known issue at once (wave 6): the known issue follows the problem; "No, mine is something else" opens it again. The priority bump of before (`bumpP2At`, `bumpP1At`) has nothing to raise since and is kept in the settings unused. | — |

### Conversation and timers

| ID | Requirement | Done when |
| --- | --- | --- |
| IS-14 | The conversation holds messages from the reporter, from operators and from the system. A reply drafted by an agent or an operator is a draft until an operator sends it, as it is or edited; nothing sends a draft by itself. Operators may write internal notes. A reporter never sees drafts or internal notes. | The investigator's reply stays a draft until `send`. |
| IS-15 | A reporter answers a case closed as fix with "It works" (noted) or "Still broken" (reopened). A closed report reopens within 7 days; later, a new report is asked for. A question unanswered for 5 days closes the case as noAction (before CF-90, noFixNeeded); an answer stops that clock (`fixedDays` has no timer since wave 6). The timers run on the leader every 5 minutes. | With a fake clock, the timer closes at its time and not a minute before. |

### Investigation, storage, events and backup

| ID | Requirement | Done when |
| --- | --- | --- |
| IS-16 | An `Investigator` interface (`Investigate`, `Verify`) looks into reports no known issue explains, in the background, a few at a time: it gives a service guess, a cause, evidence, a technical and a plain summary, a drafted reply and possibly a proposed known issue. Without one, nothing is found; 221B plugs in the Troubleshooter (`server/internal/investigate`, the report triage job, JB-19), which finds nothing while that job is paused or the Troubleshooter is off. What it writes is scrubbed. | A fake investigator's finding is kept, its reply as a draft. |
| IS-17 | Each case is a ConfigMap of its own the console makes, `<release>-case-<n>` (labels `app.kubernetes.io/instance=<release>`, `221b.io/case=<n>`; the service's record under `case.json`, the console's work under `work.json`, at most 640 KiB of record). The ConfigMap `<release>-issues` holds the index, one small line per case (640 KiB), the known issues (192 KiB), and services, settings and tokens (96 KiB), within the console's 960 KiB per ConfigMap. A change writes the index and only the case objects it changed, each conditional on its version. One report carries at most 4,000 characters of text and 200 messages. A closed case a person opened loses its text, conversation and summaries after the retention (1 year by default), its number, dates, service and outcome kept; when the index runs out of room those go first, line and object, counted by outcome; when that is not enough, a save is refused saying what to do. Closed agent-found cases are deleted after Settings › Cases' `keepAgentClosedDays`. | With a small limit, pruned cases are archived and counted, then a save is refused with "shorten the retention". |
| IS-18 | Every change raises an event (`OnChange`) for what subscribes: the webhook, cases, notifications, and the Confluence publisher (KN-13 to KN-18 in requirements.md), which writes the known issues' page tree and imports a page written by hand under it as a draft known issue with the source `confluence`. | — |
| IS-19 | `Export` writes the known issues, services, settings and the cases' numbering (`221b-issues/2`), never the tokens; `ExportCases` every case object whole, which the console's cases' part of a backup carries; `Import` and `ImportCases` add what is missing, keep what is there, index the cases restored and number on above both. The backup's registry (another package) calls them. | A restore into a console with a case keeps it and the next case is numbered after both. |

### Roles, visibility and the API

| ID | Requirement | Done when |
| --- | --- | --- |
| IS-20 | A **Report** permission: the roles in `issues.reportRoles` (all four by default) may report. A fifth, built-in **Reporter** role holds only Report and following its own reports. It is given by `issues.reporterGroups` (LDAP or OIDC groups, or local groups) and by local groups of the role `reporter` (no namespaces). It is not a Kubernetes role: no ClusterRole, no binding, no group sent to a cluster. A person who holds only it signs in like anyone else and reaches the report page and nothing else: the console's pages send them to `/report`, its API refuses them. | A local reporter opening `/` lands on `/report`; `/api/v1/snapshot` answers 403. |
| IS-21 | A reporter sees their own reports, the ones they filed and the ones they follow; a service's contacts see every report on it; operators (`issues.operatorRoles`: SuperAdmin and Admin by default) see and triage everything. Anyone else is told the report does not exist. | Per role, the lists match exactly. |
| IS-22 | What a non-operator sees of a report never carries the technical summary, the cause, the evidence, the match's workings, drafts, internal notes or anyone's email; the plain summary is scrubbed again on the way out. | The API's reporter view has none of these fields. |
| IS-23 | A versioned REST API under `/api/issues/v1/` ([issues-api.yaml](issues-api.yaml)): for reporters, create, list, get, add a message, confirm, reopen, unlink, follow; for operators, list everything with filters, triage, drafts and send, status, known issues with their log, services, match preview; for SuperAdmins, settings and tokens. It is the leader's, like the rest of the API (ADR-70). | — |
| IS-24 | Systems file reports with API tokens: a SuperAdmin makes one for an account with scopes (`reports:create`, `reports:read`, `reports:onBehalf`), sees it once, and revokes it; only its hash is kept. The intake (`/api/issues/v1/intake/…`) takes a token and nothing else, and the rest of the API takes no token. | A revoked or made-up token is refused alike; a token reads only what its account filed. |
| IS-25 | A reporter may send so many reports an hour (10 by default); operators are not held to it. | The fourth report of three allowed is refused with a 429 that says why. |
| IS-26 | Every report, status change (`issue.status`; a close `issue.close`, a reopening `issue.reopen`, wave 6), message sent, triage, known issue, service, settings change and token made or revoked writes one audit record (`issue.*`), with no report text and no token. | — |

### The report page, settings and notifications

| ID | Requirement | Done when |
| --- | --- | --- |
| IS-27 | `/report` is a page of its own (a second entry of the browser app, `report.html`), outside the console's navigation, as the mockup `Report.dc.html` on the console's tokens: a header ("Report a problem", the company's name from the settings, "Signed in as …"), one "What's wrong?" box with Send and the note that pasted passwords are removed; then the reply card, a known issue's ("This looks like a problem we already know about", its status, what you can do, expected fix, last update, "No, mine is something else", "Read the full page" when it has a link) or "Received as #N"; "Your reports" with status pills, "It works" and "Still broken" on fixed ones, why a closed one was closed, and its conversation to answer in; and "Known problems right now" with Follow, and "All known problems, on Confluence" when the settings name the page. `/report/mine` shows the reports alone. Signing in is the console's: a person not signed in gets the sign-in page and comes back to `/report`. | The vitest tests of the page; 390 px stacks the side panel under the reports. |
| IS-28 | `?text=` fills the box and never sends it; `?for=<user>` files for someone else when the person is an operator, and is ignored otherwise. | — |
| IS-29 | The settings, kept with the records and changed by a SuperAdmin (`GET` and `PUT settings`): company name, status page link, report page address (default `/report`, which other pages of 221B will link to), match threshold, targets per priority, retention, rate limit, the timers, the duplicate window, the priority bumps and the webhook. | — |
| IS-30 | The report page asks for what changed every 30 seconds (`GET updates?since=`) and says it at the top: a status change, a reply, an update of a known issue the person's reports are linked to or they follow. An optional webhook gets generic JSON with a Slack-compatible `text`, carrying summaries only: never a report's text, conversation or technical summary. | The webhook test finds no report text in the body. |
| IS-31 | Tests: Go unit tests of scrubbing, numbering, matching and confidence, duplicates, priority bumps, the state machine and its timers with a fake clock, visibility per role, tokens and scopes, the rate limit and the storage limits; API tests of the Reporter's confinement and the chart's role lists; vitest tests of the page's parts. | `go test ./...`, `tsc`, `eslint` and `vitest` pass. |

## Chart values

```yaml
issues:
  enabled: true
  reportRoles: [superAdmin, superViewer, admin, viewer]
  operatorRoles: [superAdmin, admin]
  reporterGroups: []        # LDAP, OIDC or local groups whose members hold Reporter

auth:
  local:
    groups:
      - { name: staff, role: reporter }   # no namespaces
```

## Not in this document

- The operators' screens in 221B (Inbox, Reports, Known issues) and the console's links to `/report`: the requirements' *Phase 12: Cases*.
- How the AI investigation runs (the report triage job, JB-19) and the Confluence publisher (KN-13 to KN-18): the requirements' *Phase 12: Jobs* and *Phase 12: Knowledge*.
- Email notifications.
- Reports with attachments or screenshots.

## Open questions

| Question | Proposal | Status |
| --- | --- | --- |
| Admin is bound to namespaces, but as an operator it sees every report. Right? | Yes for now: reports are about services, not namespaces; `issues.operatorRoles` can drop Admin. | Open |
| Do service contacts see the technical summary? | No: they see the reporter's view; operators see the rest. | Open |
| Should a known issue's update be a message on each linked report? | No: the report page shows the known issue as it stands, and notices carry the update; resolving one writes a message. | Open |
| Do Reporters from LDAP appear on the Users page? | Only once they have signed in, since the sync reads the groups of the four roles. | Open |
