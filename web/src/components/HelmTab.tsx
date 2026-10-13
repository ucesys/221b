import { useCallback, useMemo, useState } from 'react'
import { useDetails } from '../api/details'
import { useView } from '../api/live'
import type { PodView, ReleaseDetails, ReleaseResource, ReleaseRow, Revision } from '../api/types'
import { RESOURCE_COLUMNS, REVISION_COLUMNS, resourceLabel, resourceTerms, revisionTerms } from '../columns/objects'
import { POD_COLUMNS, podLabel, podStatusGroup, podTerms, type PodStatusFilter } from '../columns/pods'
import { fCores, fMem, fmtAge, fmtTs, nWord, pct, pctTxt } from '../lib/format'
import { href, navigate, withParams } from '../lib/router'
import { useTable } from '../lib/useTable'
import { ColumnsMenu } from './ColumnsMenu'
import { DataTable } from './DataTable'
import { Card, Drawer } from './Drawer'
import { ExportMenu } from './ExportMenu'
import { WarnIcon } from './Icons'
import { SearchBox } from './SearchBox'
import { SideList, type SideGroup } from './SideList'
import type { TabProps } from './tabs'
import { YamlView } from './YamlView'
import { colorLine } from './YamlView'

type ReleaseFilter = 'all' | 'deployed' | 'failed'
const SUBTABS = ['pods', 'workloads', 'storage', 'network', 'other', 'values', 'notes', 'history'] as const
type SubTab = (typeof SUBTABS)[number]

const WORKLOADS = new Set(['Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet', 'Job', 'CronJob', 'Pod', 'HorizontalPodAutoscaler', 'PodDisruptionBudget'])
const STORAGE = new Set(['PersistentVolumeClaim', 'PersistentVolume', 'ConfigMap', 'Secret', 'StorageClass'])
const NETWORK = new Set(['Service', 'Ingress', 'NetworkPolicy', 'EndpointSlice', 'Endpoints', 'IngressClass'])

function group(r: ReleaseResource): SubTab {
  if (WORKLOADS.has(r.kind)) return 'workloads'
  if (STORAGE.has(r.kind)) return 'storage'
  if (NETWORK.has(r.kind)) return 'network'
  return 'other'
}

const COLOR: Record<string, string> = { k: 'var(--y-key)', s: 'var(--y-str)', n: 'var(--y-num)', b: 'var(--y-bool)', p: 'var(--y-punc)' }

/** The Helm tab (P2-06): releases on the left, the chosen one on the right with its pods, resources, values, notes and history. */
export function HelmTab({ cluster, now, onToast, route }: TabProps) {
  const { rows: releases, loaded } = useView<ReleaseRow>('helm')
  const p = route.params
  const [filter, setFilter] = useState<ReleaseFilter>('all')
  const [listQuery, setListQuery] = useState('')
  const [addons, setAddons] = useState(true)
  const sorted = useMemo(() => [...releases].sort((a, b) => a.name.localeCompare(b.name) || a.namespace.localeCompare(b.namespace)), [releases])
  const [wantNs, wantName] = route.path
  const current = sorted.find((r) => r.namespace === wantNs && r.name === wantName) ?? sorted.find((r) => r.group === 'apps') ?? sorted[0] ?? null
  const sub: SubTab = SUBTABS.includes(p.get('tab') as SubTab) ? (p.get('tab') as SubTab) : 'pods'
  const set = useCallback((changes: Record<string, string | string[] | undefined>) => navigate(withParams(route, changes), true), [route])
  const details = useDetails<ReleaseDetails>(current ? `/api/v1/helm/${encodeURIComponent(current.namespace)}/${encodeURIComponent(current.name)}` : null)
  const d = details.data

  const lq = listQuery.trim().toLowerCase()
  const listed = sorted.filter((r) => (filter === 'all' || (filter === 'deployed' ? r.status === 'deployed' : r.status === 'failed')) && (addons || r.group !== 'addons') && (!lq || r.name.includes(lq) || r.chart.includes(lq) || r.namespace.includes(lq)))
  const item = (r: ReleaseRow) => ({
    key: r.key,
    href: href('helm', [r.namespace, r.name]),
    current: current?.key === r.key,
    name: r.name,
    right: (
      <span className="rk-t" data-tone={r.tone} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 11.5, fontWeight: 500, whiteSpace: 'nowrap' }}>
        <span className="rk-dot" />
        {r.status}
      </span>
    ),
    sub: (
      <>
        <span className="rk-mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }}>
          {r.chart}-{r.chartVersion}
        </span>
        <span style={{ flex: '1 1 auto' }} />
        <span className="rk-t rk-num" data-tone={r.podsFailed ? 'err' : r.pods && r.podsReady < r.pods ? 'warn' : r.pods ? 'ok' : 'muted'} style={{ whiteSpace: 'nowrap' }}>
          {r.pods ? `${r.podsReady}/${r.pods} ready` : 'no pods'}
        </span>
      </>
    ),
    sub2: `${r.namespace} · rev ${r.revision} · ${r.updated ? fmtAge(r.updated, now) + ' ago' : ''}`,
  })
  const groups: SideGroup[] = [
    { title: 'Applications', items: listed.filter((r) => r.group === 'apps').map(item) },
    { title: 'Cluster add-ons', items: listed.filter((r) => r.group === 'addons').map(item) },
  ]
  const segs: [ReleaseFilter, string, number][] = [
    ['all', 'All', releases.length],
    ['deployed', 'Deployed', releases.filter((r) => r.status === 'deployed').length],
    ['failed', 'Failed', releases.filter((r) => r.status === 'failed').length],
  ]

  // The release's pods with their usage, for the Pods sub-tab and the cards.
  const pods = useMemo<PodView[]>(() => (d ? d.podRows.map((r) => ({ ...r, usage: d.usage?.[r.key] ?? null })) : []), [d])
  const live = useMemo(() => pods.filter((r) => !r.done), [pods])
  const notReady = useMemo(() => live.filter((r) => podStatusGroup(r) !== 'running' || r.tone !== 'ok'), [live])
  const sums = useMemo(() => {
    let cpuReq = 0
    let cpuUse = 0
    let memReq = 0
    let memUse = 0
    let cpuLim = 0
    let memLim = 0
    let usageN = 0
    for (const r of live) {
      cpuReq += r.cpuRequest
      memReq += r.memoryRequest
      cpuLim += r.cpuLimit
      memLim += r.memoryLimit
      if (r.usage) {
        cpuUse += r.usage.cpuMilli
        memUse += r.usage.memoryBytes
        usageN++
      }
    }
    return { cpuReq, cpuUse, memReq, memUse, cpuLim, memLim, usageN }
  }, [live])
  const claims = d ? d.resources.filter((r) => r.kind === 'PersistentVolumeClaim') : []
  const [podStatus, setPodStatus] = useState<PodStatusFilter>('all')
  const podCounts = useMemo(() => {
    const c: Record<PodStatusFilter, number> = { all: pods.length, running: 0, pending: 0, failing: 0, completed: 0 }
    for (const r of pods) c[podStatusGroup(r)]++
    return c
  }, [pods])
  const podsShown = useMemo(() => (podStatus === 'all' ? pods : pods.filter((r) => podStatusGroup(r) === podStatus)), [pods, podStatus])
  const podTable = useTable<PodView>({ id: 'helm-pods', columns: POD_COLUMNS, rows: podsShown, terms: podTerms, now, defaultSort: { col: 'name', dir: 'asc' } })
  const resources = useMemo(() => (d ? d.resources.filter((r) => group(r) === sub) : []), [d, sub])
  const resTable = useTable<ReleaseResource>({ id: 'helm-resources', columns: RESOURCE_COLUMNS, rows: resources, terms: resourceTerms, now, defaultSort: { col: 'kind', dir: 'asc' } })
  const history = useMemo(() => d?.history ?? [], [d])
  const histTable = useTable<Revision>({ id: 'helm-history', columns: REVISION_COLUMNS, rows: history, terms: revisionTerms, now, defaultSort: { col: 'revision', dir: 'desc' } })
  const [openPod, setOpenPod] = useState<string | null>(null)
  const [openRes, setOpenRes] = useState<ReleaseResource | null>(null)
  const counts: Partial<Record<SubTab, number>> = { pods: pods.length, history: history.length }
  for (const r of d?.resources ?? []) counts[group(r)] = (counts[group(r)] ?? 0) + 1
  const subtabs: [SubTab, string][] = [
    ['pods', 'Pods'],
    ['workloads', 'Workloads'],
    ['storage', 'Storage'],
    ['network', 'Network'],
    ['other', 'Other'],
    ['values', 'Values'],
    ['notes', 'Notes'],
    ['history', 'History'],
  ]
  const valueLines = useMemo(() => (d?.values ?? '').replace(/\n$/, '').split('\n'), [d])

  return (
    <>
      <SideList
        label="Helm releases"
        title="Helm releases"
        aside={
          <div className="rk-seg" role="group" aria-label="Releases by status">
            {segs.map(([id, label, n]) => (
              <button key={id} aria-pressed={filter === id} onClick={() => setFilter(id)}>
                {label} <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{n}</span>
              </button>
            ))}
          </div>
        }
        query={listQuery}
        onQuery={setListQuery}
        placeholder="Filter by release, chart or namespace"
        extra={
          <label className="rk-hit" style={{ display: 'inline-flex', alignItems: 'center', gap: 8, cursor: 'pointer', fontSize: 12, color: 'var(--fg2)' }}>
            <input type="checkbox" checked={addons} onChange={(e) => setAddons(e.target.checked)} style={{ width: 15, height: 15, margin: 0, accentColor: 'var(--accent)', cursor: 'pointer' }} />
            <span>Show cluster add-ons</span>
          </label>
        }
        groups={groups}
        empty={loaded ? (releases.length ? 'No releases match this filter.' : 'No Helm releases in this cluster.') : 'Waiting for the first list…'}
        testId="release-list"
      />
      <main className="rk-pane" aria-label="Helm release" style={{ flex: '999 1 560px', minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        {current && (
          <section aria-label={`Release ${current.name}`} style={{ padding: '18px 24px 28px', display: 'flex', flexDirection: 'column', gap: 14 }}>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 12px' }}>
              <h2 className="rk-mono" style={{ margin: 0, fontSize: 18, fontWeight: 600 }} data-testid="release-name">
                {current.name}
              </h2>
              <span className="rk-pill" data-tone={current.tone} title={current.description}>
                <span className="rk-dot" />
                {current.status}
              </span>
              <span className="rk-tag" style={{ fontSize: 11, padding: '2px 7px' }}>
                revision {current.revision}
              </span>
            </div>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 18px', fontSize: 12.5, color: 'var(--fg3)' }} data-testid="release-facts">
              <span>
                Namespace{' '}
                <a className="rk-link rk-mono" href={href('namespaces', [current.namespace])} style={{ fontSize: 12.5 }}>
                  {current.namespace}
                </a>
              </span>
              <span>
                Chart <span className="rk-mono" style={{ color: 'var(--fg)' }}>{current.chart}-{current.chartVersion}</span>
              </span>
              <span>
                App version <span className="rk-mono" style={{ color: 'var(--fg)' }}>{current.appVersion || '—'}</span>
              </span>
              <span>
                Updated <span className="rk-num" style={{ color: 'var(--fg)' }}>{current.updated ? `${fmtAge(current.updated, now)} ago · ${fmtTs(current.updated)}` : '—'}</span>
              </span>
              <span>
                Managed by <span className="rk-mono" style={{ color: 'var(--fg)' }}>Helm</span>
              </span>
            </div>
            {current.status === 'failed' && (
              <div className="rk-callout" data-tone="err">
                <WarnIcon />
                <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
                  <strong style={{ fontSize: 12.5, fontWeight: 600 }}>The last upgrade failed</strong>
                  <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>{current.description}. The release runs whatever the previous revision left; see History.</span>
                </div>
              </div>
            )}
            {d && notReady.length > 0 && (
              <div className="rk-callout" data-tone="warn" data-testid="release-warning">
                <WarnIcon />
                <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
                  <strong style={{ fontSize: 12.5, fontWeight: 600 }}>
                    {notReady.length} of {nWord(live.length, 'pod', 'pods')} not ready
                  </strong>
                  <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>{notReady.map((r) => `${r.name} is ${r.status}${r.statusTip ? `: ${r.statusTip}` : ''}`).join(' · ')}</span>
                </div>
              </div>
            )}
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 12 }} data-testid="release-cards">
              <StatCard title="CPU" head={`requests ${sums.cpuReq ? fCores(sums.cpuReq) + ' cores' : '—'}`} big={sums.usageN ? fCores(sums.cpuUse) : '—'} unit="cores in use" pctUse={pct(sums.cpuUse, sums.cpuReq)} lines={[['Requested', sums.cpuReq ? fCores(sums.cpuReq) : '—'], ['Used', sums.usageN ? `${fCores(sums.cpuUse)} ${sums.cpuReq ? pctTxt(pct(sums.cpuUse, sums.cpuReq)) : ''}` : '—'], ['Limits', sums.cpuLim ? fCores(sums.cpuLim) : 'none']]} />
              <StatCard title="Memory" head={`requests ${sums.memReq ? fMem(sums.memReq) : '—'}`} big={sums.usageN ? fMem(sums.memUse) : '—'} unit="in use" pctUse={pct(sums.memUse, sums.memReq)} lines={[['Requested', sums.memReq ? fMem(sums.memReq) : '—'], ['Used', sums.usageN ? `${fMem(sums.memUse)} ${sums.memReq ? pctTxt(pct(sums.memUse, sums.memReq)) : ''}` : '—'], ['Limits', sums.memLim ? fMem(sums.memLim) : 'none']]} />
              <StatCard title="Pods" head={`${live.length} expected`} big={`${live.length - notReady.length}/${live.length}`} unit="ready" pctUse={pct(live.length - notReady.length, live.length)} tone={notReady.some((r) => r.tone === 'err') ? 'err' : notReady.length ? 'warn' : 'ok'} lines={[['Ready', String(live.length - notReady.length)], ['Pending', String(notReady.filter((r) => r.tone !== 'err').length)], ['Failing', String(notReady.filter((r) => r.tone === 'err').length)]]} />
              <StatCard title="Storage" head={nWord(claims.length, 'claim', 'claims')} big={String(claims.length)} unit={claims.length === 1 ? 'volume claim' : 'volume claims'} pctUse={0} lines={[['Bound', String(claims.filter((c) => c.status === 'Bound').length)], ['Pending', String(claims.filter((c) => c.status === 'Pending').length)], ['Not watched', String(claims.filter((c) => !c.watched).length)]]} />
            </div>
            <nav aria-label="Release sections" style={{ display: 'flex', flexWrap: 'wrap', gap: '0 20px', borderBottom: '1px solid var(--line)' }}>
              {subtabs.map(([id, label]) => (
                <button key={id} className="rk-tab" aria-current={sub === id ? 'page' : undefined} onClick={() => set({ tab: id === 'pods' ? undefined : id })} style={{ height: 38 }} data-subtab={id}>
                  {label}
                  {counts[id] !== undefined && counts[id] !== 0 && <span className="rk-count">{counts[id]}</span>}
                </button>
              ))}
            </nav>
            {!d && !details.error && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>Loading…</p>}
            {details.error && (
              <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }}>
                {details.error}
              </p>
            )}
            {d && sub === 'pods' && (
              <>
                <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
                  <h3 className="rk-h" style={{ fontSize: 12 }}>
                    Pods in this release
                  </h3>
                  <span className="rk-num" style={{ fontSize: 12, color: 'var(--fg3)' }}>
                    {nWord(pods.length, 'pod', 'pods')}
                  </span>
                  <div className="rk-seg" role="group" aria-label="Pods by status" style={{ marginLeft: 'auto' }}>
                    {(
                      [
                        ['all', 'All'],
                        ['running', 'Running'],
                        ['pending', 'Pending'],
                        ['failing', 'Failing'],
                        ['completed', 'Completed'],
                      ] as [PodStatusFilter, string][]
                    )
                      .filter(([id]) => id === 'all' || podCounts[id] > 0)
                      .map(([id, label]) => (
                        <button key={id} aria-pressed={podStatus === id} onClick={() => setPodStatus(id)}>
                          {label} <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{podCounts[id]}</span>
                        </button>
                      ))}
                  </div>
                  <SearchBox rows={podsShown} terms={podTerms} value={podTable.query} onChange={podTable.setQuery} placeholder="Search pods" label="Search the release's pods" width={220} />
                  <div className="rk-tools" style={{ gap: 8 }}>
                    <ColumnsMenu cols={POD_COLUMNS} hidden={podTable.hidden} defaults={podTable.defaults} onChange={podTable.setHidden} compact={false} />
                    <ExportMenu onExport={(f) => podTable.exportRows(f, cluster?.name, `${current.name}-pods`, onToast)} disabled={!podTable.shown.length} what="the pods in view" />
                  </div>
                </div>
                <DataTable<PodView> label="Pods in this release" rows={podTable.shown} cols={podTable.cols} rowKey={(r) => r.key} rowLabel={podLabel} sort={podTable.sort} onSort={podTable.onSort} selected={openPod} onOpen={(k) => setOpenPod(k)} now={now} empty="No pods belong to this release." rowAttr="pod" />
                <p style={{ margin: 0, fontSize: 11.5, color: 'var(--fg3)' }}>Pods are matched by the labels Helm charts set (app.kubernetes.io/instance and managed-by) and the release annotation. Usage comes from metrics-server.</p>
              </>
            )}
            {d && (sub === 'workloads' || sub === 'storage' || sub === 'network' || sub === 'other') && (
              <>
                <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
                  <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{nWord(resources.length, 'object', 'objects')} from the release manifest · click one for its YAML</span>
                  <SearchBox rows={resources} terms={resourceTerms} value={resTable.query} onChange={resTable.setQuery} placeholder="Search" label="Search resources" width={200} />
                  <div className="rk-tools" style={{ marginLeft: 'auto', gap: 8 }}>
                    <ColumnsMenu cols={RESOURCE_COLUMNS} hidden={resTable.hidden} defaults={resTable.defaults} onChange={resTable.setHidden} compact={false} />
                    <ExportMenu onExport={(f) => resTable.exportRows(f, cluster?.name, `${current.name}-${sub}`, onToast)} disabled={!resTable.shown.length} what="these objects" />
                  </div>
                </div>
                <DataTable<ReleaseResource> label={`${sub} of the release`} rows={resTable.shown} cols={resTable.cols} rowKey={(r) => `${r.kind}/${r.namespace}/${r.name}`} rowLabel={resourceLabel} sort={resTable.sort} onSort={resTable.onSort} selected={openRes ? `${openRes.kind}/${openRes.namespace}/${openRes.name}` : null} onOpen={(k) => setOpenRes(resources.find((r) => `${r.kind}/${r.namespace}/${r.name}` === k) ?? null)} now={now} empty="The manifest has no objects of these kinds." rowAttr="resource" />
              </>
            )}
            {d && sub === 'values' && (
              <Card title="User-supplied values" aside={<span style={{ fontSize: 12, color: 'var(--fg3)' }}>{d.maskedValues.length ? `${nWord(d.maskedValues.length, 'value', 'values')} that look like credentials masked: ${d.maskedValues.join(', ')}` : 'what helm get values shows'}</span>}>
                {d.values ? (
                  <div className="rk-code" role="region" aria-label="Values" data-testid="release-values" tabIndex={0}>
                    {valueLines.map((l, i) => (
                      <div key={i} className="rk-ln">
                        <i>{i + 1}</i>
                        {colorLine(l).map((s, j) => (
                          <span key={j} style={{ color: COLOR[s[1]] }}>
                            {s[0]}
                          </span>
                        ))}
                      </div>
                    ))}
                  </div>
                ) : (
                  <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>This release was installed with the chart's defaults: no user-supplied values.</p>
                )}
              </Card>
            )}
            {d && sub === 'notes' && (
              <Card title="Notes" aside={<span style={{ fontSize: 12, color: 'var(--fg3)' }}>what the chart printed after the last install or upgrade</span>}>
                {d.notes ? (
                  <pre className="rk-mono" style={{ margin: 0, padding: '10px 12px', borderRadius: 8, background: 'var(--sunken)', fontSize: 12, lineHeight: 1.6, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }} data-testid="release-notes">
                    {d.notes}
                  </pre>
                ) : (
                  <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>The chart has no notes.</p>
                )}
              </Card>
            )}
            {d && sub === 'history' && (
              <>
                <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
                  <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{nWord(history.length, 'revision', 'revisions')}, as helm history lists them</span>
                  <div className="rk-tools" style={{ marginLeft: 'auto', gap: 8 }}>
                    <ColumnsMenu cols={REVISION_COLUMNS} hidden={histTable.hidden} defaults={histTable.defaults} onChange={histTable.setHidden} compact={false} />
                    <ExportMenu onExport={(f) => histTable.exportRows(f, cluster?.name, `${current.name}-history`, onToast)} disabled={!histTable.shown.length} what="the history" />
                  </div>
                </div>
                <DataTable<Revision> label="History" rows={histTable.shown} cols={histTable.cols} rowKey={(r) => String(r.revision)} rowLabel={(r) => `Revision ${r.revision}, ${r.status}`} sort={histTable.sort} onSort={histTable.onSort} now={now} empty="No revisions." rowAttr="revision" />
              </>
            )}
          </section>
        )}
        {!current && <p style={{ margin: 0, padding: '28px 24px', fontSize: 12.5, color: 'var(--fg3)' }}>{loaded ? 'No Helm releases: nothing in this cluster was installed with Helm, or 221B cannot read release records.' : 'Waiting for the first list of releases…'}</p>}
      </main>
      {openPod && (
        <Drawer label="Pod YAML" title={openPod.split('/')[1] ?? openPod} kicker={<span>Pod in {openPod.split('/')[0]}</span>} onClose={() => setOpenPod(null)} width={860}>
          <p style={{ margin: 0, fontSize: 12.5 }}>
            <a className="rk-link" href={href('pods', [], { open: openPod })}>
              Open in the Pods tab for containers, events and logs →
            </a>
          </p>
          <YamlView kind="pods" namespace={openPod.split('/')[0]} name={openPod.split('/')[1] ?? ''} onToast={onToast} />
        </Drawer>
      )}
      {openRes && (
        <Drawer label="Object YAML" title={openRes.name} kicker={<span>{openRes.kind}{openRes.namespace ? ` in ${openRes.namespace}` : ''}</span>} onClose={() => setOpenRes(null)} width={860}>
          {openRes.watched ? <YamlView kind={kindName(openRes.kind)} namespace={openRes.namespace || undefined} name={openRes.name} onToast={onToast} /> : <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>221B does not watch {openRes.kind}s, so it cannot read this object.</p>}
        </Drawer>
      )}
    </>
  )
}

/** The registry name of a kind: Deployment → deployments, Ingress → ingresses. */
function kindName(kind: string): string {
  const lower = kind.toLowerCase()
  if (lower.endsWith('s') || lower.endsWith('x') || lower.endsWith('ch')) return lower + 'es'
  if (lower.endsWith('y')) return lower.slice(0, -1) + 'ies'
  return lower + 's'
}

function StatCard({ title, head, big, unit, pctUse, tone = 'ok', lines }: { title: string; head: string; big: string; unit: string; pctUse: number; tone?: 'ok' | 'warn' | 'err'; lines: [string, string][] }) {
  return (
    <div className="rk-card" style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 8 }}>
        <h3 className="rk-h">{title}</h3>
        <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{head}</span>
      </div>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 6 }}>
        <span className="rk-num" style={{ fontSize: 22, fontWeight: 600 }}>
          {big}
        </span>
        <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{unit}</span>
      </div>
      <div className="rk-bar" style={{ height: 6 }}>
        <span className="rk-use" data-tone={tone === 'ok' ? undefined : tone} style={{ width: `${Math.min(100, pctUse).toFixed(1)}%` }} />
      </div>
      <div style={{ display: 'flex', gap: 14, fontSize: 11.5, color: 'var(--fg3)' }}>
        {lines.map(([k, v]) => (
          <span key={k}>
            {k} <span className="rk-num" style={{ color: 'var(--fg)' }}>{v}</span>
          </span>
        ))}
      </div>
    </div>
  )
}
