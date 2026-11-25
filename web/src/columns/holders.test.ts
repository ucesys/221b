import { describe, expect, it } from 'vitest'
import type { RoleRow } from '../api/admin'
import { holdersText } from './admin'

// Roles review, cut list: the super roles' holders are a SuperAdmin's to
// see; anyone else was told "0 groups".
describe('the holders of a super role', () => {
  const row = { id: 'superAdmin', subjects: [] } as unknown as RoleRow
  it('are not counted for someone who may not see them', () => {
    expect(holdersText(row)).toBe('')
  })
  it('are counted for a SuperAdmin', () => {
    expect(holdersText({ ...row, holders: { groups: 0, users: 2 } })).toBe('0 groups · 2 users')
  })
})
