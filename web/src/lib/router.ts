// The hash router: #/pods?node=worker-1&ns=web opens the Pods tab with
// those filters. Links between tabs (P2-09) are plain hrefs, so they work
// in a new window and in the browser's history.

import { useSyncExternalStore } from 'react'

export type Tab = 'nodes' | 'namespaces' | 'pods' | 'helm' | 'services'

export const TABS: { id: Tab; label: string; tip: string }[] = [
  { id: 'nodes', label: 'Nodes', tip: 'Every node: role, status, what is requested and in use' },
  { id: 'namespaces', label: 'Namespaces', tip: 'Every namespace and what it holds, by kind' },
  { id: 'pods', label: 'Pods', tip: 'Every pod, filtered by node, namespace and status' },
  { id: 'helm', label: 'Helm', tip: 'Helm releases and their history' },
  { id: 'services', label: 'Services', tip: 'Every Service, its endpoints and the hostnames that publish it' },
]

/** The tabs in the bar from the left; the rest live under More. */
export const PRIMARY_TABS = 4

export interface Route {
  tab: Tab
  /** Path segments after the tab: #/namespaces/web → ["web"]. */
  path: string[]
  params: URLSearchParams
}

export function parseHash(hash: string): Route {
  const h = hash.replace(/^#\/?/, '')
  const [pathPart = '', query = ''] = h.split('?', 2) as [string?, string?]
  const segs = pathPart.split('/').filter(Boolean).map(decodeURIComponent)
  const first = segs[0] ?? ''
  const tab: Tab = TABS.some((t) => t.id === first) ? (first as Tab) : 'nodes'
  return { tab, path: tab === first ? segs.slice(1) : [], params: new URLSearchParams(query) }
}

/** Builds a hash: href('pods', [], {node: 'a'}) → "#/pods?node=a". */
export function href(tab: Tab, path: string[] = [], params: Record<string, string | string[] | undefined> = {}): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === '' || (Array.isArray(v) && v.length === 0)) continue
    q.set(k, Array.isArray(v) ? v.join(',') : v)
  }
  const s = q.toString()
  return '#/' + [tab, ...path.map(encodeURIComponent)].join('/') + (s ? '?' + s : '')
}

/** A comma-separated list parameter: ?ns=a,b → ["a", "b"]. */
export function listParam(params: URLSearchParams, key: string): string[] {
  const v = params.get(key)
  return v ? v.split(',').filter(Boolean) : []
}

const listeners = new Set<() => void>()
let current = parseHash(typeof window === 'undefined' ? '' : window.location.hash)

function onChange() {
  current = parseHash(window.location.hash)
  for (const fn of listeners) fn()
}

function subscribe(fn: () => void) {
  if (listeners.size === 0 && typeof window !== 'undefined') window.addEventListener('hashchange', onChange)
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
    if (listeners.size === 0 && typeof window !== 'undefined') window.removeEventListener('hashchange', onChange)
  }
}

export function useRoute(): Route {
  return useSyncExternalStore(subscribe, () => current, () => current)
}

/** Goes to a hash. replace keeps the history entry, for filter changes. */
export function navigate(hash: string, replace = false): void {
  if (window.location.hash === hash || (hash === '#/' && !window.location.hash)) return
  if (replace) {
    window.history.replaceState(null, '', hash)
    onChange()
  } else {
    window.location.hash = hash
  }
}

/** Changes some parameters of the current route, keeping the rest. */
export function withParams(route: Route, changes: Record<string, string | string[] | undefined>): string {
  const merged: Record<string, string | string[] | undefined> = {}
  for (const [k, v] of route.params.entries()) merged[k] = v
  Object.assign(merged, changes)
  return href(route.tab, route.path, merged)
}
