// Sherlock's UI actions in the browser (P6U-02 to P6U-13): the view state
// it reads, and the actions that change the asking person's own view. The
// list of actions is the one the server offers the model
// (server/internal/sherlock/ui_actions.json); this file holds what each
// does here and what each tab accepts. No action clicks, types, reads the
// page or fetches anything: they set the route and the tables' state, the
// way the person's own clicks do, so what a tab then shows comes through
// the same access-filtered API (P6U-07).

import actionsFile from '../../../server/internal/sherlock/ui_actions.json'
import { AC_RES, AC_VERBS } from './rbac'
import { parsePeek, peekParam, type Peek } from './peek'
import { adminHref, aiopsHref, clusterHref, clusterPageHref, currentRoute, LOCAL, navigate, onRouteChange, parseHash, TABS, withParams, type AdminPage, type Route, type Tab } from './router'
import { OBJECT_KINDS } from './sherlock'
import { drive, isDriving, onUserChange, queueQuery, setHighlight, tableHandle, tableHandles, type TableHandle } from './tables'
import { hasUnsaved, unsavedWhat } from './unsaved'
import { isSuperAdmin, pagesFor, seesAddedInMycroft } from './adminAccess'
import type { Me } from '../api/types'
import type { SortState } from './table'
import { currentAIOpsItem, type AIOpsItem } from './aiopsItem'

// ---- the list ----

export interface ActionDef {
  name: string
  changes: boolean
  description: string
  input_schema: {
    type: 'object'
    properties: Record<string, Schema>
    required: string[]
    additionalProperties: false
  }
}

interface Schema {
  type?: string | string[]
  enum?: string[]
  items?: Schema
  maxLength?: number
  additionalProperties?: Schema | boolean
  description?: string
}

export const ACTIONS: ActionDef[] = (actionsFile as unknown as { actions: ActionDef[] }).actions

// ---- what each tab accepts ----

type FilterSpec = { type: 'enum'; values: readonly string[]; none?: string } | { type: 'list' } | { type: 'text' } | { type: 'flag' }

interface TabSpec {
  filters: Record<string, FilterSpec>
  /** The URL parameter of the tab's main search box, if it has one. */
  search?: string
  /** The path segments the tab takes. */
  path?: (FilterSpec & { name: string })[]
}

const ROLE: FilterSpec = {
  type: 'enum',
  values: ['all', 'control-plane', 'worker'],
  none: 'all',
}
const PROBLEMS: FilterSpec = {
  type: 'enum',
  values: ['all', 'problems', 'errors'],
  none: 'all',
}

/** The filters each tab reads from its URL, as the tabs declare them (P6U-04). */
export const TAB_SPECS: Record<Tab, TabSpec> = {
  alerts: {
    search: 'q',
    filters: {
      kind: {
        type: 'enum',
        values: ['all', 'pods', 'nodes', 'volumes'],
        none: 'all',
      },
      show: { type: 'enum', values: ['active', 'all'], none: 'active' },
      acked: { type: 'enum', values: ['show', 'hide'], none: 'show' },
      by: { type: 'enum', values: ['node', 'kind'], none: 'node' },
    },
  },
  nodes: {
    search: 'q',
    filters: {
      role: ROLE,
      err: PROBLEMS,
      label: { type: 'list' },
      taint: { type: 'list' },
      fleet: { type: 'list' },
    },
  },
  pods: {
    search: 'q',
    filters: {
      role: ROLE,
      status: {
        type: 'enum',
        values: ['all', 'running', 'notready', 'pending', 'failing', 'completed'],
        none: 'all',
      },
      ns: { type: 'list' },
      node: { type: 'list' },
      label: { type: 'list' },
      taint: { type: 'list' },
    },
  },
  services: {
    search: 'q',
    filters: {
      ns: { type: 'text' },
      type: {
        type: 'enum',
        values: ['all', 'ClusterIP', 'Headless', 'NodePort', 'LoadBalancer', 'notReady'],
        none: 'all',
      },
    },
  },
  namespaces: {
    search: 'q',
    filters: { kinds: { type: 'list' } },
    path: [{ name: 'namespace', type: 'text' }],
  },
  helm: {
    search: 'q',
    filters: {
      tab: {
        type: 'enum',
        values: ['overview', 'pods', 'workloads', 'storage', 'network', 'roles', 'other', 'values', 'notes', 'history', 'images'],
        none: 'overview',
      },
    },
    path: [
      { name: 'namespace', type: 'text' },
      { name: 'release', type: 'text' },
    ],
  },
  storage: {
    search: 'q',
    filters: {
      ns: { type: 'list' },
      node: { type: 'list' },
      class: { type: 'list' },
      attention: { type: 'flag' },
      label: { type: 'list' },
    },
  },
  fleets: {
    search: 'q',
    filters: {
      fleet: { type: 'list' },
      pl: { type: 'list' },
      pt: { type: 'list' },
      group: { type: 'enum', values: ['fleet', 'none'], none: 'fleet' },
      err: PROBLEMS,
    },
  },
  // Automation keeps its search in the page.
  scripts: {
    filters: {
      state: { type: 'enum', values: ['review', 'draft', 'approved', 'unapproved'] },
    },
  },
  'run-history': { filters: {} },
  variables: { filters: {} },
  'run-profiles': { filters: {} },
  networks: { filters: { route: { type: 'text' } } },
  access: {
    search: 'q',
    filters: {
      kind: {
        type: 'enum',
        values: ['all', 'users', 'groups', 'sas'],
        none: 'all',
      },
      verb: { type: 'enum', values: AC_VERBS, none: 'get' },
      res: { type: 'enum', values: AC_RES.map((r) => r.res), none: 'secrets' },
      wns: { type: 'text' },
      tbl: { type: 'enum', values: ['bindings', 'roles'], none: 'bindings' },
      sys: { type: 'flag' },
      defaults: { type: 'flag' },
      id: { type: 'text' },
      scope: { type: 'text' },
    },
  },
  policies: {
    filters: {
      from: { type: 'text' },
      to: { type: 'text' },
      port: { type: 'text' },
    },
    path: [
      {
        name: 'section',
        type: 'enum',
        // list_policies' section ids, and prio, the tab's own for
        // priority (S-5).
        values: ['netpol', 'pdb', 'drain', 'quota', 'limits', 'psa', 'webhooks', 'priority', 'prio'],
      },
    ],
  },
}

const TAB_IDS = new Set<string>(TABS.map((t) => t.id))

export class ActionError extends Error {
  link?: string
  constructor(message: string, link?: string) {
    super(message)
    if (link) this.link = link
  }
}

const MAX_TEXT = 200
// Control characters, and markup or addresses where a name is expected:
// values are data, matched against rows, never HTML or a link (P6U-13).
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f]/
const NAME = /^[A-Za-z0-9][A-Za-z0-9._:/=@-]{0,252}$/

function text(v: unknown, what: string, max = MAX_TEXT): string {
  if (typeof v !== 'string') throw new ActionError(`${what} must be text.`)
  if (v.length > max) throw new ActionError(`${what} is longer than ${max} characters.`)
  if (CONTROL.test(v)) throw new ActionError(`${what} has control characters.`)
  return v
}

function filterValue(tab: Tab, name: string, v: unknown): string | undefined {
  const spec = TAB_SPECS[tab].filters[name]
  if (!spec) {
    const known = Object.keys(TAB_SPECS[tab].filters)
    throw new ActionError(`The ${tab} tab has no filter ${JSON.stringify(name)}${known.length ? `; it has ${known.join(', ')}` : ''}.`)
  }
  switch (spec.type) {
    case 'enum': {
      const s = text(v, name)
      if (!spec.values.includes(s)) throw new ActionError(`${name} must be one of ${spec.values.join(', ')}.`)
      return s === spec.none ? undefined : s
    }
    case 'flag': {
      const s = v === true ? '1' : v === false ? '' : text(v, name)
      if (s !== '' && s !== '1') throw new ActionError(`${name} is "1" or "".`)
      return s || undefined
    }
    case 'text': {
      const s = text(Array.isArray(v) ? v.join(',') : v, name).trim()
      return s || undefined
    }
    case 'list': {
      const items = (Array.isArray(v) ? v : typeof v === 'string' ? v.split(',') : null)?.map((x) => text(x, name).trim()).filter(Boolean)
      if (!items) throw new ActionError(`${name} is a list of values.`)
      if (items.join(',').length > MAX_TEXT) throw new ActionError(`${name} is longer than ${MAX_TEXT} characters.`)
      return items.length ? items.join(',') : undefined
    }
  }
}

function listOf(v: unknown, what: string): string[] {
  if (v === undefined) return []
  if (!Array.isArray(v)) throw new ActionError(`${what} is a list.`)
  return v.map((x) => text(x, what))
}

/** Checks an action's input against its schema: known fields only, of the right types (P6U-04). */
export function checkInput(a: ActionDef, input: unknown): Record<string, unknown> {
  if (input === null || typeof input !== 'object' || Array.isArray(input)) throw new ActionError('The input must be an object.')
  const obj = input as Record<string, unknown>
  for (const k of Object.keys(obj)) if (!(k in a.input_schema.properties)) throw new ActionError(`${a.name} takes no ${JSON.stringify(k)}; it takes ${Object.keys(a.input_schema.properties).join(', ') || 'nothing'}.`)
  for (const k of a.input_schema.required) if (obj[k] === undefined) throw new ActionError(`${a.name} needs ${k}.`)
  for (const [k, sch] of Object.entries(a.input_schema.properties)) {
    const v = obj[k]
    if (v === undefined) continue
    const types = Array.isArray(sch.type) ? sch.type : [sch.type]
    const ok = types.some((t) => (t === 'array' ? Array.isArray(v) : t === 'object' ? typeof v === 'object' && v !== null && !Array.isArray(v) : typeof v === t))
    if (!ok) throw new ActionError(`${k} must be ${types.join(' or ')}.`)
    if (sch.enum && !sch.enum.includes(v as string)) throw new ActionError(`${k} must be one of ${sch.enum.join(', ')}.`)
    if (sch.maxLength && typeof v === 'string' && v.length > sch.maxLength) throw new ActionError(`${k} is longer than ${sch.maxLength} characters.`)
  }
  return obj
}

// ---- the view state (P6U-02) ----

export interface TableState {
  id: string
  search?: string
  sort: string
  columns: string[]
  hidden?: string[]
  rows: string
  names: string[]
}

export interface ViewState {
  cluster: string
  view: Route['view']
  tab?: Tab
  path?: string[]
  filters?: Record<string, string>
  tables?: TableState[]
  details?: { open: string; state: 'open' | 'loading' | 'not available' }
  /** In Cases (IB-11): the item open, with its cluster and a line about it. */
  item?: AIOpsItem
}

function tableState(t: TableHandle, names: number): TableState {
  const sort = t.sort()
  const hidden = t.hidden()
  const out: TableState = {
    id: t.id,
    sort: `${sort.col} ${sort.dir}`,
    columns: t.columns.filter((c) => c.first || !hidden.has(c.id)).map((c) => c.id),
    rows: `${t.shown()} of ${t.total()}`,
    names: t.names(names),
  }
  const hid = t.columns.filter((c) => !c.first && hidden.has(c.id)).map((c) => c.id)
  if (hid.length) out.hidden = hid
  const q = t.query()
  if (q) out.search = q
  return out
}

function detailsState(peek: string): ViewState['details'] {
  if (typeof document === 'undefined') return { open: peek, state: 'open' }
  const pane = document.querySelector('[data-peek-pane]')
  if (!pane || !pane.firstElementChild) return { open: peek, state: 'loading' }
  // What a pane says when it could not show the object stays on the screen:
  // Sherlock hears only that it is not available (P6U-07).
  if (pane.querySelector('[data-peek-error]') || pane.hasAttribute('data-peek-error')) return { open: peek, state: 'not available' }
  return {
    open: peek,
    state: pane.querySelector('[data-peek-loading]') ? 'loading' : 'open',
  }
}

/** The view state, at most max characters of JSON (P6U-03). */
export function viewState(route: Route = currentRoute(), max = 2000): ViewState {
  const base: ViewState = { cluster: route.cluster, view: route.view }
  if (route.view === 'tabs') {
    base.tab = route.tab
    if (route.path.length) base.path = route.path
    const f: Record<string, string> = {}
    for (const [k, v] of route.params.entries()) if (k !== 'peek' && v) f[k] = v
    if (Object.keys(f).length) base.filters = f
    const peek = route.params.get('peek')
    if (peek && parsePeek(peek)) base.details = detailsState(peek)
  }
  // --- Sherlock in Cases (Phase 12, IB-11) ---
  // The page of the Cases area, its filters, and the item open there.
  if (route.view === 'aiops') {
    if (route.path.length) base.path = route.path
    const f: Record<string, string> = {}
    for (const [k, v] of route.params.entries()) if (v) f[k] = v
    if (Object.keys(f).length) base.filters = f
    const item = currentAIOpsItem(route)
    if (item) base.item = { ...item, summary: item.summary?.slice(0, 300) }
  }
  // --- end Sherlock in Cases ---
  // Administration and Settings (SR-12): the page, its item and its tables.
  if (route.view === 'admin' || route.view === 'cluster') {
    if (route.path.length) base.path = route.path
    const f: Record<string, string> = {}
    for (const [k, v] of route.params.entries()) if (v) f[k] = v
    if (Object.keys(f).length) base.filters = f
  }
  const handles = route.view === 'tabs' || route.view === 'admin' ? tableHandles() : []
  // Fewer row names, then fewer tables, until it fits.
  for (const names of [50, 25, 12, 6, 3, 0]) {
    for (let n = handles.length; n >= 0; n--) {
      const v: ViewState = { ...base }
      if (n) v.tables = handles.slice(0, n).map((t) => tableState(t, names))
      if (JSON.stringify(v).length <= max) return v
    }
  }
  return base
}

// ---- the actions ----

export interface Applied {
  /** What changed, for the notice under the answer and for screen readers. */
  label: string
  /** For view_details: wait for the pane to settle. */
  details?: boolean
  /** A dialog opened: focus goes to it, not back where it was. */
  dialog?: boolean
}

/** What open_tool_dialog asks the console to open (TB-42). */
export interface ToolDialogAsk {
  tool: string
  pod?: { name: string; namespace: string }
  node?: string
  prefill: Record<string, unknown>
}

let toolOpener: ((a: ToolDialogAsk) => string | null) | null = null

/**
 * The console registers how it opens a tool's dialog; the function answers
 * why not, or null when it opened. Unregistered, the Toolbox is not here.
 */
export function setToolOpener(f: ((a: ToolDialogAsk) => string | null) | null): void {
  toolOpener = f
}

const POD_TOOLS = ['shell', 'forward', 'debug-container']
const TOOLS = [...POD_TOOLS, 'debug-pod', 'node-shell']

function openToolDialog(_route: Route, input: Record<string, unknown>): Applied {
  const tool = text(input.tool, 'tool')
  if (!TOOLS.includes(tool)) throw new ActionError(`${tool} is not a tool: ${TOOLS.join(', ')}.`)
  if (!toolOpener) throw new ActionError('The Toolbox is not available in this console window: its tools need Watson, which is off here.')
  const name = (k: string) => {
    const v = input[k]
    if (v === undefined || v === '') return ''
    const s = text(v, k).trim()
    if (!NAME.test(s) || s.includes('/')) throw new ActionError(`${k} is not a name.`)
    return s
  }
  const ns = name('namespace')
  const pod = name('pod')
  const node = name('node')
  const prefill: Record<string, unknown> = {}
  for (const k of ['container', 'image', 'profile', 'labelsFrom']) if (name(k)) prefill[k === 'container' && tool === 'debug-container' ? 'target' : k] = name(k)
  if (typeof input.reason === 'string') prefill.reason = input.reason
  if (typeof input.port === 'number') {
    if (!Number.isInteger(input.port) || input.port < 1 || input.port > 65535) throw new ActionError('port is 1 to 65535.')
    prefill.port = input.port
    prefill.localPort = input.port
  }
  if (ns) prefill.namespace = ns
  let ask: ToolDialogAsk
  if (POD_TOOLS.includes(tool)) {
    if (!pod || !ns) throw new ActionError(`${tool} needs the pod and its namespace.`)
    ask = { tool, pod: { name: pod, namespace: ns }, prefill }
  } else {
    if (!node) throw new ActionError(`${tool} needs the node.`)
    if (tool === 'debug-pod' && !ns) throw new ActionError('debug-pod needs the namespace it runs in.')
    ask = { tool, node, prefill }
  }
  if (planned !== null) return { label: '' }
  const refused = toolOpener(ask)
  if (refused) throw new ActionError(refused)
  const what = ask.pod ? `pod ${ask.pod.namespace}/${ask.pod.name}` : `node ${ask.node}`
  return {
    label: `Opened the ${tool} dialog for ${what}, filled in; nothing is sent until the person presses its button`,
    dialog: true,
  }
}

function needTabs(route: Route): Tab {
  if (route.view !== 'tabs') throw new ActionError('The person is not on a tab; use view_open first.')
  return route.tab
}

function tableFor(route: Route, id: unknown, what: 'sort' | 'search' | 'columns'): TableHandle {
  // The tables of Administration's pages too: Users, Roles, Permissions (SR-12).
  if (route.view !== 'admin') needTabs(route)
  const all = tableHandles().filter((t) => t.drives.has(what))
  if (id !== undefined) {
    const t = tableHandle(text(id, 'table'))
    if (!t)
      throw new ActionError(
        `No table ${JSON.stringify(id)} is on screen; the tables are ${
          tableHandles()
            .map((x) => x.id)
            .join(', ') || 'none'
        }.`,
      )
    if (!t.drives.has(what)) throw new ActionError(`Table ${t.id} cannot be ${what === 'search' ? 'searched' : what === 'sort' ? 'sorted' : 'changed'} this way.`)
    return t
  }
  const t = all[0]
  if (!t) throw new ActionError('No table on screen can do that.')
  return t
}

function describeFilters(f: Record<string, string | undefined>): string {
  const parts = Object.entries(f)
    .filter(([, v]) => v)
    .map(([k, v]) => `${k} ${v}`)
  return parts.length ? ` with ${parts.join(', ')}` : ''
}

function tabLabel(tab: Tab): string {
  return TABS.find((t) => t.id === tab)?.label ?? tab
}

const CLUSTER_ID = /^[a-z0-9][a-z0-9-]{0,62}$/

// ---- checking a change held for the end of the answer (SR-18) ----

/**
 * While a change is checked to be held, the address it would go to; null
 * while changes are made. The actions go through go, which then notes the
 * address instead of going, and touch no table.
 */
let planned: string | null = null

/** Goes to an address, or, while a held change is checked, notes it. */
function go(hash: string, replace = false): void {
  if (planned !== null) {
    planned = hash
    return
  }
  navigate(hash, replace)
}

// ---- what Sherlock narrowed in this answer ----

/**
 * The filters and searches Sherlock set in the answer it is giving, by
 * tab. Asked for the nodes with problems, it set the filter, saw the one
 * node it should show, then took the filter off to highlight that node
 * among all of them: a filter it set stays while the table shows rows.
 */
const narrowed = new Map<string, Map<Tab, Set<string>>>()

function noteNarrowed(tab: Tab, f: Record<string, string | undefined>) {
  if (answering === null || planned !== null) return
  let byTab = narrowed.get(answering)
  if (!byTab) narrowed.set(answering, (byTab = new Map()))
  const keys = byTab.get(tab) ?? new Set<string>()
  for (const [k, v] of Object.entries(f)) if (v) keys.add(k)
  byTab.set(tab, keys)
}

/** Refuses taking off a filter this answer set while the table shows rows: they are the answer. One that left nothing to show may go. */
function keepNarrowed(tab: Tab, removed: string[]) {
  if (answering === null || planned !== null) return
  const mine = narrowed.get(answering)?.get(tab)
  const hit = removed.filter((k) => mine?.has(k))
  if (!hit.length) return
  const shown =
    tableHandles()
      .find((t) => t.drives.has('search'))
      ?.shown() ?? 0
  if (!shown) return
  throw new ActionError(`This answer set ${hit.join(', ')} to show what was asked, and the table shows ${shown} ${shown === 1 ? 'row' : 'rows'}: that is the answer. Leave the filter as it is; to point at a row, highlight it in this view.`)
}

function viewOpen(route: Route, input: Record<string, unknown>): Applied {
  const tab = text(input.tab, 'tab')
  if (!TAB_IDS.has(tab)) throw new ActionError(`There is no tab ${JSON.stringify(tab)}.`)
  const t = tab as Tab
  const spec = TAB_SPECS[t]
  const path = listOf(input.path, 'path')
  if (path.length > (spec.path?.length ?? 0)) throw new ActionError(`The ${t} tab takes ${spec.path?.length ? `a path of ${spec.path.map((p) => p.name).join(', ')}` : 'no path'}.`)
  path.forEach((seg, i) => {
    const ps = spec.path![i]!
    if (ps.type === 'enum' && !ps.values.includes(seg)) throw new ActionError(`${ps.name} must be one of ${ps.values.join(', ')}.`)
    if (!NAME.test(seg) || seg.includes('://')) throw new ActionError(`${ps.name} is not a name.`)
  })
  const filters: Record<string, string | undefined> = {}
  const given = input.filters
  if (given !== undefined) {
    if (typeof given !== 'object' || given === null || Array.isArray(given)) throw new ActionError('filters is an object of names and values.')
    for (const [k, v] of Object.entries(given)) {
      if (k === spec.search) filters[k] = text(v, k).trim() || undefined
      else filters[k] = filterValue(t, k, v)
    }
  }
  if (route.view === 'tabs' && route.tab === t)
    keepNarrowed(
      t,
      [...route.params.keys()].filter((k) => route.params.get(k) && !filters[k]),
    )
  if (input.cluster !== undefined) {
    const c = text(input.cluster, 'cluster').trim()
    if (c && c !== route.cluster) {
      // A conversation belongs to one cluster (P6U-06): offer a link.
      const link = CLUSTER_ID.test(c) || c === LOCAL ? clusterHref(c, t, path, filters) : undefined
      throw new ActionError('Sherlock does not switch to another cluster, since this conversation belongs to this one. Offer the person the link instead.', link)
    }
  }
  go(clusterHref(route.cluster, t, path, filters))
  noteNarrowed(t, filters)
  return {
    label: `Opened ${tabLabel(t)}${path.length ? ' ' + path.join('/') : ''}${describeFilters(filters)}`,
  }
}

function viewFilter(route: Route, input: Record<string, unknown>): Applied {
  const tab = needTabs(route)
  const spec = TAB_SPECS[tab]
  const changes: Record<string, string | undefined> = {}
  const clear = listOf(input.clear, 'clear')
  if (clear.includes('*')) for (const k of Object.keys(spec.filters)) changes[k] = undefined
  else
    for (const k of clear) {
      if (!spec.filters[k]) throw new ActionError(`The ${tab} tab has no filter ${JSON.stringify(k)}.`)
      changes[k] = undefined
    }
  const set = input.set
  if (set !== undefined) {
    if (typeof set !== 'object' || set === null || Array.isArray(set)) throw new ActionError('set is an object of names and values.')
    for (const [k, v] of Object.entries(set)) changes[k] = filterValue(tab, k, v)
  }
  const add = input.add
  if (add !== undefined) {
    if (typeof add !== 'object' || add === null || Array.isArray(add)) throw new ActionError('add is an object of names and lists.')
    for (const [k, v] of Object.entries(add)) {
      if (spec.filters[k]?.type !== 'list') throw new ActionError(`${k} is not a list filter of the ${tab} tab.`)
      const have = (changes[k] ?? route.params.get(k) ?? '').split(',').filter(Boolean)
      const more = (filterValue(tab, k, v) ?? '').split(',').filter(Boolean)
      changes[k] = [...new Set([...have, ...more])].join(',') || undefined
    }
  }
  if (!Object.keys(changes).length) throw new ActionError('Nothing to change: give set, add or clear.')
  keepNarrowed(
    tab,
    Object.keys(changes).filter((k) => !changes[k]),
  )
  go(withParams(route, changes), true)
  noteNarrowed(tab, changes)
  const parts = Object.entries(changes).map(([k, v]) => (v ? `${k} ${v}` : `cleared ${k}`))
  return { label: `Filtered ${tabLabel(tab)}: ${parts.join(', ')}` }
}

function viewSearch(route: Route, input: Record<string, unknown>): Applied {
  const q = text(input.text, 'text').trim()
  const tab = route.view === 'admin' ? null : needTabs(route)
  if (tab && input.table === undefined) {
    const param = TAB_SPECS[tab].search
    if (param) {
      if (!q) keepNarrowed(tab, [param])
      go(withParams(route, { [param]: q || undefined }), true)
      noteNarrowed(tab, { [param]: q || undefined })
      return {
        label: q ? `Searched ${tabLabel(tab)} for “${q}”` : `Cleared the search on ${tabLabel(tab)}`,
      }
    }
  }
  const t = tableFor(route, input.table, 'search')
  if (planned !== null) return { label: '' }
  t.setQuery(q)
  return {
    label: q ? `Searched ${t.id} for “${q}”` : `Cleared the search of ${t.id}`,
  }
}

function viewSort(route: Route, input: Record<string, unknown>): Applied {
  const t = tableFor(route, input.table, 'sort')
  const col = text(input.column, 'column')
  const c = t.columns.find((x) => x.id === col)
  if (!c) throw new ActionError(`Table ${t.id} has no column ${JSON.stringify(col)}; its columns are ${t.columns.map((x) => x.id).join(', ')}.`)
  const dir = input.direction === 'desc' ? 'desc' : 'asc'
  if (planned !== null) return { label: '' }
  t.setSort({ col, dir } satisfies SortState)
  return {
    label: `Sorted ${t.id} by ${c.label}, ${dir === 'asc' ? 'ascending' : 'descending'}`,
  }
}

function viewColumns(route: Route, input: Record<string, unknown>): Applied {
  const t = tableFor(route, input.table, 'columns')
  const show = listOf(input.show, 'show')
  const hide = listOf(input.hide, 'hide')
  if (!show.length && !hide.length) throw new ActionError('Give columns to show or hide.')
  const ids = new Map(t.columns.map((c) => [c.id, c]))
  for (const id of [...show, ...hide]) if (!ids.has(id)) throw new ActionError(`Table ${t.id} has no column ${JSON.stringify(id)}; its columns are ${[...ids.keys()].join(', ')}.`)
  for (const id of hide) if (ids.get(id)!.first) throw new ActionError(`The first column of ${t.id} always shows.`)
  const next = new Set(t.hidden())
  for (const id of show) next.delete(id)
  for (const id of hide) next.add(id)
  if (planned !== null) return { label: '' }
  // Until reload or the person's own choice, never saved (P6U-11).
  t.setOverride(next)
  const label = (ids2: string[]) => ids2.map((id) => ids.get(id)!.label).join(', ')
  return {
    label: [show.length && `Showed ${label(show)}`, hide.length && `hid ${label(hide)}`].filter(Boolean).join('; ') + ` in ${t.id}`,
  }
}

function viewDetails(route: Route, input: Record<string, unknown>): Applied {
  needTabs(route)
  if (input.close === true) {
    go(withParams(route, { peek: undefined }), true)
    return { label: 'Closed the details' }
  }
  const kind = text(input.kind ?? '', 'kind').trim()
  const name = text(input.name ?? '', 'name').trim()
  const ns = text(input.namespace ?? '', 'namespace').trim()
  if (!kind || !name) throw new ActionError('Give the kind and name, or close.')
  if (!NAME.test(name) || name.includes('/') || (ns && (!NAME.test(ns) || ns.includes('/')))) throw new ActionError('That is not an object name.')
  let peek: Peek
  if (kind === 'Node') peek = { kind: 'node', name }
  else if (!ns) throw new ActionError(`A ${kind} needs its namespace.`)
  else if (kind === 'Pod') peek = { kind: 'pod', namespace: ns, name }
  else if (kind === 'Service') peek = { kind: 'service', namespace: ns, name }
  else if (OBJECT_KINDS.has(kind)) peek = { kind: 'object', type: kind, namespace: ns, name }
  else throw new ActionError(`The console has no details pane for a ${kind}.`)
  go(withParams(route, { peek: peekParam(peek) }), true)
  return {
    label: `Opened the details of ${kind} ${ns ? ns + '/' : ''}${name}`,
    details: true,
  }
}

function viewHighlight(route: Route, input: Record<string, unknown>): Applied {
  const t = tableFor(route, input.table, 'sort')
  const row = text(input.row, 'row').trim()
  const r = t.find(row)
  if (!r) throw new ActionError(`No row named ${JSON.stringify(row)} is shown in ${t.id}.`)
  if (planned !== null) return { label: '' }
  setHighlight(t.id, r)
  return { label: `Highlighted ${row} in ${t.id}` }
}

// ---- the AIOps area (Phase 12.1, SR-09) ----

/** The pages of the AIOps area view_aiops opens: the Inbox, a case, the jobs' runs, a run. */
export const AIOPS_PAGES = ['inbox', 'case', 'runs', 'run'] as const
/** The Inbox's Whose filter, as view_aiops names it (EM-08). */
export const INBOX_WHO = ['all', 'mine', 'unassigned', 'involved'] as const
const WHO_WORDS: Record<string, string> = { mine: ', the person’s own', unassigned: ', what nobody has taken', involved: ', the cases the person is involved in' }
/** A case's id: C-12, or its number. */
const CASE_ID = /^(?:C-)?([1-9]\d{0,8})$/i
/** A run's id, as the runs name them: run-12. */
const RUN_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$/

/**
 * Opens a page of the AIOps area, as following its link does: what the
 * page then shows comes through the same API, so a person who may not see
 * the cases or the runs sees the page say so (P6U-07).
 */
function viewAIOps(_route: Route, input: Record<string, unknown>): Applied {
  const page = text(input.page, 'page')
  if (!(AIOPS_PAGES as readonly string[]).includes(page)) throw new ActionError(`page must be one of ${AIOPS_PAGES.join(', ')}.`)
  const id = input.id === undefined ? '' : text(input.id, 'id', 80).trim()
  const forInbox = input.show !== undefined || input.search !== undefined || input.who !== undefined
  if (forInbox && page !== 'inbox') throw new ActionError('show, who and search are for the inbox page.')
  let hash: string
  let label: string
  switch (page) {
    case 'inbox': {
      if (id) throw new ActionError('The Inbox takes no id: open a case with page case.')
      const show = input.show === undefined ? 'pending' : text(input.show, 'show')
      if (show !== 'pending' && show !== 'all') throw new ActionError('show must be one of pending, all.')
      const q = input.search === undefined ? '' : text(input.search, 'search').trim()
      // Whose rows (EM-08): the person's own, nobody's, or the cases they are involved in.
      const who = input.who === undefined ? 'all' : text(input.who, 'who')
      if (!(INBOX_WHO as readonly string[]).includes(who)) throw new ActionError(`who must be one of ${INBOX_WHO.join(', ')}.`)
      hash = aiopsHref('inbox', [], { show: show === 'all' ? 'all' : undefined, who: who === 'all' ? undefined : who, q: q || undefined })
      label = `Opened AIOps › Inbox${show === 'all' ? ', closed cases included' : ''}${WHO_WORDS[who] ?? ''}${q ? `, searching “${q}”` : ''}`
      break
    }
    case 'case': {
      const m = CASE_ID.exec(id)
      if (!m) throw new ActionError('A case is named by its id, such as C-12.')
      hash = aiopsHref('inbox', ['case', m[1]!])
      label = `Opened case C-${m[1]!}`
      break
    }
    case 'runs':
      if (id) throw new ActionError('The runs take no id: open one with page run.')
      hash = aiopsHref('jobs', ['runs'])
      label = 'Opened AIOps › Jobs › Runs'
      break
    default:
      if (!RUN_ID.test(id)) throw new ActionError('A run is named by its id, such as run-12.')
      hash = aiopsHref('jobs', ['runs', id])
      label = `Opened run ${id}`
  }
  go(hash)
  return { label }
}

// ---- Administration and Settings (SR-12, SR-13) ----

/** Who may open a page: everyone, anyone signed in, a SuperAdmin, or who sees Knowledge's Added in Mycroft card. */
type AdminAccess = 'all' | 'signed' | 'super' | 'knowledge'

interface AdminViewPage {
  label: string
  /** The Administration page it is on, for the role check; none for home and connection. */
  area?: AdminPage
  access: AdminAccess
  /** Its address in the cluster the person is on, with the item open. */
  hash: (cluster: string, item: string) => string
  /** Whether it opens an item's drawer, and the table whose search it sets. */
  item?: boolean
  table?: string
}

const settings =
  (section: string) =>
  (cluster: string, item: string): string =>
    adminHref('settings', section ? [section, ...(item ? [item] : [])] : [], cluster)
const admin =
  (page: AdminPage, ...path: string[]) =>
  (cluster: string, item: string): string =>
    adminHref(page, [...path, ...(item ? [item] : [])], cluster)

/**
 * The pages view_admin opens (SR-12), each in the cluster the person is on
 * so the conversation stays with them: Administration's pages and Settings'
 * sections, as their own links address them, and the home and connection
 * pages. The server's description names the same ids (testdata/ui_admin.json).
 */
export const ADMIN_VIEW_PAGES: Record<string, AdminViewPage> = {
  home: { label: 'the home page', access: 'all', hash: () => '#/' },
  connection: { label: 'this cluster’s connection page', access: 'all', hash: (c) => clusterPageHref(c) },
  roles: { label: 'Administration › Roles', area: 'roles', access: 'signed', hash: admin('roles'), item: true, table: 'admin-roles' },
  permissions: { label: 'Administration › Permissions', area: 'permissions', access: 'signed', hash: admin('permissions'), item: true, table: 'admin-permissions' },
  users: { label: 'Administration › Users', area: 'users', access: 'super', hash: admin('users'), item: true, table: 'admin-users' },
  toolbox: { label: 'Administration › Toolbox', area: 'toolbox', access: 'super', hash: admin('toolbox') },
  sessions: { label: 'Administration › Logs › Sessions', area: 'logs', access: 'super', hash: admin('logs', 'sessions') },
  'audit-log': { label: 'Administration › Logs › Audit log', area: 'logs', access: 'super', hash: admin('logs', 'audit') },
  backup: { label: 'Administration › Backup', area: 'backup', access: 'super', hash: admin('backup') },
  'getting-started': { label: 'Settings › Getting started', area: 'settings', access: 'super', hash: settings('start') },
  general: { label: 'Settings › General', area: 'settings', access: 'super', hash: settings('') },
  providers: { label: 'Settings › Providers', area: 'settings', access: 'super', hash: settings('providers'), item: true },
  agents: { label: 'Settings › Agents', area: 'settings', access: 'super', hash: settings('agents'), item: true },
  knowledge: { label: 'Settings › Knowledge', area: 'settings', access: 'knowledge', hash: settings('knowledge') },
  watson: { label: 'Settings › Watson', area: 'settings', access: 'super', hash: settings('watson') },
  'notification-channels': { label: 'Settings › Watson, with its notification channels', area: 'settings', access: 'super', hash: settings('watson') },
  applications: { label: 'Settings › Applications', area: 'settings', access: 'super', hash: settings('apps') },
  issues: { label: 'Settings › Issues', area: 'settings', access: 'super', hash: settings('issues') },
  confluence: { label: 'Settings › Issues, with its Confluence settings', area: 'settings', access: 'super', hash: settings('issues') },
  sla: { label: 'Settings › SLA', area: 'settings', access: 'super', hash: settings('sla') },
  'case-settings': { label: 'Settings › Cases', area: 'settings', access: 'super', hash: settings('cases') },
  email: { label: 'Settings › Email', area: 'settings', access: 'super', hash: settings('email') },
  'jobs-settings': { label: 'Settings › Jobs', area: 'settings', access: 'super', hash: settings('jobs') },
}

let me: Me | null = null

/** The console tells Sherlock's actions who is signed in in this window, for the pages their role opens (SR-13). */
export function setUIMe(m: Me | null): void {
  me = m
}

/** Why this person may not open a page, or null when they may: as the page itself would refuse them. */
function adminRefusal(page: AdminViewPage): string | null {
  if (page.access === 'all') return null
  if (!me) return 'Who is signed in is not known in this window yet, so the page was not opened. Say what you found in words.'
  if (me.mode === 'none' && page.area !== 'permissions') return `Sign-in is off on this console, so Administration shows Permissions alone: ${page.label} was not opened. Say what you found in words.`
  if (page.access === 'signed') return null
  if (page.access === 'knowledge' ? seesAddedInMycroft(me) : isSuperAdmin(me) && pagesFor(me).includes(page.area!)) return null
  return `${page.label} is for SuperAdmins, and this person’s role does not open it: it was not opened. Say so, and answer in words.`
}

/** Where view_admin goes and what it says, without going: for the action, and for a change left for the Show in 221B button. */
function adminTarget(route: Route, input: Record<string, unknown>): { hash: string; label: string; table?: string; q?: string; link?: string } {
  const id = text(input.page, 'page')
  const page = Object.hasOwn(ADMIN_VIEW_PAGES, id) ? ADMIN_VIEW_PAGES[id]! : undefined
  if (!page) throw new ActionError(`page must be one of ${Object.keys(ADMIN_VIEW_PAGES).join(', ')}.`)
  const item = input.item === undefined ? '' : text(input.item, 'item', 120).trim()
  if (item && !page.item)
    throw new ActionError(
      `${id} takes no item; the pages with one are ${Object.keys(ADMIN_VIEW_PAGES)
        .filter((k) => ADMIN_VIEW_PAGES[k]!.item)
        .join(', ')}.`,
    )
  if (item && (!NAME.test(item) || item.includes('/'))) throw new ActionError('item is not an id or a username.')
  const q = input.search === undefined ? '' : text(input.search, 'search').trim()
  if (q && !page.table) throw new ActionError(`${id} has no search box; roles, permissions and users do.`)
  const refused = adminRefusal(page)
  if (refused) throw new ActionError(refused)
  if (id === 'home' && route.cluster !== LOCAL) throw new ActionError('The home page belongs to the console’s own cluster, and this conversation to this one: offer the person the link instead.', '#/home')
  return {
    hash: page.hash(route.cluster, item),
    label: `Opened ${page.label}${item ? `, ${item}` : ''}${q ? `, searching “${q}”` : ''}`,
    ...(page.table && q ? { table: page.table, q } : {}),
  }
}

/**
 * Opens a page of Administration or Settings, the home page or the
 * connection page, as following its link does (SR-12): nothing on it is
 * pressed, typed or saved, and a page the person's role does not open is
 * refused here, as the page itself refuses it (SR-13).
 */
function viewAdmin(route: Route, input: Record<string, unknown>): Applied {
  const t = adminTarget(route, input)
  go(t.hash)
  if (t.table && t.q !== undefined && planned === null) queueQuery(t.table, t.q)
  return { label: t.label }
}

/** The registry: what each action does in the browser (P6U-05, P6U-12). */
export const REGISTRY: Record<string, ((route: Route, input: Record<string, unknown>) => Applied) | null> = {
  view_state: null,
  view_open: viewOpen,
  view_filter: viewFilter,
  view_search: viewSearch,
  view_sort: viewSort,
  view_columns: viewColumns,
  view_details: viewDetails,
  view_highlight: viewHighlight,
  view_aiops: viewAIOps,
  view_admin: viewAdmin,
  // The one dialog an action may open (TB-42, amending P6U-12).
  open_tool_dialog: openToolDialog,
}

// ---- the person's own changes, Undo (P6U-09, P6U-10) ----

interface Saved {
  hash: string
  tables: {
    id: string
    sort: SortState
    query: string
    override: Set<string> | null
  }[]
}

interface AnswerRecord {
  touched: boolean
  before: Saved | null
  /** Where the changes this answer holds will leave the page (SR-18). */
  planned?: string
}

/** Message ids count from the start of a conversation, so an answer is known by its cluster too. */
const answerKey = (cluster: string, answer: number) => `${cluster}/${answer}`

const answers = new Map<string, AnswerRecord>()
let answering: string | null = null
/** The hash Sherlock last set, so its own navigation is not taken for the person's. */
let expected: string | null = null

function capture(): Saved {
  return {
    hash: window.location.hash || '#/',
    tables: tableHandles().map((t) => ({
      id: t.id,
      sort: t.sort(),
      query: t.query(),
      override: t.override() ? new Set(t.override()) : null,
    })),
  }
}

/** The answer Sherlock is giving now, from the conversation; null when it is not answering. */
export function setAnswering(cluster: string, id: number | null): void {
  answering = id === null ? null : answerKey(cluster, id)
  for (const k of narrowed.keys()) if (k !== answering) narrowed.delete(k)
  if (answering !== null && !answers.has(answering)) answers.set(answering, { touched: false, before: null })
}

/**
 * Ends one answer, if it is the one being given: for a shared conversation
 * (a case's, SR-19), whose end must not end the person's own answer.
 */
export function endAnswering(scope: string, id: number): void {
  if (answering === answerKey(scope, id)) setAnswering(scope, null)
}

function touched() {
  if (answering === null) return
  const rec = answers.get(answering)
  if (rec) rec.touched = true
}

let watching = false
function watch() {
  if (watching || typeof window === 'undefined') return
  watching = true
  onRouteChange(() => {
    if (isDriving() || (expected !== null && window.location.hash === expected)) return
    touched()
  })
  onUserChange(touched)
}

/** Whether this answer's changes can be undone in this window. */
export function canUndo(cluster: string, answer: number): boolean {
  return !!answers.get(answerKey(cluster, answer))?.before
}

const undone = new Set<string>()
const undoListeners = new Set<() => void>()

export function isUndone(cluster: string, answer: number): boolean {
  return undone.has(answerKey(cluster, answer))
}

export function markUndone(cluster: string, answer: number): void {
  undone.add(answerKey(cluster, answer))
  for (const fn of undoListeners) fn()
}

export function onUndone(fn: () => void): () => void {
  undoListeners.add(fn)
  return () => undoListeners.delete(fn)
}

/** How long to wait for a frame: a hidden window draws none, and what it was doing must still finish. */
const FRAME_WAIT_MS = 100

const frame = () =>
  new Promise<void>((r) => {
    const t = setTimeout(r, FRAME_WAIT_MS)
    if (typeof requestAnimationFrame === 'undefined') return
    requestAnimationFrame(() => {
      clearTimeout(t)
      r()
    })
  })

/** Puts the view back as it was before the answer's first change (P6U-10). */
export async function undo(cluster: string, answer: number): Promise<boolean> {
  const key = answerKey(cluster, answer)
  const rec = answers.get(key)
  if (!rec?.before) return false
  const b = rec.before
  rec.before = null
  if (answering === key) rec.touched = true
  navigate(b.hash, true)
  await frame()
  await frame()
  for (const s of b.tables) {
    const t = tableHandle(s.id)
    if (!t) continue
    t.setSort(s.sort)
    t.setOverride(s.override)
    if (t.query() !== s.query) t.setQuery(s.query)
  }
  markUndone(cluster, answer)
  announce('Undid Sherlock’s changes to the view')
  return true
}

// ---- announcements ----

let said = { text: '', seq: 0 }
const sayListeners = new Set<() => void>()

/** Tells screen readers what changed, in the window's live region. */
export function announce(text: string): void {
  said = { text, seq: said.seq + 1 }
  for (const fn of sayListeners) fn()
}

export function lastAnnouncement(): { text: string; seq: number } {
  return said
}

export function onAnnounce(fn: () => void): () => void {
  sayListeners.add(fn)
  return () => sayListeners.delete(fn)
}

// ---- running a request ----

export interface UIRequest {
  id: string
  window: string
  answer: number
  action: string
  input: unknown
  /** Check the change and leave the view as it is: the answer holds it until it is done (SR-18). */
  hold?: boolean
}

export interface UIResult {
  applied: boolean
  dropped?: boolean
  /** Left undone: the window has unsaved changes open (SR-14). */
  deferred?: boolean
  /** Checked and left for the end of the answer, which sends it again then (SR-18). */
  held?: boolean
  error?: string
  label?: string
  link?: string
  view?: ViewState
}

async function settleDetails() {
  for (let i = 0; i < 40; i++) {
    await new Promise((r) => setTimeout(r, 60))
    const d = viewState().details
    if (!d || d.state !== 'loading') return
  }
}

/**
 * Checks a change to be held (SR-18) on the page the answer's held changes
 * will leave open, as far as it can be before that page is there: the tab,
 * path and filters it opens or sets, an object's details, a page of AIOps,
 * Administration or Settings the role opens, and the tables while no held
 * change has moved the page. Returns why it would be refused, or null.
 */
function plan(rec: AnswerRecord, a: ActionDef, run: (route: Route, input: Record<string, unknown>) => Applied, input: Record<string, unknown>): ActionError | null {
  const here = window.location.hash || '#/'
  const at = rec.planned ?? here
  const onTable = a.name === 'view_sort' || a.name === 'view_columns' || a.name === 'view_highlight' || (a.name === 'view_search' && input.table !== undefined)
  // The tables of a page not open yet are not there to check.
  if (onTable && at !== here) return null
  planned = at
  try {
    run(at === here ? currentRoute() : parseHash(at), input)
    rec.planned = planned
    return null
  } catch (e) {
    if (e instanceof ActionError) return e
    throw e
  } finally {
    planned = null
  }
}

/** Runs one action, from the given cluster's conversation, in this window and says what came of it. */
export async function runRequest(req: UIRequest, cluster: string): Promise<UIResult> {
  watch()
  const a = ACTIONS.find((x) => x.name === req.action)
  const run = a ? REGISTRY[a.name] : undefined
  if (!a || run === undefined)
    return {
      applied: false,
      error: `There is no view action ${JSON.stringify(req.action)}.`,
      view: viewState(),
    }
  const key = answerKey(cluster, req.answer)
  if (answering === null) setAnswering(cluster, req.answer)
  if (answering !== key)
    return {
      applied: false,
      error: 'Sherlock is not answering a question in this window.',
      view: viewState(),
    }
  let input: Record<string, unknown>
  try {
    input = checkInput(a, req.input ?? {})
  } catch (e) {
    return { applied: false, error: (e as Error).message, view: viewState() }
  }
  if (run === null) return { applied: false, view: viewState() }
  const rec = answers.get(key)!
  if (rec.touched) return { applied: false, dropped: true, view: viewState() }
  // Never away from edits not saved yet (SR-14): the change waits for the
  // answer's Show in 221B button. A page the role does not open is still
  // refused now, so no button offers it.
  if (a.changes && hasUnsaved()) {
    try {
      if (a.name === 'view_admin') adminTarget(currentRoute(), input)
    } catch (e) {
      const err = e as ActionError
      return { applied: false, error: err.message, ...(err.link ? { link: err.link } : {}), view: viewState() }
    }
    announce(`Sherlock left the view as it is: ${unsavedWhat().join(', ')} has unsaved changes`)
    return { applied: false, deferred: true, view: viewState() }
  }
  // The screen moves only once the answer is done (SR-18): a change asked
  // for during it is checked as far as it can be before the pages it needs
  // are open (its input, and a page the role does not open), and left for
  // then, when the server sends it again.
  if (a.changes && req.hold) {
    const err = plan(rec, a, run, input)
    if (err) return { applied: false, error: err.message, ...(err.link ? { link: err.link } : {}), view: viewState() }
    return { applied: false, held: true }
  }
  const focused = typeof document !== 'undefined' ? (document.activeElement as HTMLElement | null) : null
  const before = capture()
  let done: Applied
  try {
    done = drive(() => {
      const r = run(currentRoute(), input)
      expected = window.location.hash
      return r
    })
  } catch (e) {
    const err = e as ActionError
    return {
      applied: false,
      error: err.message,
      ...(err.link ? { link: err.link } : {}),
      view: viewState(),
    }
  }
  if (!rec.before) rec.before = before
  await frame()
  await frame()
  if (done.details) await settleDetails()
  // Focus stays where it was (P6U-09), but for a dialog the person reads next.
  if (!done.dialog && focused && focused.isConnected && document.activeElement !== focused) focused.focus({ preventScroll: true })
  announce(`Sherlock: ${done.label}`)
  return { applied: true, label: done.label, view: viewState() }
}

// ---- Show in 221B (SR-15) ----

/** A change an answer left undone, as the server keeps it for the button. */
export interface ShowChange {
  action: string
  input: unknown
  why?: string
}

/** How long a change waits for the table it needs, on a page that is still loading. */
const SHOW_WAIT_MS = 3000

/**
 * Applies the changes an answer left undone, in order, when the person
 * presses Show in 221B: each through the same checks as when Sherlock
 * calls it, so a refused one stays refused. It belongs to no answer, so it
 * is not undone with one. With unsaved changes open it applies nothing
 * unless forced, and says what holds them.
 */
export async function showChanges(list: ShowChange[], force = false): Promise<{ applied: string[]; failed: string[]; unsaved?: string[] }> {
  const applied: string[] = []
  const failed: string[] = []
  // The person is asked first whether to leave edits not saved yet.
  if (!force && hasUnsaved()) return { applied, failed, unsaved: unsavedWhat() }
  for (const c of list.slice(0, 10)) {
    const a = ACTIONS.find((x) => x.name === c.action)
    const run = a ? REGISTRY[a.name] : undefined
    if (!a || !run || !a.changes) continue
    let input: Record<string, unknown>
    try {
      input = checkInput(a, c.input ?? {})
    } catch (e) {
      failed.push((e as Error).message)
      continue
    }
    // A table the change needs may still be loading on the page the change
    // before it opened: try again for a moment.
    const until = Date.now() + SHOW_WAIT_MS
    for (;;) {
      try {
        const done = drive(() => run(currentRoute(), input))
        applied.push(done.label)
        await frame()
        await frame()
        if (done.details) await settleDetails()
        break
      } catch (e) {
        if (Date.now() > until || !(e instanceof ActionError) || !/table|column|row/i.test(e.message)) {
          failed.push((e as Error).message)
          break
        }
        await new Promise((r) => setTimeout(r, 150))
      }
    }
  }
  if (applied.length) announce(`Showed what Sherlock found: ${applied.join('; ')}`)
  return { applied, failed }
}

// ---- the window ----

/** This console window's id, so Sherlock changes only the one that asked (P6U-08). */
export const WINDOW_ID: string = (() => {
  const b = new Uint8Array(8)
  globalThis.crypto?.getRandomValues?.(b)
  return Array.from(b, (x) => (x % 36).toString(36)).join('') + Date.now().toString(36).slice(-4)
})()
