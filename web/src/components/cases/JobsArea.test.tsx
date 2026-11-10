import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '../../api/jobs'
import { parseHash } from '../../lib/router'
import { JobsArea } from './JobsArea'
import { caseHref, jobHref, jobsSub, pathId, ruleSection, rulesHref, runHref } from './jobsRoute'
import { MYCROFT_TABLE_MIN } from './mycroftTable'

vi.mock('../../api/jobs', async (original) => ({
  ...(await original<typeof import('../../api/jobs')>()),
  fetchJobs: vi.fn(),
  fetchRuns: vi.fn(),
  fetchRun: vi.fn(),
}))
vi.mock('../../api/rules', async (original) => ({
  ...(await original<typeof import('../../api/rules')>()),
  fetchRules: vi.fn(() => Promise.resolve({ rules: [], found: [], suggestions: [], jobs: [], types: [], mayChange: true })),
}))
vi.mock('../../api/lessons', async (original) => ({
  ...(await original<typeof import('../../api/lessons')>()),
  fetchExceptions: vi.fn(() => Promise.resolve({ exceptions: [], mayChange: true })),
}))

describe('AIOps › Jobs: Schedule, Runs and Validation rules (FX-13, CF-89)', () => {
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
    // Validation rules, since CF-89: the list, a rule, a list in its drawer.
    expect(jobsSub(parseHash('#/cases/jobs/rules').path)).toBe('rules')
    expect(pathId(parseHash('#/cases/jobs/rules').path, 'jobs')).toBe('')
    expect(pathId(parseHash('#/cases/jobs/rules/pod.privileged').path, 'rules')).toBe('pod.privileged')
    expect(pathId(parseHash('#/cases/jobs/rules/pod.privileged').path, 'jobs')).toBe('')
    expect(ruleSection(parseHash('#/cases/jobs/rules/pod.privileged/findings').path)).toBe('findings')
    expect(ruleSection(parseHash('#/cases/jobs/nightly-audit').path)).toBe('')
    expect(rulesHref).toBe('#/cases/jobs/rules')
  })

  it('has three sub-tabs, Validation rules the third, and switches to it', async () => {
    render(<JobsArea route={parseHash('#/cases/jobs')} />)
    expect(await screen.findByTestId('jobs-tab')).toBeTruthy()
    expect(within(screen.getByRole('group', { name: 'Jobs' })).getAllByRole('button').map((b) => b.textContent)).toEqual(['Schedule', 'Runs', 'Validation rules'])
    fireEvent.click(screen.getByTestId('jobs-sub-rules'))
    await waitFor(() => expect(window.location.hash).toBe('#/cases/jobs/rules'))
  })

  it('offers Validation rules to a SuperAdmin only: anyone else gets two sub-tabs, and its address shows the Schedule (CF-93)', async () => {
    window.location.hash = '#/cases/jobs/rules'
    render(<JobsArea route={parseHash('#/cases/jobs/rules')} rules={false} />)
    expect(screen.queryByTestId('jobs-sub-rules')).toBeNull()
    expect(screen.getByTestId('jobs-sub-schedule').getAttribute('aria-pressed')).toBe('true')
    await waitFor(() => expect(window.location.hash).toBe('#/cases/jobs'))
  })

  it('shows the rules page in the Jobs column, with the shared intro, toolbar and table (CF-55, CF-66)', async () => {
    render(<JobsArea route={parseHash('#/cases/jobs/rules')} />)
    const root = screen.getByTestId('jobs-area')
    expect(root.classList.contains('cs-col-inbox')).toBe(true)
    expect(screen.getByTestId('jobs-sub-rules').getAttribute('aria-pressed')).toBe('true')
    const el = await screen.findByTestId('rules-page')
    expect(el.parentElement).toBe(root)
    expect(el.children[0]!.textContent).toBe(
      'Agents use these rules for a portion of a problem identifiers - each finding becomes a case identified by cluster, object and rule. When no rule fits, an agent makes up a name: it shows as Unlisted, for a SuperAdmin to make it a rule or dismiss it.',
    )
    expect(el.children[0]!.classList.contains('cs-lead')).toBe(true)
    const bar = within(el).getByTestId('mycroft-toolbar')
    expect(el.children[1]).toBe(bar)
    await waitFor(() => expect(within(bar).getByTestId('rule-add')).toBeTruthy())
    const order = [...bar.querySelectorAll('[data-testid]')].map((x) => x.getAttribute('data-testid')).filter((x) => ['rules-show', 'rules-job', 'rules-search', 'rule-add'].includes(x!))
    expect(order).toEqual(['rules-show', 'rules-job', 'rules-search', 'rule-add'])
    const t = await within(el).findByTestId('rules-table')
    expect(t.style.minWidth).toBe(`${MYCROFT_TABLE_MIN}px`)
    expect(t.parentElement!.classList.contains('rk-card')).toBe(true)
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
