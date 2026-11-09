# Prompt sources

Nov 5, 2026 · Phase 12 (0.12.0) · the record the AI agents design asks for under *Prompts and sources*.

Every agent's fixed instructions are written in our own words, from 221B's tool list, guardrails and ADRs: one file per agent in `server/internal/ai/prompts/` (the design named `server/internal/agents/prompts/`; the package is `ai`), plus `common.md` for the rules every agent keeps, `setup.md` for Sherlock's setup skill, `cases.md` for its Cases skill and `sherlockCompact.md` for its compact form on a model with a small context (Phase 12, wave 5: a short form of its core and of `common.md`). Sherlock's own core is `corePrompt` in `server/internal/sherlock/prompt.go`, and Mycroft's wiki-writing instructions are `syncPrompt` in `server/internal/knowledge/sync.go`. The projects below gave us ideas, techniques and runbook knowledge; ideas and facts carry no license duty. **No text is copied from any of them**: no prompt file carries a `source:` comment, and the repository needs no `NOTICE` for them.

## How a source is recorded

1. Each source we read for a prompt has a row below: the repository or site, the commit or version we read, its license at that commit, and what we take.
2. By default we read the source and write our own version.
3. Text is copied only from Apache-2.0, MIT or BSD code, or CC BY 4.0 documentation. The copy keeps its copyright line, the source goes into a `NOTICE` file at the repository's root (made with the first copy), and the copied part is marked in the prompt file with a comment on one line that names the row: `<!-- source: k8sgpt analyzers -->`. A prompt file's comments never reach a model (`promptFile` takes them out).
4. Never from GPL, AGPL, LGPL, MPL, CC BY-SA or CC BY-NC sources, from repositories with no license, or from prompts leaked out of commercial products.
5. `hack/check-prompt-sources.sh` (run in CI with the chart's checks, and as `TestEveryPromptSourceIsRecorded` in `server/internal/ai`) fails when a `source:` comment names no row here or a row whose commit is still to be recorded, or when a row has no license. It reads only these files: no network.
6. Before a prompt merges, it is reviewed for secrets, internal host names and customer names (X-01).

Where the commit read was not written down when the prompt was drafted, the row says *to be recorded*: it is filled in from the reader's notes before any text from that source is copied, and a copy is refused until it is.

## Sources

| Source | Repository or site | Commit or version read | License at that commit | What we take, and where |
| --- | --- | --- | --- | --- |
| Prometheus kubernetes-mixin runbooks | github.com/kubernetes-monitoring/kubernetes-mixin (runbook.md) | to be recorded | Apache-2.0 | What to check, in order, for the common alerts (crash loops, pods not ready, nodes not ready, volumes filling): the order of the layers in the Troubleshooter's instructions (`troubleshooter.md`) and in Sherlock's built-in steps. Ideas only. |
| HolmesGPT | github.com/robusta-dev/holmesgpt | to be recorded | MIT | How a runbook names its investigation steps, and an investigation that reads before it concludes and quotes the decisive line: the Troubleshooter's "How you work" and "What you return". Ideas only. |
| k8sgpt analyzers | github.com/k8sgpt-ai/k8sgpt (pkg/analyzer) | to be recorded | Apache-2.0 | Which resource problems are worth detecting and how to explain each in a sentence: the Auditor's areas and the shape of a finding (how many, an example, why it matters, the fix). Ideas only. |
| kubectl-ai | github.com/GoogleCloudPlatform/kubectl-ai | to be recorded | Apache-2.0 | How a Kubernetes agent plans and calls tools before it answers: the Change planner's read-first rule and its use of check_command before a command is given. Ideas only. |
| Kubernetes documentation | github.com/kubernetes/website (content/en/docs) | to be recorded | CC BY 4.0 | Best-practice facts (Pod Security levels, resource requests and limits, disruption budgets, network policies) for Mycroft's best-practice pages and the Auditor's checks, which cite the page. Facts only. |
| CIS Kubernetes Benchmark | cisecurity.org, CIS Kubernetes Benchmark | to be recorded | CIS terms of use; the PDFs are CC BY-NC-SA 4.0, so nothing is copied | Audit checks for the Auditor, cited by check number only, never copied (step 4). |
| Anthropic prompting guides and cookbook | docs.anthropic.com (prompt engineering, tool use); github.com/anthropics/anthropic-cookbook | to be recorded | Guides: Anthropic's terms, nothing copied; cookbook: MIT | Prompt structure (role, how you work, what you return), tool descriptions that say when to use a tool, and hand-over wording: every file in `prompts/`, `setup.md` and `cases.md` included. Techniques only. |
