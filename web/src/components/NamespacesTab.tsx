import { useCallback, useMemo, useRef, useState } from 'react'
import { useView } from '../api/live'
import type { NamespaceRow, ObjectRow, ReleaseRow } from '../api/types'
import { OBJECT_COLUMNS, objectLabel, objectTerms } from '../columns/objects'
import { RELEASE_COLUMNS } from '../columns/releases'
import { fmtAge, fmtTs, nWord } from '../lib/format'
import { pluralKind } from '../lib/kinds'
import { href, listParam, navigate, withParams } from '../lib/router'
import { exportFileName, exportTable, csvText, matchesQuery, queryWords, sortRows, nextSort, type ExportFormat, type SortState } from '../lib/table'
import { download, xlsxBytes } from '../lib/xlsx'
import { ColumnsMenu } from './ColumnsMenu'
import { DataTable } from './DataTable'
import { ExportMenu } from './ExportMenu'
import { Chevron, FilterIcon } from './Icons'
import { SearchBox } from './SearchBox'
import { SideList, type SideGroup } from './SideList'
import { ObjectPanel } from './ObjectPanel'
import { PodPanel } from './PodPanel'
import { ServicePanel } from './ServicePanel'
import type { TabProps } from './tabs'
import { useTable } from '../lib/useTable'
import { usePref } from '../lib/prefs'

/** Rows a kind's table shows before "Show all". */
const FOLD = 8

/** The kind the funnel lists Helm releases under. */
const RELEASES = 'Helm release'

/** The namespace's Helm releases, listed as its other kinds are; a row opens the release on the Helm tab. */
function ReleasesCard({ rows, now, cluster, onToast }: { rows: ReleaseRow[]; now: number; cluster: string | undefined; onToast: (msg: string) => void }) {
  const [sort, setSort] = useState<SortState>({ col: 'name', dir: 'asc' })
  const [all, setAll] = useState(false)
  const [hiddenList, setHiddenList] = usePref<string[]>('221b.ns-releases.hidden', [])
  const hidden = useMemo(() => new Set(hiddenList), [hiddenList])
  const setHidden = (h: Set<string>) => setHiddenList([...h])
  const sorted = useMemo(() => sortRows(rows, RELEASE_COLUMNS, sort, now), [rows, sort, now])
  const shown = all || sorted.length <= FOLD ? sorted : sorted.slice(0, FOLD)
  const cols = useMemo(() => RELEASE_COLUMNS.filter((c, i) => i === 0 || !hidden.has(c.id)), [hidden])
  const exportRows = (format: ExportFormat) => {
    const file = exportFileName(cluster, 'helm-releases', format)
    try {
      const t = exportTable(sorted, cols, now)
      if (format === 'xlsx') download(xlsxBytes('Helm releases', t.head, t.rows), file)
      else if (format === 'csv') download(csvText(t.head, t.rows), file, 'text/csv;charset=utf-8')
      else download(JSON.stringify(t.json, null, 2) + '\n', file, 'application/json')
      onToast(`Downloaded ${file}`)
    } catch {
      onToast('Couldn’t build the file.')
    }
  }
  return (
    <section className="rk-card" aria-label="Helm releases" style={{ overflow: 'hidden' }} data-testid="namespace-releases">
      <div className="rk-tbar">
        <h3 style={{ margin: 0, fontSize: 13.5, fontWeight: 600 }}>Helm releases</h3>
        <span className="rk-count">{rows.length}</span>
        <span className="rk-mono" style={{ fontSize: 11, color: 'var(--fg3)' }}>
          helm.sh/release.v1
        </span>
        <span style={{ flex: '1 1 auto' }} />
        <ColumnsMenu cols={RELEASE_COLUMNS} hidden={hidden} defaults={new Set()} onChange={setHidden} compact={false} />
        <ExportMenu onExport={exportRows} disabled={!rows.length} what="these Helm releases" />
      </div>
      <DataTable<ReleaseRow>
        label="Helm releases"
        rows={shown}
        cols={cols}
        rowKey={(r) => r.key}
        rowLabel={(r) => `Helm release ${r.name}, ${r.status} — open it on the Helm tab`}
        sort={sort}
        onSort={(c) => setSort((s) => nextSort(RELEASE_COLUMNS, s, c))}
        onOpen={(key) => {
          const r = rows.find((x) => x.key === key)
          if (r) navigate(href('helm', [r.namespace, r.name]))
        }}
        now={now}
        empty="Nothing matches."
        rowAttr="release"
        footer={
          sorted.length > FOLD && (
            <button className="rk-link" onClick={() => setAll(!all)} style={{ padding: '10px 16px', fontSize: 12.5 }}>
              {all ? `Show the first ${FOLD}` : `Show all ${sorted.length}`}
            </button>
          )
        }
      />
    </section>
  )
}

/** A kind's table in the namespace: the shared table, folded past FOLD rows. */
function KindCard({ kind, apiVersion, rows, now, cluster, selected, onOpen, onToast, hidden, setHidden, defaults }: { kind: string; apiVersion: string; rows: ObjectRow[]; now: number; cluster: string | undefined; selected: string | null; onOpen: (key: string) => void; onToast: (msg: string) => void; hidden: Set<string>; setHidden: (h: Set<string>) => void; defaults: Set<string> }) {
  const [sort, setSort] = useState<SortState>({ col: 'name', dir: 'asc' })
  const [all, setAll] = useState(false)
  const plural = pluralKind(kind)
  const sorted = useMemo(() => sortRows(rows, OBJECT_COLUMNS, sort, now), [rows, sort, now])
  const shown = all || sorted.length <= FOLD ? sorted : sorted.slice(0, FOLD)
  const cols = useMemo(() => OBJECT_COLUMNS.filter((c, i) => i === 0 || !hidden.has(c.id)), [hidden])
  const exportRows = (format: ExportFormat) => {
    const file = exportFileName(cluster, pluralKind(kind).toLowerCase(), format)
    try {
      const t = exportTable(sorted, cols, now)
      if (format === 'xlsx') download(xlsxBytes(kind, t.head, t.rows), file)
      else if (format === 'csv') download(csvText(t.head, t.rows), file, 'text/csv;charset=utf-8')
      else download(JSON.stringify(t.json, null, 2) + '\n', file, 'application/json')
      onToast(`Downloaded ${file}`)
    } catch {
      onToast('Couldn’t build the file.')
    }
  }
  return (
    <section className="rk-card" aria-label={plural} style={{ overflow: 'hidden' }} data-kind={kind}>
      <div className="rk-tbar">
        <h3 style={{ margin: 0, fontSize: 13.5, fontWeight: 600 }}>{plural}</h3>
        <span className="rk-count">{rows.length}</span>
        <span className="rk-mono" style={{ fontSize: 11, color: 'var(--fg3)' }}>
          {apiVersion}
        </span>
        <span style={{ flex: '1 1 auto' }} />
        <ColumnsMenu cols={OBJECT_COLUMNS} hidden={hidden} defaults={defaults} onChange={setHidden} compact={false} />
        <ExportMenu onExport={exportRows} disabled={!rows.length} what={`these ${plural}`} />
      </div>
      <DataTable<ObjectRow>
        label={plural}
        rows={shown}
        cols={cols}
        rowKey={(r) => r.key}
        rowLabel={objectLabel}
        sort={sort}
        onSort={(c) => setSort((s) => nextSort(OBJECT_COLUMNS, s, c))}
        selected={selected}
        onOpen={onOpen}
        now={now}
        empty="Nothing matches."
        rowAttr="object"
        footer={
          sorted.length > FOLD && (
            <button className="rk-link" onClick={() => setAll(!all)} style={{ padding: '10px 16px', fontSize: 12.5 }}>
              {all ? `Show the first ${FOLD}` : `Show all ${sorted.length}`}
            </button>
          )
        }
      />
    </section>
  )
}

/**
 * The Namespaces tab (P2-02): every namespace on the left, what the chosen
 * one holds on the right, kind by kind, with the kinds funnel.
 */
export function NamespacesTab({ cluster, now, onToast, route }: TabProps) {
  const { rows: namespaces, loaded } = useView<NamespaceRow>('namespaces')
  const p = route.params
  const [listQuery, setListQuery] = useState('')
  const sorted = useMemo(() => [...namespaces].sort((a, b) => a.name.localeCompare(b.name)), [namespaces])
  const wanted = route.path[0] ?? ''
  const current = sorted.find((n) => n.name === wanted) ?? sorted.find((n) => n.group === 'apps') ?? sorted[0] ?? null
  const ns = current?.name ?? ''
  const objects = useView<ObjectRow>(ns ? `ns:${ns}` : null)
  const query = p.get('q') ?? ''
  const kindsParam = useMemo(() => listParam(p, 'kinds'), [p])
  const selected = p.get('open')
  const set = useCallback((changes: Record<string, string | string[] | undefined>) => navigate(withParams(route, changes), true), [route])
  const [funnelOpen, setFunnelOpen] = useState(false)
  const funnelButton = useRef<HTMLButtonElement>(null)

  // The kinds present, in the registry's order, with their counts.
  // Helm releases, read where Helm keeps them (P2-07), listed like the namespace's other kinds.
  const releases = useView<ReleaseRow>(current?.releases.length ? 'helm' : null)
  const nsReleases = useMemo(() => releases.rows.filter((r) => r.namespace === ns), [releases.rows, ns])
  const kinds = useMemo(() => {
    const m = new Map<string, { kind: string; apiVersion: string; order: number; n: number }>()
    for (const r of objects.rows) {
      const k = m.get(r.kind) ?? { kind: r.kind, apiVersion: r.apiVersion, order: r.order, n: 0 }
      k.n++
      m.set(r.kind, k)
    }
    return [...m.values()].sort((a, b) => a.order - b.order || a.kind.localeCompare(b.kind))
  }, [objects.rows])
  const kindSel = useMemo(() => (kindsParam.length ? new Set(kindsParam) : null), [kindsParam])
  const words = useMemo(() => queryWords(query), [query])
  const filtered = useMemo(() => objects.rows.filter((r) => (!kindSel || kindSel.has(r.kind)) && matchesQuery(objectTerms(r), words)), [objects.rows, kindSel, words])
  const byKind = useMemo(() => {
    const m = new Map<string, ObjectRow[]>()
    for (const r of filtered) m.set(r.kind, [...(m.get(r.kind) ?? []), r])
    return m
  }, [filtered])
  // One Columns choice for every kind table, kept with the shared table state.
  const table = useTable<ObjectRow>({ id: 'namespace-objects', columns: OBJECT_COLUMNS, rows: filtered, terms: objectTerms, now, defaultSort: { col: 'name', dir: 'asc' } })

  const lq = listQuery.trim().toLowerCase()
  const listed = sorted.filter((n) => !lq || n.name.includes(lq) || n.releases.some((r) => r.includes(lq)))
  const item = (n: NamespaceRow) => ({
    key: n.name,
    href: href('namespaces', [n.name]),
    current: n.name === ns,
    name: n.name,
    right:
      n.podsFailing || n.podsNotRunning ? (
        <span className="rk-t" data-tone={n.podsFailing ? 'err' : 'warn'} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 11, whiteSpace: 'nowrap' }}>
          <span className="rk-dot" />
          {n.podsFailing ? nWord(n.podsFailing, 'pod failing', 'pods failing') : nWord(n.podsNotRunning, 'pod not running', 'pods not running')}
        </span>
      ) : undefined,
    sub: `${nWord(n.pods, 'pod', 'pods')} · ${nWord(n.objects, 'object', 'objects')}`,
    sub2: n.releases.length ? n.releases.join(', ') : undefined,
  })
  const groups: SideGroup[] = [
    { title: 'Applications and add-ons', items: listed.filter((n) => n.group === 'apps').map(item) },
    { title: 'Kubernetes', items: listed.filter((n) => n.group === 'system').map(item) },
  ]

  const exportAll = (format: ExportFormat) => {
    const file = exportFileName(cluster?.name, `${ns}-objects`, format)
    try {
      const t = exportTable(filtered, OBJECT_COLUMNS, now)
      if (format === 'xlsx') download(xlsxBytes(ns.slice(0, 31) || 'Objects', t.head, t.rows), file)
      else if (format === 'csv') download(csvText(t.head, t.rows), file, 'text/csv;charset=utf-8')
      else download(JSON.stringify(t.json, null, 2) + '\n', file, 'application/json')
      onToast(`Downloaded ${file}`)
    } catch {
      onToast('Couldn’t build the file.')
    }
  }

  const selectedRow = selected ? (objects.rows.find((r) => r.key === selected) ?? null) : null
  // The drawer: open=Kind/name opens the object's own details, by kind.
  const [selKind = '', selName = ''] = (selected ?? '').split('/', 2) as [string?, string?]
  const closeDrawer = () => set({ open: undefined })
  const toggleKind = (kind: string) => {
    const next = new Set(kindSel ?? kinds.map((k) => k.kind))
    if (next.has(kind)) next.delete(kind)
    else next.add(kind)
    set({ kinds: next.size === kinds.length ? undefined : next.size ? [...next] : ['-'] })
  }

  return (
    <>
      <SideList label="Namespaces" title="Namespaces" query={listQuery} onQuery={setListQuery} placeholder="Filter namespaces" groups={groups} empty={loaded ? 'No namespaces match.' : 'Waiting for the first list…'} testId="namespace-list" foot="Cluster-scoped objects such as nodes, persistent volumes, storage classes and cluster roles belong to no namespace; find nodes in the Nodes tab." />
      <main className="rk-pane" aria-label="Namespace" style={{ flex: '999 1 560px', minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        {current && (
          <section aria-label={`Namespace ${ns}`} style={{ padding: '18px 24px 28px', display: 'flex', flexDirection: 'column', gap: 12 }}>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 14px' }}>
              <h2 className="rk-mono" style={{ margin: 0, fontSize: 18, fontWeight: 600 }} data-testid="namespace-name">
                {ns}
              </h2>
              <span className="rk-pill" data-tone={current.status === 'Active' ? 'ok' : 'warn'}>
                <span className="rk-dot" />
                {current.status}
              </span>
              <span style={{ fontSize: 12.5, color: 'var(--fg3)' }} title={fmtTs(current.created)}>
                created {fmtAge(current.created, now)} ago
              </span>
              <span style={{ fontSize: 12.5, color: 'var(--fg3)' }} data-testid="namespace-summary">
                {nWord(current.objects, 'object', 'objects')} of {nWord(current.kinds, 'kind', 'kinds')} · {nWord(current.pods, 'pod', 'pods')}
                {current.podsNotRunning ? `, ${current.podsNotRunning} not running` : ''}
              </span>
            </div>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
              <div
                className="rk-dd"
                style={{ position: 'relative' }}
                onKeyDown={(e) => {
                  if (e.key === 'Escape' && funnelOpen) {
                    e.stopPropagation()
                    setFunnelOpen(false)
                    funnelButton.current?.focus()
                  }
                }}
              >
                <button ref={funnelButton} className="rk-btn" aria-expanded={funnelOpen} aria-haspopup="true" data-on={!!kindSel} onClick={() => setFunnelOpen(!funnelOpen)} style={{ height: 32 }} data-testid="kinds-funnel">
                  <FilterIcon />
                  {kindSel ? `${kindSel.size} of ${kinds.length} kinds` : 'All kinds'}
                  <span className="rk-cnt">{filtered.length}</span>
                  <Chevron />
                </button>
                {funnelOpen && (
                  <>
                    <button className="rk-ddcatch" tabIndex={-1} aria-hidden="true" onClick={() => setFunnelOpen(false)} />
                    <div className="rk-ddmenu" role="group" aria-label="Kinds to show">
                      <div className="rk-ddhead">
                        <span style={{ flex: '1 1 auto' }}>Kinds in {ns}</span>
                        <button className="rk-link" onClick={() => set({ kinds: undefined })} style={{ fontSize: 12 }}>
                          All
                        </button>
                      </div>
                      {kinds.map((k) => (
                        <label key={k.kind} className="rk-ddopt">
                          <input type="checkbox" checked={!kindSel || kindSel.has(k.kind)} onChange={() => toggleKind(k.kind)} />
                          <span style={{ flex: '1 1 auto' }}>{pluralKind(k.kind)}</span>
                          <span className="rk-cnt">{k.n}</span>
                        </label>
                      ))}
                    </div>
                  </>
                )}
              </div>
              <SearchBox rows={objects.rows} terms={objectTerms} value={query} onChange={(q) => set({ q })} placeholder="Filter by name, status or detail" label="Filter objects" testId="object-search" />
              <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{objects.loaded ? (filtered.length < objects.rows.length ? `Showing ${filtered.length} of ${objects.rows.length} objects` : '') : 'Loading the namespace…'}</span>
              <div className="rk-tools" style={{ marginLeft: 'auto', gap: 8 }}>
                <ExportMenu onExport={exportAll} disabled={!filtered.length} what="every object shown, with its kind" />
              </div>
            </div>
            {nsReleases.length > 0 && (!kindSel || kindSel.has(RELEASES)) && <ReleasesCard rows={nsReleases.filter((r) => !query.trim() || `${r.name} ${r.chart} ${r.status}`.toLowerCase().includes(query.trim().toLowerCase()))} now={now} cluster={cluster?.name} onToast={onToast} />}
            {kinds
              .filter((k) => byKind.has(k.kind))
              .map((k) => (
                <KindCard key={k.kind} kind={k.kind} apiVersion={k.apiVersion} rows={byKind.get(k.kind) ?? []} now={now} cluster={cluster?.name} selected={selected} onOpen={(key) => set({ open: selected === key ? undefined : key })} onToast={onToast} hidden={table.hidden} setHidden={table.setHidden} defaults={table.defaults} />
              ))}
            {objects.loaded && filtered.length === 0 && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>{objects.rows.length ? 'Nothing matches these filters.' : 'This namespace is empty, or holds only kinds 221B does not watch.'}</p>}
          </section>
        )}
        {!current && <p style={{ margin: 0, padding: '28px 24px', fontSize: 12.5, color: 'var(--fg3)' }}>{loaded ? 'No namespaces.' : 'Waiting for the first list of namespaces…'}</p>}
      </main>
      {selected && selKind === 'Pod' && <PodPanel podKey={`${ns}/${selName}`} row={null} gone={objects.loaded && !selectedRow} cluster={cluster} now={now} onClose={closeDrawer} onToast={onToast} />}
      {selected && selKind === 'Service' && <ServicePanel serviceKey={`${ns}/${selName}`} row={null} gone={objects.loaded && !selectedRow} now={now} onClose={closeDrawer} onToast={onToast} />}
      {selected && selKind !== 'Pod' && selKind !== 'Service' && <ObjectPanel kind={selKind} namespace={ns} name={selName} row={selectedRow} gone={objects.loaded && !selectedRow} cluster={cluster} now={now} linkTo={(k, n) => withParams(route, { open: `${k}/${n}` })} onClose={closeDrawer} onToast={onToast} />}
    </>
  )
}
