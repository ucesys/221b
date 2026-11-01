import { BACKGROUND, unauthorized } from "./auth";
import { apiPath, LOCAL } from "../lib/router";

/**
 * Automation (Phase 11 part 2, docs/automation.md, ADR-73): scripts with a
 * `# 221b:` header, their versions and approvals, fleets' parameters, and
 * runs on a fleet's paired nodes over SSH. Every call is a SuperAdmin's.
 */

export type Privilege = "none" | "full";
export type VersionState = "draft" | "review" | "approved" | "superseded";
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
}

export interface Header {
  describe: string;
  params: Param[] | null;
  privilege: Privilege | "";
  dryRun: boolean;
  facts?: string[];
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
  created: string;
  approvals?: { by: Who; at: string }[];
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
  group: string;
  describe: string;
  version: number;
  state: VersionState;
  /** The approved version, or 0. */
  approved: number;
  approvals: number;
  privilege: Privilege | "";
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
  running: number;
  limits: { text: number; timeout: number; parallel: number; nodes: number };
}

export interface FleetParams {
  values?: Record<string, string>;
  nodes?: Record<string, Record<string, string>>;
  ceiling?: Privilege | "";
}

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
  params: FleetParams;
}

export interface AutomationFleets {
  cluster: string;
  off?: string;
  readOnly?: string;
  production: boolean;
  twoPerson: boolean;
  fleets: AutomationFleet[];
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
}

/** The fleet of every node of a cluster. */
export const ALL_NODES = "_all";

const BASE = "/api/v1/automation";

async function call<T>(
  url: string,
  init: RequestInit = {},
  background = false,
): Promise<T> {
  let res: Response;
  const headers: Record<string, string> = {
    Accept: "application/json",
    ...(background ? BACKGROUND : {}),
  };
  if (init.body !== undefined) headers["Content-Type"] = "application/json";
  try {
    res = await fetch(url, { ...init, headers });
  } catch {
    throw new Error("The server could not be reached.");
  }
  if (!res.ok) {
    if (unauthorized(res))
      throw new Error("Your session has ended; sign in again.");
    let msg = `The server answered ${res.status}.`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) msg = body.error;
    } catch {
      // Not JSON: keep the status.
    }
    throw new Error(msg);
  }
  return (await res.json()) as T;
}

const post = (body: unknown): RequestInit => ({
  method: "POST",
  body: JSON.stringify(body),
});
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
export const saveFleetParams = (
  cluster: string,
  fleet: string,
  p: FleetParams,
) =>
  call<FleetParams>(
    apiPath(`automation/fleets/${encodeURIComponent(fleet)}/params`, cluster),
    { method: "PUT", body: JSON.stringify(p) },
  );
export const startRun = (cluster: string, req: RunRequest) =>
  call<Run>(apiPath("automation/runs", cluster), post(req));

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
# 221b: privilege: full
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

export type Source = "run" | "node" | "fleet";

export interface ResolvedParam {
  name: string;
  required: boolean;
  desc?: string;
  value?: string;
  source?: Source;
}

/**
 * A script's parameters on one node, as the server resolves them (AU-06):
 * the run's value, then the node's, then the fleet's.
 */
export function resolveParams(
  params: Param[] | null | undefined,
  fp: FleetParams | undefined,
  node: string,
  run: Record<string, string>,
): ResolvedParam[] {
  return (params ?? []).map((p) => {
    const out: ResolvedParam = {
      name: p.name,
      required: p.required,
      desc: p.desc,
    };
    const layers: [Source, Record<string, string> | undefined][] = [
      ["run", run],
      ["node", fp?.nodes?.[node]],
      ["fleet", fp?.values],
    ];
    for (const [source, values] of layers) {
      if (values && Object.prototype.hasOwnProperty.call(values, p.name)) {
        out.value = values[p.name];
        out.source = source;
        break;
      }
    }
    return out;
  });
}

/** The required parameters with no value on any of the nodes, "node: NAME". */
export function missingParams(
  params: Param[] | null | undefined,
  fp: FleetParams | undefined,
  nodes: string[],
  run: Record<string, string>,
): string[] {
  const out: string[] = [];
  for (const n of nodes)
    for (const r of resolveParams(params, fp, n, run))
      if (r.required && r.value === undefined) out.push(`${n}: ${r.name}`);
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

/** Whether a fleet's ceiling allows a privilege; the default ceiling is none. */
export function allows(
  fp: FleetParams | undefined,
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
export function canApprove(v: Version, me: Who): boolean {
  if (v.state !== "review" && v.state !== "approved") return false;
  if (v.state === "approved" && (v.approvals?.length ?? 0) >= 2) return false;
  return !(v.approvals ?? []).some((a) => same(a.by, me));
}

export function same(a: Who, b: Who): boolean {
  return a.user && b.user ? a.user === b.user : a.name === b.name;
}

/**
 * Whether a version may run on a cluster: approved, and on a two-person
 * cluster approved by two people, one of them not its author.
 */
export function runnableOn(
  v: Version | undefined,
  twoPerson: boolean,
): boolean {
  if (!v || v.state !== "approved") return false;
  if (!twoPerson) return true;
  const ap = v.approvals ?? [];
  return ap.length >= 2 && ap.some((a) => !same(a.by, v.author));
}

/** The version a run would use: the newest approved one. */
export function approvedOf(s: Script): Version | undefined {
  return [...s.versions].reverse().find((v) => v.state === "approved");
}

export function latestOf(s: Script): Version {
  return s.versions[s.versions.length - 1]!;
}

/** Scripts by group, the groups sorted, "Ungrouped" last. */
export function byGroup(list: ScriptSummary[]): [string, ScriptSummary[]][] {
  const m = new Map<string, ScriptSummary[]>();
  for (const s of list) {
    const g = s.group || "";
    m.set(g, [...(m.get(g) ?? []), s]);
  }
  return [...m.entries()]
    .sort(([a], [b]) => (a === "" ? 1 : b === "" ? -1 : a.localeCompare(b)))
    .map(([g, ss]) => [
      g || "Ungrouped",
      ss.sort((x, y) => x.name.localeCompare(y.name)),
    ]);
}
