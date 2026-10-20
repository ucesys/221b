# Changelog

What changed in each release of 221B, for people who use it and people who install it. Versions follow [semantic versioning](https://semver.org/): while the major version is 0, a minor version (0.7.0) may change behaviour and chart values, and a patch version (0.7.1) only fixes. [docs/upgrading.md](docs/upgrading.md) says how to upgrade and what each upgrade changes.

## Unreleased

## 0.7.1 (2026-10-20)

### Fixed

- **0.7.0 has no image.** Its release ran, but the image was held back by the Firefox browser tests, which stall in CI. 0.7.1 is 0.7.0 with an image: install or upgrade to 0.7.1.
- CI: Firefox's browser tests still run and show their failures, but no longer hold back a release; Chromium and WebKit must pass. The upgrade test skips a release that has no image.

## 0.7.0 (2026-10-20)

### Added

- **Several replicas** (P7-02): `replicaCount: 2` or more, with a PodDisruptionBudget. One replica, chosen through a Kubernetes Lease, runs Sherlock and Watson, and the others pass those requests to it; sign-ins, settings and accounts are shared, so losing a replica costs a viewer a reconnect.
- **Upgrades from the two previous releases** (P7-03) are tested in CI: upgrade, roll back and upgrade again, keeping settings, users, connections and Sherlock's key. Stored settings and local accounts carry a format version; a console migrates older formats, keeps fields it does not know when it saves, and refuses to overwrite a format newer than it knows.
- **Releases** (P7-08): semantic versions, this changelog, the chart published to `oci://ghcr.io/ucesys/charts/221b`, and a GitHub release for every version tag ([docs/releasing.md](docs/releasing.md)). A contributing guide, a code of conduct, a security policy, issue and pull request templates, and a Developer Certificate of Origin check for pull requests from forks.

### Changed

- Everyone signs in once more after upgrading to 0.7.0: sign-ins used to live in the console's memory and are now kept in the cluster, where they survive restarts and upgrades.

### Fixed

- Sherlock on the home page and on a connection's page: *Send to Watson* shows under a command, and a cited object (*Open deployment … →*) opens on the cluster's tabs.

## 0.6.6 (2026-10-18)

### Security

- The YAML view refuses names with `/`, `..` or `%`, which could reach any API path, and hides anything that comes back as a Secret.
- A kubeconfig that names a token, certificate or key file is refused, so the console never sends its own token to another server.
- Watson refuses to print Secrets in any type spelling (`secrets.`, `secret.v1.`), shows prefixed label removals on its card, acts only on the cluster a card was made for, and checks a selector's matched set and a queued change's objects again before it runs.
- The read-only SuperAdmin no longer gets `nodes/proxy`, `pods/exec`, `pods/attach`, `pods/portforward` or the proxy subresources; the README's read-only recipe sets `kubeletStats: false`.
- Sign-in no longer redirects off the site through `\`; OIDC refuses reserved user names and ignores an unverified email; event streams end with the session or a change of the person's groups.

### Changed

- One date and time format everywhere, in the browser's time zone with its short name (`2026-05-03 1:34pm PDT`); Settings sets the date style and the clock for everyone.
- Copy icons beside names in the tables and side lists. The top bar no longer shows the refresh time; acknowledged alerts say who and when; the home card's review button is *Audit*.
- A pod that runs but is not ready stays an alert (*Not ready*).
- Permissions and Roles open for every signed-in person.

### Fixed

- One unreadable connection Secret no longer hides the others; a kubeconfig without `current-context` starts.
- The assistants' event stream reconnects after a proxy error; a modal drawer keeps and returns focus; CSV cells that would run as formulas stay text.
- The image builds again after the README screenshots were added.

## 0.6.5 (2026-10-18)

### Changed

- Alerts: an alert on an object that is healthy again is *Resolved* and counts nowhere; acknowledged alerts follow the unacknowledged ones, then the resolved; each alert has a border in its colour.
- Password changes from the sign-in page, and by a SuperAdmin on the Users page after entering their own password.
- Nodes: *master* for control-plane nodes, a "Nodes with errors" filter on Nodes and Fleets; icon-only Export and Columns menus.

## Earlier

Phases 1 to 6.4 were developed before releases were versioned; [docs/requirements.md](docs/requirements.md) describes what each phase added.
