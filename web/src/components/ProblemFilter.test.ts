import { describe, expect, it } from 'vitest'
import { problemLevel, showsNode } from './ProblemFilter'

describe('the nodes’ problem filter', () => {
  it('reads err, and err=1 from the old checkbox as errors', () => {
    expect(problemLevel(null)).toBe('all')
    expect(problemLevel('problems')).toBe('problems')
    expect(problemLevel('errors')).toBe('errors')
    expect(problemLevel('1')).toBe('errors')
    expect(problemLevel('nonsense')).toBe('all')
  })

  it('shows a node with only warnings among the problems, not the errors', () => {
    const healthy = { errors: 0, warnings: 0 }
    const cordoned = { errors: 0, warnings: 1 }
    const down = { errors: 1, warnings: 2 }
    expect([healthy, cordoned, down].filter((r) => showsNode('all', r))).toHaveLength(3)
    expect([healthy, cordoned, down].filter((r) => showsNode('problems', r))).toEqual([cordoned, down])
    expect([healthy, cordoned, down].filter((r) => showsNode('errors', r))).toEqual([down])
  })
})
