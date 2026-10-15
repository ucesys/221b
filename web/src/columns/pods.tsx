import type { PodView } from '../api/types'
import { fCores, fMem, fmtAge, fmtTs, minutesSince, round } from '../lib/format'
import { toneRank } from '../lib/nodes'
import { href } from '../lib/router'
import type { ColumnDef, Term } from '../lib/table'
import { CellLink, Mono, peekCell, Tone, UsageBar } from './cells'

const GIB = 1024 * 1024 * 1024

function cores(milli: number): string {
  return milli ? (milli < 1000 ? milli + 'm' : fCores(milli)) : '—'
}

/** The Pods table's columns. Usage comes with the row as PodView. */
export const POD_COLUMNS: ColumnDef<PodView>[] = [
  {
    id: 'name',
    label: 'Pod',
    sortName: 'pod name',
    width: 'minmax(170px, 1.5fr)',
    compactWidth: 'minmax(160px, 1.4fr)',
    min: 170,
    compactMin: 160,
    sortValue: (r) => r.name,
    cell: (r) => (
      <span className="rk-mono rk-pname" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
        {r.name}
      </span>
    ),
    export: (r) => [{ head: 'Pod', cell: r.name }],
  },
  {
    id: 'namespace',
    label: 'Namespace',
    width: 'minmax(100px, .7fr)',
    compactWidth: '100px',
    min: 100,
    sortValue: (r) => r.namespace,
    cell: (r) => <CellLink href={href('namespaces', [r.namespace])} text={r.namespace} tip={`Open namespace ${r.namespace}`} />,
    export: (r) => [{ head: 'Namespace', cell: r.namespace }],
  },
  {
    id: 'status',
    label: 'Status',
    sortName: 'status (problems first)',
    width: 'minmax(150px, 1fr)',
    compactWidth: '140px',
    min: 150,
    compactMin: 140,
    defaultDir: 'desc',
    sortValue: (r) => toneRank[r.tone] * 1000 + (r.done ? 0 : 1),
    cell: (r) => (
      <span style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0, maxWidth: '100%' }}>
        <span className="rk-pill" data-tone={r.tone} title={r.statusTip || r.status} style={{ flex: '0 1 auto', minWidth: 0 }}>
          <span className="rk-dot" />
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{r.status}</span>
        </span>
        {!r.done && (
          <span className="rk-num" style={{ color: r.ready < r.total ? 'var(--warn)' : 'var(--fg3)', fontSize: 12 }} title={`${r.ready} of ${r.total} containers ready`}>
            {r.ready}/{r.total}
          </span>
        )}
      </span>
    ),
    export: (r) => [
      { head: 'Status', cell: r.status },
      { head: 'Ready', cell: `${r.ready}/${r.total}` },
    ],
  },
  {
    id: 'restarts',
    label: 'Restarts',
    width: '104px',
    compactWidth: '90px',
    min: 104,
    compactMin: 90,
    right: true,
    defaultDir: 'desc',
    sortValue: (r) => r.restarts,
    cell: (r, now) => (
      <span className="rk-num" title={r.lastRestart ? `Last restart ${fmtTs(r.lastRestart)}${r.lastTermination ? ': ' + r.lastTermination : ''}` : 'No restarts'}>
        <span style={{ color: r.restarts ? (r.restarts >= 5 ? 'var(--err)' : 'var(--warn)') : undefined, fontWeight: r.restarts ? 500 : undefined }}>{r.restarts}</span>
        {r.lastRestart && r.restarts > 0 && (
          <span style={{ color: 'var(--fg3)', fontSize: 11.5, marginLeft: 6 }}>
            {fmtAge(r.lastRestart, now)} ago
          </span>
        )}
      </span>
    ),
    export: (r) => [
      { head: 'Restarts', cell: r.restarts },
      { head: 'Last restart', cell: r.lastRestart ? fmtTs(r.lastRestart) : null },
      { head: 'Last termination', cell: r.lastTermination ?? null },
    ],
  },
  {
    id: 'cpuReq',
    label: 'CPU req',
    sortName: 'CPU requested',
    width: '70px',
    min: 70,
    right: true,
    defaultDir: 'desc',
    sortValue: (r) => r.cpuRequest || null,
    cell: (r) => <Mono text={cores(r.cpuRequest)} tip={r.cpuLimit ? `Requests ${cores(r.cpuRequest)}, limit ${cores(r.cpuLimit)}` : `Requests ${cores(r.cpuRequest)}, no limit`} />,
    export: (r) => [
      { head: 'CPU requested (cores)', cell: round(r.cpuRequest / 1000, 3) },
      { head: 'CPU limit (cores)', cell: r.cpuLimit ? round(r.cpuLimit / 1000, 3) : null },
    ],
  },
  {
    id: 'cpuUse',
    label: 'CPU used',
    sortName: 'CPU in use',
    width: '108px',
    min: 108,
    defaultDir: 'desc',
    sortValue: (r) => r.usage?.cpuMilli ?? null,
    cell: (r) => (
      <UsageBar
        used={r.usage?.cpuMilli ?? null}
        request={r.cpuRequest}
        limit={r.cpuLimit}
        text={r.usage ? cores(r.usage.cpuMilli) : '—'}
        tip={r.usage ? `${cores(r.usage.cpuMilli)} in use of ${cores(r.cpuRequest)} requested${r.cpuLimit ? `, limit ${cores(r.cpuLimit)}` : ''}` : 'No reading from metrics-server'}
      />
    ),
    export: (r) => [{ head: 'CPU in use (cores)', cell: r.usage ? round(r.usage.cpuMilli / 1000, 3) : null }],
  },
  {
    id: 'memReq',
    label: 'Mem req',
    sortName: 'memory requested',
    width: '76px',
    min: 76,
    right: true,
    defaultDir: 'desc',
    sortValue: (r) => r.memoryRequest || null,
    cell: (r) => <Mono text={r.memoryRequest ? fMem(r.memoryRequest) : '—'} tip={r.memoryLimit ? `Requests ${fMem(r.memoryRequest)}, limit ${fMem(r.memoryLimit)}` : `Requests ${fMem(r.memoryRequest)}, no limit`} />,
    export: (r) => [
      { head: 'Memory requested (GiB)', cell: round(r.memoryRequest / GIB, 3) },
      { head: 'Memory limit (GiB)', cell: r.memoryLimit ? round(r.memoryLimit / GIB, 3) : null },
    ],
  },
  {
    id: 'memUse',
    label: 'Mem used',
    sortName: 'memory in use',
    width: '112px',
    min: 112,
    defaultDir: 'desc',
    sortValue: (r) => r.usage?.memoryBytes ?? null,
    cell: (r) => (
      <UsageBar
        used={r.usage?.memoryBytes ?? null}
        request={r.memoryRequest}
        limit={r.memoryLimit}
        text={r.usage ? fMem(r.usage.memoryBytes) : '—'}
        tip={r.usage ? `${fMem(r.usage.memoryBytes)} in use of ${fMem(r.memoryRequest)} requested${r.memoryLimit ? `, limit ${fMem(r.memoryLimit)}` : ''}` : 'No reading from metrics-server'}
      />
    ),
    export: (r) => [{ head: 'Memory in use (GiB)', cell: r.usage ? round(r.usage.memoryBytes / GIB, 3) : null }],
  },
  {
    id: 'started',
    label: 'Started',
    sortName: 'time since the pod started',
    width: '68px',
    min: 68,
    defaultDir: 'desc',
    sortValue: (r, now) => (r.started ? minutesSince(r.started, now) : null),
    cell: (r, now) => <Mono text={r.started ? fmtAge(r.started, now) : '—'} tip={r.started ? `Started ${fmtTs(r.started)}` : 'Not started'} />,
    export: (r) => [{ head: 'Started', cell: r.started ? fmtTs(r.started) : null }],
  },
  {
    id: 'node',
    label: 'Node',
    width: 'minmax(110px, .8fr)',
    compactWidth: '110px',
    min: 110,
    sortValue: (r) => r.node || null,
    cell: (r) => (r.node ? <CellLink {...peekCell({ kind: 'node', name: r.node })} text={r.node} tip={`Node ${r.node}: open its details`} /> : <Tone tone="warn" text="not scheduled" />),
    export: (r) => [{ head: 'Node', cell: r.node || null }],
  },
  {
    id: 'owner',
    label: 'Owner',
    sortName: 'owning workload',
    width: 'minmax(150px, 1fr)',
    min: 150,
    wideOnly: true,
    hiddenByDefault: true,
    sortValue: (r) => (r.ownerKind ? `${r.ownerKind}/${r.ownerName}` : null),
    cell: (r) => <Mono text={r.ownerKind ? `${r.ownerKind}/${r.ownerName}` : ''} size={12} />,
    export: (r) => [{ head: 'Owner', cell: r.ownerKind ? `${r.ownerKind}/${r.ownerName}` : null }],
  },
  {
    id: 'release',
    label: 'Helm release',
    width: '130px',
    min: 130,
    wideOnly: true,
    hiddenByDefault: true,
    sortValue: (r) => r.release || null,
    cell: (r) => (r.release ? <CellLink href={href('helm', [r.namespace, r.release])} text={r.release} tip={`Open release ${r.release}`} /> : <Mono text="" />),
    export: (r) => [{ head: 'Helm release', cell: r.release ?? null }],
  },
  {
    id: 'qos',
    label: 'QoS',
    sortName: 'quality of service class',
    width: '96px',
    min: 96,
    wideOnly: true,
    hiddenByDefault: true,
    sortValue: (r) => r.qos || null,
    cell: (r) => <Mono text={r.qos ?? ''} size={12} />,
    export: (r) => [{ head: 'QoS', cell: r.qos ?? null }],
  },
  {
    id: 'labels',
    label: 'Labels',
    width: 'minmax(180px, 1.2fr)',
    min: 180,
    wideOnly: true,
    hiddenByDefault: true,
    sortValue: (r) => labelList(r).join(','),
    cell: (r) => <Mono text={labelList(r).join(', ')} size={11.5} tip={labelList(r).join('\n')} />,
    export: (r) => [{ head: 'Labels', cell: labelList(r).join('\n'), json: labelList(r) }],
  },
  {
    id: 'age',
    label: 'Age',
    width: '64px',
    min: 64,
    wideOnly: true,
    hiddenByDefault: true,
    defaultDir: 'desc',
    sortValue: (r, now) => minutesSince(r.created, now),
    cell: (r, now) => <Mono text={fmtAge(r.created, now)} tip={`Created ${fmtTs(r.created)}`} />,
    export: (r) => [{ head: 'Created', cell: fmtTs(r.created) }],
  },
]

export function labelList(r: PodView): string[] {
  return Object.keys(r.labels)
    .sort()
    .map((k) => (r.labels[k] ? `${k}=${r.labels[k]}` : k))
}

/** What the search box matches and suggests for a pod. */
export function podTerms(r: PodView): Term[] {
  const out: Term[] = [
    { kind: 'name', text: r.name },
    { kind: 'namespace', text: r.namespace },
    { kind: 'status', text: r.status },
  ]
  if (r.node) out.push({ kind: 'node', text: r.node })
  if (r.ownerName) out.push({ kind: 'owner', text: `${r.ownerKind}/${r.ownerName}` })
  if (r.release) out.push({ kind: 'release', text: r.release })
  for (const l of labelList(r)) out.push({ kind: 'label', text: l })
  return out
}

export function podLabel(r: PodView, open: boolean): string {
  return `${r.name} in ${r.namespace}, ${r.status}${r.restarts ? `, ${r.restarts} restarts` : ''}${r.node ? `, on ${r.node}` : ', not scheduled'}${open ? ' — details open, press to close' : ' — show details'}`
}

/** The status segments of the Pods tab: All, Running, Pending, Failing, Completed. */
export type PodStatusFilter = 'all' | 'running' | 'pending' | 'failing' | 'completed'

export function podStatusGroup(r: PodView): Exclude<PodStatusFilter, 'all'> {
  if (r.done) return 'completed'
  if (r.tone === 'err') return 'failing'
  if (r.phase === 'Pending' || r.status === 'ContainerCreating' || r.status === 'PodInitializing' || r.status.startsWith('Init:')) return 'pending'
  return 'running'
}
