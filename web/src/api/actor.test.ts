import { describe, expect, it } from 'vitest'
import { actorFull, actorKind, actorName, actorText, lastChangeText } from './actor'

describe('who did it (CF-35)', () => {
  it('says a person, an agent and the system as the Inbox and the history do', () => {
    expect(actorText({ name: 'Pat', kind: 'person' })).toBe('Pat')
    expect(actorText({ name: 'Auditor', kind: 'agent' })).toBe('Auditor (agent)')
    expect(actorText({ name: 'system', kind: 'system' })).toBe('System')
    expect(actorFull({ name: 'Pat', kind: 'person' })).toBe('Pat (person)')
    expect(actorFull({ name: 'Auditor', kind: 'agent' })).toBe('Auditor (agent)')
    expect(actorFull({ name: 'x', kind: 'system' })).toBe('System')
  })

  it('reads a bare name as a person, and nothing as nobody', () => {
    expect(actorName('Ann')).toBe('Ann')
    expect(actorKind('Ann')).toBe('person')
    expect(actorKind('system')).toBe('system')
    expect(actorText(undefined)).toBe('')
    expect(actorFull(null)).toBe('')
  })

  it('puts who and how long ago in one line: "Pat · 5m ago"', () => {
    const now = Date.parse('2026-11-07T12:00:00Z')
    expect(lastChangeText({ name: 'Pat', kind: 'person' }, '2026-11-07T11:55:00Z', now)).toBe('Pat · 5m ago')
    expect(lastChangeText({ name: 'Auditor', kind: 'agent' }, '2026-11-07T10:00:00Z', now)).toBe('Auditor (agent) · 2h ago')
    expect(lastChangeText(undefined, '2026-11-07T10:00:00Z', now)).toBe('2h ago')
    expect(lastChangeText(undefined, undefined, now)).toBe('')
  })
})
