import { describe, expect, it } from 'vitest'
import { linkable, proposalId, type Proposal } from './cards'
import { proposalHref } from '../components/cases/ProposalLinks'

// Proposed changes are PC-<n>; the c<n> of before names the same card,
// and a case's C-<n> never does (CF-56).
describe('a proposed change’s id (CF-56)', () => {
  it('is written PC-<n>, whatever form it comes in', () => {
    for (const id of ['c16', 'C16', 'pc-16', 'PC-16', 'pc16', ' PC-16 ']) expect(proposalId(id)).toBe('PC-16')
    for (const id of ['C-16', 'KI-3', 'c0', 'c-e2e', 'run-9', '']) expect(proposalId(id)).toBe(id)
    expect(proposalHref('c7')).toBe('#/cases/inbox?proposal=PC-7')
  })

  it('finds a card to link by its old id too', () => {
    const card = { id: 'PC-16', title: 'Restart web', state: 'queued', cluster: 'prod', updated: '2026-11-07T00:00:00Z' } as Proposal
    expect(linkable([card], { q: 'c16' }).map((p) => p.id)).toEqual(['PC-16'])
    expect(linkable([card], { q: 'pc-16' }).map((p) => p.id)).toEqual(['PC-16'])
    expect(linkable([card], { q: 'c17' })).toEqual([])
  })
})
