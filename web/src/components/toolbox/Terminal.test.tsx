import { act, cleanup, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { focusHeld, retryDelay, Terminal } from './Terminal'

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
    cleanup()
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

  it('takes the focus on opening only from nothing, never from where the person types', () => {
    const { container } = render(<Terminal cluster="local" id="s1" dark live />)
    act(() => FakeSocket.all[0]!.open())
    expect(container.contains(document.activeElement)).toBe(true)
    cleanup()
    for (const html of ['<input />', '<textarea></textarea>', '<select></select>', '<div contenteditable="true" tabindex="0"></div>', '<div role="dialog"><button>OK</button></div>']) {
      const box = document.body.appendChild(document.createElement('div'))
      box.innerHTML = html
      box.querySelector<HTMLElement>('input, textarea, select, button, div[contenteditable]')!.focus()
      const held = document.activeElement
      expect(held).not.toBe(document.body)
      render(<Terminal cluster="local" id="s2" dark live />)
      act(() => FakeSocket.all[FakeSocket.all.length - 1]!.open())
      expect(document.activeElement).toBe(held)
      cleanup()
      box.remove()
    }
  })

  it('on a reconnect keeps the focus it had and takes none', () => {
    const { container } = render(<Terminal cluster="local" id="s1" dark live />)
    const first = FakeSocket.all[0]!
    act(() => first.open())
    const own = document.activeElement
    expect(container.contains(own)).toBe(true)
    act(() => first.drop('network'))
    act(() => {
      vi.advanceTimersByTime(retryDelay(0))
    })
    act(() => FakeSocket.all[1]!.open())
    expect(document.activeElement).toBe(own)
    // Focus elsewhere, even on a plain button: a reconnect leaves it there.
    const other = document.body.appendChild(document.createElement('button'))
    other.focus()
    act(() => FakeSocket.all[1]!.drop('network'))
    act(() => {
      vi.advanceTimersByTime(retryDelay(0))
    })
    act(() => FakeSocket.all[2]!.open())
    expect(document.activeElement).toBe(other)
    other.remove()
  })

  it('knows what holds the focus', () => {
    expect(focusHeld(null)).toBe(false)
    expect(focusHeld(document.body)).toBe(false)
    expect(focusHeld(document.createElement('button'))).toBe(false)
    expect(focusHeld(document.createElement('input'))).toBe(true)
  })
})
