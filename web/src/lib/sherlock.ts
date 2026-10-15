// Sherlock in the browser: where a cited object opens, and the panel in a
// window of its own (P5-08).

import { useEffect, useState } from 'react'
import type { SherlockRef } from '../api/sherlock'
import { peekParam, type Peek } from './peek'
import { clusterHref, LOCAL } from './router'

/** Reads a 221b:// link: 221b://Kind/namespace/name or 221b://Kind/name. */
export function parseRef(url: string): SherlockRef | null {
  if (!url.startsWith('221b://')) return null
  const parts = url.slice('221b://'.length).split('/')
  if (parts.some((p) => !p)) return null
  if (parts.length === 2) return { kind: parts[0]!, name: parts[1]! }
  if (parts.length === 3) return { kind: parts[0]!, namespace: parts[1]!, name: parts[2]! }
  return null
}

/** Where a cited object opens: details over the page (peek), or a tab. */
export type RefTarget = { peek: string } | { href: string }

/** The kinds whose details the console opens by name. */
const OBJECT_KINDS = new Set([
  'Deployment',
  'StatefulSet',
  'DaemonSet',
  'ReplicaSet',
  'Job',
  'CronJob',
  'Ingress',
  'NetworkPolicy',
  'PersistentVolumeClaim',
  'ConfigMap',
  'Secret',
  'ServiceAccount',
  'Role',
  'RoleBinding',
  'PodDisruptionBudget',
  'ResourceQuota',
  'LimitRange',
  'EndpointSlice',
  'HorizontalPodAutoscaler',
])

export function refTarget(r: SherlockRef, cluster: string): RefTarget | null {
  const ns = r.namespace ?? ''
  let p: Peek | null = null
  switch (r.kind) {
    case 'Node':
      p = { kind: 'node', name: r.name }
      break
    case 'Pod':
      if (ns) p = { kind: 'pod', namespace: ns, name: r.name }
      break
    case 'Service':
      if (ns) p = { kind: 'service', namespace: ns, name: r.name }
      break
    case 'HelmRelease':
      return ns ? { href: clusterHref(cluster, 'helm', [ns, r.name]) } : null
    case 'Namespace':
      return { href: clusterHref(cluster, 'namespaces', [r.name]) }
    default:
      if (ns && OBJECT_KINDS.has(r.kind)) p = { kind: 'object', type: r.kind, namespace: ns, name: r.name }
  }
  return p ? { peek: peekParam(p) } : null
}

// ---------- the panels in windows of their own ----------
//
// Sherlock (P5-08) and Watson (P6-09) can each move to a window of their
// own, or share one, for another monitor. The windows and the main one
// talk over a BroadcastChannel: each window says which panels it holds
// every two seconds, so the main one docks only the others.

/** A panel of the dock. */
export type Pane = 'sherlock' | 'watson'

const CHANNEL = '221b-sherlock'

export type PopMessage =
  | { type: 'hello'; cluster: string; panes?: Pane[]; win?: string }
  | { type: 'bye'; win?: string }
  | { type: 'cluster'; cluster: string }
  | { type: 'focus'; pane?: Pane }
  /** Windows holding these panels let them go: the main window docks them. */
  | { type: 'close'; panes?: Pane[] }
  /** A window that holds the other panel takes this one too. */
  | { type: 'add'; pane: Pane }
  | { type: 'open'; cluster: string; target: RefTarget }

let channel: BroadcastChannel | null = null
const listeners = new Set<(m: PopMessage) => void>()

function chan(): BroadcastChannel | null {
  if (channel || typeof BroadcastChannel === 'undefined') return channel
  channel = new BroadcastChannel(CHANNEL)
  channel.onmessage = (e: MessageEvent<PopMessage>) => {
    for (const fn of listeners) fn(e.data)
  }
  return channel
}

/** Whether this browser can move a panel to its own window. */
export function canPopOut(): boolean {
  return typeof window !== 'undefined' && typeof BroadcastChannel !== 'undefined' && typeof window.open === 'function'
}

export function post(m: PopMessage): void {
  chan()?.postMessage(m)
}

export function onPop(fn: (m: PopMessage) => void): () => void {
  chan()
  listeners.add(fn)
  return () => listeners.delete(fn)
}

/** The view of a window holding some panels. */
export function windowView(panes: Pane[]): 'sherlock' | 'watson' | 'assistants' {
  if (panes.includes('sherlock') && panes.includes('watson')) return 'assistants'
  return panes.includes('watson') ? 'watson' : 'sherlock'
}

/** The panels a window's view holds. */
export function panesOf(view: string): Pane[] {
  if (view === 'assistants') return ['sherlock', 'watson']
  if (view === 'watson') return ['watson']
  return ['sherlock']
}

/** The hash of a window holding some panels, for a cluster. */
export function windowHash(cluster: string, panes: Pane[]): string {
  const v = windowView(panes)
  return cluster === LOCAL ? `#/${v}` : '#/c/' + encodeURIComponent(cluster) + '/' + v
}

/** The hash of Sherlock's own window for a cluster. */
export function sherlockHash(cluster: string): string {
  return windowHash(cluster, ['sherlock'])
}

const popups = new Map<string, Window>()

/** Opens panels in a window of their own; false when the browser refused. */
export function popOut(cluster: string, panes: Pane[]): boolean {
  const name = '221b-' + windowView(panes)
  const open = popups.get(name)
  if (open && !open.closed) {
    open.focus()
    return true
  }
  const url = window.location.pathname + window.location.search + windowHash(cluster, panes)
  const w = window.open(url, name, `popup,width=${panes.length > 1 ? 1280 : 760},height=900`)
  if (!w) return false
  popups.set(name, w)
  w.focus()
  return true
}

/** Brings a panel's window forward, from a click in the main one. */
export function focusPopup(pane?: Pane): void {
  for (const [name, w] of popups) {
    if (w.closed) {
      popups.delete(name)
      continue
    }
    if (!pane || name === '221b-assistants' || name === '221b-' + pane) {
      w.focus()
      return
    }
  }
  post({ type: 'focus', pane })
}

/** How long a separate window may stay silent before it counts as gone. */
const STALE_MS = 5000

/**
 * In the main window: the panels in windows of their own now. Each window
 * says hello every 2 seconds and goodbye when it closes.
 */
export function usePopped(): Set<Pane> {
  const [wins, setWins] = useState<Record<string, { panes: Pane[]; at: number }>>({})
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const off = onPop((m) => {
      if (m.type === 'hello') setWins((w) => ({ ...w, [m.win ?? 'sherlock']: { panes: m.panes ?? ['sherlock'], at: Date.now() } }))
      if (m.type === 'bye')
        setWins((w) => {
          const next = { ...w }
          delete next[m.win ?? 'sherlock']
          return next
        })
    })
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => {
      off()
      clearInterval(t)
    }
  }, [])
  const out = new Set<Pane>()
  for (const w of Object.values(wins)) {
    if (now - w.at < STALE_MS) for (const p of w.panes) out.add(p)
  }
  return out
}

/** In the main window: whether Sherlock is in a window of its own now. */
export function usePoppedOut(): boolean {
  return usePopped().has('sherlock')
}

/** In a separate window: says which panels it holds until it closes. */
export function useAnnounce(cluster: string, panes: Pane[] = ['sherlock']): string {
  const [win] = useState(() => Math.random().toString(36).slice(2, 10))
  const key = panes.join(',')
  useEffect(() => {
    const list = key.split(',') as Pane[]
    const hello = () => post({ type: 'hello', cluster, panes: list, win })
    hello()
    const t = setInterval(hello, 2000)
    const bye = () => post({ type: 'bye', win })
    window.addEventListener('pagehide', bye)
    return () => {
      clearInterval(t)
      window.removeEventListener('pagehide', bye)
    }
  }, [cluster, key, win])
  return win
}

/** The question an Ask Sherlock button asks about a pod, by what is wrong with it. */
export function podQuestion(p: { namespace: string; name: string; status?: string; restarts?: number; lastTermination?: string } | null, ns: string, name: string): string {
  const id = `pod ${ns}/${name}`
  const s = p?.status ?? ''
  if (/CrashLoopBackOff|Error|ExitCode/i.test(s)) return `Why does ${id} keep crashing?`
  if (/ImagePull|ErrImage|InvalidImageName/i.test(s)) return `Why can't ${id} pull its image?`
  if (/Pending/i.test(s)) return `Why is ${id} pending?`
  if (/ContainerCreating|Init:/i.test(s)) return `Why is ${id} stuck in ${s}?`
  if (/OOMKilled/i.test(p?.lastTermination ?? '')) return `Why was ${id} OOM-killed?`
  if (p?.restarts) return `Why has ${id} restarted ${p.restarts} times?`
  return `Tell me about ${id}`
}

/** The question an alert's Ask Sherlock button asks. */
export function alertQuestion(r: { kind: string; name: string; namespace?: string; status?: string; restarts?: number; oom?: boolean }): string {
  if (r.kind === 'node') return `What is wrong with node ${r.name}?`
  if (r.kind === 'pvc') return `Why is volume claim ${r.namespace}/${r.name} not bound?`
  if (r.oom) return `Why was pod ${r.namespace}/${r.name} OOM-killed?`
  return podQuestion({ namespace: r.namespace ?? '', name: r.name, status: r.status, restarts: r.restarts }, r.namespace ?? '', r.name)
}
