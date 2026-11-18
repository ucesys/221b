import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SherlockMsg, SherlockState } from '../api/sherlock'
import { Dock } from './Dock'
import { SherlockPane, elapsed, workingLabel } from './Sherlock'

// PM-U1: while Sherlock works, a banner above the question box says so.

const at = new Date(Date.now() - 65_000).toISOString()
const answer = (m: Partial<SherlockMsg>): SherlockMsg => ({ id: 2, role: 'sherlock', text: '', status: 'thinking', at, ...m })
const stateOf = (m: SherlockMsg, busy: boolean) =>
  ({
    status: { enabled: true, available: true, model: 'm', admin: false, key: true },
    conv: { id: 'c', rev: 1, busy, messages: [{ id: 1, role: 'user', text: 'Why?', at }, m] },
    live: true,
  }) as unknown as SherlockState

const fetched = vi.fn(() => Promise.resolve(new Response('{}', { headers: { 'Content-Type': 'application/json' } })))
beforeEach(() => {
  fetched.mockClear()
  vi.stubGlobal('fetch', fetched)
  document.title = '221B'
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const pane = (m: SherlockMsg, busy: boolean) => <SherlockPane cluster="prod" clusterName="prod" state={stateOf(m, busy)} onToast={() => undefined} />

describe('the working banner', () => {
  it('shows while Sherlock is busy, and is gone, with the title restored, once it is done', () => {
    const { rerender } = render(pane(answer({}), true))
    const banner = screen.getByTestId('sherlock-working')
    expect(banner.getAttribute('role')).toBe('status')
    expect(banner.getAttribute('aria-live')).toBe('polite')
    expect(screen.getByTestId('sherlock-working-time').textContent).toMatch(/1:0[5-9]/)
    expect(screen.getByTestId('sherlock-working-time').getAttribute('aria-hidden')).toBe('true')
    expect(document.title).toBe('● 221B')
    rerender(pane(answer({ status: 'done', text: 'Because.' }), false))
    expect(screen.queryByTestId('sherlock-working')).toBeNull()
    expect(document.title).toBe('221B')
  })

  it('restores the title when it unmounts mid-run', () => {
    const { unmount } = render(pane(answer({}), true))
    expect(document.title).toBe('● 221B')
    unmount()
    expect(document.title).toBe('221B')
  })

  it('says who or what is working, first match wins', () => {
    const hand = { id: 1, from: 'sherlock', fromName: 'Sherlock', to: 'troubleshooter', toName: 'Troubleshooter', task: 'look', status: 'running' as const }
    const steps = [
      { label: 'Read the alerts', tool: 'list_alerts', status: 'done' as const },
      { label: 'List the pods', tool: 'list_pods', status: 'running' as const },
    ]
    expect(workingLabel(answer({ hands: [hand], steps }), false)).toBe('The Troubleshooter is working')
    expect(workingLabel(answer({ steps }), false)).toBe('List the pods')
    expect(workingLabel(answer({ status: 'answering', text: 'So far' }), false)).toBe('Writing the answer')
    expect(workingLabel(answer({}), false)).toBe('Thinking')
    expect(workingLabel(undefined, true)).toBe('Sending')
    render(pane(answer({ hands: [hand] }), true))
    expect(screen.getByTestId('sherlock-working-label').textContent).toBe('The Troubleshooter is working')
  })

  it('counts the reads, only when there are some', () => {
    const { rerender } = render(pane(answer({}), true))
    expect(screen.queryByTestId('sherlock-working-reads')).toBeNull()
    rerender(pane(answer({ steps: [{ label: 'Read the alerts', tool: 'list_alerts', status: 'done' }] }), true))
    expect(screen.getByTestId('sherlock-working-reads').textContent).toContain('1 read')
    rerender(
      pane(
        answer({
          steps: [
            { label: 'a', tool: 'a', status: 'done' },
            { label: 'b', tool: 'b', status: 'done' },
            { label: 'c', tool: 'c', status: 'running' },
          ],
        }),
        true,
      ),
    )
    expect(screen.getByTestId('sherlock-working-reads').textContent).toContain('3 reads')
  })

  it('has Stop, which stops the run; the header has none', async () => {
    render(pane(answer({}), true))
    const stops = screen.getAllByTestId('sherlock-stop')
    expect(stops).toHaveLength(1)
    expect(within(screen.getByTestId('sherlock-working')).getByTestId('sherlock-stop')).toBe(stops[0])
    await act(async () => {
      fireEvent.click(stops[0]!)
    })
    expect(fetched.mock.calls.some((c) => String((c as unknown[])[0]).includes('sherlock/stop'))).toBe(true)
  })

  it('asks the person to stop first in the box while it works', () => {
    render(pane(answer({}), true))
    expect(screen.getByTestId<HTMLTextAreaElement>('sherlock-input').placeholder).toBe('Sherlock is working. Stop to ask something else.')
  })

  it('formats the time as m:ss', () => {
    const t0 = Date.parse('2026-11-13T10:00:00Z')
    expect(elapsed('2026-11-13T10:00:00Z', t0 + 9_000)).toBe('0:09')
    expect(elapsed('2026-11-13T10:00:00Z', t0 + 125_000)).toBe('2:05')
  })
})

describe('the dock while Sherlock works', () => {
  const dock = (pane: 'sherlock' | 'watson', sherlockBusy: boolean) => (
    <Dock
      mode="open"
      onMode={() => undefined}
      height={300}
      onHeight={() => undefined}
      split={0.5}
      onSplit={() => undefined}
      pane={pane}
      onPane={() => undefined}
      sherlock={(_c, sw) => <div>{sw}</div>}
      watson={(_c, sw) => <div>{sw}</div>}
      waiting={0}
      busy={sherlockBusy}
      sherlockBusy={sherlockBusy}
    />
  )
  it('puts a dot on the Sherlock toggle while Watson is in front', () => {
    const { rerender } = render(dock('watson', true))
    const toggles = screen.getAllByTestId('dock-show-sherlock')
    expect(toggles.some((t) => within(t).queryByTestId('dock-sherlock-busy'))).toBe(true)
    rerender(dock('sherlock', true))
    expect(screen.queryAllByTestId('dock-sherlock-busy')).toHaveLength(0)
    rerender(dock('watson', false))
    expect(screen.queryAllByTestId('dock-sherlock-busy')).toHaveLength(0)
  })
})
