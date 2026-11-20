# Releasing 221B

How versions are numbered, how a release is cut, and what CI does with it (P7-08).

## Versions

221B follows [semantic versioning](https://semver.org/). The chart's `version` and `appVersion` are always the same, and the images of a release are tagged with it: chart 0.7.0 runs `ghcr.io/ucesys/221b:0.7.0`, and from 0.13.0 its bundled database runs `ghcr.io/ucesys/221b-postgres` of the same version (`database.bundled.image`, DB-69).

- **Patch** (0.7.1): fixes only. No change to chart values, to stored formats or to what anyone can see or do beyond the fix.
- **Minor** (0.8.0): new features, and while the major version is 0, changes in behaviour or chart values, each listed in [CHANGELOG.md](../CHANGELOG.md) and in [docs/upgrading.md](upgrading.md). A renamed value keeps working under its old name for at least one minor release.
- **Major** (1.0.0): from 1.0, anything that breaks an existing installation's values or behaviour.

Every release upgrades from each of the two releases before it; CI tests this ([upgrading.md](upgrading.md#which-upgrades-are-tested)).

Releases so far: **0.6.5** (Phase 6.5, `f789452`), **0.6.6** (Phase 6.6, `ad0b568`), **0.7.0** (Phase 7, released without an image), **0.7.1** (0.7.0 with its image) **0.9.1** (Phases 8 and 9 and the Phase 9.1 review; there is no 0.8.x or 0.9.0) , **0.10.0** (Phase 10: Helm applications from git, Secret editing, every change through Watson) **0.10.1** (Phase 10.1: the review rounds after 0.10.0, fleets on the Nodes tab, standing approvals, Sherlock notes, values files, drift explained) **0.11.1** (Phase 11: Automation and SSH to nodes, the review fixes after it, signed releases, and the 0.10.2 work; there is no 0.10.2, and 0.11.0 was tagged but never published) and **0.12.0** (Phase 12: AI agents, the AIOps area, hosts outside Kubernetes, backups to S3 and Helm repositories, and the fixes of its two reviews), **0.12.1** (Phase 12.1: email about cases, Sherlock on every view and handing work to the agents, LDAP keeping local accounts, safer Helm upgrades, agents' instructions with tests, history and limits per provider, Credentials and Repositories, live and web site knowledge sources, Administration in tabs, and hardening). Earlier phases were not released as versions.

## Cut a release

1. **On a branch, bump the version** in `deploy/helm/221b/Chart.yaml` (both `version` and `appVersion`) and in `web/package.json`. A phase branch bumps it when it starts (the minor for a phase, the patch for fixes), so every build from the branch, and the console's About and the Helm tab, show the version to come; the changelog section is `## 0.10.0 (unreleased)` until the release puts the date in.
2. **Write the notes.** In [CHANGELOG.md](../CHANGELOG.md), rename *Unreleased* to `## 0.8.0 (YYYY-MM-DD)` and start a new empty *Unreleased* above it. Add the release's section to [docs/upgrading.md](upgrading.md) if an upgrade changes anything an operator must know.
3. **Merge** it through a pull request; CI must pass, including the upgrade tests.
4. **Tag the merge commit** on main and push the tag:

   ```sh
   git checkout main && git pull
   git tag -a v0.8.0 -m "221B 0.8.0"
   git push origin v0.8.0
   ```

5. **CI** (`.github/workflows/ci.yml`) builds the tag, runs every check on that build, including the upgrade from the two releases before it, and publishes the images as `ghcr.io/ucesys/221b:0.8.0` and, from 0.13.0, `ghcr.io/ucesys/221b-postgres:0.8.0`: for each, linux/amd64 is the very image the checks ran on, and linux/arm64, which they cannot run, is built in the publish job from the same pinned base images and scanned (the database's under emulation, since its build installs Debian's packages). A release is never a prebuilt image. Both images are scanned with Grype: the console's, distroless, fails on any high or critical finding; the database's, Debian's PostgreSQL, fails on a high or critical finding that has a fix, and lists the others, which Debian marks as not to be fixed or not fixed yet. Base images are pinned in `hack/base-images.env`: those of both Dockerfiles, the BuildKit frontend, and the PostgreSQL the Go tests, the browser tests and the screenshots run against (`POSTGRES_IMAGE`, written into the workflows' services). `hack/pin-base-images.sh` moves them, and `hack/check-base-images.sh`, in the chart job, fails when a tag and its pin disagree or a base image has no pin. A build on a laptop follows the tags in the Dockerfiles and the Makefile.
6. **The release workflow** (`.github/workflows/release.yml`) checks that the tag matches the chart's version, that the chart runs both images of that version by default, and that CHANGELOG.md has its section, waits up to two hours for CI to publish both images (so a failed run stops the release; rerun CI's failed jobs within the wait, or this workflow after it), then pushes the chart to `oci://ghcr.io/ucesys/charts/221b:0.8.0`, signs it and attests its provenance, builds, signs and attests the `221b` command, and makes a GitHub release with the section as its notes and the chart, the command, its signed checksums and its SBOM attached. CI's publish job has already signed both images and attested their provenance and SBOMs ([Verify a release](#verify-a-release)).
7. **Check** the release page, verify the signatures once ([Verify a release](#verify-a-release)), then pull the images and install the chart from the registry once (`helm upgrade --install … --version 0.8.0`).

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

## Verify a release

Every image CI publishes, the console's and from 0.13.0 the bundled database's, and a release's chart and `221b` command, are signed with [cosign](https://docs.sigstore.dev/) keyless (P7-05, #79): no key is kept anywhere; the certificate names this repository's workflow and the ref it ran for, and the signature is in Sigstore's public transparency log. The images and the chart also carry SLSA build provenance, and each image an SPDX SBOM, as GitHub attestations kept beside them in the registry. A release's images and chart are never replaced: a rerun keeps what was published and only signs it again.

The image (the database's is the same, as `ghcr.io/ucesys/221b-postgres:0.13.0`, and so is the chart, as `ghcr.io/ucesys/charts/221b:0.12.1`):

```sh
cosign verify ghcr.io/ucesys/221b:0.12.1 \
  --certificate-identity-regexp '^https://github.com/ucesys/221b/\.github/workflows/ci\.yml@refs/tags/v0\.12\.1$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify oci://ghcr.io/ucesys/221b:0.12.1 --repo ucesys/221b                   # provenance
gh attestation verify oci://ghcr.io/ucesys/221b:0.12.1 --repo ucesys/221b --predicate-type https://spdx.dev/Document/v2.3   # the SBOM
```

For the chart, the identity is `release.yml` instead of `ci.yml`. The `221b` command: check the signature on `SHA256SUMS`, then each file against it.

```sh
cosign verify-blob SHA256SUMS --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github.com/ucesys/221b/\.github/workflows/release\.yml@refs/tags/v0\.12\.1$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum -c SHA256SUMS --ignore-missing
gh attestation verify 221b-linux-amd64 --repo ucesys/221b
```

The release also has the command's SBOM, `sbom-221b-command.spdx.json`. Pull requests' and main's images are signed and attested the same way, under their own refs. A build on a laptop is neither, and needs none of this.
