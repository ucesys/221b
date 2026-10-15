import type { ReleaseRow } from '../api/types'
import { fmtAge, fmtTs, minutesSince } from '../lib/format'
import { toneRank } from '../lib/nodes'
import type { ColumnDef } from '../lib/table'
import { Mono, Tone } from './cells'

/** A namespace's Helm releases, as its other kinds are listed: Name, Status, Chart, App version, Revision, Pods, Updated. */
export const RELEASE_COLUMNS: ColumnDef<ReleaseRow>[] = [
  {
    id: 'name',
    label: 'Name',
    width: 'minmax(200px, 1.4fr)',
    min: 200,
    sortValue: (r) => r.name,
    cell: (r) => (
      <span className="rk-mono rk-pname" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
        {r.name}
      </span>
    ),
    export: (r) => [{ head: 'Name', cell: r.name }],
  },
  {
    id: 'status',
    label: 'Status',
    sortName: 'status (problems first)',
    width: 'minmax(110px, .7fr)',
    min: 110,
    defaultDir: 'desc',
    sortValue: (r) => toneRank[r.tone] * 1000 + r.status.length,
    cell: (r) => <Tone tone={r.tone} text={r.status} tip={r.description} />,
    export: (r) => [{ head: 'Status', cell: r.status }],
  },
  {
    id: 'chart',
    label: 'Chart',
    width: 'minmax(180px, 1.2fr)',
    min: 180,
    sortValue: (r) => `${r.chart}-${r.chartVersion}`,
    cell: (r) => <Mono text={`${r.chart}-${r.chartVersion}`} size={12} />,
    export: (r) => [{ head: 'Chart', cell: `${r.chart}-${r.chartVersion}` }],
  },
  {
    id: 'app',
    label: 'App version',
    width: '110px',
    min: 110,
    sortValue: (r) => r.appVersion,
    cell: (r) => <Mono text={r.appVersion || '—'} size={12} />,
    export: (r) => [{ head: 'App version', cell: r.appVersion }],
  },
  {
    id: 'revision',
    label: 'Revision',
    width: '80px',
    min: 80,
    right: true,
    defaultDir: 'desc',
    sortValue: (r) => r.revision,
    cell: (r) => <span className="rk-num">{r.revision}</span>,
    export: (r) => [{ head: 'Revision', cell: r.revision }],
  },
  {
    id: 'pods',
    label: 'Pods',
    sortName: 'pods (not ready first)',
    width: '100px',
    min: 100,
    sortValue: (r) => (r.pods ? r.podsReady / r.pods : 2),
    cell: (r) => (r.pods ? <Tone tone={r.podsFailed ? 'err' : r.podsReady < r.pods ? 'warn' : 'ok'} text={`${r.podsReady}/${r.pods} ready`} /> : <Mono text="—" />),
    export: (r) => [{ head: 'Pods ready', cell: r.pods ? `${r.podsReady}/${r.pods}` : '' }],
  },
  {
    id: 'updated',
    label: 'Updated',
    sortName: 'updated (newest first)',
    width: '80px',
    min: 80,
    defaultDir: 'desc',
    sortValue: (r, now) => (r.updated ? minutesSince(r.updated, now) : null),
    cell: (r, now) => <Mono text={r.updated ? fmtAge(r.updated, now) : '—'} tip={r.updated ? `Updated ${fmtTs(r.updated)}` : undefined} />,
    export: (r) => [{ head: 'Updated', cell: r.updated ? fmtTs(r.updated) : '' }],
  },
]
