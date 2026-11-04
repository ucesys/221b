// A followed log whose stream drops and comes back shows the replay once.
import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ContainerDetail } from '../api/types'
import { PodLogs } from './PodLogs'

/** An EventSource the test drives. */
class FakeSource {
  static CONNECTING = 0
  static CLOSED = 2
  static last: FakeSource
  readyState = 0
  handlers: Record<string, ((e: unknown) => void)[]> = {}
  constructor(readonly url: string) {
    FakeSource.last = this
  }
  addEventListener(name: string, fn: (e: unknown) => void) {
    ;(this.handlers[name] ??= []).push(fn)
  }
  close() {
    this.readyState = 2
  }
  fire(name: string, e: unknown = {}) {
    act(() => this.handlers[name]?.forEach((fn) => fn(e)))
  }
  log(lines: string[]) {
    this.fire('log', { data: JSON.stringify(lines) })
  }
}

const shown = () => [...screen.getByTestId('log-lines').querySelectorAll('.rk-logline')].map((x) => x.textContent)
const containers = [{ name: 'app', init: false, state: { kind: 'running' } }] as unknown as ContainerDetail[]

describe('PodLogs', () => {
  beforeEach(() => vi.stubGlobal('EventSource', FakeSource))
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('appends while the stream lasts', () => {
    render(<PodLogs namespace="web" name="a" containers={containers} gone={false} />)
    const es = FakeSource.last
    es.fire('open')
    es.log(['1', '2'])
    es.log(['3'])
    expect(shown()).toEqual(['1', '2', '3'])
    expect(screen.queryByTestId('log-note')).toBeNull()
  })

  it('replaces the lines with what a reconnect replays, and says it reconnected', () => {
    render(<PodLogs namespace="web" name="a" containers={containers} gone={false} />)
    const es = FakeSource.last
    es.fire('open')
    es.log(['1', '2', '3'])
    // The browser retries by itself.
    es.fire('error')
    expect(screen.getByTestId('log-note').textContent).toContain('reconnecting')
    expect(screen.getByTestId('log-state').textContent).toBe('Reading…')
    es.fire('open')
    expect(screen.getByTestId('log-state').textContent).toContain('Live')
    es.log(['2', '3'])
    es.log(['4'])
    expect(shown()).toEqual(['2', '3', '4'])
    expect(screen.getByTestId('log-note').textContent).toContain('came back')
  })
})
