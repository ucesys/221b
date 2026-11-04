import { act, cleanup, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AssistantHandlers } from './assistants'
import { clock, COMMAND_TOOLS, endedWords, fBytes, fetchToolbox, HOST_TOOLS, NODE_TOOLS, shownSessions, sortSessions, tabLabel, useSessions, type SessionView } from './toolbox'
import { remaining, tabState } from '../components/toolbox/SessionPanes'

const stream = vi.hoisted(() => ({ h: null as AssistantHandlers | null }))
vi.mock('./assistants', async (original) => ({
  ...(await original<typeof import('./assistants')>()),
  subscribeAssistant: (_cluster: string, _who: string, h: AssistantHandlers) => {
    stream.h = h
    return () => undefined
  },
}))

const base: SessionView = {
  id: 's1',
  cluster: 'local',
  tool: 'shell',
  summary: 'shell in pod web/api-1',
  request: { tool: 'shell', namespace: 'web', pod: 'api-1' },
  owner: 'Ada',
  mine: true,
  state: 'live',
  created: '2026-10-26T10:00:00Z',
  started: '2026-10-26T10:00:02Z',
  lastActive: '2026-10-26T10:00:00Z',
  ends: '2026-10-26T10:30:00Z',
  pod: 'api-1',
  container: 'main',
  recorded: false,
  interactive: true,
  idleSeconds: 600,
  rev: 1,
}

describe('session labels', () => {
  it('names a tab by its tool and target', () => {
    expect(tabLabel(base)).toBe('sh api-1 · main')
    expect(tabLabel({ ...base, tool: 'forward', request: { tool: 'forward', pod: 'pg-0', port: 5432 }, pod: 'pg-0' })).toBe('pf 5432 → pg-0')
    expect(tabLabel({ ...base, tool: 'forward', request: { tool: 'forward', pod: 'pg-0', port: 5432, localPort: 15432 }, pod: 'pg-0' })).toBe('pf 15432 → pg-0')
    expect(tabLabel({ ...base, tool: 'node-shell', request: { tool: 'node-shell', node: 'worker-3' } })).toBe('node worker-3')
  })

  it('says how a session ended', () => {
    expect(endedWords({ endedBy: 'idle' })).toBe('Ended after no activity.')
    expect(endedWords({ endedBy: 'admin', endNote: 'Ended by Bob: maintenance' })).toBe('Ended by Bob: maintenance')
  })

  it('puts live sessions first, newest first', () => {
    const a = { ...base, id: 'a', created: '2026-10-26T09:00:00Z' }
    const b = { ...base, id: 'b', created: '2026-10-26T11:00:00Z', state: 'ended' as const }
    const c = { ...base, id: 'c', created: '2026-10-26T10:00:00Z' }
    expect(sortSessions([a, b, c]).map((x) => x.id)).toEqual(['c', 'a', 'b'])
  })
})

describe('the tool lists', () => {
  it('leaves the debug pod out of the tools on the node itself, and the forward out of the commands', () => {
    expect(HOST_TOOLS).toEqual(NODE_TOOLS.filter((t) => t !== 'debug-pod'))
    expect(COMMAND_TOOLS).not.toContain('forward')
  })
})

describe('the sessions shown', () => {
  const live = { ...base, id: 'live' }
  const ended = { ...base, id: 'ended', state: 'ended' as const }

  it('drops an ended session whose tab was closed, and keeps a live one', () => {
    expect(shownSessions([live, ended], [], []).map((x) => x.id)).toEqual(['live', 'ended'])
    expect(shownSessions([live, ended], ['live', 'ended'], []).map((x) => x.id)).toEqual(['live'])
  })

  it('keeps an ended session opened again, whose tab still shows', () => {
    expect(shownSessions([ended], ['ended'], ['ended']).map((x) => x.id)).toEqual(['ended'])
  })
})

describe('what is known of the nodes', () => {
  const calls = vi.fn(async () => new Response('{}'))
  const send = (name: string, data: unknown) => act(() => stream.h!.event(name, JSON.stringify(data)))
  const pairing = (state: SessionView['state'], tool: SessionView['tool'] = 'ssh-pair') => ({ ...base, tool, state })
  let n = 0
  let cluster = ''

  beforeEach(() => {
    calls.mockClear()
    vi.stubGlobal('fetch', calls)
    vi.stubGlobal('EventSource', class {})
    // The cache outlives a test: each has a cluster of its own.
    cluster = `c${++n}`
    renderHook(() => useSessions(cluster))
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('is kept a minute', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    await fetchToolbox(cluster, [], true)
    await fetchToolbox(cluster, [], true)
    expect(calls).toHaveBeenCalledTimes(1)
    vi.setSystemTime(Date.now() + 61_000)
    await fetchToolbox(cluster, [], true)
    expect(calls).toHaveBeenCalledTimes(2)
  })

  it('goes when a pairing or an unpairing ends, for the next menu', async () => {
    await fetchToolbox(cluster, [], true)
    send('session', pairing('live'))
    await fetchToolbox(cluster, [], true)
    expect(calls).toHaveBeenCalledTimes(1)
    send('session', pairing('ended'))
    await fetchToolbox(cluster, [], true)
    expect(calls).toHaveBeenCalledTimes(2)
    send('session', pairing('ended', 'ssh-unpair'))
    await fetchToolbox(cluster, [], true)
    expect(calls).toHaveBeenCalledTimes(3)
  })

  it('goes when the list that returns has a pairing that ended meanwhile', async () => {
    await fetchToolbox(cluster, [], true)
    send('all', [pairing('ended')])
    await fetchToolbox(cluster, [], true)
    expect(calls).toHaveBeenCalledTimes(2)
  })

  it('stays when another session ends, and goes on no other cluster or for pods', async () => {
    await fetchToolbox(cluster, [], true)
    await fetchToolbox(cluster, ['web'])
    await fetchToolbox(`${cluster}x`, [], true)
    send('session', pairing('ended', 'node-command'))
    await fetchToolbox(cluster, [], true)
    expect(calls).toHaveBeenCalledTimes(3)
    send('session', pairing('ended'))
    await fetchToolbox(cluster, ['web'])
    await fetchToolbox(`${cluster}x`, [], true)
    expect(calls).toHaveBeenCalledTimes(3)
  })
})

describe('session clocks', () => {
  it('counts down to the idle end and the hard limit', () => {
    const now = Date.parse('2026-10-26T10:09:00Z')
    const r = remaining(base, now)
    expect(Math.round(r.idle)).toBe(60)
    expect(Math.round(r.limit)).toBe(21 * 60)
    expect(tabState(base, now)).toBe('idle')
    expect(tabState(base, Date.parse('2026-10-26T10:01:00Z'))).toBe('live')
    expect(tabState({ ...base, state: 'ended' }, now)).toBe('ended')
  })

  it('formats', () => {
    expect(clock(65)).toBe('1:05')
    expect(clock(3725)).toBe('1:02:05')
    expect(fBytes(512)).toBe('512 B')
    expect(fBytes(1536)).toBe('1.5 KB')
    expect(fBytes(50 * 1024 * 1024)).toBe('50 MB')
  })
})
