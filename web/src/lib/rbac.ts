// Who can do what (P4-03), worked out from the roles and bindings a person
// may see, with the API server's rule matching: a rule allows a request
// when it lists the verb (or *), the API group (or *) and the resource (or
// *, or */subresource); a rule that names objects allows only requests for
// those names. A ClusterRoleBinding applies in every namespace, a
// RoleBinding in its own. server/internal/rbac has the same matching.

import type { BindingRow, Finding, FindingSeverity, RBACRule, RBACSource, RBACSubject, RoleRow, SARow } from '../api/types'
import { nWord } from './format'

export const AC_VERBS = ['get', 'list', 'watch', 'create', 'update', 'patch', 'delete'] as const

export interface AcResource {
  /** "pods", "pods/exec". */
  res: string
  group: string
  label: string
  section: string
  cluster: boolean
}

const R = (res: string, group: string, label: string, section: string, cluster = false): AcResource => ({ res, group, label, section, cluster })

/** The resources the who-can picker and the permissions matrix ask about, in the prototype's order. */
export const AC_RES: AcResource[] = [
  R('pods', '', 'Pods', 'Workloads'),
  R('pods/exec', '', 'Exec into pods', 'Workloads'),
  R('pods/log', '', 'Pod logs', 'Workloads'),
  R('deployments', 'apps', 'Deployments', 'Workloads'),
  R('statefulsets', 'apps', 'StatefulSets', 'Workloads'),
  R('daemonsets', 'apps', 'DaemonSets', 'Workloads'),
  R('jobs', 'batch', 'Jobs', 'Workloads'),
  R('services', '', 'Services', 'Network'),
  R('ingresses', 'networking.k8s.io', 'Ingresses', 'Network'),
  R('configmaps', '', 'ConfigMaps', 'Configuration and storage'),
  R('secrets', '', 'Secrets', 'Configuration and storage'),
  R('persistentvolumeclaims', '', 'PersistentVolumeClaims', 'Configuration and storage'),
  R('serviceaccounts', '', 'ServiceAccounts', 'Access'),
  R('serviceaccounts/token', '', 'ServiceAccount tokens', 'Access'),
  R('roles', 'rbac.authorization.k8s.io', 'Roles', 'Access'),
  R('rolebindings', 'rbac.authorization.k8s.io', 'RoleBindings', 'Access'),
  R('nodes', '', 'Nodes', 'Cluster-scoped', true),
  R('namespaces', '', 'Namespaces', 'Cluster-scoped', true),
  R('persistentvolumes', '', 'PersistentVolumes', 'Cluster-scoped', true),
  R('storageclasses', 'storage.k8s.io', 'StorageClasses', 'Cluster-scoped', true),
  R('clusterroles', 'rbac.authorization.k8s.io', 'ClusterRoles', 'Cluster-scoped', true),
  R('clusterrolebindings', 'rbac.authorization.k8s.io', 'ClusterRoleBindings', 'Cluster-scoped', true),
  R('customresourcedefinitions', 'apiextensions.k8s.io', 'CustomResourceDefinitions', 'Cluster-scoped', true),
]

export function acResource(res: string): AcResource {
  return AC_RES.find((r) => r.res === res) ?? R(res, '', res, '')
}

export interface Request {
  verb: string
  group: string
  resource: string
  sub?: string
  name?: string
}

function has(list: string[] | undefined, v: string): boolean {
  return !!list && (list.includes('*') || list.includes(v))
}

function resourceMatches(rules: string[] | undefined, resource: string, sub?: string): boolean {
  const combined = sub ? `${resource}/${sub}` : resource
  return (rules ?? []).some((r) => r === '*' || r === combined || (!!sub && r === `*/${sub}`))
}

/** Whether a rule covers a request for some object: null when not, [] for any object, or the names it is limited to. */
export function ruleCovers(r: RBACRule, q: Request): string[] | null {
  if (r.nonResourceURLs?.length && !r.resources?.length) return null
  if (!has(r.verbs, q.verb) || !has(r.apiGroups, q.group) || !resourceMatches(r.resources, q.resource, q.sub)) return null
  return r.resourceNames ?? []
}

/** Whether a rule allows a request as the API server decides: named rules only for those names. */
export function ruleAllows(r: RBACRule, q: Request): boolean {
  const names = ruleCovers(r, q)
  return names !== null && (names.length === 0 || (!!q.name && names.includes(q.name)))
}

/** How rules cover a request: 'all', the names when only named objects, or null. */
export function rulesCover(rules: RBACRule[], q: Request): 'all' | string[] | null {
  let names: string[] | null = null
  for (const r of rules) {
    const n = ruleCovers(r, q)
    if (n === null) continue
    if (n.length === 0) return 'all'
    names = [...new Set([...(names ?? []), ...n])]
  }
  return names
}

/** "pods/exec" → the request for verb on it. */
export function requestFor(verb: string, res: string): Request {
  const [resource = res, sub] = res.split('/')
  return { verb, group: acResource(res).group, resource, sub }
}

// ---------- identities ----------

export type IdentityKind = 'User' | 'Group' | 'ServiceAccount'

export interface Identity {
  /** "User:dana@corp.example", "Group:oidc:ops", "ServiceAccount:web/api". */
  key: string
  kind: IdentityKind
  name: string
  namespace?: string
  /** False for a ServiceAccount a binding names that does not exist. */
  exists: boolean
  system: boolean
  sa?: SARow
  bindings: BindingRow[]
}

export function subjectKey(s: Pick<RBACSubject, 'kind' | 'name' | 'namespace'>): string {
  return s.kind === 'ServiceAccount' ? `ServiceAccount:${s.namespace ?? ''}/${s.name}` : `${s.kind}:${s.name}`
}

export function splitIdentityKey(key: string): { kind: IdentityKind; name: string; namespace?: string } | null {
  const i = key.indexOf(':')
  if (i < 0) return null
  const kind = key.slice(0, i) as IdentityKind
  const rest = key.slice(i + 1)
  if (kind === 'ServiceAccount') {
    const j = rest.indexOf('/')
    if (j < 0) return null
    return { kind, namespace: rest.slice(0, j), name: rest.slice(j + 1) }
  }
  if (kind !== 'User' && kind !== 'Group') return null
  return { kind, name: rest }
}

/** "ServiceAccount web/api", "Group ops". */
export function subjectText(s: Pick<RBACSubject, 'kind' | 'name' | 'namespace'>): string {
  return s.kind === 'ServiceAccount' ? `ServiceAccount ${s.namespace}/${s.name}` : `${s.kind} ${s.name}`
}

/** The groups Kubernetes adds to everyone of a kind. */
export function implicitGroups(id: Pick<Identity, 'kind' | 'name' | 'namespace'>): string[] {
  if (id.kind === 'ServiceAccount') return ['system:serviceaccounts', `system:serviceaccounts:${id.namespace}`, 'system:authenticated']
  if (id.kind === 'User') return id.name === 'system:anonymous' ? ['system:unauthenticated'] : ['system:authenticated']
  // Someone whose token carries a group is signed in.
  return id.name === 'system:unauthenticated' ? [] : ['system:authenticated']
}

/** Whether a binding's subject is the identity, as the API server matches them. */
function names(s: RBACSubject, id: Pick<Identity, 'kind' | 'name' | 'namespace'>): boolean {
  if (id.kind === 'ServiceAccount') {
    if (s.kind === 'ServiceAccount') return s.name === id.name && s.namespace === id.namespace
    return s.kind === 'User' && s.name === `system:serviceaccount:${id.namespace}:${id.name}`
  }
  return s.kind === id.kind && s.name === id.name
}

export interface Grant {
  binding: BindingRow
  /** The implicit group it applies through, or null when it names the identity. */
  via: string | null
}

/** Every binding that names the identity or one of the groups Kubernetes puts it in. */
export function grantsOf(id: Pick<Identity, 'kind' | 'name' | 'namespace'>, bindings: BindingRow[]): Grant[] {
  const groups = implicitGroups(id)
  const out: Grant[] = []
  for (const b of bindings) {
    if (b.subjects.some((s) => names(s, id))) out.push({ binding: b, via: null })
    else {
      const g = groups.find((g) => b.subjects.some((s) => s.kind === 'Group' && s.name === g))
      if (g) out.push({ binding: b, via: g })
    }
  }
  return out
}

export type RoleIndex = Map<string, RoleRow>

export function roleIndex(roles: RoleRow[]): RoleIndex {
  return new Map(roles.map((r) => [r.kind === 'ClusterRole' ? `ClusterRole/${r.name}` : `Role/${r.namespace}/${r.name}`, r]))
}

export function roleOf(b: Pick<BindingRow, 'roleKind' | 'roleName' | 'namespace'>, roles: RoleIndex): RoleRow | undefined {
  return roles.get(b.roleKind === 'ClusterRole' ? `ClusterRole/${b.roleName}` : `Role/${b.namespace}/${b.roleName}`)
}

export type Level = 'all' | 'some' | 'named' | 'none'

export interface Check {
  level: Level
  /** Namespaces where RoleBindings grant it, when not everywhere. */
  namespaces: string[]
  /** The objects named, when only named ones. */
  names: string[]
  via: { binding: BindingRow; role: string; group: string | null; named: string[] | null }[]
}

/**
 * What grants allow for a verb on a resource: in every namespace, in the
 * one asked about (ns), or across all of them (ns "*"), where RoleBindings
 * make it "some".
 */
export function check(grants: Grant[], verb: string, res: string, ns: string, roles: RoleIndex): Check {
  const r = acResource(res)
  const q = requestFor(verb, res)
  let all = false
  const nss = new Set<string>()
  const named = new Set<string>()
  const via: Check['via'] = []
  for (const g of grants) {
    const b = g.binding
    if (b.kind === 'RoleBinding' && (r.cluster || (ns !== '*' && b.namespace !== ns))) continue
    const role = roleOf(b, roles)
    if (!role) continue
    const cover = rulesCover(role.rules, q)
    if (cover === null) continue
    via.push({ binding: b, role: role.name, group: g.via, named: cover === 'all' ? null : cover })
    if (cover !== 'all') {
      cover.forEach((n) => named.add(n))
      continue
    }
    if (b.kind === 'ClusterRoleBinding' || ns !== '*') all = true
    else nss.add(b.namespace!)
  }
  const level: Level = all ? 'all' : nss.size ? 'some' : named.size ? 'named' : 'none'
  return { level, namespaces: [...nss].sort(), names: [...named].sort(), via }
}

/** Whether the identity is Kubernetes' superuser group, which RBAC never limits. */
export function isMasters(id: Pick<Identity, 'kind' | 'name'>): boolean {
  return id.kind === 'Group' && id.name === 'system:masters'
}

/** What a person can do, as kubectl auth can-i answers: in a namespace, or "*" for all (-A). */
export function canI(id: Pick<Identity, 'kind' | 'name' | 'namespace'>, verb: string, res: string, ns: string, bindings: BindingRow[], roles: RoleIndex): boolean {
  if (isMasters(id)) return true
  return check(grantsOf(id, bindings), verb, res, ns, roles).level === 'all'
}

// ---------- who can ----------

export interface WhoRow {
  key: string
  kind: IdentityKind
  name: string
  namespace?: string
  /** Every namespace (or cluster-wide), through a ClusterRoleBinding. */
  wide: boolean
  namespaces: string[]
  /** Only these objects, by name. */
  named: string[]
  through: string[]
  system: boolean
}

/** Who a binding names that can do it, with where and through which binding. */
export function whoCan(verb: string, res: string, ns: string, bindings: BindingRow[], roles: RoleIndex, system: (key: string) => boolean): WhoRow[] {
  const r = acResource(res)
  const q = requestFor(verb, res)
  const rows = new Map<string, WhoRow>()
  for (const b of bindings) {
    if (b.kind === 'RoleBinding' && (r.cluster || (ns !== '*' && b.namespace !== ns))) continue
    const role = roleOf(b, roles)
    if (!role) continue
    const cover = rulesCover(role.rules, q)
    if (cover === null) continue
    for (const s of b.subjects) {
      const key = subjectKey(s)
      let row = rows.get(key)
      if (!row) {
        row = { key, kind: s.kind, name: s.name, namespace: s.namespace, wide: false, namespaces: [], named: [], through: [], system: system(key) }
        rows.set(key, row)
      }
      const through = `${b.kind === 'RoleBinding' ? b.namespace + '/' : ''}${b.name} → ${role.name}${cover === 'all' ? '' : ` (only ${cover.join(', ')})`}`
      if (!row.through.includes(through)) row.through.push(through)
      if (cover !== 'all') row.named = [...new Set([...row.named, ...cover])]
      else if (b.kind === 'ClusterRoleBinding') row.wide = true
      else if (!row.namespaces.includes(b.namespace!)) row.namespaces.push(b.namespace!)
    }
  }
  return [...rows.values()]
}

/** Whether a who-can row can do it for any object (not only named ones). */
export function whoAll(r: WhoRow): boolean {
  return r.wide || r.namespaces.length > 0
}

// ---------- identities and summaries ----------

/** Kubernetes' own: system: users and groups, and kube-system ServiceAccounts only its defaults bind. */
export function isSystem(kind: IdentityKind, name: string, namespace: string | undefined, bindings: BindingRow[]): boolean {
  if (kind !== 'ServiceAccount') return name.startsWith('system:')
  if (namespace !== 'kube-system') return false
  return bindings.length > 0 && bindings.every((b) => isDefault(b.source))
}

export function isDefault(s: RBACSource): boolean {
  return s.kind === 'k8s' || s.kind === 'kubeadm'
}

/** Every identity: those bindings name, and ServiceAccounts that pods use or that hold a token that never expires. */
export function identities(bindings: BindingRow[], sas: SARow[]): Identity[] {
  const out = new Map<string, Identity>()
  const saByKey = new Map(sas.map((s) => [`ServiceAccount:${s.namespace}/${s.name}`, s]))
  for (const b of bindings) {
    for (const s of b.subjects) {
      const key = subjectKey(s)
      let id = out.get(key)
      if (!id) {
        const sa = saByKey.get(key)
        id = { key, kind: s.kind, name: s.name, namespace: s.namespace, exists: s.kind !== 'ServiceAccount' || !!sa || !s.missing, system: false, sa, bindings: [] }
        out.set(key, id)
      }
      if (!id.bindings.includes(b)) id.bindings.push(b)
    }
  }
  for (const s of sas) {
    const key = `ServiceAccount:${s.namespace}/${s.name}`
    if (out.has(key) || (!s.pods.length && !s.tokens.length)) continue
    out.set(key, { key, kind: 'ServiceAccount', name: s.name, namespace: s.namespace, exists: true, system: false, sa: s, bindings: [] })
  }
  for (const id of out.values()) id.system = isSystem(id.kind, id.name, id.namespace, id.bindings)
  return [...out.values()]
}

/** "edit in analytics and ingest · view in storage", "cluster-admin everywhere". */
export function summary(id: Identity): string {
  if (!id.bindings.length) return id.kind === 'ServiceAccount' ? 'no bindings: only what every service account has' : 'no bindings'
  const byRole = new Map<string, { wide: boolean; nss: Set<string> }>()
  for (const b of id.bindings) {
    const e = byRole.get(b.roleName) ?? { wide: false, nss: new Set<string>() }
    if (b.kind === 'ClusterRoleBinding') e.wide = true
    else e.nss.add(b.namespace!)
    byRole.set(b.roleName, e)
  }
  const parts = [...byRole.entries()].sort(([a, x], [b, y]) => Number(b === 'cluster-admin') - Number(a === 'cluster-admin') || Number(y.wide) - Number(x.wide) || a.localeCompare(b))
  return parts.map(([role, e]) => (e.wide ? `${role} everywhere` : `${role} in ${listText([...e.nss].sort())}`)).join(' · ')
}

/** "a, b and c". */
export function listText(items: string[]): string {
  if (items.length <= 1) return items.join('')
  return items.slice(0, -1).join(', ') + ' and ' + items[items.length - 1]
}

export const SEV_ORDER: Record<FindingSeverity, number> = { high: 0, medium: 1, low: 2, info: 3 }
export const SEV_TONE: Record<FindingSeverity, string> = { high: 'err', medium: 'warn', low: 'info', info: 'muted' }
export const SEV_TEXT: Record<FindingSeverity, string> = { high: 'High', medium: 'Medium', low: 'Low', info: 'Info' }

export interface ReviewItem extends Finding {
  binding: BindingRow
}

/** Every finding, worst first. */
export function review(bindings: BindingRow[]): ReviewItem[] {
  const out: ReviewItem[] = []
  for (const b of bindings) for (const f of b.findings) out.push({ ...f, binding: b })
  return out.sort((a, b) => SEV_ORDER[a.severity] - SEV_ORDER[b.severity] || a.title.localeCompare(b.title))
}

/** "1 high · 2 medium · 1 low · 2 info", or "Nothing to review". */
export function reviewText(items: Finding[]): string {
  const n = (s: FindingSeverity) => items.filter((f) => f.severity === s).length
  const parts = (['high', 'medium', 'low', 'info'] as FindingSeverity[]).filter((s) => n(s)).map((s) => `${n(s)} ${s}`)
  return parts.length ? parts.join(' · ') : 'Nothing to review'
}

/** The worst finding about an identity that is not info, for its flag. */
export function flagOf(key: string, items: ReviewItem[]): ReviewItem | undefined {
  return items.find((f) => f.severity !== 'info' && f.subject && subjectKey(f.subject) === key)
}

/** The tab's badge: findings that are not info. */
export function accessBadge(items: Finding[]): { count: number; tone: string } {
  const real = items.filter((f) => f.severity !== 'info')
  return { count: real.length, tone: real.some((f) => f.severity === 'high') ? 'err' : real.some((f) => f.severity === 'medium') ? 'warn' : 'muted' }
}

// ---------- texts ----------

export const SOURCE_SHORT: Record<RBACSource['kind'], string> = { k8s: 'Kubernetes', kubeadm: 'kubeadm', helm: 'Helm', managed: 'Managed', manual: 'By hand' }

export function sourceText(s: RBACSource): string {
  switch (s.kind) {
    case 'k8s':
      return 'Kubernetes default'
    case 'kubeadm':
      return 'kubeadm'
    case 'helm':
      return `Helm release ${s.release}`
    case 'managed':
      return `Managed by ${s.manager}`
  }
  return 'No Helm release or manager label: created by hand or from a manifest'
}

/** What the powers sound like: everything is an error, RBAC and Secrets a warning. */
export function powersTone(powers: string[]): string {
  if (powers.includes('everything')) return 'err'
  if (powers.some((p) => p === 'changes RBAC' || p === 'escalate' || p === 'bind' || p === 'impersonates' || p === 'reads Secrets')) return 'warn'
  return 'muted'
}

/** The drawer's status pill for a role. */
export function roleStatus(powers: string[]): { text: string; tone: string } | null {
  if (powers.includes('everything')) return { text: 'Everything', tone: 'err' }
  if (powers.includes('escalate') || powers.includes('bind')) return { text: 'Escalates', tone: 'warn' }
  if (powers.includes('changes RBAC')) return { text: 'Changes RBAC', tone: 'warn' }
  if (powers.includes('impersonates')) return { text: 'Impersonates', tone: 'warn' }
  if (powers.includes('reads Secrets')) return { text: 'Reads Secrets', tone: 'warn' }
  return null
}

/** The purpose of Kubernetes' well-known roles. */
const PURPOSE: Record<string, string> = {
  'cluster-admin': 'Every action on every object, in every namespace',
  admin: 'Everything in a namespace, including its roles and bindings, but not the namespace itself or its quotas',
  edit: 'Read and change most objects in a namespace, Secrets included; not roles or bindings',
  view: 'Read most objects in a namespace, but not Secrets, roles or bindings',
  'system:basic-user': 'Ask what one may do',
  'system:discovery': 'Read the API’s discovery documents and version',
  'system:public-info-viewer': 'Read the version and health endpoints, signed in or not',
  'system:node': 'What a kubelet needs (the Node authorizer narrows it)',
  'system:node-proxier': 'What kube-proxy needs: Services, endpoints and nodes',
  'system:kube-scheduler': 'What the scheduler needs: bind pods to nodes',
  'system:kube-controller-manager': 'What the controller manager needs',
  'system:monitoring': 'Read the API server’s metrics and health endpoints',
  'system:auth-delegator': 'Check tokens and access with the API server, for aggregated API servers',
}

export function purpose(r: Pick<RoleRow, 'name' | 'kind'>): string {
  if (r.kind === 'ClusterRole' && PURPOSE[r.name]) return PURPOSE[r.name]!
  if (r.name.startsWith('system:controller:')) return `What the ${r.name.slice('system:controller:'.length)} needs`
  return ''
}

/** "pods, pods/log named a, b" and "get, list · core". */
export function ruleText(r: RBACRule): { main: string; sub: string } {
  if (r.nonResourceURLs?.length && !r.resources?.length) return { main: r.nonResourceURLs.join(', '), sub: `${r.verbs.join(', ')} (non-resource URLs)` }
  const groups = (r.apiGroups ?? []).map((g) => (g === '' ? 'core' : g)).join(', ')
  return { main: (r.resources ?? []).join(', ') + (r.resourceNames?.length ? ` named ${r.resourceNames.join(', ')}` : ''), sub: `${r.verbs.join(', ')} · ${groups}` }
}

/** "1 identity can", "14 identities can". */
export function canText(n: number): string {
  return `${nWord(n, 'identity', 'identities')} can`
}

/** The command that checks an answer with the API server. */
export function canICommand(id: Pick<Identity, 'kind' | 'name' | 'namespace'>, verb: string, res: string, ns: string, name?: string): string {
  const [resource, sub] = res.split('/')
  const target = name ? `${resource}/${name}` : resource
  const where = acResource(res).cluster ? '' : ns === '*' ? ' -A' : ` -n ${ns}`
  return `kubectl auth can-i ${verb} ${target}${sub ? ` --subresource=${sub}` : ''}${where} ${asArgs(id)}`
}

/** --as for a person or ServiceAccount; a made-up person in the group for a group. */
export function asArgs(id: Pick<Identity, 'kind' | 'name' | 'namespace'>): string {
  if (id.kind === 'ServiceAccount') return `--as=system:serviceaccount:${id.namespace}:${id.name}`
  if (id.kind === 'User') return `--as=${id.name}`
  return `--as=probe --as-group=${id.name}`
}
