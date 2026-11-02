import { describe, expect, it } from 'vitest'
import { lineDiff } from './linediff'

describe('lineDiff', () => {
  it('says nothing changed for the same text', () => {
    expect(lineDiff('a\nb\n', 'a\nb\n')).toEqual({ text: '', added: 0, removed: 0 })
  })

  it('shows a changed line with its context and counts', () => {
    const from = ['1', '2', '3', '4', 'old', '6', '7', '8', '9'].join('\n')
    const to = ['1', '2', '3', '4', 'new', '6', '7', '8', '9'].join('\n')
    expect(lineDiff(from, to)).toEqual({ text: ['@@ -2,7 +2,7 @@', ' 2', ' 3', ' 4', '-old', '+new', ' 6', ' 7', ' 8'].join('\n'), added: 1, removed: 1 })
  })

  it('keeps changes far apart in hunks of their own, numbered from the right lines', () => {
    const from = Array.from({ length: 30 }, (_, i) => `l${i}`)
    const to = [...from]
    to.splice(2, 0, 'added')
    to[20] = 'changed'
    const d = lineDiff(from.join('\n'), to.join('\n'))
    const heads = d.text.split('\n').filter((l) => l.startsWith('@@'))
    expect(heads).toEqual(['@@ -1,5 +1,6 @@', '@@ -17,7 +18,7 @@'])
    expect(d.added).toBe(2)
    expect(d.removed).toBe(1)
  })

  it('matches lines across an insertion, not line by line', () => {
    expect(lineDiff('a\nb\nc', 'a\nx\nb\nc').text.split('\n').slice(1)).toEqual([' a', '+x', ' b', ' c'])
  })

  it('handles an empty side', () => {
    expect(lineDiff('', 'a').added).toBe(1)
    expect(lineDiff('a', '').removed).toBe(1)
  })
})
