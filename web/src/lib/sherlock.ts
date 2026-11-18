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
export const OBJECT_KINDS = new Set([
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
    // The Storage drawers (ST-22): a claim, a volume or a class Sherlock cites.
    case 'PersistentVolumeClaim':
      if (ns) p = { kind: 'claim', namespace: ns, name: r.name }
      break
    case 'PersistentVolume':
      p = { kind: 'volume', name: r.name }
      break
    case 'StorageClass':
      p = { kind: 'class', name: r.name }
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

// ---------- the assistants' window ----------
//
// Sherlock (P5-08) and Watson (P6-09) leave the main window together, for
// another monitor: one window holds both (decided at the Phase 6 review).
// The window and the main one talk over a BroadcastChannel: the window says
// hello every two seconds and goodbye when it closes, and the main one docks
// nothing in between.

/** A panel of the dock. */
export type Pane = 'sherlock' | 'watson'

const CHANNEL = '221b-sherlock'

export type PopMessage =
  /** hidden: the browser may hold the next hello back for a minute. */
  | { type: 'hello'; cluster: string; win?: string; hidden?: boolean }
  | { type: 'bye'; win?: string }
  | { type: 'cluster'; cluster: string }
  | { type: 'focus' }
  /** The window closes, and the main window docks the panels again. */
  | { type: 'close' }
  /** window: the main window it is for, the one the assistants' window follows; '' when that one has closed. */
  | { type: 'open'; cluster: string; target: RefTarget; window: string }
  /** Sherlock's window asks the main window it follows for its view (P6U-03), and it answers. */
  | { type: 'view?'; req: string; window: string; cluster?: string }
  | { type: 'view'; req: string; window: string; view: string }
  /** Undo of an answer's view changes, asked from any window (P6U-10), and its result. */
  | { type: 'undo'; cluster: string; answer: number }
  | { type: 'undone'; cluster: string; answer: number }
  /** Show in 221B (SR-15): the changes an answer left undone, for the main window to apply, and how it went. */
  | { type: 'show'; req: string; window: string; changes: { action: string; input: unknown }[]; force: boolean }
  | { type: 'shown'; req: string; applied: number; failed: string[]; unsaved?: string[] }
  /** A question for the box in Sherlock's window, for the person to send. */
  | { type: 'fill'; cluster: string; text: string; pane?: 'sherlock' | 'watson' }

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

/** Whether this browser can move the panels to a window of their own. */
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

/** The views that open the assistants' window; the first is the one to use. */
export const WINDOW_VIEWS = ['assistants', 'sherlock', 'watson'] as const

/** The hash of the assistants' window for a cluster. */
export function windowHash(cluster: string): string {
  return cluster === LOCAL ? '#/assistants' : '#/c/' + encodeURIComponent(cluster) + '/assistants'
}

const NAME = '221b-assistants'
let popup: Window | null = null

/** Opens the assistants' window; false when the browser refused. */
export function popOut(cluster: string): boolean {
  if (popup && !popup.closed) {
    popup.focus()
    return true
  }
  const url = window.location.pathname + window.location.search + windowHash(cluster)
  const w = window.open(url, NAME, 'popup,width=1280,height=900')
  if (!w) return false
  popup = w
  w.focus()
  return true
}

/** Brings the assistants' window forward, from a click in the main one. */
export function focusPopup(): void {
  if (popup && !popup.closed) {
    popup.focus()
    return
  }
  post({ type: 'focus' })
}

/**
 * Whether a main window acts on a link from the assistants' window: the one
 * the link names, and only that one. With that window closed, any on the
 * link's cluster may, and none is pulled away from another cluster.
 */
export function opensHere(m: { cluster: string; window: string }, me: string, cluster: string): boolean {
  return m.window ? m.window === me : m.cluster === cluster
}

/** How long a window on show may stay silent before it counts as gone: its hellos come on time, so it went without a goodbye. */
const STALE_MS = 5000

/** The same for a hidden window: the browser runs its timers about once a minute, so this is two of those and a margin. */
const HIDDEN_STALE_MS = 150_000

/** When a window last said hello, and whether it was hidden then. */
export type Seen = Record<string, { at: number; hidden: boolean }>

/** Whether any window is still there: none has said goodbye, and none has been silent too long. */
export function anyOpen(wins: Seen, now: number): boolean {
  return Object.values(wins).some((w) => now - w.at < (w.hidden ? HIDDEN_STALE_MS : STALE_MS))
}

/**
 * In the main window: whether the assistants are in their own window now.
 * The window says hello every 2 seconds, and at once when it is hidden or
 * shown, and goodbye when it closes.
 */
export function usePopped(): boolean {
  const [wins, setWins] = useState<Seen>({})
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const off = onPop((m) => {
      if (m.type === 'hello') setWins((w) => ({ ...w, [m.win ?? 'assistants']: { at: Date.now(), hidden: !!m.hidden } }))
      if (m.type === 'bye')
        setWins((w) => {
          const next = { ...w }
          delete next[m.win ?? 'assistants']
          return next
        })
    })
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => {
      off()
      clearInterval(t)
    }
  }, [])
  return anyOpen(wins, now)
}

/** In the assistants' window: says it is open until it closes. */
export function useAnnounce(cluster: string): string {
  const [win] = useState(() => Math.random().toString(36).slice(2, 10))
  useEffect(() => {
    // A closing page turns hidden after its pagehide, and a hello then
    // would call back a window that has said goodbye.
    let gone = false
    const hello = () => {
      if (!gone) post({ type: 'hello', cluster, win, hidden: document.hidden })
    }
    hello()
    const t = setInterval(hello, 2000)
    const bye = () => {
      gone = true
      post({ type: 'bye', win })
    }
    const back = () => {
      gone = false
      hello()
    }
    window.addEventListener('pagehide', bye)
    window.addEventListener('pageshow', back)
    document.addEventListener('visibilitychange', hello)
    return () => {
      clearInterval(t)
      window.removeEventListener('pagehide', bye)
      window.removeEventListener('pageshow', back)
      document.removeEventListener('visibilitychange', hello)
    }
  }, [cluster, win])
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
/**
 * The question an alert's Troubleshoot button asks (Phase 6 review): a
 * troubleshooting report, in the format the system prompt gives for one.
 */
export function troubleshootQuestion(r: { kind: string; name: string; namespace?: string; status?: string; oom?: boolean; restarts?: number }): string {
  const what = r.kind === 'node' ? `node ${r.name}` : r.kind === 'pvc' ? `volume claim ${r.namespace}/${r.name}` : r.kind === 'pv' ? `volume ${r.name}` : `pod ${r.namespace}/${r.name}`
  const why = [r.oom ? 'OOM-killed' : r.status, r.restarts ? `${r.restarts} restarts` : ''].filter(Boolean).join(', ')
  return `Troubleshooting report for ${what}${why ? ` (${why})` : ''}`
}

export function alertQuestion(r: { kind: string; name: string; namespace?: string; status?: string; restarts?: number; oom?: boolean }): string {
  if (r.kind === 'node') return `What is wrong with node ${r.name}?`
  if (r.kind === 'pvc') return `Why is volume claim ${r.namespace}/${r.name} not bound?`
  if (r.oom) return `Why was pod ${r.namespace}/${r.name} OOM-killed?`
  return podQuestion({ namespace: r.namespace ?? '', name: r.name, status: r.status, restarts: r.restarts }, r.namespace ?? '', r.name)
}

/** The question a home card's Troubleshoot button asks, in place of Audit while the cluster has errors or warnings. */
export function clusterTroubleshootQuestion(cluster: string): string {
  return `Troubleshooting report for cluster ${cluster}: what is wrong now, why, and how to fix it, most severe first`
}

/** The question a home card's Audit button asks (Phase 6 review), while the cluster is healthy. */
export function reviewQuestion(cluster: string): string {
  return `Deficiency review of cluster ${cluster}: where does its Kubernetes setup fall short of good practice, and what should change first?`
}
