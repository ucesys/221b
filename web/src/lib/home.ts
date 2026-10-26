// What the home page says about each cluster: its health in a word, and
// how much of its CPU, memory and pod room is taken.

import type { HomeCluster, HomeUse } from '../api/home'
import type { Tone } from '../api/types'
import { stateOf } from '../api/clusters'
import { fCores, fMem, pct, pctTxt } from './format'

export interface Health {
  tone: Tone
  text: string
  /** Why, one short line each. */
  reasons: Reason[]
}

export interface Reason {
  text: string
  /** err or warn; none for a cluster that is not up. */
  tone?: Tone
  /** The Alerts tab lists what it counts (6.4). */
  alerts?: boolean
}

const n = (k: number, one: string, many: string) => `${k} ${k === 1 ? one : many}`

/** Alerts that are on now and not acknowledged. */
export function activeAlerts(c: HomeCluster): number {
  const a = c.alerts
  return a.failed + a.unknown + a.pending + a.nodesDown + a.nodeErrors + a.nodeWarnings + a.volumes
}

/** The tone of a cluster's alerts, as its Alerts tab shows it: red for failures, amber for the rest and for acknowledged ones. */
export function alertTone(c: HomeCluster): Tone | undefined {
  const a = c.alerts
  if (a.failed + a.unknown + a.nodesDown + a.nodeErrors > 0) return 'err'
  if (activeAlerts(c) + a.acknowledged > 0) return 'warn'
  return undefined
}

/** The Nodes box: red for a node not ready or with errors, amber for warnings or a cordoned node, plain when all is well. */
export function nodeTone(c: HomeCluster): Tone | undefined {
  if (c.nodes.total - c.nodes.ready > 0 || c.nodes.errors > 0) return 'err'
  if (c.nodes.warnings > 0 || c.nodes.cordoned > 0) return 'warn'
  return undefined
}

/** The Pods box: red for failing or Unknown pods, amber for pods not ready or pending, plain when all is well. */
export function podTone(c: HomeCluster): Tone | undefined {
  if (c.pods.failing + c.pods.unknown > 0) return 'err'
  if (c.pods.unready + c.pods.pending > 0) return 'warn'
  return undefined
}

/**
 * The Pods box, like the Nodes box: the pods ready of those that have not
 * finished (asked at the second Toolbox review). Completed pods are never
 * ready, so they are left out of both.
 */
export function podsReady(c: HomeCluster): { value: string; tip: string } {
  const p = c.pods
  const live = p.total - p.completed
  return {
    value: `${p.ready}/${live}`,
    tip: `${p.ready} of ${n(live, 'pod', 'pods')} ready` + (p.completed ? `; ${n(p.completed, 'completed pod is', 'completed pods are')} not counted` : ''),
  }
}

/** The Storage box: red for a lost claim or a failed volume, amber for anything else under Needs attention or a claim not bound. */
export function storageTone(c: HomeCluster): Tone | undefined {
  const s = c.storage
  if (s.errors > 0) return 'err'
  if (s.attention > 0 || s.bound < s.claims) return 'warn'
  return undefined
}

/** A cluster's health in a word, worst first. */
export function healthOf(c: HomeCluster): Health {
  const s = stateOf(c)
  if (c.state !== 'connected') return { tone: s.tone, text: s.text, reasons: c.message ? [{ text: c.message }] : [] }
  if (!c.synced) return { tone: 'info', text: 'Listing', reasons: [{ text: 'Still listing what the cluster has.' }] }
  const a = c.alerts
  const reasons: Reason[] = []
  const err = (text: string, alerts = true) => reasons.push({ text, tone: 'err', alerts })
  const warn = (text: string, alerts = true) => reasons.push({ text, tone: 'warn', alerts })
  const notReady = c.nodes.total - c.nodes.ready
  if (c.expired) err('Its credentials have expired.', false)
  if (notReady > 0) err(`${n(notReady, 'node is', 'nodes are')} not ready.`)
  // A node not ready is not also said to have errors, and the pods on a
  // node that stopped reporting are Unknown, not failing to start.
  if (a.unknown) err(`${n(a.unknown, 'pod is', 'pods are')} Unknown.`)
  if (a.failed) err(`${n(a.failed, 'pod fails', 'pods fail')} to start.`)
  if (a.nodeErrors) err(`${n(a.nodeErrors, 'node has', 'nodes have')} errors.`)
  if (reasons.length) return { tone: 'err', text: 'Needs attention', reasons }
  if (c.expiring) warn('Its credentials expire soon.', false)
  if (a.pending) warn(`${n(a.pending, 'pod is', 'pods are')} pending.`)
  if (a.nodeWarnings) warn(`${n(a.nodeWarnings, 'node has', 'nodes have')} warnings.`)
  if (a.volumes) warn(`${n(a.volumes, 'volume claim waits', 'volume claims wait')}.`)
  if (a.acknowledged) warn(`${n(a.acknowledged, 'alert is', 'alerts are')} acknowledged.`)
  if (reasons.length) return { tone: 'warn', text: 'Warnings', reasons }
  return { tone: 'ok', text: 'Healthy', reasons: [] }
}

export interface UseLine {
  label: string
  /** Shares of what the nodes can take, 0–100. */
  reqPct: number
  usePct: number | null
  useTone: 'ok' | 'warn' | 'err'
  text: string
  tip: string
}

function tone(p: number): 'ok' | 'warn' | 'err' {
  return p >= 90 ? 'err' : p >= 75 ? 'warn' : 'ok'
}

/** CPU or memory: requested and in use, of what the nodes can take. */
export function resourceLine(label: 'CPU' | 'Memory', u: HomeUse, metrics: boolean): UseLine {
  const f = label === 'CPU' ? (v: number) => fCores(v) : (v: number) => fMem(v)
  const unit = label === 'CPU' ? ' cores' : ''
  const reqPct = pct(u.requested, u.allocatable)
  const usePct = metrics && u.usedNodes > 0 ? pct(u.used, u.allocatable) : null
  const worst = Math.max(reqPct, usePct ?? 0)
  const text = `${f(u.requested)} of ${f(u.allocatable)}${unit} requested (${pctTxt(reqPct)})` + (usePct !== null ? ` · ${f(u.used)} in use (${pctTxt(usePct)})` : '')
  const tip = `${label}: ${f(u.allocatable)}${unit} allocatable, ${f(u.requested)} requested` + (usePct !== null ? `, ${f(u.used)} in use on ${n(u.usedNodes, 'node', 'nodes')}` : ', no usage figures (metrics-server)')
  return { label, reqPct, usePct, useTone: tone(worst), text, tip }
}

/** Pods: how many of the room the nodes have for them. */
export function podLine(c: HomeCluster): UseLine {
  const p = pct(c.pods.total, c.pods.capacity)
  return { label: 'Pods', reqPct: 0, usePct: p, useTone: tone(p), text: `${c.pods.total} of ${c.pods.capacity} (${pctTxt(p)})`, tip: `Pods: ${c.pods.total} on nodes that take ${c.pods.capacity}; ${c.pods.running} running` }
}
