import { describe, expect, it } from 'vitest'
import { banners } from './Banners'
import type { ClusterSummary } from '../api/clusters'
import type { ClusterInfo } from '../api/types'

const now = Date.UTC(2026, 9, 13)
const summary: ClusterSummary = {
  id: 'dev',
  name: 'dev',
  env: 'development',
  server: 'https://dev:6443',
  ca: 'pem',
  auth: 'token',
  mode: 'read',
  cred: { who: 'system:serviceaccount:kube-system:cluster-console', set: '2026-10-01T00:00:00Z', expires: '2026-10-15T00:00:00Z' },
  added: '2026-10-01T00:00:00Z',
  updated: '2026-10-01T00:00:00Z',
  state: 'connected',
  nodes: 3,
  pods: 20,
  synced: true,
  expiring: true,
}
const cluster: ClusterInfo = { name: 'dev', version: 'v1.37.1', mode: 'token', state: 'connected', since: '2026-10-13T00:00:00Z', synced: true, warnings: [], metrics: 'available', uptime: 'available', secrets: 'read' }
const page = '#/c/dev/cluster'

describe('banners', () => {
  it('warns about credentials that expire soon while the cluster is connected', () => {
    const list = banners(cluster, 'open', summary, page, now)
    expect(list.map((b) => [b.tone, b.link?.label])).toEqual([['warn', 'Fix the connection']])
    expect(list[0]?.detail).toContain('expires in 2 days')
  })

  it('shows one banner for a cluster it cannot reach, naming expired credentials as the cause when they are', () => {
    const away: ClusterInfo = { ...cluster, state: 'disconnected', message: 'Unauthorized.' }
    const soon = banners(away, 'open', summary, page, now)
    expect(soon.map((b) => [b.tone, b.link?.label])).toEqual([['err', 'About the connection']])
    expect(soon[0]?.title).toBe('Cannot reach the Kubernetes API server')
    expect(soon[0]?.detail).toContain('Unauthorized.')

    const expired = banners(away, 'open', { ...summary, expiring: false, expired: true, cred: { ...summary.cred, expires: '2026-10-12T00:00:00Z' } }, page, now)
    expect(expired.map((b) => [b.tone, b.link?.label])).toEqual([['err', 'Fix the connection']])
    expect(expired[0]?.title).toContain('have expired')
    expect(expired[0]?.detail).toContain('expired 24 hours ago')
    expect(expired[0]?.detail).toContain('Unauthorized.')
  })

  it('keeps partial-data and access warnings for a connected cluster only', () => {
    const partial = { ...cluster, warnings: ['Jobs cannot be watched: forbidden.'] }
    expect(banners(partial, 'open', null, page, now, 'The API server did not answer.').map((b) => b.title)).toEqual(['Nothing can be shown to you yet', 'Partial data'])
    const away = { ...partial, state: 'disconnected' as const }
    expect(banners(away, 'open', null, page, now, 'The API server did not answer.').map((b) => b.title)).toEqual(['Cannot reach the Kubernetes API server'])
  })
})
