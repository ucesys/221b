import { useMemo, useState } from 'react'
import { saveFleets } from '../api/fleets'
import { useLiveMeta, useView } from '../api/live'
import type { Fleet, FleetRow, Me, NodeRow } from '../api/types'
import { capText, fleetOf, fleetsOfNode, has, inDefinition, listText, metaText, mxColumns, mxHas, pickedNodes, presets, problemsText, reqsOf, reqText, scopeNames, selectorText, setPickedNodes, splitLabel, type MxColumn } from '../lib/fleets'
import { compareNatural, fileStamp, nWord } from '../lib/format'
import { effectTone, facetMatch, labelText } from '../lib/nodes'
import { href, listParam, navigate, withParams } from '../lib/router'
import { CopyName } from './CopyName'
import { usePref } from '../lib/prefs'
import { csvText, type ExportFormat } from '../lib/table'
import { download, xlsxBytes, type Cell } from '../lib/xlsx'
import { FleetEditor, type EditorSeed } from './FleetEditor'
import { FleetNodes, type FleetNodesMode } from './FleetNodes'
import { ProblemFilter, problemLevel, showsNode } from './ProblemFilter'
import { ExportMenu } from './ExportMenu'
import { Chevron, ColumnsIcon, PlusIcon, SearchIcon } from './Icons'
import type { TabProps } from './tabs'
import { peekLink } from '../lib/peek'


const HIDE_KEY = '221b.fleets.hidden'

function Pencil() {
  return (
    <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M4 20h4L19 9l-4-4L4 16z" />
      <path d="m13.5 6.5 4 4" />
    </svg>
  )
}

function XIcon() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" aria-hidden="true">
      <path d="M7 7l10 10M17 7 7 17" />
    </svg>
  )
}

interface Sort {
  col: string
  dir: 'asc' | 'desc'
}

/** The matrix's sort per cluster, kept while the page is open, so it is the same after a tab switch. */
const lastSort = new Map<string, Sort>()

interface Props extends TabProps {
  me: Me | null
}

/** The Fleets tab (P4-01): fleets on the left, every node against every label value and taint on the right. */
export function FleetsTab({ cluster, now, onToast, route, me }: Props) {
  const meta = useLiveMeta()
  const { rows: fleetRows, loaded } = useView<FleetRow>('fleets')
  const { rows: nodes } = useView<NodeRow>('nodes')
  const admin = me?.admin ?? true
  const defs: Fleet[] = useMemo(() => meta.settings?.fleets?.[route.cluster] ?? [], [meta.settings, route.cluster])
  const fleets = useMemo(() => fleetRows.filter((r) => r.kind === 'fleet').sort((a, b) => a.order - b.order), [fleetRows])
  const none = fleetRows.find((r) => r.kind === 'none')
  const suggestions = fleetRows.filter((r) => r.kind === 'suggestion')

  const p = route.params
  const sel = listParam(p, 'fleet').filter((id) => id === 'none' || fleets.some((f) => f.id === id))
  const group = p.get('group') === 'none' ? 'none' : 'fleet'
  const query = p.get('q') ?? ''
  const level = problemLevel(p.get('err'))
  const fx = { labels: listParam(p, 'pl'), taints: listParam(p, 'pt') }
  const fxOn = fx.labels.length + fx.taints.length > 0
  const set = (changes: Record<string, string | string[] | undefined>) => navigate(withParams(route, changes), true)

  // Kept in this browser, or with the person's account (lib/prefs).
  const [hiddenList, setHiddenList] = usePref<string[]>(HIDE_KEY, [])
  const hidden = useMemo(() => new Set(hiddenList), [hiddenList])
  const setHidden = (s: Set<string>) => setHiddenList([...s])
  const [colsOpen, setColsOpen] = useState(false)
  const [colQuery, setColQuery] = useState('')
  const [sort, setSortNow] = useState<Sort>(() => lastSort.get(route.cluster) ?? { col: 'node', dir: 'asc' })
  const setSort = (s: Sort) => {
    lastSort.set(route.cluster, s)
    setSortNow(s)
  }
  const [editor, setEditor] = useState<EditorSeed | null>(null)
  // Nodes picked for a fleet change (asked after 0.7.1), kept per cluster across tab switches.
  const [pickList, setPickList] = useState<string[]>(() => pickedNodes(route.cluster))
  const setPicks = (names: string[]) => {
    setPickedNodes(route.cluster, names)
    setPickList(names)
  }
  const [dialog, setDialog] = useState<FleetNodesMode | null>(null)

  const one = sel.length === 1 && sel[0] !== 'none' ? fleets.find((f) => f.id === sel[0]) : undefined
  const byName = useMemo(() => new Map(nodes.map((n) => [n.name, n])), [nodes])
  // Gone nodes drop out of the picks.
  const picks = pickList.filter((n) => byName.has(n))
  const pickSet = new Set(picks)
  const pickedRows = picks.map((n) => byName.get(n)!)
  const oneDef = one ? defs.find((f) => f.id === one.id) : undefined
  const allNames = useMemo(() => nodes.map((n) => n.name), [nodes])
  const cols = useMemo(() => mxColumns(nodes), [nodes])
  const visCols = cols.filter((c) => !hidden.has(c.id))
  const q = query.trim().toLowerCase()
  const scope = scopeNames(sel, fleetRows, allNames).filter((n) => byName.has(n) && (!q || n.toLowerCase().includes(q)) && showsNode(level, byName.get(n)!))
  const narrowed = !!q || level !== 'all'
  const withErrors = nodes.filter((n) => n.errors > 0).length
  const withProblems = nodes.filter((n) => n.errors + n.warnings > 0).length

  const sortVal = (n: NodeRow, col: string): string | number | null => {
    if (col === 'node') return n.name
    if (col === 'fleet') return fleetsOfNode(n.name, fleetRows)[0]?.name ?? null
    const c = cols.find((x) => x.id === col)
    return c ? (mxHas(n, c) ? 0 : 1) : null
  }
  const ordered = (names: string[]): NodeRow[] => {
    const rows = names.map((n) => byName.get(n)!).filter(Boolean)
    return rows.sort((a, b) => {
      const x = sortVal(a, sort.col)
      const y = sortVal(b, sort.col)
      if (x === null && y === null) return compareNatural(a.name, b.name)
      if (x === null) return 1
      if (y === null) return -1
      const c = typeof x === 'number' && typeof y === 'number' ? x - y : compareNatural(String(x), String(y))
      return (sort.dir === 'asc' ? c : -c) || compareNatural(a.name, b.name)
    })
  }

  // Groups of rows: one per fleet (or per selected fleet), then the nodes in none.
  interface Group {
    key: string
    row?: FleetRow
    title?: string
    rows: NodeRow[]
  }
  const groups: Group[] = (() => {
    if (group === 'none' || sel[0] === 'none') return [{ key: 'all', rows: ordered(scope) }]
    const out: Group[] = []
    const inScope = new Set(scope)
    for (const f of fleets) {
      if (sel.length && !sel.includes(f.id!)) continue
      const rows = ordered(f.members.filter((m) => inScope.has(m)))
      if (narrowed && rows.length === 0) continue
      out.push({ key: f.key, row: f, title: f.name, rows })
    }
    if (!sel.length && none && none.members.length) {
      const rows = ordered(none.members.filter((m) => inScope.has(m)))
      if (!narrowed || rows.length) out.push({ key: 'none', row: none, title: 'Not in any fleet', rows })
    }
    return out
  })()
  const rowCount = groups.reduce((n, g) => n + g.rows.length, 0)
  const shownNames = [...new Set(groups.flatMap((g) => g.rows.map((r) => r.name)))]
  const shownPicked = shownNames.filter((n) => pickSet.has(n)).length
  const togglePickNode = (name: string) => setPicks(pickSet.has(name) ? picks.filter((n) => n !== name) : [...picks, name])
  const picked = (n: NodeRow) => facetMatch(n, fx)
  const pickedCount = fxOn ? new Set(scope.filter((name) => picked(byName.get(name)!))).size : 0

  const sortTip = (col: string, label: string) =>
    sort.col === col ? `Sorted by ${label}, ${sort.dir === 'asc' ? 'ascending' : 'descending'} — click to reverse` : `Sort by ${label}`
  const sortBy = (col: string) => setSort(sort.col === col ? { col, dir: sort.dir === 'asc' ? 'desc' : 'asc' } : { col, dir: 'asc' })
  const ind = (col: string) => (sort.col === col ? (sort.dir === 'asc' ? '↑' : '↓') : '')

  const togglePick = (c: MxColumn) => {
    if (c.kind === 't') set({ pt: fx.taints.includes(c.fx) ? fx.taints.filter((t) => t !== c.fx) : [...fx.taints, c.fx] })
    else set({ pl: fx.labels.includes(c.fx) ? fx.labels.filter((l) => l !== c.fx) : [...fx.labels, c.fx] })
  }
  const pickedOn = (c: MxColumn) => (c.kind === 't' ? fx.taints.includes(c.fx) : fx.labels.includes(c.fx))

  const save = async (next: Fleet[], toast: string) => {
    await saveFleets(route.cluster, next)
    onToast(toast)
  }
  const removeFleet = async (f: Fleet) => {
    const next = defs.filter((x) => x.id !== f.id)
    await saveFleets(route.cluster, next)
    set({ fleet: undefined })
    onToast(`Deleted the fleet “${f.name}”. Its nodes keep their labels and taints.`)
  }

  const nodesHref = (labels: string[], taints: string[]) => href('nodes', [], { label: labels, taint: taints })
  const podsHref = (labels: string[], taints: string[]) => href('pods', [], { label: labels, taint: taints })

  // The name column, with room for the pick box for those who may change fleets.
  const pickW = admin ? 24 : 0
  const gridCols = `${Math.round(Math.max(150, Math.min(260, Math.max(0, ...nodes.map((n) => n.name.length)) * 7.6 + 96)) + pickW)}px var(--mxfw) ${visCols.length ? `repeat(${visCols.length}, 30px)` : ''}`
  const mxh = Math.round(Math.max(70, Math.min(190, Math.max(0, ...visCols.map((c) => Math.max(c.main.length * 6.7, c.pre.length * 6.1))) + 34)))
  const minW = Math.round(Math.max(150, Math.min(260, Math.max(0, ...nodes.map((n) => n.name.length)) * 7.6 + 96)) + pickW + 30 * visCols.length)
  const kinds: { kind: string; label: string; span: number }[] = []
  for (const c of visCols) {
    const label = c.kind === 'r' ? 'Role' : c.kind === 'l' ? 'Labels' : 'Taints'
    const last = kinds[kinds.length - 1]
    if (last && last.kind === c.kind) last.span++
    else kinds.push({ kind: c.kind, label, span: 1 })
  }

  const exportAs = (format: ExportFormat) => {
    const seen = new Set<string>()
    const rows: NodeRow[] = []
    for (const g of groups)
      for (const r of g.rows) {
        if (seen.has(r.name)) continue
        seen.add(r.name)
        rows.push(r)
      }
    const head = ['Node', 'Fleets', ...visCols.map((c) => (c.kind === 'r' ? `${c.main} (role)` : c.label))]
    const cells: Cell[][] = rows.map((r) => [r.name, fleetsOfNode(r.name, fleetRows).map((f) => f.name).join(', '), ...visCols.map((c) => mxHas(r, c))])
    const title = one ? one.name! : sel[0] === 'none' ? 'Not in any fleet' : 'Fleets'
    const base = `${(cluster?.name || 'cluster').replace(/[^A-Za-z0-9._-]+/g, '-')}-${title.toLowerCase().replace(/[^a-z0-9]+/g, '-')}-${fileStamp()}`
    if (format === 'xlsx') download(xlsxBytes(title.slice(0, 31), head, cells), base + '.xlsx')
    else if (format === 'csv') download(csvText(head, cells), base + '.csv', 'text/csv')
    else download(JSON.stringify(cells.map((c) => Object.fromEntries(head.map((h, i) => [h, c[i]]))), null, 2), base + '.json', 'application/json')
    onToast(`Downloaded ${base}.${format}`)
  }

  const summary = (() => {
    if (sel.length) return nWord(scope.length, 'node', 'nodes')
    const inNone = none?.members.length ?? 0
    const multi = nodes.filter((n) => fleetsOfNode(n.name, fleetRows).length > 1).length
    return `${nWord(nodes.length, 'node', 'nodes')} · ${nWord(fleets.length, 'fleet', 'fleets')}${inNone ? ` · ${inNone} in no fleet` : ' · every node is in a fleet'}${multi ? ` · ${multi} in more than one` : ''}`
  })()
  const title = sel[0] === 'none' ? 'Not in any fleet' : sel.length > 1 ? listText(fleets.filter((f) => sel.includes(f.id!)).map((f) => f.name!)) : 'Fleets'

  const emptyText = !visCols.length
    ? 'Every label and taint column is hidden. Choose Columns to show some.'
    : rowCount === 0
      ? q
        ? `No node in view matches “${query.trim()}”.`
        : one
          ? one.by === 'nodes'
            ? 'No node on this list is in the cluster.'
            : 'No node has all of these labels and taints yet.'
          : loaded
            ? 'No nodes.'
            : 'Waiting for the first list of nodes…'
      : ''

  // The Columns panel's groups: roles, one per label key, taints.
  const cq = colQuery.trim().toLowerCase()
  const colMatches = (c: MxColumn) => !cq || `${c.full} ${c.main}`.toLowerCase().includes(cq)
  const colGroups: { key: string; tip: string; items: MxColumn[] }[] = []
  for (const c of cols) {
    if (!colMatches(c)) continue
    const key = c.kind === 'r' ? 'Role' : c.kind === 't' ? 'Taints' : c.key
    let g = colGroups.find((x) => x.key === key)
    if (!g) {
      g = { key, tip: c.kind === 'r' ? 'node-role.kubernetes.io labels, shown as the Control plane and Worker columns' : c.kind === 't' ? 'One column per taint' : 'Label ' + c.key, items: [] }
      colGroups.push(g)
    }
    g.items.push(c)
  }
  const nodesWith = (c: MxColumn) => nodes.filter((n) => mxHas(n, c)).length
  const quick = presets(cols, nodes, defs)

  const saveFxDisabled = (() => {
    const keys = fx.labels.map((l) => splitLabel(l)[0])
    return new Set(keys).size !== keys.length
  })()

  return (
    <>
      <aside className="rk-pane" aria-label="Fleets" data-testid="fleet-list" style={{ flex: '1 1 320px', maxWidth: '100%', display: 'flex', flexDirection: 'column', background: 'var(--surface)', borderRight: '1px solid var(--line)' }}>
        <div style={{ padding: '14px 16px 12px', display: 'flex', flexDirection: 'column', gap: 8, borderBottom: '1px solid var(--line)' }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
            <h2 className="rk-h">
              Fleets <span style={{ fontWeight: 500, letterSpacing: 0, textTransform: 'none' }}>{fleets.length}</span>
            </h2>
            {admin && (
              <button className="rk-btn" onClick={() => setEditor({ fleet: null })} style={{ height: 28 }} data-testid="fleet-new">
                <PlusIcon />
                New fleet
              </button>
            )}
          </div>
        </div>
        <div style={{ padding: '0 0 6px' }}>
          <button className="rk-fl rk-fl-main" data-sel={sel.length === 0} aria-pressed={sel.length === 0} onClick={() => set({ fleet: undefined })} style={{ borderTop: 0, padding: '11px 16px 11px 36px' }}>
            <span className="rk-fl-top">
              <span className="rk-fl-name">All nodes</span>
              <span className="rk-cnt">{nodes.length}</span>
            </span>
            <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>Every node in the cluster</span>
          </button>
          {fleets.map((f) => {
            const checked = sel.includes(f.id!)
            const only = checked && sel.length === 1
            const cap = capText(f.stats)
            const probs = problemsText(f.stats)
            return (
              <div key={f.key} className="rk-fl" data-sel={checked} data-fleet={f.id}>
                <label className="rk-fl-check" title={checked ? `Remove ${f.name} from the selection` : `Add ${f.name} to the selection`}>
                  <input type="checkbox" checked={checked} aria-label={checked ? `Remove ${f.name} from the selection` : `Add ${f.name} to the selection`} onChange={() => set({ fleet: checked ? sel.filter((s) => s !== f.id) : [...sel.filter((s) => s !== 'none'), f.id!] })} />
                </label>
                <button className="rk-fl-main" aria-pressed={checked} onClick={() => set({ fleet: only ? undefined : f.id })} title={only ? 'Show all nodes again' : `Show only ${f.name}`} data-testid="fleet-pick">
                  <span className="rk-fl-top">
                    <span className="rk-fdot" data-c={f.color} />
                    <span className="rk-fl-name">{f.name}</span>
                    <span className="rk-cnt" data-testid="fleet-count">
                      {f.members.length}
                    </span>
                  </span>
                  <span className="rk-fl-def">{reqText(fleetOf(f))}</span>
                  <span className="rk-fl-cap">
                    <span className="rk-bar" style={{ width: 56 }} title={cap.tip}>
                      <span className="rk-use" style={{ width: `${cap.pct}%` }} />
                    </span>
                    <span>{cap.text}</span>
                  </span>
                  {probs.text && (
                    <span className="rk-t" data-tone={probs.tone} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 11.5, fontWeight: 500 }}>
                      <span className="rk-dot" />
                      {probs.text}
                    </span>
                  )}
                </button>
                {admin && (
                  <button className="rk-icon-btn" aria-label={`Edit fleet ${f.name}`} title="Edit this fleet" onClick={() => setEditor({ fleet: fleetOf(f) })} data-testid="fleet-edit">
                    <Pencil />
                  </button>
                )}
              </div>
            )
          })}
          {loaded && fleets.length === 0 && <p style={{ margin: 0, padding: '14px 16px', fontSize: 12.5, color: 'var(--fg3)', borderTop: '1px solid var(--line)' }}>No fleets yet. {admin ? 'Create one, or save one of the groups below.' : 'An administrator can create them.'}</p>}
          {none && (none.members.length > 0 || sel[0] === 'none') && (
            <button className="rk-fl rk-fl-main" data-sel={sel[0] === 'none'} aria-pressed={sel[0] === 'none'} onClick={() => set({ fleet: sel[0] === 'none' ? undefined : 'none' })} style={{ padding: '11px 16px 11px 36px' }} data-testid="fleet-none">
              <span className="rk-fl-top">
                <span className="rk-fl-name" style={{ fontWeight: 500 }}>
                  Not in any fleet
                </span>
                <span className="rk-cnt">{none.members.length}</span>
              </span>
              <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{none.members.length ? listText([...none.members.slice(0, 4), ...(none.members.length > 4 ? [`${none.members.length - 4} more`] : [])]) : 'Every node is in a fleet'}</span>
            </button>
          )}
        </div>
        {suggestions.length > 0 && (
          <div style={{ padding: '12px 16px', display: 'flex', flexDirection: 'column', gap: 8, borderTop: '1px solid var(--line)' }} data-testid="fleet-suggestions">
            <h3 className="rk-h">Groups not saved as a fleet</h3>
            {suggestions.map((s) => (
              <div key={s.key} style={{ display: 'flex', flexDirection: 'column', gap: 4, padding: '8px 0', borderTop: '1px dashed var(--line)' }}>
                <span style={{ fontSize: 12.5, fontWeight: 500 }}>
                  {s.name} · {nWord(s.members.length, 'node', 'nodes')}
                </span>
                <span className="rk-mono" style={{ fontSize: 11, color: 'var(--fg3)', overflowWrap: 'anywhere' }}>
                  {reqText({ labels: s.labels, taints: s.taints })}
                </span>
                {admin && (
                  <span>
                    <button className="rk-link" onClick={() => setEditor({ fleet: null, name: defs.some((f) => f.name.toLowerCase() === (s.name ?? '').toLowerCase()) ? '' : s.name, labels: s.labels, taints: s.taints })} style={{ fontSize: 12 }}>
                      Save as a fleet…
                    </button>
                  </span>
                )}
              </div>
            ))}
          </div>
        )}
      </aside>

      <section className="rk-pane" aria-label="Fleets" style={{ flex: '999 1 640px', minWidth: 0, padding: '20px 24px 32px', display: 'flex', flexDirection: 'column', gap: 14 }}>
        {one ? (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="fleet-header">
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 12px' }}>
              <span className="rk-fdot" data-c={one.color} style={{ width: 14, height: 14, borderRadius: 4 }} />
              <h1 style={{ margin: 0, fontSize: 19, fontWeight: 600, letterSpacing: '-0.01em' }}>{one.name}</h1>
              <span className="rk-tag" style={{ fontSize: 11, padding: '2px 8px' }}>
                {nWord(one.members.length, 'node', 'nodes')}
              </span>
              {problemsText(one.stats).text && (
                <span className="rk-t" data-tone={problemsText(one.stats).tone} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 12, fontWeight: 500 }}>
                  <span className="rk-dot" />
                  {problemsText(one.stats).text}
                </span>
              )}
              <span style={{ flex: '1 1 auto' }} />
              {admin && (
                <button className="rk-btn" onClick={() => setEditor({ fleet: fleetOf(one) })}>
                  Edit fleet
                </button>
              )}
              {one.members.length > 0 && one.by !== 'nodes' && (
                <>
                  <a className="rk-btn" href={nodesHref(one.labels, one.taints)}>
                    Nodes →
                  </a>
                  <a className="rk-btn" href={podsHref(one.labels, one.taints)}>
                    Pods →
                  </a>
                </>
              )}
              {one.members.length > 0 && one.by === 'nodes' && (
                <a className="rk-btn" href={href('pods', [], { node: one.members })}>
                  Pods →
                </a>
              )}
            </div>
            {one.desc && <p style={{ margin: 0, maxWidth: '100ch', fontSize: 12.5, color: 'var(--fg2)' }}>{one.desc}</p>}
            {one.by === 'nodes' ? (
              <p style={{ margin: 0, fontSize: 12, color: 'var(--fg3)' }} data-testid="fleet-list-note">
                A list of {nWord(one.listed?.length ?? 0, 'node', 'nodes')} kept in 221B
                {(one.listed?.length ?? 0) > one.members.length ? `, ${(one.listed?.length ?? 0) - one.members.length} of them no longer in the cluster` : ''}. Nodes join and leave it only here; their labels and taints do not change.
              </p>
            ) : (
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 8px' }}>
              <span style={{ fontSize: 12, color: 'var(--fg3)', marginRight: 2 }}>Nodes that have all of</span>
              {reqsOf(one).map((r) => (
                <span key={r.id} className="rk-chip">
                  {r.text}
                  {r.effect && (
                    <span className="rk-tag" data-tone={r.tone}>
                      {r.effect}
                    </span>
                  )}
                </span>
              ))}
            </div>
            )}
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: '4px 18px', fontSize: 12, color: 'var(--fg3)' }}>
              <span data-testid="fleet-stats">{metaText(one.stats)}</span>
              {one.labels.length > 0 && (
                <span title="Taints have no selector syntax">
                  kubectl get nodes -l{' '}
                  <span className="rk-mono" style={{ color: 'var(--fg2)' }}>
                    {selectorText(one.labels)}
                  </span>
                </span>
              )}
            </div>
          </div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', gap: '4px 12px' }}>
              <h1 style={{ margin: 0, fontSize: 18, fontWeight: 600, letterSpacing: '-0.01em' }}>{title}</h1>
              <span style={{ fontSize: 12.5, color: 'var(--fg3)' }} data-testid="fleet-summary">
                {summary}
              </span>
            </div>
          </div>
        )}

        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
          <div className="rk-seg" role="group" aria-label="Group rows">
            <button aria-pressed={group === 'fleet'} onClick={() => set({ group: undefined })}>
              Group by fleet
            </button>
            <button aria-pressed={group === 'none'} onClick={() => set({ group: 'none' })}>
              No grouping
            </button>
          </div>
          <label className="rk-field" style={{ width: 240, maxWidth: '100%' }}>
            <SearchIcon />
            <span className="rk-sr">Filter nodes</span>
            <input type="search" placeholder="Filter nodes by name" value={query} onChange={(e) => set({ q: e.target.value || undefined })} data-testid="fleet-filter" />
          </label>
          <ProblemFilter level={level} problems={withProblems} errors={withErrors} onChange={(l) => set({ err: l === 'all' ? undefined : l })} />
          <span style={{ flex: '1 1 auto' }} />
          <button
            className="rk-btn"
            aria-expanded={colsOpen}
            aria-label={hidden.size > 0 ? `Columns, ${visCols.length} of ${cols.length} shown` : 'Columns'}
            data-on={hidden.size > 0}
            onClick={() => setColsOpen(!colsOpen)}
            style={{ height: 32 }}
            title="Choose which label and taint columns to show"
          >
            <ColumnsIcon />
            {hidden.size > 0 && (
              <span className="rk-cnt">
                {visCols.length}/{cols.length}
              </span>
            )}
            <Chevron />
          </button>
          <ExportMenu onExport={exportAs} disabled={!rowCount || !visCols.length} what="what the table shows" />
        </div>

        {colsOpen && (
          <div className="rk-card" role="group" aria-label="Columns" style={{ padding: '12px 16px 14px', display: 'flex', flexDirection: 'column', gap: 12 }}>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 14px' }}>
              <h3 className="rk-h">Columns</h3>
              <span style={{ fontSize: 12, color: 'var(--fg3)' }}>
                Showing {visCols.length} of {cols.length} columns: {nWord(cols.filter((c) => c.kind === 'r').length, 'role', 'roles')}, {nWord(cols.filter((c) => c.kind === 'l').length, 'label value', 'label values')}, {nWord(cols.filter((c) => c.kind === 't').length, 'taint', 'taints')}
              </span>
              <span style={{ flex: '1 1 auto' }} />
              <label className="rk-field" style={{ width: 230, maxWidth: '100%' }}>
                <SearchIcon />
                <span className="rk-sr">Find a column</span>
                <input type="search" placeholder="Find a label or taint" value={colQuery} onChange={(e) => setColQuery(e.target.value)} />
              </label>
              <button className="rk-btn" onClick={() => setHidden(new Set([...hidden].filter((id) => !cols.some((c) => c.id === id && colMatches(c)))))} title={cq ? 'Show every column that matches the search' : 'Show every column'} data-testid="fleet-cols-all">
                Select all
              </button>
              <button className="rk-btn" onClick={() => setHidden(new Set([...hidden, ...cols.filter(colMatches).map((c) => c.id)]))} title={cq ? 'Hide every column that matches the search' : 'Hide every column'} data-testid="fleet-cols-none">
                Clear all
              </button>
              <button className="rk-btn" onClick={() => setColsOpen(false)}>
                Done
              </button>
            </div>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 6 }}>
              <span style={{ fontSize: 12, color: 'var(--fg3)', marginRight: 2 }}>Quick</span>
              {quick.map((pr) => (
                <button key={pr.id} className="rk-fchip" onClick={() => setHidden(new Set([...hidden, ...pr.cols]))} title={pr.tip} style={{ fontFamily: 'var(--font)', fontSize: 12 }}>
                  {pr.label}
                  <span className="rk-cnt">{pr.cols.filter((id) => !hidden.has(id)).length}</span>
                </button>
              ))}
            </div>
            <div className="rk-ccols">
              {colGroups.map((g) => {
                const shown = g.items.filter((c) => !hidden.has(c.id)).length
                return (
                  <div key={g.key} style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
                    <label className="rk-ccol" data-key="true" title={g.tip}>
                      <input
                        type="checkbox"
                        checked={shown === g.items.length}
                        ref={(el) => {
                          if (el) el.indeterminate = shown > 0 && shown < g.items.length
                        }}
                        onChange={() => {
                          const next = new Set(hidden)
                          for (const c of g.items) {
                            if (shown === g.items.length) next.add(c.id)
                            else next.delete(c.id)
                          }
                          setHidden(next)
                        }}
                      />
                      <span className="rk-mono">{g.key}</span>
                      <span className="rk-cnt">
                        {shown}/{g.items.length}
                      </span>
                    </label>
                    {g.items.map((c) => (
                      <label key={c.id} className="rk-ccol" title={`${c.full} — on ${nodesWith(c)} of ${nodes.length} nodes`} style={{ paddingLeft: 22 }}>
                        <input
                          type="checkbox"
                          checked={!hidden.has(c.id)}
                          onChange={() => {
                            const next = new Set(hidden)
                            if (next.has(c.id)) next.delete(c.id)
                            else next.add(c.id)
                            setHidden(next)
                          }}
                        />
                        <span className="rk-mono">{c.kind === 'r' ? c.main : c.kind === 't' ? c.fx.slice(0, c.fx.lastIndexOf(':')) : splitLabel(c.fx)[1] || '(set, no value)'}</span>
                        {c.effect && (
                          <span className="rk-tag" data-tone={effectTone(c.effect)}>
                            {c.effect}
                          </span>
                        )}
                        <span className="rk-cnt">{nodesWith(c)}</span>
                      </label>
                    ))}
                  </div>
                )
              })}
            </div>
            {colGroups.length === 0 && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>No label or taint matches “{colQuery.trim()}”.</p>}
          </div>
        )}

        <p style={{ margin: 0, display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '4px 14px', fontSize: 12, color: 'var(--fg3)' }} data-testid="fleet-legend">
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <span className="rk-mk" aria-hidden="true" /> The node has this label or role
          </span>
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <span className="rk-mk" data-tone="warn" aria-hidden="true" /> It has this taint
          </span>
          <span>Click a dot to pick every node that has it; click a column’s name to sort by it.</span>
        </p>
        <div className="rk-card" style={{ overflowX: 'auto', flex: 'none' }}>
          <div className="rk-mx" role="table" aria-label="Nodes against labels and taints" data-testid="fleet-matrix" style={{ ['--cols' as string]: gridCols, ['--mxh' as string]: `${mxh}px`, minWidth: `calc(${minW}px + var(--mxfw))` }}>
            <div className="rk-mxr rk-mxg" role="row">
              <span className="rk-mxn" role="columnheader" />
              <span style={{ borderLeft: 0 }} role="columnheader" />
              {kinds.map((k) => (
                <span key={k.kind} role="columnheader" aria-colspan={k.span} style={{ gridColumn: `span ${k.span}` }}>
                  {k.label}
                </span>
              ))}
            </div>
            <div className="rk-mxr rk-mxhr" role="row">
              {admin ? (
                <span className="rk-mxn" role="columnheader" style={{ padding: '0 0 0 6px', gap: 0 }}>
                  <label className="rk-pickbox">
                  <input
                    type="checkbox"
                    checked={shownNames.length > 0 && shownPicked === shownNames.length}
                    ref={(el) => {
                      if (el) el.indeterminate = shownPicked > 0 && shownPicked < shownNames.length
                    }}
                    disabled={!shownNames.length}
                    onChange={() => setPicks(shownPicked === shownNames.length ? picks.filter((n) => !shownNames.includes(n)) : [...new Set([...picks, ...shownNames])])}
                    aria-label={shownPicked === shownNames.length ? 'Unpick every node shown' : 'Pick every node shown'}
                    title={shownPicked === shownNames.length ? 'Unpick every node shown' : 'Pick every node shown, to put them in a fleet or take them out'}
                    data-testid="fleet-pick-all"
                  />
                  </label>
                  <button className="rk-th" data-active={sort.col === 'node'} title={sortTip('node', 'node name')} onClick={() => sortBy('node')} style={{ flex: '1 1 auto' }}>
                    Node<span className="rk-arrow">{ind('node')}</span>
                  </button>
                </span>
              ) : (
                <span className="rk-contents" role="columnheader">
                  <button className="rk-th rk-mxn" data-active={sort.col === 'node'} title={sortTip('node', 'node name')} onClick={() => sortBy('node')}>
                    Node<span className="rk-arrow">{ind('node')}</span>
                  </button>
                </span>
              )}
              <span className="rk-contents" role="columnheader">
                <button className="rk-th" data-active={sort.col === 'fleet'} title={sortTip('fleet', 'fleet')} onClick={() => sortBy('fleet')}>
                  Fleets<span className="rk-arrow">{ind('fleet')}</span>
                </button>
              </span>
              {visCols.map((c) => {
                const def = inDefinition(c, one)
                const tip = `${c.full} — on ${nodesWith(c)} of ${nodes.length} nodes${def ? `\nPart of the fleet “${one!.name}”` : ''}\n${sortTip(c.id, c.label + ' (nodes that have it first)')}`
                return (
                  <span key={c.id} className="rk-contents" role="columnheader">
                    <button className="rk-mxh" data-first={c.first} data-def={def} data-active={sort.col === c.id} aria-label={sortTip(c.id, c.label + ' (nodes that have it first)')} title={tip} onClick={() => sortBy(c.id)} data-col={c.id}>
                      <span className="rk-vt">
                        <b>{c.main}</b>
                        {c.pre && <em>{c.pre}</em>}
                      </span>
                      <span className="rk-arrow">{ind(c.id)}</span>
                    </button>
                  </span>
                )
              })}
            </div>
            {groups.map((g) => {
              const probs = g.row ? problemsText(g.row.stats) : { text: '', tone: 'muted' }
              return (
                <div key={g.key} role="rowgroup" data-group={g.key}>
                  {g.title && (
                    <div className="rk-fgh" role="row">
                      <div role="cell" aria-colspan={visCols.length + 2} style={{ position: 'sticky', left: 0, display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '4px 10px', padding: '8px 12px', width: 'max-content', maxWidth: '100vw' }}>
                        <span className="rk-fdot" data-c={g.row?.kind === 'fleet' ? g.row.color : undefined} />
                        <strong style={{ fontSize: 13, fontWeight: 600 }}>{g.title}</strong>
                        <span className="rk-tag" style={{ fontSize: 11, padding: '1px 7px' }}>
                          {nWord(g.rows.length, 'node', 'nodes')}
                        </span>
                        {g.row && <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{metaText(g.row.stats)}</span>}
                        {probs.text && (
                          <span className="rk-t" data-tone={probs.tone} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 12, fontWeight: 500 }}>
                            <span className="rk-dot" />
                            {probs.text}
                          </span>
                        )}
                        {g.row?.kind === 'fleet' && !one && (
                          <button className="rk-link" onClick={() => set({ fleet: g.row!.id })} style={{ fontSize: 12 }}>
                            Show only this fleet
                          </button>
                        )}
                      </div>
                    </div>
                  )}
                  {g.rows.map((n) => {
                    const fls = fleetsOfNode(n.name, fleetRows)
                    const own = n.problems.filter((x) => !x.pods)
                    const errs = own.filter((x) => x.severity === 'error').length
                    const warns = own.length - errs
                    const flag = errs ? `${nWord(errs, 'error', 'errors')}${warns ? ` · ${warns} warn` : ''}` : warns ? nWord(warns, 'warning', 'warnings') : n.cordoned ? 'cordoned' : ''
                    const flagTone = errs ? 'err' : 'warn'
                    return (
                      <div key={g.key + n.name} className="rk-mxr" role="row" data-dim={fxOn && !picked(n)} data-node={n.name} data-picked={pickSet.has(n.name) || undefined}>
                        <span className="rk-mxn" role="rowheader">
                          {admin && (
                            <label className="rk-pickbox">
                              <input type="checkbox" checked={pickSet.has(n.name)} onChange={() => togglePickNode(n.name)} aria-label={`Pick ${n.name}`} title={pickSet.has(n.name) ? `Unpick ${n.name}` : `Pick ${n.name}, to put it in a fleet or take it out`} data-testid="fleet-pick-node" />
                            </label>
                          )}
                          <span className="rk-namecopy">
                            <a className="rk-link rk-mono" {...peekLink({ kind: 'node', name: n.name })} title={`Open the details of ${n.name}`} style={{ minWidth: 0, fontSize: 12.5, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', textDecoration: 'none' }}>
                              {n.name}
                            </a>
                            <CopyName text={n.name} what="node name" />
                          </span>
                          {flag && (
                            <span className="rk-t" data-tone={flagTone} title={own.map((x) => x.title).join('\n')} style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 11, whiteSpace: 'nowrap' }}>
                              <span className="rk-dot" />
                              {flag}
                            </span>
                          )}
                        </span>
                        <span className="rk-mxf" role="cell" title={fls.length ? `In ${listText(fls.map((f) => f.name!))}` : 'Not in any fleet'}>
                          {fls.map((f) => (
                            <span key={f.key} className="rk-fdot" data-c={f.color} />
                          ))}
                          <span className="rk-mxfn">{fls.length ? fls.map((f) => f.name).join(', ') : '—'}</span>
                        </span>
                        {visCols.map((c) =>
                          mxHas(n, c) ? (
                            <span key={c.id} className="rk-contents" role="cell">
                              <button className="rk-mxc" data-first={c.first} aria-pressed={pickedOn(c)} aria-label={`${n.name} has ${c.label}`} title={`${c.full} — on ${n.name}\n${pickedOn(c) ? 'Click to stop picking nodes by it' : 'Click to pick the nodes that have it'}`} onClick={() => togglePick(c)}>
                                <span className="rk-mk" data-tone={c.kind === 't' ? effectTone(c.effect ?? '') : undefined} aria-hidden="true" />
                              </button>
                            </span>
                          ) : (
                            <span key={c.id} className="rk-mxc" role="cell" data-first={c.first}>
                              <span className="rk-sr">{`${n.name} doesn’t have ${c.label}`}</span>
                            </span>
                          ),
                        )}
                      </div>
                    )
                  })}
                </div>
              )
            })}
          </div>
          {emptyText && <p style={{ margin: 0, padding: '18px 16px', fontSize: 12.5, color: 'var(--fg3)' }}>{emptyText}</p>}
        </div>

        {admin && picks.length > 0 && (
          <div className="rk-card rk-pickbar" role="group" aria-label="Picked nodes" data-testid="fleet-nodebar" style={fxOn ? { position: 'static' } : undefined}>
            <strong style={{ fontSize: 12.5, fontWeight: 600 }}>Picked {nWord(picks.length, 'node', 'nodes')}</strong>
            <span style={{ fontSize: 12, color: 'var(--fg2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '40ch' }} title={picks.join(', ')}>
              {listText(picks.length > 4 ? [...picks.slice(0, 3), `${picks.length - 3} more`] : picks)}
            </span>
            <span style={{ flex: '1 1 auto' }} />
            <button className="rk-btn" onClick={() => setDialog('new')} title="A new fleet of these nodes, by a label they share or a new one" data-testid="fleet-nodebar-new">
              New fleet…
            </button>
            <button className="rk-btn" onClick={() => setDialog('add')} disabled={!defs.some((f) => pickedRows.some((n) => !has(f, n)))} title={defs.length ? 'Put these nodes in a fleet you choose' : 'No fleets yet: make one first'} data-testid="fleet-nodebar-add">
              Add to a fleet…
            </button>
            <button className="rk-btn" onClick={() => setDialog('remove')} disabled={!defs.some((f) => pickedRows.some((n) => has(f, n)))} title={defs.some((f) => pickedRows.some((n) => has(f, n))) ? 'Take these nodes out of a fleet you choose' : 'None of these nodes is in a fleet'} data-testid="fleet-nodebar-remove">
              Remove from a fleet…
            </button>
            <button className="rk-btn" onClick={() => setPicks([])} title="Unpick every node" data-testid="fleet-nodebar-cancel">
              Cancel
            </button>
          </div>
        )}
        {fxOn && (
          <div className="rk-card rk-pickbar" role="status" data-testid="fleet-pickbar">
            <strong style={{ fontSize: 12.5, fontWeight: 600 }}>{pickedCount ? `Picked ${nWord(pickedCount, 'node', 'nodes')} that have` : 'No node has all of'}</strong>
            {[...fx.labels.map((l) => ({ id: l, kind: 'l' as const, text: labelText(...splitLabel(l)) })), ...fx.taints.map((t) => ({ id: t, kind: 't' as const, text: 'taint ' + t }))].map((a) => (
              <button key={a.kind + a.id} className="rk-fchip" data-on="true" onClick={() => (a.kind === 'l' ? set({ pl: fx.labels.filter((x) => x !== a.id) }) : set({ pt: fx.taints.filter((x) => x !== a.id) }))} aria-label={`Remove ${a.text}`} title={`Remove ${a.text}`} style={{ background: 'var(--surface)' }}>
                {a.text}
                <XIcon />
              </button>
            ))}
            <button className="rk-link" onClick={() => set({ pl: undefined, pt: undefined })} style={{ fontSize: 12 }}>
              Clear
            </button>
            <span style={{ flex: '1 1 auto' }} />
            {admin && (
              <button className="rk-btn" disabled={saveFxDisabled} onClick={() => setEditor({ fleet: null, labels: fx.labels, taints: fx.taints })} title={saveFxDisabled ? 'A fleet needs one value per label; remove the extra picks first' : 'Opens a new fleet with these labels and taints'}>
                Save as a fleet…
              </button>
            )}
            {pickedCount > 0 && (
              <>
                <a className="rk-btn" href={nodesHref(fx.labels, fx.taints)}>
                  Show in Nodes →
                </a>
                <a className="rk-btn" href={podsHref(fx.labels, fx.taints)}>
                  Show their pods →
                </a>
              </>
            )}
          </div>
        )}
      </section>

      {dialog && admin && (
        <FleetNodes
          mode={dialog}
          nodes={pickedRows}
          all={nodes}
          fleets={defs}
          rows={fleetRows}
          current={oneDef}
          onClose={() => setDialog(null)}
          onCreate={async (f) => {
            await saveFleets(route.cluster, [...defs, f])
            set({ fleet: f.id, pl: undefined, pt: undefined })
          }}
          onSave={async (next) => {
            await saveFleets(route.cluster, next)
          }}
          onDone={(m) => {
            setDialog(null)
            setPicks([])
            onToast(m)
          }}
        />
      )}
      {editor && admin && (
        <FleetEditor
          seed={editor}
          fleets={defs}
          nodes={nodes}
          now={now}
          onClose={() => setEditor(null)}
          onSave={async (f, isNew) => {
            const next = isNew ? [...defs, f] : defs.map((x) => (x.id === f.id ? f : x))
            await save(next, isNew ? `Created the fleet “${f.name}”.` : `Saved the fleet “${f.name}”.`)
            setEditor(null)
            set({ fleet: f.id, pl: undefined, pt: undefined })
          }}
          onDelete={async (f) => {
            await removeFleet(f)
            setEditor(null)
          }}
        />
      )}
    </>
  )
}
