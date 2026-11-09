import { apiPath, LOCAL } from "../lib/router";
import { call, post } from "./call";
import reserveNodeResources from "../scripts/reserve-node-resources.sh?raw";

/**
 * Automation (Phase 11 part 2, docs/automation.md, ADR-73): scripts with a
 * `# 221b:` header, their versions and approvals, fleets' parameters, and
 * runs on a fleet's paired nodes over SSH. Every call is a SuperAdmin's.
 */

export type Privilege = "none" | "full";
export type VersionState =
  "draft" | "review" | "approved" | "superseded" | "rejected";

/** The longest reason a decision may carry, as the server allows. */
export const MAX_COMMENT = 500;
export type RunState =
  "running" | "succeeded" | "failed" | "stopped" | "cancelled" | "interrupted";
export type NodeState =
  | "waiting"
  | "running"
  | "rebooting"
  | "nochange"
  | "changed"
  | "failed"
  | "unreachable"
  | "skipped"
  | "cancelled";
export type RolloutMode = "all" | "size" | "canary";

export interface Who {
  name: string;
  user?: string;
}

export interface Param {
  name: string;
  required: boolean;
  desc?: string;
  /** The value is a reference to a Kubernetes Secret. */
  secret?: boolean;
}

export interface KubeAction {
  verb: string;
  resource: string;
}

export interface Header {
  describe: string;
  params: Param[] | null;
  privilege: Privilege | "";
  dryRun: boolean;
  facts?: string[];
  /** kubernetes for a script that acts on the cluster. */
  target?: string;
  k8s?: KubeAction[];
  runsAs?: string;
}

export interface Finding {
  level: "error" | "warning";
  line: number;
  code: string;
  msg: string;
}

export interface Version {
  n: number;
  text: string;
  hash: string;
  header: Header;
  findings: Finding[] | null;
  state: VersionState;
  author: Who;
  /** The others who saved this draft. */
  editors?: Who[];
  submitted?: { by: Who; at: string };
  created: string;
  approvals?: { by: Who; at: string }[];
  /** Who took its approvals back, and when; the approvals they took back. */
  revoked?: { by: Who; at: string }[];
  withdrawn?: { by: Who; at: string }[];
  /** The rejected version whose text this one has again. */
  sameAs?: number;
  /** What the person asking may do with it, as the server decides. */
  may?: VersionMay;
  /** Who turned it down in review, when and why. */
  rejected?: { by: Who; at: string; comment?: string };
  /** Sherlock's latest reading: advice, never an approval. */
  review?: {
    summary: string;
    findings?: { level: string; line?: number; msg: string }[];
    at: string;
  };
}

/** What a person may do with a version: the server's answer, shown as it is. */
export interface VersionMay {
  approve: boolean;
  reject: boolean;
  /** They wrote or submitted it, so someone else must approve it. */
  involved: boolean;
  runProduction: boolean;
  runElsewhere: boolean;
  dry: boolean;
}

export interface Script {
  name: string;
  group?: string;
  created: string;
  updated: string;
  versions: Version[];
}

export interface RunBrief {
  id: number;
  state: RunState;
  dryRun: boolean;
  version: number;
  nodes: number;
  counts: Partial<Record<NodeState, number>>;
  started: string;
}

export interface ScriptSummary {
  name: string;
  /** kubernetes for a script that acts on the cluster. */
  target?: string;
  group: string;
  describe: string;
  version: number;
  state: VersionState;
  /** The version waiting for review, or 0. */
  inReview: number;
  /** The approved version, or 0. */
  approved: number;
  approvals: number;
  privilege: Privilege | "";
  /** The latest version declares dry-run yes. */
  dryRun: boolean;
  /** What the latest and the approved version read. */
  params?: Param[];
  errors: number;
  warnings: number;
  created: string;
  updated: string;
  lastRun?: RunBrief;
}

export interface AutomationIndex {
  /** Why runs are off on this console, or "". */
  off: string;
  me: Who;
  scripts: ScriptSummary[];
  groups: Group[];
  running: number;
  limits: { text: number; timeout: number; parallel: number; nodes: number };
}

export interface FleetVariables {
  values?: Record<string, string>;
  nodes?: Record<string, Record<string, string>>;
  ceiling?: Privilege | "";
  /** Parameters whose change always needs approval. */
  sensitive?: string[];
  /** Minutes the fleet's nodes have to come back from a reboot; unset is the default. */
  rebootMinutes?: number;
  /** Single nodes' own reboot timeouts, which win over any fleet's. */
  nodeReboot?: Record<string, number>;
}

/** How long a rebooted node has to come back when nothing sets it. */
export const DEFAULT_REBOOT_MINUTES = 10;
/** The longest a reboot timeout may be. */
export const MAX_REBOOT_MINUTES = 240;

export interface AutomationNode {
  name: string;
  paired: boolean;
  loginUser?: string;
  /** Why the node cannot be run on, when it cannot. */
  why?: string;
}

export interface AutomationFleet {
  id: string;
  name: string;
  nodes: AutomationNode[];
  params: FleetVariables;
}

export interface AutomationFleets {
  cluster: string;
  off?: string;
  readOnly?: string;
  production: boolean;
  fleets: AutomationFleet[];
  /** Parameters a node gets from more than one fleet. */
  conflicts?: ParamConflict[];
}

export interface ParamConflict {
  node: string;
  param: string;
  fleets: string[];
}

export interface Rollout {
  mode: RolloutMode;
  size?: number;
  stopAfter: number;
  timeoutMinutes: number;
}

export interface NodeResult {
  node: string;
  batch: number;
  state: NodeState;
  exit?: number;
  msg?: string;
  error?: string;
  started?: string;
  ended?: string;
  tail?: string;
  values?: Record<string, string>;
  /** Minutes the node had to come back from a reboot; unset is the default. */
  rebootMinutes?: number;
  /** The run drained it and nothing uncordoned it since. */
  cordoned?: boolean;
}

export interface Run {
  id: number;
  script: string;
  version: number;
  hash: string;
  dryRun: boolean;
  privilege: Privilege;
  cluster: string;
  clusterName?: string;
  fleet: string;
  fleetName?: string;
  overrides?: Record<string, string>;
  rollout: Rollout;
  by: Who;
  state: RunState;
  note?: string;
  /** kubernetes for a script on the cluster. */
  target?: string;
  config?: string;
  identity?: string;
  /** Why this version could run. */
  basis?: string;
  hooks?: string[];
  retryOf?: number;
  /** Who had approved the version when the run started. */
  approvers?: Who[];
  started: string;
  ended?: string;
  beat: string;
  nodes: NodeResult[];
}

export interface RunDetail {
  run: Run;
  /** Each node's output, as far as it is kept. */
  output: Record<string, string> | null;
  /** Whether this console is running it now. */
  live: boolean;
}

export interface RunRequest {
  script: string;
  version?: number;
  dryRun: boolean;
  fleet: string;
  nodes?: string[];
  params?: Record<string, string>;
  rollout: Rollout;
  config?: string;
  hooks?: HookRefs;
  /** The page's name for this start: sent twice, it starts one run. */
  request?: string;
}

/** What runs around each node: drain, uncordon or script:<name>. */
export interface HookRefs {
  before?: string[];
  after?: string[];
}

/** The fleet of every node of a cluster. */
export const ALL_NODES = "_all";

/** The Run dialog's fleet when the nodes were picked by hand. */
export const CUSTOM_NODES = "custom";

/** The nodes of a fleet a run can reach. */
export function pairedNames(f: AutomationFleet | undefined): string[] {
  return (f?.nodes ?? []).filter((n) => n.paired).map((n) => n.name);
}

/** The fleet whose reachable nodes are exactly these, or CUSTOM_NODES. */
export function fleetOf(fleets: AutomationFleet[], picked: string[]): string {
  const want = new Set(picked);
  const f = fleets.find((x) => {
    const names = pairedNames(x);
    return names.length === want.size && names.every((n) => want.has(n));
  });
  return f && want.size > 0 ? f.id : CUSTOM_NODES;
}

/** What each rollout does, for the Run dialog. */
export function rolloutHelp(mode: RolloutMode, size: number): string {
  if (mode === "canary")
    return `One node runs alone first; any failure there stops the run. The rest then run ${size > 0 ? `${size} at a time` : "together"}.`;
  if (mode === "size")
    return `${Math.max(size, 1)} at a time; each batch finishes before the next starts. Any failure in the first batch stops the run.`;
  return "Every node at once, at most 20 at the same time. Quickest, and nothing is held back to limit a bad script.";
}

/** The Scripts page's state filter. */
export const SCRIPT_STATES: { id: string; label: string }[] = [
  { id: "review", label: "Review" },
  { id: "draft", label: "Draft" },
  { id: "approved", label: "Approved" },
];

/** What a script is written in or acts on: Shell on nodes, K8S on the cluster. */
export function scriptKind(target: string | undefined): string {
  if (!target) return "Shell";
  if (target === "kubernetes") return "K8S";
  return target.charAt(0).toUpperCase() + target.slice(1);
}

/** How a fleet's privilege ceiling reads. */
export const ceilingText = (c: string | undefined) =>
  c === "full" ? "elevated" : "nobody";

/** How a privilege reads: nobody, read-only, or elevated, the ssh-paired account's rights. */
export const privilegeText = (p: string | undefined) =>
  p === "full" ? "elevated" : p === "none" ? "nobody" : "—";

/** The names the built-in run profiles go by; a run records the id. */
const PROFILE_NAMES: Record<string, string> = {
  "read-only": "nobody",
  "login-user": "ssh-paired account",
  "as-the-person": "login-user",
};

/** A run profile's name, from the id a run records. */
export const profileName = (id: string) => PROFILE_NAMES[id] ?? id;

export function inState(s: ScriptSummary, state: string): boolean {
  switch (state) {
    case "review":
      return s.inReview > 0;
    case "draft":
      return s.state === "draft";
    case "approved":
      return s.approved > 0;
    case "unapproved":
      return s.approved === 0;
  }
  return true;
}

const BASE = "/api/v1/automation";

const nameURL = (name: string) => `${BASE}/scripts/${encodeURIComponent(name)}`;

export const fetchAutomation = (background = false) =>
  call<AutomationIndex>(BASE, {}, background);
export const checkScript = (text: string) =>
  call<{ header: Header; findings: Finding[] | null }>(
    `${BASE}/check`,
    post({ text }),
    true,
  );
export const fetchScript = (name: string) => call<Script>(nameURL(name));
export const createScript = (name: string, group: string, text: string) =>
  call<Script>(`${BASE}/scripts`, post({ name, group, text }));
export const saveScript = (name: string, group: string, text: string) =>
  call<Script>(nameURL(name), {
    method: "PUT",
    body: JSON.stringify({ group, text }),
  });
export const deleteScript = (name: string) =>
  call<{ deleted: boolean }>(nameURL(name), { method: "DELETE" });
export const submitVersion = (name: string, n: number) =>
  call<Script>(`${nameURL(name)}/versions/${n}/submit`, { method: "POST" });
export const approveVersion = (name: string, n: number) =>
  call<Script>(`${nameURL(name)}/versions/${n}/approve`, { method: "POST" });
export const revokeVersion = (name: string, n: number) =>
  call<Script>(`${nameURL(name)}/versions/${n}/revoke`, post({}));
export const rejectVersion = (name: string, n: number, comment: string) =>
  call<Script>(`${nameURL(name)}/versions/${n}/reject`, post({ comment }));
export const fetchRuns = (background = false) =>
  call<{ runs: Run[] | null }>(`${BASE}/runs`, {}, background).then(
    (r) => r.runs ?? [],
  );
export const fetchRun = (id: number, background = false) =>
  call<RunDetail>(`${BASE}/runs/${id}`, {}, background);
export const cancelRun = (id: number) =>
  call<{ cancelled: boolean }>(`${BASE}/runs/${id}/cancel`, { method: "POST" });
export const fetchFleets = (cluster: string = LOCAL) =>
  call<AutomationFleets>(apiPath("automation/fleets", cluster));
export const saveFleetVariables = (
  cluster: string,
  fleet: string,
  p: FleetVariables & { reason?: string },
) =>
  call<{ params: FleetVariables; change?: VariableChange }>(
    apiPath(
      `automation/fleets/${encodeURIComponent(fleet)}/variables`,
      cluster,
    ),
    { method: "PUT", body: JSON.stringify(p) },
  );
export const startRun = (cluster: string, req: RunRequest) =>
  call<Run>(apiPath("automation/runs", cluster), post(req));
export const retryRun = (cluster: string, id: number) =>
  call<Run>(apiPath(`automation/runs/${id}/retry`, cluster), post({}));

// ---------- configurations ----------

export interface RunProfile {
  id: string;
  name: string;
  kind: "ssh" | "kubernetes";
  desc?: string;
  privilege?: "none" | "full";
  namespace?: string;
  serviceAccount?: string;
  fleets?: string[];
  builtin?: boolean;
  /** Runs in the last seven days. */
  runs?: number;
}

export const fetchProfiles = () =>
  call<{ profiles: RunProfile[] }>("/api/v1/automation/run-profiles");
export const saveProfile = (c: RunProfile) =>
  call<RunProfile>(
    `/api/v1/automation/run-profiles/${encodeURIComponent(c.id)}`,
    {
      method: "PUT",
      body: JSON.stringify(c),
    },
  );
export const deleteProfile = (id: string) =>
  call<void>(`/api/v1/automation/run-profiles/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });

// ---------- facts ----------

export {
  collectFacts,
  fetchFacts,
  refreshFacts,
  type NodeFacts,
} from "./facts";

// ---------- parameter changes ----------

export interface ImpactItem {
  script: string;
  version: number;
  decision: "safe" | "approval";
  why: string;
  by: string;
}

export interface VariableChange {
  id: number;
  cluster: string;
  fleet: string;
  fleetName?: string;
  by: Who;
  at: string;
  reason?: string;
  changed: string[];
  before: FleetVariables;
  after: FleetVariables;
  impact: ImpactItem[];
  note?: string;
  state: "waiting" | "approved" | "rejected" | "applied";
  /** The reason given with a rejection. */
  comment?: string;
  /** Why it can only be rejected now: a newer change on its fleet. */
  overtaken?: string;
}

export const fetchVariableChanges = (cluster: string) =>
  call<{ changes: VariableChange[] }>(
    apiPath("automation/variable-changes", cluster),
  );
export const decideVariableChange = (
  cluster: string,
  id: number,
  decision: "approve" | "reject",
  comment = "",
) =>
  call<VariableChange>(
    apiPath(`automation/variable-changes/${id}/${decision}`, cluster),
    post(comment ? { comment } : {}),
  );

// ---------- Sherlock ----------

export const draftScript = (
  request: string,
  text: string,
  signal: AbortSignal,
) =>
  call<{
    text: string;
    findings: Finding[] | null;
    questions: string[] | null;
    /** What Sherlock said beside the code, a paragraph a round. */
    notes?: string[] | null;
    rounds: number;
  }>("/api/v1/automation/sherlock/draft", {
    ...post({ request, text }),
    signal,
  });

// ---------- what waits for approval ----------

/** One thing that waits, or waited, for an approval; Approved by and on name whoever decided it. */
export interface ApprovalRow {
  kind: "script" | "change" | "command" | "admin";
  state: "pending" | "approved" | "rejected" | "revoked";
  what: string;
  cluster?: string;
  clusterName?: string;
  requestedBy: string;
  requestedAt?: string;
  approvedBy?: string;
  approvedAt?: string;
  script?: string;
  version?: number;
  fleet?: string;
  id?: string;
  /** The reason given with a rejection. */
  comment?: string;
  /** Someone else decided what this person asked for. */
  mine?: boolean;
  /** This person asked for what waits, and may approve it themselves. */
  own?: boolean;
  /** Why a waiting change can only be rejected now. */
  overtaken?: string;
}

/** What waits for this person, first, then what was decided, newest first. */
export interface Approvals {
  items: ApprovalRow[];
}

export const fetchApprovals = (background = false) =>
  call<Approvals>("/api/v1/admin/approvals", {}, background);

/** Approves or rejects a console change that waits for a second SuperAdmin; whoever asked may only reject (withdraw) it. */
export const decideRequest = (id: string, approve: boolean, comment = "") =>
  call<unknown>(
    `/api/v1/admin/requests/${encodeURIComponent(id)}/${approve ? "approve" : "reject"}`,
    post({ comment }),
  );

// ---------- groups ----------

export interface Group {
  name: string;
  desc?: string;
}

export const saveGroup = (old: string | null, g: Group) =>
  old === null
    ? call<Group>("/api/v1/automation/groups", post(g))
    : call<Group>(`/api/v1/automation/groups/${encodeURIComponent(old)}`, {
        method: "PUT",
        body: JSON.stringify(g),
      });
export const deleteGroup = (name: string) =>
  call<{ deleted: boolean }>(
    `/api/v1/automation/groups/${encodeURIComponent(name)}`,
    { method: "DELETE" },
  );
export const reviewVersion = (name: string, n: number) =>
  call<Script>(`${nameURL(name)}/versions/${n}/review`, post({}));

// ---------- helpers the pages share ----------

/** The names a script may not use for a parameter: the run sets them. */
export const RESERVED = [
  "DRY_RUN",
  "NODE",
  "CLUSTER",
  "FLEET",
  "RUN_ID",
  "PATH",
  "HOME",
  "USER",
  "SHELL",
];
export const PARAM_NAME = /^[A-Z_][A-Z0-9_]{0,63}$/;
export const SCRIPT_NAME = /^[a-z0-9][a-z0-9-]{0,62}$/;

/** The template a new script starts from. */
export const NEW_SCRIPT = `#!/bin/sh
# 221b: describe: What this script does, in one line
# 221b: param: PACKAGE required The package to keep installed
# 221b: privilege: elevated
# 221b: dry-run: yes
set -eu

if dpkg -s "$PACKAGE" >/dev/null 2>&1; then
  echo '{"changed": false, "msg": "already installed"}'
  exit 0
fi
if [ "$DRY_RUN" = 1 ]; then
  echo '{"changed": true, "msg": "would install '"$PACKAGE"'"}'
  exit 0
fi
apt-get install -y "$PACKAGE"
echo '{"changed": true, "msg": "installed"}'
`;

/** What a new script can start from. */
export const SCRIPT_TEMPLATES: {
  id: string;
  label: string;
  name: string;
  group: string;
  text: string;
}[] = [
  { id: "blank", label: "An example", name: "", group: "", text: NEW_SCRIPT },
  {
    id: "reserve",
    label: "Reserve resources from Kubernetes (RKE2, k3s)",
    name: "reserve-node-resources",
    group: "Kubernetes agent",
    text: reserveNodeResources,
  },
];

export type Source = "run" | "node" | "fleet";

export interface ResolvedParam {
  name: string;
  required: boolean;
  desc?: string;
  value?: string;
  source?: Source;
  /** The fleet a value comes from. */
  from?: string;
  /** The fleets that both set it, which is an error. */
  conflict?: string[];
}

/**
 * A script's parameters on one node, as the server resolves them (AU-06):
 * the run's value, then the node's, then the fleet's.
 */
export function resolveParams(
  params: Param[] | null | undefined,
  fleets: AutomationFleet[],
  node: string,
  run: Record<string, string>,
): ResolvedParam[] {
  return (params ?? []).map((p) => {
    const out: ResolvedParam = {
      name: p.name,
      required: p.required,
      desc: p.desc,
    };
    if (Object.prototype.hasOwnProperty.call(run, p.name)) {
      out.value = run[p.name];
      out.source = "run";
      return out;
    }
    return { ...out, ...inherited(fleets, node, p.name) };
  });
}

/** What a node gets for a parameter: its own value, then its fleets', then All nodes'. */
export function inherited(
  fleets: AutomationFleet[],
  node: string,
  name: string,
): Pick<ResolvedParam, "value" | "source" | "from" | "conflict"> {
  const mine = fleets.filter(
    (f) => f.id === ALL_NODES || f.nodes.some((n) => n.name === node),
  );
  const has = (m: Record<string, string> | undefined) =>
    !!m && Object.prototype.hasOwnProperty.call(m, name);
  const own = mine.filter((f) => has(f.params?.nodes?.[node]));
  if (own.length > 0) {
    const values = new Set(own.map((f) => f.params?.nodes?.[node]?.[name]));
    if (values.size > 1) return { conflict: own.map((f) => f.name).sort() };
    return {
      value: own[0]!.params?.nodes?.[node]?.[name],
      source: "node",
      from: own[0]!.name,
    };
  }
  const set = mine.filter((f) => f.id !== ALL_NODES && has(f.params?.values));
  if (set.length > 1) return { conflict: set.map((f) => f.name).sort() };
  const from =
    set[0] ?? mine.find((f) => f.id === ALL_NODES && has(f.params?.values));
  return from
    ? { value: from.params?.values?.[name], source: "fleet", from: from.name }
    : {};
}

/** The parameters a node gets, whatever script reads them. */
export function nodeParams(
  fleets: AutomationFleet[],
  node: string,
): ResolvedParam[] {
  const names = new Set<string>();
  for (const f of fleets) {
    if (f.id !== ALL_NODES && !f.nodes.some((n) => n.name === node)) continue;
    for (const k of Object.keys(f.params?.values ?? {})) names.add(k);
    for (const k of Object.keys(f.params?.nodes?.[node] ?? {})) names.add(k);
  }
  return resolveParams(
    [...names].sort().map((name) => ({ name, required: false })),
    fleets,
    node,
    {},
  );
}

/** What stops a run: a parameter a picked node gets from more than one fleet. */
export function paramClashes(
  params: Param[] | null | undefined,
  fleets: AutomationFleet[],
  nodes: string[],
  run: Record<string, string>,
): string[] {
  const out: string[] = [];
  for (const n of nodes)
    for (const r of resolveParams(params, fleets, n, run))
      if (r.conflict)
        out.push(`${r.name} on ${n} comes from ${r.conflict.join(" and ")}`);
  return out;
}

export function missingParams(
  params: Param[] | null | undefined,
  fleets: AutomationFleet[],
  nodes: string[],
  run: Record<string, string>,
): string[] {
  const out: string[] = [];
  for (const n of nodes)
    for (const r of resolveParams(params, fleets, n, run))
      if (r.required && r.value === undefined && !r.conflict)
        out.push(n ? `${n}: ${r.name}` : r.name);
  return out;
}

/** How the rollout splits the nodes into batches, as the server does. */
export function batches(
  nodes: string[],
  r: Pick<Rollout, "mode" | "size">,
): string[][] {
  if (nodes.length === 0) return [];
  const chunks = (ns: string[], size: number | undefined): string[][] => {
    if (!size || size <= 0 || size >= ns.length) return [ns];
    const out: string[][] = [];
    for (let i = 0; i < ns.length; i += size) out.push(ns.slice(i, i + size));
    return out;
  };
  if (r.mode === "size") return chunks(nodes, r.size);
  if (r.mode === "canary")
    return nodes.length > 1
      ? [nodes.slice(0, 1), ...chunks(nodes.slice(1), r.size)]
      : [nodes.slice(0, 1)];
  return [nodes];
}

/**
 * The run profiles a run may take, and the one that fits the script: what
 * the server picks when none is named. A dry run on nodes takes nobody
 * alone; a script on the cluster takes Kubernetes profiles; one on nodes
 * takes those that reach the fleet, give at least what the script declares
 * and no more than the fleet's ceiling. The one that fits is offered under
 * any ceiling, so the dialog says what the ceiling stops.
 */
export function runProfilesFor(
  profiles: RunProfile[],
  o: {
    onCluster: boolean;
    dryRun: boolean;
    privilege: Privilege | "";
    fleet?: string;
    fp?: FleetVariables;
  },
): { options: RunProfile[]; fit: RunProfile | undefined } {
  const fitId = o.onCluster
    ? "as-the-person"
    : o.dryRun || o.privilege !== "full"
      ? "read-only"
      : "login-user";
  const options = profiles
    .filter((c) =>
      o.onCluster
        ? c.kind === "kubernetes"
        : c.kind === "ssh" &&
          (o.dryRun
            ? c.id === "read-only"
            : (!c.fleets?.length || c.fleets.includes(o.fleet ?? "")) &&
              (o.privilege !== "full" || c.privilege === "full") &&
              (c.id === fitId || allows(o.fp, c.privilege ?? ""))),
    )
    .sort((a, b) => Number(!!b.builtin) - Number(!!a.builtin));
  return { options, fit: options.find((c) => c.id === fitId) ?? options[0] };
}

/** Whether a fleet's ceiling allows a privilege; the default ceiling is none. */
export function allows(
  fp: FleetVariables | undefined,
  privilege: Privilege | "",
): boolean {
  return privilege !== "full" || fp?.ceiling === "full";
}

export type Tone = "ok" | "warn" | "err" | "info" | undefined;

export function runTone(s: RunState): Tone {
  switch (s) {
    case "succeeded":
      return "ok";
    case "running":
      return "info";
    case "failed":
    case "interrupted":
      return "err";
    case "stopped":
    case "cancelled":
      return "warn";
  }
  return undefined;
}

export function nodeTone(s: NodeState): Tone {
  switch (s) {
    case "changed":
    case "nochange":
      return "ok";
    case "running":
    case "rebooting":
      return "info";
    case "failed":
    case "unreachable":
      return "err";
    case "cancelled":
    case "skipped":
      return "warn";
  }
  return undefined;
}

export const NODE_LABEL: Record<NodeState, string> = {
  waiting: "waiting",
  running: "running",
  rebooting: "rebooting",
  nochange: "no change",
  changed: "changed",
  failed: "failed",
  unreachable: "unreachable",
  skipped: "skipped",
  cancelled: "cancelled",
};

export function versionTone(s: VersionState): Tone {
  return s === "approved"
    ? "ok"
    : s === "review"
      ? "info"
      : s === "draft"
        ? "warn"
        : s === "rejected"
          ? "err"
          : undefined;
}

/** "3 changed · 1 failed", in a fixed order. */
export function countsText(c: Partial<Record<NodeState, number>>): string {
  const order: NodeState[] = [
    "changed",
    "nochange",
    "failed",
    "unreachable",
    "running",
    "rebooting",
    "waiting",
    "skipped",
    "cancelled",
  ];
  return order
    .filter((s) => (c[s] ?? 0) > 0)
    .map((s) => `${c[s]} ${NODE_LABEL[s]}`)
    .join(" · ");
}

/**
 * What the person may do with a version: submit a draft, approve one in
 * review (not twice; on a two-person cluster the author's approval alone
 * never runs it, which the server enforces at run time).
 */
/** Whether the person may approve a version, as the server said. */
export function canApprove(v: Version): boolean {
  return !!v.may?.approve;
}

/** Whether the person may reject a version, as the server said. */
export function canReject(v: Version): boolean {
  return !!v.may?.reject;
}

/** Whether a version may run here: on production, or elsewhere. */
export function mayRun(v: Version | undefined, production: boolean): boolean {
  if (!v?.may) return false;
  return production ? v.may.runProduction : v.may.runElsewhere;
}

export function same(a: Who, b: Who): boolean {
  return a.user && b.user ? a.user === b.user : a.name === b.name;
}

/** The version a run would use: the newest approved one. */
export function approvedOf(s: Script): Version | undefined {
  return [...s.versions].reverse().find((v) => v.state === "approved");
}

export function latestOf(s: Script): Version {
  return s.versions[s.versions.length - 1]!;
}

/** The Scripts page's filter value for scripts in no group. */
export const UNGROUPED = "_ungrouped";

export interface ScriptGroup {
  id: string;
  name: string;
  count: number;
}

/** The groups for the Scripts page's left pane, Ungrouped last. */
export function scriptGroups(
  list: ScriptSummary[],
  kept: Group[] = [],
): ScriptGroup[] {
  const counts = new Map<string, number>(kept.map((g) => [g.name, 0]));
  for (const s of list) counts.set(s.group, (counts.get(s.group) ?? 0) + 1);
  return [...counts.entries()]
    .sort(([a], [b]) => (a === "" ? 1 : b === "" ? -1 : a.localeCompare(b)))
    .map(([g, count]) => ({
      id: g || UNGROUPED,
      name: g || "Ungrouped",
      count,
    }));
}

/** The groups picked, read from the URL; a group's name may hold a comma. */
export function parseGroups(param: string | null): string[] {
  return (param ?? "").split(",").filter(Boolean).map(decodeURIComponent);
}

export function formatGroups(ids: string[]): string {
  return ids.map(encodeURIComponent).join(",");
}

export function inGroups(s: ScriptSummary, ids: string[]): boolean {
  return ids.length === 0 || ids.includes(s.group || UNGROUPED);
}
