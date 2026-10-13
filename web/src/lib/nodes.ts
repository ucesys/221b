// What the Nodes tab computes from the rows: filtering, sorting, the
// summary line, the capacity cards and the Excel export. Ported from the
// prototype's nodesView and nodeCards.

import type { NodeRow, Taint, Tone } from '../api/types'
import { compareNatural, compareValues, fCores, fMem, fmtTs, minutesSince, nWord, pct, pctTxt, round } from './format'
import type { Cell } from './xlsx'

export type ColumnId = 'name' | 'status' | 'problems' | 'cpu' | 'memory' | 'pods' | 'taints' | 'zone' | 'kubelet' | 'age'
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
  defaultDir: SortDir
}

export const COLUMNS: Column[] = [
  { id: 'name', label: 'Node', sortName: 'node name', width: 'minmax(190px, 1.4fr)', compactWidth: 'minmax(170px, 1.3fr)', min: 190, compactMin: 170, defaultDir: 'asc' },
  { id: 'status', label: 'Status', sortName: 'status (problems first)', width: '108px', compactWidth: '104px', min: 108, compactMin: 104, defaultDir: 'desc' },
  { id: 'problems', label: 'Problems', sortName: 'problems (errors first)', width: '112px', compactWidth: '108px', min: 112, compactMin: 108, defaultDir: 'desc' },
  { id: 'cpu', label: 'CPU requested', sortName: 'CPU requested (% of allocatable)', width: '206px', compactWidth: '196px', min: 206, compactMin: 196, defaultDir: 'desc' },
  { id: 'memory', label: 'Memory requested', sortName: 'memory requested (% of allocatable)', width: '232px', compactWidth: '222px', min: 232, compactMin: 222, defaultDir: 'desc' },
  { id: 'pods', label: 'Pods', sortName: 'pod count', width: '88px', compactWidth: '80px', min: 88, compactMin: 80, right: true, defaultDir: 'desc' },
  { id: 'taints', label: 'Taints', sortName: 'taints (tainted nodes first)', width: 'minmax(150px, 1fr)', compactWidth: 'minmax(150px, 1fr)', min: 150, compactMin: 150, wideOnly: true, defaultDir: 'asc' },
  { id: 'zone', label: 'Zone', sortName: 'zone', width: '96px', compactWidth: '96px', min: 96, compactMin: 96, wideOnly: true, defaultDir: 'asc' },
  { id: 'kubelet', label: 'Kubelet', sortName: 'kubelet version', width: '150px', compactWidth: '150px', min: 150, compactMin: 150, wideOnly: true, defaultDir: 'asc' },
  { id: 'age', label: 'Age', sortName: 'age', width: '64px', compactWidth: '64px', min: 64, compactMin: 64, wideOnly: true, defaultDir: 'desc' },
]

export const COLUMN_BY_ID = Object.fromEntries(COLUMNS.map((c) => [c.id, c])) as Record<ColumnId, Column>

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

function sortValue(r: NodeRow, col: ColumnId, now: number): number | string {
  switch (col) {
    case 'name':
      return r.name
    case 'status':
      return toneRank[r.tone] ?? 0
    case 'problems':
      return r.errors * 10 + r.warnings
    case 'cpu':
      return pct(r.cpu.requested, r.cpu.allocatable)
    case 'memory':
      return pct(r.memory.requested, r.memory.allocatable)
    case 'pods':
      return r.pods.running
    case 'taints':
      return r.taints.length ? '0 ' + r.taints.map(taintId).sort().join(',') : '1'
    case 'zone':
      return r.zone
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

export interface GroupStats {
  controlPlane: number
  workers: number
  pods: number
  cordoned: number
  withErrors: number
  withWarnings: number
}

export function groupStats(rows: NodeRow[]): GroupStats {
  const s: GroupStats = { controlPlane: 0, workers: 0, pods: 0, cordoned: 0, withErrors: 0, withWarnings: 0 }
  for (const r of rows) {
    if (r.role === 'control-plane') s.controlPlane++
    else s.workers++
    s.pods += r.pods.running
    if (r.cordoned) s.cordoned++
    if (r.errors) s.withErrors++
    else if (r.warnings) s.withWarnings++
  }
  return s
}

/** The line under the heading, as in the prototype. */
export function summaryLine(all: NodeRow[], shown: NodeRow[]): string {
  const filtered = shown.length < all.length
  const s = groupStats(filtered ? shown : all)
  const tail =
    (s.cordoned ? ` · ${s.cordoned} cordoned` : '') +
    (s.withErrors ? ` · ${nWord(s.withErrors, 'node', 'nodes')} with errors` : '') +
    (s.withWarnings ? ` · ${s.withWarnings} with warnings` : '')
  if (!filtered) {
    return `${nWord(s.controlPlane, 'control-plane node', 'control-plane nodes')} · ${nWord(s.workers, 'worker', 'workers')} · ${nWord(s.pods, 'pod', 'pods')}${tail} — cluster totals below`
  }
  if (!shown.length) return 'No nodes match the filters below'
  return `Showing ${shown.length} of ${all.length} nodes · ${nWord(s.pods, 'pod', 'pods')}${tail} — totals below are for these nodes`
}

export interface LegendItem {
  k: string
  v: string
  sub: string
  sw: 'req' | 'use' | 'lim' | 'none'
}

export interface Card {
  label: string
  total: string
  hero: string
  heroLabel: string
  reqPct: number
  usePct: number
  useTone: Tone
  legend: LegendItem[]
  tip: string
  note: string
  noteTone: Tone
}

/** The CPU, Memory and Pods cards; unscheduled counts pods waiting for a node. */
export function capacityCards(rows: NodeRow[], unscheduled: number, metrics: boolean, metricsNote: string): Card[] {
  const a = { cpuAlloc: 0, memAlloc: 0, podsCap: 0, cpuReq: 0, memReq: 0, cpuLim: 0, memLim: 0, cpuUse: 0, memUse: 0, live: 0, completed: 0, failing: 0, pending: 0, unready: 0, measured: 0 }
  for (const r of rows) {
    a.cpuAlloc += r.cpu.allocatable
    a.memAlloc += r.memory.allocatable
    a.podsCap += r.pods.capacity
    a.cpuReq += r.cpu.requested
    a.memReq += r.memory.requested
    a.cpuLim += r.cpu.limits
    a.memLim += r.memory.limits
    if (r.cpu.used != null && r.memory.used != null) {
      a.cpuUse += r.cpu.used
      a.memUse += r.memory.used
      a.measured++
    }
    a.live += r.pods.running
    a.completed += r.pods.completed
    a.failing += r.pods.failing
    a.pending += r.pods.starting
    a.unready += r.pods.unready
  }
  const cR = pct(a.cpuReq, a.cpuAlloc)
  const cU = pct(a.cpuUse, a.cpuAlloc)
  const cL = pct(a.cpuLim, a.cpuAlloc)
  const mR = pct(a.memReq, a.memAlloc)
  const mU = pct(a.memUse, a.memAlloc)
  const mL = pct(a.memLim, a.memAlloc)
  const usage = metrics && a.measured > 0
  const partial = usage && a.measured < rows.length ? ` Used counts the ${a.measured} of ${rows.length} nodes metrics-server reports on.` : ''
  const noUsage = metricsNote || 'CPU and memory in use are not available.'

  const notReady = a.failing + a.pending + a.unready + unscheduled
  const parts: string[] = []
  if (a.failing) parts.push(`${a.failing} failing`)
  if (a.pending + unscheduled) parts.push(`${a.pending + unscheduled} pending${unscheduled ? ` (${unscheduled} without a node)` : ''}`)
  if (a.unready) parts.push(`${a.unready} failing readiness checks`)
  const nrNote = notReady ? `${notReady} ${notReady === 1 ? 'pod is' : 'pods are'} not ready: ${parts.join(', ')}.` : ''

  return [
    {
      label: 'CPU',
      total: `${fCores(a.cpuAlloc)} cores allocatable`,
      hero: fCores(Math.max(0, a.cpuAlloc - a.cpuReq)),
      heroLabel: 'cores free to schedule',
      reqPct: cR,
      usePct: usage ? cU : 0,
      useTone: cU > 85 ? 'err' : 'ok',
      legend: [
        { k: 'Requested', v: fCores(a.cpuReq), sub: pctTxt(cR), sw: 'req' },
        { k: 'Used', v: usage ? fCores(a.cpuUse) : '—', sub: usage ? pctTxt(cU) : '', sw: 'use' },
        { k: 'Limits', v: fCores(a.cpuLim), sub: pctTxt(cL), sw: 'lim' },
      ],
      tip: 'Free to schedule = allocatable − requested. Used comes from metrics-server.' + partial,
      note: !usage ? noUsage : cL > 100 ? `CPU limits are overcommitted (${pctTxt(cL)} of allocatable) — expect throttling under load.` : '',
      noteTone: !usage ? 'muted' : 'warn',
    },
    {
      label: 'Memory',
      total: `${fMem(a.memAlloc)} allocatable`,
      hero: fMem(Math.max(0, a.memAlloc - a.memReq)),
      heroLabel: 'free to schedule',
      reqPct: mR,
      usePct: usage ? mU : 0,
      useTone: mU > 85 ? 'err' : 'ok',
      legend: [
        { k: 'Requested', v: fMem(a.memReq), sub: pctTxt(mR), sw: 'req' },
        { k: 'Used', v: usage ? fMem(a.memUse) : '—', sub: usage ? pctTxt(mU) : '', sw: 'use' },
        { k: 'Limits', v: fMem(a.memLim), sub: pctTxt(mL), sw: 'lim' },
      ],
      tip: 'Free to schedule = allocatable − requested.' + partial,
      note: !usage
        ? noUsage
        : mL > 100
          ? `Memory limits exceed allocatable (${pctTxt(mL)}) — pods can be OOM-killed if they all grow at once.`
          : mR > 75
            ? `Less than ${pctTxt(100 - mR)} of memory is left for new requests.`
            : '',
      noteTone: !usage ? 'muted' : 'warn',
    },
    {
      label: 'Pods',
      total: `${a.podsCap} max`,
      hero: String(Math.max(0, a.podsCap - a.live)),
      heroLabel: 'pod slots free',
      reqPct: pct(a.live, a.podsCap),
      usePct: 0,
      useTone: 'ok',
      legend: [
        { k: 'Scheduled', v: String(a.live), sub: '', sw: 'req' },
        { k: 'Not ready', v: String(notReady), sub: '', sw: 'none' },
        { k: 'Completed', v: String(a.completed), sub: '', sw: 'none' },
      ],
      tip: 'Completed pods do not use a slot.',
      note: nrNote,
      noteTone: a.failing ? 'err' : 'warn',
    },
  ]
}

/** The same cards for one node, for the details panel. */
export function nodeCards(r: NodeRow, metrics: boolean, metricsNote: string): Card[] {
  return capacityCards([r], 0, metrics && r.cpu.used != null, r.cpu.used == null && metrics ? 'metrics-server has no reading for this node.' : metricsNote)
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
    sub: `${fCores(r.cpu.requested)} / ${fCores(r.cpu.allocatable)}`,
    tip: `CPU requested ${fCores(r.cpu.requested)} of ${fCores(r.cpu.allocatable)} cores (${pctTxt(req)})` + (use == null ? ', in use unknown' : `, in use ${fCores(r.cpu.used ?? 0)} (${pctTxt(use)})`),
  }
}

export function memoryMeter(r: NodeRow): Meter {
  const req = pct(r.memory.requested, r.memory.allocatable)
  const use = r.memory.used == null ? null : pct(r.memory.used, r.memory.allocatable)
  return {
    reqPct: req,
    usePct: use ?? 0,
    text: pctTxt(req),
    sub: `${fMem(r.memory.requested)} / ${fMem(r.memory.allocatable)}`,
    tip: `Memory requested ${fMem(r.memory.requested)} of ${fMem(r.memory.allocatable)} (${pctTxt(req)})` + (use == null ? ', in use unknown' : `, in use ${fMem(r.memory.used)} (${pctTxt(use)})`),
  }
}

const GIB = 1024 * 1024 * 1024

/**
 * The export: each column on screen, in screen order, becomes one or more
 * sheet columns. CPU and memory carry their numbers rather than the bar.
 */
export function exportSheet(rows: NodeRow[], cols: Column[]): { head: string[]; rows: Cell[][] } {
  const head: string[] = []
  const getters: ((r: NodeRow) => Cell)[] = []
  const add = (h: string, f: (r: NodeRow) => Cell) => {
    head.push(h)
    getters.push(f)
  }
  for (const c of cols) {
    switch (c.id) {
      case 'name':
        add('Node', (r) => r.name)
        add('Role', (r) => (r.role === 'control-plane' ? 'Control plane' : 'Worker'))
        break
      case 'status':
        add('Status', (r) => r.status)
        break
      case 'problems':
        add('Problems', (r) => r.problems.map((p) => p.title).join('\n'))
        break
      case 'cpu':
        add('CPU requested (cores)', (r) => round(r.cpu.requested / 1000, 3))
        add('CPU allocatable (cores)', (r) => round(r.cpu.allocatable / 1000, 3))
        add('CPU requested %', (r) => round(pct(r.cpu.requested, r.cpu.allocatable), 1))
        add('CPU in use (cores)', (r) => (r.cpu.used == null ? null : round(r.cpu.used / 1000, 3)))
        break
      case 'memory':
        add('Memory requested (GiB)', (r) => round(r.memory.requested / GIB, 2))
        add('Memory allocatable (GiB)', (r) => round(r.memory.allocatable / GIB, 2))
        add('Memory requested %', (r) => round(pct(r.memory.requested, r.memory.allocatable), 1))
        add('Memory in use (GiB)', (r) => (r.memory.used == null ? null : round(r.memory.used / GIB, 2)))
        break
      case 'pods':
        add('Pods', (r) => r.pods.running)
        add('Pod capacity', (r) => r.pods.capacity)
        break
      case 'taints':
        add('Taints', (r) => r.taints.map(taintId).join('\n'))
        break
      case 'zone':
        add('Zone', (r) => r.zone)
        break
      case 'kubelet':
        add('Kubelet', (r) => r.kubelet)
        break
      case 'age':
        add('Joined', (r) => fmtTs(r.created))
        break
    }
  }
  return { head, rows: rows.map((r) => getters.map((g) => g(r))) }
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

export function isSystemLabel(k: string): boolean {
  return /^(beta\.kubernetes\.io|kubernetes\.io|node\.kubernetes\.io)\//.test(k)
}
