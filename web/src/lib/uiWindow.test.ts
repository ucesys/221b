// The main window runs Sherlock's view actions from every conversation:
// the person's own and a shared one, a case's or a proposal's (SR-18, SR-19).
import { renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { runSharedUI, type Conversation } from '../api/sherlock'
import { setRouteForTests } from './router'
import { setAnswering, WINDOW_ID } from './uiControl'
import { useSharedAnswering, useUIControl } from './uiWindow'

const scope = '/api/v1/aiops/cases/12/sherlock'

const conv = (busy: boolean): Conversation => ({
  id: 'c',
  busy,
  rev: 1,
  messages: [
    { id: 1, role: 'user', text: 'Show the pods', at: '2026-11-13T10:00:00Z' },
    { id: 2, role: 'sherlock', text: '', status: 'thinking', at: '2026-11-13T10:00:00Z' },
  ],
})

describe('view actions from a shared conversation (SR-19)', () => {
  afterEach(() => {
    setAnswering('local', null)
    window.location.hash = ''
  })

  it('holds a change until the answer is done, then makes it, and replies to the shared conversation', async () => {
    window.location.hash = '#/nodes'
    setRouteForTests('#/nodes')
    // The page's own move to Nodes is over before Sherlock answers.
    await new Promise((r) => setTimeout(r, 20))
    const main = renderHook(() => useUIControl('local', null))
    const asked = renderHook(({ busy }) => useSharedAnswering(scope, conv(busy), new Set([2])), { initialProps: { busy: true } })
    const reply = vi.fn(async () => {})
    const req = { id: 'u1', window: WINDOW_ID, answer: 2, action: 'view_open', input: { tab: 'pods' } }
    // During the answer: checked and held; the screen stays (SR-18).
    runSharedUI({ ...req, hold: true }, scope, reply)
    await vi.waitFor(() => expect(reply).toHaveBeenCalledWith('u1', { applied: false, held: true }))
    expect(window.location.hash).toBe('#/nodes')
    // A wrong input is refused when checked, not held.
    runSharedUI({ ...req, id: 'u2', input: { tab: 'nowhere' }, hold: true }, scope, reply)
    await vi.waitFor(() => expect(reply).toHaveBeenCalledWith('u2', expect.objectContaining({ applied: false, error: expect.stringContaining('tab must be one of') })))
    // The answer done, the server sends it again: made now.
    runSharedUI({ ...req, id: 'u3' }, scope, reply)
    await vi.waitFor(() => expect(reply).toHaveBeenCalledWith('u3', expect.objectContaining({ applied: true })))
    expect(window.location.hash).toBe('#/pods')
    // Another window's request is not this one's.
    runSharedUI({ ...req, id: 'u4', window: 'someoneelse0' }, scope, reply)
    await new Promise((r) => setTimeout(r, 50))
    expect(reply).not.toHaveBeenCalledWith('u4', expect.anything())
    asked.rerender({ busy: false })
    asked.unmount()
    main.unmount()
  })

  it('ends only its own answer, never the person’s own one in progress', async () => {
    const main = renderHook(() => useUIControl('local', null))
    const reply = vi.fn(async () => {})
    setAnswering('local', 7)
    // A shared answer this window did not ask leaves the own answer alone.
    const other = renderHook(() => useSharedAnswering(scope, conv(true), new Set()))
    other.unmount()
    runSharedUI({ id: 'o1', window: WINDOW_ID, answer: 7, action: 'view_open', input: { tab: 'pods' }, hold: true }, 'local', reply)
    await vi.waitFor(() => expect(reply).toHaveBeenCalledWith('o1', { applied: false, held: true }))
    main.unmount()
  })
})
