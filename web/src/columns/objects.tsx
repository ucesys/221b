import type { ObjectRow, ReleaseResource, Revision } from '../api/types'
import { fmtAge, fmtTs, minutesSince } from '../lib/format'
import { toneRank } from '../lib/nodes'
import type { ColumnDef, Term } from '../lib/table'
import { Mono, NameCell, Tone } from './cells'
import { RevisionDiffButton, RevisionRollbackButton } from '../components/apps/revisionActions'

/** The columns of a kind's table in the Namespaces tab: Name, Status, Details, Age. */
export const OBJECT_COLUMNS: ColumnDef<ObjectRow>[] = [
  {
    id: 'name',
    label: 'Name',
    width: 'minmax(200px, 1.4fr)',
    min: 200,
    sortValue: (r) => r.name,
    cell: (r) => <NameCell name={r.name} what={`${r.kind} name`} />,
    export: (r) => [
      { head: 'Kind', cell: r.kind },
      { head: 'Name', cell: r.name },
    ],
  },
  {
    id: 'status',
    label: 'Status',
    sortName: 'status (problems first)',
    width: 'minmax(130px, .8fr)',
    min: 130,
    defaultDir: 'desc',
    sortValue: (r) => toneRank[r.tone] * 1000 + r.status.length,
    cell: (r) => <Tone tone={r.tone} text={r.status} />,
    export: (r) => [{ head: 'Status', cell: r.status }],
  },
  {
    id: 'details',
    label: 'Details',
    width: 'minmax(220px, 2fr)',
    min: 220,
    sortValue: (r) => r.details,
    cell: (r) => <Mono text={r.details} size={12} tip={r.details} />,
    export: (r) => [{ head: 'Details', cell: r.details }],
  },
  {
    id: 'age',
    label: 'Age',
    width: '72px',
    min: 72,
    defaultDir: 'desc',
    sortValue: (r, now) => minutesSince(r.created, now),
    cell: (r, now) => <Mono text={fmtAge(r.created, now)} tip={`Created ${fmtTs(r.created)}`} />,
    export: (r) => [{ head: 'Created', cell: fmtTs(r.created) }],
  },
]

export function objectTerms(r: ObjectRow): Term[] {
  return [
    { kind: 'name', text: r.name },
    { kind: 'kind', text: r.kind },
    { kind: 'status', text: r.status },
    { kind: 'detail', text: r.details },
  ]
}

export function objectLabel(r: ObjectRow, open: boolean): string {
  return `${r.kind} ${r.name}, ${r.status}${open ? ' — YAML open, press to close' : ' — show YAML'}`
}

/** A Helm release's resources, as the sub-tabs list them. */
export const RESOURCE_COLUMNS: ColumnDef<ReleaseResource>[] = [
  {
    id: 'kind',
    label: 'Kind',
    width: '150px',
    min: 150,
    sortValue: (r) => r.kind,
    cell: (r) => <Mono text={r.kind} dim={false} />,
    export: (r) => [{ head: 'Kind', cell: r.kind }],
  },
  {
    id: 'name',
    label: 'Name',
    width: 'minmax(200px, 1.4fr)',
    min: 200,
    sortValue: (r) => r.name,
    cell: (r) => <NameCell name={r.name} what={`${r.kind} name`} />,
    export: (r) => [{ head: 'Name', cell: r.name }],
  },
  {
    id: 'namespace',
    label: 'Namespace',
    width: '120px',
    min: 120,
    sortValue: (r) => r.namespace || null,
    cell: (r) => <Mono text={r.namespace} />,
    export: (r) => [{ head: 'Namespace', cell: r.namespace || null }],
  },
  {
    id: 'status',
    label: 'Status',
    width: 'minmax(130px, .8fr)',
    min: 130,
    defaultDir: 'desc',
    sortValue: (r) => (r.watched ? toneRank[r.tone ?? 'muted'] * 1000 : null),
    cell: (r) => (r.watched ? <Tone tone={r.tone ?? 'muted'} text={r.status ?? ''} /> : <Mono text="not watched" tip="221B does not watch this kind, so its state is not known" />),
    export: (r) => [{ head: 'Status', cell: r.watched ? (r.status ?? '') : 'not watched' }],
  },
  {
    id: 'details',
    label: 'Details',
    width: 'minmax(220px, 2fr)',
    min: 220,
    sortValue: (r) => r.details ?? null,
    cell: (r) => <Mono text={r.details ?? ''} size={12} tip={r.details} />,
    export: (r) => [{ head: 'Details', cell: r.details ?? null }],
  },
]

export function resourceLabel(r: ReleaseResource, open: boolean): string {
  return `${r.kind} ${r.name}${r.status ? ', ' + r.status : ''}${open ? ' — YAML open, press to close' : ' — show YAML'}`
}

export function resourceTerms(r: ReleaseResource): Term[] {
  return [
    { kind: 'name', text: r.name },
    { kind: 'kind', text: r.kind },
    { kind: 'status', text: r.status ?? '' },
  ]
}

/** A Helm release's history (P2-06). */
export const REVISION_COLUMNS: ColumnDef<Revision>[] = [
  {
    id: 'revision',
    label: 'Revision',
    width: '90px',
    min: 90,
    right: true,
    defaultDir: 'desc',
    sortValue: (r) => r.revision,
    cell: (r) => <span className="rk-num">{r.revision}</span>,
    export: (r) => [{ head: 'Revision', cell: r.revision }],
  },
  {
    id: 'diff',
    label: '',
    sortName: 'revision',
    width: '40px',
    min: 40,
    sortValue: (r) => r.revision,
    cell: (r) => <RevisionDiffButton revision={r.revision} />,
    export: () => [],
  },
  {
    id: 'rollback',
    label: '',
    sortName: 'revision',
    width: '40px',
    min: 40,
    sortValue: (r) => r.revision,
    cell: (r) => <RevisionRollbackButton revision={r.revision} />,
    export: () => [],
  },
  {
    id: 'updated',
    label: 'Updated',
    width: '180px',
    min: 180,
    defaultDir: 'desc',
    sortValue: (r, now) => (r.updated ? -minutesSince(r.updated, now) : null),
    cell: (r, now) => <Mono text={r.updated ? `${fmtTs(r.updated)} · ${fmtAge(r.updated, now)} ago` : '—'} size={12} />,
    export: (r) => [{ head: 'Updated', cell: r.updated ? fmtTs(r.updated) : null }],
  },
  {
    id: 'status',
    label: 'Status',
    width: '120px',
    min: 120,
    sortValue: (r) => r.status,
    cell: (r) => <Tone tone={r.tone} text={r.status} />,
    export: (r) => [{ head: 'Status', cell: r.status }],
  },
  {
    id: 'chart',
    label: 'Chart',
    width: '180px',
    min: 180,
    sortValue: (r) => `${r.chart}-${r.chartVersion}`,
    cell: (r) => <Mono text={`${r.chart}-${r.chartVersion}`} size={12} />,
    export: (r) => [
      { head: 'Chart', cell: r.chart },
      { head: 'Chart version', cell: r.chartVersion },
    ],
  },
  {
    id: 'app',
    label: 'App version',
    width: '100px',
    min: 100,
    sortValue: (r) => r.appVersion,
    cell: (r) => <Mono text={r.appVersion} size={12} />,
    export: (r) => [{ head: 'App version', cell: r.appVersion }],
  },
  {
    id: 'description',
    label: 'Description',
    width: 'minmax(220px, 2fr)',
    min: 220,
    sortValue: (r) => r.description,
    cell: (r) => <span title={r.description}>{r.description}</span>,
    export: (r) => [{ head: 'Description', cell: r.description }],
  },
]

export function revisionTerms(r: Revision): Term[] {
  return [
    { kind: 'status', text: r.status },
    { kind: 'chart', text: `${r.chart}-${r.chartVersion}` },
    { kind: 'detail', text: r.description },
  ]
}
