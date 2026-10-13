import { useCallback, useMemo, useState } from 'react'
import { useDetails } from '../api/details'
import { useView } from '../api/live'
import type { ServiceDetails, ServiceRow } from '../api/types'
import { Tone } from '../columns/cells'
import { SERVICE_COLUMNS, serviceFilterMatches, serviceLabel, serviceTerms, type ServiceFilter } from '../columns/services'
import { fmtAge, fmtTs, nWord } from '../lib/format'
import { href, navigate, withParams } from '../lib/router'
import { useTable } from '../lib/useTable'
import { ColumnsMenu } from './ColumnsMenu'
import { DataTable } from './DataTable'
import { Card, Chips, Drawer, Facts } from './Drawer'
import { ExportMenu } from './ExportMenu'
import { WarnIcon } from './Icons'
import { SearchBox } from './SearchBox'
import { SideList, type SideGroup } from './SideList'
import { ScrollBox, Table, TR } from './Table'
import type { TabProps } from './tabs'
import { YamlView } from './YamlView'

const FILTERS: [ServiceFilter, string][] = [
  ['all', 'All'],
  ['ClusterIP', 'ClusterIP'],
  ['Headless', 'Headless'],
  ['NodePort', 'NodePort'],
  ['LoadBalancer', 'LoadBalancer'],
  ['notReady', 'Not fully ready'],
]

function systemNamespace(ns: string): boolean {
  return ns === 'default' || ns.startsWith('kube-')
}

/** The Services tab (P2-05): namespaces on the left, the Services on the right with the External columns. */
export function ServicesTab({ cluster, now, onToast, route }: TabProps) {
  const { rows, loaded } = useView<ServiceRow>('services')
  const p = route.params
  const nsParam = p.get('ns') ?? ''
  const filter: ServiceFilter = FILTERS.some(([id]) => id === p.get('type')) ? (p.get('type') as ServiceFilter) : 'all'
  const query = p.get('q') ?? ''
  const selected = p.get('open')
  const [listQuery, setListQuery] = useState('')
  const set = useCallback((changes: Record<string, string | string[] | undefined>) => navigate(withParams(route, changes), true), [route])

  const byNs = useMemo(() => {
    const m = new Map<string, { n: number; notReady: number }>()
    for (const r of rows) {
      const e = m.get(r.namespace) ?? { n: 0, notReady: 0 }
      e.n++
      if (r.tone !== 'ok' && r.tone !== 'muted') e.notReady++
      m.set(r.namespace, e)
    }
    return m
  }, [rows])
  const namespaces = useMemo(() => [...byNs.keys()].sort(), [byNs])
  const ns = useMemo(() => (namespaces.includes(nsParam) ? nsParam : ''), [namespaces, nsParam])
  const inNs = useMemo(() => (ns ? rows.filter((r) => r.namespace === ns) : rows), [rows, ns])
  const counts = useMemo(() => Object.fromEntries(FILTERS.map(([id]) => [id, inNs.filter((r) => serviceFilterMatches(r, id)).length])) as Record<ServiceFilter, number>, [inNs])
  const scoped = useMemo(() => inNs.filter((r) => serviceFilterMatches(r, filter)), [inNs, filter])
  const table = useTable<ServiceRow>({ id: 'services', columns: SERVICE_COLUMNS, rows: scoped, terms: serviceTerms, now, defaultSort: { col: 'namespace', dir: 'asc' }, query, onQuery: (q) => set({ q }) })

  const totalNotReady = rows.filter((r) => r.tone !== 'ok' && r.tone !== 'muted').length
  const lq = listQuery.trim().toLowerCase()
  const item = (name: string) => {
    const e = byNs.get(name)!
    return {
      key: name,
      href: href('services', [], { ns: name }),
      current: ns === name,
      name,
      right: e.notReady ? (
        <span className="rk-t" data-tone="warn" style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 11 }}>
          <span className="rk-dot" />
          {e.notReady}
        </span>
      ) : undefined,
      sub: `${nWord(e.n, 'service', 'services')}${e.notReady ? ` · ${e.notReady} not fully ready` : ''}`,
    }
  }
  const groups: SideGroup[] = [
    {
      title: '',
      items: [
        {
          key: '*',
          href: href('services'),
          current: ns === '',
          name: 'All namespaces',
          right: totalNotReady ? (
            <span className="rk-t" data-tone="warn" style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 11 }}>
              <span className="rk-dot" />
              {totalNotReady}
            </span>
          ) : undefined,
          sub: `${nWord(rows.length, 'service', 'services')}${totalNotReady ? ` · ${totalNotReady} not fully ready` : ''}`,
        },
      ],
    },
    { title: 'Applications and add-ons', items: namespaces.filter((n) => !systemNamespace(n) && (!lq || n.includes(lq))).map(item) },
    { title: 'Kubernetes', items: namespaces.filter((n) => systemNamespace(n) && (!lq || n.includes(lq))).map(item) },
  ]

  const ingressed = inNs.filter((r) => r.hostnames.length).length
  const nodePorts = inNs.filter((r) => r.nodePorts.length).length
  const pendingLB = inNs.filter((r) => r.pending).length
  const notReady = inNs.filter((r) => r.tone !== 'ok' && r.tone !== 'muted').length
  const cards: { n: number; label: string; tone?: 'warn'; filter?: ServiceFilter }[] = [
    { n: inNs.length, label: ns ? `Services in ${ns}` : `Services in ${nWord(namespaces.length, 'namespace', 'namespaces')}` },
    { n: ingressed, label: 'published by an Ingress' },
    { n: nodePorts, label: 'open on node ports of every node', filter: 'NodePort' },
    { n: pendingLB, label: 'LoadBalancer without an external IP', tone: pendingLB ? 'warn' : undefined, filter: 'LoadBalancer' },
    { n: notReady, label: 'not fully ready', tone: notReady ? 'warn' : undefined, filter: 'notReady' },
  ]
  const selectedRow = selected ? (rows.find((r) => r.key === selected) ?? null) : null

  return (
    <>
      <SideList label="Namespaces with services" title="Namespaces" query={listQuery} onQuery={setListQuery} placeholder="Filter namespaces" groups={groups} empty={loaded ? 'No namespaces match.' : 'Waiting for the first list…'} testId="service-namespaces" foot="A Service gives a set of pods one stable name and address. Its endpoints are the pods its selector matches that have an IP; only ready ones get traffic." />
      <main className="rk-pane" aria-label="Services" style={{ flex: '999 1 560px', minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        <section aria-label="Service list" style={{ padding: '18px 24px 28px', display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', gap: '4px 12px' }}>
            <h2 style={{ margin: 0, fontSize: 18, fontWeight: 600 }}>Services</h2>
            <span style={{ fontSize: 12.5, color: 'var(--fg3)' }} data-testid="services-summary">
              {loaded ? `${nWord(inNs.length, 'Service', 'Services')}${ns ? ` in ${ns}` : ''}` : 'Waiting for the first list of services…'}
            </span>
          </div>
          <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)', maxWidth: 900 }}>
            Every Service{cluster?.name ? ` in ${cluster.name}` : ''}: its cluster and external addresses, its ports and node ports, how many of its endpoints are ready and the hostnames that publish it. Open one for its endpoints and YAML; Columns shows the type and the release that manages it.
          </p>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(170px, 1fr))', gap: 12 }} data-testid="service-cards">
            {cards.map((c) => (
              <button
                key={c.label}
                className="rk-card"
                onClick={() => c.filter && set({ type: filter === c.filter ? undefined : c.filter })}
                disabled={!c.filter}
                aria-pressed={c.filter ? filter === c.filter : undefined}
                style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 4, textAlign: 'left', font: 'inherit', color: 'inherit', cursor: c.filter ? 'pointer' : 'default', background: c.tone === 'warn' ? 'var(--warn-weak)' : undefined, borderColor: c.filter && filter === c.filter ? 'var(--accent)' : undefined }}
              >
                <span className="rk-num" style={{ fontSize: 22, fontWeight: 600, color: c.tone === 'warn' ? 'var(--warn)' : undefined }}>
                  {c.n}
                </span>
                <span style={{ fontSize: 12, color: c.tone === 'warn' ? 'var(--warn)' : 'var(--fg3)' }}>{c.label}</span>
              </button>
            ))}
          </div>
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
            <div className="rk-seg" role="group" aria-label="Services by type">
              {FILTERS.map(([id, label]) => (
                <button key={id} aria-pressed={filter === id} onClick={() => set({ type: id === 'all' ? undefined : id })} data-type={id}>
                  {label} <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{counts[id]}</span>
                </button>
              ))}
            </div>
            <SearchBox rows={scoped} terms={serviceTerms} value={table.query} onChange={table.setQuery} placeholder="Name, IP, port or host" label="Search services" testId="service-search" />
            <div className="rk-tools" style={{ marginLeft: 'auto', gap: 8 }}>
              <ColumnsMenu cols={SERVICE_COLUMNS} hidden={table.hidden} defaults={table.defaults} onChange={table.setHidden} compact={false} />
              <ExportMenu onExport={(f) => table.exportRows(f, cluster?.name, 'services', onToast)} disabled={!table.shown.length} what="the services in view" />
            </div>
          </div>
          <DataTable<ServiceRow> label="Services" rows={table.shown} cols={table.cols} rowKey={(r) => r.key} rowLabel={serviceLabel} sort={table.sort} onSort={table.onSort} selected={selected} onOpen={(k) => set({ open: selected === k ? undefined : k })} now={now} empty={loaded ? 'No services match these filters.' : 'Waiting for the first list of services…'} rowAttr="service" />
        </section>
      </main>
      {selected && <ServicePanel serviceKey={selected} row={selectedRow} gone={loaded && !selectedRow} now={now} onClose={() => set({ open: undefined })} onToast={onToast} />}
    </>
  )
}

function ServicePanel({ serviceKey, row, gone, now, onClose, onToast }: { serviceKey: string; row: ServiceRow | null; gone: boolean; now: number; onClose: () => void; onToast: (msg: string) => void }) {
  const [ns, name] = serviceKey.split('/', 2) as [string, string]
  const [tab, setTab] = useState<'overview' | 'yaml'>('overview')
  const details = useDetails<ServiceDetails>(`/api/v1/services/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`)
  const d = details.data
  const head = d ?? row
  const deleted = gone || details.gone
  return (
    <Drawer
      label="Service details"
      testId="service-panel"
      kicker={
        <>
          <span>Service in</span>
          <a className="rk-link rk-mono" href={href('namespaces', [ns])} style={{ fontSize: 11.5 }}>
            {ns}
          </a>
        </>
      }
      title={name}
      facts={
        head && (
          <>
            <span className="rk-pill" data-tone={head.tone}>
              <span className="rk-dot" />
              {head.status}
            </span>
            <span>
              Type <span className="rk-mono" style={{ color: 'var(--fg)' }}>{head.headless ? 'Headless' : head.type}</span>
            </span>
            {!head.headless && (
              <span>
                Cluster IP <span className="rk-mono" style={{ color: 'var(--fg)' }}>{head.clusterIP}</span>
              </span>
            )}
            {head.pending && (
              <span className="rk-t" data-tone="warn" style={{ fontWeight: 500 }} title={head.pendingWhy}>
                external IP pending
              </span>
            )}
            {head.externalIPs.length > 0 && (
              <span>
                External IP <span className="rk-mono" style={{ color: 'var(--fg)' }}>{head.externalIPs.join(', ')}</span>
              </span>
            )}
            {head.release && (
              <span>
                Helm release{' '}
                <a className="rk-link rk-mono" href={href('helm', [ns, head.release])} style={{ fontSize: 12 }}>
                  {head.release}
                </a>
              </span>
            )}
          </>
        )
      }
      tabs={[
        { id: 'overview', label: 'Overview' },
        { id: 'yaml', label: 'YAML' },
      ]}
      tab={tab}
      onTab={(t) => setTab(t as 'overview' | 'yaml')}
      onClose={onClose}
      notice={
        deleted && (
          <div className="rk-callout" data-tone="warn" style={{ margin: '12px 20px 0' }}>
            <WarnIcon />
            <span style={{ fontSize: 12.5 }}>This Service was deleted. What is shown is the last state 221B saw.</span>
          </div>
        )
      }
    >
      {details.error && !deleted && !d && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }}>
          {details.error}
        </p>
      )}
      {!d && !details.error && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>Loading…</p>}
      {d && tab === 'overview' && (
        <>
          {d.pending && (
            <div className="rk-callout" data-tone="warn">
              <WarnIcon />
              <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
                <strong style={{ fontSize: 12.5, fontWeight: 600 }}>LoadBalancer without an external IP</strong>
                <span style={{ fontSize: 12.5, color: 'var(--fg2)' }} data-testid="pending-why">
                  {d.pendingWhy}
                  {d.nodePorts.length ? ` Its node ports still work: clients outside the cluster can use any node's IP with port ${d.nodePorts.join(', ')}.` : ''}
                </span>
              </div>
            </div>
          )}
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(390px, 100%), 1fr))', gap: 16, alignItems: 'start' }}>
            <Card title="Service">
              <Facts
                items={[
                  ['Type', d.headless ? 'Headless (ClusterIP None)' : d.type],
                  ['Cluster IP', d.headless ? 'None' : d.clusterIP],
                  ['DNS name', d.dnsName],
                  ['External IPs', d.pending ? 'pending' : d.externalIPs.join(', ') || '—'],
                  ['Node ports', d.nodePorts.join(', ') || '—'],
                  ['Hostnames', d.hostnames.join(', ') || 'not published by an Ingress'],
                  ['Selector', d.selectorText.length ? d.selectorText.join(', ') : 'none: endpoints are managed by hand'],
                  ['Session affinity', d.sessionAffinity || 'None'],
                  ['Traffic policy', d.trafficPolicy || '—'],
                  ['Helm release', d.release || '—'],
                  ['Created', `${fmtTs(d.created)} · ${fmtAge(d.created, now)} ago`],
                  ['UID', d.uid],
                ]}
              />
            </Card>
            <Card title="Ports" pad={false}>
              <Table label="Ports" template="minmax(90px, 1fr) 80px 90px 100px 90px" cols={[{ label: 'Name' }, { label: 'Port', right: true }, { label: 'Protocol' }, { label: 'Target' }, { label: 'Node port', right: true }]}>
                {d.portDetails.map((p, i) => (
                  <TR key={i}>
                    <span className="rk-mono">{p.name || '—'}</span>
                    <span className="rk-r rk-num">{p.port}</span>
                    <span className="rk-mono">{p.protocol}</span>
                    <span className="rk-mono">{p.targetPort}</span>
                    <span className="rk-r rk-num">{p.nodePort || '—'}</span>
                  </TR>
                ))}
              </Table>
              {d.ingresses.length > 0 && (
                <div style={{ padding: '0 16px 12px', display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5 }}>
                  <span className="rk-h">Published by</span>
                  {d.ingresses.map((i) => (
                    <span key={i.name} className="rk-mono" style={{ fontSize: 12 }}>
                      Ingress {i.name}: {i.hosts.join(', ')}
                    </span>
                  ))}
                </div>
              )}
            </Card>
          </div>
          <Card title={`Endpoints · ${d.ready} of ${d.total} ready`} pad={false} label="Endpoints">
            {d.endpoints.length === 0 ? (
              <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 12.5, color: 'var(--fg3)' }}>{d.selectorText.length ? 'No pod matches the selector, so the Service has no endpoints.' : 'No endpoints.'}</p>
            ) : (
              <ScrollBox label="Endpoints">
                <Table label="Endpoints" template="140px 90px minmax(200px, 1.5fr) minmax(130px, 1fr) 140px" minWidth={700} cols={[{ label: 'Address' }, { label: 'Ready' }, { label: 'Pod' }, { label: 'Node' }, { label: 'Pod status' }]}>
                  {d.endpoints.map((e, i) => (
                    <TR key={i}>
                      <span className="rk-mono">{e.address}</span>
                      <span>
                        <Tone tone={e.ready ? 'ok' : 'warn'} text={e.ready ? 'ready' : 'not ready'} />
                      </span>
                      <span>{e.pod ? <a className="rk-mono rk-cell-link" href={href('pods', [], { open: `${e.namespace ?? ns}/${e.pod}` })} style={{ fontSize: 12 }} data-testid="endpoint-pod">{e.pod}</a> : <span style={{ color: 'var(--fg3)' }}>—</span>}</span>
                      <span className="rk-mono" style={{ fontSize: 12, color: 'var(--fg2)' }}>
                        {e.node || '—'}
                      </span>
                      <span>{e.podStatus ? <Tone tone={e.tone ?? 'muted'} text={e.podStatus} /> : <span style={{ color: 'var(--fg3)' }}>—</span>}</span>
                    </TR>
                  ))}
                </Table>
              </ScrollBox>
            )}
            {d.endpoints.some((e) => e.pod) && (
              <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 12.5 }}>
                <a className="rk-link" href={href('pods', [], { ns, q: d.selectorText.filter((s) => s.includes('=')).join(' ') })} data-testid="service-pods-link">
                  Open the pods behind this Service →
                </a>
              </p>
            )}
          </Card>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(390px, 100%), 1fr))', gap: 16, alignItems: 'start' }}>
            <Card title="Labels">
              <Chips map={d.labels} />
            </Card>
            <Card title="Annotations">
              <Chips map={d.annotations} />
            </Card>
          </div>
          {d.events.length > 0 && (
            <Card title="Events" pad={false}>
              <Table label="Events" template="82px 170px minmax(240px, 2fr) 60px 110px" cols={[{ label: 'Type' }, { label: 'Reason' }, { label: 'Message' }, { label: 'Count', right: true }, { label: 'Last seen' }]}>
                {d.events.map((e, i) => (
                  <TR key={i} top>
                    <span>
                      <Tone tone={e.type === 'Warning' ? 'warn' : 'muted'} text={e.type} />
                    </span>
                    <span className="rk-mono" style={{ fontSize: 12 }}>
                      {e.reason}
                    </span>
                    <span className="rk-wrap" style={{ fontSize: 12.5 }}>
                      {e.message}
                    </span>
                    <span className="rk-r rk-num">{e.count}</span>
                    <span className="rk-num" style={{ color: 'var(--fg2)' }}>
                      {e.last ? `${fmtAge(e.last, now)} ago` : '—'}
                    </span>
                  </TR>
                ))}
              </Table>
            </Card>
          )}
        </>
      )}
      {tab === 'yaml' && !deleted && <YamlView kind="services" namespace={ns} name={name} onToast={onToast} />}
      {tab === 'yaml' && deleted && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>The Service is gone, so there is no YAML to read.</p>}
    </Drawer>
  )
}
