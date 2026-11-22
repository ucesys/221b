import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { HistoryEntry, HistoryText } from '../../api/ai'
import { defaultPair, entryKey, entryOption, HistoryDialog } from './HistoryDialog'
import { versionFields } from './TextDiff'

const core = (text: string) => [{ name: 'core', label: 'Built-in instructions', text }]

const entries: HistoryEntry[] = [
  { kind: 'draft', rev: 5, by: 'ann', at: '2026-11-17T12:00:00Z', current: true, test: { at: '2026-11-17T12:05:00Z', cases: 6, passed: 5, state: 'done' } },
  { kind: 'draft', rev: 4, by: 'ann', at: '2026-11-17T11:00:00Z' },
  { kind: 'live', n: 2, rev: 3, by: 'bob', at: '2026-11-16T10:00:00Z', note: 'made live untested: hotfix', inForce: true },
  { kind: 'live', n: 1, rev: 1, by: 'bob', at: '2026-11-15T10:00:00Z', note: 'made live' },
]

const texts: Record<string, HistoryText> = {
  'draft/5': { ...entries[0]!, house: 'Weigh PCI first.\nThen SOC 2.', notes: { '*': 'Sundays off.', local: 'Here.' }, parts: core('Core A.') },
  'draft/4': { ...entries[1]!, house: 'Weigh PCI first.', parts: core('Core A.') },
  'live/2': { ...entries[2]!, house: 'Weigh PCI first.', notes: { '*': 'Sundays off.' }, parts: core('Core A.') },
  'live/1': { ...entries[3]!, house: 'Be brief.', parts: core('Core B.') },
}

describe('the history dialog (AI-32)', () => {
  const calls: { method: string; url: string }[] = []
  beforeEach(() => {
    calls.length = 0
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const u = String(url)
      calls.push({ method: init?.method ?? 'GET', url: u })
      if (u.endsWith('/history')) return new Response(JSON.stringify({ entries }))
      const m = /\/history\/(live|draft)\/(\d+)$/.exec(u)
      if (m) return new Response(JSON.stringify(texts[`${m[1]}/${m[2]}`]))
      return new Response('{}')
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('compares the draft now with the version in force as one unified diff, any two picked in the selects (AI-41)', async () => {
    const onClose = vi.fn()
    render(<HistoryDialog agentId="auditor" agentName="Auditor" clusterName={(id) => (id === 'local' ? 'Here' : id)} onClose={onClose} />)
    const dlg = await screen.findByTestId('history-dialog')
    expect(dlg.getAttribute('role')).toBe('dialog')
    expect(within(dlg).getByRole('heading').textContent).toBe('Diff of Auditor instructions')
    const from = within(dlg).getByTestId('history-from') as HTMLSelectElement
    const to = within(dlg).getByTestId('history-to') as HTMLSelectElement
    await waitFor(() => expect(from.options).toHaveLength(4))
    expect([...to.options].map((o) => o.value)).toEqual(['draft:5', 'draft:4', 'live:2', 'live:1'])
    expect(to.options[0]!.textContent).toMatch(/^Draft 5 · the draft now · tested 5\/6 passed · .+ · ann$/)
    expect(to.options[2]!.textContent).toMatch(/^Version 2 · live · in force · .+ · bob$/)
    expect(from.value).toBe('live:2')
    expect(to.value).toBe('draft:5')
    const diff = await within(dlg).findByTestId('app-diff')
    expect(diff.querySelector('p')!.textContent).toBe('Version 2 → Draft 5: 2 changed — House instructions, Notes for Here')
    const text = diff.querySelector('pre')!.textContent!
    expect(text).toContain('--- house instructions (version 2)+++ house instructions (draft 5)@@')
    expect(text).toContain('+Then SOC 2.')
    expect(text).toContain('--- notes for Here (version 2)+++ notes for Here (draft 5)')
    expect(text).not.toContain('built-in instructions')
    expect(dlg.querySelectorAll('pre')).toHaveLength(1)
    // Any two: version 1 against draft 4.
    fireEvent.change(from, { target: { value: 'live:1' } })
    fireEvent.change(to, { target: { value: 'draft:4' } })
    await waitFor(() => expect(within(dlg).getByTestId('app-diff').querySelector('p')!.textContent).toBe('Version 1 → Draft 4: 2 changed — House instructions, Built-in instructions'))
    // The same twice: said so.
    fireEvent.change(from, { target: { value: 'draft:4' } })
    expect((await within(dlg).findByTestId('app-diff-same')).textContent).toBe('Draft 4 and Draft 4 are the same.')
    // Each side is read once.
    expect(calls.filter((c) => c.url.endsWith('/history/draft/5'))).toHaveLength(1)
    expect(within(dlg).queryByTestId('history-restore')).toBeNull()
    const close = within(dlg).getByTestId('history-close')
    expect(close.className).toBe('rk-icon-btn')
    fireEvent.click(close)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('opens on a version against the one before it, and Escape closes it', async () => {
    const onClose = vi.fn()
    render(<HistoryDialog agentId="auditor" agentName="Auditor" version={2} onClose={onClose} />)
    const dlg = await screen.findByTestId('history-dialog')
    await waitFor(() => expect((within(dlg).getByTestId('history-to') as HTMLSelectElement).value).toBe('live:2'))
    expect((within(dlg).getByTestId('history-from') as HTMLSelectElement).value).toBe('live:1')
    await waitFor(() => expect(within(dlg).getByTestId('app-diff').querySelector('p')!.textContent).toContain('Version 1 → Version 2: 3 changed'))
    fireEvent.keyDown(dlg, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('picks the draft against the version in force, else the newest two', () => {
    expect(defaultPair(entries)).toEqual(['live:2', 'draft:5'])
    expect(defaultPair(entries.slice(2))).toEqual(['live:1', 'live:2'])
    expect(defaultPair([entries[2]!])).toEqual(['live:2', 'live:2'])
    expect(defaultPair([])).toBeNull()
    expect(defaultPair(entries, 1)).toEqual(['live:1', 'live:1'])
    expect(defaultPair(entries, 2)).toEqual(['live:1', 'live:2'])
    expect(defaultPair(entries, 9)).toEqual(['live:2', 'draft:5'])
    expect(entryOption({ kind: 'discarded', rev: 8 })).toBe('Draft 8 · discarded')
    expect(entryOption({ kind: 'live', n: 3, by: 'bob', request: 4 })).toBe('Version 3 · live · bob · approved in request 4')
    expect(entryKey({ kind: 'discarded', rev: 7 })).toBe('draft:7')
    expect(entryKey({ kind: 'live', n: 0 })).toBe('live:0')
  })

  it('lists every field either side has', () => {
    const f = versionFields({ house: 'a', notes: { x: '1' }, parts: core('c') }, { house: 'b', notes: { '*': '2' }, parts: [] })
    expect(f.map((x) => x.label)).toEqual(['House instructions', 'Built-in instructions', 'Notes for every cluster', 'Notes for x'])
    expect(f[1]).toMatchObject({ from: 'c', to: '' })
  })
})
