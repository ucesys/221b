import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { HistoryEntry, HistoryText } from '../../api/ai'
import { defaultPair, entryKey, HistoryDialog } from './HistoryDialog'
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

  it('lists the versions and drafts newest first and compares the draft now with the version in force, field by field', async () => {
    const onClose = vi.fn()
    render(<HistoryDialog agentId="auditor" agentName="Auditor" clusterName={(id) => (id === 'local' ? 'Here' : id)} onClose={onClose} />)
    const dlg = await screen.findByTestId('history-dialog')
    expect(dlg.getAttribute('role')).toBe('dialog')
    await waitFor(() => expect(within(dlg).getAllByTestId('history-entry')).toHaveLength(4))
    const rows = within(dlg).getAllByTestId('history-entry')
    expect(rows.map((r) => r.getAttribute('data-key'))).toEqual(['draft:5', 'draft:4', 'live:2', 'live:1'])
    expect(rows[0]!.textContent).toContain('The draft now')
    expect(rows[0]!.textContent).toContain('tested: 5 of 6 passed')
    expect(rows[2]!.textContent).toContain('made live untested: hotfix')
    expect(within(dlg).getByTestId('history-pair').textContent).toBe('Version 2 → Draft 5')
    await waitFor(() => expect(within(dlg).getAllByTestId('version-diff-field').length).toBeGreaterThan(0))
    const fields = within(dlg).getAllByTestId('version-diff-field')
    expect(fields.map((f) => f.textContent?.split(':')[0])).toEqual(['House instructions', 'Notes for Here'])
    expect(fields[0]!.textContent).toContain('+Then SOC 2.')
    expect(within(dlg).getByTestId('version-diff-unchanged').textContent).toBe('No change: Built-in instructions, Notes for every cluster.')
    // Any two: version 1 against draft 4.
    fireEvent.click(within(rows[3]!).getByTestId('history-from'))
    fireEvent.click(within(rows[1]!).getByTestId('history-to'))
    expect(within(dlg).getByTestId('history-pair').textContent).toBe('Version 1 → Draft 4')
    await waitFor(() =>
      expect(
        within(dlg)
          .getAllByTestId('version-diff-field')
          .map((f) => f.textContent?.split(':')[0]),
      ).toEqual(['House instructions', 'Built-in instructions']),
    )
    // Each side is read once.
    expect(calls.filter((c) => c.url.endsWith('/history/draft/5'))).toHaveLength(1)
    fireEvent.click(within(dlg).getByTestId('history-close'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('restores a version once live, not the one in force, and reads the history again', async () => {
    const onRestore = vi.fn(async () => {})
    render(<HistoryDialog agentId="auditor" agentName="Auditor" onRestore={onRestore} onClose={() => {}} />)
    const dlg = await screen.findByTestId('history-dialog')
    await waitFor(() => expect(within(dlg).getAllByTestId('history-entry')).toHaveLength(4))
    const restores = within(dlg).getAllByTestId('history-restore')
    expect(restores).toHaveLength(1)
    fireEvent.click(restores[0]!)
    await waitFor(() => expect(onRestore).toHaveBeenCalledWith(1))
    await waitFor(() => expect(calls.filter((c) => c.url.endsWith('/history'))).toHaveLength(2))
  })

  it('blocks Restore while there are unsaved edits, and Escape closes it', async () => {
    const onClose = vi.fn()
    render(<HistoryDialog agentId="auditor" agentName="Auditor" restoreBlocked="Save or undo your edits first." onRestore={async () => {}} onClose={onClose} />)
    const dlg = await screen.findByTestId('history-dialog')
    const restore = await within(dlg).findByTestId('history-restore')
    expect(restore.hasAttribute('disabled')).toBe(true)
    expect(restore.getAttribute('title')).toBe('Save or undo your edits first.')
    fireEvent.keyDown(dlg, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('picks the draft against the version in force, else the newest two', () => {
    expect(defaultPair(entries)).toEqual(['live:2', 'draft:5'])
    expect(defaultPair(entries.slice(2))).toEqual(['live:1', 'live:2'])
    expect(defaultPair([entries[2]!])).toEqual(['live:2', 'live:2'])
    expect(defaultPair([])).toBeNull()
    expect(entryKey({ kind: 'discarded', rev: 7 })).toBe('draft:7')
    expect(entryKey({ kind: 'live', n: 0 })).toBe('live:0')
  })

  it('lists every field either side has', () => {
    const f = versionFields({ house: 'a', notes: { x: '1' }, parts: core('c') }, { house: 'b', notes: { '*': '2' }, parts: [] })
    expect(f.map((x) => x.label)).toEqual(['House instructions', 'Built-in instructions', 'Notes for every cluster', 'Notes for x'])
    expect(f[1]).toMatchObject({ from: 'c', to: '' })
  })
})
