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

// ---------- the panel in a window of its own ----------

const CHANNEL = '221b-sherlock'
const WINDOW = '221b-sherlock'

export type PopMessage =
  | { type: 'hello'; cluster: string }
  | { type: 'bye' }
  | { type: 'cluster'; cluster: string }
  | { type: 'focus' }
  | { type: 'close' }
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

/** Whether this browser can move the panel to its own window. */
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

let popup: Window | null = null

/** The hash of Sherlock's own window for a cluster. */
export function sherlockHash(cluster: string): string {
  return cluster === LOCAL ? '#/sherlock' : '#/c/' + encodeURIComponent(cluster) + '/sherlock'
}

/** Opens Sherlock in a window of its own; false when the browser refused. */
export function popOut(cluster: string): boolean {
  if (popup && !popup.closed) {
    popup.focus()
    return true
  }
  const url = window.location.pathname + window.location.search + sherlockHash(cluster)
  popup = window.open(url, WINDOW, 'popup,width=760,height=900')
  if (!popup) return false
  popup.focus()
  return true
}

/** Brings the separate window forward, from a click in the main one. */
export function focusPopup(): void {
  if (popup && !popup.closed) popup.focus()
  else post({ type: 'focus' })
}

/** How long a separate window may stay silent before it counts as gone. */
const STALE_MS = 5000

/**
 * In the main window: whether Sherlock is in a window of its own now. The
 * separate window says hello every 2 seconds and goodbye when it closes.
 */
export function usePoppedOut(): boolean {
  const [seen, setSeen] = useState(0)
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const off = onPop((m) => {
      if (m.type === 'hello') setSeen(Date.now())
      if (m.type === 'bye') setSeen(0)
    })
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => {
      off()
      clearInterval(t)
    }
  }, [])
  return seen > 0 && now - seen < STALE_MS
}

/** In the separate window: says hello until it closes. */
export function useAnnounce(cluster: string): void {
  useEffect(() => {
    const hello = () => post({ type: 'hello', cluster })
    hello()
    const t = setInterval(hello, 2000)
    const bye = () => post({ type: 'bye' })
    window.addEventListener('pagehide', bye)
    return () => {
      clearInterval(t)
      window.removeEventListener('pagehide', bye)
    }
  }, [cluster])
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
