import { describe, expect, it } from 'vitest'
import { credentialNote, jwtClaims, jwtWho, kubeconfigCA, kubeContext, parseKubeconfig, rbacYAML } from './credentials'

function jwt(claims: Record<string, unknown>): string {
  const enc = (o: unknown) => btoa(JSON.stringify(o)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return `${enc({ alg: 'RS256' })}.${enc(claims)}.c2ln`
}

const KUBECONFIG = `apiVersion: v1
kind: Config
clusters:
  - name: dev
    cluster:
      server: https://dev.example:6443
      certificate-authority-data: ${btoa('-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n')}
  - name: prod
    cluster:
      server: https://prod.example:6443
contexts:
  - name: dev
    context:
      cluster: dev
      user: dev-admin
  - name: prod
    context:
      cluster: prod
      user: sso
current-context: dev
users:
  - name: dev-admin
    user:
      token: abc
  - name: sso
    user:
      exec:
        apiVersion: client.authentication.k8s.io/v1
        command: kubelogin
`

describe('tokens', () => {
  it('reads the claims and names the account', () => {
    const t = jwt({ sub: 'system:serviceaccount:kube-system:cluster-console', exp: 4102444800 })
    expect(jwtWho(jwtClaims(t))).toBe('kube-system/cluster-console')
    expect(jwtClaims('not.a')).toBeNull()
    expect(jwtWho(jwtClaims(jwt({ sub: 'alice' })))).toBe('alice')
  })

  it('notes what a token says', () => {
    const base = { name: 'x', env: 'development' as const, description: '', server: '', ca: 'system' as const, auth: 'token' as const, mode: 'read' as const }
    expect(credentialNote({ ...base, token: '' })).toBeNull()
    expect(credentialNote({ ...base, token: 'nope' })?.tone).toBe('err')
    const now = Date.UTC(2026, 9, 13)
    expect(credentialNote({ ...base, token: jwt({ sub: 'system:serviceaccount:kube-system:cluster-console', exp: 4102444800 }) }, now)).toEqual({ tone: 'ok', text: 'Token for kube-system/cluster-console, valid until 2100-01-01.' })
    expect(credentialNote({ ...base, token: jwt({ sub: 'x', exp: 1 }) }, now)?.text).toContain('expired on 1970-01-01')
    expect(credentialNote({ ...base, token: jwt({ sub: 'x' }) }, now)?.text).toContain('with no expiry')
  })
})

describe('kubeconfigs', () => {
  it('finds contexts, clusters, users and exec plugins', () => {
    const k = parseKubeconfig(KUBECONFIG)
    expect(k.current).toBe('dev')
    expect(k.contexts.map((c) => c.name)).toEqual(['dev', 'prod'])
    const dev = kubeContext(k, '')
    expect(dev.name).toBe('dev')
    expect(dev.cluster?.server).toBe('https://dev.example:6443')
    expect(dev.user?.exec).toBeUndefined()
    const prod = kubeContext(k, 'prod')
    expect(prod.user?.exec).toBe('yes')
    expect(prod.user?.command).toBe('kubelogin')
    expect(kubeconfigCA(k, 'dev')).toContain('BEGIN CERTIFICATE')
    expect(kubeconfigCA(k, 'prod')).toBe('')
  })

  it('notes what a kubeconfig says', () => {
    const base = { name: 'x', env: 'development' as const, description: '', server: 'https://dev.example:6443', ca: 'pem' as const, auth: 'kubeconfig' as const, mode: 'read' as const }
    expect(credentialNote({ ...base, kubeconfig: KUBECONFIG })).toEqual({ tone: 'ok', text: 'Context dev: user dev-admin on cluster dev.' })
    expect(credentialNote({ ...base, kubeconfig: KUBECONFIG, context: 'prod' })?.text).toContain('exec plugin (kubelogin)')
    expect(credentialNote({ ...base, kubeconfig: KUBECONFIG, context: 'qa' })?.text).toBe('The kubeconfig has no context “qa”. It has dev and prod.')
    expect(credentialNote({ ...base, kubeconfig: KUBECONFIG, server: 'https://other:6443' })).toMatchObject({ tone: 'warn' })
    expect(credentialNote({ ...base, kubeconfig: 'apiVersion: v1\nkind: Config\n' })?.text).toBe('That kubeconfig has no contexts.')
  })
})

describe('the RBAC to grant', () => {
  it('reads only, or also changes, and impersonates', () => {
    const read = rbacYAML('read', true)
    expect(read).toContain('name: cluster-console-read')
    expect(read).toContain('verbs: ["impersonate"]')
    expect(read).not.toContain('pods/eviction')
    const write = rbacYAML('write', false)
    expect(write).toContain('name: cluster-console-operate')
    expect(write).toContain('pods/eviction')
    expect(write).not.toContain('impersonate')
    // Watson's storage changes (ST-18), for a console with sign-in off.
    expect(write).toContain('resources: ["persistentvolumeclaims"]')
    expect(write).toContain('resources: ["storageclasses"]')
    expect(read).not.toContain('persistentvolumeclaims')
    for (const line of read.split('\n')) expect(line.length).toBeLessThanOrEqual(72)
  })
})
