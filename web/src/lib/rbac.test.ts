import { describe, expect, it } from 'vitest'
import type { BindingRow, RoleRow, SARow } from '../api/types'
import { canI, canICommand, check, grantsOf, identities, roleIndex, ruleAllows, summary, whoCan } from './rbac'

const src = { kind: 'manual' as const }
const role = (name: string, rules: RoleRow['rules'], namespace?: string): RoleRow => ({ key: `role:${namespace ? 'Role/' + namespace + '/' : 'ClusterRole/'}${name}`, kind: namespace ? 'Role' : 'ClusterRole', namespace, name, rules, powers: [], source: src })
const bind = (name: string, roleKind: 'ClusterRole' | 'Role', roleName: string, subjects: BindingRow['subjects'], namespace?: string): BindingRow => ({
  key: `binding:${namespace ? 'RoleBinding/' + namespace + '/' : 'ClusterRoleBinding/'}${name}`,
  kind: namespace ? 'RoleBinding' : 'ClusterRoleBinding',
  namespace,
  name,
  roleKind,
  roleName,
  subjects,
  source: src,
  findings: [],
})

const roles = roleIndex([
  role('reader', [{ apiGroups: [''], resources: ['pods', 'pods/log'], verbs: ['get', 'list'] }]),
  role('named', [{ apiGroups: [''], resources: ['secrets'], verbs: ['get'], resourceNames: ['tls'] }]),
  role('deployer', [{ apiGroups: ['apps'], resources: ['deployments'], verbs: ['create'] }], 'web'),
  role('discovery', [{ verbs: ['get'], nonResourceURLs: ['/api'] }]),
])
const bindings = [
  bind('readers', 'ClusterRole', 'reader', [{ kind: 'Group', name: 'ops' }]),
  bind('tls', 'ClusterRole', 'named', [{ kind: 'Group', name: 'ops' }], 'web'),
  bind('deployer', 'Role', 'deployer', [{ kind: 'ServiceAccount', name: 'ci', namespace: 'web' }, { kind: 'User', name: 'dana' }], 'web'),
  bind('auth', 'ClusterRole', 'reader', [{ kind: 'Group', name: 'system:serviceaccounts:batch' }]),
]

describe('rbac', () => {
  it('matches rules as the API server does', () => {
    const r = { apiGroups: [''], resources: ['pods'], verbs: ['create'] }
    expect(ruleAllows(r, { verb: 'create', group: '', resource: 'pods' })).toBe(true)
    expect(ruleAllows(r, { verb: 'create', group: '', resource: 'pods', sub: 'exec' })).toBe(false)
    expect(ruleAllows({ apiGroups: ['*'], resources: ['*/scale'], verbs: ['*'] }, { verb: 'get', group: 'apps', resource: 'deployments', sub: 'scale' })).toBe(true)
    expect(ruleAllows({ apiGroups: [''], resources: ['secrets'], verbs: ['get'], resourceNames: ['tls'] }, { verb: 'get', group: '', resource: 'secrets' })).toBe(false)
    expect(ruleAllows({ apiGroups: [''], resources: ['secrets'], verbs: ['get'], resourceNames: ['tls'] }, { verb: 'get', group: '', resource: 'secrets', name: 'tls' })).toBe(true)
  })

  it('answers per namespace, everywhere, or for named objects only', () => {
    const ops = { kind: 'Group' as const, name: 'ops' }
    expect(check(grantsOf(ops, bindings), 'get', 'pods/log', '*', roles).level).toBe('all')
    expect(check(grantsOf(ops, bindings), 'get', 'secrets', 'web', roles)).toMatchObject({ level: 'named', names: ['tls'] })
    const ci = { kind: 'ServiceAccount' as const, name: 'ci', namespace: 'web' }
    expect(check(grantsOf(ci, bindings), 'create', 'deployments', '*', roles)).toMatchObject({ level: 'some', namespaces: ['web'] })
    expect(canI(ci, 'create', 'deployments', 'web', bindings, roles)).toBe(true)
    expect(canI(ci, 'create', 'deployments', '*', bindings, roles)).toBe(false)
    expect(canI(ci, 'create', 'deployments', 'batch', bindings, roles)).toBe(false)
    // Through the group Kubernetes puts every ServiceAccount of a namespace in.
    expect(canI({ kind: 'ServiceAccount', name: 'job', namespace: 'batch' }, 'list', 'pods', 'web', bindings, roles)).toBe(true)
    expect(canI({ kind: 'Group', name: 'system:masters' }, 'delete', 'nodes', '*', [], roles)).toBe(true)
  })

  it('lists who can, by the subjects bindings name', () => {
    const rows = whoCan('get', 'secrets', 'web', bindings, roles, () => false)
    expect(rows.map((r) => [r.key, r.named, r.wide])).toEqual([['Group:ops', ['tls'], false]])
    const deploy = whoCan('create', 'deployments', '*', bindings, roles, () => false)
    expect(deploy.map((r) => r.key).sort()).toEqual(['ServiceAccount:web/ci', 'User:dana'])
    expect(deploy[0]!.through).toEqual(['web/deployer → deployer'])
  })

  it('finds identities and sums them up', () => {
    const sas: SARow[] = [
      { key: 'sa:web/ci', namespace: 'web', name: 'ci', pods: [], tokens: [] },
      { key: 'sa:web/idle', namespace: 'web', name: 'idle', pods: [], tokens: [] },
      { key: 'sa:web/runner', namespace: 'web', name: 'runner', pods: [{ name: 'p', status: 'Running', tone: 'ok' }], tokens: [] },
    ]
    const ids = identities(bindings, sas)
    expect(ids.map((i) => i.key).sort()).toEqual(['Group:ops', 'Group:system:serviceaccounts:batch', 'ServiceAccount:web/ci', 'ServiceAccount:web/runner', 'User:dana'])
    const ops = ids.find((i) => i.key === 'Group:ops')!
    expect(summary(ops)).toBe('reader everywhere · named in web')
    expect(ids.find((i) => i.key === 'Group:system:serviceaccounts:batch')!.system).toBe(true)
    expect(summary(ids.find((i) => i.key === 'ServiceAccount:web/runner')!)).toBe('no bindings: only what every service account has')
  })

  it('writes the kubectl command that checks an answer', () => {
    expect(canICommand({ kind: 'ServiceAccount', name: 'ci', namespace: 'web' }, 'create', 'pods/exec', 'web')).toBe('kubectl auth can-i create pods --subresource=exec -n web --as=system:serviceaccount:web:ci')
    expect(canICommand({ kind: 'Group', name: 'ops' }, 'delete', 'nodes', '*')).toBe('kubectl auth can-i delete nodes --as=probe --as-group=ops')
  })
})
