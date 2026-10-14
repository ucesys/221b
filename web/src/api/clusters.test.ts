import { describe, expect, it } from 'vitest'
import { credText, expiryText, spanText, stateOf, type ClusterSummary } from './clusters'

const base: ClusterSummary = {
  id: 'dev',
  name: 'dev',
  env: 'development',
  server: 'https://dev:6443',
  ca: 'pem',
  auth: 'token',
  mode: 'read',
  cred: { who: 'system:serviceaccount:kube-system:cluster-console', set: '2026-10-01T00:00:00Z', expires: '2026-10-20T00:00:00Z' },
  added: '2026-10-01T00:00:00Z',
  updated: '2026-10-01T00:00:00Z',
  state: 'connected',
  nodes: 3,
  pods: 20,
  synced: true,
}
const now = Date.UTC(2026, 9, 13)

describe('connection summaries', () => {
  it('spells spans and expiry', () => {
    expect(spanText(5 * 60_000)).toBe('5 minutes')
    expect(spanText(3 * 3_600_000)).toBe('3 hours')
    expect(spanText(7 * 86_400_000)).toBe('7 days')
    expect(spanText(400 * 86_400_000)).toBe('13 months')
    expect(spanText(10 * 365 * 86_400_000)).toBe('10 years')
    expect(expiryText(base.cred, now)).toBe('expires in 7 days')
    expect(expiryText({ ...base.cred, expires: '2026-10-12T00:00:00Z' }, now)).toBe('expired 24 hours ago')
    expect(expiryText({ set: '' }, now)).toBe('')
  })

  it('describes credentials by method', () => {
    expect(credText(base, now)).toBe('Token for system:serviceaccount:kube-system:cluster-console · saved 12 days ago · expires in 7 days')
    expect(credText({ auth: 'kubeconfig', cred: { context: 'dev', who: 'admin', set: '' } }, now)).toBe('Kubeconfig, context dev (user admin)')
    expect(credText({ auth: 'incluster', cred: { set: '' } }, now)).toContain('own ServiceAccount')
  })

  it('states the connection', () => {
    expect(stateOf(base)).toEqual({ tone: 'ok', text: 'Connected' })
    expect(stateOf({ ...base, expiring: true })).toMatchObject({ tone: 'warn' })
    expect(stateOf({ ...base, expired: true })).toMatchObject({ tone: 'err' })
    expect(stateOf({ ...base, state: 'failed' })).toEqual({ tone: 'err', text: 'Can’t start' })
    expect(stateOf({ ...base, synced: false })).toEqual({ tone: 'info', text: 'Listing' })
  })
})
