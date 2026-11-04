import {
  ALL_NODES,
  inherited,
  nodeParams,
  PARAM_NAME,
  RESERVED,
  type AutomationFleet,
  type FleetVariables,
  type Param,
  type ScriptSummary,
} from "../api/automation";

/** What the Parameters page shows: a fleet, All nodes, or one node. */
export interface VariableScope {
  fleet: string;
  node: string | null;
}

/** A node that does not get the value its fleet shows. */
export interface Override {
  node: string;
  value?: string;
  /** The fleet its value comes from, or "its own value". */
  from: string;
  /** The node's own value, which can be removed from here. */
  own: boolean;
  /** The fleets that both set it, which is an error. */
  conflict?: string[];
}

/** A row of the parameters table. */
export interface VariableRow {
  name: string;
  value?: string;
  /** Where the value comes from. */
  from: string;
  /** Set here, not inherited, so it can be renamed and deleted. */
  own: boolean;
  sensitive: boolean;
  /** What this value overwrites, on the way down to this scope. */
  overwrites?: { from: string; value?: string };
  /** The nodes of this scope that get something else. */
  over: Override[];
  conflict?: string[];
}

const has = (m: Record<string, string> | undefined, name: string) =>
  !!m && Object.prototype.hasOwnProperty.call(m, name);

const layersOf = (fleets: AutomationFleet[], node: string) =>
  fleets.filter(
    (f) => f.id === ALL_NODES || f.nodes.some((n) => n.name === node),
  );

const marked = (fleets: AutomationFleet[], name: string) =>
  fleets.some((f) => (f.params?.sensitive ?? []).includes(name));

/** The fleets a node's own value is kept under: All nodes, or older ones. */
function holders(fleets: AutomationFleet[], node: string, name: string) {
  return layersOf(fleets, node).filter((f) =>
    has(f.params?.nodes?.[node], name),
  );
}

function nodeRows(fleets: AutomationFleet[], node: string): VariableRow[] {
  const all = fleets.find((f) => f.id === ALL_NODES);
  const bare = fleets.map((f) => ({
    ...f,
    params: { ...f.params, nodes: undefined },
  }));
  return nodeParams(fleets, node).map((p) => {
    const own = holders(fleets, node, p.name).length > 0;
    const under = inherited(bare, node, p.name);
    const overwrites = own
      ? under.source
        ? { from: under.from ?? "", value: under.value }
        : undefined
      : p.from !== all?.name && has(all?.params?.values, p.name)
        ? { from: all?.name ?? "", value: all?.params?.values?.[p.name] }
        : undefined;
    return {
      name: p.name,
      value: p.value,
      from: own ? "This node" : (p.from ?? ""),
      own,
      sensitive: marked(layersOf(fleets, node), p.name),
      overwrites,
      over: [],
      conflict: p.conflict,
    };
  });
}

function fleetRows(fleets: AutomationFleet[], id: string): VariableRow[] {
  const fleet = fleets.find((f) => f.id === id);
  if (!fleet) return [];
  const all = id === ALL_NODES ? undefined : fleets.find((f) => f.id === ALL_NODES);
  const names = new Set([
    ...Object.keys(fleet.params?.values ?? {}),
    ...Object.keys(all?.params?.values ?? {}),
  ]);
  return [...names].sort().map((name) => {
    const own = has(fleet.params?.values, name);
    const layer = own ? fleet : all!;
    const over: Override[] = [];
    for (const { name: node } of fleet.nodes) {
      const got = inherited(fleets, node, name);
      if (got.conflict)
        over.push({ node, from: "", own: false, conflict: got.conflict });
      else if (got.source === "node")
        over.push({ node, value: got.value, from: "its own value", own: true });
      else if (got.from !== layer.name)
        over.push({ node, value: got.value, from: got.from ?? "", own: false });
    }
    return {
      name,
      value: layer.params?.values?.[name],
      from: layer.name,
      own,
      sensitive: (layer.params?.sensitive ?? []).includes(name),
      overwrites:
        own && has(all?.params?.values, name)
          ? { from: all?.name ?? "", value: all?.params?.values?.[name] }
          : undefined,
      over,
    };
  });
}

/** The table of a scope: what a node gets, or what a fleet gets and where its nodes differ. */
export function scopeRows(
  fleets: AutomationFleet[],
  scope: VariableScope,
): VariableRow[] {
  return scope.node !== null
    ? nodeRows(fleets, scope.node)
    : fleetRows(fleets, scope.fleet);
}

/** How many values a node sets itself. */
export function ownValues(fleets: AutomationFleet[], node: string): number {
  return new Set(
    layersOf(fleets, node).flatMap((f) =>
      Object.keys(f.params?.nodes?.[node] ?? {}),
    ),
  ).size;
}

/** A parameter added, changed, renamed or, with no name, deleted. */
export interface VariableEdit {
  /** Its name now, when it is already set here. */
  old?: string;
  name?: string;
  value?: string;
  sensitive?: boolean;
}

function editValues(
  m: Record<string, string> | undefined,
  e: VariableEdit,
): Record<string, string> {
  const out = { ...m };
  if (e.old) delete out[e.old];
  if (e.name) out[e.name] = e.value ?? "";
  return out;
}

const ceilingOf = (p: FleetVariables) => (p.ceiling === "full" ? "full" : "none");

function editFleet(p: FleetVariables, e: VariableEdit): FleetVariables {
  const gone = e.old ?? e.name;
  const sensitive = (p.sensitive ?? []).filter((n) => n !== gone);
  if (e.name && e.sensitive) sensitive.push(e.name);
  return {
    ...p,
    values: editValues(p.values, e),
    ceiling: ceilingOf(p),
    sensitive,
  };
}

function editNode(p: FleetVariables, node: string, e: VariableEdit): FleetVariables {
  const nodes = { ...p.nodes, [node]: editValues(p.nodes?.[node], e) };
  if (Object.keys(nodes[node]!).length === 0) delete nodes[node];
  return { ...p, nodes, ceiling: ceilingOf(p) };
}

/**
 * The saves an edit takes. A fleet's is one. A node's own value goes under
 * All nodes, unless an older one is kept under a fleet: the first place
 * holding it is changed and any other loses it, so it ends up in one.
 */
export function variableSaves(
  fleets: AutomationFleet[],
  scope: VariableScope,
  e: VariableEdit,
): { fleet: string; params: FleetVariables }[] {
  const { node } = scope;
  if (node === null) {
    const f = fleets.find((x) => x.id === scope.fleet);
    return f ? [{ fleet: f.id, params: editFleet(f.params ?? {}, e) }] : [];
  }
  const name = e.old ?? e.name ?? "";
  const held = holders(fleets, node, name);
  const places = held.length
    ? held
    : fleets.filter((f) => f.id === ALL_NODES);
  return places.map((f, i) => ({
    fleet: f.id,
    params: editNode(
      f.params ?? {},
      node,
      i === 0 ? e : { old: name },
    ),
  }));
}

/** A set of name=value rows. */
export type Row = { k: string; v: string };

/** Problems with a set of name=value rows, as the server checks them. */
export function rowProblems(rows: Row[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const r of rows) {
    if (!r.k) continue;
    if (!PARAM_NAME.test(r.k))
      out.push(`${r.k}: a name is capitals, digits and underscores`);
    else if (RESERVED.includes(r.k)) out.push(`${r.k} is set by the run`);
    if (seen.has(r.k)) out.push(`${r.k} is there twice`);
    seen.add(r.k);
  }
  return out;
}

/** What stops a parameter being saved under this name. */
export function nameProblems(
  rows: VariableRow[],
  name: string,
  old?: string,
): string[] {
  const out = rowProblems([{ k: name, v: "" }]);
  if (name !== old && rows.some((r) => r.own && r.name === name))
    out.push(`${name} is already set here`);
  return out;
}

/** The scripts that read a parameter, by the versions that can still run. */
export function scriptsUsing(
  scripts: ScriptSummary[],
  name: string,
): { script: string; param: Param }[] {
  return scripts.flatMap((s) => {
    const param = (s.params ?? []).find((p) => p.name === name);
    return param ? [{ script: s.name, param }] : [];
  });
}

/** One line of what a parameter change does, for whoever reviews it. */
export interface ValueChange {
  what: string;
  before?: string;
  after?: string;
}

/** Every value a change alters, for a person to read before approving. */
export function valueChanges(
  before: FleetVariables,
  after: FleetVariables,
): ValueChange[] {
  const out: ValueChange[] = [];
  const compare = (
    a: Record<string, string> | undefined,
    b: Record<string, string> | undefined,
    label: (name: string) => string,
  ) => {
    for (const k of [
      ...new Set([...Object.keys(a ?? {}), ...Object.keys(b ?? {})]),
    ].sort())
      if (a?.[k] !== b?.[k])
        out.push({ what: label(k), before: a?.[k], after: b?.[k] });
  };
  compare(before.values, after.values, (k) => k);
  const nodes = new Set([
    ...Object.keys(before.nodes ?? {}),
    ...Object.keys(after.nodes ?? {}),
  ]);
  for (const n of [...nodes].sort())
    compare(before.nodes?.[n], after.nodes?.[n], (k) => `${k} on ${n}`);
  if (ceilingOf(before) !== ceilingOf(after))
    out.push({
      what: "Privilege ceiling",
      before: ceilingOf(before),
      after: ceilingOf(after),
    });
  const was = before.sensitive ?? [];
  const is = after.sensitive ?? [];
  for (const n of was.filter((x) => !is.includes(x)))
    out.push({ what: `${n}: sensitive`, before: "yes", after: "no" });
  for (const n of is.filter((x) => !was.includes(x)))
    out.push({ what: `${n}: sensitive`, before: "no", after: "yes" });
  return out;
}
