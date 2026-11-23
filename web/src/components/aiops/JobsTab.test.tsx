import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '../../api/jobs'
import { setShowCosts } from '../../lib/costs'
import type { Route } from '../../lib/router'
import { exportTable } from '../../lib/table'
import { jobColumns, JobsTab, WhenCell } from './JobsTab'
import { clockInBrowser } from '../../lib/format'

vi.mock('../../api/jobs', async (original) => ({
  ...(await original<typeof import('../../api/jobs')>()),
  fetchJobs: vi.fn(),
  updateJob: vi.fn(),
  runJobNow: vi.fn(),
  rebuildJobNow: vi.fn(),
  addJob: vi.fn(),
  removeJob: vi.fn(),
  fetchJobRuns: vi.fn(),
  describeJobSchedule: vi.fn(),
  fetchRun: vi.fn(),
}))

const route = (...path: string[]): Route => ({ cluster: 'local', view: 'tabs', tab: 'alerts', path: ['jobs', ...path], params: new URLSearchParams() })

const audit: api.Job = {
  id: 'nightly-audit',
  kind: 'nightly-audit',
  when: { schedule: { kind: 'daily', at: '02:00', timeZone: 'Europe/Berlin' } },
  budget: { runUSD: 4, dayUSD: 4 },
  on: true,
  skipOverlap: true,
  catchUp: true,
  setBy: 'sam@example.com',
  setAt: '2026-11-04T10:00:00Z',
  label: 'Nightly audit',
  agent: 'auditor',
  agentName: 'Auditor',
  does: 'A report of new RBAC risks, volumes nearly full and practices not met',
  builtinTask: 'Find what has gone wrong or become risky.',
  reads: 'Each cluster the job covers, read-only.',
  proposes: 'A report, findings, and changes that wait in AIOps › Inbox.',
  schedulable: true,
  manual: true,
  whenText: 'Every day at 02:00, Europe/Berlin',
  next: '2026-11-06T01:00:00Z',
  last: { id: 'run-7', job: 'nightly-audit', label: 'Nightly audit', trigger: { kind: 'schedule', what: 'Every day at 02:00, Europe/Berlin' }, started: '2026-11-05T01:00:00Z', status: 'ok', findings: 2, proposals: 0, costUSD: 0.42 },
  running: false,
  spentToday: 0.42,
}

const alerts: api.Job = {
  id: 'alert-diagnosis',
  kind: 'alert-diagnosis',
  when: { event: 'alert', severity: 'warning' },
  clusters: ['prod'],
  budget: { runUSD: 2, dayUSD: 10 },
  on: false,
  skipOverlap: true,
  catchUp: true,
  label: 'Alert diagnosis',
  agent: 'troubleshooter',
  agentName: 'Troubleshooter',
  events: ['alert'],
  schedulable: false,
  manual: true,
  whenText: 'When an alert fires, warning and above',
  nextText: 'Paused',
  running: false,
  spentToday: 0,
}

const backup: api.Job = {
  id: 'backup-s3',
  kind: 'backup-s3',
  when: { schedule: { kind: 'daily', at: '03:00' } },
  budget: {},
  on: false,
  skipOverlap: true,
  catchUp: true,
  label: 'Backup to S3',
  schedulable: false,
  manual: true,
  whenText: 'Every day at 03:00, UTC',
  nextText: 'Paused',
  running: false,
  spentToday: 0,
  external: { ready: true, on: false, detail: 's3://backups/221b/', lastAt: '2026-11-05T02:00:00Z', last: '18234 bytes, kept 14', lastOk: true },
  where: 'Administration › Backup',
  link: '#/admin/backup',
}

const view = (mayEdit = true): api.JobsView => ({
  jobs: [audit, alerts, backup],
  kinds: [
    { id: 'nightly-audit', label: 'Nightly audit', agentName: 'Auditor', schedulable: true, task: true, addMore: true },
    { id: 'report-triage', label: 'Case triage', agentName: 'Troubleshooter', schedulable: false, task: true },
  ],
  clusters: [
    { id: 'local', name: 'local' },
    { id: 'prod', name: 'prod-eu' },
  ],
  settings: {},
  mayEdit,
  admin: mayEdit,
  timeZone: 'Europe/Berlin',
})

describe('AIOps › Jobs', () => {
  beforeAll(() => {
    // jsdom has no scrollTo on elements; the drawer scrolls its body to the top.
    Element.prototype.scrollTo = () => {}
  })
  beforeEach(() => {
    vi.mocked(api.fetchJobs).mockResolvedValue(view())
    vi.mocked(api.describeJobSchedule).mockResolvedValue({ description: 'Every day at 02:00, Europe/Berlin', next: ['2026-11-06T01:00:00Z'] })
    vi.mocked(api.fetchJobRuns).mockResolvedValue({ runs: [], total: 0 })
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('lists each job with its agent, when in plain words, budget, last and next run and state', async () => {
    render(<JobsTab route={route()} />)
    const rows = await screen.findAllByTestId('job-row')
    expect(rows).toHaveLength(3)
    const [a, b, c] = rows as [HTMLElement, HTMLElement, HTMLElement]
    expect(a.textContent).toContain('Nightly audit')
    // The agent beside the job's name, so jobs that share one are plain (I-5).
    expect(`${within(a).getByRole('link', { name: 'Nightly audit' }).textContent}${within(a).getByTestId('job-agent').textContent}`).toBe('Nightly audit · Auditor')
    expect(within(a).getByTestId('job-agent').title).toContain('Administration › AI › Agents apply to every job it runs')
    expect(a.textContent).toContain('All clusters')
    expect(a.textContent).toContain('Every day at 02:00')
    expect(a.textContent).toContain('Europe/Berlin')
    expect(a.textContent).toContain('$4 a run, $4 a day')
    expect(a.textContent).toContain('2 findings')
    expect(within(a).getByTestId('job-state').textContent).toBe('On')
    expect(within(b).getByTestId('job-agent').textContent).toBe(' · Troubleshooter')
    expect(b.textContent).toContain('On prod-eu')
    expect(b.textContent).toContain('When an alert fires, warning and above')
    expect(b.textContent).toContain('Never')
    expect(within(b).getByTestId('job-state').textContent).toBe('Paused')
    expect(c.textContent).toContain('Set in Administration › Backup · s3://backups/221b/')
    expect(within(c).queryByTestId('job-agent')).toBeNull()
    expect(c.textContent).toContain('18234 bytes, kept 14')
    expect(c.textContent).toContain('None')
  })

  it('pauses, runs now and saves a changed schedule and budget', async () => {
    vi.mocked(api.updateJob).mockImplementation(async (_id, c) => ({ ...audit, ...(c.on !== undefined ? { on: c.on } : {}), setAt: '2026-11-05T12:00:00Z' }))
    vi.mocked(api.runJobNow).mockResolvedValue({ ...audit.last!, id: 'run-8', status: 'running' })
    const toast = vi.fn()
    render(<JobsTab route={route('nightly-audit')} onToast={toast} />)
    const drawer = await screen.findByTestId('job-drawer')
    fireEvent.click(within(drawer).getByTestId('job-run-now'))
    await waitFor(() => expect(api.runJobNow).toHaveBeenCalledWith('nightly-audit'))
    fireEvent.click(within(drawer).getByTestId('job-toggle'))
    await waitFor(() => expect(api.updateJob).toHaveBeenCalledWith('nightly-audit', { on: false }))
    const panel = await screen.findByTestId('job-panel')
    expect(within(panel).getByText('No changes.')).toBeTruthy()
    fireEvent.change(within(panel).getByTestId('job-sched-kind'), { target: { value: 'weekly' } })
    fireEvent.click(within(panel).getByTestId('job-sched-day-fri'))
    fireEvent.change(within(panel).getByTestId('job-runUSD'), { target: { value: '6' } })
    fireEvent.click(within(panel).getByTestId('job-some-clusters'))
    fireEvent.click(within(panel).getByTestId('job-cluster-prod'))
    await waitFor(() => expect(api.describeJobSchedule).toHaveBeenCalled())
    fireEvent.click(within(panel).getByTestId('job-save'))
    await waitFor(() => expect(api.updateJob).toHaveBeenCalledTimes(2))
    const sent = vi.mocked(api.updateJob).mock.calls[1]![1]
    expect(sent.when?.schedule).toEqual({ kind: 'weekly', at: '02:00', timeZone: 'Europe/Berlin', every: 6, days: ['mon', 'fri'] })
    expect(sent.budget).toEqual({ runUSD: 6, dayUSD: 4 })
    expect(sent.clusters).toEqual(['prod'])
    expect(toast).toHaveBeenCalledWith('Saved.')
  })

  it('sets an event job’s severity', async () => {
    vi.mocked(api.updateJob).mockImplementation(async () => alerts)
    render(<JobsTab route={route('alert-diagnosis')} />)
    const panel = await screen.findByTestId('job-panel')
    expect(within(panel).queryByTestId('job-scheduled')).toBeNull()
    expect((within(panel).getByTestId('job-event-alert') as HTMLInputElement).checked).toBe(true)
    fireEvent.change(within(panel).getByTestId('job-severity'), { target: { value: 'error' } })
    fireEvent.click(within(panel).getByTestId('job-save'))
    await waitFor(() => expect(api.updateJob).toHaveBeenCalled())
    expect(vi.mocked(api.updateJob).mock.calls[0]![1].when).toEqual({ event: 'alert', severity: 'error' })
  })

  it('offers Rebuild now only where a job has something to rebuild, after asking (KN-29)', async () => {
    const v = view()
    vi.mocked(api.fetchJobs).mockResolvedValue({ ...v, jobs: v.jobs.map((j) => (j.id === 'nightly-audit' ? { ...j, rebuild: true } : j)) })
    vi.mocked(api.rebuildJobNow).mockResolvedValue({ ...audit.last!, id: 'run-9', status: 'running' })
    render(<JobsTab route={route('nightly-audit')} />)
    await screen.findByTestId('job-panel')
    fireEvent.click(within(screen.getByTestId('job-actions')).getByTestId('job-rebuild-now'))
    expect(screen.getByTestId('job-rebuild-text').textContent).toContain('unchanged ones too')
    expect(api.rebuildJobNow).not.toHaveBeenCalled()
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(api.rebuildJobNow).toHaveBeenCalledWith('nightly-audit'))
    cleanup()
    vi.mocked(api.fetchJobs).mockResolvedValue(view())
    render(<JobsTab route={route('nightly-audit')} />)
    await screen.findByTestId('job-panel')
    expect(screen.queryByTestId('job-rebuild-now')).toBeNull()
  })

  it('says in the header whether the job runs and when, keeps its actions in the footer, and its tabs apart from them', async () => {
    render(<JobsTab route={route('nightly-audit')} />)
    const drawer = await screen.findByTestId('job-drawer')
    expect(within(drawer).getByTestId('job-drawer-state').textContent).toMatch(/^(On|Off|Running) · /)
    const actions = within(drawer).getByTestId('job-actions')
    expect([...actions.querySelectorAll('button')].map((b) => b.textContent)).toEqual([audit.on ? 'Pause' : 'Turn on', 'Run now'])
    expect(within(screen.getByTestId('job-panel')).queryByTestId('job-run-now')).toBeNull()
    const tabs = screen.getByTestId('job-tab-settings').parentElement!
    expect(tabs.classList.contains('rk-seg')).toBe(true)
    expect(screen.getByTestId('job-tab-settings').classList.contains('rk-btn')).toBe(false)
    expect(screen.getByTestId('job-set-by').textContent).toMatch(/^(Set by |Not changed)/)
  })

  it('shows the backup to S3 as set elsewhere, with Run now and Resume', async () => {
    render(<JobsTab route={route('backup-s3')} />)
    const panel = await screen.findByTestId('job-panel')
    expect(within(panel).getByTestId('job-external').textContent).toContain('Administration › Backup')
    expect(within(panel).queryByTestId('job-form')).toBeNull()
    expect(screen.getByTestId('job-toggle').textContent).toBe('Resume')
    expect(screen.getByTestId('job-run-now')).toBeTruthy()
  })

  it('lets someone who may not edit read, not change', async () => {
    vi.mocked(api.fetchJobs).mockResolvedValue(view(false))
    render(<JobsTab route={route('nightly-audit')} />)
    const panel = await screen.findByTestId('job-panel')
    expect(screen.queryByTestId('job-run-now')).toBeNull()
    expect(screen.queryByTestId('job-toggle')).toBeNull()
    expect((within(panel).getByTestId('job-form') as HTMLFieldSetElement).disabled).toBe(true)
    expect(screen.queryByTestId('jobs-add-kind')).toBeNull()
  })

  // --- Jobs and findings, round 2 (Phase 12, wave 5) ---
  it('shows what a job does plainly, and saves its own name and instructions, back to the built-in with Reset (JR-05, JR-06)', async () => {
    vi.mocked(api.updateJob).mockImplementation(async (_id, c) => ({ ...audit, name: c.name || undefined, task: c.task || undefined, setAt: '2026-11-05T12:00:00Z' }))
    render(<JobsTab route={route('nightly-audit')} />)
    const panel = await screen.findByTestId('job-panel')
    expect(within(panel).getByTestId('job-def-does').textContent).toContain('RBAC risks')
    expect(within(panel).getByTestId('job-def-reads').textContent).toContain('read-only')
    expect(within(panel).getByTestId('job-def-proposes').textContent).toContain('AIOps › Inbox')
    const task = within(panel).getByTestId('job-task') as HTMLTextAreaElement
    expect(task.value).toBe('Find what has gone wrong or become risky.')
    expect(within(panel).getByTestId('job-task-state').textContent).toBe('Built-in')
    expect(within(panel).queryByTestId('job-task-reset')).toBeNull()
    fireEvent.change(task, { target: { value: 'Only look at volumes.' } })
    fireEvent.change(within(panel).getByTestId('job-name'), { target: { value: 'Volumes' } })
    expect(within(panel).getByTestId('job-task-state').textContent).toBe('Its own')
    fireEvent.click(within(panel).getByTestId('job-save'))
    await waitFor(() => expect(api.updateJob).toHaveBeenCalled())
    expect(vi.mocked(api.updateJob).mock.calls[0]![1]).toMatchObject({ name: 'Volumes', task: 'Only look at volumes.' })
    const again = await screen.findByTestId('job-panel')
    fireEvent.click(within(again).getByTestId('job-task-reset'))
    expect((within(again).getByTestId('job-task') as HTMLTextAreaElement).value).toBe('Find what has gone wrong or become risky.')
    fireEvent.click(within(again).getByTestId('job-save'))
    await waitFor(() => expect(api.updateJob).toHaveBeenCalledTimes(2))
    expect(vi.mocked(api.updateJob).mock.calls[1]![1].task).toBe('')
  })

  it('offers “Add another Nightly audit job” with why one would, and opens the job added (JR-07)', async () => {
    vi.mocked(api.addJob).mockResolvedValue({ ...audit, id: 'nightly-audit-1', name: 'Nightly audit 2', added: true, on: false })
    render(<JobsTab route={route('nightly-audit')} />)
    const box = await screen.findByTestId('job-add-another-box')
    expect(box.textContent).toContain('another schedule, on other clusters, or with other instructions')
    expect(within(box).getByTestId('job-add-another').textContent).toBe('Add another Nightly audit job')
    fireEvent.click(within(box).getByTestId('job-add-another'))
    await waitFor(() => expect(api.addJob).toHaveBeenCalledWith('nightly-audit'))
    await waitFor(() => expect(window.location.hash).toBe('#/aiops/jobs/nightly-audit-1'))
    window.location.hash = ''
    cleanup()
    // A kind whose events run its first job only offers none.
    render(<JobsTab route={route('alert-diagnosis')} />)
    await screen.findByTestId('job-panel')
    expect(screen.queryByTestId('job-add-another')).toBeNull()
  })

  it('lists a job’s runs, each opening to its output', async () => {
    vi.mocked(api.fetchJobRuns).mockResolvedValue({ runs: [audit.last!], total: 1 })
    vi.mocked(api.fetchRun).mockResolvedValue({ ...audit.last!, kind: 'nightly-audit', report: 'All **fine**.', tokens: { in: 1200, out: 300 }, findings: [], proposals: [] })
    render(<JobsTab route={route('nightly-audit')} />)
    fireEvent.click(await screen.findByTestId('job-tab-history'))
    fireEvent.click(await screen.findByTestId('job-history-row'))
    expect((await screen.findByTestId('run-report')).textContent).toContain('All fine.')
    expect(screen.getByTestId('run-cost').textContent).toBe('$0.42')
  })
})

// --- Owner's feedback on Cases (Phase 12, wave 6c) ---

describe('AIOps › Jobs since wave 6c (CF-33, CF-40)', () => {
  beforeAll(() => {
    Element.prototype.scrollTo = () => {}
  })
  beforeEach(() => {
    vi.mocked(api.fetchJobs).mockResolvedValue({ ...view(), jobs: [{ ...audit, minPriority: 3 }, alerts, backup] })
    vi.mocked(api.describeJobSchedule).mockResolvedValue({ description: 'Every day at 02:00, Europe/Berlin', next: ['2026-11-06T01:00:00Z'] })
    vi.mocked(api.fetchJobRuns).mockResolvedValue({ runs: [], total: 0 })
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
    setShowCosts(true)
  })

  it('sets a job’s lowest priority, P0 to P4, with what it means', async () => {
    vi.mocked(api.updateJob).mockImplementation(async (_id, c) => ({ ...audit, ...c }))
    render(<JobsTab route={route('nightly-audit')} />)
    const panel = await screen.findByTestId('job-panel')
    const pick = within(panel).getByTestId('job-min-priority') as HTMLSelectElement
    expect(pick.value).toBe('3')
    expect([...pick.options].map((o) => o.value)).toEqual(['0', '1', '2', '3', '4'])
    expect(within(panel).getByTestId('job-min-priority-field').textContent).toContain('Findings below it open no case')
    fireEvent.change(pick, { target: { value: '2' } })
    fireEvent.click(within(panel).getByTestId('job-save'))
    await waitFor(() => expect(api.updateJob).toHaveBeenCalled())
    expect(vi.mocked(api.updateJob).mock.calls[0]![1].minPriority).toBe(2)
  })

  it('offers no lowest priority for a job without findings, and sends none', async () => {
    vi.mocked(api.updateJob).mockImplementation(async () => alerts)
    render(<JobsTab route={route('alert-diagnosis')} />)
    const panel = await screen.findByTestId('job-panel')
    expect(within(panel).queryByTestId('job-min-priority')).toBeNull()
    fireEvent.click(within(panel).getByTestId('job-skip'))
    fireEvent.click(within(panel).getByTestId('job-save'))
    await waitFor(() => expect(api.updateJob).toHaveBeenCalled())
    expect(vi.mocked(api.updateJob).mock.calls[0]![1]).not.toHaveProperty('minPriority')
  })

  it('hides the budgets column, the day’s spend and the runs’ costs when Show costs is off; the budget fields stay', async () => {
    setShowCosts(false)
    vi.mocked(api.fetchJobRuns).mockResolvedValue({ runs: [audit.last!], total: 1 })
    render(<JobsTab route={route('nightly-audit')} />)
    const rows = await screen.findAllByTestId('job-row')
    expect(rows[0]!.textContent).not.toMatch(/\$\d/)
    const panel = await screen.findByTestId('job-panel')
    expect(panel.textContent).not.toContain('Today it has spent')
    // No amount; the budget fields' labels still say US$.
    expect(panel.textContent).not.toMatch(/\$\d/)
    expect((within(panel).getByTestId('job-runUSD') as HTMLInputElement).value).toBe('4')
  })
})

describe("a job's schedule beside the browser's time (R2-16)", () => {
  afterEach(cleanup)
  const job = (timeZone: string) => ({ whenText: `Every day at 09:00, ${timeZone}`, when: { schedule: { kind: 'daily', at: '09:00', timeZone } } }) as unknown as api.Job

  it("adds the browser's time beside the zone where the two differ", () => {
    // Tokyo keeps UTC+9 all year; a browser there has nothing to add.
    const here = clockInBrowser('09:00', 'Asia/Tokyo')
    render(<WhenCell j={job('Asia/Tokyo')} />)
    expect(screen.getByTestId('job-when-zone').textContent).toBe(here ? `Asia/Tokyo · ${here}` : 'Asia/Tokyo')
    expect(new Date().getTimezoneOffset() === -9 * 60 || here !== '').toBe(true)
  })

  it('shows one time where the zones agree', () => {
    const browser = Intl.DateTimeFormat().resolvedOptions().timeZone
    render(<WhenCell j={job(browser)} />)
    expect(screen.getByTestId('job-when-zone').textContent).toBe(browser)
  })
})

// --- A job whose agent is off (JB-25, the owner, November 23) ---
describe('AIOps › Jobs: a job whose agent is off, and a failed last run', () => {
  const digest: api.Job = {
    ...audit,
    id: 'daily-digest',
    kind: 'daily-digest',
    label: 'Daily digest',
    agent: 'summarizer',
    agentName: 'Summarizer',
    on: false,
    nextText: 'Paused',
    next: undefined,
    last: undefined,
    agentOff: 'the Summarizer is off',
  }
  const failed: api.Job = { ...audit, last: { ...audit.last!, id: 'run-9', status: 'failed', note: 'the Auditor is off', findings: 0 } }
  beforeAll(() => {
    Element.prototype.scrollTo = () => {}
  })
  beforeEach(() => {
    vi.mocked(api.fetchJobs).mockResolvedValue({ ...view(), jobs: [failed, digest] })
    vi.mocked(api.describeJobSchedule).mockResolvedValue({ description: 'Every day at 02:00, Europe/Berlin', next: ['2026-11-06T01:00:00Z'] })
    vi.mocked(api.fetchJobRuns).mockResolvedValue({ runs: [], total: 0 })
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('shows Run now’s refusal in the drawer, and no “started”', async () => {
    const refusal = 'The Daily digest runs on the Summarizer, which is off: turn it on in Administration › AI › Agents.'
    vi.mocked(api.runJobNow).mockRejectedValue(new Error(refusal))
    const toast = vi.fn()
    render(<JobsTab route={route('daily-digest')} onToast={toast} />)
    const drawer = await screen.findByTestId('job-drawer')
    fireEvent.click(within(drawer).getByTestId('job-run-now'))
    expect((await within(drawer).findByTestId('job-action-error')).textContent).toBe(refusal)
    expect(toast).not.toHaveBeenCalled()
  })

  it('says on the row and in the drawer that the agent is off, with a link to it, and warns on turning it on', async () => {
    vi.mocked(api.updateJob).mockImplementation(async () => ({ ...digest, on: true }))
    const toast = vi.fn()
    render(<JobsTab route={route('daily-digest')} onToast={toast} />)
    const rows = await screen.findAllByTestId('job-row')
    const row = rows.find((r) => r.dataset.job === 'daily-digest')!
    const off = within(row).getByTestId('job-agent-off')
    expect(off.textContent).toBe('The Summarizer is off: turn it on')
    expect(off.getAttribute('data-tone')).toBe('warn')
    expect(within(off).getByRole('link').getAttribute('href')).toContain('/ai/agents/summarizer')
    expect(within(rows.find((r) => r.dataset.job === 'nightly-audit')!).queryByTestId('job-agent-off')).toBeNull()
    const callout = await screen.findByTestId('job-agent-off-callout')
    expect(callout.textContent).toContain('The Summarizer is off: Run now is refused')
    expect(within(callout).getByRole('link').getAttribute('href')).toContain('/ai/agents/summarizer')
    fireEvent.click(screen.getByTestId('job-toggle'))
    await waitFor(() => expect(api.updateJob).toHaveBeenCalledWith('daily-digest', { on: true }))
    await waitFor(() => expect(toast).toHaveBeenCalledWith('Daily digest resumed. The Summarizer is off: its runs fail until it is turned on in Administration › AI › Agents.'))
    // On, it cannot run: the state says so.
    expect(within(row).getByTestId('job-state').textContent).toBe('On, cannot run')
  })

  it('shows a failed last run’s reason on the row and in the drawer, linked to the run', async () => {
    render(<JobsTab route={route('nightly-audit')} />)
    const rows = await screen.findAllByTestId('job-row')
    const last = within(rows.find((r) => r.dataset.job === 'nightly-audit')!).getByTestId('job-last-failed')
    expect(last.textContent).toBe('Failed: the Auditor is off')
    expect(last.getAttribute('href')).toContain('run-9')
    const callout = await screen.findByTestId('job-last-failed-callout')
    expect(callout.textContent).toContain('Failed · the Auditor is off. Open the run')
    expect(within(callout).getByRole('link').getAttribute('href')).toContain('run-9')
  })
})

// --- Model and Effort columns (the owner, November 23) ---
describe('AIOps › Jobs › Schedule: each job’s agent’s model and effort', () => {
  const autoAudit: api.Job = { ...audit, setup: { model: 'auto', autoFrom: 'claude-sonnet-5-5', autoTo: 'claude-opus-5-5', provider: 'Anthropic', effort: 'auto', floorEffort: 'medium' } }
  const defAlerts: api.Job = { ...alerts, setup: { model: 'claude-opus-5-5', defaultModel: true, provider: 'Anthropic' } }
  const ownDigest: api.Job = { ...alerts, id: 'daily-digest', kind: 'daily-digest', label: 'Daily digest', setup: { model: 'claude-haiku-5-5', provider: 'Anthropic', effort: 'low' } }
  beforeAll(() => {
    Element.prototype.scrollTo = () => {}
  })
  beforeEach(() => {
    vi.mocked(api.fetchJobs).mockResolvedValue({ ...view(), jobs: [autoAudit, defAlerts, ownDigest, backup] })
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('shows them in their columns, sorts by them, hides them in the column chooser, and exports them', async () => {
    render(<JobsTab route={route()} />)
    const rows = await screen.findAllByTestId('job-row')
    const cell = (r: HTMLElement, id: string) => within(r).getByTestId(id).textContent
    expect(cell(rows[0]!, 'job-model')).toBe('Auto (Claude Sonnet 5.5 to Claude Opus 5.5)on Anthropic')
    expect(cell(rows[0]!, 'job-effort')).toBe('Autoat least medium')
    expect(cell(rows[1]!, 'job-model')).toBe('Default (Claude Opus 5.5)on Anthropic')
    expect(cell(rows[1]!, 'job-effort')).toBe('Default')
    expect(cell(rows[2]!, 'job-model')).toBe('Claude Haiku 5.5on Anthropic')
    expect(cell(rows[2]!, 'job-effort')).toBe('Low')
    // The backup has no agent.
    expect(cell(rows[3]!, 'job-model')).toBe('—')
    expect(screen.getByTestId('jobs-table').querySelector('thead')!.textContent!.replace(/[↑↓]/g, '')).toBe('JobModelEffortWhenBudgetLast runNext runState')
    // In the jobs' own order until a heading sorts them.
    fireEvent.click(within(screen.getByTestId('jobs-table')).getByRole('button', { name: /^Model/ }))
    await waitFor(() => expect(screen.getAllByTestId('job-row').map((r) => r.dataset.job)).toEqual(['nightly-audit', 'daily-digest', 'alert-diagnosis', 'backup-s3']))
    fireEvent.click(within(screen.getByTestId('jobs-table-tools')).getByRole('button', { name: 'Columns' }))
    fireEvent.click(screen.getByRole('checkbox', { name: 'Model' }))
    await waitFor(() => expect(within(screen.getAllByTestId('job-row')[0]!).queryByTestId('job-model')).toBeNull())
    fireEvent.click(screen.getByTestId('cols-all'))
    const sheet = exportTable([autoAudit, defAlerts, backup], jobColumns(true, new Map(), new Map()), Date.now())
    expect(sheet.head.slice(0, 6)).toEqual(['Job', 'Agent', 'Clusters', 'Model', 'Provider', 'Effort'])
    expect(sheet.rows.map((r) => r.slice(3, 6))).toEqual([
      ['Auto (Claude Sonnet 5.5 to Claude Opus 5.5)', 'Anthropic', 'Auto (at least medium)'],
      ['Default (Claude Opus 5.5)', 'Anthropic', 'Default'],
      ['', '', ''],
    ])
  })
})
