import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { subscribeAssistant, type AssistantHandlers } from './assistants'

class FakeEventSource extends EventTarget {
  static CLOSED = 2
  static made: FakeEventSource[] = []
  readyState = 0
  closed = false
  constructor(readonly url: string) {
    super()
    FakeEventSource.made.push(this)
  }
  close() {
    this.closed = true
    this.readyState = 2
  }
  emit(name: string, data: unknown) {
    this.dispatchEvent(new MessageEvent(name, { data: JSON.stringify(data) }))
  }
}

function handlers() {
  const got: string[] = []
  const h: AssistantHandlers = {
    open: () => got.push('open'),
    error: () => got.push('error'),
    event: (name, data) => got.push(`${name} ${data}`),
  }
  return { h, got }
}

describe('the assistants’ shared stream', () => {
  beforeEach(() => {
    FakeEventSource.made = []
    vi.stubGlobal('EventSource', FakeEventSource)
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('opens one connection for Sherlock and Watson and gives each its own events', () => {
    const s = handlers()
    const w = handlers()
    const offS = subscribeAssistant('local', 'sherlock', s.h)
    const offW = subscribeAssistant('local', 'watson', w.h)
    vi.runAllTimers()
    expect(FakeEventSource.made.map((e) => e.url)).toEqual(['/api/v1/assistants/events'])
    const es = FakeEventSource.made[0]!
    es.dispatchEvent(new Event('open'))
    es.emit('sherlock-status', { enabled: true })
    es.emit('watson-items', [])
    es.emit('watson-status', { available: true })
    expect(s.got).toEqual(['open', 'status {"enabled":true}'])
    expect(w.got).toEqual(['open', 'items []', 'status {"available":true}'])
    offS()
    expect(es.closed).toBe(false)
    offW()
    expect(es.closed).toBe(true)
  })

  it('connects again for a panel that joins later, so it starts from the full state', () => {
    const s = handlers()
    const offS = subscribeAssistant('local', 'sherlock', s.h)
    vi.runAllTimers()
    const w = handlers()
    const offW = subscribeAssistant('local', 'watson', w.h)
    vi.runAllTimers()
    expect(FakeEventSource.made.length).toBe(2)
    expect(FakeEventSource.made[0]!.closed).toBe(true)
    expect(FakeEventSource.made[1]!.closed).toBe(false)
    offS()
    offW()
  })

  it('keeps a stream per cluster, under the cluster’s path', () => {
    const offA = subscribeAssistant('local', 'watson', handlers().h)
    const offB = subscribeAssistant('staging', 'watson', handlers().h)
    vi.runAllTimers()
    expect(FakeEventSource.made.map((e) => e.url).sort()).toEqual(['/api/v1/assistants/events', '/api/v1/clusters/staging/assistants/events'])
    offA()
    offB()
  })

  it('opens the stream again after an HTTP error closes it for good', () => {
    const fetched: string[] = []
    vi.stubGlobal('fetch', (url: string) => {
      fetched.push(url)
      return Promise.resolve(new Response('{}', { status: 200 }))
    })
    const w = handlers()
    const off = subscribeAssistant('local', 'watson', w.h)
    vi.runAllTimers()
    const first = FakeEventSource.made[0]!
    // A dropped connection the browser retries by itself: left to it.
    first.dispatchEvent(new Event('error'))
    vi.advanceTimersByTime(60_000)
    expect(FakeEventSource.made.length).toBe(1)
    // A 502 from a proxy while the server restarts closes it for good.
    first.readyState = 2
    first.dispatchEvent(new Event('error'))
    expect(fetched).toEqual(['/api/v1/me'])
    vi.advanceTimersByTime(4_000)
    expect(FakeEventSource.made.length).toBe(2)
    expect(w.got).toEqual(['error', 'error'])
    off()
    first.dispatchEvent(new Event('error'))
    vi.advanceTimersByTime(60_000)
    expect(FakeEventSource.made.length).toBe(2)
  })
})
