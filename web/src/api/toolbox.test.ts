import { describe, expect, it } from 'vitest'
import { clock, endedWords, fBytes, sortSessions, tabLabel, type SessionView } from './toolbox'
import { remaining, tabState } from '../components/toolbox/SessionPanes'

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
