# Security policy

221B reads, and with approval changes, Kubernetes clusters, and it can send cluster data to an AI model. We take reports seriously and thank everyone who makes one.

## Report a vulnerability

**Please do not open a public issue.** Report it privately through GitHub: on the repository's **Security** tab choose **Report a vulnerability** ([direct link](https://github.com/ucesys/221b/security/advisories/new)). Only the maintainers see the report.

Please include:

- what an attacker can do, and what they need first (a session, a role, network access);
- the version (the image tag, or `version` in the console's log), and the chart values that matter;
- steps to reproduce it, or a proof of concept.

## What happens next

| When | What |
|---|---|
| 3 working days | We confirm we have the report. |
| 10 working days | We say whether we can reproduce it, and how severe we think it is. |
| 90 days at most | A fixed release is published, with a GitHub security advisory crediting you unless you would rather not be named. |

We will tell you before anything is published, and we ask you to keep the details private until then.

## Supported versions

Fixes go into a new patch release of the latest minor version. The previous minor version gets fixes for high and critical problems for three months after the next one is released.

| Version | Supported |
|---|---|
| The latest minor (0.9.x) | Yes |
| The previous minor (0.7.x) | High and critical fixes, for three months after 0.9.1 |
| Older | No |

## Especially interesting

- Seeing or changing what Kubernetes RBAC would not let the person signed in see or change (impersonation, the per-person filtering of the live data, the YAML and log views).
- A Secret's value reaching a browser, an export, a log, Sherlock or the AI model.
- Watson running a change without approval, a change other than the one approved, or on another cluster.
- Sign-in: session handling, OIDC, LDAP, local passwords, the first-run page.
- Anything Sherlock's model output can do beyond changing the asking person's own view.

## Not in scope

Findings that need the Kubernetes credentials 221B itself holds, or admin access to the cluster it runs in; denial of service by an authenticated SuperAdmin; reports from automated scanners without a demonstrated impact.
