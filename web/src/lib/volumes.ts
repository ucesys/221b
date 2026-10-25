// The Storage tab's model (ST-01 to ST-12): the view's rows grouped by
// class or namespace, in the order of ST-04, with each group's totals, the
// heading line, search terms, label selection and Needs attention. The
// server builds the rows and their diagnosis (views/storage.go); this file
// arranges them.

import type { PairState, StorageClassInfo, StoragePair, StorageQuota, StorageRow } from '../api/storage'
import { compareNatural, minutesSince, nWord } from './format'
import type { Term } from './table'

export type StorageView = 'class' | 'namespace' | 'all'

export const STORAGE_VIEWS: StorageView[] = ['class', 'namespace', 'all']

/** Groups beyond this many start collapsed, except those with rows that need attention. */
export const OPEN_GROUPS = 12

/** Usage thresholds (ST-08), as the server has them. */
export const NEARLY_FULL = 85
export const FULL = 95

// ---- sizes ----

const UNITS = ['', 'Ki', 'Mi', 'Gi', 'Ti', 'Pi']

/** A size as the tab shows it: binary units, three figures at most ("61.2Gi", "1.28Ti", "740Gi"). */
export function bytes(n: number | null | undefined): string {
  if (n == null) return '—'
  if (n <= 0) return '0'
  let v = n
  let i = 0
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024
    i++
  }
  const s = v >= 100 || Number.isInteger(v) ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2)
  return (s.includes('.') ? s.replace(/0+$/, '').replace(/\.$/, '') : s) + UNITS[i]
}

/** A total, as group headers and the heading show it: one decimal under 10 ("3.9Ti", "1.0Ti"), whole above ("740Gi"). */
export function bytesTotal(n: number | null | undefined): string {
  if (n == null) return '—'
  if (n <= 0) return '0'
  let v = n
  let i = 0
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024
    i++
  }
  return (v >= 10 ? v.toFixed(0) : v.toFixed(1)) + UNITS[i]
}

/** The heading's sizes: "8.6 TiB". */
export function bytesLong(n: number): string {
  const s = bytesTotal(n)
  const m = /^([\d.]+)(Ki|Mi|Gi|Ti|Pi)?$/.exec(s)
  if (!m) return s
  return m[2] ? `${m[1]} ${m[2]}B` : `${m[1]} B`
}

/** An access mode as the column abbreviates it (ST-06). */
export function accessShort(m: string): string {
  switch (m) {
    case 'ReadWriteOnce':
      return 'RWO'
    case 'ReadOnlyMany':
      return 'ROX'
    case 'ReadWriteMany':
      return 'RWX'
    case 'ReadWriteOncePod':
      return 'RWOP'
  }
  return m
}

export function reclaimText(r: string): string {
  return `${r} on release`
}

export function bindingText(mode: string): string {
  return mode === 'WaitForFirstConsumer' ? 'binds when a pod is scheduled' : 'binds immediately'
}

export function expansionText(on: boolean): string {
  return on ? 'expansion allowed' : 'no expansion'
}

/** The class's facts line, as its group header shows it (ST-03). */
export function classFacts(c: StorageClassInfo): string[] {
  return [c.provisioner, reclaimText(c.reclaim), bindingText(c.binding), expansionText(c.expansion)]
}

// ---- rows ----

export interface StorageData {
  classes: StorageClassInfo[]
  pairs: StoragePair[]
  quotas: StorageQuota[]
}

/** Splits the view's rows by kind. */
export function storageData(rows: StorageRow[]): StorageData {
  const out: StorageData = { classes: [], pairs: [], quotas: [] }
  for (const r of rows) {
    if (r.class) out.classes.push(r.class)
    else if (r.pair) out.pairs.push(r.pair)
    else if (r.quota) out.quotas.push(r.quota)
  }
  return out
}

/** A pair's key, as the view's: "pvc:ns/name" for a claim, "pv:name" for a volume with none. */
export function pairKey(p: StoragePair): string {
  return p.claim && p.namespace ? `pvc:${p.namespace}/${p.claim}` : `pv:${p.volume ?? ''}`
}

/** The peek parameter that opens a pair's drawer. */
export function pairPeek(p: StoragePair): string {
  return p.claim && p.namespace ? `claim:${p.namespace}/${p.claim}` : `volume:${p.volume ?? ''}`
}

/** Where a pair stands in its group (ST-04): a Lost claim or a Failed volume first, then bound pairs, Pending claims, Released and Available volumes. */
const STATE_RANK: Record<PairState, number> = { lost: 0, failed: 0, bound: 1, pending: 2, released: 3, available: 4 }

export function comparePairs(a: StoragePair, b: StoragePair): number {
  return (
    STATE_RANK[a.state] - STATE_RANK[b.state] ||
    compareNatural(a.namespace ?? '', b.namespace ?? '') ||
    compareNatural(a.claim ?? a.volume ?? '', b.claim ?? b.volume ?? '') ||
    compareNatural(a.volume ?? '', b.volume ?? '')
  )
}

// ---- groups ----

export type GroupKind = 'class' | 'none' | 'missing' | 'namespace' | 'unclaimed'

export interface Totals {
  volumes: number
  claims: number
  provisioned: number
  requested: number
  used: number
  /** used of what reports usage, 0–100. */
  usedPct: number
  pending: number
  waiting: number
  available: number
  released: number
}

export interface StorageGroup {
  key: string
  kind: GroupKind
  name: string
  cls?: StorageClassInfo
  quota?: StorageQuota
  /** The Helm releases that own claims in a namespace group. */
  releases?: string[]
  pairs: StoragePair[]
  totals: Totals
  /** Rows that need attention (ST-09). */
  attention: number
}

export function totals(pairs: StoragePair[]): Totals {
  const t: Totals = { volumes: 0, claims: 0, provisioned: 0, requested: 0, used: 0, usedPct: 0, pending: 0, waiting: 0, available: 0, released: 0 }
  let measured = 0
  for (const p of pairs) {
    if (p.volume && p.state !== 'pending') {
      t.volumes++
      t.provisioned += p.capacity ?? 0
    }
    if (p.claim) {
      t.claims++
      t.requested += p.requested ?? 0
    }
    if (p.usage) {
      t.used += p.usage.used
      measured += p.capacity ?? p.usage.capacity
    }
    if (p.state === 'pending') {
      if (p.reason?.tone === 'info') t.waiting++
      else t.pending++
    }
    if (p.state === 'available') t.available++
    if (p.state === 'released') t.released++
  }
  t.usedPct = measured > 0 ? (t.used / measured) * 100 : 0
  return t
}

function group(key: string, kind: GroupKind, name: string, pairs: StoragePair[], extra: Partial<StorageGroup> = {}): StorageGroup {
  const sorted = [...pairs].sort(comparePairs)
  return { key, kind, name, pairs: sorted, totals: totals(sorted), attention: sorted.filter((p) => p.attention).length, ...extra }
}

/**
 * By class (ST-02, ST-04): the default class first, the other classes by
 * name, then the volumes with no class, then one group per class name
 * that claims use and no class has.
 */
export function byClass(classes: StorageClassInfo[], pairs: StoragePair[]): StorageGroup[] {
  const known = new Map(classes.map((c) => [c.name, c]))
  const of = new Map<string, StoragePair[]>()
  const none: StoragePair[] = []
  const missing = new Map<string, StoragePair[]>()
  for (const p of pairs) {
    if (!p.class) none.push(p)
    else if (known.has(p.class)) of.set(p.class, [...(of.get(p.class) ?? []), p])
    else missing.set(p.class, [...(missing.get(p.class) ?? []), p])
  }
  const ordered = [...classes].sort((a, b) => Number(b.default) - Number(a.default) || compareNatural(a.name, b.name))
  const out = ordered.map((c) => group(`class:${c.name}`, 'class', c.name, of.get(c.name) ?? [], { cls: c }))
  if (none.length) out.push(group('none', 'none', 'No storage class', none))
  for (const name of [...missing.keys()].sort(compareNatural)) out.push(group(`missing:${name}`, 'missing', name, missing.get(name)!))
  return out
}

/**
 * By namespace (ST-11): one group per namespace with claims, by name, with
 * its quota and the Helm releases that own claims there; the volumes no
 * claim holds last, for those who may see them.
 */
export function byNamespace(pairs: StoragePair[], quotas: StorageQuota[]): StorageGroup[] {
  const of = new Map<string, StoragePair[]>()
  const unclaimed: StoragePair[] = []
  for (const p of pairs) {
    if (p.claim && p.namespace) of.set(p.namespace, [...(of.get(p.namespace) ?? []), p])
    else unclaimed.push(p)
  }
  const quota = new Map(quotas.map((q) => [q.namespace, q]))
  const out = [...of.keys()].sort(compareNatural).map((ns) => {
    const ps = of.get(ns)!
    const releases = [...new Set(ps.map((p) => p.release).filter((r): r is string => !!r))].sort(compareNatural)
    return group(`ns:${ns}`, 'namespace', ns, ps, { quota: quota.get(ns), releases })
  })
  if (unclaimed.length) out.push(group('unclaimed', 'unclaimed', 'Volumes no claim holds', unclaimed))
  return out
}

/** Whether a group starts open: every group while there are few, else only those with rows that need attention. */
export function openByDefault(g: StorageGroup, count: number): boolean {
  return count <= OPEN_GROUPS || g.attention > 0
}

/** Whether a pair's pod sub-rows start open (ST-07): when any of its pods is not Running. */
export function podsOpenByDefault(p: StoragePair): boolean {
  return (p.pods ?? []).length > 1 && (p.pods ?? []).some((x) => x.status !== 'Running')
}

// ---- the heading (ST-01) ----

function list(items: string[]): string {
  if (items.length <= 1) return items.join('')
  return items.slice(0, -1).join(', ') + ' and ' + items[items.length - 1]
}

export interface Heading {
  /** Text parts; mono ones are names. */
  parts: { text: string; mono?: boolean }[]
}

/** The line beside the tab's title, for a cluster-wide role or a namespace-bound one. */
export function heading(d: StorageData, clusterWide: boolean): Heading {
  const claims = d.pairs.filter((p) => p.claim)
  const namespaces = [...new Set(claims.map((p) => p.namespace!))].sort(compareNatural)
  if (clusterWide) {
    const vols = d.pairs.filter((p) => p.volume && p.state !== 'pending')
    const provisioned = vols.reduce((s, p) => s + (p.capacity ?? 0), 0)
    const def = d.classes.find((c) => c.default)
    const parts: Heading['parts'] = [{ text: `${nWord(vols.length, 'volume', 'volumes')} · ${bytesLong(provisioned)} provisioned · ${nWord(d.classes.length, 'class', 'classes')}` }]
    if (def) parts.push({ text: ', default ' }, { text: def.name, mono: true })
    else if (d.classes.length) parts.push({ text: ', no default' })
    parts.push({ text: ` · ${nWord(claims.length, 'claim', 'claims')} in ${nWord(namespaces.length, 'namespace', 'namespaces')}` })
    return { parts }
  }
  const requested = claims.reduce((s, p) => s + (p.requested ?? 0), 0)
  const parts: Heading['parts'] = [{ text: `${nWord(claims.length, 'claim', 'claims')}${namespaces.length ? ' in ' : ''}` }]
  namespaces.forEach((ns, i) => {
    if (i > 0) parts.push({ text: i === namespaces.length - 1 ? ' and ' : ', ' })
    parts.push({ text: ns, mono: true })
  })
  parts.push({ text: ` · ${bytesLong(requested)} requested · the namespaces your role can see` })
  return { parts }
}

// ---- search, labels and Needs attention (ST-09, ST-10) ----

/** What search matches: volume, claim, namespace, pod, class and node names. */
export function pairTerms(p: StoragePair): Term[] {
  const out: Term[] = []
  if (p.volume) out.push({ kind: 'volume', text: p.volume })
  if (p.claim) out.push({ kind: 'claim', text: p.claim })
  if (p.namespace) out.push({ kind: 'namespace', text: p.namespace })
  if (p.wasClaim) out.push({ kind: 'claim', text: p.wasClaim })
  if (p.class) out.push({ kind: 'class', text: p.class })
  for (const x of p.pods ?? []) {
    out.push({ kind: 'pod', text: x.name })
    if (x.node) out.push({ kind: 'node', text: x.node })
  }
  for (const n of p.attached ?? []) out.push({ kind: 'node', text: n })
  if (p.source?.node) out.push({ kind: 'node', text: p.source.node })
  out.push({ kind: 'status', text: p.phase })
  return out
}

/** A pair's labels, the claim's and the volume's, as "key=value". */
export function pairLabels(p: StoragePair): string[] {
  const out = new Set<string>()
  for (const ls of [p.claimLabels, p.volumeLabels]) for (const [k, v] of Object.entries(ls ?? {})) out.add(`${k}=${v}`)
  return [...out]
}

/** Every label value the rows carry, by key, with how many rows carry each. */
export function labelFacets(pairs: StoragePair[]): Map<string, Map<string, number>> {
  const out = new Map<string, Map<string, number>>()
  for (const p of pairs) {
    for (const l of pairLabels(p)) {
      const i = l.indexOf('=')
      const k = l.slice(0, i)
      const v = l.slice(i + 1)
      const m = out.get(k) ?? new Map<string, number>()
      m.set(v, (m.get(v) ?? 0) + 1)
      out.set(k, m)
    }
  }
  return out
}

/** Whether a pair has the selected labels: any value picked for a key, every key picked (as the Nodes tab, P2-09). */
export function labelMatch(p: StoragePair, picked: string[]): boolean {
  if (!picked.length) return true
  const have = new Set(pairLabels(p))
  const byKey = new Map<string, string[]>()
  for (const l of picked) {
    const i = l.indexOf('=')
    const k = i < 0 ? l : l.slice(0, i)
    byKey.set(k, [...(byKey.get(k) ?? []), l])
  }
  return [...byKey.values()].every((ls) => ls.some((l) => have.has(l)))
}

/** The pod sub-row's text: "/out · read-write · container runner", or why the pod waits. */
export function mountText(x: NonNullable<StoragePair['pods']>[number]): string {
  if (x.status !== 'Running' && !x.node) return 'mounts once the pod is scheduled' + (x.note ? ` · ${x.note}` : '')
  const parts = [x.readOnly ? 'read-only' : 'read-write']
  if (x.subPath) parts.push(`subPath ${x.subPath}`)
  if (x.container) parts.push(`container ${x.container}`)
  return parts.join(' · ')
}

/** The usage tone (ST-08): amber from 85%, red from 95%. */
export function usageTone(pct: number): 'ok' | 'warn' | 'err' {
  return pct >= FULL ? 'err' : pct >= NEARLY_FULL ? 'warn' : 'ok'
}

/** The Node column: the one node, or "N nodes". */
export function nodeText(p: StoragePair): string {
  const nodes = p.nodes?.length ? p.nodes : p.source?.node ? [p.source.node] : (p.attached ?? [])
  if (!nodes.length) return ''
  return nodes.length === 1 ? nodes[0]! : `${nodes.length} nodes`
}

/** A provisioner's name for people: "Longhorn" for driver.longhorn.io. */
export function provisionerName(provisioner: string, className: string): string {
  const known: Record<string, string> = {
    'driver.longhorn.io': 'Longhorn',
    'rancher.io/local-path': 'local-path',
    'ebs.csi.aws.com': 'EBS',
    'pd.csi.storage.gke.io': 'Persistent Disk',
    'disk.csi.azure.com': 'Azure Disk',
    'nfs.csi.k8s.io': 'NFS',
    'rook-ceph.rbd.csi.ceph.com': 'Ceph RBD',
    'rook-ceph.cephfs.csi.ceph.com': 'CephFS',
  }
  return known[provisioner] ?? className
}

/** A plain-language note on a known provisioner, for the class drawer (ST-17). */
export function provisionerNote(provisioner: string): string {
  const notes: Record<string, string> = {
    'driver.longhorn.io': 'CSI, replicated across node disks',
    'kubernetes.io/no-provisioner': 'no provisioner: volumes are made by hand, usually local disks',
    'rancher.io/local-path': 'a folder on the node’s disk, made when a pod first uses the claim',
    'nfs.csi.k8s.io': 'CSI, a folder on an NFS server per volume',
    'ebs.csi.aws.com': 'CSI, AWS EBS volumes, one availability zone each',
    'pd.csi.storage.gke.io': 'CSI, Google Persistent Disks',
    'disk.csi.azure.com': 'CSI, Azure managed disks',
    'rook-ceph.rbd.csi.ceph.com': 'CSI, Ceph block images',
    'rook-ceph.cephfs.csi.ceph.com': 'CSI, CephFS folders',
  }
  return notes[provisioner] ?? (provisioner.includes('.') ? 'CSI driver' : 'in-tree provisioner')
}

/** The number of copies a replicated provisioner keeps, when its parameters say. */
export function replicas(c: StorageClassInfo): number {
  const n = Number(c.parameters?.numberOfReplicas ?? c.parameters?.replicas ?? '')
  return Number.isFinite(n) && n > 1 ? n : 1
}

/** "8 volumes", "1 claim pending" and the like, for a class header (ST-03). */
export function classTail(t: Totals): string[] {
  const out: string[] = []
  if (t.pending) out.push(nWord(t.pending, 'claim pending', 'claims pending'))
  if (t.available) out.push(`${t.available} Available`)
  if (t.released) out.push(`${t.released} Released`)
  if (t.waiting) out.push(nWord(t.waiting, 'claim waiting for a pod', 'claims waiting for a pod'))
  return out
}

/** "a, b and c". */
export function listText(items: string[]): string {
  return list(items)
}

/** An age as the Age column shows it, in one unit: "45m", "2h", "41d" (ST-06). */
export function ageShort(iso: string | undefined, now: number): string {
  if (!iso) return '—'
  const m = Math.max(0, minutesSince(iso, now))
  if (m < 60) return `${Math.max(1, Math.round(m))}m`
  if (m < 24 * 60) return `${Math.floor(m / 60)}h`
  return `${Math.floor(m / (24 * 60))}d`
}
