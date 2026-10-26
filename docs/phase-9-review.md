# Phase 9 code review

A review of the Toolbox as merged to `main` in pull request #20 (`phase-9-tools`, 193 files, about 14,800 lines added), read on the `phase-9.1-review` branch on October 26, 2026. Unlike the Phase 1 to 6 guides, which said what was built and what to check by hand, this is the review itself: what was found, how serious it is, where it is, and what to do about it. The fixes belong on `phase-9.1-review`, each with the test that fails without it (CONTRIBUTING.md).

The requirements are in [toolbox.md](toolbox.md); its *As built* table already lists the known gaps, and this review does not repeat them except where a gap matters more than the table suggests.

## Summary

The design holds: every session runs as the person, impersonated, so Kubernetes decides; the port-forward tunnel keeps credentials off the laptop; nothing a model writes runs unseen; the Tools menu's changes are an action and a name, never text; the chart values are a ceiling the server checks again. The code is clean, well commented and well tested for the paths it covers, and `go vet`, the race detector, `tsc`, `eslint`, the 200 browser unit tests and `helm lint` are all clean.

Three things need fixing before 9.1 is merged, in this order:

1. **Any signed-in person can approve a production session, which then runs as the requester.** The second person was meant to be a SuperAdmin (TB-07, TB-37, ADR-66); the code only requires one for `nodeShell.approval: always`. A Viewer can approve a SuperAdmin's node shell on production. (H-1)
2. **The full Admin can now create Jobs, and the debug pod mounts whatever ServiceAccount it is given.** That is "create pods" in disguise: an Admin bound to a namespace can run a shell as any ServiceAccount there, including ones bound to cluster-wide roles. The design asked for it (TB-50), so the decision is yours, but it needs a guard or a documented warning. (H-2)
3. **A terminal that falls behind is told the session ended, and it has not.** Output faster than the browser drains it drops the viewer, and the handler then sends `ended` for a live session; nothing reconnects. (M-1)

Everything else is medium or low: bookkeeping, missing tests, requirement rows that drifted from what was built, and text that is now stale.

## How the review was done

- Read every file of the pull request in `server/internal/sessions`, `server/internal/api/toolbox.go`, `server/internal/tunnel`, `server/internal/toolbox`, `server/cmd/221b-cli`, the Watson, roles, settings, fleets, views and live changes, the chart, `hack/`, and `web/src/components/toolbox`, `web/src/api/toolbox.ts`, the Dock, App and `uiControl` changes; skimmed the rest of the `ui-batch` merge (fleets by name, alerts by node, Namespaces tabs, home tiles).
- Ran: `gofmt -l` (clean), `go vet ./...` (clean), `go test -race ./...` (25 packages, all pass, no race), `npm run typecheck`, `npm run lint`, `vitest` (33 files, 200 tests pass), `helm lint --strict` and `helm template` for the defaults and the toolbox variants (`toolbox.enabled=false`, `transcripts=pvc`, `rbac.roles.admin.exec=true`, `superAdmin.template=readOnly`, the three refusals in `_roles.tpl`), all as expected.
- Confirmed the findings marked *probed* with throwaway Go tests against the package's own fake backend; they are not committed.
- Not run: the kind tests, anything against a real cluster, `kubeconform`, the browser end-to-end tests, Playwright against the simulator. The *As built* table says the Toolbox has not been on kind or a real cluster yet; the section *Before the first run on RKE2* below is for that.

Severity: **High** is a security or correctness problem a person will hit; **Medium** is wrong behaviour with a workaround, or a security hardening gap; **Low** is bookkeeping, text or a test gap.

## High

### H-1. Any signed-in person approves a session card on production

`server/internal/watson/service.go:442-473` (`refusalFor`), `:691-705` (`Visible`), `:708+` (`Approve`); `server/internal/api/toolbox.go:364` (`SubmitSession`).

`refusalFor` demands a second person on a two-person cluster, and a *SuperAdmin* only when `needsTwo` is set, which is the `toolbox.nodeShell.approval: always` case. With the default `approval: cluster`, a shell, a port forward, a debug container, a debug pod or a node shell requested on a production connection is approved by any signed-in person other than the requester (`Visible` shows every pending change card to everyone signed in, as P6-07 did for commands), and `startSession` then opens it as the requester with the requester's rights. For a Watson command that was safe, because the change ran as the approver and Kubernetes bounded it. For a session the approver's rights never enter: a Viewer can approve a SuperAdmin's root shell on a production node.

The project's own test shows it: `TestASessionOnProductionWaitsForASecondPerson` in `session_test.go` has `bob()`, who is not an admin, approve Alice's production shell, and expects it to open.

TB-07 ("the footer names the possible approvers: the SuperAdmins other than the requester"), the Approach table ("a second SuperAdmin where the two-person rule applies") and ADR-66 ("an approver approves a purpose and a duration") all mean the approver to be a SuperAdmin.

**Fix:** in `refusalFor`, when `approver != nil` and the item is a session card (`it.Session != nil`), require `approver.Admin` on every two-person path, not only under `needsTwo`; `Visible` should show a pending session card to admins only (commands stay as they are). Change the test so Bob is refused until `Admin: true`, and add one for a node shell with `approval: cluster` on production. The dialog's callout (`ToolDialog.tsx:522-529`) and the Watson card should say "another SuperAdmin".

### H-2. Admin's new `create jobs` is `create pods` with any ServiceAccount of the namespace

`server/internal/roles/roles.go:280-287` (`adminTools`), `deploy/helm/221b/files/roles.json`; `server/internal/sessions/spec.go:474-496` (`DebugJob`, no `automountServiceAccountToken: false`, `ServiceAccountName: r.ServiceAccount`); `web/src/components/toolbox/ToolDialog.tsx:389-395` (a free-text ServiceAccount field).

Until this phase the full Admin could create nothing but Secrets (its test said so: `create` on anything else was an error). It now creates and deletes Jobs in its namespaces (TB-50). A Job is a pod, and the debug pod dialog takes any ServiceAccount name and mounts its token, so an Admin bound to namespace `web` can start `221b-debug-pod-…` as `web`'s most privileged ServiceAccount (a controller's, an operator's, anything a ClusterRoleBinding names) and open a shell in it, with the token at `/var/run/secrets/kubernetes.io/serviceaccount`. That is a standard Kubernetes escalation, and the console's own RB-12 rationale ("nothing on RBAC, no pods/exec") was there to avoid exactly this class. SuperAdmin has every verb anyway; this concerns Admin.

**Decide:** (a) keep TB-50 and document it plainly in `roles-and-users.md` and the Permissions page ("Admin may run a pod as any ServiceAccount of its namespaces"), or (b) make the debug pod safe by default: `automountServiceAccountToken: false` on the template unless a ServiceAccount was explicitly chosen, offer the choice only to SuperAdmins (or list only ServiceAccounts the person may `use`, which RBAC cannot express for a Job), and keep Admin's Job rule. (b) does not close the hole for a hand-made `kubectl create job`, since the right is in the ClusterRole; only (a) or dropping `jobs` from `adminTools` does. Whichever you choose, add it to `TestAdminHasNoRBACNoExecNothingClusterWide`'s comment and to the Permissions page as a Sensitive flag (`sensitive` map in `roles.go:403-410` should gain `pods/ephemeralcontainers` and `jobs` too, as TB-47 asks).

## Medium

### M-1. A viewer that falls behind is told "The session ended" while it is live *(probed)*

`server/internal/sessions/session.go:690-714` (`termWriter.Write`), `server/internal/api/toolbox.go:798-810` (`sessionAttach`), `web/src/components/toolbox/Terminal.tsx:101-120`.

Each browser gets a channel of 512 chunks. Output that arrives faster than the WebSocket sends it (`cat` of a large file, `yes`, a verbose build) fills it; `termWriter.Write` then drops the viewer and closes its `Done`, with the comment "A viewer that cannot keep up reconnects and replays". The handler reads `Done` as the end of the session and sends `{"type":"ended"}` with an empty reason; the terminal prints "── The session ended. ──" and the socket closes. The session is still live, the tab still says so, and nothing reconnects: the person has to leave the tab and come back, which remounts the terminal and replays the last 256 KiB.

**Fix:** tell the two apart. Send `{"type":"dropped"}` (or close with a distinct WebSocket close code) when the viewer was dropped and the session lives, and make `Terminal.tsx` reconnect on it with a short back-off, replaying what it missed; keep `ended` for the real end. Better still, do not drop at all: block the writer briefly with a deadline, since backpressure to the pod is what a real terminal does. Add a Go test that writes 600 chunks and checks the session's state against what the API sends, and an e2e test that `cat`s the simulator's long output.

### M-2. The `221b forward` command carries the token on the command line

`server/internal/api/toolbox.go:548-559` (`cliCommand`), `server/cmd/221b-cli/main.go:86`.

The pane shows `221b forward https://… --cluster … --session … --token <24 random bytes>` for the person to paste. The token then lives in their shell history and in `ps` output for every user of the computer for as long as the forward runs. The CLI already reads `$TWOTWENTYONEB_TOKEN`, so the fix is small: show `TWOTWENTYONEB_TOKEN=… 221b forward …` (a leading assignment stays out of most histories and out of `ps`), or make `221b forward` read the token from stdin or a prompt when `--token` is absent, and have the pane's Copy button copy the token separately. Also truncate `hello.Host` (`forward.go:78`) to something sane before it is shown and audited.

### M-3. Extend extends the wrong clock

`server/internal/sessions/session.go:852-860` (`Extend` calls `Keep`), `web/src/components/toolbox/SessionPanes.tsx:130-134`.

The pane shows **Extend** when the *hard* limit is under 15 minutes away, and the server's `Extend` only resets the *idle* clock (`LastActive`); `Ends` does not move (probed). The toast says "Extended." and the session still ends at the limit. TB-22 reads "Extend adds 10 minutes within the hard limit", which is the idle clock as built, so either show the button when the idle clock is low (the forward pane already does that in its bar) or make it extend `Ends` by 10 minutes up to `maxDuration` and the approved duration. Pick one, and add a test for `Ends` either way.

### M-4. `session.open` and the approval take the sign-in's groups as they were

`server/internal/api/toolbox.go:297-370` (`postSessionCard`), compare `postWatsonApprove` (`api/watson.go:434-453`, `rereadSession`).

RB-37 made Watson's approvals reread the directory so a change runs with the groups the person has now, not those from a few minutes ago. A session opened straight from its dialog (the common path since the October 26 review) does not reread, so someone removed from a group keeps opening shells with the old groups until the session cache expires. Call `s.rereadSession` in `postSessionCard` as `postWatsonApprove` does.

### M-5. History in the settings ConfigMap can push it past 1 MiB *(probed)*

`server/internal/sessions/history.go:86-152` (`KVHistory`), `server/cmd/221b/main.go` (`Key: "toolbox-sessions.json"`), `deploy/helm/221b/values.yaml:328-330`.

A record with a 500-character reason is about 1,040 bytes; `history.keep: 2000` (the value toolbox.md names, and `values.yaml` allows) is about 2 MB, in a ConfigMap whose limit is 1 MiB and which also holds every other setting. `SaveKey` patches one key, but the API server applies the limit to the whole object: once the history key is large, its own saves fail (the warning says "the audit log has it") and so does any other settings save that makes the object bigger, such as a new acknowledgement. The default 300 is fine (about 300 KB at worst). Either cap `history.keep` in `Config.Check` to what fits (about 800 records of this size, or cap by bytes and drop the oldest), or give the history the ConfigMap of its own that TB-45 describes. Add the probe as a test: 2,000 records with a 500-character reason must fit the cap.

### M-6. The server does not refuse a profile the namespace forbids *(probed)*

`server/internal/sessions/spec.go:141-151` (`Validate`), `:391-437` (`EphemeralContainer`), `:339-347` (`ProfilesAllowed`).

TB-24's acceptance says a profile the namespace forbids is "disabled in the dialog and refused by the server". `Validate` accepts `general`, `restricted`, `netadmin` and `sysadmin` for a debug container, the dialog offers two, and `EphemeralContainer` builds whatever it is given: a `sysadmin` request for a `restricted` namespace produces a privileged ephemeral container that only the API server's Pod Security admission turns away, with Kubernetes' message rather than the console's. On a namespace that only *warns* or *audits*, nothing turns it away. Since the October 26 review a debug container has two profiles: refuse the other two in `Validate` (TB-05's "a crafted POST for a hidden item is refused with the same reason"), and refuse a profile not in `ProfilesAllowed(level)` in `run` and `PlanFor`, naming the level.

### M-7. Sherlock's `open_tool_dialog` does not check that the target exists or is visible

`web/src/lib/uiControl.ts:273-308`.

TB-42: the action "runs only when the target exists and the person may see it (the same access-filtered lookup as clicking, P6U-07)… a target the person may not see yields 'not available' and no dialog". The action validates the names and opens the dialog; the dialog then fails to read the pod and shows "The pod could not be read (404)". The reason drafted by the model lands in the dialog either way. Look the pod or node up in the live store (the registry the other actions use) before opening, and refuse with "not available" as `view_details` does. The P6U-04 contract test lists the action; add a case for an invisible target.

### M-8. Session output of terminals is downloadable with transcripts off

`server/internal/api/toolbox.go:664-673` (`getSessionOutput`), `server/internal/sessions/session.go:946-954`.

`/sessions/{id}/output` returns the replay ring, which for a terminal is the last 256 KiB of everything shown, for up to an hour after the session ended, to its owner and every SuperAdmin. TB-54 says that with `transcripts: none` "no transcript is kept". The ring is needed for replay (TB-14), but the route should serve command sessions only; a terminal's end is already in the pane. Low effort: `if sessions.Interactive(ss.Req.Tool) { 404 }`.

### M-9. A forward does not end when its pod goes

`server/internal/sessions/forward.go:181-184`, `session.go:544-558`.

TB-13: a session ends "when the pod or node is gone". A terminal's exec stream errors out when the pod dies, so it ends (as `failed`, with Kubernetes' message, which TB-10 would rather call `Ended · pod gone`). A forward keeps the kubelet connection in `s.fwd`; after the pod is replaced every `Dial` fails and the CLI gets a Close per connection, but the session stays `live` until the idle clock runs out, up to 10 minutes, with the pane saying all is well. Watch the pod (the live store already has it) or end the session on the first `Dial` error that is not a refused port, with `EndGone`.

### M-10. The tool dialog is not a modal

`web/src/components/toolbox/ToolDialog.tsx:459-547`.

`role="dialog" aria-modal="true"` is declared, but Tab leaves the dialog into the page beneath (no focus trap, no `inert` on the rest), and the dock behind it stays live. The console's other dialogs (`ConfirmDialog`, the cluster editor) are worth checking for the same, but this one is new. Trap Tab inside `tb-dlg`, or set `inert` on the app root while it is open. Add the dialog, the menu and the dock strip to `a11y.spec.ts`, which has no toolbox page yet; Phase 6 ran axe on every new surface.

## Low

### L-1. A cut command never finishes *(probed)*

`server/internal/sessions/session.go:725-735` (`limitWriter.Write`).

Past `MaxOutput`, `limitWriter` returns the truncated count; `io.Copy` in remotecommand treats a short count as `io.ErrShortWrite` and stops copying stdout, so the stream is no longer drained and the command sits until the idle clock ends the session, which the record then calls `idle` rather than `exit 0`. Return `len(p)` after the cut and keep discarding. The command tools are no longer in the menus, which is why this is low.

### L-2. Zero times are serialized *(probed)*

`server/internal/sessions/session.go:97-115` (`ForwardStats.Last`, `Conn.Closed` are `time.Time` with `omitempty`).

`omitempty` does not omit a zero `time.Time`, so `last` arrives as `0001-01-01T00:00:00Z` and the pane shows a time under "Last traffic" before any traffic (`SessionPanes.tsx:253`); the connections table works around it (`:286`). With Go 1.26, `omitzero` does what was meant: `json:"last,omitzero"`.

### L-3. Two CLIs on one forward confuse the bookkeeping

`server/internal/sessions/forward.go:32-96, 134-145`.

A second `221b forward` with the same token reuses `s.fwd` and works, but whichever disconnects first sets `Forward.CLI = ""` and `Open = 0` for both. Refuse a second CLI while one is connected (`ErrCLI`, "another 221b is connected from …"), or end the first.

### L-4. The terminal does not reconnect after a network blip

`web/src/components/toolbox/Terminal.tsx:118-120`.

TB-14 wants a browser to reconnect "after a reload or a network blip within the idle limit". A reload works (the tab remounts and replays). A blip prints "Disconnected from the console." and stops; leaving and re-entering the tab is the workaround. Reconnect with back-off while the stream still lists the session as live (this is the same mechanism M-1 needs).

### L-5. The theme toggle restarts the terminal

`web/src/components/toolbox/Terminal.tsx:151`.

`dark` is in the effect's dependencies, so switching themes disposes the terminal and reopens the WebSocket (with a replay). `term.options.theme = …` in its own effect keeps the session's scrollback.

### L-6. No read limit on the terminal WebSocket

`server/internal/api/toolbox.go:741-747`.

The CLI socket sets `SetReadLimit(1 << 20)`; the terminal socket does not, so one message can be as large as the client cares to send. The owner is signed in, so this is hardening only: 64 KiB is plenty for keystrokes and pastes.

### L-7. `rememberOwner` never forgets

`server/internal/api/toolbox.go:431-438`, `server.go` (`owners sync.Map`).

One closure per sign-in session that ever asked for a session, kept for the life of the process. Delete the entry when the card is resolved (`startSession`) or lapses, or key it with a TTL.

### L-8. The gate bypass is wider than the handler

`server/internal/api/server.go:544-551`, `toolbox.go:49-61` (`IsTunnelPath`).

Every request whose path has the tunnel's shape skips `Auth.Gate`, whatever the method; only `GET` is routed, so the mux answers 405 for the rest, which is fine today. Cheaper to reason about if the bypass also checks `r.Method == GET` and `Upgrade: websocket`.

### L-9. The toolbox NetworkPolicy applies to nothing

`deploy/helm/221b/templates/toolbox.yaml:36-50`, `docs/toolbox.md` TB-49.

All three node-shell profiles set `hostNetwork: true` (`spec.go:552-566`), and a hostNetwork pod is outside every NetworkPolicy, as the template's own comment says. The policy is harmless but dead, and TB-49 says it "allows its pods nothing but the API server", which it does not (it allows nothing). Say what is true: the pods are on the node's network, nothing applies, which is why the namespace is SuperAdmin-only; keep the policy only for a future profile without `hostNetwork`.

### L-10. The default images are not pinned

`server/internal/toolbox/toolbox.go:159-163`, `deploy/helm/221b/values.yaml:369-382`.

TB-48 says "digest-pinned; a new tag is a chart upgrade". The defaults are `busybox:1.37`, `nicolaka/netshoot:v0.14`, `ubuntu:24.04` by tag, with a comment telling the operator to pin for production. Ship the digests in `values.yaml` (and a `make` target or a note in `releasing.md` to refresh them), so the default is the safe one.

### L-11. Node pods are BestEffort

`server/internal/sessions/spec.go:535-551` (`NodePod`).

No requests or limits: the first pod evicted under memory or disk pressure is the shell someone opened to look at the pressure. Small guaranteed requests and limits (the debug Job has them), and `priorityClassName: system-node-critical` is worth a thought for the toolbox namespace, since the pod's only job is to be there.

### L-12. Ephemeral-container conflicts are not retried

`server/internal/sessions/kube.go:246-251`.

`UpdateEphemeralContainers` sends the pod as read a moment earlier, with its `resourceVersion`; a status update in between gives a 409 "the object has been modified" that the person sees as a failure. `kubectl debug` has the same shape; a single re-read and retry on `IsConflict` is enough.

### L-13. A "general" node shell is not restricted

`web/src/components/toolbox/ToolDialog.tsx:26-30`.

As you asked, the general profile reads **Restricted** in the dialog. Its pod has `hostPID`, `hostIPC`, `hostNetwork` and the node's root filesystem mounted read-write at `/host`, which is root over the node for anyone who can write to `/host/etc` or `/host/root/.ssh`; "not privileged" means only that the kernel's capability set is the default. Calling it *Standard* or *Host* would not promise less than it gives. Your call; the words are the only thing at stake.

### L-14. With sign-in off, every browser owns every session *(probed)*

`server/internal/sessions/session.go:75-80` (`Who.same`), `server/internal/api/toolbox.go:64-78`.

With `auth.mode: none` every `Who` is the same person, so any browser can attach to, type into and end any session, and sees everything in Sessions. That follows from "sign-in off: everyone is an admin" and is right, but `toolbox.md` and the `values.yaml` comment should say it in one line, as the Watson values comment does for the two-person rule.

### L-15. `getToolbox` reads namespaces as the console

`server/internal/api/toolbox.go:250-253`.

The Pod Security level comes from `console.Namespace(ctx, ns)`, with the console's own rights, for any namespace name the browser passes (up to 100 per call). A person bound to `web` learns that `payments` enforces `restricted`. Trivial, but the lookup could run as the person, falling back to `privileged` when they may not read it, which is what the dialog assumes anyway.

### L-16. "gone" events name sessions the person never saw

`server/internal/api/toolbox.go:916-922` (`sessionsPart`).

A non-admin's stream gets a `gone` event with the id of every session that changes on the cluster, since `Visible` is checked after the id was taken from the subscription. Skip ids the person never received.

### L-17. The approval's lapse message names the wrong time

`server/internal/watson/service.go` (`Approve`, the `lapseLocked` message).

A session card waiting for a second person is good for `LapseWaiting` (30 minutes); the message when it lapses says "cards are good for 10 minutes".

### L-18. `sessionViewOf` has a no-op

`server/internal/api/toolbox.go:519-522`.

`if !v.Mine { v.Request.Reason = ss.Req.Reason }` assigns what is already there. If the intent was to hide the reason from people who are not the owner or an admin, it does not; if it was to show it, it already shows.

### L-19. The command tool's error message names three shells

`server/internal/sessions/spec.go:82, 131-133`.

`shellRE` accepts `auto`, `bash`, `sh`, `ash` and `zsh`; the refusal says "auto, bash or sh".

### L-20. Audit records of a short session can arrive out of order

`server/internal/sessions/session.go:478` (`go m.record(ActionOpen, …)`), `forward.go:97`.

`session.open` is written from a goroutine; a session that fails at once can have its `session.end` logged first. Write it inline (the audit log is fast) or hand the two to one goroutine.

### L-21. The Tools menu's changes do not say when the connection is read-only

`web/src/components/toolbox/ToolsMenu.tsx:168-223`.

Cordon, Drain and Delete have no `off` reason; on a read-only connection the server refuses them and Watson says why, a step later. TB-02 wants the item disabled with its reason: `info.readOnly` is already in hand.

### L-22. The DCO sign-off

Every commit of the pull request (and of `main`) is signed off `Claude <noreply@anthropic.com>`. The Developer Certificate of Origin is a statement by a person or a legal entity that they have the right to submit the change under the licence; a sign-off by the tool is not that statement. For an Apache-licensed project that asks contributors for a DCO (CONTRIBUTING.md), your commits should carry your own `Signed-off-by` (keep the `Co-Authored-By` line for the tool). `git rebase --signoff` fixes a branch before it is merged.

## Requirement rows that drifted from what was built

These are in the TB rows but not in the *As built* table's *Differs* column. Either build them in 9.1 or note them there, so the document stays the record it is meant to be.

| Row | Built | Says |
| --- | --- | --- |
| TB-01 | The Tools button is on every row, and a Viewer's menu shows the items disabled with the reason; no `s` key opens the menu (Tab reaches the button, Enter opens it). | The cell is empty when no tool may be used; Enter or `s` on a focused row opens the menu; "the keyboard path has a browser test". |
| TB-05 | Tests cover the main cases. | "A test matrix of role × values × mode × env yields the expected items." |
| TB-07 | A reason is required only where a second person approves and for node tools on production; the footer's approver list is empty (`ToolDialog.tsx:537`; `toolboxView.ApproverNames` is never filled). | A production dialog always has Reason and For how long; the footer names the possible approvers. |
| TB-08 | Dialogs start from the prefill only. | Dialogs remember the last choices per tool and per pod or node in the browser (no `localStorage` use in `components/toolbox`). |
| TB-09 | The tab's × closes the tab and the session goes on, as its tooltip says. | The × ends a live session after a confirmation. |
| TB-13 | Forwards do not notice a replaced pod (M-9); a terminal's end says `failed` with Kubernetes' words. | Sessions end when the pod is gone, as `Ended · pod gone`. |
| TB-14 | A reload reconnects; a blip does not (L-4). | Reconnects after a reload or a network blip. |
| TB-24 | The dialog hides the forbidden profile; the server builds any (M-6). | Disabled with the reason, and refused by the server. |
| TB-42 | Names are checked; existence and visibility are not (M-7). | Access-filtered lookup first. |
| TB-45 | The settings ConfigMap, `keep: 300` (M-5). | A ConfigMap of its own, 2,000 records. |
| TB-47 | `pods/exec`, `pods/attach`, `pods/portforward`, `nodes/proxy` are flagged, as before. | `pods/ephemeralcontainers`, `jobs` and the toolbox namespace's `pods` flagged Sensitive. |
| TB-49 | The policy allows nothing and applies to no profile (L-9). | A NetworkPolicy that allows the API server. |
| TB-53 | `session.watch`, `session.extend`, `session.open`, `session.end`, `session.request` exist; `session.refuse` and `session.lapse` are Watson's `watson.refuse` and `watson.lapse` on the card. | Nine named actions. Fine as built; say so. |
| TB-57 | `TestTunnelForwardsConnections` exercises the framing end to end; `internal/tunnel` has no test file of its own. | Unit tests for the tunnel framing. |
| TB-58 | No kind test touches the toolbox (`hack/e2e-kind.sh`). | e2e on kind: shell, forward through the CLI, debug container, debug pod, node shell with each profile, two people, idle end, admin End, restart. |

## Text that is stale

- `deploy/helm/221b/values.yaml:303-311`: "Each is a Watson card first (approved by a second person on production, and by a second SuperAdmin for anything on a node)". Since the review: the dialog's button opens the session; a card only where a second person must approve; node shells follow the cluster's rule.
- `docs/toolbox.md`, TB-07 and TB-32 rows and the Scope line "Every tool as a Watson card of a new kind": amended by the October 26 notes but the rows read as before. The *As built* table is right; align the rows or mark them.
- `docs/architecture.md`, ADR-66's *Decision* paragraph still describes every session as opened by a card; its *Amended* paragraph corrects it. Fine, but the API table above it should list `POST …/watson/sessions` (201 `opened` or 202 card) and `POST …/watson/actions`.
- `web/src/components/toolbox/SessionPanes.tsx:348`: "The Tools button beside a pod or a node opens one, through Watson." Not through Watson any more.
- `web/src/components/toolbox/ToolDialog.tsx:423`: "Use for troubleshooting containers, services, and other objects…" is the one sentence in the dialogs that reads like a product blurb; the others say what happens.

## Before the first run on RKE2

What the tests cannot prove and the *As built* table leaves open, in the order that finds the most with the least:

1. `helm upgrade … --reuse-values` on your `k8s-console` release: the `221b-toolbox` namespace appears with its three Pod Security labels (`kubectl get ns 221b-toolbox --show-labels`), the ConfigMap `k8s-console-221b-toolbox` renders your values, and `kubectl auth can-i create pods -n 221b-toolbox --as system:serviceaccount:221b:k8s-console-221b` is **no** (TB-35).
2. A shell into a pod on `local-rke2` from the Tools menu: `id`, `kubectl auth whoami` if the pod has kubectl, resize the dock, leave the tab and come back (replay), reload the page (replay), then let it idle to the warning and past it (TB-13). Read the audit lines: `kubectl -n 221b logs deploy/k8s-console-221b | grep '"session\.'`.
3. A shell into a distroless pod: the "no shell" message with the debug-container hint, then the debug container itself with volumes mounted, in a namespace that enforces `baseline` and one that enforces `restricted` (M-6 is what you will see on the second if a crafted request reaches it; the dialog alone should be fine).
4. A port forward to a Service's pod with `psql` or `curl` through `221b forward`, two connections at once, Ctrl-C and run the command again (reconnect), then End from the pane and watch the CLI say so.
5. A node shell with each profile on a worker: `chroot /host journalctl -u rke2-agent -n 50` (general), `tcpdump -i any -c 5` (netadmin), `nsenter -t 1 -m -- ls /` (sysadmin); confirm the pod is gone after End and after the hard limit (`kubectl get pods -n 221b-toolbox -w`).
6. A debug pod with a pod's labels: `dig` a Service, and confirm it is never in `kubectl get endpoints` (the readiness gate), and that the Job and pod are gone after End and after `ttlAfterFinished`.
7. Two people on a production connection with the two-person rule: the requester has no Approve button, the second person does; with H-1 unfixed, try it as a Viewer and watch it work, which is the bug.
8. A second console replica (`replicaCount: 2`): the terminal and the tunnel reach the leader through the non-leader (`forwardToLeader` passes the upgrade; this is the one path with no test at all).

## Notes on running the checks here

The Go toolchain and module proxy were not reachable from the review environment, so Go 1.26.0 was built from source and the modules were fetched from their GitHub mirrors through a scratch `-modfile`; the repository's `go.mod` and `go.sum` were not touched. `helm` 3.10 rendered the chart; CI's newer Helm may word a refusal differently but renders the same objects. `go test -race ./...` passed in every package (`api` 151 s, `auth` 133 s, the rest under 25 s).
