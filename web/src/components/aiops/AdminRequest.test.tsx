import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { HistoryEntry } from '../../api/ai'
import type { ApprovalRow } from '../../api/automation'
import { AdminRequestDetail, baseEntry, requestSummary } from './AdminRequest'
import { ApprovalDrawer } from './InboxDrawers'

const row = (extra: Partial<ApprovalRow> = {}): ApprovalRow => ({
  kind: 'admin',
  state: 'pending',
  what: 'Auditor: instructions, draft 5, go live untested (reason: hotfix)',
  requestedBy: 'Ann',
  requestedAt: '2026-11-17T12:00:00Z',
  id: '3',
  request: { kind: 'prompt', agent: 'auditor', agentName: 'Auditor', rev: 5, untested: 'hotfix' },
  ...extra,
})

const entries: HistoryEntry[] = [
  { kind: 'draft', rev: 5, by: 'ann', at: '2026-11-17T11:00:00Z', current: true, test: { at: '2026-11-17T11:30:00Z', cases: 6, passed: 4, state: 'done' } },
  { kind: 'live', n: 2, rev: 3, inForce: true, at: '2026-11-16T10:00:00Z' },
  { kind: 'live', n: 1, rev: 1, at: '2026-11-15T10:00:00Z' },
]

describe('an approval says what it approves (AI-35)', () => {
  let gone = false
  beforeEach(() => {
    gone = false
    // jsdom has no scrollTo on elements; the drawer scrolls its body to the top.
    Element.prototype.scrollTo = () => {}
    vi.stubGlobal('fetch', async (url: string) => {
      const u = String(url)
      if (u.endsWith('/history')) return new Response(JSON.stringify({ entries }))
      if (u.endsWith('/history/draft/5')) return gone ? new Response(JSON.stringify({ error: 'not found' }), { status: 404 }) : new Response(JSON.stringify({ ...entries[0], house: 'Weigh PCI first.\nThen SOC 2.', parts: [] }))
      if (u.endsWith('/history/live/2')) return new Response(JSON.stringify({ ...entries[1], house: 'Weigh PCI first.', parts: [] }))
      return new Response('{}')
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('shows the agent, the draft, untested and why, its test, who asked, and its diff against the version in force', async () => {
    render(<AdminRequestDetail row={row()} />)
    expect(screen.getByTestId('approval-prompt-agent').textContent).toBe('Auditor')
    expect(screen.getByTestId('approval-prompt-agent').getAttribute('href')).toContain('admin/settings/agents/auditor')
    expect(screen.getByTestId('approval-prompt-how').textContent).toBe('untested, because: hotfix')
    await waitFor(() => expect(screen.getByTestId('approval-prompt-test').textContent).toContain('tested: 4 of 6 passed'))
    expect(screen.getByTestId('approval-prompt-rev').textContent).toContain('Draft 5, saved by ann')
    await waitFor(() => expect(screen.getByTestId('approval-prompt-diff').textContent).toContain('+Then SOC 2.'))
    expect(screen.getByTestId('approval-prompt').textContent).toContain('version 2 → draft 5')
  })

  it('says so when the draft is no longer kept', async () => {
    gone = true
    render(<AdminRequestDetail row={row({ request: { kind: 'prompt', agent: 'auditor', agentName: 'Auditor', rev: 5 } })} />)
    expect(screen.getByTestId('approval-prompt-how').textContent).toBe('tested')
    await waitFor(() => expect(screen.getByTestId('approval-prompt-gone').textContent).toBe('Draft 5 is no longer kept: a draft that went live since dropped it.'))
  })

  it('compares a draft that went live with the version before it', () => {
    expect(baseEntry(entries, { kind: 'live', n: 2, rev: 3 })?.n).toBe(1)
    expect(baseEntry(entries, entries[0]!)?.n).toBe(2)
  })

  it('says what other changes do', () => {
    expect(requestSummary({ kind: 'environment', from: 'production', to: 'staging' }, { clusterName: 'Lakehouse' })).toContain('The environment of Lakehouse changes from production to staging.')
    expect(requestSummary({ kind: 'superadmin', user: 'eve', super: true }, {})).toContain('eve becomes a SuperAdmin, by the SuperAdmin role')
  })

  it('the drawer says plainly who may act, not that a console change has no page of its own', async () => {
    render(<ApprovalDrawer id="3" row={row()} loaded onClose={() => {}} onDecided={() => {}} />)
    const drawer = await screen.findByTestId('approval-drawer')
    expect(drawer.textContent).toContain('You can approve or reject it here. The person who asked can only withdraw it.')
    expect(drawer.textContent).not.toContain('no page of its own')
    render(<ApprovalDrawer id="3" row={row({ own: true })} loaded onClose={() => {}} onDecided={() => {}} />)
    await waitFor(() => expect(screen.getAllByTestId('approval-drawer')[1]!.textContent).toContain('You asked for this: another SuperAdmin approves or rejects it. You can only withdraw it.'))
  })
})
