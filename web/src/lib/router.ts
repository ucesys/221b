// The hash router: #/pods?node=worker-1&ns=web opens the Pods tab with
// those filters. Links between tabs (P2-09) are plain hrefs, so they work
// in a new window and in the browser's history.
//
// Another cluster's pages live under its id: #/c/analytics-dev/pods. The
// cluster the console runs in (or was started against) is "local" and
// keeps the short form, so every Phase 1 and 2 link still works.

import { useSyncExternalStore } from 'react'

export type Tab = 'nodes' | 'fleets' | 'namespaces' | 'pods' | 'helm' | 'alerts' | 'services' | 'networks' | 'access' | 'policies'

export const TABS: { id: Tab; label: string; tip: string }[] = [
  { id: 'nodes', label: 'Nodes', tip: 'Every node: role, status, what is requested and in use' },
  { id: 'fleets', label: 'Fleets', tip: 'Groups of nodes by the labels and taints they share' },
  { id: 'namespaces', label: 'Namespaces', tip: 'Every namespace and what it holds, by kind' },
  { id: 'pods', label: 'Pods', tip: 'Every pod, filtered by node, namespace and status' },
  { id: 'helm', label: 'Helm', tip: 'Helm releases and their history' },
  { id: 'alerts', label: 'Alerts', tip: 'Pods failing to start or pending, node problems and warning events' },
  { id: 'services', label: 'Services', tip: 'Every Service, its endpoints and the hostnames that publish it' },
  { id: 'networks', label: 'Networks', tip: 'Ingress, DNS, pod and service networks — trace a request hop by hop' },
  { id: 'access', label: 'Access', tip: 'Access Control: who can do what in the cluster' },
  { id: 'policies', label: 'Policies', tip: 'Network policies, disruption budgets, quotas, Pod Security and admission webhooks' },
]

/** The connection of the cluster the console was started against. */
export const LOCAL = 'local'

/** The page: the tabs, or a cluster's own page (its connection, P3-07). */
export type View = 'tabs' | 'cluster'

export interface Route {
  /** The cluster's connection id; "local" without a prefix. */
  cluster: string
  view: View
  tab: Tab
  /** Path segments after the tab: #/namespaces/web → ["web"]. */
  path: string[]
  params: URLSearchParams
}

export function parseHash(hash: string): Route {
  const h = hash.replace(/^#\/?/, '')
  const [pathPart = '', query = ''] = h.split('?', 2) as [string?, string?]
  let segs = pathPart.split('/').filter(Boolean).map(decodeURIComponent)
  let cluster = LOCAL
  if (segs[0] === 'c' && segs[1]) {
    cluster = segs[1]
    segs = segs.slice(2)
  }
  const first = segs[0] ?? ''
  if (first === 'cluster') return { cluster, view: 'cluster', tab: 'nodes', path: [], params: new URLSearchParams(query) }
  const tab: Tab = TABS.some((t) => t.id === first) ? (first as Tab) : 'nodes'
  return { cluster, view: 'tabs', tab, path: tab === first ? segs.slice(1) : [], params: new URLSearchParams(query) }
}

function query(params: Record<string, string | string[] | undefined>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === '' || (Array.isArray(v) && v.length === 0)) continue
    q.set(k, Array.isArray(v) ? v.join(',') : v)
  }
  const s = q.toString()
  return s ? '?' + s : ''
}

function prefix(cluster: string): string {
  return cluster === LOCAL ? '#/' : '#/c/' + encodeURIComponent(cluster) + '/'
}

/**
 * Builds a hash in the cluster open now: href('pods', [], {node: 'a'}) →
 * "#/pods?node=a", or "#/c/dev/pods?node=a" when another cluster is open.
 * Every link between tabs stays in the cluster it was made in.
 */
export function href(tab: Tab, path: string[] = [], params: Record<string, string | string[] | undefined> = {}): string {
  return clusterHref(current.cluster, tab, path, params)
}

/** A hash in a given cluster, for the cluster menu (P3-04). */
export function clusterHref(cluster: string, tab: Tab, path: string[] = [], params: Record<string, string | string[] | undefined> = {}): string {
  return prefix(cluster) + [tab, ...path.map(encodeURIComponent)].join('/') + query(params)
}

/** A cluster's own page: "#/cluster", or "#/c/dev/cluster". */
export function clusterPageHref(cluster: string): string {
  return prefix(cluster) + 'cluster'
}

/** The API path of a cluster's data: "pods/web/api" → "/api/v1/pods/web/api", or under /api/v1/clusters/‹id›/ for another cluster. */
export function apiPath(path: string, cluster: string = current.cluster): string {
  return cluster === LOCAL ? '/api/v1/' + path : '/api/v1/clusters/' + encodeURIComponent(cluster) + '/' + path
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
  return clusterHref(route.cluster, route.tab, route.path, merged)
}

/** For tests: sets the route the links are built in. */
export function setRouteForTests(hash: string): void {
  current = parseHash(hash)
}
