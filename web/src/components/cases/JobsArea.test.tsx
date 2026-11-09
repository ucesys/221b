import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '../../api/jobs'
import { parseHash } from '../../lib/router'
import { JobsArea } from './JobsArea'
import { caseHref, jobHref, jobsSub, pathId, runHref } from './jobsRoute'

vi.mock('../../api/jobs', async (original) => ({
  ...(await original<typeof import('../../api/jobs')>()),
  fetchJobs: vi.fn(),
  fetchRuns: vi.fn(),
  fetchRun: vi.fn(),
}))

describe('Cases › Jobs: Schedule and Runs (FX-13)', () => {
  beforeEach(() => {
    vi.mocked(api.fetchJobs).mockResolvedValue({ jobs: [], kinds: [], clusters: [], settings: {}, mayEdit: false, admin: false, timeZone: 'UTC' })
    vi.mocked(api.fetchRuns).mockResolvedValue({ runs: [], total: 0 })
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
    window.location.hash = ''
  })

  it('reads the sub-tab and the open job or run from the address', () => {
    expect(jobsSub(parseHash('#/cases/jobs').path)).toBe('schedule')
    expect(jobsSub(parseHash('#/cases/jobs/nightly-audit').path)).toBe('schedule')
    expect(pathId(parseHash('#/cases/jobs/nightly-audit').path, 'jobs')).toBe('nightly-audit')
    expect(jobsSub(parseHash('#/cases/jobs/runs').path)).toBe('runs')
    expect(pathId(parseHash('#/cases/jobs/runs').path, 'jobs')).toBe('')
    expect(pathId(parseHash('#/cases/jobs/runs/run-4').path, 'runs')).toBe('run-4')
    // An old link to a run opens it under Jobs › Runs.
    expect(pathId(parseHash('#/cases/runs/run-4').path, 'runs')).toBe('run-4')
    expect(runHref('run-4')).toBe('#/cases/jobs/runs/run-4')
    expect(jobHref('nightly-audit')).toBe('#/cases/jobs/nightly-audit')
    expect(caseHref('C-12')).toBe('#/cases/inbox/case/12')
  })

  it('shows the Schedule at #/cases/jobs and the Runs at #/cases/jobs/runs, and switches between them', async () => {
    const { unmount } = render(<JobsArea route={parseHash('#/cases/jobs')} />)
    expect(await screen.findByTestId('jobs-tab')).toBeTruthy()
    expect(screen.getByTestId('jobs-sub-schedule').getAttribute('aria-pressed')).toBe('true')
    fireEvent.click(screen.getByTestId('jobs-sub-runs'))
    await waitFor(() => expect(window.location.hash).toBe('#/cases/jobs/runs'))
    unmount()
    render(<JobsArea route={parseHash('#/cases/runs')} />)
    expect(await screen.findByTestId('runs-tab')).toBeTruthy()
    expect(screen.getByTestId('jobs-sub-runs').getAttribute('aria-pressed')).toBe('true')
  })
})
