import type { BindingRow, RoleRow } from '../api/types'
import { fmtAge, fmtTs, minutesSince } from '../lib/format'
import { href } from '../lib/router'
import { listText, powersTone, purpose, SEV_ORDER, SEV_TEXT, SEV_TONE, SOURCE_SHORT, sourceText, type Grant, type ReviewItem, type WhoRow } from '../lib/rbac'
import type { ColumnDef, Term } from '../lib/table'
import { Mono } from './cells'

/** A severity as a coloured dot and word. */
export function Sev({ f, size = 11.5 }: { f: ReviewItem | { severity: ReviewItem['severity']; title: string } | undefined; size?: number }) {
  if (!f) return null
  return (
    <span className="rk-t" data-tone={SEV_TONE[f.severity]} title={f.title} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: size, fontWeight: 500, whiteSpace: 'nowrap', flex: 'none' }} data-testid="sev">
      <span className="rk-dot" />
      {SEV_TEXT[f.severity]}
    </span>
  )
}

/** Where a binding applies: cluster-wide, or its namespace. */
export function ScopeTag({ ns }: { ns?: string }) {
  return ns ? (
    <span className="rk-tag" style={{ fontSize: 11, padding: '1px 7px' }}>
      {ns}
    </span>
  ) : (
    <span className="rk-tag" data-tone="info" style={{ fontSize: 11, padding: '1px 7px' }}>
      cluster-wide
    </span>
  )
}

export function SourceCell({ s }: { s: RoleRow['source'] }) {
  return (
    <span title={sourceText(s)} style={{ display: 'inline-flex', alignItems: 'center', gap: 6, minWidth: 0, overflow: 'hidden', fontSize: 12.5, color: 'var(--fg2)' }}>
      {s.kind === 'helm' ? (
        <>
          <span className="rk-tag" style={{ fontSize: 11, padding: '1px 6px' }}>
            Helm
          </span>
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{s.release}</span>
        </>
      ) : s.kind === 'managed' ? (
        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{s.manager}</span>
      ) : (
        SOURCE_SHORT[s.kind]
      )}
    </span>
  )
}

function sourceExport(s: RoleRow['source']): string {
  return sourceText(s)
}

/** "ns/" in grey before a ServiceAccount's name. */
export function IdentityName({ kind, name, namespace, size = 12.5 }: { kind: string; name: string; namespace?: string; size?: number }) {
  return (
    <span className="rk-mono" style={{ fontSize: size, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
      {kind === 'ServiceAccount' && <span style={{ color: 'var(--fg3)' }}>{namespace}/</span>}
      {name}
    </span>
  )
}

export function subjectsText(b: BindingRow): string {
  return b.subjects.map((s) => (s.kind === 'ServiceAccount' ? `${s.namespace}/${s.name}` : s.name)).join(', ')
}

export interface BindingView extends BindingRow {
  risk?: ReviewItem
}

const age = <R extends { created?: string }>(): ColumnDef<R> => ({
  id: 'age',
  label: 'Age',
  sortName: 'age (newest first)',
  width: '68px',
  min: 68,
  sortValue: (r, now) => (r.created ? minutesSince(r.created, now) : null),
  cell: (r, now) => <Mono text={r.created ? fmtAge(r.created, now) : '—'} tip={r.created ? fmtTs(r.created) : undefined} />,
  export: (r) => [{ head: 'Created', cell: r.created ? fmtTs(r.created) : '' }],
})

export const BINDING_COLUMNS: ColumnDef<BindingView>[] = [
  {
    id: 'name',
    label: 'Binding',
    width: 'minmax(230px, 1.6fr)',
    min: 230,
    sortValue: (r) => r.name,
    cell: (r) => <Mono text={r.name} tip={r.name} dim={false} />,
    export: (r) => [
      { head: 'Binding', cell: r.name },
      { head: 'Kind', cell: r.kind },
      { head: 'Namespace', cell: r.namespace ?? '' },
    ],
  },
  {
    id: 'scope',
    label: 'Scope',
    sortName: 'scope (cluster-wide first)',
    width: '120px',
    min: 120,
    sortValue: (r) => (r.namespace ? '1 ' + r.namespace : '0'),
    cell: (r) => <ScopeTag ns={r.namespace} />,
    export: () => [],
  },
  {
    id: 'role',
    label: 'Role',
    width: 'minmax(150px, 1fr)',
    min: 150,
    sortValue: (r) => r.roleName,
    cell: (r) => <Mono text={r.roleName} tip={`${r.roleKind} ${r.roleName}${r.roleMissing ? ' (missing)' : ''}`} />,
    export: (r) => [
      { head: 'Role', cell: r.roleName },
      { head: 'Role kind', cell: r.roleKind },
    ],
  },
  {
    id: 'subjects',
    label: 'Subjects',
    width: 'minmax(200px, 1.4fr)',
    min: 200,
    sortValue: (r) => subjectsText(r),
    cell: (r) => <Mono text={subjectsText(r) || '—'} tip={[...new Set(r.subjects.map((s) => s.kind))].join(', ') + ': ' + subjectsText(r)} dim={false} />,
    export: (r) => [{ head: 'Subjects', cell: r.subjects.map((s) => (s.kind === 'ServiceAccount' ? `${s.kind} ${s.namespace}/${s.name}` : `${s.kind} ${s.name}`)).join('\n') }],
  },
  {
    id: 'risk',
    label: 'Risk',
    sortName: 'risk (worst first)',
    width: '84px',
    min: 84,
    sortValue: (r) => (r.risk ? SEV_ORDER[r.risk.severity] : r.findings.length ? 3 : 9),
    cell: (r) => <Sev f={r.risk} />,
    export: (r) => [{ head: 'Risk', cell: r.findings[0] ? `${SEV_TEXT[r.findings[0].severity]}: ${r.findings[0].title}` : '' }],
  },
  {
    id: 'source',
    label: 'Source',
    width: '180px',
    min: 180,
    sortValue: (r) => sourceText(r.source),
    cell: (r) => <SourceCell s={r.source} />,
    export: (r) => [{ head: 'Source', cell: sourceExport(r.source) }],
  },
  age<BindingView>(),
]

export function bindingTerms(r: BindingView): Term[] {
  return [
    { kind: 'name', text: r.name },
    { kind: 'namespace', text: r.namespace ?? '' },
    { kind: 'role', text: r.roleName },
    ...r.subjects.map((s) => ({ kind: 'subject', text: s.name })),
    { kind: 'source', text: sourceText(r.source) },
  ]
}

export interface RoleView extends RoleRow {
  bindings: number
}

export const ROLE_COLUMNS: ColumnDef<RoleView>[] = [
  {
    id: 'name',
    label: 'Role',
    width: 'minmax(230px, 1.4fr)',
    min: 230,
    sortValue: (r) => r.name,
    cell: (r) => <Mono text={r.name} tip={purpose(r) || r.name} dim={false} />,
    export: (r) => [
      { head: 'Role', cell: r.name },
      { head: 'Kind', cell: r.kind },
      { head: 'Namespace', cell: r.namespace ?? '' },
    ],
  },
  {
    id: 'scope',
    label: 'Scope',
    sortName: 'scope (cluster-wide first)',
    width: '120px',
    min: 120,
    sortValue: (r) => (r.namespace ? '1 ' + r.namespace : '0'),
    cell: (r) => <ScopeTag ns={r.namespace} />,
    export: () => [],
  },
  {
    id: 'rules',
    label: 'Rules',
    width: '64px',
    min: 64,
    right: true,
    defaultDir: 'desc',
    sortValue: (r) => r.rules.length,
    cell: (r) => <span className="rk-num">{r.rules.length}</span>,
    export: (r) => [{ head: 'Rules', cell: r.rules.length }],
  },
  {
    id: 'notable',
    label: 'Notable',
    width: 'minmax(200px, 1.3fr)',
    min: 200,
    sortValue: (r) => (r.powers.includes('everything') ? -100 : -r.powers.length),
    cell: (r) => (
      <span className="rk-t" data-tone={powersTone(r.powers)} title={r.powers.join(', ')} style={{ fontSize: 12.5, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {r.powers.join(', ') || '—'}
      </span>
    ),
    export: (r) => [{ head: 'Notable', cell: r.powers.join(', ') }],
  },
  {
    id: 'bindings',
    label: 'Bindings',
    width: '82px',
    min: 82,
    right: true,
    defaultDir: 'desc',
    sortValue: (r) => r.bindings,
    cell: (r) => <span className="rk-num">{r.bindings}</span>,
    export: (r) => [
      { head: 'Bindings', cell: r.bindings },
      { head: 'Purpose', cell: purpose(r) },
    ],
  },
  {
    id: 'source',
    label: 'Source',
    width: '170px',
    min: 170,
    sortValue: (r) => sourceText(r.source),
    cell: (r) => <SourceCell s={r.source} />,
    export: (r) => [{ head: 'Source', cell: sourceExport(r.source) }],
  },
  age<RoleView>(),
]

export function roleTerms(r: RoleView): Term[] {
  return [
    { kind: 'name', text: r.name },
    { kind: 'namespace', text: r.namespace ?? '' },
    { kind: 'purpose', text: purpose(r) },
    ...r.powers.map((p) => ({ kind: 'power', text: p })),
    { kind: 'source', text: sourceText(r.source) },
  ]
}

export interface WhoView extends WhoRow {
  flag?: ReviewItem
  cluster: boolean
  ns: string
}

export function whereText(r: WhoView): string {
  if (r.wide) return r.cluster ? 'cluster-wide' : 'every namespace'
  if (r.namespaces.length) return r.ns !== '*' ? r.ns : listText([...r.namespaces].sort())
  return `only ${r.named.join(', ')}`
}

export const WHO_COLUMNS: ColumnDef<WhoView>[] = [
  {
    id: 'identity',
    label: 'Identity',
    width: 'minmax(220px, 1.3fr)',
    min: 220,
    sortValue: (r) => (r.namespace ? r.namespace + '/' : '') + r.name,
    cell: (r) => (
      <span style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
        <a className="rk-link" href={href('access', [], { id: r.key })} style={{ minWidth: 0, overflow: 'hidden', textDecoration: 'none' }} data-testid="who-identity">
          <IdentityName kind={r.kind} name={r.name} namespace={r.namespace} />
        </a>
        <Sev f={r.flag} />
      </span>
    ),
    export: (r) => [
      { head: 'Identity', cell: r.name },
      { head: 'Kind', cell: r.kind },
      { head: 'Namespace of the service account', cell: r.namespace ?? '' },
    ],
  },
  {
    id: 'kind',
    label: 'Kind',
    width: '128px',
    min: 128,
    sortValue: (r) => r.kind,
    cell: (r) => (
      <span className="rk-tag" style={{ fontSize: 11, padding: '1px 7px' }}>
        {r.kind}
      </span>
    ),
    export: () => [],
  },
  {
    id: 'where',
    label: 'Where',
    sortName: 'where (widest first)',
    width: 'minmax(140px, .9fr)',
    min: 140,
    sortValue: (r) => (r.wide ? 0 : r.namespaces.length ? 1 + r.namespaces.length / 100 : 5),
    cell: (r) => (
      <span data-wide={r.wide} style={{ fontSize: 12.5, color: r.wide ? 'var(--fg)' : 'var(--fg2)', fontWeight: r.wide ? 500 : 400, overflow: 'hidden', textOverflow: 'ellipsis' }} title={whereText(r)} data-testid="who-where">
        {whereText(r)}
      </span>
    ),
    export: (r) => [{ head: 'Where', cell: whereText(r) }],
  },
  {
    id: 'through',
    label: 'Through binding → role',
    sortName: 'binding',
    width: 'minmax(240px, 1.6fr)',
    min: 240,
    sortValue: (r) => r.through.join(', '),
    cell: (r) => (
      <span className="rk-mono" title={r.through.join('\n')} style={{ fontSize: 12, color: 'var(--fg2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {r.through.join(', ')}
      </span>
    ),
    export: (r) => [{ head: 'Through', cell: r.through.join('\n') }],
  },
]

export function whoTerms(r: WhoView): Term[] {
  return [
    { kind: 'name', text: r.name },
    { kind: 'namespace', text: r.namespace ?? '' },
  ]
}

export interface GrantView extends Grant {
  key: string
  created?: string
}

export function howText(g: Grant): string {
  return g.via ? `as a member of ${g.via}` : 'names it directly'
}

export const GRANT_COLUMNS: ColumnDef<GrantView>[] = [
  {
    id: 'name',
    label: 'Binding',
    width: 'minmax(220px, 1.4fr)',
    min: 220,
    sortValue: (r) => r.binding.name,
    cell: (r) => <Mono text={(r.binding.namespace ? r.binding.namespace + '/' : '') + r.binding.name} dim={false} />,
    export: (r) => [
      { head: 'Binding', cell: r.binding.name },
      { head: 'Kind', cell: r.binding.kind },
      { head: 'Namespace', cell: r.binding.namespace ?? '' },
    ],
  },
  {
    id: 'scope',
    label: 'Scope',
    sortName: 'scope (cluster-wide first)',
    width: '120px',
    min: 120,
    sortValue: (r) => (r.binding.namespace ? '1 ' + r.binding.namespace : '0'),
    cell: (r) => <ScopeTag ns={r.binding.namespace} />,
    export: () => [],
  },
  {
    id: 'role',
    label: 'Role',
    width: 'minmax(150px, 1fr)',
    min: 150,
    sortValue: (r) => r.binding.roleName,
    cell: (r) => <Mono text={r.binding.roleName} tip={`${r.binding.roleKind} ${r.binding.roleName}`} />,
    export: (r) => [{ head: 'Role', cell: r.binding.roleName }],
  },
  {
    id: 'how',
    label: 'How it applies',
    width: 'minmax(170px, 1fr)',
    min: 170,
    sortValue: (r) => (r.via ? '1 ' + r.via : '0'),
    cell: (r) => <span style={{ fontSize: 12.5, color: r.via ? 'var(--fg2)' : 'var(--fg)' }}>{howText(r)}</span>,
    export: (r) => [{ head: 'How it applies', cell: howText(r) }],
  },
  {
    id: 'source',
    label: 'Source',
    width: '160px',
    min: 160,
    sortValue: (r) => sourceText(r.binding.source),
    cell: (r) => <SourceCell s={r.binding.source} />,
    export: (r) => [{ head: 'Source', cell: sourceExport(r.binding.source) }],
  },
  age<GrantView>(),
]

export function grantTerms(r: GrantView): Term[] {
  return [
    { kind: 'name', text: r.binding.name },
    { kind: 'role', text: r.binding.roleName },
  ]
}
