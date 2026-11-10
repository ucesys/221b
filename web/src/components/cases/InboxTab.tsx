// AIOps › Inbox (Phase 12, wave 5, IN-01 to IN-06; wave 6): one flat
// table, as the Nodes and Namespaces pages have them, of everything that
// waits for a person across clusters, and under All what was done too:
// cases (whether a person or an agent found the problem), proposed
// changes, approvals, known issues pending for their owner and knowledge
// drafts. Filters on top (Pending | All, Source: People | Agents, Mine |
// Unassigned, Priority, Type, Status, Cluster, search), all in the address;
// pending first, then P0 first, then the longest waiting, until someone
// sorts by a column. A row opens its drawer at #/cases/inbox/<kind>/<id>.
// Since wave 6c (CF-38) there is no Kind filter, and those who work the
// cases pick case rows for one action on many (BulkBar.tsx). Since CF-87
// a SuperAdmin's suggested validation rules are rows, opened here.
import { useEffect, useState } from 'react'
import { proposalId } from '../../api/cards'
import { CASE_TYPE_IDS, CASE_TYPES, SHOWN_STATUS, type ShownStatus } from '../../api/casework'
import type { CasesAccess, InboxKind, Who } from '../../lib/cases'
import { casesHref, navigate, tabHref, type Route } from '../../lib/router'
import { useTable } from '../../lib/useTable'
import { ColumnsMenu } from '../ColumnsMenu'
import { DataTable } from '../DataTable'
import { ExportMenu } from '../ExportMenu'
import { SearchBox } from '../SearchBox'
import { Select } from '../Select'
import { BulkBar, BulkResults, type BulkOutcome } from './BulkBar'
import { CaseDrawer, useNow } from './CaseDrawer'
import { ApprovalDrawer, KnowledgeDrawer } from './InboxDrawers'
import { InboxPicks, type InboxPicking } from './InboxPicks'
import { approvalId, filterRows, INBOX_COLUMNS, pendingByKind, rowLabel, rowTerms, type InboxFilter, type InboxRow, type Origin, type Show } from './inboxRows'
import { KnownDrawer } from './KnownTab'
import { NewReport } from './NewReport'
import { isPriority, PRIORITIES, PRIORITY } from './parts'
import { ProposalDrawer } from './ProposalDrawer'
import { InboxSuggestionDrawer } from './RulesView'
import { kindsFor, useInbox } from './useInbox'

export { kindsFor }

/** The filters an address asks for: ?show=all&source=agents&who=mine&priority=0&type=security&status=waiting&cluster=prod&q=; an old ?kind= is let go. */
export function filterOf(p: URLSearchParams): InboxFilter {
  const who = p.get('who')
  const raw = p.get('priority')
  const priority = raw === null || raw === '' ? NaN : Number(raw)
  const source = p.get('source')
  const status = p.get('status') ?? ''
  return {
    show: p.get('show') === 'all' ? 'all' : 'pending',
    who: who === 'mine' || who === 'unassigned' ? who : 'all',
    priority: isPriority(priority) ? priority : null,
    cluster: p.get('cluster') ?? '',
    source: source === 'people' || source === 'agents' ? source : null,
    type: p.get('type') || null,
    status: Object.hasOwn(SHOWN_STATUS, status) ? (status as ShownStatus) : null,
  }
}

/** The Inbox's filters, the parameters kept from row to row: not what an address hands one drawer (?title= for New Case). */
const FILTERS = ['show', 'source', 'who', 'priority', 'type', 'status', 'cluster', 'q']

const SOURCES: [Origin | 'all', string, string][] = [
  ['all', 'Everyone', 'People’s and the agents’'],
  ['people', 'People', 'Reported, opened by hand, from an alert, or asked for by a person'],
  ['agents', 'Agents', 'Found or proposed by an agent'],
]

/** The address's filters as parameters. */
function paramsOf(route: Route): Record<string, string> {
  const out: Record<string, string> = {}
  for (const k of FILTERS) {
    const v = route.params.get(k)
    if (v) out[k] = v
  }
  return out
}

/** The clusters the rows are about, by name, for the Cluster filter. */
function clustersOf(rows: InboxRow[]): [string, string][] {
  const m = new Map<string, string>()
  for (const r of rows) if (r.cluster) m.set(r.cluster, r.clusterName ?? r.cluster)
  return [...m.entries()].sort((a, b) => a[1].localeCompare(b[1]))
}

const WHO: [Who, string][] = [
  ['all', 'Everyone’s'],
  ['mine', 'Mine'],
  ['unassigned', 'Unassigned'],
]

/**
 * The Inbox's link to the alerts going on in the cluster open (CF-81): the
 * count the bell had before CF-80, the Alerts tab's own (alerts not yet
 * recovered); nothing while there are none.
 */
export function AlertsNow({ count, cluster }: { count?: number; cluster?: string }) {
  if (!count) return null
  return (
    <a className="rk-link" href={tabHref('alerts')} style={{ fontSize: 12, whiteSpace: 'nowrap' }} title={`${count} ${count === 1 ? 'alert' : 'alerts'} going on in ${cluster || 'this cluster'}: open its Alerts tab`} data-testid="inbox-alerts">
      {count} {count === 1 ? 'alert' : 'alerts'} now
    </a>
  )
}

export function InboxTab({ route, access, onToast, alerts, clusterName }: { route: Route; access: CasesAccess; onToast?: (msg: string) => void; alerts?: number; clusterName?: string }) {
  const f = filterOf(route.params)
  const kinds = kindsFor(access)
  const inbox = useInbox(access, f.show === 'all', true)
  const now = useNow()
  const params = paramsOf(route)
  const set = (changes: Record<string, string | undefined>) => navigate(casesHref('inbox', route.path.slice(1), { ...params, ...changes }), true)

  // The drawer the address names: #/cases/inbox/<kind>/<id>. The links of
  // the Inbox before it was one table (?proposal=, ?report=) open the same,
  // and so does a proposed change's c<n> of before, as PC-<n> (CF-56).
  const [, openKind = '', openId = ''] = route.path
  const oldProposal = route.params.get('proposal') ?? (openKind === 'proposal' && openId && proposalId(openId) !== openId ? openId : null)
  const oldReport = route.params.get('report')
  useEffect(() => {
    if (oldProposal) navigate(casesHref('inbox', ['proposal', proposalId(oldProposal)], paramsOf(route)), true)
    else if (oldReport) navigate(casesHref('inbox', ['report', oldReport], paramsOf(route)), true)
  }, [oldProposal, oldReport, route])
  const open = (r: InboxRow) => navigate(casesHref('inbox', r.open, params))
  const close = () => navigate(casesHref('inbox', [], params), true)
  const selected = openKind && openId ? `${openKind}:${openId}` : null

  // The compiler keeps these while the rows and the filters stay the same.
  const rows = inbox.rows
  const counts = pendingByKind(rows, f.who)
  const total = kinds.reduce((n, k) => n + counts[k], 0)
  const kept = filterRows(rows, f)
  const clusters = clustersOf(rows)
  const table = useTable<InboxRow>({ id: 'cases-inbox', columns: INBOX_COLUMNS, rows: kept, terms: rowTerms, now, defaultSort: { col: 'waiting', dir: 'asc' }, query: route.params.get('q') ?? '', onQuery: (q) => set({ q: q || undefined }) })

  // Out of what Pending or All lists, before the other filters.
  const listed = f.show === 'all' ? rows.length : total
  const shownText = !inbox.loaded ? 'Loading…' : table.shown.length < listed ? `Showing ${table.shown.length} of ${listed}` : ''
  const empty = !inbox.loaded ? 'Loading the Inbox…' : f.show === 'pending' && !f.type && !f.status && f.who === 'all' && f.priority === null && !f.cluster && !f.source && !table.query ? 'Nothing is pending for you.' : 'Nothing matches these filters.'

  // Picked cases (CF-38), for those who work the cases: those the cases' list still has, in the order picked.
  const [picks, setPicks] = useState<string[]>([])
  const mayPick = !!inbox.cases?.mayChange
  // The compiler keeps these between renders, as it does the rows.
  const byId = new Map((inbox.cases?.cases ?? []).map((c) => [c.id, c]))
  const live = picks.filter((id) => byId.has(id))
  const pickedCases = live.map((id) => byId.get(id)!)
  const picking: InboxPicking | null = mayPick
    ? {
        picked: new Set(live),
        toggle: (id) => setPicks((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id])),
        shown: table.shown.filter((r) => r.kind === 'case').map((r) => r.ref),
        setMany: (ids, on) => setPicks((p) => (on ? [...new Set([...p, ...ids])] : p.filter((x) => !ids.includes(x)))),
      }
    : null
  const [results, setResults] = useState<BulkOutcome | null>(null)
  const done = (o: BulkOutcome) => {
    // Each case's result shows; the refused stay picked, to try again another way; the lists read
    // again: a close may reject the changes that waited for a case, or make a known issue (CF-34).
    setResults(o)
    setPicks(o.results.filter((r) => !r.ok).map((r) => r.id))
    inbox.reload()
  }

  if (kinds.length === 0) {
    return (
      <div className="cs-col" data-testid="cases-inbox">
        <p className="tb-hint">Nothing comes to your Inbox with your role.</p>
      </div>
    )
  }
  // A case's change reads every list again, not only the cases': closing one rejects the
  // changes waiting only for it (CF-34), and a change decided may move the cases it resolves.
  const reload = (k?: InboxKind) => inbox.reload(k)
  return (
    <div className="cs-col cs-col-inbox" data-testid="cases-inbox">
      <section aria-label="Inbox" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
          <div className="rk-seg" role="group" aria-label="Show" data-testid="inbox-show">
            {(['pending', 'all'] as Show[]).map((s) => (
              <button key={s} aria-pressed={f.show === s} onClick={() => set({ show: s === 'pending' ? undefined : s })} data-testid={`inbox-show-${s}`} title={s === 'pending' ? 'What waits for a person' : 'What waits, then what was closed, completed or decided, newest first'}>
                {s === 'pending' ? 'Pending' : 'All'}
                {s === 'pending' && <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{total}</span>}
              </button>
            ))}
          </div>
          <SearchBox rows={kept} terms={rowTerms} value={table.query} onChange={table.setQuery} placeholder="Search by title, id, object, validation rule, person or RCA cause" label="Search the Inbox" testId="inbox-search" width={260} />
          <span style={{ fontSize: 12, color: 'var(--fg3)', whiteSpace: 'nowrap' }} data-testid="inbox-shown">
            {shownText}
          </span>
          <AlertsNow count={alerts} cluster={clusterName} />
          <div className="rk-tools" style={{ marginLeft: 'auto', gap: 8 }}>
            <NewReport operator={access.operator} />
          </div>
        </div>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
          <div className="rk-seg" role="group" aria-label="Source" data-testid="inbox-source">
            {SOURCES.map(([v, label, tip]) => (
              <button key={v} aria-pressed={(f.source ?? 'all') === v} onClick={() => set({ source: v === 'all' ? undefined : v })} title={tip} data-testid={`inbox-source-${v}`}>
                {label}
              </button>
            ))}
          </div>
          <div className="rk-seg" role="group" aria-label="Whose" data-testid="inbox-who">
            {WHO.map(([w, label]) => (
              <button key={w} aria-pressed={f.who === w} onClick={() => set({ who: w === 'all' ? undefined : w })} data-testid={w === 'all' ? 'inbox-who-all' : `inbox-${w}`}>
                {label}
              </button>
            ))}
          </div>
          <Select label="Priority" value={f.priority === null ? '' : String(f.priority)} options={[['', 'Any priority'], ...PRIORITIES.map((p): [string, string] => [String(p), PRIORITY[p]!])]} onChange={(v) => set({ priority: v || undefined })} testId="inbox-priority" />
          {kinds.includes('case') && <Select label="Type" value={f.type ?? ''} options={[['', 'Any type'], ...CASE_TYPE_IDS.map((t): [string, string] => [t, CASE_TYPES[t]])]} onChange={(v) => set({ type: v || undefined })} testId="inbox-type" />}
          {kinds.includes('case') && <Select label="Status" value={f.status ?? ''} options={[['', 'Any status'], ...Object.entries(SHOWN_STATUS)]} onChange={(v) => set({ status: v || undefined })} testId="inbox-status" />}
          {clusters.length > 1 && <Select label="Cluster" value={f.cluster} options={[['', 'Every cluster'], ...clusters]} onChange={(v) => set({ cluster: v || undefined })} testId="inbox-cluster" />}
        </div>
        {inbox.errors.map((e) => (
          <p key={e} className="tb-err" role="alert" style={{ margin: 0 }}>
            {e}
          </p>
        ))}
        {/* Columns and Export sit just above the table they act on (CF-66). */}
        <div className="rk-tools" data-testid="inbox-table-tools">
          <ColumnsMenu cols={INBOX_COLUMNS} hidden={table.hidden} defaults={table.defaults} onChange={table.setHidden} compact={false} />
          <ExportMenu onExport={(fmt) => table.exportRows(fmt, 'cases', 'inbox', onToast ?? (() => undefined))} disabled={!table.shown.length} what="the rows in view" />
        </div>
        <InboxPicks.Provider value={picking}>
        <DataTable<InboxRow>
          label="Inbox"
          rows={table.shown}
          cols={table.cols}
          rowKey={(r) => r.key}
          rowLabel={rowLabel}
          sort={table.sort}
          onSort={table.onSort}
          selected={selected}
          onOpen={(key) => {
            const r = table.shown.find((x) => x.key === key)
            if (r) {
              if (selected === key) close()
              else open(r)
            }
          }}
          now={now}
          empty={empty}
          rowAttr="inbox-row"
          rowHeight={40}
        />
        </InboxPicks.Provider>
        {picking && pickedCases.length > 0 && <BulkBar cases={pickedCases} onClear={() => setPicks([])} onDone={done} />}
        {results && <BulkResults outcome={results} onClose={() => setResults(null)} />}
      </section>

      {/* A case, New Case ("new") and an old report's address (a report is the case of the same number) open the one case drawer. */}
      {(openKind === 'case' || openKind === 'report') && openId && <CaseDrawer key={openId} id={openId} onClose={close} onChanged={() => reload()} onToast={onToast} />}
      {openKind === 'proposal' && openId && proposalId(openId) === openId && <ProposalDrawer key={openId} id={openId} initial={inbox.proposals?.proposals.find((p) => p.id === openId)} onClose={close} onDecided={() => reload()} onToast={onToast} />}
      {openKind === 'approval' && openId && <ApprovalDrawer key={openId} id={openId} row={inbox.approvals?.items.find((a) => approvalId(a) === openId)} loaded={inbox.loaded} onClose={close} onDecided={() => reload('approval')} />}
      {openKind === 'known' && openId && <KnownDrawer key={openId} id={openId} from={0} operator={access.operator} onClose={close} onSaved={() => reload('known')} onToast={onToast} />}
      {openKind === 'rule' && openId && <InboxSuggestionDrawer key={openId} id={openId} onClose={close} onDone={() => reload('rule')} onToast={onToast} />}
      {openKind === 'knowledge' && openId && (
        <KnowledgeDrawer
          key={openId}
          id={openId}
          run={inbox.knowledge?.runs.find((r) => r.id === openId)}
          conflict={inbox.knowledge?.conflicts.find((c) => c.id === openId)}
          request={inbox.knowledge?.requests.find((r) => r.id === openId)}
          loaded={inbox.loaded}
          onClose={close}
          onChanged={() => reload('knowledge')}
          onToast={onToast}
        />
      )}
    </div>
  )
}
