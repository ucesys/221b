// Small helpers of the Administration area (RB-41 to RB-47): names for
// roles and sources, the totals the Permissions page shows, and what a
// person can do after joining a group.

import type { Drift, Holding, LocalGroup, PermRole, PermRule, RoleId, Source, SuperRole, UserRow } from '../api/admin'

export const ROLE_NAME: Record<RoleId, string> = {
  superAdmin: 'SuperAdmin',
  superViewer: 'SuperViewer',
  admin: 'Admin',
  viewer: 'Viewer',
}

/** Highest first: the role shown for a person who holds several (RB-21). */
export const ROLE_ORDER: RoleId[] = ['superAdmin', 'superViewer', 'admin', 'viewer']

export const SOURCE_NAME: Record<Source | 'other', string> = {
  local: 'Local',
  ldap: 'LDAP',
  oidc: 'OIDC',
  other: 'Not managed by 221B',
}

/** The prefixes Kubernetes sees before names, for local and LDAP (RB-15); OIDC's are chart values, so the page shows the name the server sent. */
export const LOCAL_PREFIX = '221b-local:'
export const LDAP_PREFIX = '221b-ldap:'

/** Whether a role is bound per namespace (Admin and Viewer) or held for every namespace (RB-05, RB-06). */
export function perNamespace(id: RoleId): boolean {
  return id === 'admin' || id === 'viewer'
}

/** The highest role a person holds, or null for none. */
export function topRole(h: Holding[]): RoleId | null {
  for (const r of ROLE_ORDER) if (h.some((x) => x.role === r)) return r
  return null
}

/** "Admin via checkout-ops in checkout, web" as parts the table lays out. */
export function holdingParts(h: Holding): { role: string; via: string; namespaces: string } {
  return { role: ROLE_NAME[h.role], via: h.via ? `via ${h.via}` : '', namespaces: (h.namespaces ?? []).join(', ') }
}

/** The text a Roles or Users search matches for a holding. */
export function holdingText(h: Holding): string {
  const p = holdingParts(h)
  return [p.role, p.via, p.namespaces].filter(Boolean).join(' ')
}

/** A line for a person's roles when sorting: the highest first. */
export function roleRank(h: Holding[]): number {
  const t = topRole(h)
  return t === null ? ROLE_ORDER.length : ROLE_ORDER.indexOf(t)
}

/** Reads and changes a role has, counted as kinds of resource (RB-44). */
export function permTotals(role: Pick<PermRole, 'rules'>): { reads: number; writes: number; kinds: number } {
  const read = new Set<string>()
  const write = new Set<string>()
  // A rule with both reads and changes counts in both.
  for (const r of role.rules.flatMap((x) => splitRule(x))) {
    const key = `${r.group}/${r.resource}`
    ;(r.kind === 'read' ? read : write).add(key)
  }
  const kinds = new Set([...read, ...write])
  return { reads: read.size, writes: write.size, kinds: kinds.size }
}

/** What a role's tile says it may do: "reads 40 kinds, changes 0", or "everything" for a wildcard. */
export function roleTotalsText(role: Pick<PermRole, 'rules'>): string {
  const wild = role.rules.filter((r) => r.resource === '*' && !r.nonResource)
  if (wild.some((r) => r.verbs.includes('*'))) return 'reads and changes everything'
  if (wild.length) return `reads everything, changes ${permTotals(role).writes}`
  return totalsText(permTotals(role))
}

/** "reads 40 kinds, changes 0" for a role's heading. */
export function totalsText(t: { reads: number; writes: number }): string {
  const kind = (n: number) => `${n} ${n === 1 ? 'kind' : 'kinds'}`
  return `reads ${kind(t.reads)}, changes ${t.writes}`
}

/** True when the verbs are only reads: get, list and watch (RB-44). */
export function isRead(verbs: string[]): boolean {
  return verbs.length > 0 && verbs.every((v) => v === 'get' || v === 'list' || v === 'watch')
}

/** Splits a rule's verbs into a Read rule and a Write rule, so the page can list them apart; a rule with both becomes two. */
export function splitRule(r: Pick<PermRule, 'group' | 'resource' | 'verbs' | 'flags' | 'scope' | 'resourceNames' | 'nonResource'>): PermRule[] {
  const reads = r.verbs.filter((v) => v === 'get' || v === 'list' || v === 'watch')
  const writes = r.verbs.filter((v) => !(v === 'get' || v === 'list' || v === 'watch'))
  const out: PermRule[] = []
  if (reads.length) out.push({ ...r, verbs: reads, kind: 'read' })
  if (writes.length) out.push({ ...r, verbs: writes, kind: 'write' })
  return out
}

/** The API group as the page shows it: "core" for the empty group. */
export function groupText(g: string): string {
  return g === '' ? 'core' : g
}

/** What the Permissions page says about the live roles against the shipped ones (RB-36, RB-44). */
export function driftText(d: Drift): { tone: 'ok' | 'warn' | 'muted'; text: string } {
  switch (d.state) {
    case 'match':
      return { tone: 'ok', text: 'The roles on this cluster match the definitions shipped with 221B.' }
    case 'differs': {
      const names = d.roles.filter((r) => r.differences.length).map((r) => ROLE_NAME[r.id])
      return { tone: 'warn', text: `Differs from the shipped definitions: ${names.join(', ')}.` }
    }
    case 'missing':
      return { tone: 'warn', text: 'The roles are not installed on this cluster, so people see nothing here until they are.' }
    default:
      return { tone: 'muted', text: 'Not compared yet.' }
  }
}

/** Names the sources' groups the way a person says them: "web-readers · Viewer · web". */
export function groupChoice(g: LocalGroup): string {
  return `${g.name} · ${ROLE_NAME[g.role]} · ${g.namespaces.join(', ') || 'no namespaces'}`
}

/** What a person can do after joining groups: each group's role and namespaces, worked out from values (RB-43). */
export function afterJoining(user: Pick<UserRow, 'groups' | 'superRoles'>, join: string[], groups: LocalGroup[]): Holding[] {
  const out: Holding[] = []
  for (const s of user.superRoles) out.push({ role: s })
  for (const name of new Set([...user.groups, ...join])) {
    const g = groups.find((x) => x.name === name)
    if (g) out.push({ role: g.role, via: g.name, namespaces: g.namespaces })
  }
  return out
}

/** The groups a local user may still join. */
export function joinable(user: Pick<UserRow, 'groups'>, groups: LocalGroup[]): LocalGroup[] {
  return groups.filter((g) => !user.groups.includes(g.name))
}

/** Why a local user's password is refused, or '' (RB-46): at least 12 characters and not the user name. */
export function passwordProblem(password: string, username: string): string {
  if (password.length < 12) return 'Use at least 12 characters.'
  if (username && password.toLowerCase() === username.toLowerCase()) return 'The password cannot be the user name.'
  return ''
}

const USERNAME = /^[a-z0-9._-]+$/

/** Why a local user name is refused, or '' (RB-16, RB-17): lowercase letters, digits, dot, dash and underscore; nothing reserved. */
export function usernameProblem(name: string): string {
  if (!name) return 'Enter a user name.'
  // A reserved name, system: or 221b:, has a colon, which the pattern refuses.
  if (!USERNAME.test(name)) return name.includes(':') ? 'This name is reserved.' : 'Use lowercase letters, digits, dot, dash and underscore.'
  return ''
}

/** The two super roles a person may hold, for the details pane's checkboxes. */
export const SUPER_ROLES: SuperRole[] = ['superAdmin', 'superViewer']

export const SCOPE_NAME: Record<PermRule['scope'], string> = {
  everywhere: 'Everywhere',
  cluster: 'Cluster-wide',
  all: 'All namespaces',
  bound: 'Bound namespaces',
  own: 'Own only',
}

/** One row of the Permissions matrix: a resource and what each role may do with it (RB-44). */
export interface MatrixRow {
  key: string
  group: string
  resource: string
  access: 'read' | 'write'
  /** Every verb any role has on it, in the order Kubernetes lists them. */
  verbs: string[]
  flags: string[]
  /** A URL path such as /healthz rather than a kind. */
  nonResource: boolean
  /** What each role has: where, and its verbs; absent when it has none. */
  cells: Partial<Record<RoleId, { scope: PermRule['scope']; verbs: string[] }>>
}

const VERB_ORDER = ['get', 'list', 'watch', 'create', 'update', 'patch', 'delete', 'deletecollection', '*']

function verbRank(v: string): number {
  const i = VERB_ORDER.indexOf(v)
  return i < 0 ? VERB_ORDER.length : i
}

const byVerb = (a: string, b: string) => verbRank(a) - verbRank(b) || a.localeCompare(b)

/** Builds the matrix from the four roles' rules: one row per resource and access, one cell per role. */
export function matrixOf(roles: Pick<PermRole, 'id' | 'rules'>[]): MatrixRow[] {
  const rows = new Map<string, MatrixRow>()
  for (const role of roles) {
    for (const part of role.rules.flatMap((r) => splitRule(r))) {
      const key = `${part.kind}:${part.group}/${part.resource}`
      const row = rows.get(key) ?? { key, group: part.group, resource: part.resource, access: part.kind, verbs: [], flags: [], nonResource: !!part.nonResource, cells: {} }
      row.verbs = [...new Set([...row.verbs, ...part.verbs])].sort(byVerb)
      row.flags = [...new Set([...row.flags, ...part.flags])]
      const had = row.cells[role.id]
      row.cells[role.id] = { scope: part.scope, verbs: [...new Set([...(had?.verbs ?? []), ...part.verbs])].sort(byVerb) }
      rows.set(key, row)
    }
  }
  return [...rows.values()].sort((a, b) => (a.access === b.access ? 0 : a.access === 'read' ? -1 : 1) || a.resource.localeCompare(b.resource) || a.group.localeCompare(b.group))
}

/** What a role's tile and details say about where it applies. */
export function reachText(id: RoleId): string {
  return perNamespace(id) ? 'In the namespaces its groups are bound to' : 'Every namespace, and cluster-wide kinds'
}

/** What strict mode is, in a sentence for the Permissions page (RB-36). */
export function strictText(enforce: 'off' | 'strict'): string {
  return enforce === 'strict'
    ? 'Strict mode is on (rbac.enforce): if someone edits these roles to allow more than 221B ships, Watson refuses every change on this cluster until they are put back.'
    : 'Strict mode is off (rbac.enforce): if someone edits these roles to allow more than 221B ships, this page says so, and Watson still runs approved changes. Set rbac.enforce to strict to refuse them.'
}
