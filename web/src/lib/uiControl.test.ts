import { act, renderHook } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ACTIONS, ADMIN_VIEW_PAGES, canUndo, checkInput, isUndone, markUndone, REGISTRY, runRequest, setAnswering, setToolOpener, setUIMe, showChanges, TAB_SPECS, undo, viewState, type ActionDef } from './uiControl'
import { holdUnsaved } from './unsaved'
import type { Me } from '../api/types'
import { currentRoute, setRouteForTests, TABS } from './router'
import { tableHandle, tableHandles } from './tables'
import { useTable } from './useTable'
import type { ColumnDef } from './table'

describe('Sherlock opens a tool’s dialog, never a session (TB-42)', () => {
  it('asks the console to open the dialog, filled in, and says when it cannot', () => {
    const asked: unknown[] = []
    expect(() => REGISTRY.open_tool_dialog!(currentRoute(), { tool: 'shell', namespace: 'web', pod: 'api-1' })).toThrow('not available')
    setToolOpener((a) => {
      asked.push(a)
      return null
    })
    const done = REGISTRY.open_tool_dialog!(currentRoute(), { tool: 'debug-container', namespace: 'web', pod: 'api-1', container: 'api', image: 'network', reason: 'distroless' })
    expect(done.dialog).toBe(true)
    expect(asked[0]).toEqual({ tool: 'debug-container', pod: { name: 'api-1', namespace: 'web' }, prefill: { target: 'api', image: 'network', reason: 'distroless', namespace: 'web' } })
    expect(() => REGISTRY.open_tool_dialog!(currentRoute(), { tool: 'shell', pod: 'api-1' })).toThrow('needs the pod and its namespace')
    expect(() => REGISTRY.open_tool_dialog!(currentRoute(), { tool: 'command', namespace: 'web', pod: 'api-1', command: 'rm -rf /' })).toThrow('is not a tool')
    expect(() => REGISTRY.open_tool_dialog!(currentRoute(), { tool: 'node-shell', node: '../etc' })).toThrow('is not a name')
    setToolOpener(() => 'your role does not allow it here')
    expect(() => REGISTRY.open_tool_dialog!(currentRoute(), { tool: 'node-shell', node: 'worker-1' })).toThrow('your role')
    setToolOpener(null)
  })
})

describe('Sherlock’s UI actions are one list (P6U-04, P6U-12)', () => {
  it('the browser registry and the server’s list name the same actions', () => {
    const server = JSON.parse(readFileSync(resolve(__dirname, '../../../server/internal/sherlock/ui_actions.json'), 'utf8')) as { actions: ActionDef[] }
    const names = server.actions.map((a) => a.name)
    expect(ACTIONS.map((a) => a.name)).toEqual(names)
    expect(Object.keys(REGISTRY).sort()).toEqual([...names].sort())
    expect(names).toEqual(['view_state', 'view_open', 'view_filter', 'view_search', 'view_sort', 'view_columns', 'view_details', 'view_highlight', 'view_aiops', 'view_admin', 'open_tool_dialog'])
  })

  it('accepts exactly the tabs, filters and values of the fixture the server’s descriptions are tested against (S-3)', () => {
    const fixture = JSON.parse(readFileSync(resolve(__dirname, '../../../server/internal/sherlock/testdata/ui_tabs.json'), 'utf8')) as { tabs: unknown }
    expect(JSON.parse(JSON.stringify(TAB_SPECS))).toEqual(fixture.tabs)
    expect(Object.keys(TAB_SPECS).sort()).toEqual(TABS.map((t) => t.id).sort())
  })

  it('has nothing that clicks, types, runs script, reads the page, or reaches Watson or shared settings', () => {
    for (const a of ACTIONS) {
      // The one exception (TB-42, amending P6U-12): a tool's dialog, which the person sends.
      if (a.name === 'open_tool_dialog') continue
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

  it('opens the cluster’s tabs with view_open, and Administration only with view_admin (RB-48, SR-12)', () => {
    const open = ACTIONS.find((a) => a.name === 'view_open')!
    const tabs = (open.input_schema.properties as Record<string, { enum?: string[] }>).tab!.enum!
    expect(tabs).not.toContain('admin')
    expect(tabs.sort()).toEqual(TABS.map((t) => t.id).sort())
    setRouteForTests('#/pods')
    expect(() => REGISTRY.view_open!(currentRoute(), { tab: 'admin' })).toThrow('There is no tab "admin"')
    // A page that is neither a tab nor Administration's has no tables to drive.
    setRouteForTests('#/aiops/inbox')
    expect(() => REGISTRY.view_sort!(currentRoute(), { column: 'name', direction: 'asc' })).toThrow('not on a tab')
    // Administration's pages have their tables; with none on screen, it says so.
    setRouteForTests('#/admin/users')
    expect(() => REGISTRY.view_sort!(currentRoute(), { column: 'name', direction: 'asc' })).toThrow('No table on screen')
    expect(() => REGISTRY.view_filter!(currentRoute(), { set: { status: 'x' } })).toThrow('not on a tab')
  })

  it('opens exactly the pages of the fixture the server’s description is tested against (SR-12)', () => {
    const fixture = JSON.parse(readFileSync(resolve(__dirname, '../../../server/internal/sherlock/testdata/ui_admin.json'), 'utf8')) as { pages: Record<string, { item: boolean; search: boolean }> }
    const mine = Object.fromEntries(Object.entries(ADMIN_VIEW_PAGES).map(([id, p]) => [id, { item: !!p.item, search: !!p.table }]))
    expect(mine).toEqual(fixture.pages)
    const a = ACTIONS.find((x) => x.name === 'view_admin')!
    expect((a.input_schema.properties.page as { enum: string[] }).enum).toEqual(Object.keys(ADMIN_VIEW_PAGES))
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
    const files: Record<string, string> = {
      alerts: 'AlertsTab',
      nodes: 'NodesTab',
      fleets: 'FleetsTab',
      namespaces: 'NamespacesTab',
      pods: 'PodsTab',
      helm: 'apps/AppsTab',
      services: 'ServicesTab',
      networks: 'NetworksTab',
      access: 'AccessTab',
      policies: 'PoliciesTab',
    }
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
    REGISTRY.view_filter!(on('nodes'), { set: { err: 'problems' } })
    expect(window.location.hash).toBe('#/nodes?err=problems')
    REGISTRY.view_filter!(on('nodes'), { set: { err: 'errors' } })
    expect(window.location.hash).toBe('#/nodes?err=errors')
    REGISTRY.view_open!(on('alerts'), { tab: 'fleets', filters: { err: 'problems' } })
    expect(window.location.hash).toBe('#/fleets?err=problems')
    REGISTRY.view_filter!(on('alerts'), { set: { acked: 'hide' } })
    expect(window.location.hash).toBe('#/alerts?acked=hide')
    expect(() => REGISTRY.view_filter!(on('nodes'), { set: { err: '1' } })).toThrow('err must be one of all, problems, errors')
  })

  // Asked for the nodes with problems, Sherlock set the filter, saw the one
  // node, then took the filter off to highlight it among every node.
  it('keeps a filter it set in this answer while the table shows rows', () => {
    const nodes = (q: string) => ({ cluster: 'local', view: 'tabs' as const, tab: 'nodes' as const, path: [], params: new URLSearchParams(q) })
    setAnswering('local', 41)
    const one = renderHook(() => useTable({ id: 'nodes', columns: COLS, rows: rowsOf(1), terms, now: 0, defaultSort: { col: 'name', dir: 'asc' } }))
    REGISTRY.view_open!(nodes(''), { tab: 'nodes', filters: { err: 'problems' } })
    expect(window.location.hash).toBe('#/nodes?err=problems')
    expect(() => REGISTRY.view_filter!(nodes('err=problems'), { clear: ['err'] })).toThrow('table shows 1 row: that is the answer')
    expect(() => REGISTRY.view_filter!(nodes('err=problems'), { clear: ['*'] })).toThrow('that is the answer')
    expect(() => REGISTRY.view_open!(nodes('err=problems'), { tab: 'nodes' })).toThrow('that is the answer')
    expect(window.location.hash).toBe('#/nodes?err=problems')
    // A filter the person set may go; so may another tab.
    REGISTRY.view_filter!(nodes('err=problems&role=worker'), { clear: ['role'] })
    REGISTRY.view_open!(nodes('err=problems'), { tab: 'pods' })
    one.unmount()
    // A filter that left nothing to show may go, to correct it.
    setAnswering('local', 42)
    const none = renderHook(() => useTable({ id: 'nodes', columns: COLS, rows: [], terms, now: 0, defaultSort: { col: 'name', dir: 'asc' } }))
    REGISTRY.view_open!(nodes(''), { tab: 'nodes', filters: { err: 'problems' } })
    REGISTRY.view_filter!(nodes('err=problems'), { clear: ['err'] })
    expect(window.location.hash).toBe('#/nodes')
    none.unmount()
    // The next answer may change what the last one set.
    setAnswering('local', 43)
    const again = renderHook(() => useTable({ id: 'nodes', columns: COLS, rows: rowsOf(3), terms, now: 0, defaultSort: { col: 'name', dir: 'asc' } }))
    REGISTRY.view_filter!(nodes('err=problems'), { clear: ['err'] })
    again.unmount()
    setAnswering('local', null)
  })
})

describe('an answer’s Undo (P6U-10)', () => {
  const ask = (answer: number, tab: string) => ({ id: `r${answer}`, window: 'w', answer, action: 'view_open', input: { tab } })
  afterEach(() => {
    setAnswering('local', null)
    vi.unstubAllGlobals()
    window.location.hash = ''
  })

  // Message ids count from the start of a conversation, so two clusters have an answer 2.
  it('keeps the same message id on two clusters apart', async () => {
    window.location.hash = '#/nodes'
    setAnswering('a', 2)
    expect((await runRequest(ask(2, 'pods'), 'a')).applied).toBe(true)
    expect(canUndo('a', 2)).toBe(true)
    expect(canUndo('b', 2)).toBe(false)
    // The other cluster's answer 2 starts clean: no view of A's, no "the person touched it".
    setAnswering('b', 2)
    expect((await runRequest(ask(2, 'nodes'), 'b')).applied).toBe(true)
    expect(canUndo('b', 2)).toBe(true)
    expect(await undo('a', 2)).toBe(true)
    expect(isUndone('a', 2)).toBe(true)
    expect(isUndone('b', 2)).toBe(false)
    expect(canUndo('b', 2)).toBe(true)
    markUndone('b', 3)
    expect(isUndone('a', 3)).toBe(false)
  })

  it('refuses a request from a conversation that is not answering here', async () => {
    setAnswering('a', 7)
    expect((await runRequest(ask(7, 'pods'), 'b')).error).toBe('Sherlock is not answering a question in this window.')
  })

  // A hidden window draws no frames: the result must still go back.
  it('answers from a window whose frames never come', async () => {
    vi.stubGlobal('requestAnimationFrame', () => 0)
    setAnswering('a', 9)
    expect(await runRequest(ask(9, 'pods'), 'a')).toMatchObject({ applied: true })
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

describe('Sherlock points at a case or a run in AIOps (SR-09)', () => {
  beforeEach(() => {
    window.history.replaceState(null, '', '#/pods')
    setRouteForTests('#/pods')
  })

  it('opens the Inbox, a case, the runs and a run, as their links do', () => {
    const open = REGISTRY.view_aiops!
    expect(open(currentRoute(), { page: 'case', id: 'C-12' }).label).toBe('Opened case C-12')
    expect(window.location.hash).toBe('#/aiops/inbox/case/12')
    open(currentRoute(), { page: 'case', id: '7' })
    expect(window.location.hash).toBe('#/aiops/inbox/case/7')
    open(currentRoute(), { page: 'inbox', show: 'all', search: 'web/api' })
    expect(window.location.hash).toBe('#/aiops/inbox?show=all&q=web%2Fapi')
    // The cases the person is involved in (EM-08), as the emails link them.
    expect(open(currentRoute(), { page: 'inbox', show: 'all', who: 'involved' }).label).toBe('Opened AIOps › Inbox, closed cases included, the cases the person is involved in')
    expect(window.location.hash).toBe('#/aiops/inbox?show=all&who=involved')
    open(currentRoute(), { page: 'runs' })
    expect(window.location.hash).toBe('#/aiops/jobs/runs')
    open(currentRoute(), { page: 'run', id: 'run-41' })
    expect(window.location.hash).toBe('#/aiops/jobs/runs/run-41')
    // The view state then names what is open.
    setRouteForTests('#/aiops/jobs/runs/run-41')
    expect(viewState().item).toEqual({ kind: 'run', id: 'run-41' })
  })

  it('stays in the cluster the person is on', () => {
    window.history.replaceState(null, '', '#/c/staging/pods')
    setRouteForTests('#/c/staging/pods')
    REGISTRY.view_aiops!(currentRoute(), { page: 'case', id: 'C-3' })
    expect(window.location.hash).toBe('#/c/staging/aiops/inbox/case/3')
  })

  it('refuses what is not a case or a run, and filters on another page', () => {
    const open = REGISTRY.view_aiops!
    const before = window.location.hash
    expect(() => open(currentRoute(), { page: 'admin' })).toThrow('page must be one of inbox, case, runs, run')
    expect(() => open(currentRoute(), { page: 'case', id: '../../admin/users' })).toThrow('such as C-12')
    expect(() => open(currentRoute(), { page: 'case' })).toThrow('such as C-12')
    expect(() => open(currentRoute(), { page: 'run', id: 'javascript:alert(1)' })).toThrow('such as run-12')
    expect(() => open(currentRoute(), { page: 'run', id: 'run-1/../../x' })).toThrow('such as run-12')
    expect(() => open(currentRoute(), { page: 'inbox', id: 'C-1' })).toThrow('takes no id')
    expect(() => open(currentRoute(), { page: 'runs', search: 'x' })).toThrow('for the inbox page')
    expect(() => open(currentRoute(), { page: 'inbox', show: 'closed' })).toThrow('show must be one of pending, all')
    expect(() => open(currentRoute(), { page: 'inbox', who: 'theirs' })).toThrow('who must be one of all, mine, unassigned, involved')
    expect(() => open(currentRoute(), { page: 'case', id: 'C-1', who: 'mine' })).toThrow('for the inbox page')
    const a = ACTIONS.find((x) => x.name === 'view_aiops')!
    expect(() => checkInput(a, { page: 'case', id: 'C-1', cluster: 'prod' })).toThrow('takes no "cluster"')
    expect(window.location.hash).toBe(before)
  })
})

const ME = (over: Partial<Me> = {}): Me => ({ mode: 'oidc', user: { subject: 's', username: 'ana' }, admin: false, roles: ['viewer'], method: 'oidc', sessionIdleSeconds: 3600, ...over })
const SUPER = ME({ admin: true, roles: ['superAdmin'] })

describe('Sherlock opens Administration and Settings pages the person may open (SR-12, SR-13)', () => {
  beforeEach(() => {
    window.history.replaceState(null, '', '#/pods')
    setRouteForTests('#/pods')
    setUIMe(SUPER)
  })
  afterEach(() => setUIMe(null))
  const open = (input: Record<string, unknown>) => REGISTRY.view_admin!(currentRoute(), input)

  it('opens each page as its own link does, with an item’s drawer, in the cluster the person is on', () => {
    expect(open({ page: 'agents', item: 'sherlock' }).label).toBe('Opened Settings › Agents, sherlock')
    expect(window.location.hash).toBe('#/admin/settings/agents/sherlock')
    open({ page: 'general' })
    expect(window.location.hash).toBe('#/admin/settings')
    open({ page: 'audit-log' })
    expect(window.location.hash).toBe('#/admin/logs/audit')
    open({ page: 'confluence' })
    expect(window.location.hash).toBe('#/admin/settings/issues')
    open({ page: 'providers', item: 'gateway' })
    expect(window.location.hash).toBe('#/admin/settings/providers/gateway')
    open({ page: 'roles', item: 'admin' })
    expect(window.location.hash).toBe('#/admin/roles/admin')
    open({ page: 'home' })
    expect(window.location.hash).toBe('#/')
    // The conversation is the cluster's: its pages open under it.
    window.history.replaceState(null, '', '#/c/staging/pods')
    setRouteForTests('#/c/staging/pods')
    open({ page: 'users', item: 'bob' })
    expect(window.location.hash).toBe('#/c/staging/admin/users/bob')
    open({ page: 'connection' })
    expect(window.location.hash).toBe('#/c/staging/cluster')
    setRouteForTests('#/c/staging/cluster')
    expect(viewState()).toMatchObject({ cluster: 'staging', view: 'cluster' })
    try {
      open({ page: 'home' })
      expect.unreachable()
    } catch (e) {
      expect((e as { link?: string }).link).toBe('#/home')
    }
  })

  it('sets a page’s search once its table is on screen, and reads and sorts the tables there', async () => {
    open({ page: 'users', search: 'ana' })
    setRouteForTests('#/admin/users')
    const { result, unmount } = renderHook(() => useTable({ id: 'admin-users', columns: COLS, rows: rowsOf(5), terms, now: 0, defaultSort: { col: 'name', dir: 'asc' } }))
    await vi.waitFor(() => expect(result.current.query).toBe('ana'))
    expect(viewState().tables?.map((t) => t.id)).toContain('admin-users')
    act(() => {
      REGISTRY.view_sort!(currentRoute(), { table: 'admin-users', column: 'n', direction: 'desc' })
    })
    expect(result.current.sort).toEqual({ col: 'n', dir: 'desc' })
    unmount()
  })

  it('refuses a page the person’s role does not open, and anything but a page', () => {
    const before = window.location.hash
    setUIMe(ME())
    expect(() => open({ page: 'agents' })).toThrow('is for SuperAdmins')
    expect(() => open({ page: 'users', item: 'bob' })).toThrow('is for SuperAdmins')
    expect(() => open({ page: 'knowledge' })).toThrow('is for SuperAdmins')
    expect(window.location.hash).toBe(before)
    expect(open({ page: 'roles' }).label).toBe('Opened Administration › Roles')
    // An Admin sees Knowledge's Added in Mycroft card.
    setUIMe(ME({ roles: ['admin'] }))
    expect(open({ page: 'knowledge' }).label).toBe('Opened Settings › Knowledge')
    setUIMe(ME({ mode: 'none', roles: [] }))
    expect(() => open({ page: 'roles' })).toThrow('Sign-in is off')
    expect(open({ page: 'permissions' }).label).toBe('Opened Administration › Permissions')
    setUIMe(null)
    expect(() => open({ page: 'permissions' })).toThrow('not known')
    setUIMe(SUPER)
    expect(() => open({ page: 'general', item: 'x' })).toThrow('general takes no item')
    expect(() => open({ page: 'agents', item: '../../users' })).toThrow('is not an id')
    expect(() => open({ page: 'sla', search: 'P0' })).toThrow('has no search box')
    expect(() => open({ page: 'https://evil.example' })).toThrow('page must be one of')
    expect(() => open({ page: 'toString' })).toThrow('page must be one of')
  })
})

describe('never away from unsaved changes; Show in 221B applies them later (SR-14, SR-15)', () => {
  beforeEach(() => {
    window.history.replaceState(null, '', '#/admin/settings')
    setRouteForTests('#/admin/settings')
    setUIMe(SUPER)
  })
  afterEach(() => {
    setAnswering('local', null)
    setUIMe(null)
  })
  const ask = (answer: number, action: string, input: unknown) => ({ id: `s${answer}`, window: 'w', answer, action, input })

  it('leaves the view while a form holds unsaved changes, and says so', async () => {
    const release = holdUnsaved('the agent drawer')
    try {
      setAnswering('local', 21)
      const r = await runRequest(ask(21, 'view_admin', { page: 'agents', item: 'sherlock' }), 'local')
      expect(r).toMatchObject({ applied: false, deferred: true })
      expect(window.location.hash).toBe('#/admin/settings')
      // Reading is not moving: view_state still answers.
      expect((await runRequest(ask(21, 'view_state', {}), 'local')).view).toMatchObject({ view: 'admin' })
      // A page the role does not open is refused now, not offered for later.
      setUIMe(ME())
      expect((await runRequest(ask(21, 'view_admin', { page: 'users' }), 'local')).error).toContain('is for SuperAdmins')
      setUIMe(SUPER)
      // Show in 221B asks before leaving the edits, then shows.
      const changes = [{ action: 'view_admin', input: { page: 'agents', item: 'sherlock' } }]
      expect(await showChanges(changes)).toEqual({ applied: [], failed: [], unsaved: ['the agent drawer'] })
      expect(window.location.hash).toBe('#/admin/settings')
      expect((await showChanges(changes, true)).applied).toEqual(['Opened Settings › Agents, sherlock'])
      expect(window.location.hash).toBe('#/admin/settings/agents/sherlock')
    } finally {
      release()
    }
    // Saved or undone, the next change applies at once.
    setAnswering('local', 22)
    expect(await runRequest(ask(22, 'view_admin', { page: 'sla' }), 'local')).toMatchObject({ applied: true })
    expect(window.location.hash).toBe('#/admin/settings/sla')
  })

  // The screen moves only once the answer is done (SR-18): during it a
  // change is checked and held, a page the role does not open is refused at
  // once, and unsaved changes still leave it for Show in 221B.
  it('holds a change asked for during the answer, refusing at once what it never could do', async () => {
    setAnswering('local', 23)
    expect(await runRequest({ ...ask(23, 'view_admin', { page: 'sla' }), hold: true }, 'local')).toEqual({ applied: false, held: true })
    expect(window.location.hash).toBe('#/admin/settings')
    setUIMe(ME())
    expect((await runRequest({ ...ask(23, 'view_admin', { page: 'users' }), hold: true }, 'local')).error).toContain('is for SuperAdmins')
    setUIMe(SUPER)
    const release = holdUnsaved('the agent drawer')
    try {
      expect(await runRequest({ ...ask(23, 'view_admin', { page: 'sla' }), hold: true }, 'local')).toMatchObject({ applied: false, deferred: true })
    } finally {
      release()
    }
    // Sent again once the answer is done, it is made.
    expect(await runRequest(ask(23, 'view_admin', { page: 'sla' }), 'local')).toMatchObject({ applied: true })
    expect(window.location.hash).toBe('#/admin/settings/sla')
  })

  it('applies what was left in order and says what it could not', async () => {
    window.history.replaceState(null, '', '#/pods')
    setRouteForTests('#/pods')
    const r = await showChanges([
      { action: 'view_open', input: { tab: 'nodes', filters: { err: 'errors' } } },
      { action: 'view_state', input: {} },
      { action: 'view_admin', input: { page: 'nowhere' } },
    ])
    expect(r.applied).toEqual(['Opened Nodes with err errors'])
    expect(r.failed[0]).toContain('must be one of')
    expect(window.location.hash).toBe('#/nodes?err=errors')
  })
})

// A change held for the end of the answer (SR-18) is checked on the page
// the answer's held changes will leave open, so the model hears of a wrong
// filter or another cluster at once, and the screen does not move.
describe('a change held for the end of the answer (SR-18)', () => {
  afterEach(() => {
    setAnswering('local', null)
    window.location.hash = ''
  })
  const held = (answer: number, action: string, input: unknown) => ({ id: `h${answer}`, window: 'w', answer, action, input, hold: true })
  const ask = (x: ReturnType<typeof held>) => runRequest(x, 'local')

  it('is checked on the page the held changes before it open', async () => {
    window.history.replaceState(null, '', '#/pods')
    setRouteForTests('#/pods')
    setAnswering('local', 31)
    expect(await ask(held(31, 'view_open', { tab: 'nodes' }))).toEqual({ applied: false, held: true })
    // Nodes is not open yet, but its filters are what the next change is checked against.
    expect(await ask(held(31, 'view_filter', { set: { err: 'problems' } }))).toEqual({ applied: false, held: true })
    expect((await ask(held(31, 'view_filter', { set: { status: 'failing' } }))).error).toBeTruthy()
    const other = await ask(held(31, 'view_open', { tab: 'nodes', cluster: 'staging' }))
    expect(other.error).toContain('does not switch to another cluster')
    expect(other.link).toBe('#/c/staging/nodes')
    expect(window.location.hash).toBe('#/pods')
    // A table of a page not open yet is not there to check: held.
    expect(await ask(held(31, 'view_sort', { table: 'nodes', column: 'name', direction: 'asc' }))).toEqual({ applied: false, held: true })
  })

  it('checks the tables on screen while no held change has moved the page', async () => {
    window.history.replaceState(null, '', '#/pods')
    setRouteForTests('#/pods')
    setAnswering('local', 32)
    expect((await ask(held(32, 'view_sort', { table: 'nowhere', column: 'name', direction: 'asc' }))).error).toContain('No table "nowhere" is on screen')
    expect(window.location.hash).toBe('#/pods')
  })
})
