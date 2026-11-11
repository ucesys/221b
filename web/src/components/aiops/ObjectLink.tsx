// A link to the object a finding, case or proposed change is about, with
// how long it has been running (Phase 12, IB-06, ADR-111): a Kubernetes
// object opens its page in the Kubernetes area on its cluster, a host its
// row in Automation › Nodes. The object is read live through the endpoints
// its own drawer reads, kept for a short while for every link that names
// it, and shows "gone" once it no longer exists.
import { useEffect, useSyncExternalStore } from 'react'
import { jsonHeaders, unauthorized } from '../../api/auth'
import { fmtMinutes, minutesSince } from '../../lib/format'
import { kindNameOf } from '../../lib/kinds'
import { apiPath, clusterHref, type Tab } from '../../lib/router'

/** An object as findings and cards name it: "namespace/kind/name" or "kind/name". */
export interface ObjectRef {
  /** The kind as Kubernetes names it ("Deployment"), or "host". */
  kind: string
  namespace: string
  name: string
  /** Whether 221B reads this kind, so it can link it and say its age. */
  known: boolean
}

// The kinds a resource may name, by every spelling agents use: the kind,
// its plural and kubectl's short name, any case. Cluster-scoped ones say so.
const KINDS: { kind: string; also: string[]; cluster?: boolean }[] = [
  { kind: 'Node', also: ['nodes', 'no'], cluster: true },
  { kind: 'Namespace', also: ['namespaces', 'ns'], cluster: true },
  { kind: 'Pod', also: ['pods', 'po'] },
  { kind: 'Deployment', also: ['deployments', 'deploy'] },
  { kind: 'StatefulSet', also: ['statefulsets', 'sts'] },
  { kind: 'DaemonSet', also: ['daemonsets', 'ds'] },
  { kind: 'ReplicaSet', also: ['replicasets', 'rs'] },
  { kind: 'Job', also: ['jobs'] },
  { kind: 'CronJob', also: ['cronjobs', 'cj'] },
  { kind: 'Service', also: ['services', 'svc'] },
  { kind: 'Ingress', also: ['ingresses', 'ing'] },
  { kind: 'NetworkPolicy', also: ['networkpolicies', 'netpol'] },
  { kind: 'PersistentVolumeClaim', also: ['persistentvolumeclaims', 'pvc'] },
  { kind: 'ConfigMap', also: ['configmaps', 'cm'] },
  { kind: 'Secret', also: ['secrets'] },
  { kind: 'ServiceAccount', also: ['serviceaccounts', 'sa'] },
  { kind: 'Role', also: ['roles'] },
  { kind: 'RoleBinding', also: ['rolebindings'] },
  { kind: 'PodDisruptionBudget', also: ['poddisruptionbudgets', 'pdb'] },
  { kind: 'ResourceQuota', also: ['resourcequotas', 'quota'] },
  { kind: 'LimitRange', also: ['limitranges', 'limits'] },
  { kind: 'EndpointSlice', also: ['endpointslices'] },
  { kind: 'ClusterRole', also: ['clusterroles'], cluster: true },
  { kind: 'ClusterRoleBinding', also: ['clusterrolebindings'], cluster: true },
  { kind: 'PriorityClass', also: ['priorityclasses', 'pc'], cluster: true },
  { kind: 'StorageClass', also: ['storageclasses', 'sc'], cluster: true },
  { kind: 'PersistentVolume', also: ['persistentvolumes', 'pv'], cluster: true },
  { kind: 'IngressClass', also: ['ingressclasses'], cluster: true },
]

const BY_NAME = new Map<string, (typeof KINDS)[number]>()
for (const k of KINDS) for (const n of [k.kind, ...k.also]) BY_NAME.set(n.toLowerCase(), k)

/** The kinds whose age is how long they have been running; the others' is their age. */
const RUNS = new Set(['Pod', 'Node', 'Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet', 'Job', 'CronJob', 'Service'])

/** Reads a resource; null when it names no object (a single word, or empty). */
export function parseResource(resource: string): ObjectRef | null {
  const parts = resource
    .trim()
    .split('/')
    .map((p) => p.trim())
  if (parts.some((p) => !p)) return null
  let ns = ''
  let kind: string
  let name: string
  if (parts.length === 3) [ns, kind, name] = parts as [string, string, string]
  else if (parts.length === 2) [kind, name] = parts as [string, string]
  else return null
  if (/^hosts?$/i.test(kind)) return { kind: 'host', namespace: '', name, known: true }
  const k = BY_NAME.get(kind.toLowerCase())
  if (!k) return { kind, namespace: ns, name, known: false }
  // A namespaced kind with no namespace cannot be read; a cluster-scoped
  // one with a namespace keeps its name.
  if (!k.cluster && !ns) return { kind: k.kind, namespace: '', name, known: false }
  return { kind: k.kind, namespace: k.cluster ? '' : ns, name, known: true }
}

/** The object's page in its cluster: the tab that lists it, with it open. */
export function objectHref(cluster: string, o: ObjectRef): string | null {
  if (!o.known) return null
  const at = (tab: Tab, path: string[], open?: string) => clusterHref(cluster, tab, path, open ? { open } : {})
  switch (o.kind) {
    case 'host':
    case 'Node':
      return at('nodes', [], o.name)
    case 'Pod':
      return at('pods', [], `${o.namespace}/${o.name}`)
    case 'Service':
      return at('services', [], `${o.namespace}/${o.name}`)
    case 'Namespace':
      return at('namespaces', [o.name])
    case 'ClusterRole':
    case 'ClusterRoleBinding':
      return at('access', [])
    case 'StorageClass':
      return clusterHref(cluster, 'storage', [], { peek: `class:${o.name}` })
    case 'PersistentVolume':
      return clusterHref(cluster, 'storage', [], { peek: `volume:${o.name}` })
    case 'PersistentVolumeClaim':
      return clusterHref(cluster, 'storage', [], { peek: `claim:${o.namespace}/${o.name}` })
  }
  return o.namespace ? at('namespaces', [o.namespace], `${o.kind}/${o.name}`) : null
}

/** Where the object is read: the endpoint its drawer reads. Null for a host, which has no age here. */
export function objectApi(cluster: string, o: ObjectRef): string | null {
  if (!o.known || o.kind === 'host') return null
  const e = encodeURIComponent
  switch (o.kind) {
    case 'Node':
      return apiPath(`nodes/${e(o.name)}`, cluster)
    case 'Pod':
      return apiPath(`pods/${e(o.namespace)}/${e(o.name)}`, cluster)
    case 'Service':
      return apiPath(`services/${e(o.namespace)}/${e(o.name)}`, cluster)
  }
  // A cluster-scoped object is asked about with any namespace: it is not read.
  return apiPath(`objects/${e(kindNameOf(o.kind))}/${e(o.namespace || '-')}/${e(o.name)}`, cluster)
}

/** What a read found: since when it runs, or that it is gone or hidden. */
export interface ObjectState {
  state: 'loading' | 'live' | 'gone' | 'hidden' | 'unknown'
  /** When it started (a pod's startTime) or was created. */
  since?: string
}

/** How long a read is kept for every link that names the object. */
export const OBJECT_TTL = 30_000

const cache = new Map<string, { at: number; value: ObjectState; pending?: boolean }>()
const listeners = new Set<() => void>()
// What changes for every link: a read that came back, or the minute.
let version = 0
let minute = Date.now()

function changed() {
  version++
  for (const fn of listeners) fn()
}

function read(url: string): Promise<ObjectState> {
  return fetch(url, { headers: jsonHeaders(true) }).then(
    async (r): Promise<ObjectState> => {
      if (r.status === 404) return { state: 'gone' }
      if (r.status === 403) return { state: 'hidden' }
      if (!r.ok || unauthorized(r)) return { state: 'unknown' }
      const d = (await r.json()) as { started?: string; created?: string }
      const since = d.started || d.created
      return since ? { state: 'live', since } : { state: 'live' }
    },
    (): ObjectState => ({ state: 'unknown' }),
  )
}

/** Reads an object once for every link that shows it, and again once its read is OBJECT_TTL old. */
export function lookUp(url: string, now = Date.now()): void {
  const hit = cache.get(url)
  if (hit && (hit.pending || now - hit.at < OBJECT_TTL)) return
  cache.set(url, { at: hit?.at ?? 0, value: hit?.value ?? LOADING, pending: true })
  void read(url).then((v) => {
    cache.set(url, { at: Date.now(), value: v })
    changed()
  })
}

/** For tests: forgets every object read. */
export function forgetObjectsForTests(): void {
  cache.clear()
}

const LOADING: ObjectState = { state: 'loading' }
const UNKNOWN: ObjectState = { state: 'unknown' }

// One clock for every link: their ages move on together, once a minute,
// and each read is renewed when its time is up.
let clock: ReturnType<typeof setInterval> | undefined
function subscribe(fn: () => void) {
  listeners.add(fn)
  if (!clock) {
    // The first link after none: the time moves on to now, which the
    // store sees as a change and draws again.
    minute = Date.now()
    version++
    clock = setInterval(() => {
      minute = Date.now()
      changed()
    }, 60_000)
  }
  return () => {
    listeners.delete(fn)
    if (listeners.size === 0 && clock) {
      clearInterval(clock)
      clock = undefined
    }
  }
}
const versionNow = () => version

/** The live state of an object, read through the shared cache, and the time for its age. */
export function useObjectState(url: string | null): { value: ObjectState; now: number } {
  const v = useSyncExternalStore(subscribe, versionNow, versionNow)
  useEffect(() => {
    if (url) lookUp(url)
  }, [url, v])
  return { value: url ? (cache.get(url)?.value ?? LOADING) : UNKNOWN, now: minute }
}

const minuteNow = () => minute

/** The time, moving on once a minute with every other age on the page. */
export function useMinute(): number {
  return useSyncExternalStore(subscribe, minuteNow, minuteNow)
}

/** How long an item has been open or waiting: "3h 5m" (IB-07). */
export function OpenFor({ since, prefix = '' }: { since: string; prefix?: string }) {
  const now = useMinute()
  if (!since) return null
  return (
    <span className="cs-open" data-testid="open-for">
      {prefix}
      {fmtMinutes(minutesSince(since, now))}
    </span>
  )
}

/** "running 3d 4h", "age 12m": how long the object has been there. */
export function ageText(kind: string, since: string, now: number): string {
  return `${RUNS.has(kind) ? 'running' : 'age'} ${fmtMinutes(minutesSince(since, now))}`
}

/**
 * The object a finding, case or proposed change names, linked to its page
 * on its cluster, with its age, or "gone". What names no object, or a kind
 * 221B does not read, shows as text.
 */
export function ObjectLink({ cluster, resource, label }: { cluster: string; resource: string; label?: string }) {
  const o = parseResource(resource)
  const href = o && cluster ? objectHref(cluster, o) : null
  const api = o && cluster ? objectApi(cluster, o) : null
  const { value, now } = useObjectState(api)
  const text = label ?? resource
  return (
    <span className="cs-obj" data-testid="object-link" data-state={api ? value.state : 'text'}>
      {href ? (
        <a className="rk-link rk-mono" href={href} title={`Open ${o!.kind === 'host' ? 'the host' : o!.kind} ${o!.name} on ${cluster}`}>
          {text}
        </a>
      ) : (
        <span className="rk-mono">{text}</span>
      )}
      {api && value.state === 'live' && value.since && (
        <span className="cs-obj-age" title={`Since ${value.since}`} data-testid="object-age">
          {ageText(o!.kind, value.since, now)}
        </span>
      )}
      {api && value.state === 'gone' && (
        <span className="rk-tag" data-tone="muted" title="It no longer exists on the cluster" data-testid="object-gone">
          gone
        </span>
      )}
    </span>
  )
}
