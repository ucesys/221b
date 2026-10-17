// The columns of the Administration tables (RB-42, RB-44, RB-45): Roles,
// Permissions and Users. Each defines what it shows and what it
// exports, so the tables sort, pick columns and export as every other
// table does (RB-55, X-13).

import type { RoleId, RoleRow, UserRow } from '../api/admin'
import { fmtAge, fmtTs } from '../lib/format'
import { groupText, holdingParts, ROLE_NAME, ROLE_ORDER, roleRank, SCOPE_NAME, SOURCE_NAME, type MatrixRow } from '../lib/admin'
import type { ColumnDef, Term } from '../lib/table'
import { Mono } from './cells'

const DEFINITION = { full: 'Full', readOnly: 'Read-only' } as const

/** What a Roles search matches. */
export function roleTerms(r: RoleRow): Term[] {
  return [
    { kind: 'name', text: ROLE_NAME[r.id] },
    { kind: 'name', text: r.clusterRole },
    ...r.subjects.map((s) => ({ kind: 'name', text: s.name })),
    ...r.subjects.flatMap((s) => s.namespaces.map((n) => ({ kind: 'namespace', text: n }))),
  ]
}

function holdersText(r: RoleRow): string {
  const g = r.subjects.filter((s) => s.kind === 'Group' && s.managed).length
  const others = r.subjects.filter((s) => !s.managed).length
  const parts: string[] = []
  if (r.id === 'superAdmin' || r.id === 'superViewer') {
    parts.push(`${r.holders.groups} ${r.holders.groups === 1 ? 'group' : 'groups'}`)
    if (r.holders.users) parts.push(`${r.holders.users} ${r.holders.users === 1 ? 'user' : 'users'}`)
  } else {
    parts.push(`${g} ${g === 1 ? 'group' : 'groups'}`)
  }
  if (others) parts.push(`${others} not managed by 221B`)
  return parts.join(' · ')
}

export const ROLE_COLUMNS: ColumnDef<RoleRow>[] = [
  {
    id: 'role',
    label: 'Role',
    width: 'minmax(260px, 2fr)',
    min: 260,
    // In order of reach, the way the roles are listed everywhere.
    sortValue: (r) => ROLE_ORDER.indexOf(r.id),
    cell: (r) => (
      <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0 }}>
        <span style={{ fontWeight: 600 }}>{ROLE_NAME[r.id]}</span>
        <span style={{ fontSize: 11.5, color: 'var(--fg3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{r.description}</span>
      </span>
    ),
    export: (r) => [{ head: 'Role', cell: ROLE_NAME[r.id] }],
  },
  {
    id: 'scope',
    label: 'Where it applies',
    width: 'minmax(170px, 1fr)',
    min: 170,
    sortValue: (r) => r.scope,
    cell: (r) => <span>{r.scope}</span>,
    export: (r) => [{ head: 'Where it applies', cell: r.scope }],
  },
  {
    id: 'definition',
    label: 'Definition',
    width: '110px',
    min: 110,
    sortValue: (r) => r.definition,
    cell: (r) => (
      <span className="rk-tag" data-tone={r.definition === 'full' ? 'info' : 'muted'}>
        {DEFINITION[r.definition]}
      </span>
    ),
    export: (r) => [{ head: 'Definition', cell: DEFINITION[r.definition] }],
  },
  {
    id: 'holders',
    label: 'Holders',
    sortName: 'number of holders',
    width: 'minmax(190px, 1.2fr)',
    min: 190,
    defaultDir: 'desc',
    sortValue: (r) => r.subjects.length,
    cell: (r) => <span>{holdersText(r)}</span>,
    export: (r) => [{ head: 'Holders', cell: holdersText(r) }],
  },
  {
    id: 'object',
    label: 'Kubernetes object',
    width: 'minmax(150px, .9fr)',
    min: 150,
    sortValue: (r) => r.clusterRole,
    cell: (r) => <Mono text={r.clusterRole} size={12} tip={r.present ? undefined : 'Not installed on this cluster'} />,
    export: (r) => [{ head: 'Kubernetes object', cell: r.clusterRole }],
  },
]

const ACCESS = { read: 'Read', write: 'Write' } as const

const roleCol = (id: RoleId): ColumnDef<MatrixRow> => ({
  id: `role-${id}`,
  label: ROLE_NAME[id],
  hint: `Where ${ROLE_NAME[id]} has it`,
  width: 'minmax(120px, .8fr)',
  min: 120,
  sortValue: (r) => {
    const c = r.cells[id]
    return c ? Object.keys(SCOPE_NAME).indexOf(c.scope) : null
  },
  cell: (r) => {
    const c = r.cells[id]
    if (!c) return <span style={{ color: 'var(--fg3)' }}>—</span>
    const partial = c.verbs.join() !== r.verbs.join()
    return (
      <span title={c.verbs.join(', ')} style={{ display: 'inline-flex', flexDirection: 'column', minWidth: 0 }}>
        <span>{SCOPE_NAME[c.scope]}</span>
        {partial && (
          <span className="rk-mono" style={{ fontSize: 11, color: 'var(--fg3)' }}>
            {c.verbs.join(', ')}
          </span>
        )}
      </span>
    )
  },
  export: (r) => {
    const c = r.cells[id]
    return [{ head: ROLE_NAME[id], cell: c ? SCOPE_NAME[c.scope] + (c.verbs.join() !== r.verbs.join() ? ` (${c.verbs.join(', ')})` : '') : 'None' }]
  },
})

/** The matrix of RB-44: a resource, what it is, the verbs, and each role's reach. */
export const MATRIX_COLUMNS: ColumnDef<MatrixRow>[] = [
  {
    id: 'resource',
    label: 'Resource',
    width: 'minmax(200px, 1.4fr)',
    min: 200,
    sortValue: (r) => r.resource,
    cell: (r) => (
      <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0 }}>
        <span className="rk-mono" style={{ fontSize: 12.5, overflow: 'hidden', textOverflow: 'ellipsis' }}>
          {r.resource === '*' ? (r.nonResource ? 'every URL path' : 'every kind') : r.resource}
        </span>
        <span className="rk-mono" style={{ fontSize: 11, color: 'var(--fg3)' }}>
          {r.nonResource ? 'non-resource URL' : r.group === '*' ? 'every API group' : groupText(r.group)}
        </span>
      </span>
    ),
    export: (r) => [
      { head: 'Resource', cell: r.resource },
      { head: 'API group', cell: r.nonResource ? '' : groupText(r.group) },
    ],
  },
  {
    id: 'access',
    label: 'Access',
    width: '80px',
    min: 80,
    sortValue: (r) => (r.access === 'read' ? 0 : 1),
    cell: (r) => <span>{ACCESS[r.access]}</span>,
    export: (r) => [{ head: 'Access', cell: ACCESS[r.access] }],
  },
  {
    id: 'verbs',
    label: 'Verbs',
    width: 'minmax(140px, 1fr)',
    min: 140,
    sortValue: (r) => r.verbs.length,
    cell: (r) => <Mono text={r.verbs.includes('*') ? 'every verb' : r.verbs.join(', ')} size={12} />,
    export: (r) => [{ head: 'Verbs', cell: r.verbs.join(', ') }],
  },
  roleCol('superAdmin'),
  roleCol('superViewer'),
  roleCol('admin'),
  roleCol('viewer'),
  {
    id: 'flags',
    label: 'Sensitive',
    hint: 'Wildcards, Secrets, escalate, bind, impersonate, exec, attach, port-forward, node proxy and service account tokens',
    width: 'minmax(110px, .7fr)',
    min: 110,
    sortValue: (r) => r.flags.length,
    cell: (r) => (r.flags.length ? <span style={{ color: 'var(--warn)' }}>{r.flags.join(', ')}</span> : <span style={{ color: 'var(--fg3)' }}>—</span>),
    export: (r) => [{ head: 'Sensitive', cell: r.flags.join(', ') }],
  },
]

/** What a Permissions search matches. */
export function matrixTerms(r: MatrixRow): Term[] {
  return [
    { kind: 'name', text: r.resource },
    { kind: 'type', text: groupText(r.group) },
    ...r.verbs.map((v) => ({ kind: 'detail', text: v })),
    ...r.flags.map((f) => ({ kind: 'detail', text: f })),
  ]
}

function rolesText(u: UserRow): string {
  if (!u.holdings.length) return 'None'
  return u.holdings
    .map((h) => {
      const p = holdingParts(h)
      return [p.role, p.via, p.namespaces].filter(Boolean).join(' ')
    })
    .join('; ')
}

/** What a Users search matches. */
export function userTerms(u: UserRow): Term[] {
  return [
    { kind: 'name', text: u.username },
    ...(u.name ? [{ kind: 'name', text: u.name }] : []),
    ...(u.email ? [{ kind: 'detail', text: u.email }] : []),
    { kind: 'type', text: SOURCE_NAME[u.source] },
    { kind: 'status', text: u.status === 'active' ? 'Active' : 'Disabled' },
    ...u.holdings.flatMap((h) => [{ kind: 'detail', text: ROLE_NAME[h.role] }, ...(h.via ? [{ kind: 'detail', text: h.via }] : []), ...(h.namespaces ?? []).map((n) => ({ kind: 'namespace', text: n }))]),
  ]
}

export const USER_COLUMNS: ColumnDef<UserRow>[] = [
  {
    id: 'name',
    label: 'Name',
    width: 'minmax(220px, 1.6fr)',
    min: 220,
    sortValue: (u) => u.name || u.username,
    cell: (u) => (
      <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0 }}>
        <span style={{ fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis' }}>{u.name || u.username}</span>
        <span style={{ fontSize: 11.5, color: 'var(--fg3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{u.email || (u.managed ? 'Defined in values.yaml' : u.username)}</span>
      </span>
    ),
    export: (u) => [
      { head: 'Name', cell: u.name || u.username },
      { head: 'User name', cell: u.username },
      { head: 'Email', cell: u.email ?? '' },
    ],
  },
  {
    id: 'source',
    label: 'Source',
    width: '150px',
    min: 150,
    sortValue: (u) => u.source,
    cell: (u) => (
      <span style={{ display: 'inline-flex', gap: 6, alignItems: 'center' }}>
        <span className="rk-tag" data-tone="muted">
          {SOURCE_NAME[u.source]}
        </span>
        {u.managed && (
          <span className="rk-tag" data-tone="info" title="Defined in values.yaml; change it with helm upgrade">
            Helm
          </span>
        )}
      </span>
    ),
    export: (u) => [{ head: 'Source', cell: SOURCE_NAME[u.source] + (u.managed ? ' (Helm)' : '') }],
  },
  {
    id: 'status',
    label: 'Status',
    width: 'minmax(200px, 1.2fr)',
    min: 200,
    sortValue: (u) => (u.status === 'active' ? 0 : 1),
    cell: (u) => (
      <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0 }}>
        <span className="rk-t" data-tone={u.status === 'active' ? 'ok' : 'muted'} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontWeight: 500 }}>
          <span className="rk-dot" />
          {u.status === 'active' ? 'Active' : 'Disabled'}
        </span>
        {u.statusNote && <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{u.statusNote}</span>}
      </span>
    ),
    export: (u) => [{ head: 'Status', cell: (u.status === 'active' ? 'Active' : 'Disabled') + (u.statusNote ? ` — ${u.statusNote}` : '') }],
  },
  {
    id: 'lastSignIn',
    label: 'Last sign-in',
    width: '130px',
    min: 130,
    defaultDir: 'desc',
    sortValue: (u) => (u.lastSignIn ? Date.parse(u.lastSignIn) : null),
    cell: (u, now) => (u.lastSignIn ? <span title={fmtTs(u.lastSignIn)}>{fmtAge(u.lastSignIn, now)} ago</span> : <span style={{ color: 'var(--fg3)' }}>Never</span>),
    export: (u) => [{ head: 'Last sign-in', cell: u.lastSignIn ?? '' }],
  },
  {
    id: 'roles',
    label: 'Roles',
    width: 'minmax(220px, 1.6fr)',
    min: 220,
    sortValue: (u) => roleRank(u.holdings),
    cell: (u) =>
      u.holdings.length === 0 ? (
        <span style={{ color: 'var(--fg3)' }}>None</span>
      ) : (
        <span style={{ display: 'flex', flexWrap: 'wrap', gap: '2px 8px', alignItems: 'center' }}>
          {u.holdings.map((h, i) => {
            const p = holdingParts(h)
            return (
              <span key={i} style={{ display: 'inline-flex', gap: 5, alignItems: 'center' }}>
                <span className="rk-tag" data-tone={h.role === 'superAdmin' || h.role === 'admin' ? 'info' : 'muted'}>
                  {p.role}
                </span>
                {p.via && <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{p.via}</span>}
                {p.namespaces && (
                  <span className="rk-mono" style={{ fontSize: 11.5, color: 'var(--fg3)' }}>
                    {p.namespaces}
                  </span>
                )}
              </span>
            )
          })}
        </span>
      ),
    export: (u) => [{ head: 'Roles', cell: rolesText(u) }],
  },
]
