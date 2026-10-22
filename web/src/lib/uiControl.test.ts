import { act, renderHook } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ACTIONS, checkInput, REGISTRY, TAB_SPECS, viewState, type ActionDef } from './uiControl'
import { currentRoute, setRouteForTests, TABS } from './router'
import { tableHandle, tableHandles } from './tables'
import { useTable } from './useTable'
import type { ColumnDef } from './table'

describe('Sherlock’s UI actions are one list (P6U-04, P6U-12)', () => {
  it('the browser registry and the server’s list name the same actions', () => {
    const server = JSON.parse(readFileSync(resolve(__dirname, '../../../server/internal/sherlock/ui_actions.json'), 'utf8')) as { actions: ActionDef[] }
    const names = server.actions.map((a) => a.name)
    expect(ACTIONS.map((a) => a.name)).toEqual(names)
    expect(Object.keys(REGISTRY).sort()).toEqual([...names].sort())
    expect(names).toEqual(['view_state', 'view_open', 'view_filter', 'view_search', 'view_sort', 'view_columns', 'view_details', 'view_highlight'])
  })

  it('has nothing that clicks, types, runs script, reads the page, or reaches Watson or shared settings', () => {
    for (const a of ACTIONS) {
      expect(a.name).toMatch(/^view_/)
      expect(a.name).not.toMatch(/click|type|script|eval|screenshot|dom|approve|watson|setting|fleet|ack|export|submit|cluster/)
      expect(a.input_schema.additionalProperties).toBe(false)
    }
  })

  it('checks inputs against each action’s schema', () => {
    const sort = ACTIONS.find((a) => a.name === 'view_sort')!
    expect(() => checkInput(sort, { column: 'name' })).toThrow('needs direction')
    expect(() => checkInput(sort, { column: 'name', direction: 'up' })).toThrow('must be one of asc, desc')
    expect(() => checkInput(sort, { column: 'name', direction: 'asc', click: '#approve' })).toThrow('takes no "click"')
    expect(checkInput(sort, { column: 'name', direction: 'asc' })).toEqual({ column: 'name', direction: 'asc' })
    const search = ACTIONS.find((a) => a.name === 'view_search')!
    expect(() => checkInput(search, { text: 'x'.repeat(201) })).toThrow('longer than 200')
  })

  it('cannot reach the Administration area (RB-48)', () => {
    const open = ACTIONS.find((a) => a.name === 'view_open')!
    const tabs = (open.input_schema.properties as Record<string, { enum?: string[] }>).tab!.enum!
    expect(tabs).not.toContain('admin')
    expect(tabs.sort()).toEqual(TABS.map((t) => t.id).sort())
    setRouteForTests('#/pods')
    expect(() => REGISTRY.view_open!(currentRoute(), { tab: 'admin' })).toThrow('There is no tab "admin"')
    // Nothing else drives a page that is not a tab: sorting and searching need one.
    setRouteForTests('#/admin/users')
    expect(() => REGISTRY.view_sort!(currentRoute(), { column: 'name', direction: 'asc' })).toThrow('not on a tab')
  })

  it('declares the filters of every tab', () => {
    expect(Object.keys(TAB_SPECS).sort()).toEqual(TABS.map((t) => t.id).sort())
    for (const spec of Object.values(TAB_SPECS)) expect(spec.filters).not.toHaveProperty('peek')
  })

  // A filter a tab reads but Sherlock cannot set left it showing every
  // node when asked for the nodes with problems: each one a tab reads
  // from its URL is declared, but open (the row whose pane is open, which
  // view_details opens) and the search boxes, which view_search sets.
  it('declares every filter a tab reads from its URL', () => {
    const files: Record<string, string> = { alerts: 'AlertsTab', nodes: 'NodesTab', fleets: 'FleetsTab', namespaces: 'NamespacesTab', pods: 'PodsTab', helm: 'HelmTab', services: 'ServicesTab', networks: 'NetworksTab', access: 'AccessTab', policies: 'PoliciesTab' }
    for (const [tab, file] of Object.entries(files)) {
      const src = readFileSync(resolve(__dirname, `../components/${file}.tsx`), 'utf8')
      const read = new Set([...src.matchAll(/(?:\.get\(|listParam\(\w+, )'([a-z]+)'/g)].map((m) => m[1]!))
      const spec = TAB_SPECS[tab as keyof typeof TAB_SPECS]
      const searched = new Set(['open', spec.search, tab === 'access' ? 'tq' : undefined])
      for (const name of read) if (!searched.has(name)) expect(spec.filters, `${tab} reads ${name}`).toHaveProperty(name)
    }
  })
})

describe('the actions', () => {
  beforeEach(() => {
    window.history.replaceState(null, '', '#/pods?status=failing')
    setRouteForTests('#/pods?status=failing')
  })

  it('refuses an unknown filter, value or tab, and another cluster with a link (P6U-04, P6U-06)', () => {
    const route = () => ({ cluster: 'local', view: 'tabs' as const, tab: 'pods' as const, path: [], params: new URLSearchParams('status=failing') })
    expect(() => REGISTRY.view_filter!(route(), { set: { open: 'x' } })).toThrow('has no filter "open"')
    expect(() => REGISTRY.view_filter!(route(), { set: { status: 'broken' } })).toThrow('status must be one of')
    expect(() => REGISTRY.view_open!(route(), { tab: 'https://evil.example' })).toThrow('There is no tab')
    expect(() => REGISTRY.view_open!(route(), { tab: 'helm', path: ['javascript:alert(1)', 'x'] })).toThrow('is not a name')
    try {
      REGISTRY.view_open!(route(), { tab: 'nodes', cluster: 'staging' })
      expect.unreachable()
    } catch (e) {
      expect((e as { link?: string }).link).toBe('#/c/staging/nodes')
    }
    expect(window.location.hash).toBe('#/pods?status=failing')
  })

  it('shows only the nodes with errors, and hides acknowledged alerts', () => {
    const on = (tab: 'nodes' | 'fleets' | 'alerts') => ({ cluster: 'local', view: 'tabs' as const, tab, path: [], params: new URLSearchParams() })
    REGISTRY.view_filter!(on('nodes'), { set: { err: '1' } })
    expect(window.location.hash).toBe('#/nodes?err=1')
    REGISTRY.view_open!(on('alerts'), { tab: 'fleets', filters: { err: '1' } })
    expect(window.location.hash).toBe('#/fleets?err=1')
    REGISTRY.view_filter!(on('alerts'), { set: { acked: 'hide' } })
    expect(window.location.hash).toBe('#/alerts?acked=hide')
    expect(() => REGISTRY.view_filter!(on('nodes'), { set: { err: 'yes' } })).toThrow('err is "1" or ""')
  })
})

interface Row {
  name: string
  ns: string
  n: number
}

const COLS: ColumnDef<Row>[] = [
  { id: 'name', label: 'Name', width: '1fr', min: 80, sortValue: (r) => r.name, cell: (r) => r.name, export: (r) => [{ head: 'Name', cell: r.name }] },
  { id: 'namespace', label: 'Namespace', width: '1fr', min: 80, sortValue: (r) => r.ns, cell: (r) => r.ns, export: (r) => [{ head: 'Namespace', cell: r.ns }] },
  { id: 'n', label: 'Number', width: '80px', min: 60, sortValue: (r) => r.n, cell: (r) => r.n, export: (r) => [{ head: 'Number', cell: r.n }] },
  { id: 'extra', label: 'Extra', width: '80px', min: 60, hiddenByDefault: true, sortValue: (r) => r.n, cell: (r) => r.n, export: (r) => [{ head: 'Extra', cell: r.n }] },
]
const terms = (r: Row) => [{ kind: 'name', text: r.name }]
const rowsOf = (n: number): Row[] => Array.from({ length: n }, (_, i) => ({ name: `pod-${i}`, ns: i % 2 ? 'web' : 'batch', n: (i * 7919) % 10007 }))

describe('every table registers under its id (P6U-01, P6U-11)', () => {
  afterEach(() => localStorage.clear())

  it('reads and sets sort, search and columns from outside; Sherlock’s columns are not saved', () => {
    const rows = rowsOf(30)
    const { result, unmount } = renderHook(() => useTable({ id: 'test-pods', columns: COLS, rows, terms, now: 0, defaultSort: { col: 'name', dir: 'asc' } }))
    const t = tableHandle('test-pods')!
    expect(tableHandles().map((h) => h.id)).toContain('test-pods')
    expect(t.sort()).toEqual({ col: 'name', dir: 'asc' })
    expect(t.names(2)).toEqual(['batch/pod-0', 'web/pod-1'])
    act(() => t.setSort({ col: 'n', dir: 'desc' }))
    expect(result.current.sort).toEqual({ col: 'n', dir: 'desc' })
    act(() => t.setQuery('pod-1'))
    expect(result.current.query).toBe('pod-1')
    expect(t.shown()).toBe(11)
    act(() => t.setOverride(new Set(['n'])))
    expect(result.current.cols.map((c) => c.id)).toEqual(['name', 'namespace', 'extra'])
    expect(localStorage.getItem('221b.test-pods.hidden')).toBeNull()
    expect(t.find('web/pod-1')).toBe(rows[1])
    // The person's own choice replaces Sherlock's, and is saved.
    act(() => result.current.setHidden(new Set(['extra'])))
    expect(t.override()).toBeNull()
    expect(localStorage.getItem('221b.test-pods.hidden')).toContain('extra')
    unmount()
    expect(tableHandle('test-pods')).toBeUndefined()
  })

  it('sorts and searches 10,000 rows through the registry in under 200 ms (P6U-20)', () => {
    const rows = rowsOf(10_000)
    renderHook(() => useTable({ id: 'test-big', columns: COLS, rows, terms, now: 0, defaultSort: { col: 'name', dir: 'asc' } }))
    const t = tableHandle('test-big')!
    let start = performance.now()
    act(() => t.setSort({ col: 'n', dir: 'desc' }))
    expect(performance.now() - start).toBeLessThan(200)
    start = performance.now()
    act(() => t.setQuery('pod-99'))
    expect(performance.now() - start).toBeLessThan(200)
    expect(t.shown()).toBe(111)
  })

  it('puts the tables on screen in the view state, within 2,000 characters (P6U-02)', () => {
    const rows = rowsOf(500)
    setRouteForTests('#/pods?ns=web')
    renderHook(() => useTable({ id: 'test-state', columns: COLS, rows, terms, now: 0, defaultSort: { col: 'name', dir: 'asc' } }))
    const v = viewState()
    expect(v.tab).toBe('pods')
    expect(v.filters).toEqual({ ns: 'web' })
    const tbl = v.tables!.find((x) => x.id === 'test-state')!
    expect(tbl.rows).toBe('500 of 500')
    expect(tbl.hidden).toEqual(['extra'])
    expect(JSON.stringify(v).length).toBeLessThanOrEqual(2000)
  })
})
