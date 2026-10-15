# Phase 5 review

This is the guide for the review that closes Phase 5 ([requirements](requirements.md#phase-5-sherlock)). For each item on the review list it says what was built, what CI already proves on every pull request, and what is left for you to check by hand. The Phase 4 guide is [phase-4-review.md](phase-4-review.md); [architecture.md](architecture.md) has been brought up to date, ADR-9 is now accepted as built, and seven decision records are new (ADR-42 to ADR-48).

## What Phase 5 built

**Sherlock** answers questions about a cluster from its live state. It sits in a panel docked at the bottom of every tab, as the prototype has it, and moves to a window of its own for a second monitor. You ask in the panel, or with an **Ask Sherlock** button on an alert, in a drawer (a node, a pod, a Service, any object, a policy, a role) or on a callout (a traced request, a connection check, an identity, a Helm release). The answer streams in under the steps Sherlock took ("Read pod web/api-0", "Read the previous run's log of web/api-0"). It cites the objects it relied on as links that open them over the tab you are on, ends with buttons for those objects and questions to ask next, and can be stopped.

Behind it, the server sends the question to the Claude API with 22 read-only tools. Each one runs as the person asking: the console's own API, called in-process with their session, so a tool sees exactly what that person's tabs show. What the tools return is scrubbed of anything that looks like a credential before it leaves. An administrator turns Sherlock on per cluster, saves the API key (kept in a Secret, never shown again), picks the model and sets daily and monthly spending caps. Each question is audited with the tools it used and what it cost.

Decisions worth a look, because they shape what you will see:

- **ADR-42:** conversations live on the server, one per session and cluster, so a reload keeps them and any window of the session shows the same one. They end with the session, or after 12 hours unused, and do not survive a restart.
- **ADR-43:** the tools are the console's own read endpoints, called as the person, so Sherlock cannot see more than the tabs show. The list is fixed by a test.
- **ADR-44:** everything a tool returns is scrubbed, and Helm values are not sent at all.
- **ADR-45:** an answer links only to objects the tools returned; any other "link" becomes plain text.
- **ADR-46:** spend is the console's estimate from the tokens the API reports, at list price; your account's bill is the authority.
- **ADR-47:** the separate window (new requirement P5-08, and P6-09 for Watson) follows the same conversation and talks to the main window over a `BroadcastChannel`.
- **ADR-48:** pull requests test against a fake Claude API in the simulator; a nightly workflow measures the real one on ten sample problems.

## Setting up for the review

1. Get a Claude API key for the review from the Claude Console (console.anthropic.com).
2. On a test installation, open **Settings** as an administrator. Under **Sherlock**, paste the key and **Save key**, tick the cluster under **On for**, and **Save Sherlock settings**. The panel appears at the bottom of every tab.
3. For the nightly evaluation, add the key to the repository as the Actions secret `ANTHROPIC_API_KEY` (Settings → Secrets and variables → Actions). Until then the nightly run checks the plumbing against the fake API and says so. You can also run it locally: `ANTHROPIC_API_KEY=… make sherlock-eval`.

With `connections.store: secrets` (the default) the key is kept in the Secret `<release>-sherlock`; to manage the key yourself instead, set `sherlock.existingSecret` and `sherlock.existingSecretKey`. The chart's NetworkPolicy allows HTTPS out for Sherlock (`sherlock.egress`, on by default).

## 1. The panel (P5-01)

**CI proves** (`web/e2e/sherlock.spec.ts`, in the simulator on Chromium, Firefox and WebKit and in kind on 1.35 to 1.37):
- Off for a cluster, nothing of Sherlock shows: no panel, no Ask Sherlock buttons. On, the panel docks at the bottom of every tab.
- It resizes by its grip, both dragged and with the arrow keys. It maximizes over the page (a dialog; Escape restores it) and minimizes to an icon, which this browser remembers across a reload.
- Ask Sherlock on an alert asks about that alert. In a pod's drawer it asks about the pod and shows the answer maximized, above the drawer.
- axe finds no WCAG 2.2 AA failure in the panel docked or maximized, or in the settings, in either theme.

| Prototype | Console |
| --- | --- |
| ![Prototype, the panel with an answer](images/phase-5/prototype-answer-light-1440.png) | ![Console, the panel with an answer](images/phase-5/app-answer-light-1440.png) |

Watson's half of the prototype's panel arrives in Phase 6; until then Sherlock has the whole width, and its header carries the minimize, maximize and new-window buttons.

| Maximized | Ask Sherlock on the Alerts tab | Phone width |
| --- | --- | --- |
| ![Maximized](images/phase-5/app-max-light-1440.png) | ![Ask Sherlock on alerts](images/phase-5/app-alerts-ask-light-1440.png) | ![390 px](images/phase-5/app-dock-light-390.png) |

**To check by hand:** use the panel on a tab with a drawer open, at your usual window size and on a phone, and ask from a few Ask Sherlock buttons.

## 2. Read-only tools, run as the person asking (P5-02, P5-07)

**CI proves:**
- In Go (`server/internal/api/sherlock_test.go`), a viewer denied `kube-system` asks about a pod there, its namespace and its logs. None of that pod's UID, its log lines or the events of that namespace reach any request the fake API receives. Once Kubernetes lets them list it, the same question gets the log.
- In the browser test, a person limited to some namespaces (two in the simulator, the console's own in kind, where RBAC decides) asks about a pod in another. The requests the fake API received carry nothing of that namespace's pods.
- The toolset is fixed by `TestSherlockToolsOnlyRead`: 22 names, none of them a write. The interface the tools reach (`api.Cluster`) has no method that writes, which the compiler enforces.

The tools: `cluster_overview`, `list_alerts`, `list_nodes`, `get_node`, `list_pods`, `get_pod`, `pod_logs`, `list_events`, `list_objects`, `get_object`, `get_yaml`, `list_services`, `get_service`, `list_helm_releases`, `get_helm_release`, `top`, `list_fleets`, `trace_ingress`, `check_connection`, `list_policies` (network policies, budgets, drains, quotas, limits, Pod Security, webhooks, priority classes), `who_can` and `list_access`. When a fix needs a change, Sherlock gives the commands in a block with **Copy**; Watson runs them from Phase 6.

**To check by hand:** sign in as someone limited to one namespace and ask about another. Then read the audit records (`kubectl -n 221b logs deploy/console-221b | grep sherlock.ask`): each names the tools the question used.

## 3. Nothing secret reaches the Claude API (P5-03)

**CI proves** (`web/e2e/sherlock.spec.ts`, `server/internal/sherlock`):
- After questions about a Secret holding a planted value, two pods' logs and the Helm releases (one has a planted password in its values), no request the fake API received contains the planted Secret value, the Helm password, the API key, or the credentials the simulator plants in every pod's log (a password in a `postgres://` URL and an `api_key=`).
- The log lines are there, scrubbed: `postgres://orders:[REDACTED]@db.internal…`.
- The scrubber's unit tests cover private keys, bearer tokens, JSON Web Tokens, cloud and forge token shapes, and environment variables named like secrets.

**To check by hand:** in a namespace you control, run a pod that prints a fake password (`echo "password=hunter2-review"`), ask Sherlock for its logs, and confirm the answer shows `[REDACTED]`.

## 4. Answers stream, cite and stop (P5-04)

**CI proves:**
- An answer's first step shows within 5 seconds.
- Every cited link opens its object over the tab (a node's or pod's details).
- A link to an object no tool returned is plain text.
- Stopping keeps what arrived and marks it stopped, and the next question is answered.

With the real API, time to first output depends on the model; the nightly report gives the 90th percentile across the ten cases.

**To check by hand:** ask a broad question ("What needs attention right now?"), click each link, and stop a long answer part way.

## 5. Settings, spend and audit (P5-05)

| Settings |
| --- |
| ![Sherlock's settings](images/phase-5/app-settings-light-1440.png) |

**CI proves:**
- Only administrators read or change the settings and the key.
- The key never comes back from any endpoint or in the audit log.
- Only clusters that exist can be turned on, and a daily cap above the monthly cap is refused.
- With the daily cap below what was spent, the panel says "Sherlock has reached the daily spending cap of $… an administrator set. It answers again tomorrow (UTC), or when an administrator raises the cap in Settings." A question is refused with 429, and raising the cap lets questions in again.
- Each question writes a `sherlock.ask` record with the question, model, tools, tokens and cost.

**To check by hand:** set a daily cap of $0.01, ask once, and see the next question refused. Check that the spend shown in Settings is close to the usage your Claude Console reports for the key.

## 6. The evaluation set (P5-06)

`server/cmd/221b-eval` finds ten of the prototype's sample problems in the simulator and asks Sherlock about each. An answer passes only if it names the facts that make the cause:

| Case | The answer must name |
| --- | --- |
| pending-pod: a pod no node has room for | insufficient CPU, and the nodes |
| image-pull: an image that cannot be pulled | the image, and the pull error |
| crash-loop: a container that keeps crashing | the database at `db.internal:5432` refusing connections, from the previous run's log |
| oom-killed: a container killed for its memory | out of memory, and the limit |
| node-down: a node that stopped reporting | the kubelet not reporting |
| disk-pressure: a node short of disk | disk pressure |
| helm-failed: a failed Helm upgrade | the timeout, and the revisions or a rollback |
| lb-pending: a LoadBalancer without an address | the load balancer, and the missing controller or address |
| drain-blocked: a drain blocked by a disruption budget | the budget, by name |
| quota-stall: a rollout a quota would stall | the quota, and the room the surge pod needs |

The simulator gained one release for this: `analytics/superset`, whose last upgrade failed. That is the prototype's case.

**CI proves** on every pull request that the cases are found and the harness runs end to end (against the fake API, which grades nothing). The **nightly workflow** (`.github/workflows/sherlock-eval.yml`) runs the cases against the real API with `ANTHROPIC_API_KEY` and fails below 9 of 10. Its report, with each answer, is in the run's summary and artifacts.

**To check by hand:** once the secret is set, run the workflow from the Actions tab (it takes a model as input) and read the report.

## 7. Sherlock in a window of its own (P5-08, new)

| The main window | The separate window |
| --- | --- |
| ![Main window while Sherlock is apart](images/phase-5/app-popped-light-1440.png) | ![Sherlock's own window](images/phase-5/app-window-light.png) |

**CI proves:**
- The new-window button opens Sherlock alone in a window of its own, and the main window shows an icon instead of the panel.
- Ask Sherlock in the main window is answered in the other window.
- A link there opens its object in the main window.
- **Dock back** closes the window, and the panel returns to the main window with the conversation.

**To check by hand:** drag the window to another monitor, switch clusters in the main window (Sherlock follows), and close the window with the browser's own button.

## Not in Phase 5

Running anything: Watson arrives in Phase 6 and will take the commands Sherlock proposes. Conversations end with the session and are not kept elsewhere; the audit log keeps the questions.
