import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { subscribeAssistant, type AssistantHandlers } from './assistants'

class FakeEventSource extends EventTarget {
  static made: FakeEventSource[] = []
  closed = false
  constructor(readonly url: string) {
    super()
    FakeEventSource.made.push(this)
  }
  close() {
    this.closed = true
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
})
