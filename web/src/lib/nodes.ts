// What the Nodes tab computes from the rows: filtering, sorting, the
// columns and the exports. Ported from the prototype's nodesView.

import type { NodeRow, Taint, Tone } from '../api/types'
import { compareNatural, compareValues, fCores, fMem, fmtTs, minutesSince, nWord, pct, pctTxt, round } from './format'
import type { Cell } from './xlsx'

export type ColumnId =
  | 'name'
  | 'role'
  | 'status'
  | 'problems'
  | 'cpu'
  | 'cpuAlloc'
  | 'cpuFree'
  | 'memory'
  | 'memAlloc'
  | 'memFree'
  | 'pods'
  | 'taints'
  | 'zone'
  | 'labels'
  | 'kubelet'
  | 'age'
export type SortDir = 'asc' | 'desc'

export interface Column {
  id: ColumnId
  label: string
  /** How the sort tooltip names the column. */
  sortName: string
  /** Grid track when all columns show, and when the details panel is open. */
  width: string
  compactWidth: string
  /** Minimum width in px, for the table's min-width. */
  min: number
  compactMin: number
  right?: boolean
  /** Dropped while the details panel is open, as in the prototype. */
  wideOnly?: boolean
  /** Off until picked in the Columns menu. */
  hiddenByDefault?: boolean
  defaultDir: SortDir
}

export const COLUMNS: Column[] = [
  { id: 'name', label: 'Node', sortName: 'node name', width: 'minmax(180px, 1.4fr)', compactWidth: 'minmax(160px, 1.3fr)', min: 180, compactMin: 160, defaultDir: 'asc' },
  { id: 'role', label: 'Role', sortName: 'role (control plane first)', width: '80px', compactWidth: '76px', min: 80, compactMin: 76, defaultDir: 'asc' },
  { id: 'status', label: 'Status', sortName: 'status (problems first)', width: '100px', compactWidth: '96px', min: 100, compactMin: 96, defaultDir: 'desc' },
  { id: 'problems', label: 'Problems', sortName: 'problems (errors first)', width: '106px', compactWidth: '100px', min: 106, compactMin: 100, defaultDir: 'desc' },
  { id: 'cpu', label: 'CPU requested', sortName: 'CPU requested (% of allocatable)', width: '164px', compactWidth: '154px', min: 164, compactMin: 154, defaultDir: 'desc' },
  { id: 'cpuAlloc', label: 'CPU allocatable', sortName: 'CPU allocatable to pods', width: '104px', compactWidth: '96px', min: 104, compactMin: 96, right: true, wideOnly: true, defaultDir: 'desc' },
  { id: 'cpuFree', label: 'CPU free', sortName: 'CPU free to schedule', width: '82px', compactWidth: '76px', min: 82, compactMin: 76, right: true, wideOnly: true, defaultDir: 'desc' },
  { id: 'memory', label: 'Memory requested', sortName: 'memory requested (% of allocatable)', width: '174px', compactWidth: '164px', min: 174, compactMin: 164, defaultDir: 'desc' },
  { id: 'memAlloc', label: 'Memory allocatable', sortName: 'memory allocatable to pods', width: '124px', compactWidth: '112px', min: 124, compactMin: 112, right: true, wideOnly: true, defaultDir: 'desc' },
  { id: 'memFree', label: 'Memory free', sortName: 'memory free to schedule', width: '100px', compactWidth: '92px', min: 100, compactMin: 92, right: true, wideOnly: true, defaultDir: 'desc' },
  { id: 'pods', label: 'Pods', sortName: 'pod count', width: '84px', compactWidth: '78px', min: 84, compactMin: 78, right: true, defaultDir: 'desc' },
  { id: 'taints', label: 'Taints', sortName: 'taints (tainted nodes first)', width: 'minmax(150px, 1fr)', compactWidth: 'minmax(150px, 1fr)', min: 150, compactMin: 150, wideOnly: true, hiddenByDefault: true, defaultDir: 'asc' },
  { id: 'zone', label: 'Zone', sortName: 'zone', width: '96px', compactWidth: '96px', min: 96, compactMin: 96, wideOnly: true, hiddenByDefault: true, defaultDir: 'asc' },
  { id: 'labels', label: 'Labels', sortName: 'labels', width: 'minmax(170px, 1.2fr)', compactWidth: 'minmax(170px, 1.2fr)', min: 170, compactMin: 170, wideOnly: true, hiddenByDefault: true, defaultDir: 'asc' },
  { id: 'kubelet', label: 'Kubelet', sortName: 'kubelet version', width: '110px', compactWidth: '110px', min: 110, compactMin: 110, wideOnly: true, hiddenByDefault: true, defaultDir: 'asc' },
  { id: 'age', label: 'Age', sortName: 'age', width: '64px', compactWidth: '64px', min: 64, compactMin: 64, wideOnly: true, defaultDir: 'desc' },
]

export const COLUMN_BY_ID = Object.fromEntries(COLUMNS.map((c) => [c.id, c])) as Record<ColumnId, Column>

/** The columns hidden until picked: taints, zone, labels and kubelet. */
export const DEFAULT_HIDDEN: ReadonlySet<ColumnId> = new Set(COLUMNS.filter((c) => c.hiddenByDefault).map((c) => c.id))

/** The columns on screen: the user's choice, less the wide-only ones while the panel is open. */
export function visibleColumns(hidden: ReadonlySet<ColumnId>, compact: boolean): Column[] {
  return COLUMNS.filter((c) => c.id === 'name' || (!hidden.has(c.id) && !(compact && c.wideOnly)))
}

export function gridTemplate(cols: Column[], compact: boolean): { template: string; minWidth: number } {
  return {
    template: cols.map((c) => (compact ? c.compactWidth : c.width)).join(' '),
    minWidth: cols.reduce((s, c) => s + (compact ? c.compactMin : c.min), 0),
  }
}

export type RoleFilter = 'all' | 'control-plane' | 'worker'

export function taintId(t: Taint): string {
  return t.key + (t.value ? '=' + t.value : '') + ':' + t.effect
}

/** The short form the table shows: node-role.kubernetes.io/ is dropped. */
export function taintShort(t: Taint): string {
  return t.key.replace(/^node(-role)?\.kubernetes\.io\//, '') + (t.value ? '=' + t.value : '')
}

export function isSystemLabel(k: string): boolean {
  return /^(beta\.kubernetes\.io|kubernetes\.io|node\.kubernetes\.io)\//.test(k)
}

/** A node's own labels as key=value, sorted: kubernetes.io labels and roles (which have their own column) left out. */
export function ownLabels(r: NodeRow): string[] {
  return Object.keys(r.labels)
    .filter((k) => !isSystemLabel(k) && !k.startsWith('node-role.kubernetes.io/'))
    .sort()
    .map((k) => (r.labels[k] ? `${k}=${r.labels[k]}` : k))
}

/** Matches the name, a label as key=value, or a taint as key=value:Effect. */
export function matches(r: NodeRow, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  if (r.name.toLowerCase().includes(q)) return true
  if (Object.entries(r.labels).some(([k, v]) => `${k}=${v}`.toLowerCase().includes(q))) return true
  return r.taints.some((t) => `${t.key}=${t.value ?? ''}:${t.effect}`.toLowerCase().includes(q))
}

export function filterRows(rows: NodeRow[], role: RoleFilter, query: string): NodeRow[] {
  return rows.filter((r) => (role === 'all' || r.role === role) && matches(r, query))
}

const toneRank: Record<Tone, number> = { err: 3, warn: 2, info: 1, muted: 0, ok: 0 }

/** Allocatable less requested: what the scheduler can still place. */
export function free(res: { allocatable: number; requested: number }): number {
  return Math.max(0, res.allocatable - res.requested)
}

function sortValue(r: NodeRow, col: ColumnId, now: number): number | string {
  switch (col) {
    case 'name':
      return r.name
    case 'role':
      return r.role === 'control-plane' ? 0 : 1
    case 'status':
      return toneRank[r.tone] ?? 0
    case 'problems':
      return r.errors * 10 + r.warnings
    case 'cpu':
      return pct(r.cpu.requested, r.cpu.allocatable)
    case 'cpuAlloc':
      return r.cpu.allocatable
    case 'cpuFree':
      return free(r.cpu)
    case 'memory':
      return pct(r.memory.requested, r.memory.allocatable)
    case 'memAlloc':
      return r.memory.allocatable
    case 'memFree':
      return free(r.memory)
    case 'pods':
      return r.pods.running
    case 'taints':
      return r.taints.length ? '0 ' + r.taints.map(taintId).sort().join(',') : '1'
    case 'zone':
      return r.zone
    case 'labels':
      return ownLabels(r).join(',')
    case 'kubelet':
      return r.kubelet
    case 'age':
      return minutesSince(r.created, now)
  }
}

/** Sorts by a column; ties fall back to the node name. */
export function sortRows(rows: NodeRow[], col: ColumnId, dir: SortDir, now: number): NodeRow[] {
  const sign = dir === 'asc' ? 1 : -1
  const keyed = rows.map((r) => ({ r, v: sortValue(r, col, now) }))
  keyed.sort((a, b) => compareValues(a.v, b.v) * sign || compareNatural(a.r.name, b.r.name))
  return keyed.map((k) => k.r)
}

export interface ProblemsCell {
  text: string
  tone: Tone
  tip: string
}

/** "1 error · 2 warn", "3 warnings" or "—". */
export function problemsCell(r: NodeRow): ProblemsCell {
  const e = r.errors
  const w = r.warnings
  const text = e ? `${nWord(e, 'error', 'errors')}${w ? ` · ${w} warn` : ''}` : w ? nWord(w, 'warning', 'warnings') : '—'
  return { text, tone: e ? 'err' : w ? 'warn' : 'muted', tip: r.problems.map((p) => p.title).join('\n') || 'No known problems' }
}

/** The short role tag: control-plane nodes are "cp", as in many node names. */
export function roleTag(r: NodeRow): { text: string; title: string } {
  if (r.role === 'control-plane') {
    const extra = r.roles.filter((x) => x !== 'control-plane' && x !== 'master')
    return { text: 'cp', title: 'Control plane' + (extra.length ? ` (also ${extra.join(', ')})` : '') }
  }
  return { text: 'worker', title: r.roles.length ? `Worker (${r.roles.join(', ')})` : 'Worker' }
}

export interface Meter {
  reqPct: number
  usePct: number
  text: string
  sub: string
  tip: string
}

export function cpuMeter(r: NodeRow): Meter {
  const req = pct(r.cpu.requested, r.cpu.allocatable)
  const use = r.cpu.used == null ? null : pct(r.cpu.used, r.cpu.allocatable)
  return {
    reqPct: req,
    usePct: use ?? 0,
    text: pctTxt(req),
    sub: fCores(r.cpu.requested),
    tip:
      `CPU requested ${fCores(r.cpu.requested)} of ${fCores(r.cpu.allocatable)} cores (${pctTxt(req)}); ${fCores(free(r.cpu))} free to schedule` +
      (use == null ? '; in use unknown' : `; in use ${fCores(r.cpu.used ?? 0)} (${pctTxt(use)})`),
  }
}

export function memoryMeter(r: NodeRow): Meter {
  const req = pct(r.memory.requested, r.memory.allocatable)
  const use = r.memory.used == null ? null : pct(r.memory.used, r.memory.allocatable)
  return {
    reqPct: req,
    usePct: use ?? 0,
    text: pctTxt(req),
    sub: fMem(r.memory.requested),
    tip:
      `Memory requested ${fMem(r.memory.requested)} of ${fMem(r.memory.allocatable)} (${pctTxt(req)}); ${fMem(free(r.memory))} free to schedule` +
      (use == null ? '; in use unknown' : `; in use ${fMem(r.memory.used)} (${pctTxt(use)})`),
  }
}

const GIB = 1024 * 1024 * 1024

interface ExportColumn {
  head: string
  cell: (r: NodeRow) => Cell
  /** For JSON, when a value is a list rather than lines of text. */
  json?: (r: NodeRow) => unknown
}

/**
 * The export columns: each column on screen, in screen order, becomes one
 * or more. CPU and memory carry their numbers rather than the bar.
 */
export function exportColumns(cols: Column[]): ExportColumn[] {
  const out: ExportColumn[] = []
  const add = (head: string, cell: (r: NodeRow) => Cell, json?: (r: NodeRow) => unknown) => out.push({ head, cell, json })
  for (const c of cols) {
    switch (c.id) {
      case 'name':
        add('Node', (r) => r.name)
        break
      case 'role':
        add('Role', (r) => (r.role === 'control-plane' ? 'Control plane' : 'Worker'))
        break
      case 'status':
        add('Status', (r) => r.status)
        break
      case 'problems':
        add(
          'Problems',
          (r) => r.problems.map((p) => p.title).join('\n'),
          (r) => r.problems.map((p) => p.title),
        )
        break
      case 'cpu':
        add('CPU requested (cores)', (r) => round(r.cpu.requested / 1000, 3))
        add('CPU requested %', (r) => round(pct(r.cpu.requested, r.cpu.allocatable), 1))
        add('CPU in use (cores)', (r) => (r.cpu.used == null ? null : round(r.cpu.used / 1000, 3)))
        break
      case 'cpuAlloc':
        add('CPU allocatable (cores)', (r) => round(r.cpu.allocatable / 1000, 3))
        break
      case 'cpuFree':
        add('CPU free (cores)', (r) => round(free(r.cpu) / 1000, 3))
        break
      case 'memory':
        add('Memory requested (GiB)', (r) => round(r.memory.requested / GIB, 2))
        add('Memory requested %', (r) => round(pct(r.memory.requested, r.memory.allocatable), 1))
        add('Memory in use (GiB)', (r) => (r.memory.used == null ? null : round(r.memory.used / GIB, 2)))
        break
      case 'memAlloc':
        add('Memory allocatable (GiB)', (r) => round(r.memory.allocatable / GIB, 2))
        break
      case 'memFree':
        add('Memory free (GiB)', (r) => round(free(r.memory) / GIB, 2))
        break
      case 'pods':
        add('Pods', (r) => r.pods.running)
        add('Pod capacity', (r) => r.pods.capacity)
        break
      case 'taints':
        add(
          'Taints',
          (r) => r.taints.map(taintId).join('\n'),
          (r) => r.taints.map(taintId),
        )
        break
      case 'zone':
        add('Zone', (r) => r.zone)
        break
      case 'labels':
        add(
          'Labels',
          (r) => ownLabels(r).join('\n'),
          (r) => ownLabels(r),
        )
        break
      case 'kubelet':
        add('Kubelet', (r) => r.kubelet)
        break
      case 'age':
        add('Joined', (r) => fmtTs(r.created))
        break
    }
  }
  return out
}

/** The Excel export: a header row and one row per node. */
export function exportSheet(rows: NodeRow[], cols: Column[]): { head: string[]; rows: Cell[][] } {
  const ec = exportColumns(cols)
  return { head: ec.map((c) => c.head), rows: rows.map((r) => ec.map((c) => c.cell(r))) }
}

/** The JSON export: one object per node, keyed like the sheet's headers, lists as arrays. */
export function exportJSON(rows: NodeRow[], cols: Column[]): Record<string, unknown>[] {
  const ec = exportColumns(cols)
  return rows.map((r) => Object.fromEntries(ec.map((c) => [c.head, c.json ? c.json(r) : c.cell(r)])))
}

/** The note under a node's taints, from the prototype. */
export function taintNote(r: NodeRow): string {
  if (!r.taints.length) return 'No taints — any pod can be scheduled here.'
  if (r.cordoned) return 'Cordoned: no new pods land here. Pods already running stay until the node is drained.'
  if (r.role === 'control-plane') return 'The control-plane taint keeps ordinary workloads off this node; only pods that tolerate it — control-plane components, DaemonSets and a few add-ons — run here.'
  return 'Only pods that tolerate these taints can be scheduled here.'
}

export function effectTone(effect: string): Tone {
  return effect === 'NoExecute' ? 'err' : effect === 'NoSchedule' ? 'warn' : 'info'
}

/** A line of a resource section in the node details. */
export interface ResourceLine {
  k: string
  v: string
  sub?: string
  tone?: Tone
}

export interface ResourceSection {
  label: string
  reqPct: number
  usePct: number
  useTone: Tone
  lines: ResourceLine[]
  note: string
  noteTone: Tone
}

/** The CPU, Memory and Pods sections of the node details. */
export function resourceSections(r: NodeRow, metrics: boolean, metricsNote: string): ResourceSection[] {
  const usage = metrics && r.cpu.used != null && r.memory.used != null
  const noUsage = metrics && !usage ? 'metrics-server has no reading for this node.' : metricsNote || 'CPU and memory in use are not available.'
  const cR = pct(r.cpu.requested, r.cpu.allocatable)
  const cU = usage ? pct(r.cpu.used ?? 0, r.cpu.allocatable) : 0
  const cL = pct(r.cpu.limits, r.cpu.allocatable)
  const mR = pct(r.memory.requested, r.memory.allocatable)
  const mU = usage ? pct(r.memory.used ?? 0, r.memory.allocatable) : 0
  const mL = pct(r.memory.limits, r.memory.allocatable)
  const p = r.pods
  const notReady: string[] = []
  if (p.failing) notReady.push(`${p.failing} failing`)
  if (p.starting) notReady.push(`${p.starting} still starting`)
  if (p.unready) notReady.push(`${p.unready} failing readiness checks`)
  return [
    {
      label: 'CPU',
      reqPct: cR,
      usePct: cU,
      useTone: cU > 85 ? 'err' : 'ok',
      lines: [
        { k: 'Allocatable', v: `${fCores(r.cpu.allocatable)} cores`, sub: r.cpu.capacity > r.cpu.allocatable ? `of ${fCores(r.cpu.capacity)} on the machine` : '' },
        { k: 'Requested', v: fCores(r.cpu.requested), sub: pctTxt(cR) },
        { k: 'Free to schedule', v: fCores(free(r.cpu)), sub: pctTxt(100 - Math.min(100, cR)) },
        { k: 'In use', v: usage ? fCores(r.cpu.used ?? 0) : '—', sub: usage ? pctTxt(cU) : '', tone: cU > 85 ? 'err' : undefined },
        { k: 'Limits', v: fCores(r.cpu.limits), sub: pctTxt(cL), tone: cL > 100 ? 'warn' : undefined },
      ],
      note: !usage ? noUsage : cL > 100 ? `CPU limits are overcommitted (${pctTxt(cL)} of allocatable) — expect throttling under load.` : '',
      noteTone: !usage ? 'muted' : 'warn',
    },
    {
      label: 'Memory',
      reqPct: mR,
      usePct: mU,
      useTone: mU > 85 ? 'err' : 'ok',
      lines: [
        { k: 'Allocatable', v: fMem(r.memory.allocatable), sub: r.memory.capacity > r.memory.allocatable ? `of ${fMem(r.memory.capacity)} on the machine` : '' },
        { k: 'Requested', v: fMem(r.memory.requested), sub: pctTxt(mR) },
        { k: 'Free to schedule', v: fMem(free(r.memory)), sub: pctTxt(100 - Math.min(100, mR)) },
        { k: 'In use', v: usage ? fMem(r.memory.used) : '—', sub: usage ? pctTxt(mU) : '', tone: mU > 85 ? 'err' : undefined },
        { k: 'Limits', v: fMem(r.memory.limits), sub: pctTxt(mL), tone: mL > 100 ? 'warn' : undefined },
      ],
      note: !usage
        ? ''
        : mL > 100
          ? `Memory limits exceed allocatable (${pctTxt(mL)}) — pods can be OOM-killed if they all grow at once.`
          : mR > 75
            ? `Less than ${pctTxt(100 - mR)} of memory is left for new requests.`
            : '',
      noteTone: 'warn',
    },
    {
      label: 'Pods',
      reqPct: pct(p.running, p.capacity),
      usePct: 0,
      useTone: 'ok',
      lines: [
        { k: 'Capacity', v: String(p.capacity), sub: 'pod slots' },
        { k: 'Scheduled', v: String(p.running), sub: pctTxt(pct(p.running, p.capacity)) },
        { k: 'Free slots', v: String(Math.max(0, p.capacity - p.running)) },
        { k: 'Not ready', v: String(p.notReady), sub: notReady.join(', '), tone: p.failing ? 'err' : p.notReady ? 'warn' : undefined },
        { k: 'Completed', v: String(p.completed), sub: p.completed ? 'take no slot' : '' },
      ],
      note: '',
      noteTone: 'muted',
    },
  ]
}
