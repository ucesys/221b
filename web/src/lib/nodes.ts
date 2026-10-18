// What the Nodes tab computes from the rows: the meters, the problem
// cells, the details' sections and the label and taint facets. Ported from
// the prototype's nodesView. The columns are in columns/nodes.tsx.

import type { NodeRow, Taint, Tone } from '../api/types'
import { fCores, fMem, nWord, pct, pctTxt } from './format'

export type RoleFilter = 'all' | 'control-plane' | 'worker'

export const toneRank: Record<Tone, number> = { err: 3, warn: 2, info: 1, muted: 0, ok: 0 }

/** Allocatable less requested: what the scheduler can still place. */
export function free(res: { allocatable: number; requested: number }): number {
  return Math.max(0, res.allocatable - res.requested)
}

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

/** A label as the facets name it: key=value, or the key alone for an empty value. */
export function labelText(k: string, v: string): string {
  return v ? `${k}=${v}` : k
}

/** The label and taint facets a node list offers, with how many nodes carry each (P2-09). */
export interface Facets {
  labels: { key: string; values: { text: string; value: string; count: number }[] }[]
  taints: { id: string; key: string; value: string; effect: string; count: number }[]
}

export function nodeFacets(rows: NodeRow[]): Facets {
  const labels = new Map<string, Map<string, number>>()
  const taints = new Map<string, { id: string; key: string; value: string; effect: string; count: number }>()
  for (const r of rows) {
    for (const [k, v] of Object.entries(r.labels)) {
      if (isSystemLabel(k) || k.startsWith('node-role.kubernetes.io/')) continue
      const m = labels.get(k) ?? new Map<string, number>()
      m.set(v, (m.get(v) ?? 0) + 1)
      labels.set(k, m)
    }
    for (const t of r.taints) {
      const id = taintId(t)
      const e = taints.get(id) ?? { id, key: t.key, value: t.value ?? '', effect: t.effect, count: 0 }
      e.count++
      taints.set(id, e)
    }
  }
  return {
    labels: [...labels.entries()]
      .sort((a, b) => a[0].localeCompare(b[0]))
      .map(([key, m]) => ({ key, values: [...m.entries()].sort((a, b) => a[0].localeCompare(b[0])).map(([value, count]) => ({ text: labelText(key, value), value, count })) })),
    taints: [...taints.values()].sort((a, b) => a.id.localeCompare(b.id)),
  }
}

/** The selected facets: labels as key=value, taints as key=value:Effect. */
export interface FacetSelection {
  labels: string[]
  taints: string[]
}

export function facetsActive(f: FacetSelection): boolean {
  return f.labels.length > 0 || f.taints.length > 0
}

/**
 * Whether a node has the selected facets: any of the values chosen for a
 * label key, every key chosen, and every taint chosen.
 */
export function facetMatch(r: NodeRow, f: FacetSelection): boolean {
  const byKey = new Map<string, string[]>()
  for (const l of f.labels) {
    const [k, v = ''] = l.split('=', 2) as [string, string?]
    byKey.set(k, [...(byKey.get(k) ?? []), v])
  }
  for (const [k, values] of byKey) {
    const have = r.labels[k]
    if (have === undefined || !values.includes(have)) return false
  }
  return f.taints.every((id) => r.taints.some((t) => taintId(t) === id))
}

/** "zone=a or zone=b, dedicated=analytics:NoSchedule" */
export function facetText(f: FacetSelection): string {
  const byKey = new Map<string, string[]>()
  for (const l of f.labels) {
    const [k, v = ''] = l.split('=', 2) as [string, string?]
    byKey.set(k, [...(byKey.get(k) ?? []), labelText(k, v)])
  }
  return [...[...byKey.values()].map((vs) => vs.join(' or ')), ...f.taints].join(', ')
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

/** The short role tag: control-plane nodes are "master", as RKE2 and kubectl get nodes call them (6.4). */
export function roleTag(r: NodeRow): { text: string; title: string } {
  if (r.role === 'control-plane') {
    const extra = r.roles.filter((x) => x !== 'control-plane' && x !== 'master')
    return { text: 'master', title: 'Control plane' + (extra.length ? ` (also ${extra.join(', ')})` : '') }
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
