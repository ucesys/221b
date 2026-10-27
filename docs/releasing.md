# Releasing 221B

How versions are numbered, how a release is cut, and what CI does with it (P7-08).

## Versions

221B follows [semantic versioning](https://semver.org/). The chart's `version` and `appVersion` are always the same, and the image of a release is tagged with it: chart 0.7.0 runs `ghcr.io/ucesys/221b:0.7.0`.

- **Patch** (0.7.1): fixes only. No change to chart values, to stored formats or to what anyone can see or do beyond the fix.
- **Minor** (0.8.0): new features, and while the major version is 0, changes in behaviour or chart values, each listed in [CHANGELOG.md](../CHANGELOG.md) and in [docs/upgrading.md](upgrading.md). A renamed value keeps working under its old name for at least one minor release.
- **Major** (1.0.0): from 1.0, anything that breaks an existing installation's values or behaviour.

Every release upgrades from each of the two releases before it; CI tests this ([upgrading.md](upgrading.md#which-upgrades-are-tested)).

Releases so far: **0.6.5** (Phase 6.5, `f789452`), **0.6.6** (Phase 6.6, `ad0b568`), **0.7.0** (Phase 7, released without an image), **0.7.1** (0.7.0 with its image) and **0.9.1** (Phases 8 and 9 and the Phase 9.1 review; there is no 0.8.x or 0.9.0). Earlier phases were not released as versions.

## Cut a release

1. **On a branch, bump the version** in `deploy/helm/221b/Chart.yaml` (both `version` and `appVersion`) and in `web/package.json`. A phase branch bumps it when it starts (the minor for a phase, the patch for fixes), so every build from the branch, and the console's About and the Helm1 table, show the version to come; the changelog section is `## 0.10.0 (unreleased)` until the release puts the date in.
2. **Write the notes.** In [CHANGELOG.md](../CHANGELOG.md), rename *Unreleased* to `## 0.8.0 (YYYY-MM-DD)` and start a new empty *Unreleased* above it. Add the release's section to [docs/upgrading.md](upgrading.md) if an upgrade changes anything an operator must know.
3. **Merge** it through a pull request; CI must pass, including the upgrade tests.
4. **Tag the merge commit** on main and push the tag:

   ```sh
   git checkout main && git pull
   git tag -a v0.8.0 -m "221B 0.8.0"
   git push origin v0.8.0
   ```

5. **CI** (`.github/workflows/ci.yml`) runs every check on the tag, including the upgrade from the two releases before it, and publishes the image as `ghcr.io/ucesys/221b:0.8.0`.
6. **The release workflow** (`.github/workflows/release.yml`) checks that the tag matches the chart's version and that CHANGELOG.md has its section, pushes the chart to `oci://ghcr.io/ucesys/charts/221b:0.8.0`, and makes a GitHub release with the section as its notes and the chart attached.
7. **Check** the release page, then pull the image and install the chart from the registry once (`helm upgrade --install … --version 0.8.0`).

A tag is never moved or deleted once pushed: a mistake is fixed by the next patch release.

## Patch releases

A fix for the latest release goes to main first. To release it without what else main has gained, cut a branch from the release's tag, pick the fix onto it, and release from the branch:

```sh
git checkout -b release-0.7 v0.7.0
git cherry-pick -x <the fix's commit>
# bump to 0.7.1 in Chart.yaml and web/package.json, add "## 0.7.1" to CHANGELOG.md
git push origin release-0.7      # open a pull request into release-0.7 if you want CI first
git tag -a v0.7.1 -m "221B 0.7.1" && git push origin v0.7.1
```

Add the 0.7.1 notes to CHANGELOG.md on main too. The previous minor gets high and critical security fixes for three months after the next minor is released ([SECURITY.md](../SECURITY.md#supported-versions)).

## What is not done yet

Images and charts are not signed and carry no SBOM or build provenance (P7-05 was left out of Phase 7). Until they are, verify an image by its digest from the release's CI run.
