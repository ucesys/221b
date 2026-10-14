import type { ServiceRow } from '../api/types'
import { fmtAge, fmtTs, minutesSince } from '../lib/format'
import { toneRank } from '../lib/nodes'
import { href } from '../lib/router'
import type { ColumnDef, Term } from '../lib/table'
import { CellLink, Mono, Tone } from './cells'

/** The Services table's columns, with the External group (P2-05). */
export const SERVICE_COLUMNS: ColumnDef<ServiceRow>[] = [
  {
    id: 'name',
    label: 'Service',
    sortName: 'service name',
    width: 'minmax(160px, 1.3fr)',
    compactWidth: 'minmax(140px, 1.2fr)',
    min: 160,
    compactMin: 140,
    sortValue: (r) => r.name,
    cell: (r) => (
      <span className="rk-mono rk-pname" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
        {r.name}
      </span>
    ),
    export: (r) => [{ head: 'Service', cell: r.name }],
  },
  {
    id: 'namespace',
    label: 'Namespace',
    width: 'minmax(110px, .8fr)',
    compactWidth: '110px',
    min: 110,
    sortValue: (r) => r.namespace,
    cell: (r) => <CellLink href={href('namespaces', [r.namespace])} text={r.namespace} tip={`Open namespace ${r.namespace}`} />,
    export: (r) => [{ head: 'Namespace', cell: r.namespace }],
  },
  {
    id: 'type',
    label: 'Type',
    width: '110px',
    min: 110,
    sortValue: (r) => (r.headless ? 'Headless' : r.type),
    cell: (r) => <Mono text={r.headless ? 'Headless' : r.type} size={12} />,
    export: (r) => [{ head: 'Type', cell: r.headless ? 'Headless' : r.type }],
  },
  {
    id: 'clusterIP',
    label: 'Cluster IP',
    width: '124px',
    min: 124,
    sortValue: (r) => (r.headless ? '' : r.clusterIP),
    cell: (r) => (r.headless ? <span className="rk-tag">headless</span> : <Mono text={r.clusterIP} dim={false} />),
    export: (r) => [{ head: 'Cluster IP', cell: r.headless ? 'None (headless)' : r.clusterIP }],
  },
  {
    id: 'ports',
    label: 'Ports',
    width: 'minmax(110px, .9fr)',
    min: 110,
    sortValue: (r) => r.ports.join(', '),
    cell: (r) => <Mono text={r.ports.join(', ')} tip={r.ports.join('\n')} size={12} />,
    export: (r) => [{ head: 'Ports', cell: r.ports.join('\n'), json: r.ports }],
  },
  {
    id: 'endpoints',
    label: 'Endpoints',
    sortName: 'ready endpoints (problems first)',
    width: '120px',
    min: 120,
    defaultDir: 'desc',
    sortValue: (r) => toneRank[r.tone] * 1000 + r.total - r.ready,
    cell: (r) => <Tone tone={r.tone} text={r.status} tip={r.selector ? `${r.ready} of ${r.total} endpoints ready` : 'No selector: endpoints are managed by hand or by another controller'} />,
    export: (r) => [
      { head: 'Ready endpoints', cell: r.ready },
      { head: 'Endpoints', cell: r.total },
    ],
  },
  {
    id: 'externalIP',
    label: 'IP',
    group: 'External',
    sortName: 'external IP (pending first)',
    width: '120px',
    min: 120,
    defaultDir: 'desc',
    sortValue: (r) => (r.pending ? 'zz pending' : r.externalIPs.join(', ') || null),
    cell: (r) => (r.pending ? <Tone tone="warn" text="pending" tip={r.pendingWhy ?? 'No external IP yet'} /> : <Mono text={r.externalIPs.join(', ')} dim={false} />),
    export: (r) => [{ head: 'External IP', cell: r.pending ? 'pending' : r.externalIPs.join('\n') || null, json: r.pending ? 'pending' : r.externalIPs }],
  },
  {
    id: 'nodePorts',
    label: 'Node ports',
    group: 'External',
    width: '100px',
    min: 100,
    sortValue: (r) => (r.nodePorts.length ? r.nodePorts[0]! : null),
    cell: (r) => <Mono text={r.nodePorts.join(', ')} tip={r.nodePorts.length ? 'Open on this port of every node' : 'No node ports'} />,
    export: (r) => [{ head: 'Node ports', cell: r.nodePorts.join('\n') || null, json: r.nodePorts }],
  },
  {
    id: 'hostnames',
    label: 'Hostname',
    group: 'External',
    sortName: 'Ingress hostname',
    width: 'minmax(150px, 1.1fr)',
    min: 150,
    sortValue: (r) => r.hostnames.join(', ') || null,
    cell: (r) => <Mono text={r.hostnames.join(', ')} tip={r.hostnames.length ? 'Published by an Ingress at ' + r.hostnames.join(', ') : 'No Ingress publishes this Service'} />,
    export: (r) => [{ head: 'Hostnames', cell: r.hostnames.join('\n') || null, json: r.hostnames }],
  },
  {
    id: 'release',
    label: 'Helm release',
    width: '130px',
    min: 130,
    wideOnly: true,
    hiddenByDefault: true,
    sortValue: (r) => r.release || null,
    cell: (r) => (r.release ? <CellLink href={href('helm', [r.namespace, r.release])} text={r.release} /> : <Mono text="" />),
    export: (r) => [{ head: 'Helm release', cell: r.release ?? null }],
  },
  {
    id: 'age',
    label: 'Age',
    width: '64px',
    min: 64,
    wideOnly: true,
    defaultDir: 'desc',
    sortValue: (r, now) => minutesSince(r.created, now),
    cell: (r, now) => <Mono text={fmtAge(r.created, now)} tip={`Created ${fmtTs(r.created)}`} />,
    export: (r) => [{ head: 'Created', cell: fmtTs(r.created) }],
  },
]

export function serviceTerms(r: ServiceRow): Term[] {
  const out: Term[] = [
    { kind: 'name', text: r.name },
    { kind: 'namespace', text: r.namespace },
    { kind: 'type', text: r.headless ? 'Headless' : r.type },
  ]
  if (!r.headless && r.clusterIP) out.push({ kind: 'ip', text: r.clusterIP })
  for (const ip of r.externalIPs) out.push({ kind: 'ip', text: ip })
  for (const h of r.hostnames) out.push({ kind: 'host', text: h })
  for (const p of r.ports) out.push({ kind: 'port', text: p })
  if (r.pending) out.push({ kind: 'status', text: 'pending' })
  if (r.release) out.push({ kind: 'release', text: r.release })
  return out
}

export function serviceLabel(r: ServiceRow, open: boolean): string {
  return `${r.name} in ${r.namespace}, ${r.headless ? 'headless' : r.type}, ${r.status}${r.pending ? ', external IP pending' : ''}${open ? ' — details open, press to close' : ' — show details'}`
}

export type ServiceFilter = 'all' | 'ClusterIP' | 'Headless' | 'NodePort' | 'LoadBalancer' | 'notReady'

export function serviceFilterMatches(r: ServiceRow, f: ServiceFilter): boolean {
  switch (f) {
    case 'all':
      return true
    case 'Headless':
      return r.headless
    case 'notReady':
      return r.tone !== 'ok' && r.tone !== 'muted'
    default:
      return !r.headless && r.type === f
  }
}
