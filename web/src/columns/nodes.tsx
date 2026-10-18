import type { NodeRow } from '../api/types'
import { CopyName } from '../components/CopyName'
import { clampPct, fCores, fMem, fmtAge, fmtTs, minutesSince, pct, round } from '../lib/format'
import { cpuMeter, free, memoryMeter, ownLabels, problemsCell, roleTag, taintId, taintShort, toneRank, type Meter } from '../lib/nodes'
import { href } from '../lib/router'
import type { ColumnDef, Term } from '../lib/table'
import { Tone } from './cells'

const GIB = 1024 * 1024 * 1024

/** How to read the bars, on hover (asked at the Phase 6 review, in place of a legend under the table). */
export const BAR_LEGEND = 'Light bar: requested. Solid bar: in use. The percentage is what is requested of what is allocatable.'
const FREE_LEGEND = 'Free = allocatable − requested.'

function MeterCell({ m }: { m: Meter }) {
  return (
    <span className="rk-meter" title={`${m.tip}\n\n${BAR_LEGEND}`}>
      <span className="rk-bar" style={{ width: 56 }}>
        <span className="rk-req" style={{ width: `${clampPct(m.reqPct).toFixed(1)}%` }} />
        <span className="rk-use" style={{ width: `${clampPct(m.usePct).toFixed(1)}%` }} />
      </span>
      <span className="rk-num" style={{ minWidth: 30, textAlign: 'right' }}>
        {m.text}
      </span>
      <span className="rk-num" style={{ color: 'var(--fg3)' }}>
        {m.sub}
      </span>
    </span>
  )
}

/** A free amount, in warning colour when little is left. */
function FreeCell({ amount, share, title }: { amount: string; share: number; title: string }) {
  return (
    <span className="rk-num" title={`${title}\n\n${FREE_LEGEND}`} style={{ color: share < 10 ? 'var(--warn)' : undefined }}>
      {amount}
    </span>
  )
}

/** The Nodes table's columns, in the prototype's order. */
export const NODE_COLUMNS: ColumnDef<NodeRow>[] = [
  {
    id: 'name',
    label: 'Node',
    sortName: 'node name',
    width: 'minmax(180px, 1.4fr)',
    compactWidth: 'minmax(160px, 1.3fr)',
    min: 180,
    compactMin: 160,
    sortValue: (r) => r.name,
    // An experiment: a copy icon beside the name while it is hovered.
    cell: (r) => (
      <span className="rk-namecopy">
        <span className="rk-mono rk-pname" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
          {r.name}
        </span>
        <CopyName text={r.name} what="node name" />
      </span>
    ),
    export: (r) => [{ head: 'Node', cell: r.name }],
  },
  {
    id: 'role',
    label: 'Role',
    sortName: 'role (control plane first)',
    width: '80px',
    compactWidth: '76px',
    min: 80,
    compactMin: 76,
    sortValue: (r) => (r.role === 'control-plane' ? 0 : 1),
    cell: (r) => {
      const t = roleTag(r)
      return (
        <span className="rk-role" data-role={r.role} title={t.title}>
          {t.text}
        </span>
      )
    },
    export: (r) => [{ head: 'Role', cell: r.role === 'control-plane' ? 'Control plane' : 'Worker' }],
  },
  {
    id: 'status',
    label: 'Status',
    sortName: 'status (problems first)',
    width: '100px',
    compactWidth: '96px',
    min: 100,
    compactMin: 96,
    defaultDir: 'desc',
    sortValue: (r) => toneRank[r.tone] ?? 0,
    cell: (r) => <Tone tone={r.tone} text={r.status} tip={r.statusTip} />,
    export: (r) => [{ head: 'Status', cell: r.status }],
  },
  {
    id: 'problems',
    label: 'Problems',
    sortName: 'problems (errors first)',
    width: '106px',
    compactWidth: '100px',
    min: 106,
    compactMin: 100,
    defaultDir: 'desc',
    sortValue: (r) => r.errors * 10 + r.warnings,
    cell: (r) => {
      const p = problemsCell(r)
      return <Tone tone={p.tone} text={p.text} tip={p.tip} />
    },
    export: (r) => [{ head: 'Problems', cell: r.problems.map((p) => p.title).join('\n'), json: r.problems.map((p) => p.title) }],
  },
  {
    id: 'cpu',
    label: 'CPU requested',
    sortName: 'CPU requested (% of allocatable)',
    hint: BAR_LEGEND,
    width: '164px',
    compactWidth: '154px',
    min: 164,
    compactMin: 154,
    defaultDir: 'desc',
    sortValue: (r) => pct(r.cpu.requested, r.cpu.allocatable),
    cell: (r) => <MeterCell m={cpuMeter(r)} />,
    export: (r) => [
      { head: 'CPU requested (cores)', cell: round(r.cpu.requested / 1000, 3) },
      { head: 'CPU requested %', cell: round(pct(r.cpu.requested, r.cpu.allocatable), 1) },
      { head: 'CPU in use (cores)', cell: r.cpu.used == null ? null : round(r.cpu.used / 1000, 3) },
    ],
  },
  {
    id: 'cpuAlloc',
    label: 'CPU allocatable',
    sortName: 'CPU allocatable to pods',
    width: '104px',
    compactWidth: '96px',
    min: 104,
    compactMin: 96,
    right: true,
    wideOnly: true,
    defaultDir: 'desc',
    sortValue: (r) => r.cpu.allocatable,
    cell: (r) => (
      <span className="rk-num" style={{ color: 'var(--fg2)' }} title={`${fCores(r.cpu.allocatable)} cores allocatable to pods, of ${fCores(r.cpu.capacity)} on the machine`}>
        {fCores(r.cpu.allocatable)}
      </span>
    ),
    export: (r) => [{ head: 'CPU allocatable (cores)', cell: round(r.cpu.allocatable / 1000, 3) }],
  },
  {
    id: 'cpuFree',
    label: 'CPU free',
    sortName: 'CPU free to schedule',
    hint: FREE_LEGEND,
    width: '82px',
    compactWidth: '76px',
    min: 82,
    compactMin: 76,
    right: true,
    wideOnly: true,
    defaultDir: 'desc',
    sortValue: (r) => free(r.cpu),
    cell: (r) => <FreeCell amount={fCores(free(r.cpu))} share={100 - (r.cpu.allocatable ? (r.cpu.requested / r.cpu.allocatable) * 100 : 100)} title={`${fCores(free(r.cpu))} of ${fCores(r.cpu.allocatable)} cores free to schedule`} />,
    export: (r) => [{ head: 'CPU free (cores)', cell: round(free(r.cpu) / 1000, 3) }],
  },
  {
    id: 'memory',
    label: 'Memory requested',
    sortName: 'memory requested (% of allocatable)',
    hint: BAR_LEGEND,
    width: '174px',
    compactWidth: '164px',
    min: 174,
    compactMin: 164,
    defaultDir: 'desc',
    sortValue: (r) => pct(r.memory.requested, r.memory.allocatable),
    cell: (r) => <MeterCell m={memoryMeter(r)} />,
    export: (r) => [
      { head: 'Memory requested (GiB)', cell: round(r.memory.requested / GIB, 2) },
      { head: 'Memory requested %', cell: round(pct(r.memory.requested, r.memory.allocatable), 1) },
      { head: 'Memory in use (GiB)', cell: r.memory.used == null ? null : round(r.memory.used / GIB, 2) },
    ],
  },
  {
    id: 'memAlloc',
    label: 'Memory allocatable',
    sortName: 'memory allocatable to pods',
    width: '124px',
    compactWidth: '112px',
    min: 124,
    compactMin: 112,
    right: true,
    wideOnly: true,
    defaultDir: 'desc',
    sortValue: (r) => r.memory.allocatable,
    cell: (r) => (
      <span className="rk-num" style={{ color: 'var(--fg2)' }} title={`${fMem(r.memory.allocatable)} allocatable to pods, of ${fMem(r.memory.capacity)} on the machine`}>
        {fMem(r.memory.allocatable)}
      </span>
    ),
    export: (r) => [{ head: 'Memory allocatable (GiB)', cell: round(r.memory.allocatable / GIB, 2) }],
  },
  {
    id: 'memFree',
    label: 'Memory free',
    sortName: 'memory free to schedule',
    hint: FREE_LEGEND,
    width: '100px',
    compactWidth: '92px',
    min: 100,
    compactMin: 92,
    right: true,
    wideOnly: true,
    defaultDir: 'desc',
    sortValue: (r) => free(r.memory),
    cell: (r) => <FreeCell amount={fMem(free(r.memory))} share={100 - (r.memory.allocatable ? (r.memory.requested / r.memory.allocatable) * 100 : 100)} title={`${fMem(free(r.memory))} of ${fMem(r.memory.allocatable)} free to schedule`} />,
    export: (r) => [{ head: 'Memory free (GiB)', cell: round(free(r.memory) / GIB, 2) }],
  },
  {
    id: 'pods',
    label: 'Pods',
    sortName: 'pod count',
    width: '84px',
    compactWidth: '78px',
    min: 84,
    compactMin: 78,
    right: true,
    defaultDir: 'desc',
    sortValue: (r) => r.pods.running,
    // A link to the Pods tab filtered to this node (P2-09).
    cell: (r) => (
      <a className="rk-num rk-cell-link" href={href('pods', [], { node: r.name })} title={`${r.pods.running} pods use slots on this node, of ${r.pods.capacity}. Open them in the Pods tab.`}>
        {r.pods.running} / {r.pods.capacity}
      </a>
    ),
    export: (r) => [
      { head: 'Pods', cell: r.pods.running },
      { head: 'Pod capacity', cell: r.pods.capacity },
    ],
  },
  {
    id: 'taints',
    label: 'Taints',
    sortName: 'taints (tainted nodes first)',
    width: 'minmax(150px, 1fr)',
    min: 150,
    wideOnly: true,
    hiddenByDefault: true,
    sortValue: (r) => (r.taints.length ? '0 ' + r.taints.map(taintId).sort().join(',') : '1'),
    cell: (r) => (
      <span className="rk-mono" style={{ fontSize: 11.5, color: 'var(--fg2)' }} title={r.taints.map(taintId).join('\n') || 'No taints'}>
        {r.taints.length ? r.taints.map(taintShort).join(', ') : '—'}
      </span>
    ),
    export: (r) => [{ head: 'Taints', cell: r.taints.map(taintId).join('\n'), json: r.taints.map(taintId) }],
  },
  {
    id: 'zone',
    label: 'Zone',
    width: '96px',
    min: 96,
    wideOnly: true,
    hiddenByDefault: true,
    sortValue: (r) => r.zone,
    cell: (r) => (
      <span className="rk-mono" style={{ color: 'var(--fg2)' }} title={r.zone || 'No zone label'}>
        {r.zone || '—'}
      </span>
    ),
    export: (r) => [{ head: 'Zone', cell: r.zone }],
  },
  {
    id: 'labels',
    label: 'Labels',
    width: 'minmax(170px, 1.2fr)',
    min: 170,
    wideOnly: true,
    hiddenByDefault: true,
    sortValue: (r) => ownLabels(r).join(','),
    cell: (r) => {
      const labels = ownLabels(r)
      return (
        <span className="rk-mono" style={{ fontSize: 11.5, color: 'var(--fg2)' }} title={labels.join('\n') || 'Only kubernetes.io labels'}>
          {labels.length ? labels.join(', ') : '—'}
        </span>
      )
    },
    export: (r) => [{ head: 'Labels', cell: ownLabels(r).join('\n'), json: ownLabels(r) }],
  },
  {
    id: 'kubelet',
    label: 'Kubelet',
    sortName: 'kubelet version',
    width: '110px',
    min: 110,
    wideOnly: true,
    hiddenByDefault: true,
    sortValue: (r) => r.kubelet,
    cell: (r) => (
      <span className="rk-mono" style={{ fontSize: 12, color: 'var(--fg2)' }}>
        {r.kubelet}
      </span>
    ),
    export: (r) => [{ head: 'Kubelet', cell: r.kubelet }],
  },
  {
    id: 'age',
    label: 'Age',
    width: '64px',
    min: 64,
    wideOnly: true,
    defaultDir: 'desc',
    sortValue: (r, now) => minutesSince(r.created, now),
    cell: (r, now) => (
      <span className="rk-num" style={{ color: 'var(--fg2)' }} title={`Joined ${fmtTs(r.created)}`}>
        {fmtAge(r.created, now)}
      </span>
    ),
    export: (r) => [{ head: 'Joined', cell: fmtTs(r.created) }],
  },
]

/** What the search box matches and suggests for a node. */
export function nodeTerms(r: NodeRow): Term[] {
  const out: Term[] = [
    { kind: 'name', text: r.name },
    { kind: 'status', text: r.status },
  ]
  if (r.zone) out.push({ kind: 'label', text: `zone=${r.zone}` })
  for (const [k, v] of Object.entries(r.labels)) out.push({ kind: 'label', text: v ? `${k}=${v}` : k })
  for (const t of r.taints) out.push({ kind: 'taint', text: taintId(t) })
  return out
}

export function nodeLabel(r: NodeRow, open: boolean): string {
  const p = problemsCell(r)
  return `${r.name}, ${r.role}, ${r.status}${p.text !== '—' ? ', ' + p.text : ''}${open ? ' — details open, press to close' : ' — show details'}`
}
