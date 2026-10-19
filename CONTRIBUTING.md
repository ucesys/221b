# Contributing to 221B

Thank you for helping. This guide says how to set up, what a change needs before it is merged, and how to sign off your work.

## Before you start

- **A bug:** open an issue with the bug template, or fix it and open a pull request that says how to see the bug.
- **A feature or a change in behaviour:** open an issue first. 221B is built against written requirements ([docs/requirements.md](docs/requirements.md)), and a change that adds or alters one should be agreed before the code is written.
- **A security problem:** do not open an issue. Follow [SECURITY.md](SECURITY.md).

## Set up

You need Go 1.26, Node.js 24 and make. Docker, kind, kubectl and helm are needed only for the image and the kind tests.

```sh
make dev-sim     # the console on http://127.0.0.1:8080 against a simulated cluster
make test        # Go and browser unit tests
make lint        # gofmt, go vet, tsc and eslint
make e2e-sim     # browser end-to-end tests against the simulator
make chart-lint  # lint and render the Helm chart
make help        # everything else
```

The [README](README.md#develop) has more: running against a real cluster, the browser app with hot reload, and the simulator.

## What a change needs

- **Tests.** Go code has unit tests next to it and passes `go test -race ./...`. Browser code has unit tests next to it (`*.test.ts`), and a change a person can see has a browser test in `web/e2e`. A fix comes with a test that fails without it.
- **Lint.** `make lint` is clean. Go is formatted by gofmt; TypeScript passes `tsc` and `eslint`.
- **Requirements.** A change that meets or changes a requirement names it (for example `P3-02` or `RB-41`) in the code comment and the pull request, and updates [docs/requirements.md](docs/requirements.md). A design decision gets a record in [docs/architecture.md](docs/architecture.md).
- **Security stance.** 221B never shows, logs or exports a Secret's value; every read and write is made as the person asking (Kubernetes impersonation); nothing an AI model writes runs without a person's approval. A change that touches any of these says so in the pull request.
- **Upgrades.** A change to what the server keeps in the cluster (the settings ConfigMap, connection Secrets, local accounts) or to chart values keeps upgrades from the two previous releases working ([docs/upgrading.md](docs/upgrading.md)). Rename a value with a fallback for the old name rather than removing it.
- **Release notes.** Add a line under *Unreleased* in [CHANGELOG.md](CHANGELOG.md) for anything a user or an operator would notice.
- **Writing.** Comments and docs are plain English sentences, written for someone new to the code.

CI runs all of this on every pull request, plus the kind tests on every Kubernetes minor release still maintained upstream and the 500-node scale test.

## Sign off your work

Every commit carries a [Developer Certificate of Origin](https://developercertificate.org/) sign-off, which says you wrote the change, or otherwise have the right to submit it under the project's license (Apache 2.0):

```sh
git commit -s -m "Nodes: sort by uptime"
```

`-s` adds a `Signed-off-by: Your Name <you@example.com>` line with the name and email in your git config. CI's *Sign-off* check fails a pull request with a commit that is not signed off by its author; `git rebase --signoff main` signs off commits already made.

## Pull requests

- Keep a pull request to one change. Small ones are reviewed sooner.
- Say what changes for a person using 221B, and how you tested it. Screenshots help for anything visible.
- A maintainer reviews it; CI must pass. Maintainers merge with a merge commit, so your commits stay as you wrote them.

## Code of conduct

Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).
