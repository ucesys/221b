import { describe, expect, it } from 'vitest'
import type { LocalGroup, UserRow } from '../api/admin'
import { userTerms } from '../columns/admin'
import { groupChoice, holdingParts, topRole } from './admin'

// Roles review, finding 15: the Users API names the fifth role, reporter,
// in holdings and local groups; the page knew four, so a search threw.
describe('the Reporter role', () => {
  const user = {
    id: 'local:ann',
    username: 'ann',
    source: 'local',
    status: 'active',
    holdings: [{ role: 'reporter', via: 'staff' }],
    groups: ['staff'],
    superRoles: [],
    managed: false,
    kubeName: '221b-local:ann',
  } as unknown as UserRow
  it('is named', () => {
    expect(holdingParts(user.holdings[0]!).role).toBe('Reporter')
    expect(groupChoice({ name: 'staff', role: 'reporter', namespaces: [], members: 1 } as LocalGroup)).toBe('staff · Reporter · no namespaces')
    expect(topRole(user.holdings)).toBe('reporter')
  })
  it('can be searched for', () => {
    const terms = userTerms(user)
    expect(terms.every((t) => typeof t.text === 'string')).toBe(true)
    expect(terms.map((t) => t.text)).toContain('Reporter')
    expect(() => terms.map((t) => t.text.toLowerCase())).not.toThrow()
  })
})
