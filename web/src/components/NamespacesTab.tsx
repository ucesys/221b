import { useCallback, useMemo, useState, type ReactNode } from 'react'
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
import { SearchBox } from './SearchBox'
import { SideList, type SideGroup } from './SideList'
import { ObjectPanel } from './ObjectPanel'
import { PodPanel } from './PodPanel'
import { ServicePanel } from './ServicePanel'
import type { TabProps } from './tabs'
import { useTable } from '../lib/useTable'
import { usePref } from '../lib/prefs'

/** The tab Helm releases are listed under. */
const RELEASES = 'Helm release'

/** A tab's bar: the kind's API version, the search, and the table's tools. */
function TabBar({ apiVersion, search, children }: { apiVersion: string; search: ReactNode; children: ReactNode }) {
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
      {search}
      <span className="rk-mono" style={{ fontSize: 11, color: 'var(--fg3)' }}>
        {apiVersion}
      </span>
      <div className="rk-tools" style={{ marginLeft: 'auto', gap: 8 }}>
        {children}
      </div>
    </div>
  )
}

/** The namespace's Helm releases, listed as its other kinds are; a row opens the release on the Helm tab. */
function ReleasesTable({ rows, now, cluster, onToast, search }: { rows: ReleaseRow[]; now: number; cluster: string | undefined; onToast: (msg: string) => void; search: ReactNode }) {
  const [sort, setSort] = useState<SortState>({ col: 'name', dir: 'asc' })
  const [hiddenList, setHiddenList] = usePref<string[]>('221b.ns-releases.hidden', [])
  const hidden = useMemo(() => new Set(hiddenList), [hiddenList])
  const setHidden = (h: Set<string>) => setHiddenList([...h])
  const sorted = useMemo(() => sortRows(rows, RELEASE_COLUMNS, sort, now), [rows, sort, now])
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
    <section aria-label="Helm releases" style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="namespace-releases" data-kind={RELEASES}>
      <TabBar apiVersion="helm.sh/release.v1" search={search}>
        <ColumnsMenu cols={RELEASE_COLUMNS} hidden={hidden} defaults={new Set()} onChange={setHidden} compact={false} />
        <ExportMenu onExport={exportRows} disabled={!rows.length} what="these Helm releases" />
      </TabBar>
      <DataTable<ReleaseRow>
        label="Helm releases"
        rows={sorted}
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
      />
    </section>
  )
}

/** A kind's table in the namespace, the tab's whole content. */
function KindTable({ kind, apiVersion, rows, now, cluster, selected, onOpen, onToast, hidden, setHidden, defaults, search }: { kind: string; apiVersion: string; rows: ObjectRow[]; now: number; cluster: string | undefined; selected: string | null; onOpen: (key: string) => void; onToast: (msg: string) => void; hidden: Set<string>; setHidden: (h: Set<string>) => void; defaults: Set<string>; search: ReactNode }) {
  const [sort, setSort] = useState<SortState>({ col: 'name', dir: 'asc' })
  const plural = pluralKind(kind)
  const sorted = useMemo(() => sortRows(rows, OBJECT_COLUMNS, sort, now), [rows, sort, now])
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
    <section aria-label={plural} style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-kind={kind}>
      <TabBar apiVersion={apiVersion} search={search}>
        <ColumnsMenu cols={OBJECT_COLUMNS} hidden={hidden} defaults={defaults} onChange={setHidden} compact={false} />
        <ExportMenu onExport={exportRows} disabled={!rows.length} what={`these ${plural}`} />
      </TabBar>
      <DataTable<ObjectRow>
        label={plural}
        rows={sorted}
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
      />
    </section>
  )
}

/**
 * The Namespaces tab (P2-02): every namespace on the left, what the chosen
 * one holds on the right, a tab per kind as the Helm tab has them (asked at
 * the second Toolbox review, in place of a table per kind one under another).
 * The tab is the first of the kinds parameter, which Sherlock also sets.
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
  const words = useMemo(() => queryWords(query), [query])
  const filtered = useMemo(() => objects.rows.filter((r) => matchesQuery(objectTerms(r), words)), [objects.rows, words])
  const releasesShown = useMemo(() => nsReleases.filter((r) => !query.trim() || `${r.name} ${r.chart} ${r.status}`.toLowerCase().includes(query.trim().toLowerCase())), [nsReleases, query])
  const byKind = useMemo(() => {
    const m = new Map<string, ObjectRow[]>()
    for (const r of filtered) m.set(r.kind, [...(m.get(r.kind) ?? []), r])
    return m
  }, [filtered])
  // One Columns choice for every kind table, kept with the shared table state.
  const table = useTable<ObjectRow>({ id: 'namespace-objects', columns: OBJECT_COLUMNS, rows: filtered, terms: objectTerms, now, defaultSort: { col: 'name', dir: 'asc' }, drives: ['columns'] })
  // A tab per kind, Pods first as on the Helm tab, Helm releases last; each
  // counts what the search leaves.
  const tabs = useMemo(() => {
    const list = kinds.map((k) => ({ id: k.kind, label: pluralKind(k.kind), apiVersion: k.apiVersion, n: byKind.get(k.kind)?.length ?? 0 }))
    list.sort((a, b) => Number(b.id === 'Pod') - Number(a.id === 'Pod'))
    if (nsReleases.length) list.push({ id: RELEASES, label: 'Helm releases', apiVersion: 'helm.sh/release.v1', n: releasesShown.length })
    return list
  }, [kinds, byKind, nsReleases.length, releasesShown.length])
  const tab = tabs.find((t) => t.id === kindsParam[0]) ?? tabs[0] ?? null

  const lq = listQuery.trim().toLowerCase()
  const listed = sorted.filter((n) => !lq || n.name.includes(lq) || n.releases.some((r) => r.includes(lq)))
  const item = (n: NamespaceRow) => ({
    key: n.name,
    href: href('namespaces', [n.name]),
    current: n.name === ns,
    name: n.name,
    copy: 'namespace name',
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

  const selectedRow = selected ? (objects.rows.find((r) => r.key === selected) ?? null) : null
  // The drawer: open=Kind/name opens the object's own details, by kind.
  const [selKind = '', selName = ''] = (selected ?? '').split('/', 2) as [string?, string?]
  const closeDrawer = () => set({ open: undefined })
  const search = <SearchBox rows={objects.rows} terms={objectTerms} value={query} onChange={(q) => set({ q })} placeholder="Filter by name, status or detail" label="Filter objects" testId="object-search" width={240} />

  return (
    <>
      <SideList label="Namespaces" title="Namespaces" query={listQuery} onQuery={setListQuery} placeholder="Filter namespaces" groups={groups} empty={loaded ? 'No namespaces match.' : 'Waiting for the first list…'} testId="namespace-list" foot={
        <>
          Cluster-scoped objects such as nodes, persistent volumes, storage classes and cluster roles belong to no namespace; find nodes in the <a href={href('nodes')} style={{ color: 'var(--accent)' }}>Nodes</a> tab, and volumes, claims and classes in the <a href={href('storage', [], ns ? { ns } : {})} style={{ color: 'var(--accent)' }} data-testid="namespace-storage-footer">Storage</a> tab.
        </>
      }
    />
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
              {/* ST-21: this namespace's claims on the Storage tab. */}
              <a className="rk-link" href={href('storage', [], { ns })} style={{ fontSize: 12.5 }} data-testid="namespace-storage-link">
                Storage →
              </a>
            </div>
            {tabs.length > 0 && (
              <nav aria-label="Kinds in this namespace" style={{ display: 'flex', flexWrap: 'wrap', gap: '0 20px', borderBottom: '1px solid var(--line)' }} data-testid="namespace-kinds">
                {tabs.map((t) => (
                  <button key={t.id} className="rk-tab" aria-current={tab?.id === t.id ? 'page' : undefined} onClick={() => set({ kinds: t.id === tabs[0]?.id ? undefined : [t.id], open: undefined })} style={{ height: 38 }} data-subtab={t.id}>
                    {t.label}
                    {t.n > 0 && <span className="rk-count">{t.n}</span>}
                  </button>
                ))}
              </nav>
            )}
            {!objects.loaded && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>Loading the namespace…</p>}
            {tab?.id === RELEASES && <ReleasesTable rows={releasesShown} now={now} cluster={cluster?.name} onToast={onToast} search={search} />}
            {tab && tab.id !== RELEASES && (
              <KindTable key={tab.id} kind={tab.id} apiVersion={tab.apiVersion} rows={byKind.get(tab.id) ?? []} now={now} cluster={cluster?.name} selected={selected} onOpen={(key) => set({ open: selected === key ? undefined : key })} onToast={onToast} hidden={table.hidden} setHidden={table.setHidden} defaults={table.defaults} search={search} />
            )}
            {objects.loaded && !tabs.length && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>This namespace is empty, or holds only kinds 221B does not watch.</p>}
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
