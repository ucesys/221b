import { useMemo, useState } from 'react'
import { useDetails } from '../api/details'
import type { ClusterInfo, ObjectDetails, ObjectRow, PodView } from '../api/types'
import { Tone } from '../columns/cells'
import { POD_COLUMNS, podLabel, podTerms } from '../columns/pods'
import { fMem, fmtAge, fmtTs, nWord } from '../lib/format'
import { kindNameOf, WORKLOAD_KINDS } from '../lib/kinds'
import { href, navigate } from '../lib/router'
import { useTable } from '../lib/useTable'
import { ColumnsMenu } from './ColumnsMenu'
import { DataTable } from './DataTable'
import { Card, Chips, Drawer, Facts } from './Drawer'
import { EventsTable } from './EventsTable'
import { ExportMenu } from './ExportMenu'
import { WarnIcon } from './Icons'
import { SearchBox } from './SearchBox'
import { ScrollBox, Table, TR } from './Table'
import { YamlView } from './YamlView'

interface Props {
  /** The kind as the rows carry it: "Deployment". */
  kind: string
  namespace: string
  name: string
  /** The row from the table, for the header while the details load. */
  row: ObjectRow | null
  /** The object is no longer in the cluster. */
  gone: boolean
  cluster: ClusterInfo | null
  now: number
  /** The hash that opens another object of this namespace in the same tab: an owner, one of the pods. */
  linkTo: (kind: string, name: string) => string
  onClose: () => void
  onToast: (msg: string) => void
}

type DrawerTabId = 'details' | 'pods' | 'events' | 'yaml'

function fBytes(n: number): string {
  return n < 1024 ? `${n} B` : fMem(n)
}

/**
 * The details drawer of any namespaced object that is not a pod or a Service
 * (those have drawers of their own): what the kind says about itself, the
 * pods a workload runs, its events and its YAML. Opening a pod or an owner
 * from here opens that object's drawer in place.
 */
export function ObjectPanel({ kind, namespace: ns, name, row, gone, cluster, now, linkTo, onClose, onToast }: Props) {
  const kindName = kindNameOf(kind)
  const [tab, setTab] = useState<DrawerTabId>('details')
  const details = useDetails<ObjectDetails>(`/api/v1/objects/${encodeURIComponent(kindName)}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`)
  const d = details.data
  const deleted = gone || details.gone
  const head = d ?? row
  const workload = d ? d.workload : WORKLOAD_KINDS.has(kind)
  const pods = useMemo<PodView[]>(() => (d ? d.pods.map((r) => ({ ...r, usage: d.usage?.[r.key] ?? null })) : []), [d])
  const podTable = useTable<PodView>({ id: 'object-pods', columns: POD_COLUMNS, rows: pods, terms: podTerms, now, defaultSort: { col: 'name', dir: 'asc' } })
  const ready = pods.filter((p) => p.tone === 'ok' && !p.done).length
  const failing = pods.filter((p) => p.tone === 'err').length

  const tabs = [
    { id: 'details', label: 'Details' },
    ...(workload ? [{ id: 'pods', label: 'Pods', count: d?.pods.length }] : []),
    { id: 'events', label: 'Events', count: d?.events.length },
    { id: 'yaml', label: 'YAML' },
  ]

  const facts: [string, string][] = [
    ...(d?.facts ?? []).map((f) => [f.label, f.value] as [string, string]),
    ['Namespace', ns],
    ...(head ? ([['Created', `${fmtTs(head.created)} · ${fmtAge(head.created, now)} ago`]] as [string, string][]) : []),
    ...(d ? ([['Controlled by', d.owners.length ? d.owners.map((o) => `${o.kind}/${o.name}`).join(' → ') : 'nothing: created directly']] as [string, string][]) : []),
    ...(d && !d.facts.some((f) => f.label === 'Helm release') ? ([['Helm release', d.release || '—']] as [string, string][]) : []),
    ...(head ? ([['API version', head.apiVersion]] as [string, string][]) : []),
  ]

  return (
    <Drawer
      label={`${kind} details`}
      testId="object-panel"
      kicker={
        <>
          <span>{kind} in</span>
          <a className="rk-link rk-mono" href={href('namespaces', [ns])} title="Show everything in this namespace" style={{ fontSize: 11.5 }}>
            {ns}
          </a>
        </>
      }
      title={name}
      facts={
        head && (
          <>
            <span className="rk-pill" data-tone={head.tone} data-testid="object-status">
              <span className="rk-dot" />
              {head.status}
            </span>
            {head.details && <span style={{ color: 'var(--fg2)' }}>{head.details}</span>}
            <span>
              Created{' '}
              <span className="rk-num" style={{ color: 'var(--fg)' }} title={fmtTs(head.created)}>
                {fmtAge(head.created, now)} ago
              </span>
            </span>
            {d && d.owners.length > 0 && (
              <span>
                {d.owners[0]!.kind}{' '}
                {d.owners.map((o, i) => (
                  <span key={`${o.kind}/${o.name}`}>
                    {i > 0 && <span style={{ color: 'var(--fg3)' }}> ← {o.kind} </span>}
                    <a className="rk-link rk-mono" href={linkTo(o.kind, o.name)} style={{ fontSize: 12 }} data-testid="object-owner-link">
                      {o.name}
                    </a>
                  </span>
                ))}
              </span>
            )}
            {d?.release && (
              <span>
                Helm release{' '}
                <a className="rk-link rk-mono" href={href('helm', [ns, d.release])} style={{ fontSize: 12 }} data-testid="object-release-link">
                  {d.release}
                </a>
              </span>
            )}
            {workload && d && (
              <span>
                Pods{' '}
                <span className="rk-t rk-num" data-tone={failing ? 'err' : pods.length && ready < pods.length ? 'warn' : pods.length ? 'ok' : 'muted'} style={{ fontWeight: 500 }}>
                  {pods.length ? `${ready}/${pods.length} ready` : 'none'}
                </span>
              </span>
            )}
          </>
        )
      }
      tabs={tabs}
      tab={tab}
      onTab={(t) => setTab(t as DrawerTabId)}
      onClose={onClose}
      notice={
        deleted && (
          <div className="rk-callout" data-tone="warn" style={{ margin: '12px 20px 0', borderRadius: 8 }} data-testid="object-deleted">
            <WarnIcon />
            <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
              <strong style={{ fontSize: 12.5, fontWeight: 600 }}>This {kind} was deleted</strong>
              <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>{d ? 'What is shown is the last state 221B saw.' : 'It is gone from the cluster, and nothing more is known about it.'}</span>
            </div>
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
      {d && tab === 'details' && (
        <>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(390px, 100%), 1fr))', gap: 16, alignItems: 'start' }}>
            <Card title={kind}>
              <Facts items={facts} />
            </Card>
            {(d.conditions.length > 0 || workload) && (
              <Card title="Conditions">
                <div style={{ display: 'flex', flexDirection: 'column' }}>
                  {d.conditions.map((c) => (
                    <div key={c.type} style={{ display: 'flex', alignItems: 'flex-start', gap: 10, padding: '6px 0', borderBottom: '1px solid var(--line)', fontSize: 12.5 }}>
                      <Tone tone={c.status === 'True' ? 'ok' : c.status === 'False' ? 'warn' : 'muted'} text={c.status} />
                      <span style={{ flex: '1 1 auto', minWidth: 0, display: 'flex', flexDirection: 'column', gap: 2 }}>
                        <span className="rk-mono">
                          {c.type}
                          {c.reason && <span style={{ color: 'var(--fg3)' }}> · {c.reason}</span>}
                        </span>
                        {c.message && <span style={{ fontSize: 12, color: 'var(--fg2)', overflowWrap: 'anywhere' }}>{c.message}</span>}
                      </span>
                      <span className="rk-num" style={{ color: 'var(--fg3)', fontSize: 12, whiteSpace: 'nowrap' }} title={c.since ? fmtTs(c.since) : undefined}>
                        {c.since ? fmtAge(c.since, now) + ' ago' : ''}
                      </span>
                    </div>
                  ))}
                  {d.conditions.length === 0 && <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>None reported</span>}
                </div>
              </Card>
            )}
          </div>
          {d.keys && (
            <Card title={`Keys · ${nWord(d.keys.length, 'key', 'keys')}`} pad={false} label="Keys">
              {d.keys.length === 0 ? (
                <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 12.5, color: 'var(--fg3)' }}>No data.</p>
              ) : (
                <ScrollBox label="Keys">
                  <Table label="Keys" template="minmax(200px, 1fr) 110px" minWidth={310} cols={[{ label: 'Key' }, { label: 'Size', right: true }]}>
                    {d.keys.map((k) => (
                      <TR key={k.key}>
                        <span className="rk-mono" style={{ fontSize: 12 }}>
                          {k.key}
                        </span>
                        <span className="rk-r rk-num">{fBytes(k.bytes)}</span>
                      </TR>
                    ))}
                  </Table>
                </ScrollBox>
              )}
              <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 11.5, color: 'var(--fg3)' }} data-testid="keys-note">
                {kind === 'Secret' ? '221B shows a Secret’s keys and their sizes, never their values (X-01); the YAML tab hides them too.' : 'Keys and their sizes; the YAML tab has the contents.'}
              </p>
            </Card>
          )}
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(390px, 100%), 1fr))', gap: 16, alignItems: 'start' }}>
            <Card title="Labels">
              <Chips map={d.labels} />
            </Card>
            <Card title="Annotations">
              {Object.keys(d.annotations).length === 0 ? (
                <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>None</p>
              ) : (
                <dl className="rk-kv" style={{ gridTemplateColumns: 'minmax(0, 1fr)', gap: 2 }}>
                  {Object.keys(d.annotations)
                    .sort()
                    .map((k) => (
                      <div key={k} style={{ display: 'contents' }}>
                        <dt className="rk-mono" style={{ fontSize: 11.5, marginTop: 6 }}>
                          {k}
                        </dt>
                        <dd className="rk-mono" style={{ fontSize: 12, whiteSpace: 'pre-wrap' }}>
                          {d.annotations[k]}
                        </dd>
                      </div>
                    ))}
                </dl>
              )}
            </Card>
          </div>
        </>
      )}
      {d && tab === 'pods' && (
        <>
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
            <span style={{ fontSize: 12, color: 'var(--fg3)' }}>
              {pods.length ? `${nWord(pods.length, 'pod', 'pods')} controlled by this ${kind}` : `No pod is controlled by this ${kind}`}
              {pods.length ? ` · ${ready} ready${failing ? ` · ${failing} failing` : ''}` : ''}
            </span>
            <SearchBox rows={pods} terms={podTerms} value={podTable.query} onChange={podTable.setQuery} placeholder="Search pods" label={`Search the pods of ${name}`} width={220} />
            <div className="rk-tools" style={{ marginLeft: 'auto', gap: 8 }}>
              <ColumnsMenu cols={POD_COLUMNS} hidden={podTable.hidden} defaults={podTable.defaults} onChange={podTable.setHidden} compact={false} />
              <ExportMenu onExport={(f) => podTable.exportRows(f, cluster?.name, `${name}-pods`, onToast)} disabled={!podTable.shown.length} what="the pods in view" />
            </div>
          </div>
          <DataTable<PodView> label={`Pods of ${name}`} rows={podTable.shown} cols={podTable.cols} rowKey={(r) => r.key} rowLabel={podLabel} sort={podTable.sort} onSort={podTable.onSort} onOpen={(k) => navigate(linkTo('Pod', k.split('/')[1] ?? k))} now={now} empty={pods.length ? 'No pod matches.' : `No pod is controlled by this ${kind}${kind === 'CronJob' ? ': its Jobs create pods when they run' : ''}.`} rowAttr="pod" />
          <p style={{ margin: 0, fontSize: 11.5, color: 'var(--fg3)' }}>Pods whose owners lead to this {kind}, through its ReplicaSets or Jobs. Click a pod for its containers, events and logs.</p>
        </>
      )}
      {d && tab === 'events' && <EventsTable events={d.events} now={now} />}
      {tab === 'yaml' && !deleted && <YamlView kind={kindName} namespace={ns} name={name} onToast={onToast} />}
      {tab === 'yaml' && deleted && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>The {kind} is gone, so there is no YAML to read.</p>}
    </Drawer>
  )
}
