// The Alerts tab's model (P4-07), as the prototype words it: the server
// sends one row per pod, node or volume claim with something wrong; this
// orders them into sections, writes their headers and context, and works
// out the tiles, the tab's badge and the banners every other tab shows.

import type { AlertEntry, AlertRow } from '../api/types'
import { fmtAge, fMem, minutesSince, nWord } from './format'

export type AlertKind = 'all' | 'pods' | 'nodes' | 'volumes'

const SEV_RANK: Record<string, number> = { failed: 0, pending: 1, recovered: 2 }

export function sevLabel(r: AlertRow): { label: string; tone: string } {
  switch (r.severity) {
    case 'failed':
      return { label: 'Failed to start', tone: 'err' }
    case 'pending':
      // A pod that runs but fails its readiness check waits too.
      return { label: r.status === 'Running' ? 'Not ready' : 'Pending', tone: 'warn' }
    case 'recovered':
      return { label: 'Resolved', tone: 'ok' }
    case 'error':
      return { label: 'Error', tone: 'err' }
  }
  return { label: 'Warning', tone: 'warn' }
}

export function kindLabel(r: AlertRow): string {
  if (r.kind === 'pod') return 'Pod'
  if (r.kind === 'pvc') return 'PVC'
  return r.role === 'control-plane' ? 'Control plane' : 'Worker'
}

/** The grey line after the name: where it is and what it belongs to. */
export function metaText(r: AlertRow): string {
  if (r.kind === 'pod') return `${r.namespace ?? ''} · ${r.node || 'no node'}${r.release ? ' · release ' + r.release : ''}`
  if (r.kind === 'pvc') return `${r.namespace ?? ''}${r.release ? ' · release ' + r.release : ''}`
  const parts = [r.role === 'control-plane' ? 'control plane' : r.pool ? `worker · ${r.pool} pool` : 'worker']
  if (r.ip) parts.push(r.ip)
  if (r.zone) parts.push(r.zone)
  return parts.join(' · ')
}

function capFirst(s: string): string {
  return s ? s[0]!.toUpperCase() + s.slice(1) : s
}

function sentence(s: string): string {
  const t = s.trim()
  return t && !/[.!?]$/.test(t) ? t + '.' : t
}

/** The paragraph under the header: why it is here, and whether it is monospace. */
export function contextOf(r: AlertRow, now: number): { text: string; mono: boolean } {
  if (r.kind === 'pod') {
    if (r.severity === 'failed') {
      if (r.waiting && !r.waiting.startsWith('CrashLoop')) {
        return { text: `${r.waiting}${r.waitingMessage ? ': ' + r.waitingMessage : ''}`, mono: true }
      }
      const head = r.lastReason ? `Last exit: ${r.lastReason} (code ${r.lastExit ?? '?'}) · ${nWord(r.restarts ?? 0, 'restart', 'restarts')}` : `${r.status} · ${nWord(r.restarts ?? 0, 'restart', 'restarts')}`
      return { text: head + (r.lastMessage ? '\n' + r.lastMessage : ''), mono: true }
    }
    if (r.severity === 'pending') {
      return { text: `${r.node ? r.status : 'Pending'} for ${fmtAge(r.started, now)}. ${capFirst(sentence(r.why ?? ''))}`.trim(), mono: false }
    }
    const ready = `${r.ready ?? 0}/${r.total ?? 0}`
    if (r.oom && r.lastAt) {
      return { text: `Now ${r.status} (${ready} ready). A container was killed for running out of memory ${fmtAge(r.lastAt, now)} ago (OOMKilled, exit code ${r.lastExit ?? 137}); ${nWord(r.restarts ?? 0, 'restart', 'restarts')} in all.`, mono: false }
    }
    return { text: `Now ${r.status} (${ready} ready). These warnings were raised while it started ${fmtAge(r.started, now)} ago.`, mono: false }
  }
  if (r.kind === 'pvc') {
    const users = (r.users ?? []).map((u) => `${u.pod} (${u.status}${u.node ? ', ' + u.node : ''})`).join(', ')
    const parts = [`Volume ${r.volume || '(not bound)'}`]
    if (r.storageClass) parts.push(r.storageClass)
    if (r.capacity) parts.push(fMem(r.capacity) + (r.resizing && r.requested ? ` (expansion to ${fMem(r.requested)} requested)` : ''))
    if (r.accessModes?.length) parts.push(r.accessModes.join(', '))
    return { text: parts.join(' · ') + (users ? '\nUsed by ' + users : '\nNo pod uses it.'), mono: true }
  }
  return { text: (r.problems ?? []).map((p) => `${p.title}. ${p.summary}`).join('\n\n'), mono: false }
}

/** Every word search looks in. */
export function searchText(r: AlertRow): string {
  const parts = [r.name, r.namespace ?? '', r.volume ?? '']
  for (const p of r.problems ?? []) parts.push(p.title)
  for (const e of r.events) parts.push(e.reason, e.message)
  return parts.join(' ').toLowerCase()
}

function lastOf(r: AlertRow): number {
  const e = r.events[0]
  return e?.last ? Date.parse(e.last) : e?.first ? Date.parse(e.first) : 0
}

export interface AlertSections {
  pods: AlertRow[]
  controlPlane: AlertRow[]
  workers: AlertRow[]
  volumes: AlertRow[]
}

/** Whether an alert is still going on. The server marks one resolved once its object is green with nothing going on (6.5): a pod running again, a node ready with only past warnings, a claim bound with its pods running. */
export function isActive(r: AlertRow): boolean {
  return r.severity !== 'recovered'
}

/** Where an alert goes in its section: what nobody has acknowledged, then what someone has, then what recovered. */
function stage(r: AlertRow): number {
  return !isActive(r) ? 2 : r.ack ? 1 : 0
}

/** The rows in their sections and order: unacknowledged, acknowledged, recovered; within those failed, then pending, newest first; nodes with errors first. */
export function sectionsOf(rows: AlertRow[]): AlertSections {
  const pods = rows.filter((r) => r.kind === 'pod').sort((a, b) => stage(a) - stage(b) || (SEV_RANK[a.severity] ?? 3) - (SEV_RANK[b.severity] ?? 3) || lastOf(b) - lastOf(a) || a.name.localeCompare(b.name))
  const nodes = rows.filter((r) => r.kind === 'node').sort((a, b) => stage(a) - stage(b) || (a.severity === 'error' ? 0 : 1) - (b.severity === 'error' ? 0 : 1) || a.name.localeCompare(b.name, undefined, { numeric: true }))
  const volumes = rows.filter((r) => r.kind === 'pvc').sort((a, b) => stage(a) - stage(b) || (a.severity === 'error' ? 0 : 1) - (b.severity === 'error' ? 0 : 1) || lastOf(b) - lastOf(a) || a.name.localeCompare(b.name))
  return { pods, controlPlane: nodes.filter((r) => r.role === 'control-plane'), workers: nodes.filter((r) => r.role !== 'control-plane'), volumes }
}

/** A node and the pods with alerts on it; row is the node's own alert, when it has one. */
export interface NodeGroup {
  node: string
  row?: AlertRow
  pods: AlertRow[]
}

export interface ByNode {
  groups: NodeGroup[]
  /** Pods no node has taken: pending, unscheduled. */
  unscheduled: AlertRow[]
  volumes: AlertRow[]
}

/** How pressing an alert is: unacknowledged before acknowledged before resolved; failing before waiting. */
function urgency(r: AlertRow): number {
  const sev = r.severity === 'failed' || r.severity === 'error' ? 0 : r.severity === 'recovered' ? 2 : 1
  return stage(r) * 3 + sev
}

/**
 * The alerts as a hierarchy (asked after 0.7.1): each pod under the node
 * it runs on, the node's own alert heading the group when it has one. The
 * groups come in the order of their most pressing alert, so a node whose
 * pods are failing is near the top even when the node itself is fine.
 */
export function byNode(rows: AlertRow[]): ByNode {
  const sec = sectionsOf(rows)
  const groups = new Map<string, NodeGroup>()
  const group = (node: string) => {
    let g = groups.get(node)
    if (!g) groups.set(node, (g = { node, pods: [] }))
    return g
  }
  for (const r of [...sec.controlPlane, ...sec.workers]) group(r.name).row = r
  const unscheduled: AlertRow[] = []
  for (const r of sec.pods) {
    if (r.node) group(r.node).pods.push(r)
    else unscheduled.push(r)
  }
  const top = (g: NodeGroup) => Math.min(...[...(g.row ? [g.row] : []), ...g.pods].map(urgency))
  const list = [...groups.values()].sort((a, b) => top(a) - top(b) || a.node.localeCompare(b.node, undefined, { numeric: true }))
  return { groups: list, unscheduled, volumes: sec.volumes }
}

export interface Tile {
  count: number
  label: string
  tone: string
}

/** The six tiles; search and the kind filter do not change them. Resolved alerts count only on the last. */
export function tilesOf(rows: AlertRow[]): Tile[] {
  const n = (f: (r: AlertRow) => boolean) => rows.filter(f).length
  const failed = n((r) => r.kind === 'pod' && r.severity === 'failed')
  const pending = n((r) => r.kind === 'pod' && r.severity === 'pending')
  const nodeErr = n((r) => r.kind === 'node' && r.severity === 'error')
  const nodeWarn = n((r) => r.kind === 'node' && r.severity === 'warning')
  const vols = n((r) => r.kind === 'pvc' && isActive(r))
  const recovered = n((r) => !isActive(r))
  return [
    { count: failed, label: failed === 1 ? 'pod failed to start' : 'pods failed to start', tone: failed ? 'err' : 'muted' },
    { count: pending, label: pending === 1 ? 'pod pending' : 'pods pending', tone: pending ? 'warn' : 'muted' },
    { count: nodeErr, label: nodeErr === 1 ? 'node with errors' : 'nodes with errors', tone: nodeErr ? 'err' : 'muted' },
    { count: nodeWarn, label: nodeWarn === 1 ? 'node with warnings' : 'nodes with warnings', tone: nodeWarn ? 'warn' : 'muted' },
    { count: vols, label: vols === 1 ? 'volume with warnings' : 'volumes with warnings', tone: vols ? 'warn' : 'muted' },
    { count: recovered, label: recovered === 1 ? 'alert resolved' : 'alerts resolved', tone: recovered ? 'ok' : 'muted' },
  ]
}

/**
 * The tab's badge: every active alert, acknowledged ones included (asked at
 * the Phase 6 review), resolved ones aside (6.5). Red while something
 * unacknowledged is failing.
 */
export function badgeOf(rows: AlertRow[]): { count: number; tone: string } {
  let count = 0
  let err = false
  for (const r of rows) {
    if (!isActive(r)) continue
    count++
    if (!r.ack && ((r.kind === 'pod' && r.severity === 'failed') || (r.kind === 'node' && r.severity === 'error'))) err = true
  }
  return { count, tone: err ? 'err' : count ? 'warn' : 'muted' }
}

/**
 * The colour of the Alerts tab's name: red while something unacknowledged
 * is failing, yellow while anything else is going on, acknowledged alerts
 * included, so the tab still says so when the banners do not.
 */
export function tabTone(rows: AlertRow[]): 'err' | 'warn' | undefined {
  const active = rows.filter(isActive)
  if (active.some((r) => !r.ack && (r.severity === 'failed' || r.severity === 'error'))) return 'err'
  return active.length ? 'warn' : undefined
}

export interface AlertBanner {
  tone: 'err' | 'warn'
  title: string
  detail: string
  /** The detail's items; a volume's opens its Storage drawer (ST-21). */
  items?: BannerItem[]
}

export interface BannerItem {
  text: string
  /** The peek target the item opens: "claim:ns/name". */
  peek?: string
}

/** A volume alert's banner item (ST-21): what is wrong with the claim. */
function volumeItem(r: AlertRow): BannerItem {
  const failed = r.events.some((e) => e.reason === 'ProvisioningFailed')
  return { text: `${r.namespace}/${r.name} — ${r.status}${failed ? ', the volume could not be provisioned' : ''}`, peek: `claim:${r.namespace}/${r.name}` }
}

function lowerFirst(s: string): string {
  return s ? s[0]!.toLowerCase() + s.slice(1) : s
}

/** The banners every other tab shows: red for what is failing, amber for what waits. Acknowledged and resolved alerts stay on the Alerts tab only. */
export function bannersOf(all: AlertRow[], now: number): AlertBanner[] {
  const rows = all.filter((r) => !r.ack && isActive(r))
  const failed = rows.filter((r) => r.kind === 'pod' && r.severity === 'failed')
  const pending = rows.filter((r) => r.kind === 'pod' && r.severity === 'pending')
  const nodeErr = rows.filter((r) => r.kind === 'node' && r.severity === 'error')
  const nodeWarn = rows.filter((r) => r.kind === 'node' && r.severity === 'warning')
  const volErr = rows.filter((r) => r.kind === 'pvc' && r.severity === 'error')
  const volWarn = rows.filter((r) => r.kind === 'pvc' && r.severity === 'warning')
  const nodeItem = (r: AlertRow, sev: string) =>
    `${r.name} — ${(r.problems ?? [])
      .filter((p) => sev === 'any' || p.severity === sev)
      .map((p) => lowerFirst(p.title))
      .join(', ')}`
  const out: AlertBanner[] = []
  const banner = (tone: 'err' | 'warn', title: string, items: BannerItem[]): AlertBanner => {
    const b: AlertBanner = { tone, title, detail: items.map((i) => i.text).join('  ·  ') }
    if (items.some((i) => i.peek)) b.items = items
    return b
  }
  if (failed.length || nodeErr.length || volErr.length) {
    const title = [failed.length ? nWord(failed.length, 'pod failed to start', 'pods failed to start') : '', nodeErr.length ? nWord(nodeErr.length, 'node error', 'node errors') : '', volErr.length ? nWord(volErr.length, 'volume error', 'volume errors') : ''].filter(Boolean).join(' · ')
    const items: BannerItem[] = [...failed.map((r) => ({ text: `${r.namespace}/${r.name} — ${r.status}${r.restarts ? `, ${nWord(r.restarts, 'restart', 'restarts')}` : ''}` })), ...nodeErr.map((r) => ({ text: nodeItem(r, 'error') })), ...volErr.map(volumeItem)]
    out.push(banner('err', title, items))
  }
  if (pending.length || nodeWarn.length || volWarn.length) {
    const title = [pending.length ? nWord(pending.length, 'pod pending', 'pods pending') : '', nodeWarn.length ? nWord(nodeWarn.length, 'node warning', 'node warnings') : '', volWarn.length ? nWord(volWarn.length, 'volume warning', 'volume warnings') : ''].filter(Boolean).join(' · ')
    const items: BannerItem[] = [...pending.map((r) => ({ text: `${r.namespace}/${r.name} — ${r.node ? r.status : 'Unschedulable'} for ${fmtAge(r.started, now)}` })), ...nodeWarn.map((r) => ({ text: nodeItem(r, 'any') })), ...volWarn.map(volumeItem)]
    out.push(banner('warn', title, items))
  }
  return out
}

/** "×231". */
export function countText(e: AlertEntry): string {
  return '×' + e.count
}

/** "51m ago", or "—" with no time. */
export function agoText(iso: string | undefined, now: number): string {
  return iso ? fmtAge(iso, now) + ' ago' : '—'
}

/** How many minutes since, for sorting: no time sorts last. */
export function minutesOf(iso: string | undefined, now: number): number {
  return iso ? minutesSince(iso, now) : Number.POSITIVE_INFINITY
}

export function openLabel(r: AlertRow): string {
  return r.kind === 'pod' ? 'Open pod' : r.kind === 'pvc' ? 'Open claim' : 'Open node'
}
