import { useMemo } from 'react'
import { useView } from '../api/live'
import type { HopLink, NetFactsRow, NetworkRow, NodeNetRow, TraceRow } from '../api/types'
import { Mono } from '../columns/cells'
import { fmtAge, fmtTs, minutesSince, nWord } from '../lib/format'
import { listText } from '../lib/rbac'
import { navigate, withParams } from '../lib/router'
import type { ColumnDef, Term } from '../lib/table'
import { useTable } from '../lib/useTable'
import { DataTable } from './DataTable'
import { ExportMenu } from './ExportMenu'
import { Select } from './Select'
import type { TabProps } from './tabs'
import { peekHref, peekLink, type Peek } from '../lib/peek'
import { AskSherlock } from './Sherlock'

const TONE_RANK: Record<string, number> = { err: 0, warn: 1, ok: 2, muted: 2, info: 2 }

/** What a hop's target opens: its details over this tab. */
export function linkOf(l: HopLink): Peek | null {
  switch (l.kind) {
    case 'ingress':
      return { kind: 'object', type: 'Ingress', namespace: l.namespace!, name: l.name }
    case 'secret':
      return { kind: 'object', type: 'Secret', namespace: l.namespace!, name: l.name }
    case 'service':
      return { kind: 'service', namespace: l.namespace!, name: l.name }
    case 'pod':
      return { kind: 'pod', namespace: l.namespace!, name: l.name }
    case 'netpol':
      return { kind: 'policy', key: `np:${l.namespace}/${l.name}` }
    case 'node':
      return { kind: 'node', name: l.name }
  }
  return null
}

function routeName(r: TraceRow): string {
  return (r.host || '*') + (r.path && r.path !== '/' ? r.path : '')
}

const Tone = ({ h }: { h: { text: string; tone: string; tip?: string } }) => (
  <span className="rk-t" data-tone={h.tone} title={h.tip ?? h.text} style={{ fontSize: 12.5, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
    {h.text}
  </span>
)

const ROUTE_COLUMNS: ColumnDef<TraceRow>[] = [
  { id: 'host', label: 'Host', width: 'minmax(200px, 1.3fr)', min: 200, sortValue: (r) => routeName(r), cell: (r) => <Mono text={routeName(r)} dim={false} />, export: (r) => [{ head: 'URL', cell: r.url }] },
  { id: 'ingress', label: 'Ingress', width: 'minmax(170px, 1.1fr)', min: 170, sortValue: (r) => `${r.namespace}/${r.ingress}`, cell: (r) => <Mono text={`${r.namespace}/${r.ingress}`} size={12} />, export: (r) => [{ head: 'Ingress', cell: `${r.namespace}/${r.ingress}` }] },
  { id: 'backend', label: 'Backend', width: 'minmax(150px, 1fr)', min: 150, sortValue: (r) => r.backend, cell: (r) => <Mono text={r.backend || '—'} size={12} />, export: (r) => [{ head: 'Backend', cell: r.backend }] },
  { id: 'eps', label: 'Endpoints', sortName: 'readiness (least ready first)', width: '130px', min: 130, sortValue: (r) => TONE_RANK[r.endpoints.tone] ?? 2, cell: (r) => <Tone h={r.endpoints} />, export: (r) => [{ head: 'Endpoints', cell: r.endpoints.text }] },
  { id: 'policy', label: 'Network policy', width: 'minmax(150px, 1fr)', min: 150, sortValue: (r) => TONE_RANK[r.policy.tone] ?? 2, cell: (r) => <Tone h={r.policy} />, export: (r) => [{ head: 'Network policy', cell: r.policy.text }] },
  { id: 'cert', label: 'Certificate', sortName: 'certificate (expires soonest first)', width: '130px', min: 130, sortValue: (r) => (r.notAfter ? Date.parse(r.notAfter) : r.cert.tone === 'err' ? 0 : Number.MAX_SAFE_INTEGER), cell: (r) => <Tone h={r.cert} />, export: (r) => [{ head: 'Certificate valid until', cell: r.notAfter ? fmtTs(r.notAfter) : r.cert.text }] },
  {
    id: 'verdict',
    label: 'Request',
    sortName: 'request outcome',
    width: '160px',
    min: 160,
    sortValue: (r) => TONE_RANK[r.tone] ?? 2,
    cell: (r) => (
      <span className="rk-pill" data-tone={r.tone} data-testid="route-verdict">
        <span className="rk-dot" />
        {r.verdict}
      </span>
    ),
    export: (r) => [{ head: 'Request', cell: r.verdict }],
  },
  { id: 'age', label: 'Age', sortName: 'age (newest first)', width: '60px', min: 60, right: true, sortValue: (r, now) => (r.created ? minutesSince(r.created, now) : null), cell: (r, now) => <Mono text={r.created ? fmtAge(r.created, now) : '—'} tip={fmtTs(r.created)} />, export: (r) => [{ head: 'Created', cell: fmtTs(r.created) }] },
]

const routeTerms = (r: TraceRow): Term[] => [
  { kind: 'host', text: r.host },
  { kind: 'ingress', text: r.ingress },
]

const NODE_COLUMNS: ColumnDef<NodeNetRow>[] = [
  { id: 'name', label: 'Node', width: 'minmax(150px, 1fr)', min: 150, sortValue: (r) => r.name, cell: (r) => <Mono text={r.name} dim={false} />, export: (r) => [{ head: 'Node', cell: r.name }] },
  {
    id: 'role',
    label: 'Role',
    width: '120px',
    min: 120,
    sortValue: (r) => r.role,
    cell: (r) => (
      <span className="rk-tag" data-tone={r.role === 'control-plane' ? 'info' : undefined} style={{ fontSize: 11, padding: '1px 7px' }}>
        {r.role === 'control-plane' ? 'Control plane' : 'Worker'}
      </span>
    ),
    export: (r) => [{ head: 'Role', cell: r.role }],
  },
  { id: 'ip', label: 'Internal IP', width: '130px', min: 130, sortValue: (r) => r.internalIP.split('.').map((x) => x.padStart(3, '0')).join('.'), cell: (r) => <Mono text={r.internalIP} />, export: (r) => [{ head: 'Internal IP', cell: r.internalIP }] },
  { id: 'cidr', label: 'Pod CIDR', width: '140px', min: 140, sortValue: (r) => r.podCIDR, cell: (r) => <Mono text={r.podCIDR || '—'} />, export: (r) => [{ head: 'Pod CIDR', cell: r.podCIDR }] },
  {
    id: 'ips',
    label: 'Pod IPs in use',
    sortName: 'pod IPs in use (most first)',
    width: '150px',
    min: 150,
    defaultDir: 'desc',
    sortValue: (r) => r.podIPs,
    cell: (r) => {
      const pct = r.capacity ? Math.min(100, (r.podIPs / r.capacity) * 100) : 0
      return (
        <span className="rk-meter" title={r.capacity ? `${r.podIPs} of ${r.capacity} addresses in ${r.podCIDR}` : `${r.podIPs} pod IPs`} style={{ gap: 8 }}>
          <span className="rk-num" style={{ fontSize: 12.5, minWidth: 56 }}>
            {r.podIPs}
            {r.capacity ? ` of ${r.capacity}` : ''}
          </span>
          {r.capacity > 0 && (
            <span className="rk-bar" style={{ width: 40 }}>
              <span className="rk-use" data-tone={pct > 80 ? 'warn' : 'ok'} style={{ width: `${pct.toFixed(1)}%` }} />
            </span>
          )}
        </span>
      )
    },
    export: (r) => [{ head: 'Pod IPs in use', cell: r.podIPs }],
  },
  { id: 'hn', label: 'Host-network pods', width: '140px', min: 140, right: true, defaultDir: 'desc', sortValue: (r) => r.hostNetwork, cell: (r) => <span className="rk-num">{r.hostNetwork}</span>, export: (r) => [{ head: 'Host-network pods', cell: r.hostNetwork }] },
  { id: 'zone', label: 'Zone', width: '110px', min: 110, sortValue: (r) => r.zone ?? '', cell: (r) => <Mono text={r.zone ?? '—'} />, export: (r) => [{ head: 'Zone', cell: r.zone ?? '' }] },
]

const nodeTerms = (r: NodeNetRow): Term[] => [{ kind: 'name', text: r.name }]

/** The Networks tab (P4-02): the path of a request to each Ingress host, hop by hop. */
export function NetworksTab({ cluster, now, onToast, route }: TabProps) {
  const view = useView<NetworkRow>('networks')
  const { routes, nodes, facts } = useMemo(() => {
    const routes: TraceRow[] = []
    const nodes: NodeNetRow[] = []
    let facts: NetFactsRow | undefined
    for (const r of view.rows) {
      if (r.key.startsWith('route:')) routes.push(r as TraceRow)
      else if (r.key.startsWith('node:')) nodes.push(r as NodeNetRow)
      else if (r.key.startsWith('facts:')) facts = r as NetFactsRow
    }
    routes.sort((a, b) => routeName(a).localeCompare(routeName(b)) || a.key.localeCompare(b.key))
    return { routes, nodes, facts }
  }, [view.rows])
  const want = route.params.get('route') ?? ''
  const sel = routes.find((r) => r.key === want) ?? routes.find((r) => r.tone === 'err') ?? routes.find((r) => r.tone === 'warn') ?? routes[0]
  const set = (c: Record<string, string | undefined>) => navigate(withParams(route, c), true)
  const rt = useTable<TraceRow>({ id: 'net-routes', columns: ROUTE_COLUMNS, rows: routes, terms: routeTerms, now, defaultSort: { col: 'host', dir: 'asc' } })
  const nt = useTable<NodeNetRow>({ id: 'net-nodes', columns: NODE_COLUMNS, rows: nodes, terms: nodeTerms, now, defaultSort: { col: 'ip', dir: 'asc' } })
  const classes = facts?.classes ?? []
  const summary = [facts?.podCIDRs.length ? `Pods ${facts.podCIDRs.length > 2 ? `${facts.podCIDRs[0]} and ${facts.podCIDRs.length - 1} more` : facts.podCIDRs.join(', ')}` : '', facts?.serviceIP ? `Services from ${facts.serviceIP}` : '', `${nWord(routes.length, 'route', 'routes')}${classes.length ? ` via ${listText(classes.map((c) => c.name))}` : ''}`].filter(Boolean).join(' · ')
  return (
    <section className="rk-pane" aria-label="Networks" data-testid="networks-main" style={{ flex: '1 1 auto', minWidth: 0, padding: '20px 24px 32px', display: 'flex', flexDirection: 'column', gap: 16 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, flexWrap: 'wrap' }}>
          <h1 style={{ margin: 0, fontSize: 18, fontWeight: 600 }}>Networks</h1>
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>{summary}</span>
        </div>
      </div>

      <section className="rk-card" aria-label="Trace a request" style={{ overflow: 'hidden', flex: 'none' }} data-testid="trace">
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap', padding: '12px 16px', borderBottom: '1px solid var(--line)' }}>
          <h2 className="rk-h">Trace a request</h2>
          {routes.length > 0 && <Select label="Route" value={sel?.key ?? ''} options={routes.map((r) => [r.key, routeName(r) + (r.tone === 'ok' ? '' : ' · ' + r.verdict.toLowerCase())])} onChange={(v) => set({ route: v })} testId="trace-route" width={380} />}
        </div>
        {!sel ? (
          <p style={{ margin: 0, padding: '14px 16px', fontSize: 12.5, color: 'var(--fg3)' }}>{view.loaded ? 'No Ingress publishes anything in this cluster.' : 'Reading Ingresses…'}</p>
        ) : (
          <div style={{ padding: '12px 16px 14px', display: 'flex', flexDirection: 'column', gap: 12 }}>
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, flexWrap: 'wrap' }}>
              <span className="rk-pill" data-tone={sel.tone} data-testid="trace-verdict">
                <span className="rk-dot" />
                {sel.verdict}
              </span>
              <strong className="rk-mono" style={{ fontSize: 13 }}>
                {sel.url}
              </strong>
              <span style={{ fontSize: 12.5, color: 'var(--fg2)' }} data-testid="trace-text">
                {sel.text}
              </span>
              <span style={{ marginLeft: 'auto' }}>
                <AskSherlock question={sel.tone === 'ok' ? `Trace a request to ${sel.url}` : `Why does a request to ${sel.url} fail?`} compact />
              </span>
            </div>
            <ol className="rk-hops" data-testid="hops">
              {sel.hops.map((h, i) => {
                const to = h.link ? linkOf(h.link) : null
                return (
                  <li key={h.k} className="rk-hop" data-tone={h.tone} data-hop={h.k} data-testid="hop">
                    <span className="rk-hop-n">{i + 1}</span>
                    <span className="rk-hop-k">{h.k}</span>
                    <span className="rk-hop-t rk-mono" style={{ fontWeight: 600, fontSize: 12.5, overflowWrap: 'anywhere' }}>
                      {to ? (
                        <a className="rk-link" {...peekLink(to)} title={`Open the details of ${h.t}`} style={{ textDecoration: 'none' }}>
                          {h.t}
                        </a>
                      ) : (
                        h.t
                      )}
                    </span>
                    <span className="rk-hop-d" style={{ fontSize: 12, color: 'var(--fg2)', whiteSpace: 'pre-line' }} data-testid="hop-d">
                      {h.d}
                    </span>
                    <span className="rk-hop-s rk-t" data-tone={h.tone} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, justifySelf: 'end', fontSize: 12, fontWeight: 500, whiteSpace: 'nowrap' }} data-testid="hop-s">
                      <span className="rk-dot" />
                      {h.s}
                    </span>
                  </li>
                )
              })}
            </ol>
          </div>
        )}
      </section>

      <section className="rk-card" aria-label="Ingress routes" style={{ overflow: 'hidden', flex: 'none' }} data-testid="routes">
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, padding: '12px 16px', borderBottom: '1px solid var(--line)' }}>
          <h2 className="rk-h">Ingress routes</h2>
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>click a row to trace it</span>
          <span style={{ marginLeft: 'auto' }}>
            <ExportMenu onExport={(f) => rt.exportRows(f, cluster?.name, 'ingress-routes', onToast)} disabled={!rt.shown.length} what="the routes" />
          </span>
        </div>
        <DataTable<TraceRow> label="Ingress routes" rows={rt.shown} cols={rt.cols} rowKey={(r) => r.key} rowLabel={(r) => `Trace ${r.url}`} sort={rt.sort} onSort={rt.onSort} selected={sel?.key ?? null} onOpen={(k) => set({ route: k })} now={now} empty="No Ingress publishes anything in this cluster." rowAttr="route" />
      </section>

      {facts && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(340px, 100%), 1fr))', gap: 12, alignItems: 'start' }}>
          <Fact title="Cluster network" sub={facts.cni.name ?? 'network plugin unknown'} items={[['Network plugin', facts.cni.name ? `${facts.cni.name}${facts.cni.enforces === 'yes' ? ', enforces NetworkPolicies' : facts.cni.enforces === 'no' ? ', does not enforce NetworkPolicies' : ''}` : 'unknown'], ['Pod ranges', facts.podCIDRs.length ? `${nWord(facts.podCIDRs.length, 'node range', 'node ranges')}: ${facts.podCIDRs.slice(0, 3).join(', ')}${facts.podCIDRs.length > 3 ? ', …' : ''}` : 'not set on the nodes'], ['Services', `${nWord(facts.clusterIPs, 'cluster IP', 'cluster IPs')} in use${facts.serviceIP ? `; the API server’s is ${facts.serviceIP}` : ''}`], ['kube-proxy', facts.kubeProxy ?? 'not found as a DaemonSet (the plugin may replace it)'], ['Load balancers', facts.pendingLBs.length ? `${listText(facts.pendingLBs)} ${facts.pendingLBs.length === 1 ? 'waits' : 'wait'} for an external IP` : 'none waiting for an address'], ['Network policies', facts.policies ? `${nWord(facts.policies, 'policy', 'policies')} in ${listText(facts.policyNamespaces)}` : 'none']]} />
          {facts.dns && <Fact title="Cluster DNS" sub={facts.dns.service} items={[['Service', `${facts.dns.service} · ${facts.dns.ip}`], ['Pods', `${facts.dns.ready} of ${facts.dns.total} ready${facts.dns.nodes.length ? ` on ${listText(facts.dns.nodes)}` : ''}`], ['Names', 'a Service resolves as name.namespace.svc and its cluster domain']]} />}
          {classes.map((c) => (
            <Fact key={c.name} title="Ingress" sub={`class ${c.name}`} items={[['Class', `${c.name}${c.default ? ', the default' : ''} · ${c.controller}`], ['Controller', c.total ? `${c.ready} of ${c.total} pods ready` : 'no pods found'], ['Routes', nWord(c.hosts, 'host', 'hosts')]]} />
          ))}
        </div>
      )}

      <section className="rk-card" aria-label="Node addresses and pod ranges" style={{ overflow: 'hidden', flex: 'none' }}>
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, padding: '12px 16px', borderBottom: '1px solid var(--line)' }}>
          <h2 className="rk-h">Node addresses and pod ranges</h2>
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>each node hands out pod IPs from its own range</span>
          <span style={{ marginLeft: 'auto' }}>
            <ExportMenu onExport={(f) => nt.exportRows(f, cluster?.name, 'node-addresses', onToast)} disabled={!nt.shown.length} what="the nodes" />
          </span>
        </div>
        <DataTable<NodeNetRow> label="Node addresses" rows={nt.shown} cols={nt.cols} rowKey={(r) => r.name} rowLabel={(r) => `${r.name} — open its details`} sort={nt.sort} onSort={nt.onSort} onOpen={(k) => navigate(peekHref({ kind: 'node', name: k }))} now={now} empty="No nodes." rowAttr="netnode" />
      </section>
    </section>
  )
}

function Fact({ title, sub, items }: { title: string; sub: string; items: [string, string][] }) {
  return (
    <section className="rk-card" aria-label={title} style={{ padding: '12px 16px', display: 'flex', flexDirection: 'column', gap: 10 }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
        <h3 className="rk-h">{title}</h3>
        <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{sub}</span>
      </div>
      <dl className="rk-kv">
        {items.map(([k, v]) => (
          <div key={k} style={{ display: 'contents' }}>
            <dt>{k}</dt>
            <dd style={{ whiteSpace: 'pre-line' }}>{v}</dd>
          </div>
        ))}
      </dl>
    </section>
  )
}
