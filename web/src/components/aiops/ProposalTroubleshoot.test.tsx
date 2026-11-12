// A proposed change's Sherlock and Watson in its drawer (Phase 12, CF-61, CF-62).
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as cards from '../../api/cards'
import type { Proposal } from '../../api/cards'
import * as proposalSherlock from '../../api/proposalSherlock'
import type { SherlockState } from '../../api/sherlock'
import * as watson from '../../api/watson'
import type { WatsonState } from '../../api/watson'
import { forgetSharedForTests } from '../../lib/cases'
import { ProposalDrawer } from './ProposalDrawer'

vi.mock('../../api/cards', async (original) => ({ ...(await original<typeof import('../../api/cards')>()), fetchProposal: vi.fn(), approveProposal: vi.fn(), rejectProposal: vi.fn() }))
vi.mock('../../api/proposalSherlock', async (original) => ({ ...(await original<typeof import('../../api/proposalSherlock')>()), useProposalSherlock: vi.fn(), askProposalSherlock: vi.fn(), stopProposalSherlock: vi.fn() }))
vi.mock('../../api/watson', async (original) => ({ ...(await original<typeof import('../../api/watson')>()), useWatson: vi.fn(), sendToWatson: vi.fn() }))

const txt = (el: Element) => el.textContent ?? ''

const card = (over: Partial<Proposal> = {}): Proposal =>
  ({
    id: 'PC-7',
    agent: 'troubleshooter',
    agentName: 'Troubleshooter',
    source: { kind: 'case', id: 'C-12', label: 'Case C-12' },
    cluster: 'prod',
    clusterName: 'Production',
    title: 'Raise web’s memory limit',
    reasoning: 'web runs out of memory.',
    actions: [{ kind: 'kubectl', args: { command: 'kubectl set resources deployment/web -n shop --limits=memory=512Mi' }, risk: 'low' }],
    condition: { kind: 'none' },
    expires: '2099-11-09T02:00:00Z',
    state: 'queued',
    created: '2026-11-08T02:00:00Z',
    updated: '2026-11-08T02:00:00Z',
    needed: 1,
    plans: [{ summary: 'kubectl set resources deployment/web -n shop --limits=memory=512Mi', targets: ['deployment shop/web'], impact: ['3 pods replaced'] }],
    resolves: ['C-12', 'C-14'],
    resolvesTitles: { 'C-12': 'The checkout is slow', 'C-14': 'web restarts' },
    canApprove: true,
    mayAsk: true,
    ...over,
  }) as Proposal

const status = { available: true, enabled: true, key: true, model: 'claude-opus-5-5', admin: false }
const chat: SherlockState = {
  status,
  live: true,
  conv: {
    id: 'c1',
    busy: false,
    rev: 2,
    messages: [
      { id: 1, role: 'user', text: 'Is 512Mi enough?', asker: 'Pat', at: '2026-11-08T10:05:00Z' },
      { id: 2, role: 'sherlock', text: 'Check the peak first.\n\n```sh\nkubectl -n shop top pods\n```', status: 'done', at: '2026-11-08T10:05:00Z' },
    ],
  },
}

const watsonState: WatsonState = {
  live: true,
  status: { available: true, readOnly: false, twoPerson: true, env: 'production', signedIn: true, me: 'Pat', prompt: 'pat@prod:~$', lapseSeconds: 600, reads: ['get'], changes: ['set resources'], admin: false },
  items: [],
}

let width = 1024
function setWidth(w: number) {
  width = w
  Object.defineProperty(window, 'innerWidth', { configurable: true, get: () => width })
}

beforeEach(() => {
  Element.prototype.scrollTo = () => {}
  // The object links look their objects up; here nothing answers.
  vi.stubGlobal('fetch', async () => new Response('{}', { status: 404 }))
  setWidth(1400)
  vi.mocked(proposalSherlock.useProposalSherlock).mockReturnValue(chat)
  vi.mocked(proposalSherlock.askProposalSherlock).mockResolvedValue(chat.conv!)
  vi.mocked(watson.useWatson).mockReturnValue(watsonState)
  vi.mocked(watson.sendToWatson).mockResolvedValue([])
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
  forgetSharedForTests()
  window.history.replaceState(null, '', '/')
})

async function open(p: Proposal) {
  vi.mocked(cards.fetchProposal).mockResolvedValue(p)
  render(<ProposalDrawer id={p.id} initial={p} onClose={() => undefined} />)
  await waitFor(() => expect(cards.fetchProposal).toHaveBeenCalled())
}

describe('the proposed change drawer, near full screen, with Sherlock and Watson (CF-61)', () => {
  it('splits the proposal and Troubleshoot side by side when wide, linking the cases it resolves', async () => {
    await open(card())
    expect(screen.getByTestId('proposal-drawer').getAttribute('data-size')).toBe('full')
    expect(screen.getByTestId('drawer-split').getAttribute('data-layout')).toBe('split')
    expect(screen.getByTestId('proposal-trouble').getAttribute('data-pane')).toBe('sherlock')
    // The conversation is the proposal's own, each question naming who asked.
    expect(txt(screen.getByTestId('sherlock-question'))).toContain('Pat')
    expect(screen.getAllByTestId('resolves-link').map(txt)).toEqual(['C-12', 'C-14'])
    expect(screen.getAllByTestId('proposal-trouble-switch').length).toBeGreaterThan(0)
  })

  it('shows Proposal | Troubleshoot as tabs on a phone', async () => {
    setWidth(600)
    await open(card())
    expect(screen.getByTestId('drawer-split').getAttribute('data-layout')).toBe('tabs')
    expect(txt(screen.getByTestId('drawer-split-tab-main'))).toBe('Proposed change')
    expect(txt(screen.getByTestId('drawer-split-tab-side'))).toBe('Troubleshoot')
    // Run in Watson at the foot shows the Troubleshoot tab, on Watson.
    fireEvent.click(screen.getByTestId('proposal-run-watson'))
    expect(screen.getByTestId('drawer-split-side').hidden).toBe(false)
    expect(screen.getByTestId('proposal-trouble').getAttribute('data-pane')).toBe('watson')
    expect(screen.getByTestId('watson-proposal-card')).toBeTruthy()
  })

  it('sends Sherlock’s commands to the drawer’s Watson with Run in Watson, from the proposal', async () => {
    await open(card())
    const run = screen.getByTestId('sherlock-run-watson')
    expect(txt(run)).toBe('Run in Watson')
    fireEvent.click(run)
    await waitFor(() => expect(watson.sendToWatson).toHaveBeenCalledWith('prod', 'kubectl -n shop top pods', { from: 'proposal:PC-7', run: true, why: 'Sherlock proposed this answering “Is 512Mi enough?”.' }))
    expect(screen.getByTestId('proposal-trouble').getAttribute('data-pane')).toBe('watson')
    expect(screen.queryByTestId('sherlock-send-watson')).toBeNull()
  })

  it('asks on the proposal’s conversation, and lets who may not ask only read it', async () => {
    await open(card({ mayAsk: false }))
    expect(screen.queryByTestId('proposal-trouble-switch')).toBeNull()
    expect(screen.getByTestId('proposal-trouble').textContent).toContain('You read this conversation')
  })

  it('lists the commands run from the proposal, read or change, with how each ended', async () => {
    await open(
      card({
        commands: [
          { id: 'w1', line: 'kubectl -n shop top pods', by: 'Pat', at: '2026-11-08T10:06:00Z', change: false, status: 'done', exit: 0 },
          { id: 'w2', line: 'kubectl -n shop rollout restart deployment/web', by: 'Pat', at: '2026-11-08T10:07:00Z', change: true, status: 'pending' },
        ],
      }),
    )
    const rows = screen.getAllByTestId('proposal-command')
    expect(rows.map((r) => r.getAttribute('data-kind'))).toEqual(['change', 'read'])
    expect(txt(rows[0]!)).toContain('waiting for approval')
    expect(txt(rows[1]!)).toContain('$ kubectl -n shop top pods')
  })
})

describe('Run in Watson replaces Approve: one approval, in Watson (CF-62)', () => {
  it('has no Approve at the foot: Run in Watson brings the proposal’s own card to Watson, whose approval is the proposal’s', async () => {
    const p = card()
    vi.mocked(cards.approveProposal).mockResolvedValue({ ...p, state: 'approved', approvals: [{ by: { key: 'pat', name: 'Pat' }, at: '2026-11-08T10:10:00Z' }] })
    await open(p)
    const foot = screen.getByTestId('proposal-run-watson')
    expect(txt(foot)).toBe('Run in Watson')
    expect(foot.getAttribute('data-on')).toBe('true')
    expect(within(screen.getByTestId('proposal-drawer')).queryByRole('button', { name: 'Approve' })).toBeNull()
    expect(screen.getByTestId('proposal-recheck')).toBeTruthy()
    expect(screen.getByTestId('proposal-reject')).toBeTruthy()
    expect(screen.queryByTestId('watson-proposal-card')).toBeNull()
    fireEvent.click(foot)
    expect(screen.getByTestId('proposal-trouble').getAttribute('data-pane')).toBe('watson')
    const inWatson = within(screen.getByTestId('watson')).getByTestId('watson-proposal-card')
    expect(txt(within(inWatson).getByTestId('watson-card-command'))).toBe('kubectl set resources deployment/web -n shop --limits=memory=512Mi')
    expect(txt(inWatson)).toContain('Acts on deployment shop/web')
    // Nothing new was sent to Watson: the card is the proposal's.
    expect(watson.sendToWatson).not.toHaveBeenCalled()
    const approve = within(inWatson).getByTestId('proposal-approve')
    expect(txt(approve)).toBe('Approve and run')
    await act(async () => {
      fireEvent.click(approve)
    })
    expect(cards.approveProposal).toHaveBeenCalledTimes(1)
    expect(cards.approveProposal).toHaveBeenCalledWith('PC-7')
    expect(watson.sendToWatson).not.toHaveBeenCalled()
    await waitFor(() => expect(screen.getByTestId('proposal-state').textContent).toBe('Approved, running'))
    expect(screen.getByTestId('watson-proposal-card').getAttribute('data-state')).toBe('approved')
  })

  it('says "Needs a second approval" only when two are needed and one was given', async () => {
    await open(card({ needed: 2, production: true, approvals: [{ by: { key: 'sam', name: 'Sam' }, at: '2026-11-08T10:00:00Z' }] }))
    expect(txt(screen.getByTestId('proposal-count'))).toBe('Needs a second approval')
    fireEvent.click(screen.getByTestId('proposal-run-watson'))
    expect(txt(screen.getByTestId('proposal-card-second'))).toContain('Sam approved it')
    cleanup()
    await open(card({ needed: 2, production: true }))
    expect(screen.queryByTestId('proposal-count')).toBeNull()
    fireEvent.click(screen.getByTestId('proposal-run-watson'))
    expect(txt(screen.getByTestId('proposal-approve'))).toBe('Approve')
    cleanup()
    await open(card())
    expect(screen.queryByTestId('proposal-count')).toBeNull()
    expect(screen.queryByTestId('proposal-card-second')).toBeNull()
  })

  it('keeps Run in Watson off, with why, for who may not approve it now', async () => {
    await open(card({ canApprove: false, cannot: 'You approved it already; it waits for someone else.' }))
    const foot = screen.getByTestId('proposal-run-watson') as HTMLButtonElement
    expect(foot.disabled).toBe(true)
    expect(foot.title).toBe('You approved it already; it waits for someone else.')
  })

  it('still shows the card where Watson’s terminal is not (Sherlock off on the cluster)', async () => {
    vi.mocked(proposalSherlock.useProposalSherlock).mockReturnValue({ ...chat, status: { ...status, enabled: false } })
    await open(card())
    expect(screen.queryByTestId('proposal-trouble-switch')).toBeNull()
    fireEvent.click(screen.getByTestId('proposal-run-watson'))
    expect(screen.getByTestId('proposal-trouble').getAttribute('data-pane')).toBe('watson')
    expect(screen.getByTestId('proposal-trouble-alone')).toBeTruthy()
    expect(screen.getByTestId('watson-proposal-card')).toBeTruthy()
  })
})

describe('Reject…, which was Refuse…', () => {
  it('rejects with a reason and says "Rejected."', async () => {
    const p = card()
    vi.mocked(cards.rejectProposal).mockResolvedValue({ ...p, state: 'rejected', rejection: { by: { key: 'pat', name: 'Pat' }, at: '2026-11-08T10:10:00Z', reason: 'not now' } } as Proposal)
    const toast = vi.fn()
    vi.mocked(cards.fetchProposal).mockResolvedValue(p)
    render(<ProposalDrawer id={p.id} initial={p} onClose={() => undefined} onToast={toast} />)
    await waitFor(() => expect(cards.fetchProposal).toHaveBeenCalled())
    const reject = screen.getByTestId('proposal-reject')
    expect(txt(reject)).toBe('Reject…')
    expect(screen.queryByTestId('proposal-refuse')).toBeNull()
    fireEvent.click(reject)
    expect(screen.getByTestId('confirm-ok').textContent).toBe('Reject')
    expect(screen.getByTestId('confirm').textContent).toContain('Rejecting leaves things as they are')
    fireEvent.change(screen.getByTestId('decide-comment'), { target: { value: 'not now' } })
    await act(async () => {
      fireEvent.click(screen.getByTestId('confirm-ok'))
    })
    expect(cards.rejectProposal).toHaveBeenCalledWith('PC-7', 'not now')
    expect(toast).toHaveBeenCalledWith('Rejected.')
  })
})
