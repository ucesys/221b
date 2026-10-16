# Sherlock UI control: requirements

Oct 15, 2026 · Mikhail Stolpner

An addition to [Phase 6](requirements.md#phase-6-watson), written against the `phase-6` branch at commit `067f67c`, and built on that branch (see [As built](#as-built)). It lets Sherlock read what you are looking at, move you around the console, and change what a table shows, so that "show me the crash-looping pods in web" or "why is this node not ready?" works on the screen you have open. It changes nothing in a cluster and nothing other people see. The requirement IDs start with P6U so they cannot clash with the P6 numbers; they fold into the P6 list when this is merged into the requirements.

## Scope

Sherlock today answers in the panel and links to objects; you do the clicking. This addition gives it three abilities:

- **Read the view.** It knows the cluster, the tab, the filters, the search text, the sort, the columns, how many rows are shown and which details pane is open.
- **Navigate.** It opens a tab, or an object's details, over the page you are on.
- **Update the view.** It sets filters and search text, sorts a table, shows or hides columns, and highlights a row.

**What it does not do.** It does not change a cluster (that stays with Watson and an approval card), change settings other people share, fill in or submit any form, or press any button of yours. It never acts unless you have asked a question and it is answering.

## Approach

| Decision | Choice | Why |
| --- | --- | --- |
| How Sherlock sees the UI | A small structured snapshot of the view, not a screenshot | The console already holds the state; the snapshot is exact, cheap, and shows nothing the screen does not. |
| How Sherlock acts | A fixed registry of typed actions that run in your browser; no click, type, DOM or script access | The worst outcome of a bad instruction is a changed filter, and every action can be validated, audited and tested. |
| Where the model loop runs | Unchanged: on the server. The UI actions are tools the server cannot run itself, so it sends each to your browser and waits for the result | Keeps the key, the scrubbing, the caps and the audit where they are (ADR-42, ADR-44). |
| Where view state lives | Filters stay in the URL, as the tabs already keep them; sort, search text and column choices are reached through the shared table hook | `useTable` is used by every table and already has an `id` for each, so one change covers them all. |
| What an action may touch | Only your own view. Shared settings and clusters are out of reach | Matches X-03 and ADR-53: the browser's only way to change a cluster is Watson's card. |

**Alternatives considered.** Driving the page like a person would (screenshots, clicks, typing, as a browser extension does) is slower, costs more per step, breaks when the layout changes, could reach Approve and run, and shows up in the audit as nothing but clicks. It is rejected.

## What the view state is today

Read from the branch, because it decides how big the change is:

- **Filters** (for example `label`, `taint`, `fleet`, `kinds`, `pt`, `pl`, `open`, and `peek` for the details pane) are URL parameters read and written through `lib/router.ts`. Navigation and filtering already need no new plumbing.
- **Sort** is component state inside `useTable`, so nothing outside the table can read or set it.
- **Search text** is in the URL on the Nodes, Pods, Services and Access tables and in component state in the rest (the Helm and object-panel tables, for example).
- **Column choices** are kept in this browser's storage, per table `id`.
- **Sherlock's tools** are 22 server-side reads, a list fixed by a test (ADR-43); the conversation lives on the server and every window of the session follows one event stream (ADR-42, P5-08).

So reading and navigating are mostly there. The new work is making sort and search text reachable, a tool path that goes out to the browser and back, and the rules around it.

## Requirements

These apply on top of the requirements for every phase (X-01 to X-17). Those that matter most here: X-01 (nothing Secret reaches Sherlock), X-03 (nothing changes a cluster without a card), X-06 (the new endpoint is protected like the other writes), X-12 (accessibility) and X-16 (what leaves for the Claude API).

### Reading the view

| ID | Requirement | Done when |
| --- | --- | --- |
| P6U-01 | Every table's sort and search text can be read and set from outside the table, through `useTable`, under the table's `id`. Behaviour of tables that nobody drives is unchanged. | A test finds every table the app builds registered, sets and reads its sort and search text, and the table shows them. |
| P6U-02 | A view-state snapshot: cluster, tab and path, filters, search text, sort (column and direction), shown and hidden columns, rows shown against rows in the table, the names of the first 50 rows shown, and the open details pane. It holds nothing the screen does not show. | The snapshot of each tab matches what the tab shows, for every tab. |
| P6U-03 | The snapshot is sent with every question (2,000 characters at most), so "this node" and "these pods" mean what is on screen, and a tool, `view_state`, reads it afresh after Sherlock has changed something. Like every tool result it is scrubbed first (ADR-44). | With a node's details open, "why is this node not ready?" is answered about that node. A token-shaped string typed in the search box is `[REDACTED]` in the request the fake Claude API received. |

### Acting on the view

| ID | Requirement | Done when |
| --- | --- | --- |
| P6U-04 | The actions are one fixed list, defined once and used by both sides: the server's tool definitions and the browser's registry. Arguments are checked against what each tab and table accept: filter names and values, column ids, sortable columns only. Anything else is refused with a message the model can use. | A contract test fails when the two lists differ; a call with an unknown action, filter or column is refused and changes nothing. |
| P6U-05 | The actions in [the table below](#the-actions), and no others. Each returns the new view state, so Sherlock sees what it did. | Each action has a passing browser test, in Chromium, Firefox and WebKit. |
| P6U-06 | Navigation stays inside the console: tabs, a tab's path and filters, and object details over the page. No external address, no new window or tab, no download. Sherlock does not switch to another cluster, because a conversation belongs to one cluster (ADR-42); it offers a link to it. | A request to open another cluster or an outside address is refused, and Sherlock answers with the link. |
| P6U-07 | UI actions fetch no data of their own. What a tab or a details pane shows comes through the same access-filtered API as when you click. An action aimed at an object you may not see fails the way clicking would, and Sherlock learns no more than that it is not available. | A person limited to two namespaces asks Sherlock to open a pod in another namespace: the answer says it is not available, and nothing about the pod reaches Sherlock. |
| P6U-08 | Actions run in the console window that asked, or, when the question was asked in Sherlock's own window (P5-08), in the main window that window follows. With no such window, or none answering within 5 seconds, the tool says so and Sherlock tells you instead of guessing. | Questions asked in the main window, in the separate window, and with the main window closed each end as described. Two browser tabs of one session: only the one that asked changes. |

### Manners and limits

| ID | Requirement | Done when |
| --- | --- | --- |
| P6U-09 | Sherlock changes the view only while it is answering your question. At most 10 UI actions per answer, counted with the existing limit of 12 rounds. If you change the view yourself during the answer, Sherlock's later view changes in that answer are not applied and it is told so. Focus stays where it was. | An unprompted change never happens; an eleventh action is refused; a tab you click mid-answer is not overridden. |
| P6U-10 | After Sherlock has changed the view, its answer shows "Sherlock changed this view" with an Undo button that puts back what the view was before the answer's first change (tab, filters, search text, sort, columns, details pane). Screen readers hear a short description of each change. | Undo restores each kind of change; the announcement is in an `aria-live` region and does not move focus. |
| P6U-11 | Column changes made by Sherlock last until the page reloads or you change columns yourself. They are not written to the choices saved in the browser. | After Sherlock hides a column and the page reloads, the column is back. |
| P6U-12 | No UI action reaches Watson, an approval card or the Approve and run button, any setting shared with others (refresh rate, fleets, acknowledged alerts, Sherlock's and Watson's settings), any editor, dialog or form, an export, or any control that changes a cluster. There is no generic click, type, script or screenshot action. | A test lists the registry and finds none of these; a Sherlock-driven view change cannot produce a Watson approval. |
| P6U-13 | Text that comes from a cluster (names, labels, annotations, events, log lines) can cause no action outside the list, and every value a UI action sets is treated as data: matched against rows, never rendered as HTML, never used as an address. | Planted instructions in a pod label and a log line never lead to an action the question did not call for (nightly evaluation, P6U-17); values with markup and script render as plain text. |
| P6U-14 | An administrator switch in Sherlock's settings, **Let Sherlock change the view**, on by default. With it off, Sherlock still reads the view state and answers, and the UI tools that change the view are not offered. | With the switch off, no such tool appears in the request the fake API receives. The change is audited like Sherlock's other settings (`sherlock.settings`). |

### Audit and the toolset

| ID | Requirement | Done when |
| --- | --- | --- |
| P6U-15 | Each question's one audit record (P5-05) also lists its UI actions: name, scrubbed arguments, and whether each was applied, dropped or found no window. No new kinds of record. Results count as tool results for tokens and spending. | A question with three UI actions writes exactly one record naming all three. |
| P6U-16 | P5-07 is reworded, since the toolset now holds tools that act on the browser: "Sherlock's toolset contains no tool that writes to a cluster or to settings shared with others. Its UI tools change only the asking person's own view." The tool-list test is updated to say which tools are which. | The test fails if a tool that writes to a cluster or to shared settings is added. |

### Transport, accessibility and tests

| ID | Requirement | Done when |
| --- | --- | --- |
| P6U-17 | The server sends each action to the browser as a `ui` event on the conversation's existing event stream and waits, up to 5 seconds, for the result, which the browser posts to `/api/v1/sherlock/ui/{id}`. The post needs the session that asked and is protected like the other writes (X-06). Several replicas (Phase 7) need the same sticky routing as conversations (ADR-42). | A result posted from another person's session is refused; a late result is ignored; an action with no answer ends the tool call with a timeout. |
| P6U-18 | The change notice, the Undo button and the highlight meet WCAG 2.2 AA in both themes and at 390 px, respect reduced motion, and do not take focus. | axe finds no failure; the highlight is not conveyed by colour alone. |
| P6U-19 | Tests. The fake Claude API (ADR-48) scripts UI tool calls; browser tests cover every action in the simulator and in kind, with the panel docked and in a window of its own. The nightly evaluation (P5-06) gains UI tasks, such as "show me the pods that are not running in web" and "open the node with the most problems", graded by the resulting view state, and cases with instructions planted in cluster text. | At least 90% of the UI tasks end in the right view state, and no planted instruction is followed. |
| P6U-20 | No regressions. Existing tables and tabs behave as before, the Phase 1 to 6 browser tests pass unchanged, and setting a sort or search through the registry on 10,000 rows takes under 200 ms (X-08). | CI passes on the pull request. |

### The actions

| Action | What it does | Limits |
| --- | --- | --- |
| `view_state` | Reads the view state (P6U-02). | Read only. |
| `view_open` | Opens a tab of the cluster you are on, with an optional path and filters. | Tabs and filters the console has; no other cluster. |
| `view_filter` | Sets, adds to or clears the filters of the tab you are on, such as namespace, node, status, labels, taints, fleet and object kinds. | Names and values the tab accepts. |
| `view_search` | Sets or clears the search text of a table. | 200 characters. |
| `view_sort` | Sorts a table by a column, ascending or descending. | Sortable columns of that table. |
| `view_columns` | Shows or hides columns of a table. | That table's columns; not saved (P6U-11). |
| `view_details` | Opens or closes the details pane of an object, as a link does with `peek`. | Objects you may see (P6U-07). |
| `view_highlight` | Scrolls a row into view and marks it for a few seconds. | One row, not saved, no focus change. |

## Where the work lands

Mostly new files, to keep clear of the other changes to Phase 6 that are going in alongside:

- **Browser.** A new `lib/uiControl.ts` (the registry and the snapshot), a small change in `lib/useTable.ts` (register under its `id`, accept outside sort, search and column overrides), `api/sherlock.ts` (handle `ui` events, post results), and `components/Sherlock.tsx` (the change notice and Undo). Tabs change only where a filter is not yet declared to the registry.
- **Server.** `server/internal/sherlock` (a tool the model calls that waits for the browser), `server/internal/api/sherlock_tools.go` (the UI tool definitions and `view_state`), a route for the results, `server/internal/fakeclaude` (scripted UI calls), the evaluation set, and the audit record.
- **Docs.** The Sherlock sections of the architecture document and a decision record, with the P5-07 rewording.

## Not in this addition

- Switching clusters, exporting, and anything on the Settings page, the fleet editor or the connection editor.
- Acknowledging alerts, which is shared and audited (ADR-39).
- Anything in Watson: typing commands, approving, discarding.
- Acting without a question, on a schedule, or while no console window is open.
- Screenshots, DOM access or scripts.

## Open questions

| Question | Proposal | Status |
| --- | --- | --- |
| May Sherlock switch to another cluster? | No (P6U-06). It offers a link, since a conversation belongs to one cluster and would otherwise be lost on the switch. | Decided as proposed |
| May Sherlock acknowledge an alert? | No. It opens the alert and you acknowledge it. | Decided as proposed |
| Should Sherlock be able to fill in, but not submit, editors such as the fleet editor? | Not now. Maybe later, never for credential fields. | Decided as proposed |
| Is the administrator switch (P6U-14) on or off by default? | On, wherever Sherlock is on. | Decided as proposed |
| Is the rewording of P5-07 (P6U-16) acceptable? | Yes: the rule that matters is that nothing Sherlock can call writes to a cluster or to what others share. | Decided as proposed |

## As built

Built as specified, with the open questions settled as proposed. Where the work landed and what was decided along the way:

- **One list.** `server/internal/sherlock/ui_actions.json` holds the eight actions with their descriptions and input schemas. The server embeds it as the tool definitions; the browser imports the same file for its registry (`web/src/lib/uiControl.ts`), and the image build copies it into the browser's build. A Go test and a unit test fail if the lists differ or grow.
- **The view state** goes with the question inside `<console-view>`, after scrubbing. The details pane is reported as open, loading or not available, and nothing more about an object the person may not see.
- **Which window.** Each console window has an id. A question from a main window names that window; one from Sherlock's own window names the main window that opened it, and asks it for its view. A `ui` event names its window, and only that window runs it.
- **The tables.** Every table built with `useTable` registers under its id (`web/src/lib/tables.ts`). The Namespaces tab's table of objects drives only the columns of its kind cards. Tables built without `useTable` (the kind cards, the Fleets matrix, the alert cards' event lists and the release list on Namespaces) are not sorted or searched by Sherlock, though their tabs' filters can be set.
- **A drawer Sherlock opens** stops above the dock and is not modal, so the answer, Undo and the question box stay in reach. Drawers the person opens are unchanged.
- **Undo** is offered under the answer in any window; the main window that ran the changes holds what to restore, so Undo from Sherlock's own window puts that main window back.
- **Tests.** The fake Claude API runs the tool calls a question scripts as `[[tool {input}]]`, one per round, and `[[sleep ms]]` waits before the next. With the details of a node open, "this node" is answered about it. `web/e2e/sherlock-ui.spec.ts` covers each requirement in the simulator and in kind, in Chromium, Firefox and WebKit; `server/internal/api/sherlock_ui_test.go` covers the transport, limits, switch and audit.
- **Evaluation.** `221b-eval` gained UI tasks graded by the view they leave and two cases with instructions planted in a pod's annotation and log (`221b-sim -planted`). A stand-in console window answers the `ui` events. The nightly run fails below 90% of the UI tasks or if any planted instruction is followed.
- **One existing test changed.** The P5-02 browser test checks that no hidden pod's name reaches the Claude API. The view state now names the rows on screen, and a pod the person may see can share its name with a hidden one (`cache-0` in two namespaces). The test now skips names that are also visible.

## Your review

- [ ] The list of actions and what they may not touch (P6U-04 to P6U-06, P6U-12)
- [ ] Manners: only while answering, the 10-action limit, yielding to you, Undo (P6U-09 to P6U-11)
- [ ] Permissions and untrusted text (P6U-07, P6U-13)
- [ ] The rewording of P5-07 and the administrator switch (P6U-14, P6U-16)
- [ ] The five open questions
- [ ] Go-ahead to build it as part of Phase 6
