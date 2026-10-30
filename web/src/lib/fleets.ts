// The Fleets tab's model (P4-01), ported from the prototype: a fleet is a
// set of labels and taints, and every node that has all of them belongs to
// it, or a list of nodes by name (asked after 0.7.1). The server works out the saved fleets' members and totals; this file
// draws the matrix of nodes against labels and taints, and previews a draft
// in the editor before it is saved.

import type { Fleet, FleetRow, FleetStats, NodeRow } from '../api/types'
import { effectTone, labelText, taintId } from './nodes'
import { fCores, fMem, nWord, pct, pctTxt } from './format'

export { labelText, taintId }

/** The six swatches, in order. */
export const COLOR_NAMES = ['Teal', 'Blue', 'Violet', 'Amber', 'Rose', 'Green']

export const TAINT_EFFECTS = ['NoSchedule', 'PreferNoSchedule', 'NoExecute']

export const LABEL_KEY_RE = /^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*\/)?[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$/
export const LABEL_VAL_RE = /^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$/

/** Splits "key=value" on the first "=". */
export function splitLabel(s: string): [string, string] {
  const i = s.indexOf('=')
  return i < 0 ? [s, ''] : [s.slice(0, i), s.slice(i + 1)]
}

/** Splits "key[=value]:Effect". */
export function splitTaint(id: string): { key: string; value: string; effect: string } {
  const i = id.lastIndexOf(':')
  const [key, value] = splitLabel(i < 0 ? id : id.slice(0, i))
  return { key, value, effect: i < 0 ? '' : id.slice(i + 1) }
}

/** "key[=value]", a taint without its effect. */
export function taintKeyText(id: string): string {
  const t = splitTaint(id)
  return t.key + (t.value ? '=' + t.value : '')
}

/** Whether a fleet lists its nodes by name. */
export function isList(f: Pick<Fleet, 'kind'>): boolean {
  return f.kind === 'nodes'
}

/** Whether a node is in a fleet: on its list, or with every one of its labels and taints. */
export function has(f: Pick<Fleet, 'labels' | 'taints' | 'kind' | 'nodes'>, n: Pick<NodeRow, 'labels' | 'taints'> & { name?: string }): boolean {
  if (isList(f)) return !!n.name && (f.nodes ?? []).includes(n.name)
  return f.labels.every((l) => {
    const [k, v] = splitLabel(l)
    return k in n.labels && n.labels[k] === v
  }) && f.taints.every((t) => n.taints.some((x) => taintId(x) === t))
}

export interface Missing {
  kind: 'l' | 't'
  id: string
  key: string
  val: string
  text: string
  /** The node's value for the key, when it has the key with another value. */
  conflict: string | null
}

/** What a node lacks of a fleet's requirements. */
export function missing(f: Pick<Fleet, 'labels' | 'taints'>, n: Pick<NodeRow, 'labels' | 'taints'>): Missing[] {
  const out: Missing[] = []
  for (const l of f.labels) {
    const [k, v] = splitLabel(l)
    if (k in n.labels && n.labels[k] === v) continue
    out.push({ kind: 'l', id: l, key: k, val: v, text: labelText(k, v), conflict: k in n.labels ? (n.labels[k] ?? '') : null })
  }
  for (const t of f.taints) {
    if (!n.taints.some((x) => taintId(x) === t)) out.push({ kind: 't', id: t, key: splitTaint(t).key, val: '', text: 'taint ' + t, conflict: null })
  }
  return out
}

/** The command that gives a node what it lacks, shown, not run: changes are Phase 6. */
export function fixCommand(node: string, m: Missing): string {
  if (m.kind === 'l') return `kubectl label nodes ${node} ${m.key}=${m.val}${m.conflict !== null ? ' --overwrite' : ''}`
  return `kubectl taint nodes ${node} ${m.id} --overwrite`
}

/** "workload=general, taint dedicated=dremio:NoSchedule", or "a list of 3 nodes". */
export function reqText(f: Pick<Fleet, 'labels' | 'taints' | 'kind' | 'nodes'>): string {
  if (isList(f)) return `a list of ${nWord(f.nodes?.length ?? 0, 'node', 'nodes')}`
  const parts = [...f.labels.map((l) => labelText(...splitLabel(l))), ...f.taints.map((t) => 'taint ' + t)]
  return parts.length ? parts.join(', ') : 'no requirements'
}

/** The selector kubectl takes: k=v, a bare k for an empty value. Taints have none. */
export function selectorText(labels: string[]): string {
  const byKey = new Map<string, string[]>()
  for (const l of labels) {
    const [k, v] = splitLabel(l)
    byKey.set(k, [...(byKey.get(k) ?? []), v])
  }
  return [...byKey.entries()].map(([k, vs]) => (vs.length > 1 ? `${k} in (${vs.join(',')})` : vs[0] ? `${k}=${vs[0]}` : k)).join(',')
}

export interface Req {
  text: string
  effect?: string
  tone?: string
  /** For removing it in the editor. */
  id: string
  kind: 'l' | 't'
}

export function reqsOf(f: Pick<Fleet, 'labels' | 'taints'>): Req[] {
  return [
    ...f.labels.map((l): Req => ({ text: labelText(...splitLabel(l)), id: l, kind: 'l' })),
    ...f.taints.map((t): Req => {
      const s = splitTaint(t)
      return { text: s.key + (s.value ? '=' + s.value : ''), effect: s.effect, tone: effectTone(s.effect), id: t, kind: 't' }
    }),
  ]
}

/** "1 node with errors · 2 with warnings", "3 cordoned" or "". */
export function problemsText(s: FleetStats): { text: string; tone: string } {
  if (s.errors) return { text: `${nWord(s.errors, 'node', 'nodes')} with errors${s.warnings ? ` · ${s.warnings} with warnings` : ''}`, tone: 'err' }
  if (s.warnings) return { text: `${nWord(s.warnings, 'node', 'nodes')} with warnings`, tone: 'warn' }
  if (s.cordoned) return { text: `${s.cordoned} cordoned`, tone: 'warn' }
  return { text: '', tone: 'muted' }
}

/** "96 cores · 376.8Gi · 74% of CPU and 64% of memory requested · 20 pods". */
export function metaText(s: FleetStats): string {
  if (!s.nodes) return 'no node has all of these labels and taints yet'
  return `${fCores(s.cpu)} cores · ${fMem(s.memory)} · ${pctTxt(pct(s.cpuReq, s.cpu))} of CPU and ${pctTxt(pct(s.memoryReq, s.memory))} of memory requested · ${nWord(s.pods, 'pod', 'pods')}`
}

/** The aside's capacity line. */
export function capText(s: FleetStats): { text: string; tip: string; pct: number } {
  if (!s.nodes) return { text: 'no nodes yet', tip: 'No nodes', pct: 0 }
  const p = pct(s.cpuReq, s.cpu)
  return { text: `${fCores(s.cpu)} cores · ${pctTxt(p)} CPU requested`, tip: `${fCores(s.cpuReq)} of ${fCores(s.cpu)} cores requested`, pct: Math.min(100, p) }
}

/** "a, b and c". */
export function listText(items: string[]): string {
  if (items.length <= 1) return items.join('')
  return items.slice(0, -1).join(', ') + ' and ' + items[items.length - 1]
}

/** The first colour no fleet uses, or the next in turn. */
export function nextColor(fleets: Fleet[]): number {
  for (let c = 0; c < 6; c++) if (!fleets.some((f) => f.color === c)) return c
  return fleets.length % 6
}

/** An id for a new fleet, from its name, unique among the others. */
export function newId(name: string, fleets: Fleet[]): string {
  const base =
    name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .slice(0, 40) || 'fleet'
  let id = base
  for (let i = 2; fleets.some((f) => f.id === id); i++) id = `${base}-${i}`
  return id
}

/** Reads a typed requirement: key=value for a label, key=value:Effect for a taint. */
export function parseReq(s: string): { label?: string; taint?: string; error?: string } {
  const m = /^([^=:\s]+)(?:=([^:\s]*))?(?::(\w+))?$/.exec(s.trim())
  if (!m) return { error: 'Write key=value for a label, or key=value:NoSchedule for a taint.' }
  const key = m[1]!
  const val = m[2] ?? ''
  const eff = m[3]
  if (!LABEL_KEY_RE.test(key)) return { error: `“${key}” isn’t a valid label or taint key.` }
  if (!LABEL_VAL_RE.test(val)) return { error: `“${val}” isn’t a valid value: up to 63 letters, digits, dashes, dots and underscores.` }
  if (eff !== undefined) {
    if (!TAINT_EFFECTS.includes(eff)) return { error: 'A taint effect is NoSchedule, PreferNoSchedule or NoExecute.' }
    return { taint: key + (val ? '=' + val : '') + ':' + eff }
  }
  return { label: key + '=' + val }
}

/** Adds a label to a draft, replacing another value of its key: a fleet takes one value per key. */
export function withLabel(labels: string[], l: string): string[] {
  const [k] = splitLabel(l)
  return [...labels.filter((x) => splitLabel(x)[0] !== k), l]
}

// ---------- the matrix ----------

export interface MxColumn {
  id: string
  kind: 'r' | 'l' | 't'
  /** The bold part of the header, and the dimmer key prefix under it. */
  main: string
  pre: string
  full: string
  /** What a pick of it selects: a label "k=v" or a taint id. */
  fx: string
  key: string
  first: boolean
  /** For sort tips: "workload=general", "taint …", "Control plane role". */
  label: string
  effect?: string
}

const MX_ROLES: [string, string][] = [
  ['control-plane', 'Control plane'],
  ['worker', 'Worker'],
]

function splitKey(k: string): [string, string] {
  const i = k.lastIndexOf('/')
  return i < 0 ? ['', k] : [k.slice(0, i + 1), k.slice(i + 1)]
}

/** One column per role, per label value and per taint, across every node. */
export function mxColumns(nodes: NodeRow[]): MxColumn[] {
  const out: MxColumn[] = MX_ROLES.map(([role, head]) => ({
    id: 'r:' + role,
    kind: 'r',
    main: head,
    pre: '',
    full: `Role ${head.toLowerCase()} (label node-role.kubernetes.io/${role})`,
    fx: `node-role.kubernetes.io/${role}=`,
    key: `node-role.kubernetes.io/${role}`,
    first: false,
    label: `${head} role`,
  }))
  const labels = new Set<string>()
  const taints = new Map<string, string>()
  for (const n of nodes) {
    for (const [k, v] of Object.entries(n.labels)) if (!k.startsWith('node-role.kubernetes.io/')) labels.add(k + '=' + v)
    for (const t of n.taints) taints.set(taintId(t), t.effect)
  }
  let lastKey = ''
  for (const l of [...labels].sort((a, b) => {
    const [ak, av] = splitLabel(a)
    const [bk, bv] = splitLabel(b)
    return ak.localeCompare(bk) || av.localeCompare(bv, undefined, { numeric: true })
  })) {
    const [k, v] = splitLabel(l)
    const [pre, tail] = splitKey(k)
    out.push({ id: 'l:' + l, kind: 'l', main: tail + (v ? '=' + v : ''), pre, full: 'Label ' + labelText(k, v), fx: l, key: k, first: k !== lastKey, label: labelText(k, v) })
    lastKey = k
  }
  for (const [id, effect] of [...taints.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
    const t = splitTaint(id)
    const [pre, tail] = splitKey(t.key)
    out.push({ id: 't:' + id, kind: 't', main: tail + (t.value ? '=' + t.value : '') + ':' + t.effect, pre, full: 'Taint ' + id, fx: id, key: t.key, first: false, label: 'taint ' + id, effect })
  }
  return out
}

/** Whether a node has a column's role, label value or taint. */
export function mxHas(n: NodeRow, c: MxColumn): boolean {
  if (c.kind === 'r') return c.key in n.labels
  if (c.kind === 'l') {
    const [k, v] = splitLabel(c.fx)
    return k in n.labels && n.labels[k] === v
  }
  return n.taints.some((t) => taintId(t) === c.fx)
}

/** Whether a column is one of a fleet's requirements. */
export function inDefinition(c: MxColumn, f: Pick<Fleet, 'labels' | 'taints'> | undefined): boolean {
  if (!f) return false
  if (c.kind === 't') return f.taints.includes(c.fx)
  if (c.kind === 'l') return f.labels.includes(c.fx)
  return f.labels.some((l) => splitLabel(l)[0] === c.key)
}

export interface Preset {
  id: string
  label: string
  tip: string
  cols: string[]
}

/** The Columns panel's quick choices: each hides a set of columns. */
export function presets(cols: MxColumn[], nodes: NodeRow[], fleets: Pick<Fleet, 'labels' | 'taints'>[]): Preset[] {
  const used = new Set<string>()
  for (const f of fleets) {
    for (const l of f.labels) used.add('l:' + l)
    for (const t of f.taints) used.add('t:' + t)
  }
  const every = (c: MxColumn) => nodes.length > 0 && nodes.every((n) => mxHas(n, c))
  return [
    { id: 'sys', label: 'Hide kubernetes.io labels', tip: 'kubernetes.io, beta.kubernetes.io and node.kubernetes.io labels', cols: cols.filter((c) => c.kind === 'l' && /^(beta\.kubernetes\.io|kubernetes\.io|node\.kubernetes\.io)\//.test(c.key)).map((c) => c.id) },
    { id: 'host', label: 'Hide hostnames', tip: 'One kubernetes.io/hostname column per node', cols: cols.filter((c) => c.kind === 'l' && c.key === 'kubernetes.io/hostname').map((c) => c.id) },
    { id: 'every', label: 'Hide columns every node has', tip: 'Labels and taints that every node shares tell no node apart', cols: cols.filter(every).map((c) => c.id) },
    { id: 'def', label: 'Only what defines a fleet', tip: 'Hides every label and taint that no fleet uses; roles stay', cols: cols.filter((c) => c.kind !== 'r' && !used.has(c.id)).map((c) => c.id) },
  ]
}

/** The rows of a selection: no selection is every node, ["none"] those in no fleet, else the selected fleets' members. */
export function scopeNames(sel: string[], rows: FleetRow[], all: string[]): string[] {
  if (sel.length === 0) return all
  if (sel[0] === 'none') return rows.find((r) => r.kind === 'none')?.members ?? []
  const out = new Set<string>()
  for (const r of rows) if (r.kind === 'fleet' && r.id && sel.includes(r.id)) for (const m of r.members) out.add(m)
  return [...out]
}

/** The fleets a node is in, in their order. */
export function fleetsOfNode(name: string, rows: FleetRow[]): FleetRow[] {
  return rows.filter((r) => r.kind === 'fleet' && r.members.includes(name)).sort((a, b) => a.order - b.order)
}

export function fleetOf(r: FleetRow): Fleet {
  const f: Fleet = { id: r.id ?? '', name: r.name ?? '', color: r.color, desc: r.desc ?? '', labels: r.labels, taints: r.taints, ...(r.deploy ? { deploy: r.deploy } : {}) }
  return r.by === 'nodes' ? { ...f, kind: 'nodes', nodes: r.listed ?? [] } : f
}

// ---------- nodes picked in the matrix ----------

/** The nodes picked in the matrix, per cluster, kept while the page is open (a tab switch keeps them). */
const picked = new Map<string, string[]>()

export function pickedNodes(cluster: string): string[] {
  return picked.get(cluster) ?? []
}

export function setPickedNodes(cluster: string, names: string[]): void {
  picked.set(cluster, names)
}

const names = (ns: Pick<NodeRow, 'name'>[]) => ns.map((n) => n.name).join(' ')

/**
 * The commands that bring nodes into a fleet (asked after 0.7.1): one per
 * label and taint, naming every node that lacks it. --overwrite where a
 * node has the key with another value, which takes it out of the fleets
 * that need the old one.
 */
export function joinCommands(f: Pick<Fleet, 'labels' | 'taints'>, nodes: Pick<NodeRow, 'name' | 'labels' | 'taints'>[]): string[] {
  const out: string[] = []
  for (const l of f.labels) {
    const [k, v] = splitLabel(l)
    const lack = nodes.filter((n) => !(k in n.labels) || n.labels[k] !== v)
    if (lack.length) out.push(`kubectl label nodes ${names(lack)} ${k}=${v}${lack.some((n) => k in n.labels) ? ' --overwrite' : ''}`)
  }
  for (const t of f.taints) {
    const s = splitTaint(t)
    const lack = nodes.filter((n) => !n.taints.some((x) => taintId(x) === t))
    if (lack.length) out.push(`kubectl taint nodes ${names(lack)} ${t}${lack.some((n) => n.taints.some((x) => x.key === s.key && x.effect === s.effect)) ? ' --overwrite' : ''}`)
  }
  return out
}

/** The commands that take a fleet's labels and taints off nodes: each from the nodes that have it. */
export function leaveCommands(f: Pick<Fleet, 'labels' | 'taints'>, nodes: Pick<NodeRow, 'name' | 'labels' | 'taints'>[]): string[] {
  const out: string[] = []
  for (const l of f.labels) {
    const [k, v] = splitLabel(l)
    const with_ = nodes.filter((n) => k in n.labels && n.labels[k] === v)
    if (with_.length) out.push(`kubectl label nodes ${names(with_)} ${k}-`)
  }
  for (const t of f.taints) {
    const s = splitTaint(t)
    const with_ = nodes.filter((n) => n.taints.some((x) => taintId(x) === t))
    if (with_.length) out.push(`kubectl taint nodes ${names(with_)} ${s.key}:${s.effect}-`)
  }
  return out
}

/** What a change to nodes' labels and taints means beyond the fleet: other fleets they leave, taints that keep pods off or evict them. */
export function joinNotes(f: Pick<Fleet, 'id' | 'labels' | 'taints'>, nodes: Pick<NodeRow, 'name' | 'labels' | 'taints'>[], rows: FleetRow[]): string[] {
  const out: string[] = []
  for (const l of f.labels) {
    const [k, v] = splitLabel(l)
    for (const n of nodes) {
      if (!(k in n.labels) || n.labels[k] === v) continue
      const old = `${k}=${n.labels[k]}`
      const leaving = rows.filter((r) => r.kind === 'fleet' && r.id !== f.id && r.labels.includes(old) && r.members.includes(n.name)).map((r) => r.name!)
      out.push(`${n.name}: ${k} changes from ${n.labels[k] || '""'} to ${v || '""'}${leaving.length ? `, so it leaves ${listText(leaving)}` : ''}.`)
    }
  }
  for (const t of f.taints) {
    const s = splitTaint(t)
    if (!nodes.some((n) => !n.taints.some((x) => taintId(x) === t))) continue
    if (s.effect === 'NoExecute') out.push(`The taint ${t} evicts the pods on these nodes that do not tolerate it. Watson's card lists them before anyone approves.`)
    else if (s.effect === 'NoSchedule') out.push(`With the taint ${t}, new pods that do not tolerate it are not scheduled on these nodes; running pods stay.`)
    else out.push(`With the taint ${t}, the scheduler avoids these nodes for pods that do not tolerate it.`)
  }
  return out
}

/** What taking a fleet's labels and taints off nodes means: other fleets that need them lose the nodes too, and a label Kubernetes sets may come back. */
export function leaveNotes(f: Pick<Fleet, 'id' | 'labels' | 'taints'>, nodes: Pick<NodeRow, 'name'>[], rows: FleetRow[]): string[] {
  const out: string[] = []
  const gone = new Set([...f.labels, ...f.taints])
  const inSel = new Set(nodes.map((n) => n.name))
  const others = rows.filter((r) => r.kind === 'fleet' && r.id !== f.id && [...r.labels, ...r.taints].some((x) => gone.has(x)) && r.members.some((m) => inSel.has(m)))
  if (others.length) out.push(`${listText(others.map((r) => r.name!))} ${others.length === 1 ? 'needs' : 'need'} some of the same labels or taints, so ${nodes.length === 1 ? 'the node leaves' : 'these nodes leave'} ${others.length === 1 ? 'it' : 'them'} too.`)
  const sys = f.labels.map((l) => splitLabel(l)[0]).filter((k) => /(^|\.)(kubernetes\.io|k8s\.io)\//.test(k))
  if (sys.length) out.push(`${listText(sys)} ${sys.length === 1 ? 'is a label' : 'are labels'} Kubernetes or the kubelet may set; the kubelet can put ${sys.length === 1 ? 'it' : 'them'} back, and workloads may select on ${sys.length === 1 ? 'it' : 'them'}.`)
  if (f.labels.length) out.push('Pods already running stay; new pods that select these labels no longer land on these nodes.')
  return out
}

/** Labels that these nodes, and no other node, have: a new fleet can be defined by one without changing a node. */
export function ownLabels(sel: Pick<NodeRow, 'name' | 'labels'>[], all: Pick<NodeRow, 'name' | 'labels'>[]): string[] {
  if (!sel.length) return []
  const inSel = new Set(sel.map((n) => n.name))
  const first = sel[0]!
  return Object.entries(first.labels)
    .filter(([k, v]) => sel.every((n) => k in n.labels && n.labels[k] === v) && all.every((n) => inSel.has(n.name) || !(k in n.labels) || n.labels[k] !== v))
    .map(([k, v]) => `${k}=${v}`)
    .sort((a, b) => Number(a.startsWith('kubernetes.io/hostname=')) - Number(b.startsWith('kubernetes.io/hostname=')) || a.localeCompare(b))
}

/** A label value from a fleet's name: lowercase, dashes, at most 63 characters. */
export function labelValueOf(name: string): string {
  return (
    name
      .toLowerCase()
      .replace(/[^a-z0-9._-]+/g, '-')
      .replace(/^[^a-z0-9]+|[^a-z0-9]+$/g, '')
      .slice(0, 63)
      .replace(/[^a-z0-9]+$/, '') || 'fleet'
  )
}
