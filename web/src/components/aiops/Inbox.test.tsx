import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as automation from '../../api/automation'
import { CallError } from '../../api/call'
import * as cards from '../../api/cards'
import * as casework from '../../api/casework'
import * as issues from '../../api/issues'
import * as knowledge from '../../api/knowledge'
import * as rules from '../../api/rules'
import type { Me } from '../../api/types'
import { casesAccess, forgetSharedForTests, setIssueMeForTests } from '../../lib/cases'
import { parseHash } from '../../lib/router'
import { AIOpsPage } from './AIOpsPage'
import type { InboxCase } from './inboxRows'
import { kindsFor } from './useInbox'

vi.mock('../../api/issues', async (original) => ({
  ...(await original<typeof import('../../api/issues')>()),
  fetchIssueMe: vi.fn(() => new Promise(() => undefined)),
  fetchReports: vi.fn(),
  fetchReport: vi.fn(),
  fetchKnownAll: vi.fn(),
  fetchKnownIssue: vi.fn(),
  fetchServices: vi.fn(),
  previewMatch: vi.fn(),
}))
vi.mock('../../api/automation', async (original) => ({ ...(await original<typeof import('../../api/automation')>()), fetchApprovals: vi.fn(), decideRequest: vi.fn() }))
vi.mock('../../api/casework', async (original) => ({ ...(await original<typeof import('../../api/casework')>()), fetchCases: vi.fn(), fetchCase: vi.fn(), bulkCases: vi.fn(), closeKnown: vi.fn(), fetchOpenKnown: vi.fn() }))
vi.mock('../../api/cards', async (original) => ({ ...(await original<typeof import('../../api/cards')>()), fetchProposals: vi.fn(), fetchProposal: vi.fn() }))
vi.mock('../../api/knowledge', async (original) => ({
  ...(await original<typeof import('../../api/knowledge')>()),
  fetchDrafts: vi.fn(),
  fetchConflicts: vi.fn(),
  fetchPageRequests: vi.fn(),
  approvePageRequest: vi.fn(),
  rejectPageRequest: vi.fn(),
}))
vi.mock('../../api/rules', async (original) => ({ ...(await original<typeof import('../../api/rules')>()), fetchSuggestions: vi.fn(), fetchRules: vi.fn(), promoteRule: vi.fn() }))

const admin: Me = { mode: 'oidc', user: { subject: 's', username: 'ann', name: 'Ann' }, admin: true, roles: ['superAdmin'], method: 'oidc', sessionIdleSeconds: 0 }
const viewer: Me = { ...admin, admin: false, roles: ['viewer'] }
const op = { person: { id: 'ann', name: 'Ann' }, report: true, operator: true, admin: true, company: '', statusPage: '', reportURL: '/report' }

const kase = (n: number, over: Partial<InboxCase> = {}): InboxCase => ({
  id: `C-${n}`,
  number: n,
  title: `Case ${n}`,
  priority: 2,
  cluster: 'prod',
  source: { kind: 'manual' },
  status: 'inProgress',
  agentFound: false,
  slaApplies: true,
  createdBy: 'Bob (bob)',
  created: '2026-11-03T00:00:00Z',
  updated: '2026-11-03T00:00:00Z',
  since: '2026-11-03T00:00:00Z',
  // Pending unless closed, as the server says (CU-05).
  pending: over.status !== 'closed',
  ...over,
})

/** A problem report: a case of its own, from the issue service (CU-01). */
const reported = (n: number, over: Partial<InboxCase> = {}): InboxCase =>
  kase(n, {
    title: `Checkout hangs after Pay, number ${n}`,
    priority: 3,
    cluster: undefined,
    source: { kind: 'report' },
    status: 'new',
    createdBy: 'Bob',
    created: `2026-11-05T0${n}:00:00Z`,
    updated: `2026-11-05T0${n}:00:00Z`,
    since: `2026-11-05T0${n}:00:00Z`,
    waitingSince: `2026-11-05T0${n}:00:00Z`,
    ...over,
  })

/** A case an agent found, In progress and seen three times: no SLA below P1 (wave 6). */
const found = kase(6, {
  title: 'cluster-admin bound to a service account',
  priority: 1,
  agentFound: true,
  slaApplies: false,
  source: { kind: 'finding', run: 'r-1', job: 'Auditor' },
  key: { cluster: 'prod', object: 'ClusterRoleBinding/ci', check: 'rbac.cluster-admin-binding' },
  seen: { count: 3, first: '2026-11-02T00:00:00Z', last: '2026-11-04T00:00:00Z' },
  createdBy: 'auditor',
  since: '2026-11-04T00:00:00Z',
  waitingSince: '2026-11-04T00:00:00Z',
})

const proposal = {
  id: 'PC-1',
  agent: 'troubleshooter',
  agentName: 'Troubleshooter',
  source: { kind: 'alert', label: 'Alert' },
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
  plans: [],
  canApprove: true,
} as unknown as cards.Proposal

const run: knowledge.DraftRun = {
  id: 'd1',
  started: '2026-11-01T00:00:00Z',
  by: 'mycroft',
  state: 'waiting',
  store: 'git',
  pages: [{ path: 'vpn.md', title: 'VPN', kind: 'page', diff: '+ drops hourly', added: 1, removed: 0, state: 'waiting' }],
}

const console1: automation.ApprovalRow = { kind: 'admin', state: 'pending', what: 'Prod: production → staging', requestedBy: 'Cy', requestedAt: '2026-11-02T00:00:00Z', id: '9', cluster: 'prod', clusterName: 'Production' }

/** The rows on screen, in order, by their keys. */
const keys = () => [...document.querySelectorAll('[data-inbox-row]')].map((r) => r.getAttribute('data-inbox-row'))

beforeEach(() => {
  Element.prototype.scrollTo = () => {}
  // The object links look their objects up; here nothing answers.
  vi.stubGlobal('fetch', async () => new Response('{}', { status: 404 }))
  vi.mocked(issues.fetchServices).mockResolvedValue([])
  vi.mocked(issues.previewMatch).mockResolvedValue({ known: [], services: [], threshold: 80 })
  vi.mocked(issues.fetchKnownAll).mockResolvedValue([{ id: 'KI-2', title: 'VPN drops', status: 'investigating', kind: 'normal', updated: '', following: false, source: 'agent', created: '2026-11-05T00:00:00Z', draft: true }])
  vi.mocked(automation.fetchApprovals).mockResolvedValue({ items: [{ kind: 'script', state: 'pending', what: 'reserve v2', requestedBy: 'Cy', requestedAt: '2026-11-02T00:00:00Z', script: 'reserve', version: 2 }] })
  // The reports are cases: C-3 nobody has, C-4 Ann has, C-5 Cy has.
  const all = [kase(1), kase(2, { status: 'closed', outcome: 'fix', updated: '2026-11-06T00:00:00Z' }), reported(3), reported(4, { assignee: { id: 'ann', name: 'Ann' } }), reported(5, { assignee: { id: 'cy', name: 'Cy' } }), found]
  vi.mocked(casework.fetchCases).mockResolvedValue({ cases: all, clusters: [{ id: 'prod', name: 'Production' }], mayChange: true, me: 'Ann (ann)' })
  vi.mocked(casework.fetchCase).mockImplementation(async (id) => {
    const n = Number(String(id).replace('C-', ''))
    return { ...(all.find((c) => c.number === n) ?? kase(n)), messages: [], proposals: [], history: [], mayChange: true, related: [], allowedOutcomes: ['fix'], rcaRequired: false, mayReopen: true } as casework.Case
  })
  vi.mocked(cards.fetchProposals).mockResolvedValue({ proposals: [proposal], waiting: 1, mayApprove: true })
  vi.mocked(cards.fetchProposal).mockResolvedValue(proposal)
  vi.mocked(knowledge.fetchDrafts).mockResolvedValue({ runs: [] })
  vi.mocked(knowledge.fetchConflicts).mockResolvedValue({ conflicts: [] })
  vi.mocked(knowledge.fetchPageRequests).mockResolvedValue({ requests: [] })
  vi.mocked(rules.fetchSuggestions).mockResolvedValue({ suggestions: [] })
  setIssueMeForTests(op)
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
  forgetSharedForTests()
  setIssueMeForTests(null, false)
  window.history.replaceState(null, '', '/')
})

const page = (hash: string, me: Me = admin) => {
  window.history.replaceState(null, '', '/' + hash)
  return render(<AIOpsPage route={parseHash(hash)} me={me} onToast={() => undefined} />)
}

describe('the Inbox as one table (IN-01 to IN-04)', () => {
  it('offers the kinds by role; findings are not a kind (wave 6)', () => {
    expect(kindsFor(casesAccess(admin, op))).toEqual(['case', 'proposal', 'approval', 'known', 'knowledge', 'rule'])
    expect(kindsFor(casesAccess({ ...viewer, roles: ['admin'] }, { ...op, admin: false }))).toEqual(['case', 'known'])
    expect(kindsFor(casesAccess({ ...viewer, roles: ['superViewer'] }, null))).toEqual(['case', 'proposal', 'knowledge'])
    expect(kindsFor(casesAccess(viewer, { ...op, operator: false, admin: false }))).toEqual(['case'])
  })

  it('lists every kind in one table, not grouped: pending first, then P0 first, then the longest waiting', async () => {
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toHaveLength(8))
    // The agent-found case at P1 first; the closed one is not listed under Pending.
    expect(keys()).toEqual(['case:6', 'case:1', 'case:3', 'case:4', 'case:5', 'approval:script-reserve-v2', 'proposal:PC-1', 'known:KI-2'])
    expect(screen.getByTestId('inbox-show-pending').getAttribute('aria-pressed')).toBe('true')
    const first = document.querySelector('[data-inbox-row="case:1"]') as HTMLElement
    // No Kind column (CF-38): the row's name still says what it is.
    expect(within(first).queryByTestId('inbox-row-kind')).toBeNull()
    expect(first.getAttribute('aria-label')).toMatch(/^Case C-1: Case 1, In progress, P2/)
    expect(within(first).getByTestId('inbox-row-priority').textContent).toBe('P2')
    expect(within(first).getByTestId('inbox-row-status').textContent).toBe('In progress')
    expect(first.textContent).toContain('Production')
    const agent = document.querySelector('[data-inbox-row="case:6"]') as HTMLElement
    expect(within(agent).getByTestId('inbox-row-status').textContent).toBe('In progress · seen ×3')
    expect(within(document.querySelector('[data-inbox-row="case:3"]') as HTMLElement).getByTestId('inbox-row-status').textContent).toBe('New')
    // The columns, as the Nodes table has them: a header that sorts.
    const heads = [...document.querySelectorAll('[role=columnheader]')].map((h) => h.textContent?.replace(/[↑↓]/g, ''))
    expect(heads).toEqual(['Priority', 'Title', 'Type', 'Incident', 'Cluster', 'Object', 'Status', 'Waiting', 'Assignee', 'Last change'])
    fireEvent.click(screen.getByRole('button', { name: /^Title: sort/ }))
    await waitFor(() => expect(keys()[0]).toBe('case:1'))
    expect(keys()[1]).toBe('case:3')
    // Back to the Inbox's order, which the table keeps while the page is open.
    fireEvent.click(screen.getByRole('button', { name: /^Waiting: sort/ }))
    await waitFor(() => expect(keys()[1]).toBe('case:1'))
  })

  it('lists a suggested validation rule for a SuperAdmin, counted and opened in its drawer; never for anyone else (CF-87)', async () => {
    const s: rules.Suggestion = {
      id: 'node.etcd-slow',
      says: 'etcd answers late',
      type: 'other',
      jobs: ['nightly-audit'],
      from: ['other.etcd-slow'],
      runs: 3,
      clusters: 1,
      created: '2026-11-09T03:30:00Z',
      why: 'Found 3 times in 3 runs on 1 cluster by Nightly audit: 3 runs or more make it a candidate.',
    }
    vi.mocked(rules.fetchSuggestions).mockResolvedValue({ suggestions: [s] })
    vi.mocked(rules.fetchRules).mockResolvedValue({ rules: [], found: [], suggestions: [s], jobs: [{ id: 'nightly-audit', label: 'Nightly audit' }], types: ['other'], mayChange: true })
    vi.mocked(rules.promoteRule).mockResolvedValue({ rules: [], found: [], suggestions: [], jobs: [], types: [], mayChange: true })
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('rule:node.etcd-slow'))
    const row = document.querySelector('[data-inbox-row="rule:node.etcd-slow"]') as HTMLElement
    expect(row.getAttribute('aria-label')).toMatch(/^Suggested rule node\.etcd-slow: etcd answers late, Suggested rule, pending/)
    expect(screen.getByTestId('inbox-show-pending').textContent).toBe('Pending9')
    fireEvent.click(row)
    await waitFor(() => expect(window.location.hash).toBe('#/aiops/inbox/rule/node.etcd-slow'))
    cleanup()
    page('#/aiops/inbox/rule/node.etcd-slow')
    expect((await screen.findByTestId('suggestion-why')).textContent).toContain('3 runs or more')
    fireEvent.click(screen.getByTestId('suggestion-promote'))
    await waitFor(() => expect(rules.promoteRule).toHaveBeenCalledWith({ from: ['other.etcd-slow'], id: 'node.etcd-slow', says: 'etcd answers late', type: 'other', jobs: ['nightly-audit'] }))
    // Who is not a SuperAdmin is never asked for them.
    cleanup()
    forgetSharedForTests()
    vi.mocked(rules.fetchSuggestions).mockClear()
    page('#/aiops/inbox', { ...viewer, roles: ['superViewer'] })
    await waitFor(() => expect(keys().length).toBeGreaterThan(0))
    expect(rules.fetchSuggestions).not.toHaveBeenCalled()
    expect(keys()).not.toContain('rule:node.etcd-slow')
  })

  it('lists a request to retire a wiki page; who asked may not approve it, another person does (KN-24)', async () => {
    const req: knowledge.PageRequest = {
      id: 'p-1',
      action: 'retire',
      path: 'pages/system/backups.md',
      title: 'Backups',
      reason: 'The NAS was replaced.',
      by: 'ann',
      name: 'Ann',
      at: '2026-11-09T10:00:00Z',
      state: 'waiting',
      canApprove: false,
      mine: true,
    }
    vi.mocked(knowledge.fetchPageRequests).mockResolvedValue({ requests: [req] })
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('knowledge:p-1'))
    const row = document.querySelector('[data-inbox-row="knowledge:p-1"]') as HTMLElement
    expect(row.textContent).toContain('Retire: Backups')
    cleanup()
    page('#/aiops/inbox/knowledge/p-1')
    const card = await screen.findByTestId('page-request')
    expect(within(card).getByTestId('page-request-reason').textContent).toBe('The NAS was replaced.')
    expect(within(card).getByTestId('page-request-approve')).toHaveProperty('disabled', true)
    expect(within(card).getByTestId('page-request-rule').textContent).toContain('You asked for this')
    expect(within(card).getByTestId('page-request-reject').textContent).toBe('Withdraw…')
    // Withdraw… opens the shared dialog (I-4), its reason optional here as before.
    vi.mocked(knowledge.rejectPageRequest).mockResolvedValue({ ...req, state: 'rejected' })
    fireEvent.click(within(card).getByTestId('page-request-reject'))
    expect(screen.getByRole('alertdialog').getAttribute('aria-label')).toBe('Withdraw the request to retire Backups?')
    expect(screen.getByText('Why (optional)')).toBeTruthy()
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(knowledge.rejectPageRequest).toHaveBeenCalledWith('p-1', ''))
    // Another reviewer approves it.
    cleanup()
    forgetSharedForTests()
    vi.mocked(knowledge.fetchPageRequests).mockResolvedValue({ requests: [{ ...req, canApprove: true, mine: false }] })
    vi.mocked(knowledge.approvePageRequest).mockResolvedValue({ ...req, state: 'approved' })
    page('#/aiops/inbox/knowledge/p-1')
    const approve = await screen.findByTestId('page-request-approve')
    expect(approve).toHaveProperty('disabled', false)
    // Decided, it leaves Pending; its drawer stays open and says so.
    vi.mocked(knowledge.fetchPageRequests).mockImplementation((all) => Promise.resolve({ requests: all ? [{ ...req, state: 'approved', decidedName: 'Bob', decided: '2026-11-09T11:00:00Z' }] : [] }))
    fireEvent.click(approve)
    await waitFor(() => expect(knowledge.approvePageRequest).toHaveBeenCalledWith('p-1'))
    await waitFor(() => expect(screen.getByTestId('page-request-state').textContent).toBe('Approved'))
    expect(screen.queryByTestId('page-request-approve')).toBeNull()
  })

  it('adds under All what was done, newest first, after what is pending', async () => {
    page('#/aiops/inbox?show=all')
    await waitFor(() => expect(keys()).toContain('case:2'))
    expect(keys()[keys().length - 1]).toBe('case:2')
    expect(within(document.querySelector('[data-inbox-row="case:2"]') as HTMLElement).getByTestId('inbox-row-status').textContent).toBe('Closed: fixed')
    expect(casework.fetchCases).toHaveBeenCalledWith({}, false)
    expect(knowledge.fetchDrafts).toHaveBeenCalledWith(true)
  })

  it('has no Kind filter (CF-38): Pending counts every kind', async () => {
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toHaveLength(8))
    expect(screen.queryByTestId('inbox-kinds')).toBeNull()
    expect(screen.queryByTestId('inbox-kind-case')).toBeNull()
    expect(screen.getByTestId('inbox-show-pending').textContent).toBe('Pending8')
  })

  it('narrows to Mine, Unassigned and a priority, and says so in the address; an old ?kind= is let go', async () => {
    const { unmount } = page('#/aiops/inbox?who=mine')
    await waitFor(() => expect(keys()).toEqual(['case:4']))
    expect(screen.getByTestId('inbox-mine').getAttribute('aria-pressed')).toBe('true')
    fireEvent.click(screen.getByTestId('inbox-unassigned'))
    expect(window.location.hash).toBe('#/aiops/inbox?who=unassigned')
    unmount()
    const next = page('#/aiops/inbox?who=unassigned&priority=3')
    await waitFor(() => expect(keys()).toEqual(['case:3']))
    next.unmount()
    // An address of before, with a kind: every row, and the kind drops out of the address at the next filter.
    page('#/aiops/inbox?kind=approvals')
    await waitFor(() => expect(keys()).toHaveLength(8))
    fireEvent.click(screen.getByTestId('inbox-source-people'))
    expect(window.location.hash).toBe('#/aiops/inbox?source=people')
  })

  it('narrows to People or Agents with the Source filter (wave 6)', async () => {
    const { unmount } = page('#/aiops/inbox?source=agents')
    await waitFor(() => expect(keys()).toEqual(['case:6', 'proposal:PC-1', 'known:KI-2']))
    expect(screen.getByTestId('inbox-source-agents').getAttribute('aria-pressed')).toBe('true')
    fireEvent.click(screen.getByTestId('inbox-source-people'))
    expect(window.location.hash).toBe('#/aiops/inbox?source=people')
    unmount()
    page('#/aiops/inbox?source=people')
    await waitFor(() => expect(keys()).toEqual(['case:1', 'case:3', 'case:4', 'case:5', 'approval:script-reserve-v2']))
    fireEvent.click(screen.getByTestId('inbox-source-all'))
    expect(window.location.hash).toBe('#/aiops/inbox')
  })

  it('takes P0 as a priority, not as any', async () => {
    vi.mocked(casework.fetchCases).mockResolvedValue({ cases: [kase(7, { priority: 0 }), kase(1)], clusters: [], mayChange: true, me: '' })
    page('#/aiops/inbox?priority=0&kind=case')
    await waitFor(() => expect(keys()).toEqual(['case:7']))
    expect(within(document.querySelector('[data-inbox-row="case:7"]') as HTMLElement).getByTestId('inbox-row-priority').textContent).toBe('P0')
    expect((screen.getByTestId('inbox-priority') as HTMLSelectElement).value).toBe('0')
  })

  it('opens a row in its drawer at #/aiops/inbox/<kind>/<id>, keeping the filters', async () => {
    const { unmount } = page('#/aiops/inbox?priority=2')
    await waitFor(() => expect(keys()).toEqual(['case:1']))
    fireEvent.click(document.querySelector('[data-inbox-row="case:1"] [role=rowheader]')!)
    expect(window.location.hash).toBe('#/aiops/inbox/case/1?priority=2')
    unmount()
    page('#/aiops/inbox/case/1?priority=2')
    expect((await screen.findByTestId('case-drawer')).textContent).toContain('Case 1')
    expect(document.querySelector('[data-inbox-row="case:1"]')?.getAttribute('aria-selected')).toBe('true')
  })

  it('opens the links it had before: a proposed change, its c<n> as PC-<n> (CF-56), and a report as its case', async () => {
    const { unmount } = page('#/aiops/inbox?proposal=c1')
    await waitFor(() => expect(window.location.hash).toBe('#/aiops/inbox/proposal/PC-1'))
    unmount()
    const old = page('#/aiops/inbox/proposal/c1')
    await waitFor(() => expect(window.location.hash).toBe('#/aiops/inbox/proposal/PC-1'))
    // The drawer opens at the new address only: the card is not read as c1.
    expect(screen.queryByTestId('proposal-drawer')).toBeNull()
    expect(vi.mocked(cards.fetchProposal)).not.toHaveBeenCalledWith('c1')
    old.unmount()
    page(window.location.hash)
    expect((await screen.findByTestId('proposal-drawer')).textContent).toContain('Restart web')
    cleanup()
    page('#/aiops/inbox/report/3')
    expect((await screen.findByTestId('case-drawer')).textContent).toContain('Checkout hangs after Pay, number 3')
  })

  it('lists a known issue back for its owner as pending (wave 6)', async () => {
    vi.mocked(issues.fetchKnownAll).mockResolvedValue([
      {
        id: 'KI-5',
        title: 'Disk fills on db-1',
        status: 'investigating',
        kind: 'normal',
        updated: '2026-11-06T00:00:00Z',
        following: false,
        source: 'operator',
        created: '2026-10-01T00:00:00Z',
        owner: { id: 'ann' },
        stillPresent: { count: 2, last: '2026-11-06T06:00:00Z' },
      },
      {
        id: 'KI-6',
        title: 'Someone else’s',
        status: 'investigating',
        kind: 'normal',
        updated: '2026-11-06T00:00:00Z',
        following: false,
        source: 'operator',
        created: '2026-10-01T00:00:00Z',
        owner: { id: 'cy' },
        stillPresent: { count: 2, last: '2026-11-06T06:00:00Z' },
      },
    ])
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('known:KI-5'))
    expect(keys()).not.toContain('known:KI-6')
    expect(within(document.querySelector('[data-inbox-row="known:KI-5"]') as HTMLElement).getByTestId('inbox-row-status').textContent).toBe('Investigating: still seen')
  })

  it('opens a knowledge draft with its pages', async () => {
    vi.mocked(knowledge.fetchDrafts).mockResolvedValue({ runs: [run] })
    page('#/aiops/inbox/knowledge/d1')
    expect(within(await screen.findByTestId('knowledge-drawer')).getByTestId('inbox-drawer-missing').textContent).toBe('Loading…')
    await screen.findByTestId('knowledge-run')
  })

  it('says a case is late and where the targets are set (IN-05), only where the SLA applies', async () => {
    vi.mocked(casework.fetchCases).mockResolvedValue({ cases: [kase(5, { late: { response: true } }), kase(8, { late: { response: true }, slaApplies: false, agentFound: true })], clusters: [], mayChange: true, me: '' })
    page('#/aiops/inbox?kind=case')
    const late = await screen.findByTestId('inbox-row-late')
    expect(late.getAttribute('href')).toBe('#/admin/settings/sla')
    expect(late.getAttribute('title')).toContain('Its response target passed')
    expect(screen.getAllByTestId('inbox-row-late')).toHaveLength(1)
  })

  it('lists nothing for a source the person may not see, quietly', async () => {
    vi.mocked(cards.fetchProposals).mockRejectedValue(new CallError('Only approvers.', 403))
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toHaveLength(7))
    expect(screen.queryByRole('alert')).toBeNull()
  })
})

describe('an approval in its drawer (CS-06, IB-09)', () => {
  beforeEach(() => {
    vi.mocked(automation.decideRequest).mockReset().mockResolvedValue({})
  })

  it('opens a waiting script version to review where it lives', async () => {
    page('#/aiops/inbox/approval/script-reserve-v2')
    expect((await screen.findByTestId('approval-script-review')).getAttribute('href')).toContain('scripts')
  })

  it('decides a console change here, after a confirmation, and reads the list again', async () => {
    vi.mocked(automation.fetchApprovals).mockResolvedValue({ items: [console1] })
    page('#/aiops/inbox/approval/9')
    fireEvent.click(await screen.findByTestId('approval-admin-approve'))
    expect(automation.decideRequest).not.toHaveBeenCalled()
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(automation.decideRequest).toHaveBeenCalledWith('9', true, ''))
    await waitFor(() => expect(vi.mocked(automation.fetchApprovals).mock.calls.length).toBeGreaterThan(1))
  })

  it('lets whoever asked only withdraw it, with a reason', async () => {
    vi.mocked(automation.fetchApprovals).mockResolvedValue({ items: [{ ...console1, own: true }] })
    page('#/aiops/inbox/approval/9')
    const withdraw = await screen.findByTestId('approval-admin-reject')
    expect(screen.queryByTestId('approval-admin-approve')).toBeNull()
    expect(withdraw.textContent).toBe('Withdraw…')
    fireEvent.click(withdraw)
    // The shared dialog says Withdraw, not Reject, and still asks why (I-4).
    expect(screen.getByTestId('confirm-ok').textContent).toBe('Withdraw')
    expect(screen.getByRole('alertdialog').getAttribute('aria-label')).toMatch(/^Withdraw /)
    expect((screen.getByTestId('confirm-ok') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByTestId('decide-comment'), { target: { value: 'not now' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(automation.decideRequest).toHaveBeenCalledWith('9', false, 'not now'))
  })

  it('says who decided it first when another SuperAdmin did, and shows it decided', async () => {
    // Sam approved it while the drawer was open: the list says so once read after.
    let samFirst = false
    vi.mocked(automation.fetchApprovals).mockImplementation(async () => ({ items: [samFirst ? { ...console1, state: 'approved', approvedBy: 'Sam', approvedAt: '2026-11-03T11:00:00Z' } : console1] }))
    vi.mocked(automation.decideRequest).mockImplementation(async () => {
      samFirst = true
      throw new CallError('Already approved by Sam at 2026-11-03 11:00:00 UTC.', 409, { error: 'Already approved by Sam at 2026-11-03 11:00:00 UTC.' })
    })
    page('#/aiops/inbox/approval/9')
    fireEvent.click(await screen.findByTestId('approval-admin-approve'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    const conflict = await screen.findByTestId('approval-conflict')
    expect(conflict.textContent).toMatch(/^Already approved by Sam at /)
    expect(conflict.textContent).not.toContain('11:00:00')
    await waitFor(() => expect(screen.getByTestId('approval-state').textContent).toBe('Approved'))
    expect(screen.queryByTestId('approval-admin-approve')).toBeNull()
  })
})

// --- Owner's feedback on Cases (Phase 12, wave 6c) ---

describe('the Inbox since wave 6c (CF-35 to CF-38)', () => {
  const pick = (id: string) => fireEvent.click(document.querySelector(`[data-testid="inbox-pick"][data-id="${id}"]`)!)

  it('shows a case’s type, incident number, queued analysis and last change, and narrows by type', async () => {
    vi.mocked(casework.fetchCases).mockResolvedValue({
      cases: [
        kase(1, { type: 'security', incident: 'INC0012345', analysisQueued: true, stage: 'analysisQueued', lastBy: { name: 'Auditor', kind: 'agent' }, lastAt: new Date(Date.now() - 2 * 3600_000).toISOString() }),
        kase(3, { type: 'capacity', lastBy: { name: 'Pat', kind: 'person' }, lastAt: new Date(Date.now() - 5 * 60_000).toISOString() }),
      ],
      clusters: [],
      mayChange: true,
      me: '',
    })
    const { unmount } = page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('case:1'))
    const one = document.querySelector('[data-inbox-row="case:1"]') as HTMLElement
    expect(within(one).getByTestId('inbox-row-type').textContent).toBe('Security')
    expect(within(one).getByTestId('inbox-row-incident').textContent).toBe('INC0012345')
    expect(within(one).getByTestId('inbox-row-stage').textContent).toBe('Waiting for analysis')
    expect(within(one).getByTestId('inbox-row-last').textContent).toBe('Auditor (agent) · 2h ago')
    expect(within(document.querySelector('[data-inbox-row="case:3"]') as HTMLElement).getByTestId('inbox-row-last').textContent).toBe('Pat · 5m ago')
    // The type filter: in the address, cases of that type only.
    fireEvent.change(screen.getByTestId('inbox-type'), { target: { value: 'security' } })
    expect(window.location.hash).toBe('#/aiops/inbox?type=security')
    unmount()
    page('#/aiops/inbox?type=capacity')
    await waitFor(() => expect(keys()).toEqual(['case:3']))
    expect((screen.getByTestId('inbox-type') as HTMLSelectElement).value).toBe('capacity')
  })

  it('shows a case that waits for its reporter as such in the Status column, and narrows to it with the Status filter (CF-67)', async () => {
    vi.mocked(casework.fetchCases).mockResolvedValue({ cases: [kase(1, { status: 'inProgress', askedReporter: true }), kase(3, { status: 'inProgress' }), kase(4, { status: 'new' })], clusters: [], mayChange: true, me: '' })
    const { unmount } = page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('case:1'))
    const status = (n: number) => within(document.querySelector(`[data-inbox-row="case:${n}"]`) as HTMLElement).getByTestId('inbox-row-status').textContent
    expect([status(1), status(3), status(4)]).toEqual(['Waiting for the reporter', 'In progress', 'New'])
    const filter = screen.getByTestId('inbox-status') as HTMLSelectElement
    expect([...filter.options].map((o) => o.textContent)).toEqual(['Any status', 'New', 'In progress', 'Waiting for the reporter', 'Closed'])
    fireEvent.change(filter, { target: { value: 'waiting' } })
    expect(window.location.hash).toBe('#/aiops/inbox?status=waiting')
    unmount()
    page('#/aiops/inbox?status=waiting')
    await waitFor(() => expect(keys()).toEqual(['case:1']))
    expect((screen.getByTestId('inbox-status') as HTMLSelectElement).value).toBe('waiting')
  })

  it('finds a case by its incident number', async () => {
    vi.mocked(casework.fetchCases).mockResolvedValue({ cases: [kase(1, { incident: 'INC0099' }), kase(3)], clusters: [], mayChange: true, me: '' })
    page('#/aiops/inbox?q=INC0099')
    await waitFor(() => expect(keys()).toEqual(['case:1']))
  })

  it('finds a case by its RCA’s cause, closed ones under All: past RCAs are searched as closed cases (wave 6e)', async () => {
    const fixed = (n: number, over: Partial<InboxCase>) => kase(n, { status: 'closed', outcome: 'fix', updated: '2026-11-06T00:00:00Z', ...over })
    vi.mocked(casework.fetchCases).mockResolvedValue({ cases: [fixed(1, { rcaCause: 'The inotify watches ran out on the nodes.' }), fixed(2, { rcaId: 'RCA-4' }), fixed(3, {}), kase(4)], clusters: [], mayChange: true, me: '' })
    const { unmount } = page('#/aiops/inbox?show=all&q=inotify')
    await waitFor(() => expect(keys()).toEqual(['case:1']))
    // The search box says what it searches.
    expect(screen.getByTestId('inbox-search').getAttribute('placeholder')).toBe('Search by title, id, object, validation rule, person or RCA cause')
    unmount()
    // An old case is found by its RCA's id still.
    page('#/aiops/inbox?show=all&q=RCA-4')
    await waitFor(() => expect(keys()).toEqual(['case:2']))
  })

  it('gives a box to case rows only, and none to a person who does not work the cases', async () => {
    const { unmount } = page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toHaveLength(8))
    expect(screen.getAllByTestId('inbox-pick').map((b) => b.getAttribute('data-id'))).toEqual(['C-6', 'C-1', 'C-3', 'C-4', 'C-5'])
    expect(document.querySelector('[data-inbox-row="proposal:PC-1"] [data-testid="inbox-pick"]')).toBeNull()
    unmount()
    vi.mocked(casework.fetchCases).mockResolvedValue({ cases: [kase(1)], clusters: [], mayChange: false, me: '' })
    forgetSharedForTests()
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('case:1'))
    expect(screen.queryByTestId('inbox-pick')).toBeNull()
    expect(screen.queryByTestId('inbox-pick-all')).toBeNull()
  })

  it('picks every case in view with the heading’s box; the bar says how many', async () => {
    page('#/aiops/inbox?source=people')
    await waitFor(() => expect(keys()).toContain('case:3'))
    expect(screen.queryByTestId('inbox-bulkbar')).toBeNull()
    fireEvent.click(screen.getByTestId('inbox-pick-all'))
    expect(screen.getByTestId('inbox-bulk-count').textContent).toBe('4 selected')
    fireEvent.click(screen.getByTestId('inbox-pick-all'))
    expect(screen.queryByTestId('inbox-bulkbar')).toBeNull()
    pick('C-3')
    expect(screen.getByTestId('inbox-bulk-count').textContent).toBe('1 selected')
    fireEvent.click(screen.getByTestId('inbox-bulk-clear'))
    expect(screen.queryByTestId('inbox-bulkbar')).toBeNull()
  })

  it('sets one priority for the picked cases, shows each case’s result, and keeps the refused picked', async () => {
    vi.mocked(casework.bulkCases).mockResolvedValue({
      results: [
        { id: 'C-1', ok: true },
        { id: 'C-3', ok: false, error: 'C-3 is closed.' },
      ],
    })
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('case:3'))
    pick('C-1')
    pick('C-3')
    fireEvent.click(screen.getByTestId('inbox-bulk-priority'))
    fireEvent.change(screen.getByTestId('inbox-bulk-priority-pick'), { target: { value: '0' } })
    const reads = vi.mocked(casework.fetchCases).mock.calls.length
    const changes = vi.mocked(cards.fetchProposals).mock.calls.length
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await screen.findByTestId('inbox-bulk-results')
    expect(casework.bulkCases).toHaveBeenCalledWith(['C-1', 'C-3'], 'priority', { priority: 0 })
    const res = screen.getAllByTestId('inbox-bulk-result')
    expect(res.map((r) => [r.getAttribute('data-id'), r.getAttribute('data-ok')])).toEqual([
      ['C-1', 'true'],
      ['C-3', 'false'],
    ])
    expect(res[1]!.textContent).toContain('C-3 is closed.')
    // The lists read again, the proposed changes too (a close rejects those waiting only for
    // its case, CF-34); the refused one stays picked.
    await waitFor(() => expect(vi.mocked(casework.fetchCases).mock.calls.length).toBeGreaterThan(reads))
    await waitFor(() => expect(vi.mocked(cards.fetchProposals).mock.calls.length).toBeGreaterThan(changes))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    expect(screen.getByTestId('inbox-bulk-count').textContent).toBe('1 selected')
  })

  it('assigns, sets a type and an incident number for all, each as its single change takes it', async () => {
    vi.mocked(casework.bulkCases).mockResolvedValue({ results: [{ id: 'C-1', ok: true }] })
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('case:1'))
    pick('C-1')
    fireEvent.click(screen.getByTestId('inbox-bulk-assign'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(casework.bulkCases).toHaveBeenLastCalledWith(['C-1'], 'assign', { assignee: { id: 'me' } }))
    fireEvent.click(await screen.findByTestId('confirm-ok'))
    // Done, the case is unpicked: pick it again for the next.
    expect(screen.queryByTestId('inbox-bulkbar')).toBeNull()
    pick('C-1')
    fireEvent.click(screen.getByTestId('inbox-bulk-type'))
    fireEvent.change(screen.getByTestId('inbox-bulk-type-pick'), { target: { value: 'compliance' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(casework.bulkCases).toHaveBeenLastCalledWith(['C-1'], 'type', { type: 'compliance' }))
    fireEvent.click(await screen.findByTestId('confirm-ok'))
    pick('C-1')
    fireEvent.click(screen.getByTestId('inbox-bulk-incident'))
    fireEvent.change(screen.getByTestId('inbox-bulk-incident-text'), { target: { value: 'x'.repeat(65) } })
    expect((screen.getByTestId('confirm-ok') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByTestId('inbox-bulk-incident-text'), { target: { value: ' INC42 ' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(casework.bulkCases).toHaveBeenLastCalledWith(['C-1'], 'incident', { incident: 'INC42' }))
  })

  it('closes many with one outcome: fix with one text and RCA, duplicate of one case, exception with a last day', async () => {
    vi.mocked(casework.bulkCases).mockResolvedValue({
      results: [
        { id: 'C-1', ok: true },
        { id: 'C-3', ok: true },
      ],
    })
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('case:3'))
    pick('C-1')
    pick('C-3')
    fireEvent.click(screen.getByTestId('inbox-bulk-close'))
    // Fix: the text is required; the RCA's fields always show, one RCA for all (CF-42).
    expect(screen.queryByTestId('inbox-bulk-rca-add')).toBeNull()
    expect((screen.getByTestId('confirm-ok') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByTestId('inbox-bulk-text'), { target: { value: 'Raised the limits' } })
    expect((screen.getByTestId('confirm-ok') as HTMLButtonElement).disabled).toBe(false)
    fireEvent.change(screen.getByTestId('inbox-bulk-rca-cause'), { target: { value: 'No limits set' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(casework.bulkCases).toHaveBeenLastCalledWith(['C-1', 'C-3'], 'fix', { text: 'Raised the limits', rca: { cause: 'No limits set', prevention: '' }, saveFix: true }))
    fireEvent.click(await screen.findByTestId('confirm-ok'))
    // Duplicate: one original, never one of the picked.
    pick('C-1')
    pick('C-3')
    fireEvent.click(screen.getByTestId('inbox-bulk-close'))
    fireEvent.click(screen.getByTestId('inbox-bulk-close-duplicate'))
    fireEvent.change(screen.getByTestId('inbox-bulk-original'), { target: { value: 'C-3' } })
    fireEvent.change(screen.getByTestId('inbox-bulk-text'), { target: { value: 'Same as C-5' } })
    expect(screen.getByText('The original cannot be one of the picked cases.')).toBeTruthy()
    fireEvent.change(screen.getByTestId('inbox-bulk-original'), { target: { value: 'c-5' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(casework.bulkCases).toHaveBeenLastCalledWith(['C-1', 'C-3'], 'duplicate', { outcome: 'duplicate', explanation: 'Same as C-5', original: 'C-5' }))
    fireEvent.click(await screen.findByTestId('confirm-ok'))
    // Exception, with a reason and a last day; No action needed goes as Close.
    pick('C-1')
    fireEvent.click(screen.getByTestId('inbox-bulk-close'))
    fireEvent.click(screen.getByTestId('inbox-bulk-close-exception'))
    fireEvent.change(screen.getByTestId('inbox-bulk-reason'), { target: { value: 'Accepted on CI' } })
    fireEvent.change(screen.getByTestId('inbox-bulk-expires'), { target: { value: '2027-01-31' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(casework.bulkCases).toHaveBeenLastCalledWith(['C-1'], 'exception', { reason: 'Accepted on CI', expires: '2027-01-31' }))
    fireEvent.click(await screen.findByTestId('confirm-ok'))
    pick('C-1')
    fireEvent.click(screen.getByTestId('inbox-bulk-close'))
    // One No action needed for every case, since CF-90: no Not a problem, no No fix needed.
    const choices = [...screen.getByTestId('confirm').querySelectorAll('input[name="bulk-close"]')].map((r) => r.getAttribute('data-testid'))
    expect(choices).toEqual(['inbox-bulk-close-fix', 'inbox-bulk-close-noAction', 'inbox-bulk-close-knownIssue', 'inbox-bulk-close-duplicate', 'inbox-bulk-close-exception'])
    expect(screen.getByTestId('confirm').textContent).toContain('No action needed Not a problem, or it went away: say why.')
    fireEvent.click(screen.getByTestId('inbox-bulk-close-noAction'))
    fireEvent.change(screen.getByTestId('inbox-bulk-text'), { target: { value: 'Gone by itself' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(casework.bulkCases).toHaveBeenLastCalledWith(['C-1'], 'close', { outcome: 'noAction', explanation: 'Gone by itself' }))
  })

  it('shows the RCA’s fields under Fixed, with a hint by priority, and sends an RCA only with its cause (CF-42)', async () => {
    vi.mocked(casework.bulkCases).mockResolvedValue({
      results: [
        { id: 'C-1', ok: true },
        { id: 'C-3', ok: true },
      ],
    })
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('case:6'))
    // P2 and P3: optional, one RCA covering them all; empty, none goes.
    pick('C-1')
    pick('C-3')
    fireEvent.click(screen.getByTestId('inbox-bulk-close'))
    expect(screen.getByTestId('inbox-bulk-rca-hint').textContent).toBe('Optional. One RCA covers every case closed here, with what was done as its fix; it is kept on each case closed.')
    // What was done is the RCA's fix: no second field for it (wave 6e); notes are the RCA's own.
    for (const f of ['cause', 'prevention', 'text']) expect(screen.getByTestId(`inbox-bulk-rca-${f}`)).toBeTruthy()
    expect(screen.queryByTestId('inbox-bulk-rca-fix')).toBeNull()
    expect(screen.getByTestId('confirm').textContent).not.toContain('RCAs')
    expect(screen.queryByTestId('inbox-bulk-rca-refused')).toBeNull()
    fireEvent.change(screen.getByTestId('inbox-bulk-text'), { target: { value: 'Restarted them' } })
    fireEvent.change(screen.getByTestId('inbox-bulk-rca-prevention'), { target: { value: 'Alerts' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(casework.bulkCases).toHaveBeenLastCalledWith(['C-1', 'C-3'], 'fix', { text: 'Restarted them', saveFix: true }))
    fireEvent.click(await screen.findByTestId('confirm-ok'))
    // A P1 among them: required, and without a cause the hint says they are refused.
    vi.mocked(casework.bulkCases).mockResolvedValue({
      results: [
        { id: 'C-1', ok: true },
        { id: 'C-6', ok: false, error: 'An RCA is required at P1.' },
      ],
    })
    pick('C-1')
    pick('C-6')
    fireEvent.click(screen.getByTestId('inbox-bulk-close'))
    expect(screen.getByTestId('inbox-bulk-rca-hint').textContent).toMatch(/^Required for the P0 and P1 cases\. One RCA covers/)
    expect(screen.getByTestId('inbox-bulk-rca-refused').textContent).toContain('the P0 and P1 cases are refused')
    fireEvent.change(screen.getByTestId('inbox-bulk-text'), { target: { value: 'Removed the binding' } })
    fireEvent.change(screen.getByTestId('inbox-bulk-rca-cause'), { target: { value: 'A leftover from setup' } })
    fireEvent.change(screen.getByTestId('inbox-bulk-rca-text'), { target: { value: 'Found in the audit.' } })
    expect(screen.queryByTestId('inbox-bulk-rca-refused')).toBeNull()
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(casework.bulkCases).toHaveBeenLastCalledWith(['C-1', 'C-6'], 'fix', { text: 'Removed the binding', rca: { cause: 'A leftover from setup', prevention: '', text: 'Found in the audit.' }, saveFix: true }))
  })

  it('saves one fix for all the cases fixed together, unless Save fix is unticked (wave 6g)', async () => {
    vi.mocked(casework.bulkCases).mockResolvedValue({
      results: [
        { id: 'C-1', ok: true },
        { id: 'C-3', ok: true },
      ],
    })
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('case:3'))
    pick('C-1')
    pick('C-3')
    fireEvent.click(screen.getByTestId('inbox-bulk-close'))
    const box = screen.getByTestId('inbox-bulk-save-fix') as HTMLInputElement
    expect(box.checked).toBe(true)
    expect(screen.getByTestId('confirm').textContent).toContain('One fix for all of them in Mycroft › Fixes')
    fireEvent.click(box)
    fireEvent.change(screen.getByTestId('inbox-bulk-text'), { target: { value: 'Restarted them' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(casework.bulkCases).toHaveBeenLastCalledWith(['C-1', 'C-3'], 'fix', { text: 'Restarted them', saveFix: false }))
    fireEvent.click(await screen.findByTestId('confirm-ok'))
    // Another outcome sends no saveFix.
    pick('C-1')
    fireEvent.click(screen.getByTestId('inbox-bulk-close'))
    fireEvent.click(screen.getByTestId('inbox-bulk-close-noAction'))
    expect(screen.queryByTestId('inbox-bulk-save-fix')).toBeNull()
  })

  it('offers Duplicate for any picked case, agent-found too, and lets the server check each (CF-42)', async () => {
    vi.mocked(casework.bulkCases).mockResolvedValue({ results: [{ id: 'C-6', ok: true }] })
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('case:6'))
    pick('C-6')
    fireEvent.click(screen.getByTestId('inbox-bulk-close'))
    fireEvent.click(screen.getByTestId('inbox-bulk-close-duplicate'))
    fireEvent.change(screen.getByTestId('inbox-bulk-original'), { target: { value: 'C-1' } })
    fireEvent.change(screen.getByTestId('inbox-bulk-text'), { target: { value: 'Same binding' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(casework.bulkCases).toHaveBeenLastCalledWith(['C-6'], 'duplicate', { outcome: 'duplicate', explanation: 'Same binding', original: 'C-1' }))
  })

  it('makes one known issue from the first case and links it on the others', async () => {
    vi.mocked(casework.fetchOpenKnown).mockResolvedValue([])
    vi.mocked(casework.closeKnown).mockResolvedValue({ ...kase(1), knownIssue: 'KI-9' } as unknown as casework.Case)
    vi.mocked(casework.bulkCases).mockResolvedValue({ results: [{ id: 'C-3', ok: false, error: 'C-3 is closed.' }] })
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('case:3'))
    pick('C-1')
    pick('C-3')
    fireEvent.click(screen.getByTestId('inbox-bulk-close'))
    fireEvent.click(screen.getByTestId('inbox-bulk-close-knownIssue'))
    await screen.findByText('No known issue is open: make one.')
    fireEvent.click(screen.getByTestId('inbox-bulk-known-create'))
    fireEvent.change(screen.getByTestId('inbox-bulk-known-title'), { target: { value: 'Checkout hangs' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await screen.findByTestId('inbox-bulk-results')
    expect(casework.closeKnown).toHaveBeenCalledWith('C-1', { create: { title: 'Checkout hangs', symptoms: ['Checkout hangs'], workaround: '' } })
    expect(casework.bulkCases).toHaveBeenCalledWith(['C-3'], 'known', { id: 'KI-9' })
    expect(screen.getAllByTestId('inbox-bulk-result').map((r) => r.getAttribute('data-ok'))).toEqual(['true', 'false'])
  })

  it('refuses each case of a request the server refuses whole', async () => {
    vi.mocked(casework.bulkCases).mockRejectedValue(new CallError('At most 100 cases.', 400))
    page('#/aiops/inbox')
    await waitFor(() => expect(keys()).toContain('case:1'))
    pick('C-1')
    fireEvent.click(screen.getByTestId('inbox-bulk-analyze'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    const res = await screen.findAllByTestId('inbox-bulk-result')
    expect(res[0]!.textContent).toContain('At most 100 cases.')
    expect(casework.bulkCases).toHaveBeenCalledWith(['C-1'], 'analyze', {})
  })
})

describe('the Inbox is wider, with Columns and Export just above its table (CF-66)', () => {
  it('has its own width and padding, which Jobs shares', async () => {
    page('#/aiops/inbox')
    await waitFor(() => expect(keys().length).toBeGreaterThan(0))
    expect(screen.getByTestId('cases-inbox').classList.contains('cs-col-inbox')).toBe(true)
    expect(screen.getByTestId('cases-page').classList.contains('cs-page-inbox')).toBe(true)
    cleanup()
    // Jobs takes the same wide column, every sub-tab of it.
    page('#/aiops/jobs')
    expect(screen.getByTestId('cases-page').classList.contains('cs-page-inbox')).toBe(true)
    expect(screen.getByTestId('jobs-area').classList.contains('cs-col-inbox')).toBe(true)
  })

  it('keeps New case on the first row and puts Columns and Export in a row right above the table', async () => {
    page('#/aiops/inbox')
    await waitFor(() => expect(keys().length).toBeGreaterThan(0))
    const tools = screen.getByTestId('inbox-table-tools')
    expect(within(tools).getByRole('button', { name: 'Columns' })).toBeTruthy()
    expect(within(tools).getByRole('button', { name: 'Export' })).toBeTruthy()
    expect(within(tools).queryByTestId('cases-new-case')).toBeNull()
    // The first row holds Show and the search, and New case at its right.
    const first = screen.getByTestId('inbox-show').parentElement!
    expect(within(first).getByTestId('cases-new-case')).toBeTruthy()
    expect(within(first).queryByRole('button', { name: 'Columns' })).toBeNull()
    // The row is the table's own: nothing but the errors comes between.
    expect(tools.compareDocumentPosition(document.querySelector('[data-inbox-row]')!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(tools.previousElementSibling?.querySelector('[data-testid=inbox-source]')).toBeTruthy()
  })
})

describe('the Inbox links to the alerts going on (CF-81)', () => {
  const at = (alerts?: number) => {
    window.history.replaceState(null, '', '/#/aiops/inbox')
    return render(<AIOpsPage route={parseHash('#/aiops/inbox')} me={admin} onToast={() => undefined} alerts={alerts} clusterName="Production" />)
  }

  it('says how many alerts are going on in the cluster open and opens its Alerts tab', async () => {
    at(3)
    await waitFor(() => expect(keys().length).toBeGreaterThan(0))
    const link = screen.getByTestId('inbox-alerts')
    expect(link.tagName).toBe('A')
    expect(link.textContent).toBe('3 alerts now')
    expect(link.getAttribute('href')).toMatch(/#\/alerts$/)
    expect(link.getAttribute('title')).toBe('3 alerts going on in Production: open its Alerts tab')
    // On the toolbar's first row, beside the search.
    expect(within(screen.getByTestId('inbox-show').parentElement!).getByTestId('inbox-alerts')).toBe(link)
    cleanup()
    at(1)
    expect(screen.getByTestId('inbox-alerts').textContent).toBe('1 alert now')
  })

  it('is not there while no alert is going on', async () => {
    at(0)
    await waitFor(() => expect(keys().length).toBeGreaterThan(0))
    expect(screen.queryByTestId('inbox-alerts')).toBeNull()
    cleanup()
    at(undefined)
    expect(screen.queryByTestId('inbox-alerts')).toBeNull()
  })
})
