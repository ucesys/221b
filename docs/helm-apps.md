# Helm applications and Secret editing: requirements

Phase 10, asked for on October 27, 2026: a Helm page that lists every release in one table and deploys charts from a git repository, and Secret editing for Admins and SuperAdmins. The page was built as **Helm1** beside the old Helm tab and replaced it on October 28, 2026, keeping the tab id and the addresses. The requirement rows are in [requirements.md](requirements.md#phase-10-helm-applications-and-secret-editing) (HA-01 to HA-20, SE-01 to SE-06); the decisions are ADR-68 and ADR-69 in [architecture.md](architecture.md).

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
- Rollback to a chosen revision: since 0.10.1 each revision in History has a wrench with Diff… and Roll back to this revision…, the latter a `helm rollback` card for Watson (M-03); Upgrade… redeploys from the source.

## Approach

| Decision | Choice | Why |
| --- | --- | --- |
| Where the chart comes from | The console clones the repository at the ref, shallow and in memory, with go-git, within limits on what is sent, unpacked and written out (HA-04) | The image has no git; nothing touches disk; a private repository is fetched with a credential of Settings › Credentials chosen by name (0.12.1, CR-11), sent only under its endpoint: a change of repository it does not cover lets it go |
| Who deploys | Watson, as the person: `helm install\|upgrade\|uninstall … --git --path --ref [--tag] --app`; the card pins the commit and the values, and the run deploys those or refuses | The dry run, the card, the two-person rule, the audit log and the output are the ones every change has (ADR-66, ADR-68) |
| What the console keeps | A record per application in a Secret of its namespace, beside the connections | Survives restarts and replicas; the release itself stays Helm's |
| Drift | Each object of the manifest read as the person and compared with what the manifest sets; kept two minutes | Catches changes by hand without caching anything, and without showing values |
| Comparing | Manifests rendered on the console (`helm template`-style, `lookup` finds nothing; an upgrade takes the release's next revision and the cluster's version and APIs) and diffed object by object, every object masked as release values are and a Secret's values shown as sizes | No Secret read for a comparison; the same render serves the card's impact |
| Rendering | Every render of a chart before approval (the card's impact, a dry run, the diff, the source check on save, the check before a deploy) runs in a child process, the console's binary started again as `221b render-chart`: 256 MiB of address space over its start, 60 s of CPU, killed after 60 s, two at a time, 32 MB of output, and the loop budget as an early stop | A chart's templates are untrusted code; a render past the limits ends with "its templates used more memory or time than allowed" and the console goes on |
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

```yaml
apps:
  # The hosts charts are fetched from: git repositories, Helm repositories
  # and OCI registries. Empty (the default) allows any host but the
  # console's own pod and node and the clouds' metadata services.
  allowedHosts:
    - git.example.com
    - "*.charts.example.com"    # any name under charts.example.com
    - registry.example.com:5443 # that port only
```

With `apps.allowedHosts` set, a git source, a Helm repository or an OCI registry on any other host is refused when it is saved (an application's source, Settings › Repositories, a restored backup) and again when the console connects, each redirect included. An entry is a host name, or `*.domain` for any name under that domain (not the domain itself); either may end in `:port` to allow that port only.

Fetching a chart takes the rights of managing applications. In Watson, `helm install` and `helm upgrade` with `--git` or a repository's chart, a dry run included, are for an Admin or SuperAdmin whom Kubernetes lets manage applications in the namespace, asked before anything is fetched.

When a fetch fails on the way to the repository (the host refuses the connection or does not answer, its name does not resolve, TLS fails, or it answers an unexpected HTTP status), the person is told only that the repository could not be fetched; the cause is in the console's log (`a repository could not be fetched`). A refused token or credentials, and a repository that is not there, are said as such, so the source can be fixed.

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
| `POST apps/refs` | Lists a repository's branches and tags for the dialog, with the `credential` chosen there |
| `GET apps/{ns}/{name}` | One record |
| `PUT apps/{ns}/{name}` | Changes the source, values, parameters or `credential` (an id of Settings › Credentials, `""` for none); with `upgrade`, submits `helm upgrade [--install]`. Another repository the credential does not cover lets it go, and `credentialRemoved` says so |
| `POST apps/{ns}/{name}/upgrade` | Submits `helm upgrade`, moving the ref first when given |
| `DELETE apps/{ns}/{name}` | Submits `helm uninstall`; `?record=1` forgets the source only |
| `GET apps/{ns}/{name}/drift` | Compares the cluster with the manifest, as the person (`?refresh=1` asks again) |
| `GET apps/{ns}/{name}/refs` | The repository's refs, the newer tags, whether the branch moved, with the application's credential |
| `GET apps/{ns}/{name}/inspect` | The charts and values files in the repository at its ref, with the application's credential |
| `GET apps/{ns}/{name}/revisions` | The release's revisions |
| `GET apps/{ns}/{name}/diff?from=N&to=M` or `?to=source[&ref=&refKind=]` | A unified diff |
| `GET apps/{ns}/{name}/images?revision=N` | The images a revision runs, with `checks`: by image, whether a newer one is published (HA-22) |
| `GET helm/newer-images` | The releases the person may see with a newer image, by `namespace/name`; `enabled` false when Settings turned the check off, `pending` the images still being asked about (HA-22) |

Every `apps/…` route is served by the leader replica, where Watson runs. What changes a record, and what uses its credential or values (`inspect`, `refs`, a diff with the source on either side), needs the role and Kubernetes' leave in the namespace (list and create Secrets), asked before the record is read (HA-02).

## Credentials (0.12.1)

A git repository, a Helm repository or an OCI registry of Settings › Repositories signs in with a credential of **Settings › Credentials** (CR-01 to CR-14), chosen by name: the picker offers those that serve the use and cover the address, preselects the only one, and makes you choose among several; *None (public)* is always there. A git repository takes a token sent as Basic (`x-access-token` for GitHub, `oauth2` for GitLab) or Bearer, a user and password, a GitHub App (the console mints its installation tokens) or an OAuth2 client; a Helm repository or OCI registry a user and password, or a token sent as Basic or Bearer (a registry exchanges it at its token service, as Helm does). The credential is sent only to addresses under its endpoint; the record keeps its id, never the secret.

Watson's typed `helm install|upgrade NAME --git URL …` fetches with `--credential NAME` when given, else with the application's own credential when a record is kept for that repository, else with the one credential that covers the repository; when several do, it refuses and asks for `--credential`.

Before 0.12.1 an application kept a token of its own, and a repository its own password or token. The upgrade made credentials of them (docs/upgrading.md, 0.12.1); a record not moved yet keeps using its own until it is, and the Setup card says so.

## Repositories (0.12.1)

**Settings › Repositories** (RP-01 to RP-06; Settings › Applications before, whose address leads there) lists every repository applications and Mycroft's sources come from: git repositories, Helm repositories and OCI registries, each with a short name, its address, its credential (or *None (public)*), *Used by* and its last test; a git repository may have a default branch. SuperAdmins add, change, rename, test and remove them; those who manage applications see the list and pick from it.

- **An application's git source names a repository.** The new application and a release's Setup card pick a git repository from the list (a SuperAdmin adds one inline with *Add a repository…*); the application keeps its own chart path and branch or tag. The record holds `source.repository` and, as before, the address, which is read from the repository each time: change the address or the credential once, on the repository, and every application that names it follows. A credential sent beside a listed repository is refused; an address sent without one that the list has is recorded as that repository.
- **Mycroft's git sources** pick a repository the same way and keep their branch and directory.
- **Watson** takes the name: `helm install NAME --git REPOSITORY --path P --ref R -n NS`, fetched with the repository's credential (`--credential` other than its own is refused). A full address still works; one the list has is recorded as its repository. The Helm tab's own lines name the repository.
- **In use, not removed.** `DELETE /api/v1/helm-repositories/{name}` answers 409 naming the applications and sources that use it, and the audit log keeps the refusal.
- **The upgrade** made one git repository for every address applications and git sources typed before 0.12.1, with the credential they used, and switched them to it (docs/upgrading.md); the page lists what it made, to be renamed (`POST /api/v1/helm-repositories/{name}/rename`, followed by what names it), until dismissed.

## Open questions

| Question | Proposal | Status |
| --- | --- | --- |
| The shipped Admin role cannot create workloads, so only a SuperAdmin can install an application; should the full Admin template grow? | Leave the roles as they are until the page replaces the Helm tab; an Admin's install fails when it runs, with Kubernetes' own refusal | Open |
| When does the Helm tab go? | Once the Helm1 panel has the pods, resources and notes the Helm tab shows | Done on October 28, 2026: the Helm1 page became the Helm tab |
| Charts with dependencies in chart repositories | Vendor them in the repository (`helm dependency build` committed), or add a `helm dependency build` step later | Open |

## As built

| Area | As built | Differs from the requirements |
| --- | --- | --- |
| Table and panel | `AppsTab` joins the live `helm` view with the records; with a release open the table folds into the Helm tab's side list, and the panel is `ReleaseBody` (shared with the Helm tab) with Overview and Images added | A record whose install failed is a *not installed* row with Install, values, Change source and Forget |
| Changes | Watson commands with `--git/--path/--ref/--tag/--app`; the person's click is the approval unless the two-person rule applies | — |
| Drift | Subset comparison, per object, cached two minutes per person; each changed object carries a unified diff of the manifest's fields against the cluster's, shown in the *What drifted* dialog (R2-10) | Lists are compared element by element as far as the manifest's goes; an element the cluster added is not drift |
| Values | `apps.Compose`: the chart's `values.yaml` (unless the record ignores it), the record's second values file read from the checkout, the Values tab, then the parameters and the fleet's deploy label as `nodeSelector` (R2-08, R2-09, R2-11) | The second file must be in the chart's directory (one subdirectory down at most); `..` and absolute paths are refused |
| Secret editing | `SecretData` in the object drawer, `SecretDialog` on the Secrets tab; reveal and edit gated by `secrets/config` | Immutable Secrets are refused before Kubernetes would refuse them |

## Your review

| Date | Note |
| --- | --- |
| | |

## Newer images

Asked on November 13, 2026 (HA-21 to HA-23, ADR-154). For every image the newest deployed revision of a release runs, the console looks in the image's registry for a newer one, and says so:

- **In the release's panel**, at the top: *A newer image is available: ghcr.io/acme/api:1.2.4 (running 1.2.3)*, the other images with a newer one after it, and a link to the Images tab. The notice informs; the image changes with the chart or its values, through an upgrade.
- **On the Images tab**, beside each tag: *1.2.4 available*, *checking…* while the registry is asked, or *could not check* with the reason in its tip.
- **In the table**, a *newer image* mark beside the App version, with the images in its tip; the export has a Newer images column.

**What is newer.** The newest tag of the running tag's form: the same prefix (`v`), as many numbers, and the same suffix but for its last number, so `1.2.3-alpine` is compared with `1.2.9-alpine` and `1.25.3-debian-12-r4` with `1.25.4-debian-12-r0`, never with `1.26.0-debian-11-r0` or `1.2.4`. Pre-releases (`-rc.1`, `-beta`) count only when a pre-release runs, and then the release they lead to counts too. `latest`, commit hashes and date stamps are not versions. A tag that is not a version but is pinned by digest (`nginx:stable@sha256:…`) is newer when the tag points at another digest now. `latest` without a digest, and an image pinned by digest alone, are not checked: nothing says what runs.

**How.** The leader asks, in the background, over the OCI distribution API: the tag list (`/v2/<repository>/tags/list`, following its pages) or the tag's manifest digest (a `HEAD`). Docker Hub's images are asked at `registry-1.docker.io`, official ones as `library/<name>`. A registry that challenges for a Bearer token (ghcr.io, Docker Hub, quay.io, Harbor and most others) is given one asked for **without credentials**; a Basic challenge, or a token service that refuses, is *could not check*. The console never reads the workloads' image pull secrets, so private repositories are not checked. Answers are kept per repository for six hours (a failure for one hour), at most four registries are asked at once, twenty seconds each, and no request waits for one: the page asks again while answers are pending, then every ten minutes. A standby replica asks no registry (ADR-70). Only HTTPS registries are asked.

**Network.** The registries are reached over HTTPS (443) through the proxy in `HTTPS_PROXY` when set. The chart's network policy allows that by default: `sherlock.egress` (on) opens `networkPolicy.outboundPorts` to any address. With `sherlock.egress: false`, or `networkPolicy.outboundCIDRs` narrowed, list the registries' addresses there, or under `helmRepositories.egressCIDRs` with the chart registries; otherwise the checks say *could not check* and nothing else. An air-gapped cluster sees no notice and no error.

**Off.** Settings › Newer images › Off: no registry is asked, and the Helm tab shows nothing of it.

## Who sees what

Admins and Viewers see a release, or an application the console keeps, in a namespace they reach. They also see an application an admin created when that admin had the same namespace access, so an application whose namespace does not exist yet is not lost to the people it belongs to; that view leaves its values, parameters and commands out, and changing it still takes Kubernetes' leave in its namespace. An application saved before November 2, 2026 records no creator's access, so only the first rule applies to it.

## Adopting a release, and what a deploy removes

Asked on November 17, 2026, after an upgrade of the console's own release reset it to the chart's defaults: the release was adopted as an application with a few LDAP keys in its values, and `helm upgrade --app` deploys **only** the application's values, so every value the release had (service type, allowed hosts, image tag, replicas, node selector, network policy) went back to the chart's default and the console went down (HA-40 to HA-43).

**Adopting keeps the release's values.** Setting the source of a release the console did not install (*Set its source*) makes the application's values file from the release's current user-supplied values, what `helm get values` shows for its newest revision. Values sent with that first save are laid over them, key by key, theirs winning; they never replace them. Later saves replace the values file as before. The record notes the revision (`adoptedRevision`), the Values tab says *These values came from the release … at revision N*, and the audit record of the save carries `values_from_revision=N`. A release with no values, or no release yet, starts with what was typed.

**Credentials stay masked.** The values file is kept in the application's Secret (`<prefix>-app-<id>`, beside the cluster connections) as before. The page receives it with credentials masked as the release's Values tab masks them (`kube.MaskValues`), with the masked keys in `valuesMasked`; a file with nothing to mask is sent as typed, comments and all. A masked placeholder sent back keeps the value saved at its place, in maps and lists alike, so the placeholder is never saved as a value; one with nothing saved at its place is refused (400) and the record is left as it was.

**Every deploy card lists what it removes or changes.** An upgrade card (Redeploy, Switch, an application's command, or `helm upgrade` typed in Watson) compares the values it will deploy with the release's current user-supplied values and adds, by key and never by value, *Removes N values the release has now (helm get values): … They fall back to the chart's defaults.* and *Changes N values the release has now: …*, at most 20 keys each and *and N more*. A map removed whole is one key (`ingress`); lists compare whole. A card that removes values has the warning tone (`plan.tone: warn`, a heavier border).

**The console's own release.** The chart sets `SELF_RELEASE` to `<namespace>/<release>` (flag `-self-release`); it names a release in the local cluster. A Watson card that installs, upgrades, rolls back or uninstalls that release says first, in red, *This is the release that runs this console: if it fails, the console goes down.* (`plan.alert`, `plan.tone: danger`), and its panel in the Helm tab shows the same callout. An upgrade of it that would remove values is **refused at approval** (rule *own release*): put the values it lists in the application's values, then send the command again. A durable card (an agent's proposal) carries the alert as its first impact line and is refused the same way when it runs. A rollback or uninstall of it is a danger but may be approved.
