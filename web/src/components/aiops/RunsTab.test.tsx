import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '../../api/jobs'
import { setShowCosts } from '../../lib/costs'
import type { Route } from '../../lib/router'
import { findingTitles } from './RunDetail'
import { RunsTab } from './RunsTab'

vi.mock('../../api/jobs', async (original) => ({
  ...(await original<typeof import('../../api/jobs')>()),
  fetchRuns: vi.fn(),
  fetchRun: vi.fn(),
  cancelRun: vi.fn(),
}))

const route = (...path: string[]): Route => ({ cluster: 'local', view: 'tabs', tab: 'alerts', path: ['runs', ...path], params: new URLSearchParams() })

const brief: api.RunBrief = {
  id: 'run-12',
  job: 'alert-diagnosis',
  label: 'Alert diagnosis',
  agent: 'troubleshooter',
  trigger: { kind: 'event', what: 'Alert CrashLoopBackOff on shop/web-7f9 at 03:12', setBy: 'sam@example.com' },
  ref: 'alert:prod/pod:shop/web-7f9',
  clusters: ['prod'],
  repeats: 2,
  started: '2026-11-05T03:12:00Z',
  ended: '2026-11-05T03:13:10Z',
  status: 'ok',
  findings: 1,
  proposals: 1,
  costUSD: 0.31,
  model: 'claude-opus-5-5',
}

const run: api.Run = {
  ...brief,
  kind: 'alert-diagnosis',
  report: 'The pod runs out of memory.',
  findings: [
    {
      id: 'run-12.1',
      severity: 'critical',
      check: 'pod.oom-killed',
      title: 'shop/web-7f9 is OOMKilled',
      detail: 'Its limit is 256Mi.',
      evidence: ['OOMKilled 4 times'],
      cluster: 'prod',
      resource: 'shop/Pod/web-7f9',
      key: { cluster: 'prod', object: 'shop/Pod/web-7f9', check: 'pod.oom-killed' },
      routing: 'opened',
      case: 'C-31',
    },
  ],
  routed: { opened: 1, seen: 0, known: 0, excepted: 0, reopened: 0, regression: 0, verified: 0, capped: 0, invalid: 0 },
  proposals: [{ id: 'card-3', title: 'Raise the memory limit of web' }],
  tokens: { in: 5200, out: 800 },
}

describe('AIOps › Runs', () => {
  beforeAll(() => {
    // jsdom has no scrollTo on elements; the drawer scrolls its body to the top.
    Element.prototype.scrollTo = () => {}
  })
  beforeEach(() => {
    vi.mocked(api.fetchRuns).mockResolvedValue({ runs: [brief, { ...brief, id: 'run-11', status: 'budget', note: 'not started: the day’s budget of $10.00 is spent', findings: 0, proposals: 0, costUSD: 0, repeats: 0 }], total: 2 })
    vi.mocked(api.fetchRun).mockResolvedValue(run)
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('lists the runs with what started them, their result and cost', async () => {
    render(<RunsTab route={route()} />)
    const rows = await screen.findAllByTestId('run-row')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.textContent).toContain('Alert CrashLoopBackOff on shop/web-7f9 at 03:12')
    expect(rows[0]!.textContent).toContain('2 repeats')
    expect(rows[0]!.textContent).toContain('1 finding, 1 proposed change')
    expect(rows[0]!.textContent).toContain('$0.31')
    expect(rows[1]!.textContent).toContain('Budget spent')
    expect(rows[1]!.textContent).toContain('budget of $10.00 is spent')
    fireEvent.click(screen.getByTestId('runs-mine'))
    await waitFor(() => expect(api.fetchRuns).toHaveBeenLastCalledWith({ status: '', mine: true, limit: 200 }, false))
  })

  it('shows what each proposed change resolves, linked, and why an agent’s change that names no case was not filed (CF-25, CF-27)', async () => {
    vi.mocked(api.fetchRun).mockResolvedValue({
      ...run,
      proposals: [
        { id: 'card-3', title: 'Raise the memory limit of web', resolves: ['C-31', 'KI-2'] },
        { id: '', title: 'Restart everything', error: 'names no case it resolves' },
      ],
    })
    render(<RunsTab route={route('run-12')} />)
    const detail = await screen.findByTestId('run-detail')
    const [filed, refused] = within(detail).getAllByTestId('run-proposal')
    expect(filed!.getAttribute('data-filed')).toBe('true')
    expect(within(filed!).getByRole('link', { name: 'card-3 in the Inbox' }).getAttribute('href')).toBe('#/aiops/inbox/proposal/card-3')
    const links = within(filed!).getAllByTestId('run-proposal-resolves-link')
    expect(links.map((a) => [a.textContent, a.getAttribute('href')])).toEqual([
      ['C-31', '#/aiops/inbox/case/31'],
      ['KI-2', '#/aiops/mycroft/known/KI-2'],
    ])
    expect(refused!.getAttribute('data-filed')).toBe('false')
    expect(within(refused!).getByTestId('run-proposal-error').textContent).toContain('not filed: names no case it resolves')
    expect(within(refused!).queryByTestId('run-proposal-resolves')).toBeNull()
  })

  it('opens a run with its report, what its findings did, proposals and cost; no finding actions (wave 6)', async () => {
    render(<RunsTab route={route('run-12')} />)
    const detail = await screen.findByTestId('run-detail')
    expect(within(detail).getByTestId('run-report').textContent).toContain('runs out of memory')
    expect(within(detail).getByTestId('run-trigger').textContent).toContain('Alert CrashLoopBackOff')
    expect(within(detail).getByText(/trigger set by sam@example.com/)).toBeTruthy()
    expect(within(detail).getByTestId('run-cost').textContent).toBe('$0.31')
    expect(within(detail).getByTestId('run-proposals').textContent).toContain('Raise the memory limit of web')
    expect(within(detail).getByTestId('run-ref').getAttribute('href')).toBe('#/c/prod/alerts')
    expect(within(detail).getByTestId('run-routed-opened').textContent).toBe('1 new case')
    expect(within(detail).getByTestId('finding-run-12.1-routing').textContent).toContain('New case')
    expect(within(detail).getByTestId('finding-run-12.1-routing-case').getAttribute('href')).toBe('#/aiops/inbox/case/31')
    expect(within(detail).getByText('pod.oom-killed')).toBeTruthy()
    // A finding is not decided on its own: no priority, New Case, Ignore or Reopen.
    expect(within(detail).queryByTestId('finding-run-12.1-priority')).toBeNull()
    expect(within(detail).queryByTestId('finding-run-12.1-ignore')).toBeNull()
    expect(within(detail).queryByTestId('finding-run-12.1-case')).toBeNull()
    expect(within(detail).queryByTestId('run-cancel')).toBeNull()
  })

  it('says what every finding did, each linked to its case, known issue or exception, with verified fixes and new checks', async () => {
    const f = run.findings![0]!
    vi.mocked(api.fetchRun).mockResolvedValue({
      ...run,
      routed: { opened: 1, seen: 1, known: 1, excepted: 1, reopened: 1, regression: 1, verified: 2, capped: 3, invalid: 1 },
      verifiedCases: ['C-4', 'C-5'],
      newChecks: ['pod.oom-killed'],
      ruleMeanings: { 'pod.oom-killed': 'a container was killed for memory' },
      findings: [
        { ...f, id: 'run-12.1', routing: 'seen', case: 'C-12' },
        { ...f, id: 'run-12.2', routing: 'known', case: undefined, knownIssue: 'KI-3' },
        { ...f, id: 'run-12.3', routing: 'excepted', case: undefined, exception: 'EX-2' },
        { ...f, id: 'run-12.4', routing: 'reopened', case: 'C-9', newCheck: true, confidence: 'likely' },
        { ...f, id: 'run-12.5', routing: 'invalid', case: undefined, invalidReason: 'the check "made.up" is not in the list' },
      ],
    })
    render(<RunsTab route={route('run-12')} />)
    const detail = await screen.findByTestId('run-detail')
    // A cause the agent could not confirm says so; a confirmed one, or a finding of before the field, says nothing (PM-J1d).
    expect(within(detail).getByTestId('finding-run-12.4-likely').textContent).toBe('likely')
    expect(within(detail).queryByTestId('finding-run-12.1-likely')).toBeNull()
    expect(within(detail).getByTestId('run-routed').textContent).toContain('1 case reopened: the fix did not hold')
    expect(within(detail).getByTestId('run-routed-capped').textContent).toBe('3 over the cap of new cases')
    expect(within(detail).getByTestId('run-routed-verified').textContent).toBe('2 fixes verified')
    expect([...within(detail).getByTestId('run-verified').querySelectorAll('a')].map((a) => a.getAttribute('href'))).toEqual(['#/aiops/inbox/case/4', '#/aiops/inbox/case/5'])
    // Validation rules, by id, with what each means (build 14).
    expect(within(detail).getByTestId('run-new-checks').textContent).toBe('Validation rules new on this cluster: pod.oom-killed')
    expect(within(detail).getByTestId('run-new-checks').querySelector('span[title]')!.getAttribute('title')).toBe('a container was killed for memory')
    expect(within(detail).getByTestId('finding-run-12.1-rule').textContent).toBe('pod.oom-killed · a container was killed for memory')
    // What needs a person first: the reopened case.
    expect(
      within(detail)
        .getAllByTestId('run-finding')
        .map((el) => el.getAttribute('data-routing')),
    ).toEqual(['reopened', 'seen', 'known', 'excepted', 'invalid'])
    expect(within(detail).getByTestId('finding-run-12.1-routing-case').getAttribute('href')).toBe('#/aiops/inbox/case/12')
    expect(within(detail).getByTestId('finding-run-12.2-routing-known').getAttribute('href')).toBe('#/aiops/mycroft/known/KI-3')
    expect(within(detail).getByTestId('finding-run-12.3-routing-exception').getAttribute('href')).toBe('#/aiops/mycroft/exceptions/EX-2')
    expect(within(detail).getByTestId('finding-run-12.4-new-check').textContent).toBe('new validation rule')
    expect(within(detail).getByTestId('finding-run-12.5-invalid').textContent).toContain('is not in the list')
  })

  // --- Jobs and findings, round 2 (Phase 12, wave 5) ---
  it('shows when each run started and ended, its health, and a status that takes the tone of what it found (JR-01, JR-03)', async () => {
    vi.mocked(api.fetchRuns).mockResolvedValue({
      runs: [
        { ...brief, health: 'critical', severities: { critical: 2, warning: 1 } },
        { ...brief, id: 'run-11', health: 'warning', severities: { warning: 1 } },
        { ...brief, id: 'run-10', health: 'none', severities: {}, findings: 0 },
        { ...brief, id: 'run-9', status: 'failed', health: 'failed', severities: {}, note: 'prod: timeout' },
        { ...brief, id: 'run-8', status: 'running', ended: undefined, health: undefined, severities: {} },
      ],
      total: 5,
    })
    render(<RunsTab route={route()} />)
    const rows = await screen.findAllByTestId('run-row')
    expect(screen.getByTestId('runs-table').querySelector('thead')!.textContent).toBe('StartedEndedJobStarted byStatusHealthResultCost')
    const status = (i: number) => within(rows[i]!).getByTestId('run-status-cell')
    const health = (i: number) => within(rows[i]!).queryByTestId('run-health-cell')
    expect(status(0).textContent).toBe('Done · 2 critical')
    expect(status(0).getAttribute('data-tone')).toBe('err')
    expect(health(0)!.textContent).toBe('Critical')
    expect(status(1).textContent).toBe('Done · 1 warning')
    expect(status(1).getAttribute('data-tone')).toBe('warn')
    expect(status(2).textContent).toBe('Done')
    expect(status(2).getAttribute('data-tone')).toBe('ok')
    expect(health(2)!.getAttribute('data-tone')).toBe('ok')
    expect(health(3)!.textContent).toBe('Failed')
    expect(health(4)).toBeNull()
    expect(within(rows[4]!).getByTestId('run-ended').textContent).toBe('running')
    expect(within(rows[0]!).getByTestId('run-ended').textContent).not.toBe('—')
  })

  it('says a note said many times once, with how many (JR-02)', async () => {
    const refuse = 'a proposed command Watson would refuse was left out: kubectl delete is refused on production'
    vi.mocked(api.fetchRun).mockResolvedValue({ ...run, note: [refuse, refuse, refuse, refuse].join('; ') })
    render(<RunsTab route={route('run-12')} />)
    expect((await screen.findByTestId('run-note')).textContent).toBe('4 proposed commands Watson would refuse were left out: kubectl delete is refused on production')
  })

  it('cancels a run under way, for someone who may edit jobs', async () => {
    vi.mocked(api.fetchRun)
      .mockResolvedValueOnce({ ...run, status: 'running', ended: undefined, findings: [], mayCancel: true })
      .mockResolvedValue({ ...run, status: 'cancelled', note: 'cancelled by sam', findings: [] })
    vi.mocked(api.cancelRun).mockResolvedValue({ ...brief, status: 'cancelled' })
    const toast = vi.fn()
    render(<RunsTab route={route('run-12')} onToast={toast} />)
    fireEvent.click(await screen.findByTestId('run-cancel'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(api.cancelRun).toHaveBeenCalledWith('run-12'))
    expect(toast).toHaveBeenCalledWith('Cancelled.')
    await waitFor(() => expect(screen.getByTestId('run-status').textContent).toBe('Cancelled'))
    expect(screen.queryByTestId('run-cancel')).toBeNull()
  })
})

// --- Owner's feedback on Cases (Phase 12, wave 6c) ---

describe('AIOps › Runs since wave 6c (CF-33, CF-39, CF-40)', () => {
  beforeAll(() => {
    Element.prototype.scrollTo = () => {}
  })
  beforeEach(() => {
    vi.mocked(api.fetchRuns).mockResolvedValue({ runs: [brief], total: 1 })
    vi.mocked(api.fetchRun).mockResolvedValue(run)
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
    setShowCosts(true)
  })

  it('keeps a finding below the job’s lowest priority as ignored, counted, opening no case', async () => {
    const f = run.findings![0]!
    vi.mocked(api.fetchRun).mockResolvedValue({
      ...run,
      routed: { ...run.routed!, opened: 0, ignored: 2 },
      findings: [
        { ...f, id: 'run-12.6', severity: 'info', routing: 'ignored', case: undefined, priority: 4 },
        { ...f, id: 'run-12.7', routing: 'opened' },
      ],
    })
    render(<RunsTab route={route('run-12')} />)
    const detail = await screen.findByTestId('run-detail')
    expect(within(detail).getByTestId('run-routed-ignored').textContent).toBe('2 below the job’s lowest priority')
    expect(within(detail).getByTestId('finding-run-12.6-routing').textContent).toBe('Ignored')
    expect(within(detail).getByTestId('finding-run-12.6-ignored').textContent).toBe('Opened no case: below the job’s lowest priority (it is P4).')
    expect(within(detail).queryByTestId('finding-run-12.6-routing-case')).toBeNull()
    // What needs a person comes first; the ignored last.
    expect(
      within(detail)
        .getAllByTestId('run-finding')
        .map((el) => el.getAttribute('data-routing')),
    ).toEqual(['opened', 'ignored'])
  })

  it('keeps an expected finding, with the agent’s reason, counted, opening no case whatever its severity (CF-82)', async () => {
    const f = run.findings![0]!
    vi.mocked(api.fetchRun).mockResolvedValue({
      ...run,
      routed: { ...run.routed!, opened: 0, expected: 1 },
      findings: [{ ...f, id: 'run-17.1', severity: 'critical', routing: 'expected', case: undefined, expected: true, expectedWhy: 'r620-worker is annotated nodepower/status: asleep.' }],
    })
    render(<RunsTab route={route('run-12')} />)
    const detail = await screen.findByTestId('run-detail')
    expect(within(detail).getByTestId('run-routed-expected').textContent).toBe('1 expected: no problem')
    expect(within(detail).getByTestId('finding-run-17.1-routing').textContent).toBe('Expected')
    expect(within(detail).getByTestId('finding-run-17.1-expected').textContent).toBe('Opened no case: expected — r620-worker is annotated nodepower/status: asleep.')
    expect(within(detail).queryByTestId('finding-run-17.1-routing-case')).toBeNull()
  })

  it('follows what a proposal resolves with its title, from the run or its findings', async () => {
    vi.mocked(api.fetchRun).mockResolvedValue({ ...run, proposals: [{ id: 'card-3', title: 'Raise the memory limit of web', resolves: ['C-31', 'KI-3'], resolvesTitles: { 'KI-3': 'Certificates renewed by hand' } }] })
    render(<RunsTab route={route('run-12')} />)
    const r = await screen.findByTestId('run-proposal-resolves')
    expect(r.textContent).toBe('Resolves C-31 · shop/web-7f9 is OOMKilled, KI-3 · Certificates renewed by hand')
    expect(findingTitles({ findings: [{ ...run.findings![0]!, case: undefined, knownIssue: 'KI-1', title: 'x' }] })).toEqual({ 'KI-1': 'x' })
  })

  it('hides every cost when Show costs is off, and shows the tokens instead', async () => {
    setShowCosts(false)
    render(<RunsTab route={route('run-12')} />)
    const detail = await screen.findByTestId('run-detail')
    expect(within(detail).queryByTestId('run-cost')).toBeNull()
    expect(within(detail).getByTestId('run-tokens').textContent).toBe('5,200 in, 800 out')
    const table = screen.getByTestId('runs-table')
    expect([...table.querySelectorAll('th')].map((h) => h.textContent)).not.toContain('Cost')
    expect(table.textContent).not.toContain('$')
    expect(detail.textContent).not.toContain('$')
  })
})
