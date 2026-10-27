import type { AppView } from '../api/apps'
import { sourceText } from '../api/apps'
import type { ReleaseRow } from '../api/types'
import { fmtAge, fmtTs, minutesSince } from '../lib/format'
import { toneRank } from '../lib/nodes'
import type { ColumnDef, Term } from '../lib/table'
import { NameWithAppTools } from '../components/apps/AppMenu'
import { Mono, NameCell, Tone } from './cells'

/** A release on the Helm1 page: the release, and what the console keeps about it (HA-01). */
export interface AppRow extends ReleaseRow {
  app?: AppView
}

export function driftText(r: AppRow): string {
  if (!r.app) return '—'
  return r.app.driftState ?? 'not checked'
}

export function driftTone(r: AppRow): 'ok' | 'warn' | 'err' | 'muted' {
  switch (r.app?.driftState) {
    case 'in sync':
      return 'ok'
    case 'drifted':
      return 'err'
    case 'unknown':
      return 'warn'
  }
  return 'muted'
}

/** Name, Namespace, Chart, App version, Revision, Status, Drift, Source, Updated (HA-01). */
export const APP_COLUMNS: ColumnDef<AppRow>[] = [
  {
    id: 'name',
    label: 'Name',
    width: 'minmax(180px, 1.2fr)',
    compactWidth: 'minmax(160px, 1fr)',
    min: 160,
    sortValue: (r) => r.name,
    cell: (r) => (
      <NameWithAppTools row={r}>
        <NameCell name={r.name} what="release name" />
      </NameWithAppTools>
    ),
    export: (r) => [{ head: 'Name', cell: r.name }],
  },
  {
    id: 'namespace',
    label: 'Namespace',
    width: 'minmax(120px, .8fr)',
    min: 120,
    sortValue: (r) => r.namespace,
    cell: (r) => <Mono text={r.namespace} size={12} />,
    export: (r) => [{ head: 'Namespace', cell: r.namespace }],
  },
  {
    id: 'chart',
    label: 'Chart',
    width: 'minmax(170px, 1fr)',
    min: 170,
    sortValue: (r) => `${r.chart}-${r.chartVersion}`,
    cell: (r) => <Mono text={`${r.chart}-${r.chartVersion}`} size={12} />,
    export: (r) => [{ head: 'Chart', cell: `${r.chart}-${r.chartVersion}` }],
  },
  {
    id: 'app',
    label: 'App version',
    width: '100px',
    min: 100,
    sortValue: (r) => r.appVersion,
    cell: (r) => <Mono text={r.appVersion || '—'} size={12} />,
    export: (r) => [{ head: 'App version', cell: r.appVersion }],
  },
  {
    id: 'revision',
    label: 'Revision',
    width: '76px',
    min: 76,
    right: true,
    defaultDir: 'desc',
    sortValue: (r) => r.revision,
    cell: (r) => <span className="rk-num">{r.revision}</span>,
    export: (r) => [{ head: 'Revision', cell: r.revision }],
  },
  {
    id: 'status',
    label: 'Status',
    sortName: 'status (problems first)',
    width: 'minmax(104px, .7fr)',
    min: 104,
    defaultDir: 'desc',
    sortValue: (r) => toneRank[r.tone] * 1000 + r.status.length,
    cell: (r) => <Tone tone={r.tone} text={r.status} tip={r.description} />,
    export: (r) => [{ head: 'Status', cell: r.status }],
  },
  {
    id: 'drift',
    label: 'Drift',
    hint: 'Whether the cluster still matches what Helm applied; checked for releases with a source',
    width: 'minmax(96px, .6fr)',
    min: 96,
    sortValue: (r) => ({ err: 0, warn: 1, muted: 2, ok: 3 })[driftTone(r)],
    cell: (r) => (r.app ? <Tone tone={driftTone(r)} text={driftText(r)} tip={r.app.drift ? `${r.app.drift.changed} changed · ${r.app.drift.missing} missing · ${r.app.drift.same} as applied` : 'Open the release to check'} /> : <span className="rk-dim">—</span>),
    export: (r) => [{ head: 'Drift', cell: driftText(r) }],
  },
  {
    id: 'source',
    label: 'Source',
    hint: 'The repository, chart path and branch or tag the console deploys it from',
    width: 'minmax(220px, 1.6fr)',
    min: 200,
    sortValue: (r) => (r.app ? sourceText(r.app.source) : ''),
    cell: (r) => (r.app ? <Mono text={sourceText(r.app.source)} tip={r.app.commit ? `commit ${r.app.commit.slice(0, 12)}${r.app.commitSubject ? ': ' + r.app.commitSubject : ''}` : undefined} size={12} /> : <span className="rk-dim">—</span>),
    export: (r) => [{ head: 'Source', cell: r.app ? sourceText(r.app.source) : '' }],
  },
  {
    id: 'updated',
    label: 'Updated',
    width: '110px',
    min: 110,
    defaultDir: 'desc',
    sortValue: (r) => (r.updated ? Date.parse(r.updated) : 0),
    cell: (r, now) => <span title={r.updated ? fmtTs(r.updated) : ''}>{r.updated ? fmtAge(r.updated, now) + ' ago' : '—'}</span>,
    export: (r, now) => [{ head: 'Updated (min ago)', cell: r.updated ? minutesSince(r.updated, now) : '' }],
  },
]

export function appTerms(r: AppRow): Term[] {
  return [
    { kind: 'namespace', text: r.namespace },
    { kind: 'chart', text: `${r.chart}-${r.chartVersion}` },
    { kind: 'status', text: r.status },
    { kind: 'drift', text: driftText(r) },
    ...(r.app ? [{ kind: 'source', text: sourceText(r.app.source) }, { kind: 'ref', text: r.app.source.ref }] : []),
  ]
}
export const appLabel = (r: AppRow) => `${r.namespace}/${r.name}`
