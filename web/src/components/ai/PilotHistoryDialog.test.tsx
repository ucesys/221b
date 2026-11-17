import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { PilotEntry } from '../../api/ai'
import { newestFirst, PILOT_PAGE, PilotHistoryDialog } from './PilotHistoryDialog'

const entry = (n: number): PilotEntry => ({
  n,
  agent: 'troubleshooter',
  version: n % 2 ? 'pilot draft 3' : 'live',
  at: `2026-11-17T10:${String(n % 60).padStart(2, '0')}:00Z`,
  who: 'dev@corp.example',
  cluster: 'local',
  question: `Question ${n}?`,
  answer: `Answer ${n}.`,
  model: 'claude-opus-5-5',
  costUSD: 0.01,
})

describe('the pilot history dialog (AI-27)', () => {
  const calls: { method: string; url: string; body?: unknown }[] = []
  let entries: PilotEntry[] = []
  beforeEach(() => {
    calls.length = 0
    // The server answers newest first; the dialog sorts them so anyway.
    entries = Array.from({ length: 60 }, (_, i) => entry(i + 1))
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push({ method, url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      return new Response(JSON.stringify({ entries }))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('lists a long history newest first, a page at a time, scrolled inside the dialog, with nothing to rate (AI-29)', async () => {
    const onCount = vi.fn()
    render(<PilotHistoryDialog agentId="troubleshooter" agentName="Troubleshooter" onClose={() => {}} onCount={onCount} />)
    const dlg = await screen.findByTestId('pilot-history-dialog')
    expect(dlg.getAttribute('role')).toBe('dialog')
    expect(dlg.getAttribute('aria-modal')).toBe('true')
    await waitFor(() => expect(within(dlg).getAllByTestId('pilot-history-entry')).toHaveLength(PILOT_PAGE))
    expect(calls[0]).toMatchObject({ method: 'GET', url: '/api/v1/ai/agents/troubleshooter/pilot' })
    expect(onCount).toHaveBeenCalledWith(60)
    expect(within(dlg).getByTestId('pilot-history-count').textContent).toBe('60 answers')
    const rows = () => within(dlg).getAllByTestId('pilot-history-entry')
    expect(rows()[0]!.textContent).toContain('Question 60?')
    expect(rows()[0]!.textContent).toContain('the live version')
    expect(rows()[1]!.textContent).toContain('the draft')
    expect(within(dlg).getByTestId('pilot-history-list').style.overflow).toBe('auto')
    fireEvent.click(within(dlg).getByTestId('pilot-history-more'))
    expect(rows()).toHaveLength(2 * PILOT_PAGE)
    expect(within(dlg).getByTestId('pilot-history-more').textContent).toBe('Show 10 more')
    fireEvent.click(within(dlg).getByTestId('pilot-history-more'))
    expect(rows()).toHaveLength(60)
    expect(within(dlg).queryByTestId('pilot-history-more')).toBeNull()
    expect(rows()[59]!.textContent).toContain('Question 1?')
    expect(within(dlg).queryByTestId('pilot-history-good')).toBeNull()
    expect(within(dlg).queryByTestId('pilot-history-poor')).toBeNull()
    expect(within(dlg).queryByRole('button', { name: /good|poor/i })).toBeNull()
    expect(calls.every((c) => c.method === 'GET')).toBe(true)
  })

  it('says when nothing was logged yet, and Escape closes it', async () => {
    entries = []
    const onClose = vi.fn()
    render(<PilotHistoryDialog agentId="troubleshooter" agentName="Troubleshooter" onClose={onClose} />)
    const dlg = await screen.findByTestId('pilot-history-dialog')
    await waitFor(() => expect(dlg.textContent).toContain('No answer logged in this pilot yet.'))
    fireEvent.keyDown(dlg, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('sorts newest first by number', () => {
    expect(newestFirst([entry(2), entry(9), entry(5)]).map((e) => e.n)).toEqual([9, 5, 2])
  })
})
