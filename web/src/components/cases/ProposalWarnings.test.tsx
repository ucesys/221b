// A fleet proposal's warnings in its drawer (Phase 12, KN-30): an empty
// fleet's plan, and Check again finding a fleet empty now.
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as cards from '../../api/cards'
import type { Proposal } from '../../api/cards'
import * as proposalSherlock from '../../api/proposalSherlock'
import * as watson from '../../api/watson'
import { forgetSharedForTests } from '../../lib/cases'
import { ProposalDrawer } from './ProposalDrawer'

vi.mock('../../api/cards', async (original) => ({ ...(await original<typeof import('../../api/cards')>()), fetchProposal: vi.fn() }))
vi.mock('../../api/proposalSherlock', async (original) => ({ ...(await original<typeof import('../../api/proposalSherlock')>()), useProposalSherlock: vi.fn() }))
vi.mock('../../api/watson', async (original) => ({ ...(await original<typeof import('../../api/watson')>()), useWatson: vi.fn() }))

const card = (over: Partial<Proposal> = {}): Proposal =>
  ({
    id: 'PC-22',
    agent: 'mycroft',
    agentName: 'Mycroft',
    source: { kind: 'knowledge', id: 'sandbox:nodes.md', label: 'sandbox-knowledge “nodes.md”' },
    cluster: 'local',
    clusterName: 'local',
    title: 'Add 1 fleet from “nodes.md”',
    reasoning: '“nodes.md” names the group GPU workers.',
    actions: [{ kind: 'fleet.create', args: { name: 'GPU workers', labels: ['pool=gpu'], taints: [] }, risk: 'low' }],
    condition: { kind: 'none' },
    expires: '2099-11-09T02:00:00Z',
    state: 'queued',
    created: '2026-11-08T02:00:00Z',
    updated: '2026-11-08T02:00:00Z',
    needed: 1,
    plans: [{ summary: 'Create fleet GPU workers', targets: ['fleet GPU workers'], impact: ['Creates the fleet GPU workers: the nodes with labels pool=gpu.', '2 nodes now: worker1, worker2.'], fingerprint: 'absent' }],
    canApprove: true,
    mayAsk: false,
    ...over,
  }) as Proposal

beforeEach(() => {
  Element.prototype.scrollTo = () => {}
  vi.stubGlobal('fetch', async () => new Response('{}', { status: 404 }))
  vi.mocked(proposalSherlock.useProposalSherlock).mockReturnValue({ status: { available: false, enabled: false, key: false, model: '', admin: false }, live: false, conv: null })
  vi.mocked(watson.useWatson).mockReturnValue({ live: false, items: [] } as unknown as ReturnType<typeof watson.useWatson>)
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
  forgetSharedForTests()
})

async function open(p: Proposal) {
  vi.mocked(cards.fetchProposal).mockResolvedValue(p)
  render(<ProposalDrawer id={p.id} initial={p} onClose={() => undefined} />)
  await waitFor(() => expect(cards.fetchProposal).toHaveBeenCalled())
}

describe('a fleet proposal’s members (KN-30)', () => {
  it('shows the members now and no warning while the fleet has some', async () => {
    await open(card())
    // The members are on the step's line (CF-92), their names in its hover.
    expect(screen.getByTestId('proposal-step').textContent).toContain('2 nodes now')
    expect(screen.queryByTestId('proposal-plan-warning')).toBeNull()
    expect(screen.queryByTestId('proposal-recheck-warning')).toBeNull()
  })

  it('warns when Check again finds the fleet empty now, though it still holds', async () => {
    const warning = 'Action 1 (Create fleet GPU workers): No node or host has its labels now: the fleet would be empty.'
    await open(card({ recheck: { at: '2026-11-08T03:00:00Z', ok: true, warnings: [warning] } }))
    expect(screen.getByTestId('proposal-recheck-result').textContent).toContain('it holds')
    expect(screen.getByTestId('proposal-recheck-warning').textContent).toBe(warning)
  })

  it('shows a plan’s own warning', async () => {
    await open(card({ plans: [{ summary: 'Create fleet Burst', impact: ['0 nodes now.'], fingerprint: 'absent', warning: 'No node or host has its labels now: the fleet would be empty.' }] }))
    expect(screen.getByTestId('proposal-plan-warning').textContent).toBe('No node or host has its labels now: the fleet would be empty.')
  })
})
