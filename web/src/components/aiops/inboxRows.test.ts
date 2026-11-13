import { describe, expect, it } from 'vitest'
import type { ApprovalRow } from '../../api/automation'
import type { Proposal } from '../../api/cards'
import { inboxKindOf, inboxOrderKey } from '../../lib/cases'
import { STAGE } from '../../api/casework'
import { sortRows } from '../../lib/table'
import {
  approvalId,
  approvalRow,
  caseRow,
  conflictRow,
  draftRunRow,
  filterRows,
  fromText,
  INBOX_COLUMNS,
  knownRow,
  pageRequestRow,
  pendingByKind,
  proposalRow,
  rowTerms,
  suggestionRow,
  whoseMatch,
  type InboxCase,
  type InboxFilter,
  type RowContext,
} from './inboxRows'

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
const agent = (n: number, over: Partial<InboxCase> = {}): InboxCase =>
  kase(n, { agentFound: true, slaApplies: false, source: { kind: 'finding', job: 'Auditor' }, key: { cluster: 'prod', object: 'shop/Deployment/web', check: 'pod.no-limits' }, resource: 'shop/Deployment/web', ...over })

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
    const done = caseRow(kase(2, { status: 'closed', outcome: 'noAction' }), ctx)
    expect(done).toMatchObject({ pending: false, status: 'Closed: no action needed', tone: 'muted' })
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
    expect(knownRow({ id: 'KI-2', title: 'VPN drops', status: 'investigating', kind: 'normal', updated: '', following: false, source: 'agent', created: '2026-11-01T00:00:00Z', draft: true }, ctx)).toMatchObject({
      kind: 'known',
      open: ['known', 'KI-2'],
      pending: true,
      status: 'Draft',
      origin: 'agents',
    })
    expect(
      knownRow(
        {
          id: 'KI-3',
          title: 'Disk fills',
          status: 'investigating',
          kind: 'normal',
          updated: '2026-11-02T00:00:00Z',
          following: false,
          source: 'operator',
          created: '2026-10-01T00:00:00Z',
          owner: { id: 'ann' },
          stillPresent: { count: 2, last: '2026-11-04T00:00:00Z' },
        },
        ctx,
      ),
    ).toMatchObject({ status: 'Investigating: still seen', tone: 'err', since: '2026-11-04T00:00:00Z', mine: true })
    const run = draftRunRow({
      id: 'd1',
      started: '2026-11-01T00:00:00Z',
      by: 'mycroft',
      state: 'partial',
      store: 'git',
      pages: [
        { path: 'a.md', title: 'Alpha', kind: 'page', diff: '', added: 1, removed: 0, state: 'waiting' },
        { path: 'b.md', title: 'Beta', kind: 'page', diff: '', added: 1, removed: 0, state: 'published' },
      ],
    })
    expect(run).toMatchObject({ title: 'Alpha and 1 more page', pending: true, status: 'Partly reviewed' })
    expect(conflictRow({ id: 'cf1', kind: 'known', title: 'VPN', their: '', at: '2026-11-01T00:00:00Z', ownerName: 'Ann' })).toMatchObject({ title: 'Edited in Confluence: VPN', pending: true, assignee: 'Ann' })
  })

  it('lists a request to retire a wiki page: pending for another person, never for who asked (KN-24)', () => {
    const req = { id: 'p-1', action: 'retire' as const, path: 'pages/system/backups.md', title: 'Backups', reason: 'The NAS was replaced.', by: 'bob', name: 'Bob', at: '2026-11-09T10:00:00Z', state: 'waiting' as const }
    expect(pageRequestRow({ ...req, canApprove: true })).toMatchObject({
      key: 'knowledge:p-1',
      kind: 'knowledge',
      open: ['knowledge', 'p-1'],
      title: 'Retire: Backups',
      status: 'Retire: waits for approval',
      pending: true,
      forMe: true,
      mine: false,
      origin: 'people',
      since: '2026-11-09T10:00:00Z',
    })
    expect(pageRequestRow({ ...req, canApprove: false, mine: true })).toMatchObject({ pending: true, forMe: false, mine: true })
    expect(pageRequestRow({ ...req, action: 'activate', canApprove: true }).title).toBe('Make active again: Backups')
    expect(pageRequestRow({ ...req, state: 'approved', decidedName: 'Ann', decided: '2026-11-09T11:00:00Z' })).toMatchObject({ pending: false, forMe: false, status: 'Approved', since: '2026-11-09T11:00:00Z', lastBy: 'Ann' })
    expect(rowTerms(pageRequestRow(req)).map((t) => t.text)).toContain('The NAS was replaced.')
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

  it('narrows to the cases the person is involved in, as the server says (EM-08)', () => {
    const mixed = [caseRow(kase(8, { involved: true }), ctx), caseRow(kase(9), ctx), caseRow(kase(10, { status: 'closed', involved: true }), ctx), proposalRow(card('PC-2'))]
    expect(filterRows(mixed, { ...f, who: 'involved' }).map((r) => r.ref)).toEqual(['C-8'])
    expect(filterRows(mixed, { ...f, show: 'all', who: 'involved' }).map((r) => r.ref)).toEqual(['C-8', 'C-10'])
    expect(pendingByKind(mixed, 'involved')).toMatchObject({ case: 1, proposal: 0 })
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
    const r = caseRow(agent(9, { type: 'security', incident: 'INC0012345', stage: 'analysisQueued', lastBy: { name: 'Auditor', kind: 'agent' }, lastAt: '2026-11-06T00:00:00Z' }), ctx)
    expect(r).toMatchObject({ type: 'security', incident: 'INC0012345', stage: 'analysisQueued', lastBy: { name: 'Auditor', kind: 'agent' }, lastAt: '2026-11-06T00:00:00Z' })
    // The search finds it by its type in words, its incident number and who changed it.
    expect(rowTerms(r).map((t) => t.text)).toEqual(expect.arrayContaining(['Security', 'INC0012345', 'Auditor', 'waiting for analysis']))
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
      caseRow(kase(4, { status: 'closed', outcome: 'noAction', askedReporter: false }), ctx),
      proposalRow(card('PC-1')),
    ]
    expect(rows.map((r) => r.status)).toEqual(['Waiting for the reporter', 'In progress', 'New', 'Closed: no action needed', expect.any(String)])
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
    const r = caseRow(kase(12, { type: 'outage', incident: 'INC7', stage: 'analysisQueued', lastBy: { name: 'Pat', kind: 'person' }, lastAt: new Date(Date.now() - 5 * 60_000).toISOString() }), ctx)
    const col = (id: string) => INBOX_COLUMNS.find((c) => c.id === id)!
    expect(col('type').export(r, Date.now())).toEqual([{ head: 'Type', cell: 'Outage' }])
    expect(col('incident').export(r, Date.now())).toEqual([{ head: 'Incident', cell: 'INC7' }])
    expect(col('last').export(r, Date.now())[0]).toEqual({ head: 'Last change by', cell: 'Pat' })
    expect(col('status').export(r, Date.now())[0]!.cell).toContain('(waiting for analysis)')
  })

  it('says what a case waits for, one stage beside its status, in the export and to the search (CF-107)', () => {
    const said = (over: Partial<InboxCase>) => INBOX_COLUMNS.find((c) => c.id === 'status')!.export(caseRow(kase(5, over), ctx), Date.now())[0]!.cell
    expect(Object.keys(STAGE)).toEqual(['analysisQueued', 'analyzing', 'fixSuggested', 'approval', 'secondApproval', 'applying', 'changeFailed', 'validation', 'closure', 'notHeld', 'verified'])
    expect(said({ status: 'new', stage: 'analysisQueued' })).toBe('New (waiting for analysis)')
    expect(said({ status: 'new', stage: 'analyzing' })).toBe('New (analysis running)')
    expect(said({ status: 'new', stage: 'fixSuggested' })).toBe('New (fix suggested)')
    expect(said({ status: 'new', stage: 'approval' })).toBe('New (waiting for approval)')
    expect(said({ status: 'new', stage: 'secondApproval' })).toBe('New (waiting for a second approval)')
    expect(said({ status: 'new', stage: 'applying' })).toBe('New (change running)')
    expect(said({ status: 'inProgress', stage: 'changeFailed' })).toBe('In progress (change failed)')
    expect(said({ status: 'inProgress', stage: 'validation' })).toBe('In progress (waiting for a validation run)')
    expect(said({ status: 'inProgress', stage: 'closure' })).toBe('In progress (waiting for closure)')
    expect(said({ status: 'inProgress', stage: 'notHeld' })).toBe('In progress (fix did not hold)')
    // A case a person closed as fixed still waits for its run; verified, it says so.
    expect(said({ status: 'closed', outcome: 'fix', pending: false, stage: 'validation' })).toBe('Closed: fixed (waiting for a validation run)')
    expect(said({ status: 'closed', outcome: 'fix', pending: false, stage: 'verified' })).toBe('Closed: fixed (fix verified)')
    // No stage, and one this console has no words for, add nothing.
    expect(said({ status: 'inProgress' })).toBe('In progress')
    expect(said({ status: 'inProgress', stage: 'later' as never })).toBe('In progress')
    expect(rowTerms(caseRow(kase(5, { stage: 'closure' }), ctx)).map((t) => t.text)).toContain('waiting for closure')
  })
})

// --- FU-02: who or what raised each row ---

describe('the Inbox’s From column (FU-02)', () => {
  it('says who raised each kind of row: a person, an agent with its job, or the system’s source', () => {
    expect(caseRow(kase(1, { source: { kind: 'report' }, createdBy: { name: 'Pat', kind: 'person' } }), ctx).from).toEqual({ name: 'Pat', kind: 'person' })
    // A bare name, as the server sent one before, is a person's.
    expect(caseRow(kase(2), ctx).from).toEqual({ name: 'Bob (bob)', kind: 'person' })
    expect(caseRow(agent(3, { createdBy: { name: 'Auditor', kind: 'agent' } }), ctx).from).toEqual({ name: 'Auditor', kind: 'agent', via: 'Auditor' })
    expect(caseRow(agent(4, { createdBy: { name: 'an agent', kind: 'agent' }, source: { kind: 'finding', job: 'nightly-audit' } }), ctx).from).toEqual({ name: 'An agent', kind: 'agent', via: 'nightly-audit' })
    expect(caseRow(kase(5, { source: { kind: 'alert', alert: 'KubePodCrashLooping' }, createdBy: { name: 'system', kind: 'system' } }), ctx).from).toEqual({ name: 'Alert: KubePodCrashLooping', kind: 'system' })
    expect(caseRow(kase(6, { createdBy: undefined }), ctx).from).toBeUndefined()
    expect(proposalRow(card('PC-1', { proposedBy: { name: 'Troubleshooter', kind: 'agent' }, source: { kind: 'job', job: 'alert-diagnosis' } } as Partial<Proposal>)).from).toEqual({
      name: 'Troubleshooter',
      kind: 'agent',
      via: 'alert-diagnosis',
    })
    expect(proposalRow(card('PC-2', { source: { kind: 'chat', setBy: 'Ann' } } as Partial<Proposal>)).from).toEqual({ name: 'Troubleshooter', kind: 'agent', via: 'chat with Ann' })
    expect(approvalRow(approval()).from).toEqual({ name: 'Cy', kind: 'person' })
    const known = { id: 'KI-1', title: 'Slow', status: 'investigating', kind: 'normal', created: '2026-11-01T00:00:00Z', updated: '2026-11-01T00:00:00Z' }
    expect(knownRow({ ...known, source: 'agent', draft: true } as never, ctx).from).toEqual({ name: 'An agent', kind: 'agent' })
    expect(knownRow({ ...known, source: 'confluence', draft: true } as never, ctx).from).toEqual({ name: 'Confluence', kind: 'system' })
    expect(knownRow({ ...known, source: 'operator', createdBy: { id: 'olga', name: 'Olga' } } as never, ctx).from).toEqual({ name: 'Olga', kind: 'person' })
    // Older than FU-02: nobody is named.
    expect(knownRow({ ...known, source: 'operator' } as never, ctx).from).toBeUndefined()
    expect(draftRunRow({ id: 'r-1', started: '2026-11-01T00:00:00Z', by: 'schedule', state: 'waiting', store: 'git', pages: [] }).from).toEqual({ name: 'Mycroft', kind: 'agent', via: 'schedule' })
    expect(pageRequestRow({ id: 'q-1', action: 'retire', path: 'a.md', title: 'A', reason: 'old', by: 'sam', name: 'Sam', at: '2026-11-01T00:00:00Z', state: 'waiting' }).from).toEqual({ name: 'Sam', kind: 'person' })
    expect(conflictRow({ id: 'x', kind: 'wiki', title: 'A', their: '', at: '2026-11-01T00:00:00Z' }).from).toEqual({ name: 'Confluence', kind: 'system' })
    expect(suggestionRow({ id: 'r', says: 'x', type: 't', jobs: [], from: [], runs: 1, clusters: 1, created: '2026-11-01T00:00:00Z' }).from).toEqual({ name: 'Housekeeping', kind: 'system' })
  })

  it('is a column before Assignee, sorted by name, exported, searched and filtered by', () => {
    const ids = INBOX_COLUMNS.map((c) => c.id)
    expect(ids.indexOf('from')).toBe(ids.indexOf('assignee') - 1)
    const col = INBOX_COLUMNS.find((c) => c.id === 'from')!
    expect(col.label).toBe('From')
    const pat = caseRow(kase(1, { createdBy: { name: 'Pat', kind: 'person' } }), ctx)
    const auditor = caseRow(agent(2, { createdBy: { name: 'Auditor', kind: 'agent' }, source: { kind: 'finding', job: 'nightly-audit' } }), ctx)
    const none = caseRow(kase(3, { createdBy: undefined }), ctx)
    const sorted = sortRows([pat, none, auditor], INBOX_COLUMNS, { col: 'from', dir: 'asc' }, Date.now())
    expect(sorted.map((r) => r.ref)).toEqual(['C-2', 'C-1', 'C-3'])
    expect(col.export(auditor, Date.now())).toEqual([{ head: 'From', cell: 'Auditor (agent) · nightly-audit' }])
    expect(col.export(none, Date.now())).toEqual([{ head: 'From', cell: null }])
    expect(fromText(pat.from)).toBe('Pat')
    expect(rowTerms(auditor).map((t) => t.text)).toEqual(expect.arrayContaining(['Auditor', 'nightly-audit']))
    const f: InboxFilter = { show: 'all', who: 'all', priority: null, cluster: '', source: null, type: null, from: 'Pat' }
    expect(filterRows([pat, auditor, none], f).map((r) => r.ref)).toEqual(['C-1'])
    expect(filterRows([pat, auditor, none], { ...f, from: '' })).toHaveLength(3)
  })
})
