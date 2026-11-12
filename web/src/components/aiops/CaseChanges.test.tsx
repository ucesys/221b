import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CallError } from '../../api/call'
import * as cards from '../../api/cards'
import type { Proposal } from '../../api/cards'
import * as api from '../../api/casework'
import type { Case, CaseBrief } from '../../api/casework'
import { forgetSharedForTests } from '../../lib/cases'
import { CaseDrawer } from './CaseDrawer'
import { caseRow, INBOX_COLUMNS, proposalRow, type RowContext } from './inboxRows'
import { ProposalDrawer } from './ProposalDrawer'
import { resolvedHref } from './ProposalLinks'

vi.mock('../../api/casework', async (original) => ({
  ...(await original<typeof import('../../api/casework')>()),
  fetchCase: vi.fn(),
  linkChange: vi.fn(),
  unlinkChange: vi.fn(),
  fetchOpenKnown: vi.fn(),
}))
vi.mock('../../api/cards', async (original) => ({ ...(await original<typeof import('../../api/cards')>()), fetchProposals: vi.fn(), fetchProposal: vi.fn() }))

const txt = (el: Element) => el.textContent ?? ''
const NOW = Date.parse('2026-11-07T12:00:00Z')

/** An agent-found case on prod, In progress, at P3. */
const kase = (over: Partial<Case> = {}): Case => ({
  id: 'C-20',
  number: 20,
  title: 'analytics has no network policy',
  priority: 3,
  status: 'inProgress',
  pending: true,
  agentFound: true,
  cluster: 'prod',
  resource: 'Namespace/analytics',
  source: { kind: 'finding', run: 'run-7', job: 'Nightly audit' },
  key: { cluster: 'prod', object: 'Namespace/analytics', check: 'netpol.missing' },
  createdBy: 'auditor',
  created: '2026-11-06T02:00:00Z',
  updated: '2026-11-06T02:00:00Z',
  since: '2026-11-06T02:00:00Z',
  slaApplies: false,
  messages: [],
  proposals: [],
  history: [],
  mayChange: true,
  related: [],
  allowedOutcomes: ['fix', 'noAction', 'knownIssue', 'exception'],
  rcaRequired: false,
  mayReopen: true,
  ...over,
})

const card = (over: Partial<Proposal> = {}): Proposal =>
  ({
    id: 'p-1',
    agent: 'auditor',
    agentName: 'Auditor',
    source: { kind: 'job', id: 'run-7', label: 'Nightly audit' },
    cluster: 'prod',
    clusterName: 'Production',
    title: 'Add a default-deny policy to analytics',
    actions: [{ kind: 'kubectl', args: { command: 'kubectl apply -f deny.yaml -n analytics' } }],
    condition: { kind: 'none' },
    expires: '2026-11-08T02:00:00Z',
    state: 'queued',
    created: '2026-11-07T02:00:00Z',
    updated: '2026-11-07T02:00:00Z',
    needed: 1,
    plans: [],
    canApprove: true,
    ...over,
  }) as Proposal

beforeEach(() => {
  Element.prototype.scrollTo = () => {}
  // The object links look their objects up; here nothing answers.
  vi.stubGlobal('fetch', async () => new Response('{}', { status: 404 }))
  vi.mocked(api.fetchOpenKnown).mockResolvedValue([])
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
  forgetSharedForTests()
  window.history.replaceState(null, '', '/')
})

describe('a proposed change names what it resolves (CF-25)', () => {
  it('links a case to its drawer and a known issue to Mycroft', () => {
    expect(resolvedHref('C-12')).toBe('#/aiops/inbox/case/12')
    expect(resolvedHref('ki-3')).toBe('#/aiops/mycroft/known/KI-3')
    expect(resolvedHref('run-7')).toBeNull()
  })

  it('shows "Resolves C-12, KI-3" in the proposal drawer, each linked, and says when a chat’s change names none', async () => {
    const p = card({ resolves: ['C-12', 'KI-3'], resolvesBy: { 'C-12': 'agent', 'KI-3': 'Ann' } })
    vi.mocked(cards.fetchProposal).mockResolvedValue(p)
    render(<ProposalDrawer id="p-1" initial={p} onClose={() => undefined} />)
    const row = await screen.findByTestId('proposal-resolves')
    const links = within(row).getAllByTestId('resolves-link')
    expect(links.map(txt)).toEqual(['C-12', 'KI-3'])
    expect(links[0]!.getAttribute('href')).toBe('#/aiops/inbox/case/12')
    expect(links[1]!.getAttribute('href')).toBe('#/aiops/mycroft/known/KI-3')
    expect(links[1]!.getAttribute('title')).toBe('Linked by Ann')
    cleanup()

    const chat = card({ id: 'p-2', source: { kind: 'chat', label: 'Asked by Ann in a chat', setBy: 'Ann' }, resolves: undefined })
    vi.mocked(cards.fetchProposal).mockResolvedValue(chat)
    render(<ProposalDrawer id="p-2" initial={chat} onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('proposal-resolves'))).toBe('No case named.')
  })

  it('shows what a change resolves on its Inbox row, linked, found by search and in the export', () => {
    const row = proposalRow(card({ resolves: ['C-12', 'KI-3'] }))
    expect(row.resolves).toEqual(['C-12', 'KI-3'])
    expect(row.words).toContain('KI-3')
    const title = INBOX_COLUMNS.find((c) => c.id === 'title')!
    render(<>{title.cell(row, NOW)}</>)
    const cell = screen.getByTestId('inbox-row-resolves')
    expect(txt(cell)).toBe('Resolves C-12, KI-3')
    expect(
      within(cell)
        .getAllByRole('link')
        .map((a) => a.getAttribute('href')),
    ).toEqual(['#/aiops/inbox/case/12', '#/aiops/mycroft/known/KI-3'])
    expect(title.export(row, NOW)).toContainEqual({ head: 'Title', cell: 'Add a default-deny policy to analytics (resolves C-12, KI-3)' })
    // A change that names nothing shows nothing more.
    expect(proposalRow(card()).resolves).toBeUndefined()
  })
})

describe('a case lists the changes that resolve it, and a person links one (CF-28)', () => {
  it('lists every change from the link, with its state, who linked it and when it was applied; Unlink only on a person’s link', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(
      kase({
        proposals: [
          { id: 'p-1', title: 'Add a default-deny policy to analytics', state: 'applied', linkedBy: 'agent', appliedAt: '2026-11-07T03:00:00Z' },
          { id: 'p-9', title: 'kubectl label namespace analytics zone=dmz', state: 'queued', linkedBy: 'Ann' },
          { id: 'p-4', state: 'gone', linkedBy: 'agent' },
        ],
      }),
    )
    vi.mocked(api.unlinkChange).mockResolvedValue(kase({ proposals: [{ id: 'p-1', title: 'Add a default-deny policy to analytics', state: 'applied', linkedBy: 'agent' }] }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    await screen.findByTestId('case-cards')
    const rows = screen.getAllByTestId('case-card')
    expect(rows.map((r) => r.getAttribute('data-id'))).toEqual(['p-1', 'p-9', 'p-4'])
    expect(txt(rows[0]!)).toContain('Applied')
    expect(txt(rows[0]!)).toContain('named by the agent · applied')
    expect(within(rows[0]!).getByRole('link').getAttribute('href')).toBe('#/aiops/inbox/proposal/p-1')
    expect(txt(rows[1]!)).toContain('Waiting for approval')
    expect(txt(rows[1]!)).toContain('p-9 · linked by Ann')
    expect(txt(rows[2]!)).toContain('No longer kept')
    // An agent's own link stays; a person's may go.
    expect(within(rows[0]!).queryByTestId('case-card-unlink')).toBeNull()
    expect(within(rows[2]!).queryByTestId('case-card-unlink')).toBeNull()
    fireEvent.click(within(rows[1]!).getByTestId('case-card-unlink'))
    await waitFor(() => expect(api.unlinkChange).toHaveBeenCalledWith('C-20', 'p-9'))
    await waitFor(() => expect(screen.getAllByTestId('case-card')).toHaveLength(1))
  })

  it('offers no Link a change or Unlink to who does not work the cases, nor on a closed case', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ mayChange: false, proposals: [{ id: 'p-9', title: 'x', state: 'queued', linkedBy: 'Ann' }] }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    await screen.findByTestId('case-cards')
    expect(screen.queryByTestId('case-link-change')).toBeNull()
    expect(screen.queryByTestId('case-card-unlink')).toBeNull()
    cleanup()
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ status: 'closed', outcome: 'noAction', pending: false }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    await screen.findByTestId('case-cards')
    expect(screen.queryByTestId('case-link-change')).toBeNull()
  })

  it('picks the cards a case may link: its cluster’s, waiting, approved or applied in the last 30 days, not linked yet, by title or id', () => {
    const list = [
      card({ id: 'p-1', updated: '2026-11-07T02:00:00Z' }),
      card({ id: 'p-2', state: 'approved', title: 'Restart web', updated: '2026-11-07T04:00:00Z' }),
      card({ id: 'p-3', state: 'applied', updated: '2026-10-20T00:00:00Z' }),
      card({ id: 'p-4', state: 'applied', updated: '2026-09-30T00:00:00Z' }),
      card({ id: 'p-5', state: 'rejected' }),
      card({ id: 'p-6', state: 'expired' }),
      card({ id: 'p-7', cluster: 'dev' }),
      card({ id: 'p-8', state: 'failed' }),
    ]
    const ids = (o: Parameters<typeof cards.linkable>[1]) => cards.linkable(list, { now: NOW, ...o }).map((p) => p.id)
    expect(ids({ cluster: 'prod' })).toEqual(['p-2', 'p-1', 'p-3'])
    expect(ids({ cluster: 'prod', linked: ['p-1'] })).toEqual(['p-2', 'p-3'])
    expect(ids({ cluster: 'prod', q: 'restart' })).toEqual(['p-2'])
    expect(ids({ cluster: 'prod', q: 'P-3' })).toEqual(['p-3'])
    expect(ids({})).toContain('p-7')
  })

  it('links a change from a list of the case’s cluster’s cards: found by title, picked, then listed on the case', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ proposals: [{ id: 'p-1', title: 'Add a default-deny policy to analytics', state: 'queued', linkedBy: 'agent' }] }))
    vi.mocked(cards.fetchProposals).mockResolvedValue({
      proposals: [
        card({ id: 'p-1' }),
        card({ id: 'p-9', title: 'kubectl label namespace analytics zone=dmz', agentName: 'Sherlock', source: { kind: 'chat', label: 'Asked by Ann in a chat', setBy: 'Ann' } }),
        card({ id: 'p-5', title: 'Restart web', state: 'rejected' }),
      ],
      waiting: 2,
      mayApprove: true,
    })
    const after = kase({
      proposals: [
        { id: 'p-1', title: 'Add a default-deny policy to analytics', state: 'queued', linkedBy: 'agent' },
        { id: 'p-9', title: 'kubectl label namespace analytics zone=dmz', state: 'queued', linkedBy: 'Ann' },
      ],
    })
    vi.mocked(api.linkChange).mockResolvedValue(after)
    const toast = vi.fn()
    const changed = vi.fn()
    render(<CaseDrawer id="20" onClose={() => undefined} onToast={toast} onChanged={changed} />)
    fireEvent.click(await screen.findByTestId('case-link-change'))
    expect(cards.fetchProposals).toHaveBeenCalledWith({ cluster: 'prod' })
    // Only what may be linked: not the one linked already, not a rejected one.
    const options = await screen.findAllByTestId('case-link-option')
    expect(options.map((o) => o.getAttribute('data-id'))).toEqual(['p-9'])
    expect((screen.getByTestId('confirm-ok') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByTestId('case-link-search'), { target: { value: 'nothing like it' } })
    expect(txt(screen.getByTestId('case-link-none'))).toBe('No change matches.')
    fireEvent.change(screen.getByTestId('case-link-search'), { target: { value: 'zone=dmz' } })
    fireEvent.click(within(screen.getByTestId('case-link-option')).getByRole('radio'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(api.linkChange).toHaveBeenCalledWith('C-20', 'p-9'))
    await waitFor(() => expect(screen.queryByTestId('confirm')).toBeNull())
    expect(screen.getAllByTestId('case-card').map((r) => r.getAttribute('data-id'))).toEqual(['p-1', 'p-9'])
    expect(toast).toHaveBeenCalledWith('Linked: the change resolves C-20.')
    expect(changed).toHaveBeenCalled()
  })

  it('shows the server’s refusal in the dialog: another cluster, linked already, or the case closed (409)', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    vi.mocked(cards.fetchProposals).mockResolvedValue({ proposals: [card({ id: 'p-9' })], waiting: 1, mayApprove: true })
    vi.mocked(api.linkChange).mockRejectedValue(new CallError('p-9 already resolves C-20.', 409, { error: 'p-9 already resolves C-20.' }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    fireEvent.click(await screen.findByTestId('case-link-change'))
    fireEvent.click(within(await screen.findByTestId('case-link-option')).getByRole('radio'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    const dialog = screen.getByTestId('confirm')
    await waitFor(() => expect(within(dialog).getByRole('alert').textContent).toBe('p-9 already resolves C-20.'))
    // The dialog stays open for another pick, or Cancel.
    expect(screen.getByTestId('case-link-list')).toBeTruthy()
  })

  it('says why the cards cannot be listed, in the dialog', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    vi.mocked(cards.fetchProposals).mockRejectedValue(new CallError('Only the people who may approve proposed changes see them (cases.approverRoles).', 403))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    fireEvent.click(await screen.findByTestId('case-link-change'))
    expect(txt(await screen.findByTestId('case-link-load-error'))).toContain('Only the people who may approve proposed changes see them')
  })
})

describe('a fix a change applied, and its check (CF-29 to CF-31)', () => {
  const applied = { text: 'Fix applied by change p-1: Add a default-deny policy to analytics', by: 'Ann', at: '2026-11-07T03:00:00Z', change: 'p-1' }

  it('waits for the next run of the job that found it, linked', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ fixApplied: applied, verification: 'waiting', verifyJob: { id: 'nightly-audit', label: 'Nightly audit' } }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    const banner = await screen.findByTestId('case-verification')
    expect(banner.getAttribute('data-state')).toBe('waiting')
    expect(txt(banner)).toContain('Fix applied by change p-1 — waiting for a validation run: the next run of Nightly audit re-checks it.')
    expect(txt(banner)).toContain('the case closes as fixed by itself')
    expect(within(banner).getByTestId('case-verification-change').getAttribute('href')).toBe('#/aiops/inbox/proposal/p-1')
    expect(within(banner).getByTestId('case-verify-job').getAttribute('href')).toBe('#/aiops/jobs/nightly-audit')
    expect(txt(screen.getByTestId('case-fix-applied'))).toContain('Fix applied by change p-1')
  })

  it('at P0 and P1, verified: add the RCA to close, with Fix… at hand', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ priority: 1, rcaRequired: true, slaApplies: true, fixApplied: applied, verification: 'verified', verifyJob: { id: 'nightly-audit', label: 'Nightly audit' } }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    const banner = await screen.findByTestId('case-verification')
    expect(banner.getAttribute('data-state')).toBe('verified')
    expect(txt(banner)).toContain('Verified — add the RCA to close.')
    fireEvent.click(within(banner).getByTestId('case-verification-fix'))
    expect(await screen.findByTestId('case-fix-form')).toBeTruthy()
    expect(screen.getByTestId('case-rca-required')).toBeTruthy()
  })

  it('says when the fix did not hold', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ fixApplied: applied, verification: 'notHeld', verifyJob: { id: 'nightly-audit', label: 'Nightly audit' } }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    const banner = await screen.findByTestId('case-verification')
    expect(banner.getAttribute('data-state')).toBe('notHeld')
    expect(txt(banner)).toContain('The fix did not hold.')
    expect(txt(banner)).toContain('found the problem again after change p-1')
  })

  it('says the fix did not hold on a case a run reopened, closed as fixed by a person (CF-107)', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(
      kase({ status: 'new', stage: 'notHeld', fixApplied: { text: 'Pinned the tag.', by: 'Olivia', at: '2026-11-07T03:00:00Z' }, verification: 'notHeld', verifyJob: { id: 'nightly-audit', label: 'Nightly audit' } }),
    )
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    const banner = await screen.findByTestId('case-verification')
    expect(txt(banner)).toContain('found the problem again after it was closed as fixed.')
    expect(txt(banner)).toContain('The case stays open')
    expect(txt(screen.getByTestId('case-stage'))).toBe('Fix did not hold')
    expect(screen.getByTestId('case-stage').getAttribute('data-stage')).toBe('notHeld')
  })

  it('says how long the fix has waited and how many runs did not re-check it (I-1)', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ fixApplied: applied, verification: 'waiting', verifyJob: { id: 'nightly-audit', label: 'Nightly audit' }, verifyWait: { since: '2026-11-07T03:00:00Z', unchecked: 2 } }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    const wait = await screen.findByTestId('case-verify-wait')
    expect(txt(wait)).toMatch(/^Waiting since 2026-11-0\d .+; 2 runs of the job did not re-check it\.$/)
    expect(txt(screen.getByTestId('case-verification'))).toContain('If that run re-checks it and finds the problem gone, the case closes as fixed by itself.')
  })

  it('names the run a verification rested on and what it read (I-1)', async () => {
    const verifiedBy = { run: 'run-9', job: 'Nightly audit', at: '2026-11-07T04:00:00Z', evidence: 'read the policy: it denies by default.' }
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ priority: 1, rcaRequired: true, fixApplied: applied, verification: 'verified', verifyJob: { id: 'nightly-audit', label: 'Nightly audit' }, verifiedBy }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-verified-by'))).toMatch(/^Verified by the Nightly audit, run-9, on 2026-11-0\d .+: read the policy: it denies by default\.$/)
    cleanup()
    // Closed as fixed: the closing section says it too; one still waiting says since when.
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ status: 'closed', outcome: 'fix', closedAt: '2026-11-07T04:00:00Z', verified: '2026-11-07T04:00:00Z', verifiedBy }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-verified'))).toContain('Verified by the Nightly audit, run-9, on ')
    cleanup()
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ status: 'closed', outcome: 'fix', closedAt: '2026-11-07T04:00:00Z', verifyWait: { since: '2026-11-07T04:00:00Z', unchecked: 1 } }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-verified'))).toMatch(/^The next run of the job re-checks that the fix holds\. Waiting since .+; 1 run of the job did not re-check it\.$/)
  })

  it('says a verification and a wait in plain words', () => {
    const at = (s: string) => s.slice(0, 10)
    expect(api.verifiedText({ run: 'run-3', at: '2026-11-07T04:00:00Z' }, at)).toBe('Verified by run-3 on 2026-11-07.')
    expect(api.verifiedText({ run: 'run-3', job: 'Alert diagnosis', at: '2026-11-07T04:00:00Z', evidence: 'web-1 runs, 0 restarts...' }, at)).toBe('Verified by the Alert diagnosis, run-3, on 2026-11-07: web-1 runs, 0 restarts.')
    expect(api.waitText({ since: '2026-11-06T00:00:00Z', unchecked: 0 }, at)).toBe('Waiting since 2026-11-06.')
    expect(api.waitText({ since: '2026-11-06T00:00:00Z', unchecked: 2, why: 'run-12 found it again and opened no case: the agent marked it expected' }, at)).toBe(
      'Waiting since 2026-11-06; 2 runs of the job did not re-check it (run-12 found it again and opened no case: the agent marked it expected).',
    )
  })

  it('says on a waiting case why the last run did not re-check it (R2-10)', async () => {
    const why = 'run-12 found it again and opened no case: below the job’s lowest priority'
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ status: 'closed', outcome: 'fix', closedAt: '2026-11-07T04:00:00Z', verifyWait: { since: '2026-11-07T04:00:00Z', unchecked: 1, why } }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-verified'))).toContain(`1 run of the job did not re-check it (${why}).`)
  })

  it('shows no check for a case a person opened: the fix is recorded, a person closes it', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ agentFound: false, key: undefined, source: { kind: 'manual' }, fixApplied: applied }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-fix-applied'))).toContain('close the case once it is fixed')
    expect(screen.queryByTestId('case-verification')).toBeNull()
  })

  it('badges a case row in the Inbox with its check, found by search and in the export', () => {
    const ctx: RowContext = { me: 'Ann (ann)', person: 'ann', names: new Map() }
    const brief: CaseBrief = { ...kase(), verification: 'waiting', stage: 'validation' }
    const row = caseRow(brief, ctx)
    expect(row.stage).toBe('validation')
    expect(row.words).toContain('waiting for a validation run')
    const status = INBOX_COLUMNS.find((c) => c.id === 'status')!
    render(<>{status.cell(row, NOW)}</>)
    expect(txt(screen.getByTestId('inbox-row-stage'))).toBe('Waiting for a validation run')
    expect(status.export(row, NOW)).toEqual([{ head: 'Status', cell: 'In progress (waiting for a validation run)' }])
    cleanup()
    render(<>{status.cell(caseRow({ ...brief, verification: 'notHeld', stage: 'notHeld' }, ctx), NOW)}</>)
    expect(screen.getByTestId('inbox-row-stage').getAttribute('data-tone')).toBe('err')
    expect(txt(screen.getByTestId('inbox-row-stage'))).toBe('Fix did not hold')
    cleanup()
    render(<>{status.cell(caseRow({ ...brief, verification: undefined, stage: undefined }, ctx), NOW)}</>)
    expect(screen.queryByTestId('inbox-row-stage')).toBeNull()
  })
})

// --- Owner's feedback on Cases (Phase 12, wave 6c) ---

describe('who proposed and decided a change, and the titles of what it resolves (CF-35, CF-39)', () => {
  it('shows "Resolves C-12 · title" in the drawer, the Inbox row and the export', async () => {
    const p = card({ resolves: ['C-12', 'KI-3'], resolvesTitles: { 'C-12': 'web has no limits', 'KI-3': 'Certificates renewed by hand' } })
    vi.mocked(cards.fetchProposal).mockResolvedValue(p)
    render(<ProposalDrawer id="p-1" onClose={() => undefined} />)
    const r = await screen.findByTestId('proposal-resolves')
    await waitFor(() => expect(txt(r)).toBe('C-12 · web has no limits, KI-3 · Certificates renewed by hand'))
    cleanup()
    const row = proposalRow(p)
    render(<div>{INBOX_COLUMNS.find((c) => c.id === 'title')!.cell(row, NOW)}</div>)
    expect(txt(screen.getByTestId('inbox-row-resolves'))).toBe('Resolves C-12 · web has no limits, KI-3 · Certificates renewed by hand')
    expect(INBOX_COLUMNS.find((c) => c.id === 'title')!.export(row, NOW)[1]!.cell).toBe('Add a default-deny policy to analytics (resolves C-12 · web has no limits, KI-3 · Certificates renewed by hand)')
  })

  it('says who proposed it and who decided it, and who did each thing in its history', async () => {
    vi.mocked(cards.fetchProposal).mockResolvedValue(
      card({
        state: 'rejected',
        proposedBy: { name: 'Auditor', kind: 'agent' },
        decidedBy: { name: 'system', kind: 'system' },
        rejection: { by: { key: 'system', name: 'System' }, at: '2026-11-07T03:00:00Z', comment: 'Case C-20 was closed as no action needed' },
        history: [
          { at: '2026-11-07T02:00:00Z', what: 'Proposed', by: 'auditor', actor: { name: 'Auditor', kind: 'agent' } },
          { at: '2026-11-07T03:00:00Z', what: 'Rejected: Case C-20 was closed as no action needed', by: 'system', actor: { name: 'system', kind: 'system' } },
          { at: '2026-11-07T03:01:00Z', what: 'A note of before', by: 'Pat' },
        ],
      }),
    )
    render(<ProposalDrawer id="p-1" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('proposal-proposed-by'))).toMatch(/^Auditor \(agent\), /)
    expect(txt(screen.getByTestId('proposal-decided-by'))).toMatch(/^System, /)
    expect(screen.getAllByTestId('proposal-history-by').map(txt)).toEqual([' · Auditor (agent)', ' · System', ' · Pat'])
  })

  it('says who decided a card from its approvals when the server does not say', async () => {
    vi.mocked(cards.fetchProposal).mockResolvedValue(card({ state: 'applied', approvals: [{ by: { key: 'ann', name: 'Ann' }, at: '2026-11-07T04:00:00Z' }] }))
    render(<ProposalDrawer id="p-1" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('proposal-decided-by'))).toMatch(/^Ann, /)
    expect(screen.queryByTestId('proposal-decided-by')).toBeTruthy()
    cleanup()
    vi.mocked(cards.fetchProposal).mockResolvedValue(card())
    render(<ProposalDrawer id="p-1" onClose={() => undefined} />)
    await screen.findByTestId('proposal-proposed-by')
    expect(screen.queryByTestId('proposal-decided-by')).toBeNull()
  })

  it('lists on the case who proposed and who decided each change', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ proposals: [{ id: 'p-1', title: 'Add a policy', state: 'applied', linkedBy: 'agent', proposedBy: { name: 'Auditor', kind: 'agent' }, decidedBy: { name: 'Ann', kind: 'person' } }] }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-card-who'))).toBe('proposed by Auditor (agent) · decided by Ann')
  })
})
