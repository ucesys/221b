import { describe, expect, it } from 'vitest'
import type { HookRow, PolicyNSRow, RolloutRow } from '../api/types'
import { findings, flagOf, modelOf, sectionSub } from './policies'

const psa = { level: 'baseline', version: 'latest', fail: [], warnings: [] }
const ns = (name: string, o: Partial<PolicyNSRow> = {}): PolicyNSRow => ({ key: `ns:${name}`, name, pods: 3, hostNetwork: 0, policies: 0, isolatedIn: 0, isolatedOut: 0, open: ['api'], baseline: psa, restricted: { ...psa, level: 'restricted' }, quotas: [], limitRanges: [], budgets: 0, ...o })

describe('policies', () => {
  it('finds open namespaces, stalled rollouts and unreachable webhooks', () => {
    const rollout: RolloutRow = { key: 'rollout:shop/api', namespace: 'shop', name: 'api', replicas: 1, strategy: 'RollingUpdate', surge: 1, unavailable: 0, requests: {}, limits: {}, short: [{ quota: 'q', resource: 'requests.memory', need: '512Mi', left: '256Mi' }], stalls: true }
    const hook = { key: 'hook:Validating/x/w.example', kind: 'Validating', config: 'x', webhook: 'w.example', rules: ['pods (core) · CREATE'], service: 'ns/svc:443', failurePolicy: 'Fail', timeout: 10, namespaces: 'all namespaces', objects: 'every object', caBundle: true, reach: { state: 'noEndpoints', ready: 0, total: 0, text: 'no endpoints', tone: 'err', pods: [] } } as HookRow
    const m = modelOf([ns('shop'), ns('kube-system', { system: true }), ns('safe', { isolatedIn: 3, enforce: 'baseline' }), rollout, hook])
    const fs = findings(m)
    expect(fs.map((f) => [f.id, f.severity])).toEqual([
      ['hook-hook:Validating/x/w.example', 'high'],
      ['np-open', 'medium'],
      ['quota-rollout:shop/api', 'medium'],
      ['psa-shop', 'low'],
    ])
    expect(fs[1]!.title).toBe('shop accepts connections from any pod')
    expect(fs[1]!.text).toContain('safe shows the pattern to copy')
    expect(flagOf(fs)).toEqual({ count: 4, tone: 'err' })
    expect(sectionSub('netpol', m, fs)).toBe('0 policies · 1 of 2 app namespaces isolated')
    expect(sectionSub('psa', m, fs)).toBe('baseline or stricter in 1 of 3 namespaces')
  })
})
