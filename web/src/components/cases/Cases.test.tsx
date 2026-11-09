import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as issues from '../../api/issues'
import { fetchApprovals } from '../../api/automation'
import { fetchCases, type CaseBrief } from '../../api/casework'
import type { Me } from '../../api/types'
import { forgetSharedForTests, setIssueMeForTests } from '../../lib/cases'
import { parseHash } from '../../lib/router'
import { servicesForAlert } from './AlertReports'
import { CasesPage } from './CasesPage'
import { inputOf, KnownDrawer, KnownTab } from './KnownTab'
import type { CasesAccess } from '../../lib/cases'
import { MYCROFT_TABLE_MIN } from './mycroftTable'

vi.mock('../../api/issues', async (original) => {
  const real = await original<typeof import('../../api/issues')>()
  return {
    ...real,
    fetchIssueMe: vi.fn(() => new Promise(() => undefined)),
    fetchReports: vi.fn(),
    fetchReport: vi.fn(),
    fetchKnownAll: vi.fn(),
    fetchKnownIssue: vi.fn(),
    fetchServices: vi.fn(),
    previewMatch: vi.fn(),
    triageReport: vi.fn(),
    sendMessage: vi.fn(),
    draftReply: vi.fn(),
    sendDraft: vi.fn(),
    discardDraft: vi.fn(),
    createKnown: vi.fn(),
    saveKnown: vi.fn(),
    addKnownUpdate: vi.fn(),
    acceptKnown: vi.fn(),
    followKnownIssue: vi.fn(),
  }
})
vi.mock('../../api/automation', async (original) => ({ ...(await original<typeof import('../../api/automation')>()), fetchApprovals: vi.fn() }))
vi.mock('../../api/casework', async (original) => ({ ...(await original<typeof import('../../api/casework')>()), fetchCases: vi.fn() }))

const admin: Me = { mode: 'oidc', user: { subject: 's', username: 'ann', name: 'Ann' }, admin: true, roles: ['superAdmin'], method: 'oidc', sessionIdleSeconds: 0 }
const viewer: Me = { ...admin, admin: false, roles: ['viewer'] }
const op = { person: { id: 'ann', name: 'Ann' }, report: true, operator: true, admin: true, company: '', statusPage: '', reportURL: '/report' }

const report = (n: number, over: Partial<issues.OpReport> = {}): issues.OpReport => ({
  number: n,
  text: `Checkout hangs after Pay, number ${n}`,
  reporter: { id: 'bob', name: 'Bob' },
  created: `2026-11-05T0${n}:00:00Z`,
  updated: `2026-11-05T0${n}:00:00Z`,
  status: 'new',
  statusAt: '2026-11-05T00:00:00Z',
  priority: 3,
  mine: false,
  watching: false,
  canReopen: false,
  ...over,
})

/** A problem report, as the Inbox reads it: a case from the issue service (CU-01). */
const reportedCase = (n: number, over: Partial<CaseBrief> = {}): CaseBrief => ({
  id: `C-${n}`,
  number: n,
  title: `Checkout hangs after Pay, number ${n}`,
  priority: 3,
  status: 'new',
  pending: true,
  agentFound: false,
  slaApplies: true,
  source: { kind: 'report' },
  createdBy: 'Bob',
  created: `2026-11-05T0${n}:00:00Z`,
  updated: `2026-11-05T0${n}:00:00Z`,
  since: `2026-11-05T0${n}:00:00Z`,
  ...over,
})

const known = (id: string, over: Partial<issues.OpKnown> = {}): issues.OpKnown => ({ id, title: `Known ${id}`, status: 'investigating', kind: 'normal', updated: '2026-11-05T00:00:00Z', following: false, source: 'operator', created: '2026-11-05T00:00:00Z', ...over })

beforeEach(() => {
  // jsdom has no scrollTo on elements; the drawer scrolls its body to the top.
  Element.prototype.scrollTo = () => {}
  vi.mocked(issues.fetchServices).mockResolvedValue([{ id: 'shop', name: 'Shop' }])
  vi.mocked(issues.fetchKnownAll).mockResolvedValue([known('KI-1'), known('KI-2', { draft: true, source: 'agent' })])
  vi.mocked(issues.fetchReports).mockResolvedValue([report(1), report(2, { owner: { id: 'ann' } }), report(3, { owner: { id: 'cy' } })])
  vi.mocked(issues.previewMatch).mockResolvedValue({ known: [{ id: 'KI-1', title: 'Known KI-1', confidence: 41 }], services: [], threshold: 80 })
  vi.mocked(fetchCases).mockResolvedValue({ cases: [reportedCase(1), reportedCase(2, { assignee: { id: 'ann' } })], clusters: [], mayChange: true, me: 'ann' })
  vi.mocked(fetchApprovals).mockResolvedValue({ items: [{ kind: 'script', state: 'pending', what: 'reserve v2', requestedBy: 'cy', script: 'reserve', version: 2 }] })
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  forgetSharedForTests()
  setIssueMeForTests(null, false)
  window.history.replaceState(null, '', '/')
})

describe('the Cases shell (CS-01, CS-02)', () => {
  it('opens the Inbox for an operator, as one table with its reports as cases (IN-01)', async () => {
    setIssueMeForTests(op)
    render(<CasesPage route={parseHash('#/cases')} me={admin} onToast={() => undefined} />)
    expect(await screen.findByTestId('cases-inbox')).toBeTruthy()
    await waitFor(() => expect(document.querySelector('[data-inbox-row="case:1"]')).toBeTruthy())
    expect(document.querySelector('[data-inbox-row="approval:script-reserve-v2"]')).toBeTruthy()
    expect(document.querySelector('[data-inbox-row="known:KI-2"]')).toBeTruthy()
    // One table, no groups and no left pane.
    expect(screen.queryByTestId('inbox-groups')).toBeNull()
    expect(screen.getAllByRole('grid')).toHaveLength(1)
  })

  it('opens the Inbox for someone who may only report, with only cases and nothing to triage', async () => {
    setIssueMeForTests({ ...op, operator: false, admin: false })
    // Their own cases, which they do not work: the server says so with the list.
    vi.mocked(fetchCases).mockResolvedValue({ cases: [reportedCase(1, { createdBy: 'ann' })], clusters: [], mayChange: false, me: 'ann' })
    render(<CasesPage route={parseHash('#/cases/reports')} me={viewer} onToast={() => undefined} />)
    expect(await screen.findByTestId('cases-inbox')).toBeTruthy()
    // Their own case, with no Kind filter (CF-38), and no box to pick it: they do not work the cases.
    await waitFor(() => expect(document.querySelector('[data-inbox-row="case:1"]')).toBeTruthy())
    expect(screen.queryByTestId('inbox-kinds')).toBeNull()
    expect(screen.queryByTestId('inbox-pick')).toBeNull()
    // No On behalf of, and no New Case: only those who work the cases see it.
    expect(screen.queryByTestId('cases-on-behalf')).toBeNull()
    expect(screen.queryByTestId('cases-new-case')).toBeNull()
  })

  it('says so to someone with nothing there', async () => {
    setIssueMeForTests({ ...op, operator: false, report: false, admin: false })
    render(<CasesPage route={parseHash('#/cases')} me={viewer} onToast={() => undefined} />)
    expect(await screen.findByTestId('cases-refusal')).toBeTruthy()
  })

  it('waits for the issue service before choosing a tab', () => {
    render(<CasesPage route={parseHash('#/cases')} me={admin} onToast={() => undefined} />)
    expect(screen.queryByTestId('cases-inbox')).toBeNull()
    expect(screen.getByText('Loading…')).toBeTruthy()
  })

  it('renders the other packages’ tabs through their files', async () => {
    setIssueMeForTests(op)
    render(<CasesPage route={parseHash('#/cases/jobs')} me={admin} onToast={() => undefined} />)
    expect(await screen.findByTestId('jobs-tab')).toBeTruthy()
  })
})

// The Inbox's own tests are in Inbox.test.tsx and inboxRows.test.ts.

// The triage drawer's tests moved to CaseDrawer.test.tsx: a report is a case (Phase 12, wave 5).

describe('the known-issue editor (CS-13, CS-14)', () => {
  beforeEach(() => setIssueMeForTests(op))

  it('saves only what changed on an existing one', () => {
    const k = known('KI-1', { symptoms: ['slow'], workaround: 'wait' })
    const d = { title: 'Known KI-1', symptoms: 'slow\nhangs', services: [], status: 'identified' as const, workaround: 'wait', expected: '', owner: '', link: '', kind: 'normal' as const, start: '', end: '' }
    expect(inputOf(d, k)).toEqual({ symptoms: ['slow', 'hangs'], status: 'identified' })
  })

  it('starts from a report, and links it and its alike reports once created', async () => {
    vi.mocked(issues.fetchReport).mockResolvedValue(report(7, { service: 'shop', similar: [5] }))
    vi.mocked(issues.createKnown).mockResolvedValue(known('KI-9'))
    vi.mocked(issues.triageReport).mockResolvedValue(report(7))
    const onSaved = vi.fn()
    render(<KnownDrawer id="new" from={7} operator onClose={() => undefined} onSaved={onSaved} />)
    await waitFor(() => expect((screen.getByTestId('known-title') as HTMLInputElement).value).toBe('Checkout hangs after Pay, number 7'))
    expect((await screen.findByTestId('known-service-shop') as HTMLInputElement).checked).toBe(true)
    fireEvent.change(screen.getByTestId('known-workaround'), { target: { value: 'Try again later.' } })
    fireEvent.click(screen.getByTestId('known-save'))
    await waitFor(() => expect(issues.triageReport).toHaveBeenCalledWith(5, { knownIssue: 'KI-9' }))
    expect(issues.createKnown).toHaveBeenCalledWith(expect.objectContaining({ title: 'Checkout hangs after Pay, number 7', services: ['shop'], workaround: 'Try again later.', symptoms: ['Checkout hangs after Pay, number 7'], owner: { id: 'ann' } }))
    expect(issues.triageReport).toHaveBeenCalledWith(7, { knownIssue: 'KI-9' })
    expect(onSaved).toHaveBeenLastCalledWith(expect.objectContaining({ id: 'KI-9' }), true)
  })

  it('adds a dated update with a status', async () => {
    vi.mocked(issues.fetchKnownIssue).mockResolvedValue(known('KI-2', { draft: true, source: 'agent', updates: [{ at: '2026-11-05T00:00:00Z', by: { id: 'agent' }, text: 'Proposed.' }] }))
    vi.mocked(issues.addKnownUpdate).mockResolvedValue(known('KI-2', { status: 'identified' }))
    render(<KnownDrawer id="KI-2" from={0} operator onClose={() => undefined} onSaved={() => undefined} />)
    expect((await screen.findByTestId('known-updates')).textContent).toContain('Proposed.')
    fireEvent.change(screen.getByTestId('known-update-text'), { target: { value: 'Cause found.' } })
    fireEvent.change(screen.getByTestId('known-update-status'), { target: { value: 'identified' } })
    fireEvent.click(screen.getByTestId('known-update-add'))
    await waitFor(() => expect(issues.addKnownUpdate).toHaveBeenCalledWith('KI-2', 'Cause found.', 'identified'))
  })

  it('accepts a proposal', async () => {
    vi.mocked(issues.fetchKnownIssue).mockResolvedValue(known('KI-2', { draft: true, source: 'agent' }))
    vi.mocked(issues.acceptKnown).mockResolvedValue(known('KI-2'))
    render(<KnownDrawer id="KI-2" from={0} operator onClose={() => undefined} onSaved={() => undefined} />)
    fireEvent.click(await screen.findByTestId('known-accept'))
    await waitFor(() => expect(issues.acceptKnown).toHaveBeenCalledWith('KI-2'))
    await waitFor(() => expect(screen.queryByTestId('known-accept')).toBeNull())
  })

  it('shows how often the agents still see it, and the cases closed as it (wave 6)', async () => {
    vi.mocked(issues.fetchKnownIssue).mockResolvedValue(known('KI-3', { status: 'resolved', stillPresent: { count: 4, last: '2026-11-06T06:00:00Z' }, cases: ['C-20', 'C-21'] }))
    render(<KnownDrawer id="KI-3" from={0} operator onClose={() => undefined} onSaved={() => undefined} />)
    const still = await screen.findByTestId('known-still-present')
    expect(still.textContent).toContain('×4')
    expect(still.textContent).toContain('the next run of each job that saw it checks again')
    const linked = screen.getByTestId('known-linked')
    expect([...linked.querySelectorAll('a')].map((a) => a.getAttribute('href'))).toEqual(['#/cases/inbox/case/20', '#/cases/inbox/case/21'])
  })

  it('lets someone who is not an operator read it and follow it, not edit it', async () => {
    vi.mocked(issues.fetchKnownIssue).mockResolvedValue(known('KI-1', { workaround: 'Wait.' }))
    vi.mocked(issues.followKnownIssue).mockResolvedValue(known('KI-1', { following: true }))
    render(<KnownDrawer id="KI-1" from={0} operator={false} onClose={() => undefined} onSaved={() => undefined} />)
    expect(await screen.findByText('Wait.')).toBeTruthy()
    expect(screen.queryByTestId('known-editor')).toBeNull()
    fireEvent.click(screen.getByTestId('known-follow'))
    await waitFor(() => expect(issues.followKnownIssue).toHaveBeenCalledWith('KI-1', true))
  })
})

describe('the known issues table (wave 6g)', () => {
  const opAccess: CasesAccess = { show: true, approver: true, operator: true, reporter: true, console: true, tabs: ['inbox', 'jobs', 'mycroft'] }
  // As Mycroft hands it over: the sub-tab's path, and the show filter in the query.
  const at = (q = '') => ({ ...parseHash('#/cases/mycroft/known' + q), path: ['known'] })
  const ids = () => screen.getAllByTestId(/^known-row-/).map((r) => r.getAttribute('data-testid')!.replace('known-row-', ''))
  const head = (col: string) => document.querySelector(`[data-testid="known-table"] button[data-col="${col}"]`)!

  it('sorts by Updated, newest first, and the other way on a click; by its other headings too', async () => {
    vi.mocked(issues.fetchKnownAll).mockResolvedValue([
      known('KI-1', { updated: '2026-11-05T00:00:00Z', title: 'Bravo' }),
      known('KI-3', { updated: '2026-11-07T00:00:00Z', title: 'Alpha' }),
      known('KI-4', { updated: '2026-11-01T00:00:00Z', title: 'Charlie' }),
    ])
    render(<KnownTab route={at()} access={opAccess} />)
    await waitFor(() => expect(ids()).toEqual(['KI-3', 'KI-1', 'KI-4']))
    expect(head('updated').closest('th')!.getAttribute('aria-sort')).toBe('descending')
    // As wide as Mycroft's other tables (build 14).
    expect(screen.getByTestId('known-table').style.minWidth).toBe(`${MYCROFT_TABLE_MIN}px`)
    fireEvent.click(head('updated'))
    expect(ids()).toEqual(['KI-4', 'KI-1', 'KI-3'])
    expect(head('updated').closest('th')!.getAttribute('aria-sort')).toBe('ascending')
    fireEvent.click(head('title'))
    expect(ids()).toEqual(['KI-3', 'KI-1', 'KI-4'])
    fireEvent.click(head('id'))
    expect(ids()).toEqual(['KI-4', 'KI-3', 'KI-1'])
  })

  it('keeps the inactive ones out of Open and shows them under Inactive, with what they duplicate', async () => {
    vi.mocked(issues.fetchKnownAll).mockResolvedValue([known('KI-1'), known('KI-5', { inactive: true, duplicateOf: 'KI-1' })])
    render(<KnownTab route={at()} access={opAccess} />)
    await waitFor(() => expect(ids()).toEqual(['KI-1']))
    expect(screen.getByTestId('known-show-inactive')).toBeTruthy()
    cleanup()
    render(<KnownTab route={at('?show=inactive')} access={opAccess} />)
    await waitFor(() => expect(ids()).toEqual(['KI-5']))
    expect(screen.getByTestId('known-show-inactive').getAttribute('aria-pressed')).toBe('true')
    const tag = screen.getByTestId('known-inactive-KI-5')
    expect(tag.textContent).toBe('inactive: duplicate of KI-1')
    expect(tag.querySelector('a')!.getAttribute('href')).toMatch(/known\/KI-1$/)
    cleanup()
    render(<KnownTab route={at('?show=all')} access={opAccess} />)
    await waitFor(() => expect(ids().sort()).toEqual(['KI-1', 'KI-5']))
  })

  it('lets an operator make an inactive one active again', async () => {
    vi.mocked(issues.fetchKnownIssue).mockResolvedValue(known('KI-5', { inactive: true, duplicateOf: 'KI-1' }))
    vi.mocked(issues.saveKnown).mockResolvedValue(known('KI-5'))
    const toast = vi.fn()
    render(<KnownDrawer id="KI-5" from={0} operator onClose={() => undefined} onSaved={() => undefined} onToast={toast} />)
    expect((await screen.findByTestId('known-inactive')).textContent).toContain('duplicate of KI-1')
    fireEvent.click(screen.getByTestId('known-activate'))
    await waitFor(() => expect(issues.saveKnown).toHaveBeenCalledWith('KI-5', { inactive: false }))
    expect(toast).toHaveBeenCalledWith('KI-5 is active again: reports and cases may match it.')
    await waitFor(() => expect(screen.queryByTestId('known-activate')).toBeNull())
    expect(screen.queryByTestId('known-inactive')).toBeNull()
  })

  it('offers no Make active to someone who only reads', async () => {
    vi.mocked(issues.fetchKnownIssue).mockResolvedValue(known('KI-5', { inactive: true, duplicateOf: 'KI-1' }))
    render(<KnownDrawer id="KI-5" from={0} operator={false} onClose={() => undefined} onSaved={() => undefined} />)
    expect(await screen.findByTestId('known-inactive')).toBeTruthy()
    expect(screen.queryByTestId('known-activate')).toBeNull()
  })
})

describe('alerts on what a service runs on (CS-20)', () => {
  const svcs: issues.IssueService[] = [
    { id: 'shop', name: 'Shop', runsOn: [{ kind: 'workload', value: 'shop/web' }] },
    { id: 'db', name: 'DB', runsOn: [{ kind: 'host', value: 'worker-3', cluster: 'prod' }] },
    { id: 'all', name: 'Platform', runsOn: [{ kind: 'namespace', value: 'shop', cluster: 'other' }] },
  ]

  it('finds the services an alert’s pod or node is part of', () => {
    expect(servicesForAlert({ kind: 'pod', name: 'web-7f9c4d-x2', namespace: 'shop' }, ['local', 'prod'], svcs).map((s) => s.id)).toEqual(['shop'])
    expect(servicesForAlert({ kind: 'pod', name: 'web-7f9c4d-x2', namespace: 'other' }, ['local', 'prod'], svcs)).toEqual([])
    expect(servicesForAlert({ kind: 'node', name: 'worker-3' }, ['local', 'prod'], svcs).map((s) => s.id)).toEqual(['db'])
    expect(servicesForAlert({ kind: 'node', name: 'worker-3' }, ['local', 'dev'], svcs)).toEqual([])
  })
})
