import { describe, expect, it } from 'vitest'
import type { ApprovalRow } from '../../api/automation'
import type { Proposal } from '../../api/cards'
import { inboxKindOf, inboxOrderKey } from '../../lib/cases'
import { sortRows } from '../../lib/table'
import { approvalId, approvalRow, caseRow, conflictRow, draftRunRow, filterRows, INBOX_COLUMNS, knownRow, pendingByKind, proposalRow, rowTerms, whoseMatch, type InboxCase, type InboxFilter, type RowContext } from './inboxRows'

const ctx: RowContext = { me: 'Ann (ann)', person: 'ann', names: new Map([['prod', 'Production']]) }

const kase = (n: number, over: Partial<InboxCase> = {}): InboxCase => ({
  id: `C-${n}`,
  number: n,
  title: `Case ${n}`,
  priority: 3,
  cluster: 'prod',
  source: { kind: 'manual' },
  status: 'new',
  agentFound: false,
  slaApplies: true,
  createdBy: 'Bob (bob)',
  created: '2026-11-01T00:00:00Z',
  updated: '2026-11-02T00:00:00Z',
  since: '2026-11-01T00:00:00Z',
  // Pending unless closed, as the server says (CU-05).
  pending: over.status !== 'closed',
  ...over,
})

/** A case an agent found. */
const agent = (n: number, over: Partial<InboxCase> = {}): InboxCase => kase(n, { agentFound: true, slaApplies: false, source: { kind: 'finding', job: 'Auditor' }, key: { cluster: 'prod', object: 'shop/Deployment/web', check: 'pod.no-limits' }, resource: 'shop/Deployment/web', ...over })

const card = (id: string, over: Partial<Proposal> = {}): Proposal =>
  ({
    id,
    agent: 'troubleshooter',
    agentName: 'Troubleshooter',
    source: { kind: 'case', id: 'C-4', label: 'Case C-4' },
    cluster: 'prod',
    clusterName: 'Production',
    title: 'Restart web',
    actions: [],
    condition: { kind: 'none' },
    expires: '2026-11-09T00:00:00Z',
    state: 'queued',
    created: '2026-11-04T00:00:00Z',
    updated: '2026-11-04T00:00:00Z',
    needed: 1,
    plans: [{ summary: 'restart', targets: ['deployment shop/web'] }],
    canApprove: true,
    ...over,
  }) as Proposal

const approval = (over: Partial<ApprovalRow> = {}): ApprovalRow => ({ kind: 'script', state: 'pending', what: 'reserve v2', requestedBy: 'Cy', requestedAt: '2026-11-02T00:00:00Z', script: 'reserve', version: 2, ...over })

describe('the Inbox’s rows (IN-02)', () => {
  it('keeps every case New or In progress pending, with its object and cluster', () => {
    for (const status of ['new', 'inProgress'] as const) expect(caseRow(kase(1, { status }), ctx).pending).toBe(true)
    const done = caseRow(kase(2, { status: 'closed', outcome: 'noFixNeeded' }), ctx)
    expect(done).toMatchObject({ pending: false, status: 'Closed: no fix needed', tone: 'muted' })
    // Closed, it orders by when it was closed.
    expect(done.since).toBe('2026-11-02T00:00:00Z')
    expect(caseRow(kase(2, { status: 'closed', outcome: 'fix', closedAt: '2026-11-03T00:00:00Z' }), ctx)).toMatchObject({ since: '2026-11-03T00:00:00Z', tone: 'ok' })
    const r = caseRow(kase(3, { resource: 'shop/Pod/web-1' }), ctx)
    expect(r).toMatchObject({ key: 'case:3', open: ['case', '3'], ref: 'C-3', clusterName: 'Production', resource: 'shop/Pod/web-1', status: 'New', assignable: true, origin: 'people' })
    // What the Cases package adds: pending, the assignee, the waiting time, which target passed.
    const more = caseRow(kase(4, { pending: false, assignee: { id: 'ann', name: 'Ann' }, waitingSince: '2026-11-05T00:00:00Z', late: { response: true } }), ctx)
    expect(more).toMatchObject({ pending: false, assignee: 'Ann', mine: true, late: 'Its response target passed' })
    expect(caseRow(kase(4, { assignee: { id: 'cy' }, createdBy: 'Ann (ann)' }), ctx)).toMatchObject({ assignee: 'cy', mine: false })
    // Mine: assigned to the person, or opened by them while nobody has it.
    expect(caseRow(kase(5, { createdBy: 'Ann (ann)' }), ctx).mine).toBe(true)
    expect(caseRow(kase(6), ctx).mine).toBe(false)
    // The server's name of the cluster comes first (CF-60): the console's own is "local", named in Settings › Clusters.
    expect(caseRow(kase(7, { cluster: 'local', clusterName: 'Lakehouse' }), ctx).clusterName).toBe('Lakehouse')
  })

  it('reads an agent-found case: from the agents, seen ×N, never late without an SLA', () => {
    const r = caseRow(agent(7, { status: 'inProgress', seen: { count: 4, first: '2026-11-01T00:00:00Z', last: '2026-11-04T00:00:00Z' }, late: { resolve: true } }), ctx)
    expect(r).toMatchObject({ origin: 'agents', status: 'In progress · seen ×4', late: undefined })
    expect(caseRow(agent(8, { slaApplies: true, late: { resolve: true } }), ctx).late).toBe('Its resolution target passed')
    expect(rowTerms(r).map((t) => t.text)).toEqual(expect.arrayContaining(['pod.no-limits', 'Auditor', 'agent']))
  })

  it('names a proposed change’s object, and whether it waits for this person', () => {
    const r = proposalRow(card('PC-1'))
    expect(r).toMatchObject({ kind: 'proposal', pending: true, forMe: true, resource: 'shop/deployment/web', status: 'Waiting' })
    expect(proposalRow(card('PC-2', { canApprove: false })).forMe).toBe(false)
    expect(proposalRow(card('PC-3', { state: 'applied' })).pending).toBe(false)
  })

  it('gives every approval a stable id, and leaves out of the gear what the person asked for', () => {
    expect(approvalId(approval({ kind: 'admin', id: '12' }))).toBe('12')
    expect(approvalId(approval())).toBe('script-reserve-v2')
    const change = approval({ kind: 'change', cluster: 'prod', fleet: 'web', what: 'web: 2 variables' })
    expect(approvalId(change)).toBe(approvalId({ ...change }))
    expect(approvalId(change)).not.toBe(approvalId({ ...change, what: 'web: 3 variables' }))
    expect(approvalRow(approval()).forMe).toBe(true)
    expect(approvalRow(approval({ own: true }))).toMatchObject({ forMe: false, mine: true, pending: true })
    expect(approvalRow(approval({ state: 'approved', approvedAt: '2026-11-03T00:00:00Z' }))).toMatchObject({ pending: false, since: '2026-11-03T00:00:00Z', status: 'Approved' })
  })

  it('lists drafts and known issues back for their owner, wiki runs and Confluence edits', () => {
    expect(knownRow({ id: 'KI-2', title: 'VPN drops', status: 'investigating', kind: 'normal', updated: '', following: false, source: 'agent', created: '2026-11-01T00:00:00Z', draft: true }, ctx)).toMatchObject({ kind: 'known', open: ['known', 'KI-2'], pending: true, status: 'Draft', origin: 'agents' })
    expect(knownRow({ id: 'KI-3', title: 'Disk fills', status: 'investigating', kind: 'normal', updated: '2026-11-02T00:00:00Z', following: false, source: 'operator', created: '2026-10-01T00:00:00Z', owner: { id: 'ann' }, stillPresent: { count: 2, last: '2026-11-04T00:00:00Z' } }, ctx)).toMatchObject({ status: 'Investigating: still seen', tone: 'err', since: '2026-11-04T00:00:00Z', mine: true })
    const run = draftRunRow({ id: 'd1', started: '2026-11-01T00:00:00Z', by: 'mycroft', state: 'partial', store: 'git', pages: [{ path: 'a.md', title: 'Alpha', kind: 'page', diff: '', added: 1, removed: 0, state: 'waiting' }, { path: 'b.md', title: 'Beta', kind: 'page', diff: '', added: 1, removed: 0, state: 'published' }] })
    expect(run).toMatchObject({ title: 'Alpha and 1 more page', pending: true, status: 'Partly reviewed' })
    expect(conflictRow({ id: 'cf1', kind: 'known', title: 'VPN', their: '', at: '2026-11-01T00:00:00Z', ownerName: 'Ann' })).toMatchObject({ title: 'Edited in Confluence: VPN', pending: true, assignee: 'Ann' })
  })
})

describe('the Inbox’s filters and order (IN-03, IN-04)', () => {
  const rows = [
    caseRow(kase(1, { priority: 2, since: '2026-11-03T00:00:00Z' }), ctx),
    caseRow(kase(2, { priority: 1, since: '2026-11-04T00:00:00Z' }), ctx),
    caseRow(kase(3, { priority: 1, since: '2026-11-01T00:00:00Z', assignee: { id: 'ann', name: 'Ann' } }), ctx),
    caseRow(kase(4, { status: 'closed', updated: '2026-11-05T00:00:00Z' }), ctx),
    caseRow(kase(5, { status: 'closed', updated: '2026-11-06T00:00:00Z', cluster: 'dev' }), ctx),
    caseRow(agent(6, { priority: 4, since: '2026-10-01T00:00:00Z', createdBy: 'Ann (ann)' }), ctx),
    proposalRow(card('PC-1')),
    caseRow(kase(7, { priority: 0, since: '2026-11-05T00:00:00Z' }), ctx),
  ]
  const f: InboxFilter = { show: 'pending', who: 'all', priority: null, cluster: '', source: null, type: null }

  it('sorts pending first, then P0 first, then the longest waiting; then what was done, newest first', () => {
    const sorted = sortRows(rows, INBOX_COLUMNS, { col: 'waiting', dir: 'asc' }, Date.now())
    expect(sorted.map((r) => r.ref)).toEqual(['C-7', 'C-3', 'C-2', 'C-1', 'C-6', 'PC-1', 'C-5', 'C-4'])
    expect(inboxOrderKey({ waiting: true, since: '2026-01-01T00:00:00Z' })).toBeGreaterThan(inboxOrderKey({ waiting: true, priority: 4, since: '2026-12-01T00:00:00Z' }))
  })

  it('keeps what is pending, or everything under All', () => {
    expect(filterRows(rows, f).map((r) => r.ref)).toEqual(['C-1', 'C-2', 'C-3', 'C-6', 'PC-1', 'C-7'])
    expect(filterRows(rows, { ...f, show: 'all' })).toHaveLength(8)
  })

  it('narrows by source, whose, priority (P0 too) and cluster; no more by kind (CF-38)', () => {
    expect(f).not.toHaveProperty('kind')
    expect(filterRows(rows, { ...f, source: 'agents' }).map((r) => r.ref)).toEqual(['C-6', 'PC-1'])
    expect(filterRows(rows, { ...f, source: 'people' }).map((r) => r.ref)).toEqual(['C-1', 'C-2', 'C-3', 'C-7'])
    expect(filterRows(rows, { ...f, priority: 1 }).map((r) => r.ref)).toEqual(['C-2', 'C-3'])
    expect(filterRows(rows, { ...f, priority: 0 }).map((r) => r.ref)).toEqual(['C-7'])
    expect(filterRows(rows, { ...f, show: 'all', cluster: 'dev' }).map((r) => r.ref)).toEqual(['C-5'])
    expect(filterRows(rows, { ...f, who: 'mine' }).map((r) => r.ref)).toEqual(['C-3', 'C-6'])
    // Unassigned: cases nobody has; kinds nobody is assigned to, while they wait.
    expect(filterRows(rows, { ...f, who: 'unassigned' }).map((r) => r.ref)).toEqual(['C-1', 'C-2', 'C-6', 'PC-1', 'C-7'])
    expect(whoseMatch(proposalRow(card('PC-9', { state: 'applied' })), 'unassigned')).toBe(false)
  })

  it('counts what is pending of each kind', () => {
    expect(pendingByKind(rows)).toEqual({ case: 5, proposal: 1, approval: 0, known: 0, knowledge: 0, rule: 0 })
    expect(pendingByKind(rows, 'mine')).toMatchObject({ case: 2, proposal: 0 })
  })

  it('reads the kinds of old addresses, and finds a row by its words', () => {
    expect(inboxKindOf('approvals')).toBe('approval')
    expect(inboxKindOf('reports')).toBe('case')
    expect(inboxKindOf('finding')).toBeNull()
    expect(inboxKindOf('nonsense')).toBeNull()
    const words = rowTerms(rows[7]!).map((t) => t.text)
    expect(words).toEqual(expect.arrayContaining(['Case', 'C-7', 'P0', 'Production']))
  })
})

// --- Owner's feedback on Cases (Phase 12, wave 6c) ---

describe('the Inbox’s rows since wave 6c (CF-32, CF-35 to CF-38)', () => {
  it('has no Kind column; the first column is Priority, with the boxes', () => {
    const ids = INBOX_COLUMNS.map((c) => c.id)
    expect(ids).not.toContain('kind')
    expect(ids[0]).toBe('priority')
    expect(INBOX_COLUMNS[0]!.head).toBeTypeOf('function')
    expect(ids).toEqual(expect.arrayContaining(['type', 'incident', 'last']))
  })

  it('reads a case’s type, incident number, queued analysis and last change', () => {
    const r = caseRow(agent(9, { type: 'security', incident: 'INC0012345', analysisQueued: true, lastBy: { name: 'Auditor', kind: 'agent' }, lastAt: '2026-11-06T00:00:00Z' }), ctx)
    expect(r).toMatchObject({ type: 'security', incident: 'INC0012345', analysisQueued: true, lastBy: { name: 'Auditor', kind: 'agent' }, lastAt: '2026-11-06T00:00:00Z' })
    // The search finds it by its type in words, its incident number and who changed it.
    expect(rowTerms(r).map((t) => t.text)).toEqual(expect.arrayContaining(['Security', 'INC0012345', 'Auditor', 'analysis queued']))
    // Without a last change of its own, the last update is when.
    expect(caseRow(kase(10), ctx).lastAt).toBe('2026-11-02T00:00:00Z')
    // createdBy as an actor: Mine still finds what the person opened.
    expect(caseRow(kase(11, { createdBy: { name: 'Ann (ann)', kind: 'person' } }), ctx).mine).toBe(true)
  })

  it('narrows to the cases of one type (CF-37)', () => {
    const rows = [caseRow(kase(1, { type: 'security' }), ctx), caseRow(kase(2, { type: 'capacity' }), ctx), proposalRow(card('PC-1'))]
    const f: InboxFilter = { show: 'pending', who: 'all', priority: null, cluster: '', source: null, type: 'security' }
    expect(filterRows(rows, f).map((r) => r.ref)).toEqual(['C-1'])
    expect(filterRows(rows, { ...f, type: null })).toHaveLength(3)
  })

  it('shows a case that waits for its reporter as "Waiting for the reporter", and narrows to it by status (CF-67)', () => {
    const rows = [
      caseRow(kase(1, { source: { kind: 'report' }, status: 'inProgress', askedReporter: true }), ctx),
      caseRow(kase(2, { source: { kind: 'report' }, status: 'inProgress' }), ctx),
      caseRow(kase(3), ctx),
      caseRow(kase(4, { status: 'closed', outcome: 'noFixNeeded', askedReporter: false }), ctx),
      proposalRow(card('PC-1')),
    ]
    expect(rows.map((r) => r.status)).toEqual(['Waiting for the reporter', 'In progress', 'New', 'Closed: no fix needed', expect.any(String)])
    expect(rows[0]).toMatchObject({ shownStatus: 'waiting', tone: 'muted', pending: true })
    expect(rows[1]).toMatchObject({ shownStatus: 'inProgress', tone: 'info' })
    const col = INBOX_COLUMNS.find((c) => c.id === 'status')!
    expect(col.export(rows[0]!, Date.now())[0]!.cell).toBe('Waiting for the reporter')
    const f: InboxFilter = { show: 'all', who: 'all', priority: null, cluster: '', source: null, type: null, status: 'waiting' }
    expect(filterRows(rows, f).map((r) => r.ref)).toEqual(['C-1'])
    expect(filterRows(rows, { ...f, status: 'inProgress' }).map((r) => r.ref)).toEqual(['C-2'])
    expect(filterRows(rows, { ...f, status: 'new' }).map((r) => r.ref)).toEqual(['C-3'])
    expect(filterRows(rows, { ...f, status: 'closed' }).map((r) => r.ref)).toEqual(['C-4'])
    expect(filterRows(rows, { ...f, status: null })).toHaveLength(5)
  })

  it('says the last change of a proposed change: its last actor, else who decided or proposed it (CF-35)', () => {
    expect(proposalRow(card('PC-1', { proposedBy: { name: 'Troubleshooter', kind: 'agent' } }))).toMatchObject({ lastBy: { name: 'Troubleshooter', kind: 'agent' }, lastAt: '2026-11-04T00:00:00Z' })
    expect(proposalRow(card('PC-2', { lastBy: { name: 'system', kind: 'system' }, lastAt: '2026-11-05T00:00:00Z' }))).toMatchObject({ lastBy: { kind: 'system' }, lastAt: '2026-11-05T00:00:00Z' })
  })

  it('shows the type, incident, last change and queued analysis in their columns, and exports them', () => {
    const r = caseRow(kase(12, { type: 'outage', incident: 'INC7', analysisQueued: true, lastBy: { name: 'Pat', kind: 'person' }, lastAt: new Date(Date.now() - 5 * 60_000).toISOString() }), ctx)
    const col = (id: string) => INBOX_COLUMNS.find((c) => c.id === id)!
    expect(col('type').export(r, Date.now())).toEqual([{ head: 'Type', cell: 'Outage' }])
    expect(col('incident').export(r, Date.now())).toEqual([{ head: 'Incident', cell: 'INC7' }])
    expect(col('last').export(r, Date.now())[0]).toEqual({ head: 'Last change by', cell: 'Pat' })
    expect(col('status').export(r, Date.now())[0]!.cell).toContain('(analysis queued)')
  })
})
