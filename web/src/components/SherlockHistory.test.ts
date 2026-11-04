import { describe, expect, it } from 'vitest'
import { recall, remember } from './Sherlock'

// The arrow keys recall earlier questions in the question box (asked
// October 30, 2026), as a shell does.
describe('Sherlock question history', () => {
  it('remembers questions once each, newest last, fifty at most', () => {
    let h = remember([], 'why is shop pending?')
    h = remember(h, 'what changed?')
    h = remember(h, 'why is shop pending?')
    expect(h).toEqual(['what changed?', 'why is shop pending?'])
    for (let i = 0; i < 60; i++) h = remember(h, `q${i}`)
    expect(h).toHaveLength(50)
    expect(h[49]).toBe('q59')
  })

  it('walks up through the history and back down to the draft', () => {
    const h = ['first', 'second', 'third']
    const at = { index: -1, draft: '' }
    expect(recall('ArrowUp', 'typing', 6, h, at)).toBe('third')
    expect(recall('ArrowUp', 'third', 5, h, at)).toBe('second')
    expect(recall('ArrowUp', 'second', 6, h, at)).toBe('first')
    // Past the oldest it stays.
    expect(recall('ArrowUp', 'first', 5, h, at)).toBe('first')
    expect(recall('ArrowDown', 'first', 5, h, at)).toBe('second')
    expect(recall('ArrowDown', 'second', 6, h, at)).toBe('third')
    expect(recall('ArrowDown', 'third', 5, h, at)).toBe('typing')
    // At the draft, down does nothing; up starts again from the newest.
    expect(recall('ArrowDown', 'typing', 6, h, at)).toBeNull()
    expect(recall('ArrowUp', 'typing', 6, h, at)).toBe('third')
  })

  it('leaves the keys alone inside a multi-line draft, and without history', () => {
    const at = { index: -1, draft: '' }
    expect(recall('ArrowUp', 'line one\nline two', 12, ['x'], at)).toBeNull()
    expect(recall('ArrowUp', 'line one\nline two', 3, ['x'], at)).toBe('x')
    const at2 = { index: 0, draft: 'd' }
    expect(recall('ArrowDown', 'line one\nline two', 3, ['x'], at2)).toBeNull()
    expect(recall('ArrowUp', 'anything', 0, [], at)).toBeNull()
  })
})
