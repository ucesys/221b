// Sherlock on a proposed change (Phase 12, CF-61): one conversation per
// proposal, kept by the cards on the server and shared by the people who
// may approve it or work the cases, each question naming who asked. It is
// streamed as a case's is, and deleted once the proposal is decided.

import { useSharedSherlock } from './caseSherlock'
import { sherlockCall, type AskContext, type Conversation, type SherlockState } from './sherlock'

const base = (id: string) => `/api/v1/aiops/proposals/${encodeURIComponent(id)}/sherlock`

/** A proposal's conversation's address, which also names it to the view actions (SR-19). */
export const proposalSherlockBase = base

/** Asks on a proposal, with the asker's window and view, as on a case (SR-19). */
export const askProposalSherlock = (id: string, question: string, ctx?: AskContext) => sherlockCall<Conversation>('POST', `${base(id)}/ask`, { question, ...(ctx ? { window: ctx.window, view: ctx.view } : {}) })
export const stopProposalSherlock = (id: string) => sherlockCall<{ stopped: boolean }>('POST', `${base(id)}/stop`, {})

/** A proposal's conversation as it changes: off while on is false (the proposal decided). */
export function useProposalSherlock(id: string, on = true): SherlockState {
  return useSharedSherlock(base(id), on)
}

/** Where a command sent from a proposal says it came from: "proposal:PC-12" (CF-61). */
export const proposalFrom = (id: string) => `proposal:${id.toUpperCase()}`
