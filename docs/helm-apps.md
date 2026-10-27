# Helm applications and Secret editing: requirements

Phase 10, asked for on October 27, 2026: a Helm page that lists every release in one table and deploys charts from a git repository, and Secret editing for Admins and SuperAdmins. The page is shown as **Helm1** beside the Helm tab until it has everything the Helm tab offers; then the Helm tab goes. The requirement rows are in [requirements.md](requirements.md#phase-10-helm-applications-and-secret-editing) (HA-01 to HA-20, SE-01 to SE-06); the decisions are ADR-68 and ADR-69 in [architecture.md](architecture.md).

## Scope

In:

- A table of every release: name, namespace, chart, app version, revision, status, drift, source, updated; no summary above it.
- Installing a chart from a git repository at a branch or a tag; upgrading, switching the branch or tag, changing the values file and parameters, uninstalling; adopting a release the console did not install by setting its source.
- A release's panel beside the table: overview with source, commit and drift; values file and parameters beside the values as deployed; history with diffs of two revisions or of the deployed one against the source, and a notice when a newer tag or commit exists; the images each revision runs.
- Secret editing on the Namespaces tab: replace, add and remove keys, make a Secret; values shown only where `secrets.reveal` is on.

Out, for now:

- Charts from chart repositories or OCI registries (the repository must vendor its dependencies under `charts/`).
- Automatic upgrades when a branch moves (the page says so; a person upgrades).
- Cluster-wide drift beyond a release's own objects, and a diff of live objects against the manifest (drift names the fields that differ; values are never shown).
- Rollback from the Helm1 page (the Helm tab's Watson `helm rollback` works as before).

## Approach

| Decision | Choice | Why |
| --- | --- | --- |
| Where the chart comes from | The console clones the repository at the ref, shallow and in memory, with go-git | The image has no git; nothing touches disk; a token for a private repository stays on the server |
| Who deploys | Watson, as the person: `helm install\|upgrade\|uninstall … --git --path --ref [--tag] --app` | The dry run, the card, the two-person rule, the audit log and the output are the ones every change has (ADR-66, ADR-68) |
| What the console keeps | A record per application in a Secret of its namespace, beside the connections | Survives restarts and replicas; the release itself stays Helm's |
| Drift | Each object of the manifest read as the person and compared with what the manifest sets; kept two minutes | Catches changes by hand without caching anything, and without showing values |
| Comparing | Manifests rendered on the console (`helm template`-style) and diffed object by object | No cluster call for a comparison; the same render serves the card's impact |
| Seeing a Secret's value | Only with `secrets.reveal`, one key at a time, as the person, audited | X-01 stays the default; the operator decides (ADR-69) |

## What exists today

- `server/internal/watson`: the Helm SDK through an impersonating client (`helm rollback`), cards with impact and guards, the action path that runs a person's own change at once.
- `server/internal/kube/secrets.go`: Secrets reduced to keys and sizes in the cache; `views.BuildReleaseDetails` for the Helm tab.
- `server/internal/clusters/store.go`: connections kept in Secrets of the console's namespace, the model for the application records.
- `web/src/components/HelmTab.tsx`, `ObjectPanel.tsx`, `toolbox/ToolsMenu.tsx`: the layouts and the menu the page reuses.

## Chart values

```yaml
secrets:
  read: true
  # Let Admins and SuperAdmins see a Secret's values, one key at a time,
  # read live as them and written to the audit log. Off, they edit blind.
  reveal: false
```

No value is needed for the applications: they need Watson (`localCluster.mode: write` or a connection that may change things) and the egress to the repository host over HTTPS, which `sherlock.egress` (on by default) already allows. With `sherlock.egress: false` and `outboundCIDRs` narrowed, add the repository host.

## Where the work lands

- `server/internal/apps`: the source, the git fetch and chart load, the client-side render, images, diffs, drift, and the record store (Secrets or memory).
- `server/internal/watson/apps.go`: the three commands, their runner and their card.
- `server/internal/api/apps.go`: the page's endpoints under `apps/…`; `api/secrets.go`: `secrets/config`, `secrets/{ns}/{name}/value`, `PUT secrets/{ns}/{name}`, `POST secrets/{ns}`.
- `server/internal/kube/secrets_edit.go`: the only live reads and writes of a Secret's values, as the person.
- `web/src/components/apps/*`: the tab, the panel, the dialog, the menu and the diff view; `web/src/components/SecretData.tsx` and `SecretDialog.tsx`.
- `docs/requirements.md`: the HA and SE rows; `docs/architecture.md`: ADR-68 and ADR-69.

### Endpoints

| Method and path | What |
| --- | --- |
| `GET apps` | The records of this cluster the person may see, with their last drift check |
| `POST apps` | Creates a record and submits `helm install` |
| `POST apps/refs` | Lists a repository's branches and tags for the dialog |
| `GET apps/{ns}/{name}` | One record |
| `PUT apps/{ns}/{name}` | Changes the source, values, parameters or token; with `upgrade`, submits `helm upgrade [--install]` |
| `POST apps/{ns}/{name}/upgrade` | Submits `helm upgrade`, moving the ref first when given |
| `DELETE apps/{ns}/{name}` | Submits `helm uninstall`; `?record=1` forgets the source only |
| `GET apps/{ns}/{name}/drift` | Compares the cluster with the manifest, as the person (`?refresh=1` asks again) |
| `GET apps/{ns}/{name}/refs` | The repository's refs, the newer tags, whether the branch moved |
| `GET apps/{ns}/{name}/revisions` | The release's revisions |
| `GET apps/{ns}/{name}/diff?from=N&to=M` or `?to=source[&ref=&refKind=]` | A unified diff |
| `GET apps/{ns}/{name}/images?revision=N` | The images a revision runs |

Every `apps/…` route is served by the leader replica, where Watson runs.

## Open questions

| Question | Proposal | Status |
| --- | --- | --- |
| The shipped Admin role cannot create workloads, so only a SuperAdmin can install an application; should the full Admin template grow? | Leave the roles as they are until the page replaces the Helm tab; an Admin's install fails at the dry run with Kubernetes' own refusal | Open |
| When does the Helm tab go? | Once the Helm1 panel has the pods, resources and notes the Helm tab shows | Open |
| Charts with dependencies in chart repositories | Vendor them in the repository (`helm dependency build` committed), or add a `helm dependency build` step later | Open |

## As built

| Area | As built | Differs from the requirements |
| --- | --- | --- |
| Table and panel | `AppsTab` joins the live `helm` view with the records; with a release open the table folds into the Helm tab's side list, and the panel is `ReleaseBody` (shared with the Helm tab) with Overview and Images added | A record whose install failed is a *not installed* row with Install, values, Change source and Forget |
| Changes | Watson commands with `--git/--path/--ref/--tag/--app`; the person's click is the approval unless the two-person rule applies | — |
| Drift | Subset comparison, per object, cached two minutes per person | Lists are compared element by element as far as the manifest's goes; an element the cluster added is not drift |
| Secret editing | `SecretData` in the object drawer, `SecretDialog` on the Secrets tab; reveal and edit gated by `secrets/config` | Immutable Secrets are refused before Kubernetes would refuse them |

## Your review

| Date | Note |
| --- | --- |
| | |
