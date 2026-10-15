import { useState } from 'react'
import { useDetails } from '../api/details'
import type { ServiceDetails, ServiceRow } from '../api/types'
import { Tone } from '../columns/cells'
import { fmtAge, fmtTs } from '../lib/format'
import { apiPath, href } from '../lib/router'
import { Card, Chips, Drawer, Facts } from './Drawer'
import { EventsTable } from './EventsTable'
import { WarnIcon } from './Icons'
import { ScrollBox, Table, TR } from './Table'
import { YamlView } from './YamlView'
import { peekLink } from '../lib/peek'

interface Props {
  /** namespace/name. */
  serviceKey: string
  /** The row from the table, for the header while the details load. */
  row: ServiceRow | null
  /** The Service is no longer in the cluster. */
  gone: boolean
  now: number
  onClose: () => void
  onToast: (msg: string) => void
}

/** The Service details drawer (P2-05): overview with ports, endpoints and the Ingresses that publish it, and YAML. */
export function ServicePanel({ serviceKey, row, gone, now, onClose, onToast }: Props) {
  const [ns, name] = serviceKey.split('/', 2) as [string, string]
  const [tab, setTab] = useState<'overview' | 'yaml'>('overview')
  const details = useDetails<ServiceDetails>(apiPath(`services/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`))
  const d = details.data
  const head = d ?? row
  const deleted = (gone || details.gone) && !details.forbidden
  return (
    <Drawer
      ask={head?.pending ? `Why has Service ${ns}/${name} no external IP?` : head && head.tone !== 'ok' && head.tone !== 'muted' ? `What is wrong with Service ${ns}/${name}?` : `Tell me about Service ${ns}/${name}`}
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
                  {d.ingresses.map((i, n) => (
                    <span key={`${i.name}-${n}`} className="rk-mono" style={{ fontSize: 12 }} data-testid="service-ingress">
                      Ingress {i.name}: {i.tls ? 'https' : 'http'}://{i.host || '*'}
                      {i.path && i.path !== '/' ? i.path : ''}
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
                      <span>{e.pod ? <a className="rk-mono rk-cell-link" {...peekLink({ kind: 'pod', namespace: e.namespace ?? ns, name: e.pod })} style={{ fontSize: 12 }} data-testid="endpoint-pod">{e.pod}</a> : <span style={{ color: 'var(--fg3)' }}>—</span>}</span>
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
              <EventsTable events={d.events} now={now} />
            </Card>
          )}
        </>
      )}
      {tab === 'yaml' && !deleted && <YamlView kind="services" namespace={ns} name={name} onToast={onToast} />}
      {tab === 'yaml' && deleted && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>The Service is gone, so there is no YAML to read.</p>}
    </Drawer>
  )
}
