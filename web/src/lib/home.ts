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
  reasons: string[]
}

const n = (k: number, one: string, many: string) => `${k} ${k === 1 ? one : many}`

/** Alerts that are on now and not acknowledged. */
export function activeAlerts(c: HomeCluster): number {
  const a = c.alerts
  return a.failed + a.pending + a.nodeErrors + a.nodeWarnings + a.volumes
}

/** The tone of a cluster's alerts, as its Alerts tab shows it: red for failures, amber for the rest and for acknowledged ones. */
export function alertTone(c: HomeCluster): Tone | undefined {
  const a = c.alerts
  if (a.failed + a.nodeErrors > 0) return 'err'
  if (activeAlerts(c) + a.acknowledged > 0) return 'warn'
  return undefined
}

/** A cluster's health in a word, worst first. */
export function healthOf(c: HomeCluster): Health {
  const s = stateOf(c)
  if (c.state !== 'connected') return { tone: s.tone, text: s.text, reasons: c.message ? [c.message] : [] }
  if (!c.synced) return { tone: 'info', text: 'Listing', reasons: ['Still listing what the cluster has.'] }
  const a = c.alerts
  const reasons: string[] = []
  const notReady = c.nodes.total - c.nodes.ready
  if (c.expired) reasons.push('Its credentials have expired.')
  if (notReady > 0) reasons.push(`${n(notReady, 'node is', 'nodes are')} not ready.`)
  if (a.failed) reasons.push(`${n(a.failed, 'pod fails', 'pods fail')} to start.`)
  if (a.nodeErrors) reasons.push(`${n(a.nodeErrors, 'node has', 'nodes have')} errors.`)
  if (reasons.length) return { tone: 'err', text: 'Needs attention', reasons }
  if (c.expiring) reasons.push('Its credentials expire soon.')
  if (a.pending) reasons.push(`${n(a.pending, 'pod is', 'pods are')} pending.`)
  if (a.nodeWarnings) reasons.push(`${n(a.nodeWarnings, 'node has', 'nodes have')} warnings.`)
  if (a.volumes) reasons.push(`${n(a.volumes, 'volume claim waits', 'volume claims wait')}.`)
  if (a.acknowledged) reasons.push(`${n(a.acknowledged, 'alert is', 'alerts are')} acknowledged.`)
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
