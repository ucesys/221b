// The phase 12 review's findings from the screenshots: names where ids
// stood (U-11, U-12), and the styles that keep a case's second lines and
// a table's object apart from what is beside them (U-10, U-15).
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as api from '../../api/jobs'
import type { Route } from '../../lib/router'
import { keyClusterName } from './CaseDrawer'
import { RunsTab } from './RunsTab'

vi.mock('../../api/jobs', async (original) => ({
  ...(await original<typeof import('../../api/jobs')>()),
  fetchRuns: vi.fn(),
  fetchJobs: vi.fn(),
}))

const route: Route = { cluster: 'local', view: 'tabs', tab: 'alerts', path: ['runs'], params: new URLSearchParams() }

const brief = {
  id: 'run-3',
  job: 'nightly-audit',
  label: 'Nightly audit',
  agent: 'auditor',
  clusters: ['local'],
  trigger: { kind: 'schedule', what: 'schedule' },
  started: '2026-11-09T18:48:00Z',
  ended: '2026-11-09T18:49:00Z',
  status: 'ok',
  findings: 1,
  proposals: 0,
  costUSD: 0.06,
} as unknown as api.RunBrief

const jobs = { jobs: [], kinds: [{ id: 'nightly-audit', label: 'Nightly audit', agent: 'auditor', agentName: 'Auditor' }], clusters: [{ id: 'local', name: 'simulated' }] } as unknown as api.JobsView

describe('names where a run carries ids (U-11, U-12)', () => {
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('shows the agent and the cluster by name in the Runs table', async () => {
    vi.mocked(api.fetchRuns).mockResolvedValue({ runs: [brief], total: 1 })
    vi.mocked(api.fetchJobs).mockResolvedValue(jobs)
    render(<RunsTab route={route} />)
    const row = await screen.findByTestId('run-row')
    await waitFor(() => expect(row.textContent).toContain('Auditor · simulated'))
    expect(row.textContent).not.toContain('auditor · local')
  })

  it('keeps the ids for whoever may not read the jobs', async () => {
    vi.mocked(api.fetchRuns).mockResolvedValue({ runs: [brief], total: 1 })
    vi.mocked(api.fetchJobs).mockRejectedValue(new Error('403'))
    render(<RunsTab route={route} />)
    const row = await screen.findByTestId('run-row')
    expect(row.textContent).toContain('auditor · local')
  })

  it('names the cluster of a case’s key, and leaves another cluster’s id as it is', () => {
    const key = { cluster: 'local', object: 'Namespace/analytics', check: 'netpol.missing' }
    expect(keyClusterName({ key, cluster: 'local', clusterName: 'simulated' })).toBe('simulated')
    expect(keyClusterName({ key, cluster: 'local' })).toBe('local')
    expect(keyClusterName({ key, cluster: 'other', clusterName: 'Other' })).toBe('local')
  })
})

describe('styles that keep parts apart (U-10, U-15)', () => {
  const css = readFileSync(join(__dirname, '..', '..', 'styles', 'cases.css'), 'utf8')
  const rule = (selector: string) => css.slice(css.indexOf(selector)).split('}')[0]

  it('puts a case’s second lines on a line of their own', () => {
    expect(css).toContain('.cs-kv dd .tb-sub')
    expect(rule('.cs-sec .tb-sub')).toContain('display: block')
  })

  it('ends a long object in a table’s cell with an ellipsis', () => {
    expect(rule('.cs-obj-clip .cs-obj > :first-child')).toContain('text-overflow: ellipsis')
  })
})
