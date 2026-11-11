import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '../../api/rules'
import { parseHash } from '../../lib/router'
import { JobsArea } from './JobsArea'
import { behindOf, casesState, InboxSuggestionDrawer, meaningOf, originText, promoteOf, rowOf, rowsOf, rowsShown, rulePatchOf, RulesView } from './RulesView'

vi.mock('../../api/rules', async (original) => ({
  ...(await original<typeof import('../../api/rules')>()),
  fetchRules: vi.fn(),
  addRule: vi.fn(),
  editRule: vi.fn(),
  retireRule: vi.fn(),
  activateRule: vi.fn(),
  replaceRule: vi.fn(),
  deleteRule: vi.fn(),
  promoteRule: vi.fn(),
  dismissSuggestion: vi.fn(),
}))
vi.mock('../../api/lessons', async (original) => ({
  ...(await original<typeof import('../../api/lessons')>()),
  fetchExceptions: vi.fn(() => Promise.resolve({ exceptions: [], mayChange: false })),
}))
vi.mock('../../api/fixes', async (original) => ({
  ...(await original<typeof import('../../api/fixes')>()),
  fetchFixes: vi.fn(() => Promise.resolve({ fixes: [], mayChange: false })),
}))

const rule = (id: string, over: Partial<api.Rule> = {}): api.Rule => ({
  id,
  says: `means ${id}`,
  type: 'security',
  jobs: ['nightly-audit'],
  builtIn: true,
  state: 'active',
  openCases: 0,
  cases: 0,
  exceptions: 0,
  findings: 0,
  used: false,
  ...over,
})

const JOBS: api.RuleJob[] = [
  { id: 'nightly-audit', label: 'Nightly audit' },
  { id: 'alert-diagnosis', label: 'Alert diagnosis' },
  { id: 'daily-digest', label: 'Daily digest' },
]

const PAGE: api.RulesPage = {
  rules: [
    rule('pod.privileged', {
      openCases: 2,
      cases: 3,
      exceptions: 1,
      findings: 5,
      runs: 4,
      clusters: ['prod', 'dev'],
      lastFound: '2026-11-07T02:00:00Z',
      used: true,
      caseList: [
        { id: 'C-7', title: 'web runs privileged', status: 'new', priority: 3, open: true },
        { id: 'C-5', title: 'api runs privileged', status: 'inProgress', priority: 3, open: true },
        { id: 'C-2', title: 'old', status: 'closed', outcome: 'exception', priority: 3, open: false },
      ],
      runIds: ['run-9', 'run-4'],
      latest: [{ run: 'run-9', id: 'run-9.1', at: '2026-11-07T02:00:00Z', cluster: 'prod', object: 'shop/Pod/web', title: 'web runs privileged', routing: 'seen', case: 'C-7' }],
      exceptionIds: ['EX-3'],
    }),
    rule('pod.crashloop', { type: 'availability', jobs: ['alert-diagnosis'], aliases: ['pod.restart-loop'], openCases: 1, used: true }),
    rule('node.not-ready', { type: 'availability', jobs: ['nightly-audit', 'alert-diagnosis'], edited: true }),
    rule('pod.host-path', { builtIn: false, type: 'security' }),
    rule('pod.root-user', { state: 'retired', used: true }),
    rule('pod.restart-loop', { builtIn: false, state: 'replaced', replacedBy: 'pod.crashloop', used: true }),
  ],
  found: [
    {
      id: 'other.etcd-slow',
      findings: 4,
      runs: 4,
      clusters: ['prod', 'dev'],
      jobs: ['nightly-audit'],
      cases: 2,
      openCases: 1,
      exceptions: 0,
      title: 'etcd is slow',
      type: 'other',
      first: '2026-11-04T02:00:00Z',
      last: '2026-11-07T02:00:00Z',
      caseList: [
        { id: 'C-9', title: 'etcd is slow on cp-1', status: 'new', priority: 3, open: true },
        { id: 'C-8', title: 'etcd is slow on cp-9', status: 'closed', outcome: 'noAction', priority: 3, open: false },
      ],
      runIds: ['run-12', 'run-1'],
      latest: [
        { run: 'run-12', id: 'run-12.2', at: '2026-11-07T02:00:00Z', cluster: 'prod', object: 'Node/cp-1', title: 'etcd is slow', routing: 'seen', case: 'C-9' },
        { run: 'run-1', id: 'run-1.1', at: '2026-11-04T02:00:00Z', cluster: 'dev', object: 'Node/cp-9', title: 'etcd is slow', routing: 'ignored' },
      ],
      exceptionIds: [],
      titles: [{ text: 'etcd is slow', n: 4 }],
      kinds: { Node: 4 },
      descriptions: [{ job: 'nightly-audit', kind: 'nightly-audit', text: 'etcd answers writes late. No rule given is about the control plane’s store.', run: 'run-12', at: '2026-11-07T02:00:00Z' }],
    },
    {
      id: 'other.dns-timeout',
      findings: 5,
      runs: 5,
      clusters: ['prod'],
      jobs: ['nightly-audit'],
      cases: 0,
      openCases: 0,
      exceptions: 0,
      title: 'DNS times out',
      type: 'other',
      last: '2026-11-06T02:00:00Z',
      suggestion: 'node.dns-timeout',
      runIds: ['run-11'],
      latest: [],
      caseList: [],
      exceptionIds: [],
    },
  ],
  suggestions: [
    {
      id: 'node.dns-timeout',
      says: 'DNS times out',
      type: 'other',
      jobs: ['nightly-audit'],
      from: ['other.dns-timeout', 'other.dns-timeout-errors'],
      runs: 6,
      clusters: 1,
      created: '2026-11-07T03:30:00Z',
      why: 'Found 6 times in 6 runs on 1 cluster by Nightly audit; grouped with other.dns-timeout-errors because their ids are alike in words (dns timeout; dns timeout errors).',
      descriptions: [{ id: 'other.dns-timeout', job: 'nightly-audit', text: 'Lookups time out. No rule is about DNS.', run: 'run-11', at: '2026-11-06T02:00:00Z' }],
    },
  ],
  jobs: JOBS,
  types: ['security', 'outage', 'performance', 'availability', 'configuration', 'capacity', 'compliance', 'question', 'other'],
  clusterNames: { prod: 'Production', dev: 'dev' },
  mayChange: true,
}

const ids = () => screen.getAllByTestId('rule-row').map((r) => r.getAttribute('data-id'))

describe('AIOps › Mycroft › Validation rules (ADR-138)', () => {
  beforeAll(() => {
    Element.prototype.scrollIntoView = () => {}
    Element.prototype.scrollTo = () => {}
  })
  beforeEach(() => {
    vi.mocked(api.fetchRules).mockResolvedValue(PAGE)
    for (const f of [api.addRule, api.editRule, api.retireRule, api.activateRule, api.replaceRule, api.deleteRule, api.promoteRule, api.dismissSuggestion]) vi.mocked(f).mockResolvedValue(PAGE)
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
    window.history.replaceState(null, '', '/')
  })

  it('lists the active rules by id, with counts on the filters, the jobs, the origin, and the counts', async () => {
    const onCount = vi.fn()
    render(<RulesView open="" onCount={onCount} />)
    await waitFor(() => expect(ids()).toEqual(['node.not-ready', 'pod.crashloop', 'pod.host-path', 'pod.privileged']))
    expect(onCount).toHaveBeenCalledWith(4)
    expect(screen.getByTestId('rules-table').getAttribute('style')).toContain('min-width: 980px')
    expect(screen.getByTestId('rules-show-active').textContent).toBe('Active 4')
    expect(screen.getByTestId('rules-show-retired').textContent).toBe('Retired 1')
    expect(screen.getByTestId('rules-show-replaced').textContent).toBe('Replaced 1')
    expect(screen.getByTestId('rules-show-suggested').textContent).toBe('Suggested 1')
    expect(screen.getByTestId('rules-show-found').textContent).toBe('Unlisted 2')
    expect(screen.getByTestId('rules-show-all').textContent).toBe('All 9')
    const row = screen.getAllByTestId('rule-row')[3]!
    expect(within(row).getByTestId('rule-open').textContent).toBe('2')
    expect(within(row).getByTestId('rule-exceptions').textContent).toBe('1')
    expect(within(row).getByTestId('rule-origin').textContent).toBe('Built-in')
    // Findings, Runs and Clusters are columns of every row; the counts link into the row's drawer (CF-85).
    expect(within(row).getByTestId('rule-findings').textContent).toBe('5')
    expect(within(row).getByTestId('rule-runs').textContent).toBe('4')
    expect(within(row).getByTestId('rule-clusters').textContent).toBe('Production, dev')
    expect(within(row).getByTestId('rule-open').getAttribute('href')).toBe('#/aiops/jobs/rules/pod.privileged/cases')
    expect(within(row).getByTestId('rule-findings').getAttribute('href')).toBe('#/aiops/jobs/rules/pod.privileged/findings')
    expect(within(screen.getAllByTestId('rule-row')[0]!).getByTestId('rule-open').tagName).toBe('SPAN')
    const nr = screen.getAllByTestId('rule-row')[0]!
    expect(within(nr).getByTestId('rule-jobs').textContent).toBe('Nightly audit, Alert diagnosis')
    expect(within(nr).getByTestId('rule-origin').textContent).toBe('Built-in, edited')
    expect(within(screen.getAllByTestId('rule-row')[1]!).getByTestId('rule-aliases').textContent).toBe('also pod.restart-loop')
    // No rule has a control: no column for it.
    expect(screen.queryByText('Control')).toBeNull()
  })

  it('filters by state, job and words, and sorts by any column', async () => {
    render(<RulesView open="" />)
    await waitFor(() => expect(ids()).toHaveLength(4))
    fireEvent.click(screen.getByTestId('rules-show-replaced'))
    expect(ids()).toEqual(['pod.restart-loop'])
    expect(screen.getByTestId('rule-replaced-by').textContent).toBe('by pod.crashloop')
    fireEvent.click(screen.getByTestId('rules-show-all'))
    fireEvent.change(screen.getByTestId('rules-job'), { target: { value: 'alert-diagnosis' } })
    expect(ids()).toEqual(['node.not-ready', 'pod.crashloop'])
    fireEvent.change(screen.getByTestId('rules-job'), { target: { value: '' } })
    fireEvent.change(screen.getByTestId('rules-search'), { target: { value: 'restart' } })
    // The alias's own row, and the rule it is an alias of.
    expect(ids()).toEqual(['pod.crashloop', 'pod.restart-loop'])
    fireEvent.change(screen.getByTestId('rules-search'), { target: { value: '' } })
    fireEvent.click(document.querySelector('[data-testid="rules-table"] button[data-col="open"]')!)
    expect(ids()[0]).toBe('pod.privileged')
    expect(rowsShown(rowsOf(PAGE), 'retired', '', '')).toHaveLength(1)
    // A found id is found by its agents' description, a suggestion by the ids it groups.
    expect(rowsShown(rowsOf(PAGE), 'all', '', 'control plane').map((r) => r.id)).toEqual(['other.etcd-slow'])
    expect(rowsShown(rowsOf(PAGE), 'all', '', 'dns-timeout-errors').map((r) => r.id)).toEqual(['node.dns-timeout'])
  })

  it('opens a rule in a drawer, read only for who is not a SuperAdmin', async () => {
    vi.mocked(api.fetchRules).mockResolvedValue({ ...PAGE, mayChange: false })
    render(<RulesView open="pod.privileged" />)
    const d = await screen.findByTestId('rule-drawer')
    // Its cases named and linked, with how many are open (CF-85).
    expect(within(d).getByTestId('rule-cases-summary').textContent).toBe('3: C-7, C-5, C-2, 2 open')
    expect(within(d).getByTestId('rule-cases-summary').querySelector('a')?.getAttribute('href')).toBe('#/aiops/inbox/case/7')
    expect(within(d).queryByTestId('rule-edit')).toBeNull()
    expect(within(d).queryByTestId('rule-retire')).toBeNull()
    expect(screen.queryByTestId('rule-add')).toBeNull()
    cleanup()
    render(<RulesView open="other.etcd-slow" />)
    expect(within(await screen.findByTestId('rule-drawer')).getByTestId('rule-cases-list')).toBeTruthy()
    expect(screen.queryByTestId('found-promote')).toBeNull()
    cleanup()
    render(<RulesView open="node.dns-timeout" />)
    expect(within(await screen.findByTestId('rule-drawer')).getByTestId('suggestion-why')).toBeTruthy()
    expect(screen.queryByTestId('suggestion-promote')).toBeNull()
  })

  it('edits, retires, replaces and makes a rule active; deletes only an added rule nothing holds', async () => {
    const { rerender } = render(<RulesView open="pod.privileged" />)
    let d = await screen.findByTestId('rule-drawer')
    expect(within(d).queryByTestId('rule-delete')).toBeNull()
    fireEvent.click(within(d).getByTestId('rule-edit'))
    fireEvent.change(within(d).getByTestId('rule-says'), { target: { value: 'a container runs privileged' } })
    fireEvent.click(within(d).getByTestId('rule-job-alert-diagnosis'))
    fireEvent.click(within(d).getByTestId('rule-save'))
    await waitFor(() => expect(api.editRule).toHaveBeenCalledWith('pod.privileged', { says: 'a container runs privileged', jobs: ['nightly-audit', 'alert-diagnosis'] }))
    d = screen.getByTestId('rule-drawer')
    fireEvent.click(await within(d).findByTestId('rule-retire'))
    await waitFor(() => expect(api.retireRule).toHaveBeenCalledWith('pod.privileged'))
    fireEvent.click(await within(d).findByTestId('rule-replace'))
    fireEvent.change(within(d).getByTestId('rule-replace-by'), { target: { value: 'pod.host-path' } })
    fireEvent.click(within(d).getByTestId('rule-replace-confirm'))
    await waitFor(() => expect(api.replaceRule).toHaveBeenCalledWith('pod.privileged', 'pod.host-path'))
    rerender(<RulesView open="pod.root-user" />)
    d = await screen.findByTestId('rule-drawer')
    expect(within(d).getByTestId('rule-retired')).toBeTruthy()
    fireEvent.click(within(d).getByTestId('rule-activate'))
    await waitFor(() => expect(api.activateRule).toHaveBeenCalledWith('pod.root-user'))
    rerender(<RulesView open="pod.host-path" />)
    d = await screen.findByTestId('rule-drawer')
    fireEvent.click(await within(d).findByTestId('rule-delete'))
    fireEvent.click(within(screen.getByTestId('confirm')).getByText('Delete'))
    await waitFor(() => expect(api.deleteRule).toHaveBeenCalledWith('pod.host-path'))
  })

  it('adds a rule once it has an id, a meaning and a job', async () => {
    render(<RulesView open="" />)
    fireEvent.click(await screen.findByTestId('rule-add'))
    const f = screen.getByTestId('rule-form')
    const submit = within(f).getByTestId('rule-form-submit') as HTMLButtonElement
    expect(submit.disabled).toBe(true)
    fireEvent.change(within(f).getByTestId('rule-form-id'), { target: { value: 'pod.host-ipc' } })
    fireEvent.change(within(f).getByTestId('rule-form-says'), { target: { value: 'a pod shares the host IPC' } })
    fireEvent.change(within(f).getByTestId('rule-form-type'), { target: { value: 'security' } })
    fireEvent.change(within(f).getByTestId('rule-form-control'), { target: { value: 'CIS 5.2.4' } })
    expect(submit.disabled).toBe(false)
    fireEvent.click(submit)
    await waitFor(() => expect(api.addRule).toHaveBeenCalledWith({ id: 'pod.host-ipc', says: 'a pod shares the host IPC', type: 'security', jobs: ['nightly-audit'], control: 'CIS 5.2.4' }))
    await waitFor(() => expect(screen.queryByTestId('rule-form')).toBeNull())
  })

  it('lists the ids found outside the list as rows, with their title, description and counts, under Unlisted (CF-85, CF-86)', async () => {
    render(<RulesView open="" />)
    await waitFor(() => expect(ids()).toHaveLength(4))
    fireEvent.click(screen.getByTestId('rules-show-found'))
    expect([...ids()].sort()).toEqual(['other.dns-timeout', 'other.etcd-slow'])
    const row = screen.getAllByTestId('rule-row').find((r) => r.getAttribute('data-id') === 'other.etcd-slow')!
    expect(row.getAttribute('data-kind')).toBe('found')
    expect(row.textContent).toContain('etcd is slow')
    expect(within(row).getByTestId('rule-description').textContent).toBe('etcd answers writes late. No rule given is about the control plane’s store.')
    expect(within(row).getByTestId('rule-clusters').textContent).toBe('Production, dev')
    expect(within(row).getByTestId('rule-findings').textContent).toBe('4')
    expect(within(row).getByTestId('rule-state').textContent).toBe('Unlisted')
    expect(within(row).getByTestId('rule-origin').textContent).toBe('Agent')
    // A found id a suggestion groups says so.
    expect(within(screen.getAllByTestId('rule-row').find((r) => r.getAttribute('data-id') === 'other.dns-timeout')!).getByTestId('rule-in-suggestion').textContent).toBe('in suggestion node.dns-timeout')
  })

  it('opens a found id in a drawer that lists its findings with their runs and routing, its cases with their status, and promotes it (CF-85)', async () => {
    const { rerender } = render(<RulesView open="other.etcd-slow" section="cases" />)
    const d = await screen.findByTestId('rule-drawer')
    expect(within(d).getByTestId('rule-descriptions').textContent).toContain('etcd answers writes late.')
    const findings = within(d).getAllByTestId('rule-finding')
    expect(findings.map((f) => f.getAttribute('data-run'))).toEqual(['run-12', 'run-1'])
    expect(within(findings[0]!).getByTestId('rule-finding-run').getAttribute('href')).toBe('#/aiops/jobs/runs/run-12')
    expect(within(findings[0]!).getByTestId('rule-finding-routing').textContent).toBe('Seen againC-9')
    expect(within(findings[1]!).getByTestId('rule-finding-routing').getAttribute('data-routing')).toBe('ignored')
    expect(
      within(d)
        .getAllByTestId('rule-run')
        .map((a) => a.textContent),
    ).toEqual(['run-12', 'run-1'])
    expect(
      within(d)
        .getAllByTestId('rule-case-status')
        .map((t) => t.textContent),
    ).toEqual(['New', 'Closed: no action needed'])
    expect(within(d).getAllByTestId('rule-case')[0]!.querySelector('a')!.getAttribute('href')).toBe('#/aiops/inbox/case/9')
    // A count in the table opens the drawer at its list.
    expect(within(d).getByTestId('rule-cases-list').getAttribute('data-at')).toBe('true')
    expect(within(d).getByTestId('rule-fields').textContent).toContain('Node ×4')
    // Promote as it is: its meaning drafted from the agents' description.
    fireEvent.click(within(d).getByTestId('found-promote'))
    await waitFor(() => expect(api.promoteRule).toHaveBeenCalledWith({ from: ['other.etcd-slow'], id: '', says: 'etcd answers writes late.', type: 'other', jobs: ['nightly-audit'] }))
    // Edit then promote, under a clean id.
    fireEvent.click(within(screen.getByTestId('rule-drawer')).getByTestId('found-edit'))
    const f = screen.getByTestId('rule-form')
    expect((within(f).getByTestId('rule-form-says') as HTMLInputElement).value).toBe('etcd answers writes late.')
    expect((within(f).getByTestId('rule-form-id') as HTMLInputElement).placeholder).toBe('other.etcd-slow')
    fireEvent.change(within(f).getByTestId('rule-form-id'), { target: { value: 'node.etcd-slow' } })
    fireEvent.click(within(f).getByTestId('rule-form-submit'))
    await waitFor(() => expect(api.promoteRule).toHaveBeenLastCalledWith({ from: ['other.etcd-slow'], id: 'node.etcd-slow', says: 'etcd answers writes late.', type: 'other', jobs: ['nightly-audit'] }))
    // Dismissed alone, not suggested again; a found id in a suggestion is decided there.
    fireEvent.click(within(screen.getByTestId('rule-drawer')).getByTestId('found-dismiss'))
    await waitFor(() => expect(api.dismissSuggestion).toHaveBeenCalledWith('other.etcd-slow'))
    rerender(<RulesView open="other.dns-timeout" />)
    const g = await screen.findByTestId('found-in-suggestion')
    expect(g.textContent).toBe('In suggestion node.dns-timeout: decide it there.')
    expect(screen.queryByTestId('found-dismiss')).toBeNull()
    expect(promoteOf({ ...PAGE.found[0]!, jobs: ['case-analysis'] }, JOBS).jobs).toEqual(['nightly-audit'])
    expect(promoteOf({ ...PAGE.found[0]!, descriptions: [] }, JOBS).says).toBe('etcd is slow')
    expect(meaningOf('One. Two.')).toBe('One.')
  })

  it('lists a suggested rule as a row; its drawer says why, the ids it groups, and promotes, edits then promotes, or dismisses it (CF-85, CF-86)', async () => {
    render(<RulesView open="" />)
    await waitFor(() => expect(ids()).toHaveLength(4))
    fireEvent.click(screen.getByTestId('rules-show-suggested'))
    expect(ids()).toEqual(['node.dns-timeout'])
    const row = screen.getByTestId('rule-row')
    expect(within(row).getByTestId('rule-from').textContent).toBe('from other.dns-timeout, other.dns-timeout-errors')
    expect(within(row).getByTestId('rule-state').textContent).toBe('Suggested')
    expect(within(row).getByTestId('rule-runs').textContent).toBe('6')
    expect(within(row).getByTestId('rule-findings').textContent).toBe('5')
    cleanup()
    render(<RulesView open="node.dns-timeout" />)
    const d = await screen.findByTestId('rule-drawer')
    expect(within(d).getByTestId('suggestion-why').textContent).toContain('grouped with other.dns-timeout-errors because their ids are alike in words')
    expect(
      within(d)
        .getAllByTestId('suggestion-from-id')
        .map((a) => a.getAttribute('href')),
    ).toEqual(['#/aiops/jobs/rules/other.dns-timeout', '#/aiops/jobs/rules/other.dns-timeout-errors'])
    expect(within(d).getByTestId('rule-descriptions').textContent).toContain('other.dns-timeout: Lookups time out.')
    expect(
      within(d)
        .getAllByTestId('rule-run')
        .map((a) => a.textContent),
    ).toEqual(['run-11'])
    fireEvent.click(within(d).getByTestId('suggestion-promote'))
    await waitFor(() => expect(api.promoteRule).toHaveBeenCalledWith({ from: ['other.dns-timeout', 'other.dns-timeout-errors'], id: 'node.dns-timeout', says: 'DNS times out', type: 'other', jobs: ['nightly-audit'] }))
    cleanup()
    render(<RulesView open="node.dns-timeout" />)
    fireEvent.click(within(await screen.findByTestId('rule-drawer')).getByTestId('suggestion-edit'))
    const f = screen.getByTestId('rule-form')
    expect((within(f).getByTestId('rule-form-id') as HTMLInputElement).value).toBe('node.dns-timeout')
    fireEvent.change(within(f).getByTestId('rule-form-says'), { target: { value: 'DNS lookups time out' } })
    fireEvent.click(within(f).getByTestId('rule-form-submit'))
    await waitFor(() => expect(api.promoteRule).toHaveBeenLastCalledWith(expect.objectContaining({ says: 'DNS lookups time out', id: 'node.dns-timeout' })))
    cleanup()
    render(<RulesView open="other.dns-timeout" />)
    // The suggestion's first id opens the found id's own drawer; the suggestion opens by its id.
    expect(await screen.findByTestId('found-in-suggestion')).toBeTruthy()
    cleanup()
    render(<RulesView open="node.dns-timeout" />)
    fireEvent.click(within(await screen.findByTestId('rule-drawer')).getByTestId('suggestion-dismiss'))
    await waitFor(() => expect(api.dismissSuggestion).toHaveBeenCalledWith('other.dns-timeout'))
    expect(rowOf(rowsOf(PAGE), 'pod.crashloop')?.kind).toBe('rule')
    expect(behindOf(PAGE.found).runIds).toEqual(['run-12', 'run-11', 'run-1'])
  })

  it('opens a suggested rule in the Inbox, and says the Inbox when it is decided (CF-87)', async () => {
    const onDone = vi.fn()
    const onClose = vi.fn()
    render(<InboxSuggestionDrawer id="node.dns-timeout" onClose={onClose} onDone={onDone} />)
    await screen.findByTestId('suggestion-why')
    const d = screen.getByTestId('rule-drawer')
    expect(within(d).getByTestId('suggestion-rules-link').getAttribute('href')).toBe('#/aiops/jobs/rules/node.dns-timeout')
    fireEvent.click(within(d).getByTestId('suggestion-dismiss'))
    await waitFor(() => expect(onDone).toHaveBeenCalled())
    expect(onClose).toHaveBeenCalled()
    cleanup()
    vi.mocked(api.fetchRules).mockResolvedValue({ ...PAGE, suggestions: [] })
    render(<InboxSuggestionDrawer id="node.dns-timeout" onClose={onClose} onDone={onDone} />)
    await waitFor(() => expect(screen.getByTestId('rules-error').textContent).toBe('There is no suggested rule node.dns-timeout any more: it was promoted or dismissed.'))
  })

  it('opens a rule in a drawer that lists its cases, runs, findings and exceptions (CF-85)', async () => {
    render(<RulesView open="pod.privileged" />)
    const d = await screen.findByTestId('rule-drawer')
    expect(within(d).getByTestId('rule-fields').textContent).toContain('5, from 4 runs')
    expect(
      within(d)
        .getAllByTestId('rule-case')
        .map((c) => c.getAttribute('data-open')),
    ).toEqual(['true', 'true', 'false'])
    expect(
      within(d)
        .getAllByTestId('rule-exception')
        .map((a) => a.getAttribute('href')),
    ).toEqual(['#/aiops/mycroft/exceptions/EX-3'])
    expect(
      within(d)
        .getAllByTestId('rule-run')
        .map((a) => a.textContent),
    ).toEqual(['run-9', 'run-4'])
    expect(within(d).getAllByTestId('rule-finding')).toHaveLength(1)
  })

  it('says why a change was refused', async () => {
    vi.mocked(api.retireRule).mockRejectedValue(new Error('pod.privileged is retired already'))
    render(<RulesView open="pod.privileged" />)
    fireEvent.click(await screen.findByTestId('rule-retire'))
    expect((await screen.findByTestId('rules-error')).textContent).toBe('pod.privileged is retired already')
  })

  it('sends only what changed, and names the origin', () => {
    const r = PAGE.rules[0]!
    expect(rulePatchOf({ says: r.says, type: r.type, jobs: [...r.jobs], control: '' }, r)).toEqual({})
    expect(rulePatchOf({ says: ' new ', type: 'capacity', jobs: r.jobs, control: 'CIS 1' }, r)).toEqual({ says: 'new', type: 'capacity', control: 'CIS 1' })
    expect(originText(rule('x.y', { builtIn: false, auto: true }))).toBe('Added by housekeeping')
  })

  it('is a sub-tab of Jobs at #/aiops/jobs/rules, where the old Mycroft address leads (CF-89)', async () => {
    for (const hash of ['#/aiops/jobs/rules/pod.crashloop', '#/aiops/mycroft/rules/pod.crashloop']) {
      render(<JobsArea route={parseHash(hash)} />)
      expect(screen.getByTestId('jobs-sub-rules').getAttribute('aria-pressed')).toBe('true')
      expect(screen.getByTestId('jobs-sub-rules').textContent).toBe('Validation rules')
      expect(await screen.findByTestId('rule-drawer')).toBeTruthy()
      expect(within(screen.getByTestId('rule-drawer')).getByTestId('rule-drawer-aliases').textContent).toBe('pod.restart-loop')
      cleanup()
    }
    // A count's link opens the rule's drawer at that list.
    render(<JobsArea route={parseHash('#/aiops/jobs/rules/pod.privileged/findings')} />)
    const d = await screen.findByTestId('rule-drawer')
    expect(d.querySelector('[data-section=findings]')!.getAttribute('data-at')).toBe('true')
    expect(api.ruleHref('a.b')).toBe('#/aiops/jobs/rules/a.b')
  })

  it('names clusters as cases do, the id in the hover, says how many cases are open, and leaves out exceptions when there are none', async () => {
    render(<RulesView open="other.etcd-slow" />)
    const d = await screen.findByTestId('rule-drawer')
    const prod = within(d)
      .getAllByTestId('rule-cluster')
      .find((c) => c.getAttribute('title') === 'prod')
    expect(prod?.textContent).toBe('Production')
    expect(within(d).getByTestId('rule-fields').textContent).toContain('4, from 4 runs')
    expect(within(d).getByTestId('rule-fields').textContent).not.toContain('Exceptions')
    expect(within(d).queryByTestId('rule-exceptions-list')).toBeNull()
    expect(casesState(2, 0)).toBe('both closed')
    expect(casesState(1, 0)).toBe('closed')
    expect(casesState(3, 3)).toBe('all open')
    expect(casesState(3, 1)).toBe('1 open')
  })
})
