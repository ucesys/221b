import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from './api'
import { firstLine, MyReports } from './MyReports'
import { KnownPanel } from './KnownPanel'
import { POLL_EVERY, initials, ReportApp } from './ReportApp'
import { ReplyCard } from './ReplyCard'
import { forgetHandoffForTests, reportHref } from '../lib/reportLink'

vi.mock('./api', async (original) => {
  const real = await original<typeof import('./api')>()
  return {
    ...real,
    fetchIssueMe: vi.fn(),
    fetchMine: vi.fn(),
    fetchKnown: vi.fn(),
    sendReport: vi.fn(),
    unlinkReport: vi.fn(),
    reopenReport: vi.fn(),
    addMessage: vi.fn(),
    followKnown: vi.fn(),
    fetchUpdates: vi.fn(),
  }
})

const known: api.Known = {
  id: 'KI-1',
  title: 'Checkout times out in the EU store',
  status: 'fixInProgress',
  workaround: 'Payments are not taken twice, so it is safe to try again in a few minutes.',
  expected: 'today, 15:00',
  kind: 'normal',
  updated: '2026-11-05T10:00:00Z',
  lastUpdate: { at: '2026-11-05T10:00:00Z', by: { id: 'olga' }, text: 'cause found, fix waiting for approval' },
  link: 'https://wiki.example.com/KI-1',
  following: false,
}

const report = (n: number, over: Partial<api.Report> = {}): api.Report => ({
  number: n,
  text: `Problem number ${n}`,
  reporter: { id: 'ada', name: 'Ada Byron' },
  created: '2026-11-05T09:00:00Z',
  updated: '2026-11-05T09:30:00Z',
  status: 'new',
  statusAt: '2026-11-05T09:00:00Z',
  priority: 3,
  mine: true,
  watching: false,
  canReopen: false,
  ...over,
})

const me: api.IssueMe = { person: { id: 'ada', name: 'Ada Byron' }, report: true, operator: false, admin: false, company: 'Example IT', statusPage: 'https://wiki.example.com/status', reportURL: '/report' }

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  vi.useRealTimers()
  window.history.replaceState(null, '', '/')
})

describe('the reply card', () => {
  it('says the known issue as the operators wrote it, and links its page', () => {
    render(<ReplyCard receipt={{ report: report(143, { status: 'closed', outcome: 'knownIssue', knownIssue: 'KI-1' }), known, removed: false }} onUnlink={vi.fn()} />)
    const card = screen.getByTestId('reply-card')
    expect(card.textContent).toContain('This looks like a problem we already know about')
    expect(card.textContent).toContain('case C-143')
    expect(card.textContent).toContain('Fix in progress')
    expect(card.textContent).toContain(known.workaround)
    expect(card.textContent).toContain('today, 15:00')
    expect(card.textContent).toContain('cause found, fix waiting for approval')
    expect(screen.getByRole('link', { name: 'Read the full page' }).getAttribute('href')).toBe(known.link)
    expect(screen.queryByTestId('reply-removed')).toBeNull()
  })

  it('"No, mine is something else" unlinks it and says it goes to investigation', async () => {
    const onUnlink = vi.fn().mockResolvedValue(undefined)
    render(<ReplyCard receipt={{ report: report(7), known: { ...known, link: undefined }, removed: true }} onUnlink={onUnlink} />)
    expect(screen.queryByRole('link', { name: 'Read the full page' })).toBeNull()
    expect(screen.getByTestId('reply-removed').textContent).toContain('looked like a password')
    fireEvent.click(screen.getByRole('button', { name: 'No, mine is something else' }))
    await waitFor(() => expect(screen.getByTestId('reply-card').textContent).toContain('as a problem of its own'))
    expect(onUnlink).toHaveBeenCalledWith(7)
    expect(screen.getByTestId('reply-card').textContent).toContain('Received as case C-7')
  })

  it('without a known issue, says the number it was received as', () => {
    render(<ReplyCard receipt={{ report: report(12), removed: false }} onUnlink={vi.fn()} />)
    expect(screen.getByTestId('reply-card').textContent).toContain('Received as case C-12')
    expect(screen.queryByRole('button', { name: /something else/ })).toBeNull()
  })
})

describe('your reports', () => {
  it('shows each with its status and priority, and the explanation on a closed one', async () => {
    const list = [
      report(143, { status: 'closed', outcome: 'knownIssue', knownIssue: 'KI-1' }),
      report(138, { status: 'inProgress', priority: 0 }),
      report(131, { status: 'closed', outcome: 'fix' }),
      report(127, { status: 'closed', outcome: 'notAProblem', reason: 'A browser extension blocks the second step.' }),
      report(120),
    ]
    render(<MyReports reports={list} onReopen={vi.fn()} onMessage={vi.fn()} />)
    expect(screen.getByTestId('report-143').textContent).toContain('A known problem')
    expect(screen.getByTestId('report-138').textContent).toContain('Being worked on')
    expect(screen.getByTestId('report-131').textContent).toContain('Fixed')
    expect(screen.getByTestId('report-127').textContent).toContain('Closed: not a problem')
    expect(screen.getByTestId('report-120').textContent).toContain('Received')
    // No "It works" any more: a fixed case is closed, and Still broken opens it again.
    expect(screen.queryByRole('button', { name: 'It works' })).toBeNull()
    fireEvent.click(within(screen.getByTestId('report-127')).getByRole('button', { name: 'Why' }))
    expect(screen.getByTestId('report-127').textContent).toContain('A browser extension blocks the second step.')
    fireEvent.click(screen.getByRole('button', { name: 'Problem number 138' }))
    expect(screen.getByTestId('report-138').textContent).toContain('P0: Outage')
  })

  it('opens a closed case again only with what still happens', async () => {
    const onReopen = vi.fn().mockResolvedValue(undefined)
    render(<MyReports reports={[report(131, { status: 'closed', outcome: 'fix', canReopen: true })]} onReopen={onReopen} onMessage={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Problem number 131' }))
    fireEvent.click(screen.getByRole('button', { name: 'Still broken' }))
    const again = screen.getByRole('button', { name: 'Open again' }) as HTMLButtonElement
    expect(again.disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('What still happens'), { target: { value: 'Pay still hangs.' } })
    fireEvent.click(again)
    await waitFor(() => expect(onReopen).toHaveBeenCalledWith(131, 'Pay still hangs.'))
  })

  it('opens a report to its conversation, where the reporter answers', async () => {
    const onMessage = vi.fn().mockResolvedValue(undefined)
    const r = report(9, { waiting: '2026-11-05T09:10:00Z', messages: [{ id: 1, at: '2026-11-05T09:10:00Z', kind: 'operator', from: { id: 'olga', name: 'Olga' }, text: 'Which page?' }] })
    render(<MyReports reports={[r]} onReopen={vi.fn()} onMessage={onMessage} />)
    expect(screen.getByTestId('report-9').textContent).toContain('We asked you something')
    fireEvent.click(screen.getByRole('button', { name: 'Problem number 9' }))
    expect(screen.getByRole('list', { name: 'Conversation' }).textContent).toContain('Which page?')
    fireEvent.change(screen.getByLabelText('Add to case C-9'), { target: { value: 'The basket page.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(onMessage).toHaveBeenCalledWith(9, 'The basket page.'))
  })

  it('cuts a long first line', () => {
    expect(firstLine('a'.repeat(200), 10)).toBe('aaaaaaaaa…')
    expect(firstLine('first\nsecond')).toBe('first')
    expect(firstLine(undefined)).toBe('')
  })
})

describe('known problems right now', () => {
  it('follows a known issue and links the status page', async () => {
    const onFollow = vi.fn().mockResolvedValue(undefined)
    const planned: api.Known = { ...known, id: 'KI-2', title: 'Database maintenance', kind: 'planned', status: 'identified', window: { start: '2026-11-08T02:00:00Z', end: '2026-11-08T04:00:00Z' }, link: undefined }
    render(<KnownPanel known={[known, planned]} statusPage="https://wiki.example.com/status" onFollow={onFollow} />)
    const list = screen.getByTestId('known-list')
    expect(list.textContent).toContain('Planned: Database maintenance')
    fireEvent.click(within(list).getAllByRole('button', { name: 'Follow' })[0]!)
    await waitFor(() => expect(onFollow).toHaveBeenCalledWith('KI-1', true))
    expect(screen.getByRole('link', { name: 'All known problems, on Confluence' }).getAttribute('href')).toBe('https://wiki.example.com/status')
  })

  it('leaves the status page out when the settings name none', () => {
    render(<KnownPanel known={[]} statusPage="" onFollow={vi.fn()} />)
    expect(screen.queryByRole('link', { name: /Confluence/ })).toBeNull()
    expect(screen.getByText('None just now.')).toBeTruthy()
  })
})

describe('the report page', () => {
  beforeEach(() => {
    vi.mocked(api.fetchIssueMe).mockResolvedValue(me)
    vi.mocked(api.fetchMine).mockResolvedValue([report(3, { status: 'closed', outcome: 'fix' })])
    vi.mocked(api.fetchKnown).mockResolvedValue([known])
    vi.mocked(api.fetchUpdates).mockResolvedValue({ updates: [], now: '2026-11-05T10:00:00Z' })
  })

  it('fills the box from ?text= and never sends it by itself', async () => {
    window.history.replaceState(null, '', '/report?text=' + encodeURIComponent('Checkout hangs'))
    render(<ReportApp />)
    await waitFor(() => expect(screen.getByText('Signed in as Ada Byron')).toBeTruthy())
    expect((screen.getByTestId('report-text') as HTMLTextAreaElement).value).toBe('Checkout hangs')
    expect(api.sendReport).not.toHaveBeenCalled()
    expect(screen.getByText('Example IT')).toBeTruthy()
    expect(screen.getByText(/Don't paste passwords or tokens/)).toBeTruthy()
  })

  // Phase 12 (CS-15, ADR-89): a link from the console hands its text over.
  it('fills the box from the console’s handover once, and never sends it by itself', async () => {
    const url = reportHref('/report', { text: 'I asked Sherlock about prod: why is shop down?' })
    window.history.replaceState(null, '', url)
    render(<ReportApp />)
    await waitFor(() => expect(screen.getByText('Signed in as Ada Byron')).toBeTruthy())
    expect((screen.getByTestId('report-text') as HTMLTextAreaElement).value).toBe('I asked Sherlock about prod: why is shop down?')
    expect(screen.getByTestId('handoff-note').textContent).toContain('nothing is sent until you press Send')
    expect(api.sendReport).not.toHaveBeenCalled()
    // Read once: the stored text and the fragment are gone.
    expect(Object.keys(localStorage).filter((k) => k.startsWith('221b.report.handoff.'))).toEqual([])
    expect(window.location.hash).toBe('')
    fireEvent.change(screen.getByTestId('report-text'), { target: { value: 'Shop is down for me too.' } })
    expect(screen.queryByTestId('handoff-note')).toBeNull()
    forgetHandoffForTests()
  })

  it('sends what was written and shows the reply card', async () => {
    vi.mocked(api.sendReport).mockResolvedValue({ report: report(144, { status: 'closed', outcome: 'knownIssue', knownIssue: 'KI-1' }), known, removed: false })
    render(<ReportApp />)
    const box = await screen.findByTestId('report-text')
    fireEvent.change(box, { target: { value: '  Checkout page hangs after I press Pay  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(screen.getByTestId('reply-card').textContent).toContain('already know about'))
    expect(api.sendReport).toHaveBeenCalledWith('Checkout page hangs after I press Pay', { for: undefined, priority: undefined, cluster: undefined })
    expect((screen.getByTestId('report-text') as HTMLTextAreaElement).value).toBe('')
  })

  it('shows the known problems what is typed may be, with their workarounds, never anyone’s cases (wave 6)', async () => {
    render(<ReportApp />)
    const box = await screen.findByTestId('report-text')
    await waitFor(() => expect(api.fetchKnown).toHaveBeenCalled())
    fireEvent.change(box, { target: { value: 'The checkout in the store times out' } })
    const maybe = await screen.findByTestId('report-maybe-known', {}, { timeout: 2000 })
    expect(maybe.textContent).toContain('Checkout times out in the EU store')
    expect(maybe.textContent).toContain('safe to try again')
    // Sending stays the person's choice.
    expect((screen.getByRole('button', { name: 'Send' }) as HTMLButtonElement).disabled).toBe(false)
    fireEvent.change(box, { target: { value: 'My printer is jammed' } })
    await waitFor(() => expect(screen.queryByTestId('report-maybe-known')).toBeNull(), { timeout: 2000 })
  })

  it('files for someone else with ?for= only for an operator', async () => {
    window.history.replaceState(null, '', '/report?for=bob&text=VPN')
    vi.mocked(api.sendReport).mockResolvedValue({ report: report(5), removed: false })
    const { unmount } = render(<ReportApp />)
    await screen.findByText('Signed in as Ada Byron')
    expect(screen.queryByTestId('for-note')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(api.sendReport).toHaveBeenCalledWith('VPN', expect.objectContaining({ for: undefined })))
    unmount()
    vi.mocked(api.fetchIssueMe).mockResolvedValue({ ...me, operator: true })
    render(<ReportApp />)
    await waitFor(() => expect(screen.getByTestId('for-note').textContent).toContain('Reporting for bob'))
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(api.sendReport).toHaveBeenLastCalledWith('VPN', expect.objectContaining({ for: 'bob' })))
  })

  // CF-64: the reporter may choose the priority, each with what it means,
  // and the cluster by its name; left alone, nothing is sent for them.
  it('sends the priority and cluster the reporter chose, each option saying what it means', async () => {
    vi.mocked(api.fetchIssueMe).mockResolvedValue({ ...me, clusters: [{ id: 'local', name: 'local' }, { id: 'c-prod', name: 'Production' }] })
    vi.mocked(api.sendReport).mockResolvedValue({ report: report(150, { priority: 1 }), removed: false })
    render(<ReportApp />)
    const box = await screen.findByTestId('report-text')
    const prio = screen.getByLabelText('How bad is it?') as HTMLSelectElement
    expect(prio.value).toBe('')
    expect(Array.from(prio.options).map((o) => o.text)).toEqual([
      'Not sure',
      "P1: Blocking (you can't work, and nothing gets around it)",
      'P2: Service degradation (slow or partly broken, but you can work)',
      'P3: Preemptive (not broken yet, but will be if left)',
      'P4: Question (a question or a request)',
    ])
    const cluster = (await screen.findByLabelText('Which cluster?')) as HTMLSelectElement
    expect(Array.from(cluster.options).map((o) => [o.value, o.text])).toEqual([
      ['', 'Not sure'],
      ['local', 'local'],
      ['c-prod', 'Production'],
    ])
    fireEvent.change(box, { target: { value: 'Nothing loads for the whole team' } })
    fireEvent.change(prio, { target: { value: '1' } })
    fireEvent.change(cluster, { target: { value: 'c-prod' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(api.sendReport).toHaveBeenCalledWith('Nothing loads for the whole team', { for: undefined, priority: 1, cluster: 'c-prod' }))
    // Sent, the choices go back to Not sure.
    await waitFor(() => expect((screen.getByLabelText('How bad is it?') as HTMLSelectElement).value).toBe(''))
    expect((screen.getByLabelText('Which cluster?') as HTMLSelectElement).value).toBe('')
  })

  it('offers no cluster to choose when none is connected', async () => {
    render(<ReportApp />)
    await screen.findByText('Signed in as Ada Byron')
    expect(screen.getByTestId('report-priority')).toBeTruthy()
    expect(screen.queryByTestId('report-cluster')).toBeNull()
  })

  it('shows the reports alone at /report/mine', async () => {
    window.history.replaceState(null, '', '/report/mine')
    render(<ReportApp />)
    await screen.findByTestId('my-reports')
    expect(screen.queryByTestId('report-text')).toBeNull()
    expect(screen.getByRole('link', { name: 'Report a new problem' }).getAttribute('href')).toBe('/report')
  })

  it('says what changed, asked for every half minute', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    vi.mocked(api.fetchUpdates).mockResolvedValue({ updates: [{ at: '2026-11-05T10:01:00Z', report: 3, text: '#3 is fixed; say whether it works.' }], now: '2026-11-05T10:01:00Z' })
    render(<ReportApp />)
    await screen.findByTestId('my-reports')
    expect(api.fetchUpdates).not.toHaveBeenCalled()
    await act(async () => {
      vi.advanceTimersByTime(POLL_EVERY)
    })
    await waitFor(() => expect(screen.getByRole('region', { name: 'What changed' }).textContent).toContain('#3 is fixed'))
    expect(api.fetchMine).toHaveBeenCalledTimes(2)
  })

  it('makes initials of a name', () => {
    expect(initials('Ada Byron')).toBe('AB')
    expect(initials('ada.byron@corp.example')).toBe('AE')
    expect(initials('pat')).toBe('P')
    expect(initials('')).toBe('?')
  })
})

describe('matching known problems (wave 6)', () => {
  it('keeps open known issues sharing words, best first, at most three', () => {
    const k = (id: string, title: string, over: Partial<api.Known> = {}): api.Known => ({ ...known, id, title, ...over })
    const list = [k('KI-1', 'Checkout times out in the EU store'), k('KI-2', 'VPN drops every hour'), k('KI-3', 'Checkout slow', { status: 'resolved' })]
    expect(api.matchKnown('checkout times out', list).map((x) => x.id)).toEqual(['KI-1'])
    expect(api.matchKnown('the connection drops every hour', list).map((x) => x.id)).toEqual(['KI-2'])
    expect(api.matchKnown('checkout', list)).toEqual([])
    expect(api.matchKnown('my printer jams with paper', list)).toEqual([])
  })

  it('never suggests a known issue made inactive as a duplicate (CF-63)', () => {
    const list: api.Known[] = [{ ...known, id: 'KI-1' }, { ...known, id: 'KI-9', inactive: true }]
    expect(api.matchKnown('checkout times out', list).map((x) => x.id)).toEqual(['KI-1'])
  })
})

describe('the pills', () => {
  it('reads a status as a reporter would', () => {
    expect(api.statusPill({ status: 'closed', outcome: 'noFixNeeded' })).toEqual({ text: 'Closed: no fix needed', tone: 'muted' })
    expect(api.statusPill({ status: 'closed', outcome: 'fix' })).toEqual({ text: 'Fixed', tone: 'ok' })
    expect(api.statusPill({ status: 'new' }).text).toBe('Received')
    // Asked something, the reporter reads that it waits for their answer (CF-67).
    expect(api.statusPill({ status: 'inProgress', waiting: '2026-11-05T09:10:00Z' })).toEqual({ text: 'Waiting for your answer', tone: 'warn' })
    expect(api.statusPill({ status: 'closed', outcome: 'noFixNeeded', waiting: '2026-11-05T09:10:00Z' }).text).toBe('Closed: no fix needed')
    expect(api.priorityText(0)).toBe('P0: Outage')
    expect(api.priorityText(4)).toBe('P4: Question')
    expect(api.knownPill({ status: 'fixInProgress', kind: 'normal' })).toEqual({ text: 'Fix in progress', tone: 'warn' })
  })
})
