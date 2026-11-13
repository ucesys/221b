# Getting started

Phase 12 (0.12.0). The requirements are *Phase 12: Getting started* (GS-01 to GS-14) in [requirements.md](requirements.md); the decisions ADR-103 and ADR-104 in [architecture.md](architecture.md).

Getting started is a checklist of setting the console up. A SuperAdmin sees it on the home page until every step is done or dismissed, and always in **Administration › Settings › Getting started**. The console checks each step against what it has now, every time the list is read: nobody ticks a step by hand, and a step that stops being true opens again. A step that does not apply here (no SSH, say) can be dismissed, and brought back; both are in the audit log (`setup.dismiss`).

The checklist works without AI: the first steps come before any model is set up. Once a provider works, Sherlock helps with the rest (its *setup skill*, below).

Passwords, keys and tokens are always typed into their own field on the screen, which keeps them in a Kubernetes Secret. Neither the checklist nor Sherlock asks for one in a chat.

## The steps

### 1. Sign-in

Done when people sign in through OIDC or LDAP, or with local users the console keeps: those in the chart's values, and those added on **Administration › Users** while the console keeps them in a Secret. Open when sign-in is off (`auth.mode: none`), when everyone holds the anonymous role and SSH, Automation and Secret values stay off; or when users added in the console are kept in memory only. Sign-in comes first because roles, approvals and the audit log all name a person. See [roles-and-users.md](roles-and-users.md).

### 2. Clusters

Done when the console has a connected cluster. The cluster it runs in counts; add the others with **Add a cluster** on the home page, pasting a token or kubeconfig into the dialog. Every later step works on the clusters the console knows. See the README's *Set up a remote cluster*.

### 3. AI provider and default model

Done when the default provider has its credential and a default model, on **Administration › Settings › Providers**. The key is typed into the provider's credential field; the console keeps it in a Secret and shows only whether it is set. Other providers can be added there for agents that should use another model.

### 4. Agents

Done when Sherlock is on for at least one cluster and the overall limits (a day's and a month's spend) are set, on **Administration › Settings › Agents**. Choices worth making here:

- **Which model.** Sherlock and the Change planner do well on a balanced model; the Troubleshooter on the strongest reasoning model; the Auditor on a strong model with a long context, since it reads a whole cluster's review at once; the Summarizer on the cheapest. The Script reviewer runs on another provider than the Automation author, so its second opinion is independent.
- **What acting on its own costs.** A job runs an agent with no person asking; every run is charged to the agent's own budget and to the job's daily budget, and stops when either is spent. Start small, read what the first runs cost on **AIOps › Jobs › Runs**, then raise it.
- **Which role an agent on its own reads with.** An agent asked by a person reads as that person. On its own it reads as a reserved identity with the role a SuperAdmin picks in **Settings › Jobs**: Viewer unless a job needs to read more, and never a role that may change anything.

### 5. Knowledge

Done when the wiki has a store (a git repository or an S3 bucket) and at least one source besides the console itself is connected: a Confluence space, a git repository of runbooks, or an uploaded document, on **Administration › Settings › Knowledge**. A source's token is typed into its own field. Mycroft reads a new source at once and proposes the hosts and the groups of hosts it lists, with their labels, as cards in **AIOps › Inbox** (its *first import*); approved hosts are then paired in the Add host flow. That is why knowledge comes before fleets and hosts.

### 6. Jobs and notifications

Done when an agent's job is on (the nightly audit, an alert's diagnosis, the report triage) on **AIOps › Jobs**, and a notification channel is on with its address set on **Administration › Settings › Watson**. A webhook's address is typed into its own field and kept in a Secret. Jobs only read; what they propose waits for a person in Cases.

#### Email about cases

Optional, and not a step of the checklist. On **Administration › Settings › Email** a SuperAdmin gives the SMTP server, its port and security (STARTTLS, TLS, or none for a relay inside the network that needs no sign-in), a user name and password, and the From address, saves, and presses **Send test email** (to their own address unless another is typed). Email is on only once that test got through, and any change of those settings turns it off until the next test. From then on the people involved in a case (who reported or opened it, its assignee, who follows it, wrote in its conversation or approved its fix) are emailed when it is opened and when its status, assignee or priority changes or someone writes in it: one email per case and minute, never about their own changes, with a link to the case and to **AIOps › Inbox › I'm involved**, every case they are involved in. The links start with the console's address set in Settings › Watson. The password is kept in the Secret `<release>-cases` and never shown again. With the chart's network policy on, the console reaches the SMTP server only once its address is listed under `networkPolicy.outboundCIDRs` and its port (587, 465 or 25) under `networkPolicy.outboundPorts`.

### 7. Fleets

Done when any cluster has a fleet. Fleets are made on the **Nodes** tab's fleet list from the labels and taints nodes share; Automation runs on fleets, and the views filter by them.

### 8. SSH and hosts outside Kubernetes

Done when a node is paired for SSH, or a host outside Kubernetes is paired, on the **Nodes** tab (**Pair for SSH**, **Add host**). The node's password or key is typed into the pairing dialog and used once; the console's own certificate authority signs in afterwards. See [toolbox.md](toolbox.md) and *Hosts outside Kubernetes* in [automation.md](automation.md).

### 9. Backups

Done when a backup was downloaded from **Administration › Backup**, or a scheduled backup to S3 worked. Download one first, then schedule them to S3 there; the access key is typed into its own field.

#### The backup key

Every backup carries the console's secrets (connections' credentials, tokens, API keys, notification addresses, local users' password hashes, the SSH authority's key) sealed with this install's backup key, AES-256-GCM, never in clear (ADR-137). The console makes the key at its first start in the Kubernetes Secret `<release>-backup-key` of its namespace, keeps it when the release is uninstalled, and never shows it, logs it or puts it in a backup: the page shows only its fingerprint. A SuperAdmin keeps a copy, apart from the backups:

```sh
kubectl -n <namespace> get secret <release>-backup-key -o jsonpath='{.data.key}' | base64 -d
```

That prints 64 hexadecimal characters. **A lost key means the backups' secrets cannot be recovered**: a backup still restores everything else, and lists each secret to set again with the page and field that take it. To restore on a new install with the secrets, put the key there first, replacing the one the console made (it is read again for every restore, so no restart is needed):

```sh
kubectl -n <namespace> create secret generic <release>-backup-key --from-literal=key=<the 64 characters> \
  --dry-run=client -o yaml | kubectl apply -f -
```

A restore whose secrets were sealed with another key asks *Restore the rest without the secrets?* first. With `connections.store=memory` there is no key, and backups hold a placeholder for each secret, as before.

## Sherlock's setup skill

While a step is open, a SuperAdmin's Sherlock has one more part to its instructions and one more read-only tool, `setup_status`, which reads this checklist as the console works it out; the Sherlock panel shows a *setup* tag. It:

- answers **What's next?** from the checklist: what is left, in what order, and why;
- takes the person to the step's screen: a link to it, which field or button to use there, and for the Nodes and Fleets tabs the tab itself when Sherlock may change the view;
- explains choices, such as those of step 4, citing this page and the organization's own best-practice pages in Mycroft's wiki (`ask_knowledge`);
- never touches a secret: it opens the screen and the person types. Pasted into the chat, one is not repeated, and Sherlock says to type it into its field instead.

Once every step is done or dismissed the skill leaves Sherlock's instructions, and the tool goes with it. Other people's Sherlock never has it. Its behaviour is tested by three of Sherlock's shipped test cases (`base-sherlock-setup-*`).
