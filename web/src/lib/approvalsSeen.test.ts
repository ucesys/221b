import { describe, expect, it } from 'vitest'
import type { ApprovalRow } from '../api/automation'
import { newestSeen, unseen } from './approvalsSeen'

const row = (over: Partial<ApprovalRow>): ApprovalRow => ({ kind: 'script', state: 'approved', what: 's v1', requestedBy: 'Pat', ...over })

describe('approvals seen', () => {
  const items = [
    row({ state: 'pending', requestedAt: '2026-11-03T14:00:00Z' }),
    row({ state: 'pending', kind: 'command', own: true, requestedAt: '2026-11-03T14:30:00Z' }),
    row({ mine: true, approvedAt: '2026-11-03T10:00:00Z' }),
    row({ state: 'rejected', mine: true, approvedAt: '2026-11-03T12:00:00Z' }),
    row({ mine: false, approvedAt: '2026-11-03T13:00:00Z' }),
  ]
  it('flags new reviews to approve and decisions on what the person asked for, after the mark', () => {
    expect(unseen(items, '2026-11-03T11:00:00Z')).toEqual({ waiting: 1, decided: 1 })
    expect(unseen(items, '2026-11-03T13:30:00Z')).toEqual({ waiting: 1, decided: 0 })
    expect(unseen(items, '2026-11-03T14:00:00Z')).toEqual({ waiting: 0, decided: 0 })
  })
  it('flags nothing before anything was seen', () => {
    expect(unseen(items, null)).toEqual({ waiting: 0, decided: 0 })
  })
  it('takes the newest row as the mark, a review asked for included', () => {
    expect(newestSeen(items)).toBe('2026-11-03T14:30:00Z')
    expect(newestSeen([])).toBe('')
  })
})
