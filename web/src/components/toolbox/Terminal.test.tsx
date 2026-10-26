import { act, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { retryDelay, Terminal } from './Terminal'

/** A WebSocket the test drives: every one made is kept, in order. */
class FakeSocket {
  static all: FakeSocket[] = []
  static OPEN = 1
  readonly OPEN = 1
  readyState = 0
  binaryType = ''
  sent: unknown[] = []
  onopen: (() => void) | null = null
  onmessage: ((e: { data: ArrayBuffer | string }) => void) | null = null
  onclose: ((e: { reason: string }) => void) | null = null
  constructor(readonly url: string) {
    FakeSocket.all.push(this)
  }
  send(d: unknown) {
    this.sent.push(d)
  }
  close() {
    this.readyState = 3
  }
  open() {
    this.readyState = 1
    this.onopen?.()
  }
  text(s: string) {
    this.onmessage?.({ data: s })
  }
  bytes(s: string) {
    this.onmessage?.({ data: new TextEncoder().encode(s).buffer as ArrayBuffer })
  }
  drop(reason = '') {
    this.readyState = 3
    this.onclose?.({ reason })
  }
}

describe('Terminal', () => {
  beforeEach(() => {
    FakeSocket.all = []
    vi.stubGlobal('WebSocket', FakeSocket)
    vi.stubGlobal(
      'ResizeObserver',
      class {
        observe() {}
        disconnect() {}
      },
    )
    // jsdom has no matchMedia, which xterm reads for the device pixel ratio.
    vi.stubGlobal('matchMedia', () => ({ matches: false, media: '', addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }))
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('reconnects when the server says this window fell behind, and starts over from the replay', () => {
    render(<Terminal cluster="local" id="s1" dark live />)
    expect(FakeSocket.all).toHaveLength(1)
    const first = FakeSocket.all[0]!
    act(() => first.open())
    act(() => first.bytes('old output'))
    act(() => first.text(JSON.stringify({ type: 'dropped', note: 'The output ran ahead of this window' })))
    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(FakeSocket.all).toHaveLength(2)
    expect(FakeSocket.all[1]!.url).toBe(first.url)
    expect(first.readyState).toBe(3)
  })

  it('tries again after a lost connection while the session is live, and not after its end', () => {
    const { rerender } = render(<Terminal cluster="local" id="s1" dark live />)
    const first = FakeSocket.all[0]!
    act(() => first.open())
    act(() => first.drop('network'))
    expect(FakeSocket.all).toHaveLength(1)
    act(() => {
      vi.advanceTimersByTime(retryDelay(0))
    })
    expect(FakeSocket.all).toHaveLength(2)
    // The server says the session ended: nothing comes back.
    const second = FakeSocket.all[1]!
    act(() => second.open())
    act(() => second.text(JSON.stringify({ type: 'ended', note: 'Ended by its owner.' })))
    act(() => second.drop())
    act(() => {
      vi.advanceTimersByTime(60_000)
    })
    expect(FakeSocket.all).toHaveLength(2)
    // A session the stream already reports as ended is not retried either.
    rerender(<Terminal cluster="local" id="s1" dark live={false} />)
    act(() => {
      vi.advanceTimersByTime(60_000)
    })
    expect(FakeSocket.all).toHaveLength(2)
  })

  it('backs off from a second to ten', () => {
    expect([0, 1, 2, 3, 4, 9].map(retryDelay)).toEqual([1000, 2000, 4000, 8000, 10000, 10000])
  })
})
