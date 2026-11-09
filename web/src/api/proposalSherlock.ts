// Sherlock on a proposed change (Phase 12, CF-61): one conversation per
// proposal, kept by the cards on the server and shared by the people who
// may approve it or work the cases, each question naming who asked. It is
// streamed as a case's is, and deleted once the proposal is decided.

import { useSharedSherlock } from './caseSherlock'
import { sherlockCall, type Conversation, type SherlockState } from './sherlock'

const base = (id: string) => `/api/v1/cases/proposals/${encodeURIComponent(id)}/sherlock`

export const askProposalSherlock = (id: string, question: string) => sherlockCall<Conversation>('POST', `${base(id)}/ask`, { question })
export const stopProposalSherlock = (id: string) => sherlockCall<{ stopped: boolean }>('POST', `${base(id)}/stop`, {})

/** A proposal's conversation as it changes: off while on is false (the proposal decided). */
export function useProposalSherlock(id: string, on = true): SherlockState {
  return useSharedSherlock(base(id), on)
}

/** Where a command sent from a proposal says it came from: "proposal:PC-12" (CF-61). */
export const proposalFrom = (id: string) => `proposal:${id.toUpperCase()}`
