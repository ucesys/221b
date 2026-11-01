import { cleanup, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ACTIVE_EVERY, checkSession, useActivity } from './auth'

const moved = (x: number) => Object.assign(new Event('pointermove'), { movementX: x, movementY: 0 })

describe('what counts as activity', () => {
  let asked: RequestInit[]
  let status: number
  // The throttle outlives a test: each starts past the last one's interval.
  let clock = Date.UTC(2026, 9, 31)

  beforeEach(() => {
    asked = []
    status = 200
    clock += 2 * ACTIVE_EVERY
    vi.useFakeTimers({ now: clock })
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init: RequestInit) => {
        expect(url).toBe('/api/v1/me')
        asked.push(init)
        return Promise.resolve(new Response('{}', { status }))
      }),
    )
  })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('tells the server of a key at most every few minutes, in a request not marked', () => {
    renderHook(() => useActivity())
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'a' }))
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'b' }))
    expect(asked.length).toBe(1)
    expect(asked[0]!.headers).not.toHaveProperty('X-221B-Background')
    vi.advanceTimersByTime(ACTIVE_EVERY)
    window.dispatchEvent(new Event('pointerdown'))
    expect(asked.length).toBe(2)
  })

  it('hears a key a terminal keeps to itself', () => {
    renderHook(() => useActivity())
    const terminal = document.body.appendChild(document.createElement('textarea'))
    terminal.addEventListener('keydown', (e) => e.stopPropagation())
    terminal.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', bubbles: true }))
    terminal.remove()
    expect(asked.length).toBe(1)
  })

  it('counts a pointer that moved and not one that stood still', () => {
    renderHook(() => useActivity())
    window.dispatchEvent(moved(0))
    expect(asked.length).toBe(0)
    window.dispatchEvent(moved(3))
    expect(asked.length).toBe(1)
  })

  it('stops listening with the page', () => {
    const { unmount } = renderHook(() => useActivity())
    unmount()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'a' }))
    expect(asked.length).toBe(0)
  })

  it('marks the check a failed stream makes', () => {
    checkSession()
    expect(asked[0]!.headers).toHaveProperty('X-221B-Background', '1')
  })
})
