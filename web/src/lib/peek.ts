// Details over the tab you are on (asked at the Phase 4 review): a link to
// one node, pod, Service, object or policy opens its details pane over the
// current tab, as ?peek=‹target›, instead of switching tabs. A link whose
// result is several objects (a node's pods, a namespace) still goes to the
// tab that lists them, filtered.
//
// Targets: node:‹name›, pod:‹ns›/‹name›, service:‹ns›/‹name›,
// object:‹Kind›/‹ns›/‹name›, policy:‹policy view key›.

import { currentRoute, href, withParams, type Tab } from './router'

export type Peek =
  | { kind: 'node'; name: string }
  | { kind: 'pod'; namespace: string; name: string }
  | { kind: 'service'; namespace: string; name: string }
  | { kind: 'object'; type: string; namespace: string; name: string }
  | { kind: 'policy'; key: string }

/** The peek parameter of a target. */
export function peekParam(p: Peek): string {
  switch (p.kind) {
    case 'node':
      return `node:${p.name}`
    case 'pod':
    case 'service':
      return `${p.kind}:${p.namespace}/${p.name}`
    case 'object':
      return `object:${p.type}/${p.namespace}/${p.name}`
    case 'policy':
      return `policy:${p.key}`
  }
}

/** Reads a peek parameter; null when it names nothing this console opens. */
export function parsePeek(s: string | null | undefined): Peek | null {
  if (!s) return null
  const i = s.indexOf(':')
  if (i < 0) return null
  const kind = s.slice(0, i)
  const rest = s.slice(i + 1)
  if (!rest) return null
  const slash = rest.indexOf('/')
  switch (kind) {
    case 'node':
      return { kind, name: rest }
    case 'pod':
    case 'service':
      return slash > 0 && slash < rest.length - 1 ? { kind, namespace: rest.slice(0, slash), name: rest.slice(slash + 1) } : null
    case 'object': {
      const parts = rest.split('/')
      return parts.length === 3 && parts.every(Boolean) ? { kind, type: parts[0]!, namespace: parts[1]!, name: parts[2]! } : null
    }
    case 'policy':
      return { kind, key: rest }
  }
  return null
}

/** The objects whose kind has its own pane rather than the generic one. */
function byKind(type: string, namespace: string, name: string): Peek {
  if (type === 'Pod') return { kind: 'pod', namespace, name }
  if (type === 'Service') return { kind: 'service', namespace, name }
  return { kind: 'object', type, namespace, name }
}

/**
 * What a tab link opens, when it opens one object: ?open= on Nodes, Pods
 * and Services, Kind/name on a namespace page, a policy's key. Null for a
 * link to a list.
 */
export function peekOf(tab: Tab, path: string[], params: Record<string, string | string[] | undefined>): Peek | null {
  const open = params.open
  if (typeof open !== 'string' || !open) return null
  const ns = (k: string) => {
    const i = k.indexOf('/')
    return i > 0 ? [k.slice(0, i), k.slice(i + 1)] : null
  }
  switch (tab) {
    case 'nodes':
      return { kind: 'node', name: open }
    case 'pods':
    case 'services': {
      const k = ns(open)
      return k ? { kind: tab === 'pods' ? 'pod' : 'service', namespace: k[0]!, name: k[1]! } : null
    }
    case 'namespaces': {
      const k = ns(open)
      return k && path[0] ? byKind(k[0]!, path[0], k[1]!) : null
    }
    case 'policies':
      return { kind: 'policy', key: open }
  }
  return null
}

/** The hash that opens a target over the page you are on. */
export function peekHref(p: Peek): string {
  return withParams(currentRoute(), { peek: peekParam(p) })
}

/** The attributes of a link to a target: its hash, and data-peek, which App reads on click so the hash is the one of the page as it is then. */
export function peekLink(p: Peek): { href: string; 'data-peek': string } {
  return { href: peekHref(p), 'data-peek': peekParam(p) }
}

/**
 * The attributes of a link to a tab: over the current page when it opens
 * one object, to the tab otherwise.
 */
export function objectLink(tab: Tab, path: string[] = [], params: Record<string, string | string[] | undefined> = {}): { href: string; 'data-peek'?: string } {
  // On the tab that lists the object, it opens there, as it always has.
  const here = currentRoute()
  if (here.view === 'tabs' && here.tab === tab && (tab !== 'namespaces' || here.path[0] === path[0])) return { href: href(tab, path, params) }
  const p = peekOf(tab, path, params)
  return p ? peekLink(p) : { href: href(tab, path, params) }
}

/** Where a click on a link with data-peek should go now: the page as it is, with that target over it. */
export function peekClickHash(target: string): string {
  return withParams(currentRoute(), { peek: target })
}
