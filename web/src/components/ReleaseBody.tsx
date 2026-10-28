import { useMemo, useState, type ReactNode } from 'react'
import type { ClusterInfo, PodView, ReleaseDetails, ReleaseResource, ReleaseRow, Revision } from '../api/types'
import { RESOURCE_COLUMNS, REVISION_COLUMNS, resourceLabel, resourceTerms, revisionTerms } from '../columns/objects'
import { POD_COLUMNS, podLabel, podStatusesShown, podStatusGroup, podStatusZero, podTerms, type PodStatusFilter } from '../columns/pods'
import { fCores, fMem, nWord, pct, pctTxt } from '../lib/format'
import { isWatchedKind, pluralKind } from '../lib/kinds'
import { peekHref } from '../lib/peek'
import { href, navigate, withParams, type Route } from '../lib/router'
import { useTable } from '../lib/useTable'
import { ColumnsMenu } from './ColumnsMenu'
import { DataTable } from './DataTable'
import { Card, Drawer } from './Drawer'
import { ExportMenu } from './ExportMenu'
import { WarnIcon } from './Icons'
import { ObjectPanel } from './ObjectPanel'
import { PodPanel } from './PodPanel'
import { SearchBox } from './SearchBox'
import { ServicePanel } from './ServicePanel'
import { colorLine } from './YamlView'

export const SUBTABS = ['pods', 'workloads', 'storage', 'network', 'roles', 'other', 'values', 'notes', 'history'] as const
export type SubTab = (typeof SUBTABS)[number]

const WORKLOADS = new Set(['Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet', 'Job', 'CronJob', 'Pod', 'HorizontalPodAutoscaler', 'PodDisruptionBudget'])
const STORAGE = new Set(['PersistentVolumeClaim', 'PersistentVolume', 'ConfigMap', 'Secret', 'StorageClass'])
const NETWORK = new Set(['Service', 'Ingress', 'NetworkPolicy', 'EndpointSlice', 'Endpoints', 'IngressClass'])
const ROLES = new Set(['Role', 'ClusterRole', 'RoleBinding', 'ClusterRoleBinding'])

function group(r: ReleaseResource): SubTab {
  if (WORKLOADS.has(r.kind)) return 'workloads'
  if (STORAGE.has(r.kind)) return 'storage'
  if (NETWORK.has(r.kind)) return 'network'
  if (ROLES.has(r.kind)) return 'roles'
  return 'other'
}

const COLOR: Record<string, string> = { k: 'var(--y-key)', s: 'var(--y-str)', n: 'var(--y-num)', b: 'var(--y-bool)', p: 'var(--y-punc)' }

/** A tab a page adds before or after the release's own. */
export interface ExtraTab {
  id: string
  label: string
  count?: number
  render: () => ReactNode
}

interface Props {
  cluster: ClusterInfo | null
  now: number
  onToast: (msg: string) => void
  route: Route
  current: ReleaseRow
  d: ReleaseDetails | null
  error: string
  /** The open section; the page keeps it in ?tab=. */
  sub: string
  onSub: (id: string) => void
  /** Tabs the page adds before and after the release's own (Overview, Images). */
  before?: ExtraTab[]
  after?: ExtraTab[]
  /** What the page shows above a section's own content (the values file above the deployed values). */
  above?: Partial<Record<SubTab, ReactNode>>
}

/**
 * What a release shows, on the Helm tab (P2-06, HA-14): its
 * failed and not-ready callouts, the CPU, memory, pod and storage cards,
 * and its sections: pods, the objects by kind, values, notes and history,
 * each object opening its drawer in place (?open=Kind/namespace/name).
 */
export function ReleaseBody({ cluster, now, onToast, route, current, d, error, sub, onSub, before = [], after = [], above = {} }: Props) {
  const p = route.params
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
    const c = podStatusZero(pods.length)
    for (const r of pods) c[podStatusGroup(r)]++
    return c
  }, [pods])
  const podsShown = useMemo(() => (podStatus === 'all' ? pods : pods.filter((r) => podStatusGroup(r) === podStatus)), [pods, podStatus])
  const podTable = useTable<PodView>({ id: 'helm-pods', columns: POD_COLUMNS, rows: podsShown, terms: podTerms, now, compact: false, defaultSort: { col: 'name', dir: 'asc' } })
  const resources = useMemo(() => (d ? d.resources.filter((r) => group(r) === sub) : []), [d, sub])
  const resTable = useTable<ReleaseResource>({ id: 'helm-resources', columns: RESOURCE_COLUMNS, rows: resources, terms: resourceTerms, now, defaultSort: { col: 'kind', dir: 'asc' } })
  const history = useMemo(() => d?.history ?? [], [d])
  const histTable = useTable<Revision>({ id: 'helm-history', columns: REVISION_COLUMNS, rows: history, terms: revisionTerms, now, defaultSort: { col: 'revision', dir: 'desc' } })
  // The open drawer, from the URL: open=Kind/namespace/name.
  const open = useMemo<{ kind: string; namespace: string; name: string } | null>(() => {
    const v = p.get('open')
    if (!v) return null
    const [kind = '', namespace = '', name = ''] = v.split('/', 3) as [string?, string?, string?]
    return kind && name ? { kind, namespace, name } : null
  }, [p])
  const openKey = (kind: string, namespace: string, name: string) => withParams(route, { open: `${kind}/${namespace}/${name}` })
  const openObject = (kind: string, namespace: string, name: string) => {
    // Cluster-scoped kinds have tabs of their own.
    if (kind === 'Node') navigate(peekHref({ kind: 'node', name }))
    else if (kind === 'Namespace') navigate(href('namespaces', [name]))
    else navigate(openKey(kind, namespace, name), true)
  }
  const openPodKey = open?.kind === 'Pod' ? `${open.namespace}/${open.name}` : null
  const openResKey = open && open.kind !== 'Pod' ? `${open.kind}/${open.namespace}/${open.name}` : null
  const openRes = openResKey ? (d?.resources.find((r) => `${r.kind}/${r.namespace}/${r.name}` === openResKey) ?? null) : null
  const close = () => navigate(withParams(route, { open: undefined }), true)
  const counts: Partial<Record<string, number>> = { pods: pods.length, history: history.length }
  for (const r of d?.resources ?? []) counts[group(r)] = (counts[group(r)] ?? 0) + 1
  for (const t of [...before, ...after]) if (t.count !== undefined) counts[t.id] = t.count
  const subtabs: [string, string][] = [...before.map((t) => [t.id, t.label] as [string, string]), ['pods', 'Pods'], ['workloads', 'Workloads'], ['storage', 'Storage'], ['network', 'Network'], ['roles', 'Roles'], ['other', 'Other'], ['values', 'Values'], ['notes', 'Notes'], ['history', 'History'], ...after.map((t) => [t.id, t.label] as [string, string])]
  const valueLines = useMemo(() => (d?.values ?? '').replace(/\n$/, '').split('\n'), [d])
  const extraTab = [...before, ...after].find((t) => t.id === sub)

  return (
    <>
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
          <button key={id} className="rk-tab" aria-current={sub === id ? 'page' : undefined} onClick={() => onSub(id)} style={{ height: 38 }} data-subtab={id}>
            {label}
            {counts[id] !== undefined && counts[id] !== 0 && <span className="rk-count">{counts[id]}</span>}
          </button>
        ))}
      </nav>
      {extraTab && extraTab.render()}
      {!extraTab && !d && !error && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>Loading…</p>}
      {!extraTab && error && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }}>
          {error}
        </p>
      )}
      {above[sub as SubTab]}
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
              {podStatusesShown(podCounts, podStatus).map(([id, label]) => (
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
          <DataTable<PodView> label="Pods in this release" rows={podTable.shown} cols={podTable.cols} rowKey={(r) => r.key} rowLabel={podLabel} sort={podTable.sort} onSort={podTable.onSort} selected={openPodKey} onOpen={(k) => openObject('Pod', k.split('/')[0] ?? current.namespace, k.split('/')[1] ?? k)} now={now} empty="No pods belong to this release." rowAttr="pod" />
        </>
      )}
      {d && (sub === 'workloads' || sub === 'storage' || sub === 'network' || sub === 'roles' || sub === 'other') && (
        <>
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
            <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{nWord(resources.length, 'object', 'objects')} from the release manifest · click one for its details</span>
            <SearchBox rows={resources} terms={resourceTerms} value={resTable.query} onChange={resTable.setQuery} placeholder="Search" label="Search resources" width={200} />
            <div className="rk-tools" style={{ marginLeft: 'auto', gap: 8 }}>
              <ColumnsMenu cols={RESOURCE_COLUMNS} hidden={resTable.hidden} defaults={resTable.defaults} onChange={resTable.setHidden} compact={false} />
              <ExportMenu onExport={(f) => resTable.exportRows(f, cluster?.name, `${current.name}-${sub}`, onToast)} disabled={!resTable.shown.length} what="these objects" />
            </div>
          </div>
          <DataTable<ReleaseResource>
            label={`${sub} of the release`}
            rows={resTable.shown}
            cols={resTable.cols}
            rowKey={(r) => `${r.kind}/${r.namespace}/${r.name}`}
            rowLabel={resourceLabel}
            sort={resTable.sort}
            onSort={resTable.onSort}
            selected={openResKey}
            onOpen={(k) => {
              const r = resources.find((x) => `${x.kind}/${x.namespace}/${x.name}` === k)
              if (r) openObject(r.kind, r.namespace || current.namespace, r.name)
            }}
            now={now}
            empty="The manifest has no objects of these kinds."
            rowAttr="resource"
          />
        </>
      )}
      {d && sub === 'values' && (
        <Card title={above.values ? 'Values as deployed' : 'User-supplied values'} aside={<span style={{ fontSize: 12, color: 'var(--fg3)' }}>{d.maskedValues.length ? `${nWord(d.maskedValues.length, 'value', 'values')} that look like credentials masked: ${d.maskedValues.join(', ')}` : 'what helm get values shows'}</span>}>
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
          <p style={{ margin: 0, fontSize: 11.5, color: 'var(--fg3)' }} data-testid="values-note">
            The values given at install or upgrade: every values file and --set, merged into one as Helm merged them, which is what helm get values shows. The chart's own defaults are not part of it.
          </p>
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
      {open && open.kind === 'Pod' && <PodPanel podKey={`${open.namespace}/${open.name}`} row={pods.find((r) => r.key === `${open.namespace}/${open.name}`) ?? null} gone={false} cluster={cluster} now={now} onClose={close} onToast={onToast} />}
      {open && open.kind === 'Service' && <ServicePanel serviceKey={`${open.namespace}/${open.name}`} row={null} gone={false} now={now} onClose={close} onToast={onToast} />}
      {open && open.kind !== 'Pod' && open.kind !== 'Service' && (openRes ? openRes.watched : isWatchedKind(open.kind)) && (
        <ObjectPanel kind={open.kind} namespace={open.namespace} name={open.name} row={null} gone={false} cluster={cluster} now={now} linkTo={(k, n) => openKey(k, open.namespace, n)} onClose={close} onToast={onToast} />
      )}
      {open && open.kind !== 'Pod' && open.kind !== 'Service' && !(openRes ? openRes.watched : isWatchedKind(open.kind)) && (
        <Drawer label={`${open.kind} details`} title={open.name} kicker={<span>{open.kind}{open.namespace ? ` in ${open.namespace}` : ''}</span>} onClose={close} width={860} testId="object-panel">
          <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>221B does not watch {pluralKind(open.kind)}, so it cannot read this object. The release manifest lists it{openRes?.status ? ` as ${openRes.status}` : ''}.</p>
        </Drawer>
      )}
    </>
  )
}

export function StatCard({ title, head, big, unit, pctUse, tone = 'ok', lines }: { title: string; head: string; big: string; unit: string; pctUse: number; tone?: 'ok' | 'warn' | 'err'; lines: [string, string][] }) {
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

/** The side list's items for releases, for the Helm tab's side list. */
export function releaseItem(r: ReleaseRow, current: boolean, hrefOf: (r: ReleaseRow) => string, now: number, fmtAge: (iso: string | undefined, now: number) => string) {
  return {
    key: r.key,
    href: hrefOf(r),
    current,
    name: r.name,
    copy: 'release name',
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
  }
}
