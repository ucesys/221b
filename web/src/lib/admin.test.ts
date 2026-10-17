import { describe, expect, it } from 'vitest'
import type { Drift, LocalGroup, PermRole, UserRow } from '../api/admin'
import { afterJoining, permRowsOf, roleTotalsText, driftText, groupChoice, holdingText, isRead, joinable, passwordProblem, permTotals, roleRank, splitRule, topRole, totalsText, usernameProblem } from './admin'

const groups: LocalGroup[] = [
  { name: 'web-readers', role: 'viewer', namespaces: ['web'], members: 1 },
  { name: 'checkout-ops', role: 'admin', namespaces: ['checkout', 'web'], members: 2 },
]

describe('roles held', () => {
  it('shows the highest role and ranks people by it', () => {
    expect(topRole([{ role: 'viewer' }, { role: 'admin' }])).toBe('admin')
    expect(topRole([{ role: 'viewer' }, { role: 'superViewer' }])).toBe('superViewer')
    expect(topRole([])).toBeNull()
    expect(roleRank([{ role: 'superAdmin' }])).toBeLessThan(roleRank([{ role: 'admin' }]))
    expect(roleRank([])).toBeGreaterThan(roleRank([{ role: 'viewer' }]))
  })

  it('writes a holding as a person would say it', () => {
    expect(holdingText({ role: 'admin', via: 'checkout-ops', namespaces: ['checkout', 'web'] })).toBe('Admin via checkout-ops checkout, web')
    expect(holdingText({ role: 'superAdmin' })).toBe('SuperAdmin')
  })
})

describe('permissions', () => {
  const role: PermRole = {
    id: 'admin',
    name: 'Admin',
    clusterRole: '221b-admin',
    definition: 'full',
    present: true,
    rules: [
      { group: '', resource: 'pods', verbs: ['get', 'list', 'watch'], kind: 'read', scope: 'bound', flags: [] },
      { group: 'apps', resource: 'deployments', verbs: ['get'], kind: 'read', scope: 'bound', flags: [] },
      { group: 'apps', resource: 'deployments', verbs: ['patch'], kind: 'write', scope: 'bound', flags: [] },
    ],
  }

  it('counts reads and changes as kinds', () => {
    expect(permTotals(role)).toEqual({ reads: 2, writes: 1, kinds: 2 })
    expect(totalsText({ reads: 40, writes: 0 })).toBe('reads 40 kinds, changes 0')
    expect(totalsText({ reads: 1, writes: 2 })).toBe('reads 1 kind, changes 2')
  })

  it('splits a rule with both reads and changes into two', () => {
    const parts = splitRule({ group: 'apps', resource: 'deployments', verbs: ['get', 'list', 'patch', 'delete'], scope: 'bound', flags: [] })
    expect(parts.map((p) => [p.kind, p.verbs])).toEqual([
      ['read', ['get', 'list']],
      ['write', ['patch', 'delete']],
    ])
    expect(splitRule({ group: '', resource: 'pods', verbs: ['get'], scope: 'bound', flags: [] })).toHaveLength(1)
    expect(isRead(['get', 'watch'])).toBe(true)
    expect(isRead(['get', 'delete'])).toBe(false)
    expect(isRead([])).toBe(false)
  })

  it('says whether the live roles match', () => {
    const d = (state: Drift['state']): Drift => ({ state, roles: state === 'differs' ? [{ id: 'admin', differences: ['adds delete on nodes'] }] : [] })
    expect(driftText(d('match')).tone).toBe('ok')
    expect(driftText(d('differs')).text).toContain('Admin')
    expect(driftText(d('missing')).tone).toBe('warn')
    expect(driftText(d('unknown')).tone).toBe('muted')
  })
})

describe('local users', () => {
  const bob: Pick<UserRow, 'groups' | 'superRoles'> = { groups: ['web-readers'], superRoles: [] }

  it('shows what joining a group gives, from values', () => {
    expect(afterJoining(bob, ['checkout-ops'], groups)).toEqual([
      { role: 'viewer', via: 'web-readers', namespaces: ['web'] },
      { role: 'admin', via: 'checkout-ops', namespaces: ['checkout', 'web'] },
    ])
    expect(afterJoining({ groups: [], superRoles: ['superViewer'] }, [], groups)).toEqual([{ role: 'superViewer' }])
  })

  it('offers only the groups a person is not in', () => {
    expect(joinable(bob, groups).map((g) => g.name)).toEqual(['checkout-ops'])
    expect(groupChoice(groups[1]!)).toBe('checkout-ops · Admin · checkout, web')
  })

  it('checks names and passwords as the server does (RB-16, RB-17, RB-46)', () => {
    expect(usernameProblem('')).not.toBe('')
    expect(usernameProblem('Alice')).toContain('lowercase')
    expect(usernameProblem('system:masters')).toBe('This name is reserved.')
    expect(usernameProblem('221b:superadmins')).toBe('This name is reserved.')
    expect(usernameProblem('alice.o-1_x')).toBe('')
    expect(passwordProblem('short', 'alice')).toContain('12')
    expect(passwordProblem('alicealicealice', 'alice')).toBe('')
    expect(passwordProblem('ALICE', 'alice')).toContain('12')
    expect(passwordProblem('a-long-enough-password', 'alice')).toBe('')
  })
})

describe('the permission rows', () => {
  it('has a row per role, resource and access, with where the role has it', () => {
    const rows = permRowsOf([
      {
        id: 'viewer',
        rules: [{ group: 'apps', resource: 'deployments', verbs: ['get', 'list', 'watch'], kind: 'read', scope: 'bound', flags: [] }],
      },
      {
        id: 'superAdmin',
        rules: [
          { group: 'apps', resource: 'deployments', verbs: ['watch', 'get', 'list', 'patch', 'delete'], kind: 'write', scope: 'all', flags: [] },
          { group: '', resource: 'secrets', verbs: ['get'], kind: 'read', scope: 'all', flags: ['Secrets'] },
        ],
      },
    ])
    expect(rows.map((r) => r.key)).toEqual(['superAdmin|read:apps/deployments', 'superAdmin|read:/secrets', 'superAdmin|write:apps/deployments', 'viewer|read:apps/deployments'])
    expect(rows[0]!.verbs).toEqual(['get', 'list', 'watch'])
    expect(rows[2]!.verbs).toEqual(['patch', 'delete'])
    expect(rows[3]!.scope).toBe('bound')
    expect(rows[1]!.flags).toEqual(['Secrets'])
  })

  it('says a wildcard role reads and changes everything rather than counting kinds', () => {
    expect(roleTotalsText({ rules: [{ group: '*', resource: '*', verbs: ['*'], kind: 'write', scope: 'all', flags: ['wildcard'] }] })).toBe('reads and changes everything')
    expect(roleTotalsText({ rules: [{ group: '*', resource: '*', verbs: ['get', 'list', 'watch'], kind: 'read', scope: 'all', flags: ['wildcard'] }] })).toBe('reads everything, changes 0')
    expect(roleTotalsText({ rules: [{ group: '', resource: 'pods', verbs: ['get'], kind: 'read', scope: 'bound', flags: [] }] })).toBe('reads 1 kind, changes 0')
  })
})
