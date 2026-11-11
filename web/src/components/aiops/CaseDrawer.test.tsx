import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CallError } from '../../api/call'
import * as api from '../../api/casework'
import type { Case, DuplicateCheck } from '../../api/casework'
import { LiveContext, LiveStore } from '../../api/live'
import type { AlertRow } from '../../api/types'
import { forgetSharedForTests } from '../../lib/cases'
import { setRouteForTests } from '../../lib/router'
import { CasesSettings, rangeError } from '../CasesSettings'
import { SLASettings } from '../SLASettings'
import { alertObject, alertSeverity } from './AlertReports'
import { CaseDrawer, offerText } from './CaseDrawer'
import { namesObject, prefillOf } from './NewCase'

// The live alerts of the cluster open, as a case opened from an alert reads
// them (CF-84).
const liveAlerts = vi.hoisted(() => ({ rows: [] as AlertRow[], loaded: true }))
vi.mock('../../api/acks', async (original) => ({ ...(await original<typeof import('../../api/acks')>()), useAlerts: () => liveAlerts }))

vi.mock('../../api/casework', async (original) => {
  const real = await original<typeof import('../../api/casework')>()
  return {
    ...real,
    fetchCases: vi.fn(),
    fetchCase: vi.fn(),
    openCase: vi.fn(),
    updateCase: vi.fn(),
    sayOnCase: vi.fn(),
    sendCaseDraft: vi.fn(),
    discardCaseDraft: vi.fn(),
    keepDraft: vi.fn(),
    closeKnown: vi.fn(),
    fixCase: vi.fn(),
    closeCase: vi.fn(),
    checkDuplicate: vi.fn(),
    applyOffer: vi.fn(),
    closeException: vi.fn(),
    reopenCase: vi.fn(),
    skipValidation: vi.fn(),
    joinCase: vi.fn(),
    fetchSimilar: vi.fn(),
    analyzeCase: vi.fn(),
    draftRCA: vi.fn(),
    fetchOpenKnown: vi.fn(),
    fetchSLA: vi.fn(),
    saveSLA: vi.fn(),
    fetchCasesSettings: vi.fn(),
    saveCasesSettings: vi.fn(),
  }
})

/** An element's text, and a field's value. */
const txt = (el: Element) => el.textContent ?? ''
const val = (el: Element) => (el as HTMLInputElement).value
const disabled = (id: string) => (screen.getByTestId(id) as HTMLButtonElement).disabled

const sla = { target: { responseMinutes: 15, resolveMinutes: 240 }, responseDue: '2026-11-06T10:15:00Z', resolveDue: '2026-11-06T14:00:00Z' }

const HUMAN: api.Outcome[] = ['fix', 'duplicate', 'noAction', 'knownIssue']
// Since CF-42 an agent-found case may close as a duplicate too, and since
// CF-90 as no action needed.
const AGENT: api.Outcome[] = ['fix', 'duplicate', 'noAction', 'knownIssue', 'exception']

const kase = (over: Partial<Case> = {}): Case => ({
  id: 'C-12',
  number: 12,
  title: 'The checkout is slow',
  priority: 1,
  status: 'inProgress',
  pending: true,
  agentFound: false,
  cluster: 'prod',
  resource: 'shop/Deployment/web',
  object: { namespace: 'shop', kind: 'Deployment', name: 'web' },
  source: { kind: 'manual' },
  createdBy: 'Olivia',
  created: '2026-11-06T10:00:00Z',
  updated: '2026-11-06T10:00:00Z',
  since: '2026-11-06T10:00:00Z',
  waitingSince: '2026-11-06T10:00:00Z',
  proposals: [],
  messages: [],
  history: [{ at: '2026-11-06T10:00:00Z', what: 'Opened by hand, P1', by: 'Olivia' }],
  sla,
  slaApplies: true,
  mayChange: true,
  related: [],
  allowedOutcomes: HUMAN,
  rcaRequired: false,
  mayReopen: true,
  ...over,
})

/** An agent-found case, In progress, seen three times. */
const found = (over: Partial<Case> = {}): Case =>
  kase({
    id: 'C-20',
    number: 20,
    title: 'web has no memory limit',
    priority: 3,
    agentFound: true,
    source: { kind: 'finding', run: 'run-7', job: 'Daily health check' },
    key: { cluster: 'prod', object: 'shop/Deployment/web', check: 'pod.no-limits' },
    seen: { count: 3, first: '2026-11-04T06:00:00Z', last: '2026-11-06T06:00:00Z' },
    slaApplies: false,
    allowedOutcomes: AGENT,
    ...over,
  })

/** Opens the one Close… and picks an outcome (CF-42). */
async function closeAs(o: api.Outcome) {
  fireEvent.click(await screen.findByTestId('case-close-open'))
  fireEvent.click(screen.getByTestId(`case-outcome-${o}`))
}

/** The outcomes Close… lists, in its order. */
const listed = () => [...screen.getByTestId('case-close-form').querySelectorAll('input[type="radio"]')].map((r) => r.getAttribute('data-testid')?.replace('case-outcome-', ''))

beforeEach(() => {
  Element.prototype.scrollTo = () => {}
  vi.mocked(api.fetchOpenKnown).mockResolvedValue([{ id: 'KI-3', title: 'DNS is slow', status: 'identified' }])
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  forgetSharedForTests()
  window.history.replaceState(null, '', '/')
})

describe('one drawer for every case (CU-09, CU-10)', () => {
  it('shows a case that waits for its reporter as "Waiting for the reporter" in its status pill, with no tag of its own (CF-67)', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ source: { kind: 'report' }, askedReporter: true }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    const pill = await screen.findByTestId('case-status')
    expect(txt(pill)).toBe('Waiting for the reporter')
    expect(pill.getAttribute('data-tone')).toBe('muted')
    expect(screen.getAllByText('Waiting for the reporter')).toHaveLength(1)
    cleanup()
    // Answered: In progress again.
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ source: { kind: 'report' }, askedReporter: false }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-status'))).toBe('In progress')
  })

  it('shows a case opened by hand: what it is about, the analysis, the fix, the SLA', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(
      kase({
        analysis: { at: '2026-11-06T10:01:00Z', run: 'run-4', cause: 'The memory limit is too low.', fix: { summary: 'Raise it', commands: ['kubectl set resources deployment/web -n shop --limits=memory=512Mi'] } },
        late: { resolve: true },
      }),
    )
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-kicker'))).toContain('Case C-12 · opened by Olivia')
    // Those who work the case set the priority in the header (CF-58).
    expect(val(screen.getByTestId('case-priority-set'))).toBe('1')
    expect(screen.queryByTestId('case-priority')).toBeNull()
    expect(txt(screen.getByTestId('case-status'))).toContain('In progress')
    expect(txt(screen.getByTestId('case-source-kind'))).toContain('Manual')
    expect(screen.getByTestId('case-object').querySelector('[data-testid="object-link"]')).not.toBeNull()
    expect(txt(screen.getByTestId('case-fix-command'))).toContain('kubectl set resources')
    expect(txt(screen.getByTestId('case-late'))).toContain('Resolution late · target 4 h')
    expect(screen.getByTestId('case-late').getAttribute('href')).toBe('#/admin/settings/sla')
    expect(screen.queryByTestId('case-no-sla')).toBeNull()
    // No reporter: notes only.
    expect(screen.queryByTestId('case-send')).toBeNull()
    expect(screen.getByTestId('case-note-add')).toBeTruthy()
  })

  it('says what the analysis left out because Watson would refuse a command, with no fix shown short (W-6)', async () => {
    const leftOut = 'The suggested fix, with all its commands, because Watson would refuse “kubectl delete namespace scratch”: Watson does not delete namespaces'
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ analysis: { at: '2026-11-06T10:01:00Z', run: 'run-4', cause: 'Scratch fills the node.', fix: {}, leftOut: [leftOut] } }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-fix-left-out'))).toBe('Left out: ' + leftOut)
    expect(screen.queryByTestId('case-fix-command')).toBeNull()
    expect(txt(screen.getByTestId('case-fix'))).toContain('No fix suggested.')
  })

  it('shows any words for what a case is about, without a link', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ cluster: undefined, resource: 'the disk of db-1, /var at 90%', object: undefined }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-object'))).toContain('the disk of db-1, /var at 90%')
    expect(screen.getByTestId('case-object').querySelector('[data-testid="object-link"]')).toBeNull()
    expect(screen.queryByTestId('case-analyze')).toBeNull()
  })

  it('shows a reported case with its words and conversation, and replies', async () => {
    const reported = kase({
      source: { kind: 'report' },
      reporter: { id: 'rita', name: 'Rita' },
      detail: 'The shop shows an error',
      messages: [{ id: 1, at: '2026-11-06T10:05:00Z', kind: 'operator', from: { id: 'agent' }, text: 'We are looking.', draft: true, draftBy: 'agent' }],
    })
    vi.mocked(api.fetchCase).mockResolvedValue(reported)
    vi.mocked(api.sayOnCase).mockResolvedValue(reported)
    vi.mocked(api.sendCaseDraft).mockResolvedValue(reported)
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    expect(txt(await screen.findByTestId('case-kicker'))).toContain('reported by Rita')
    expect(txt(screen.getByTestId('case-detail'))).toContain('The shop shows an error')
    fireEvent.click(screen.getByTestId('case-message-send'))
    await waitFor(() => expect(api.sendCaseDraft).toHaveBeenCalledWith('C-12', 1, undefined))
    fireEvent.change(screen.getByTestId('case-reply'), { target: { value: 'Which page?' } })
    fireEvent.click(screen.getByTestId('case-ask'))
    await waitFor(() => expect(api.sayOnCase).toHaveBeenCalledWith('C-12', 'Which page?', { ask: true }))
  })

  it('has one Close… that lists the outcomes the server allows, in its order (CF-42)', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    const { unmount } = render(<CaseDrawer id="12" onClose={() => undefined} />)
    fireEvent.click(await screen.findByTestId('case-close-open'))
    for (const old of ['case-fix-open', 'case-known-open', 'case-exception-open']) expect(screen.queryByTestId(old)).toBeNull()
    // A person's case: nothing to mute, so no exception.
    expect(listed()).toEqual(['fix', 'duplicate', 'noAction', 'knownIssue'])
    // Nothing is picked until the person picks; then only its fields show.
    expect(screen.queryByTestId('case-fix-form')).toBeNull()
    fireEvent.click(screen.getByTestId('case-outcome-knownIssue'))
    expect(screen.getByTestId('case-known-form')).toBeTruthy()
    expect(screen.queryByTestId('case-fix-form')).toBeNull()
    unmount()
    vi.mocked(api.fetchCase).mockResolvedValue(found())
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    fireEvent.click(await screen.findByTestId('case-close-open'))
    // Agent-found: Duplicate and No action needed are offered as for any case.
    expect(listed()).toEqual(['fix', 'duplicate', 'noAction', 'knownIssue', 'exception'])
  })

  it('lists No action needed once, with its hint, for a person’s case and an agent-found one (CF-90)', async () => {
    for (const c of [kase(), found()]) {
      vi.mocked(api.fetchCase).mockResolvedValue(c)
      const { unmount } = render(<CaseDrawer id={String(c.number)} onClose={() => undefined} />)
      fireEvent.click(await screen.findByTestId('case-close-open'))
      const form = txt(screen.getByTestId('case-close-form'))
      expect(form.split('No action needed').length - 1).toBe(1)
      expect(form).toContain('Not a problem, or it went away: say why.')
      expect(form).not.toContain('No fix needed')
      unmount()
    }
  })

  it('picks the only outcome a case allows, and offers no Close… when none', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(found({ allowedOutcomes: ['noAction'] }))
    const { unmount } = render(<CaseDrawer id="20" onClose={() => undefined} />)
    fireEvent.click(await screen.findByTestId('case-close-open'))
    expect((screen.getByTestId('case-outcome-noAction') as HTMLInputElement).checked).toBe(true)
    expect(screen.getByTestId('case-close-reason')).toBeTruthy()
    unmount()
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ allowedOutcomes: [] }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    expect(await screen.findByText('Your role may not close this case.')).toBeTruthy()
    expect(screen.queryByTestId('case-close-open')).toBeNull()
  })

  it('closes an agent-found case as a duplicate, with the live check (CF-42)', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(found())
    vi.mocked(api.checkDuplicate).mockResolvedValue({ allowed: true, original: 'C-9', status: 'inProgress', offers: [] })
    vi.mocked(api.closeCase).mockResolvedValue(found({ status: 'closed', outcome: 'duplicate', duplicateOf: 'C-9', pending: false }))
    render(<CaseDrawer id="20" onClose={() => undefined} onToast={() => undefined} />)
    await closeAs('duplicate')
    fireEvent.change(screen.getByTestId('case-close-original'), { target: { value: 'C-9' } })
    fireEvent.change(screen.getByTestId('case-close-reason'), { target: { value: 'The same missing limit.' } })
    await screen.findByTestId('case-duplicate-ok', {}, { timeout: 2000 })
    expect(api.checkDuplicate).toHaveBeenCalledWith('C-20', 'C-9')
    fireEvent.click(screen.getByTestId('case-close-save'))
    await waitFor(() => expect(api.closeCase).toHaveBeenCalledWith('C-20', { outcome: 'duplicate', explanation: 'The same missing limit.', original: 'C-9' }))
  })

  it('fixes with an RCA, required at P0 and P1', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ rcaRequired: true }))
    vi.mocked(api.draftRCA).mockResolvedValue({ cause: 'The limit.', fix: 'Raised it.', prevention: 'Load tests.', run: 'run-9' })
    vi.mocked(api.fixCase).mockResolvedValue(
      kase({ status: 'closed', outcome: 'fix', pending: false, rca: { case: 'C-12', title: 't', cause: 'The limit.', fix: 'Raised the limit to 512Mi.', prevention: 'Load tests.', by: 'Olivia', at: '2026-11-06T12:00:00Z' } }),
    )
    const toast = vi.fn()
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={toast} />)
    await closeAs('fix')
    expect(txt(screen.getByTestId('case-rca-required'))).toContain('required at P1')
    expect(screen.queryByTestId('case-rca-optional')).toBeNull()
    fireEvent.change(screen.getByTestId('case-fix-text'), { target: { value: 'Raised the limit to 512Mi.' } })
    // What was done is not enough at P1: the cause is required.
    expect(disabled('case-fix-save')).toBe(true)
    fireEvent.click(screen.getByTestId('case-rca-draft'))
    await waitFor(() => expect(val(screen.getByTestId('case-rca-cause'))).toBe('The limit.'))
    // What was done was written: the draft's fix does not replace it.
    expect(val(screen.getByTestId('case-fix-text'))).toBe('Raised the limit to 512Mi.')
    expect(disabled('case-fix-save')).toBe(false)
    fireEvent.click(screen.getByTestId('case-fix-save'))
    await waitFor(() => expect(api.fixCase).toHaveBeenCalledWith('C-12', 'Raised the limit to 512Mi.', { cause: 'The limit.', prevention: 'Load tests.', text: '' }, true, false))
    // No RCA id, no Mycroft: the RCA stays on the case.
    await waitFor(() => expect(toast).toHaveBeenCalledWith('C-12 is fixed.'))
    expect(txt(await screen.findByTestId('case-outcome'))).toBe('Fixed')
  })

  it('fixes without an RCA below P1, or with one when its fields are written', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ priority: 3 }))
    vi.mocked(api.fixCase).mockResolvedValue(kase({ priority: 3, status: 'closed', outcome: 'fix', pending: false }))
    const { unmount } = render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    await closeAs('fix')
    // The RCA's fields always show, optional below P1.
    expect(txt(screen.getByTestId('case-rca-optional'))).toContain('optional')
    expect(screen.getByTestId('case-rca-cause')).toBeTruthy()
    expect(screen.getByTestId('case-rca-draft')).toBeTruthy()
    fireEvent.change(screen.getByTestId('case-fix-text'), { target: { value: 'Restarted it.' } })
    expect(disabled('case-fix-save')).toBe(false)
    // Prevention written without a cause: an RCA needs its cause.
    fireEvent.change(screen.getByTestId('case-rca-prevention'), { target: { value: 'Alerts.' } })
    expect(disabled('case-fix-save')).toBe(true)
    fireEvent.change(screen.getByTestId('case-rca-prevention'), { target: { value: '' } })
    fireEvent.click(screen.getByTestId('case-fix-save'))
    await waitFor(() => expect(api.fixCase).toHaveBeenCalledWith('C-12', 'Restarted it.', undefined, true, false))
    unmount()
    vi.mocked(api.fixCase).mockClear()
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    await closeAs('fix')
    fireEvent.change(screen.getByTestId('case-fix-text'), { target: { value: 'Restarted it.' } })
    fireEvent.change(screen.getByTestId('case-rca-cause'), { target: { value: 'A leak.' } })
    fireEvent.click(screen.getByTestId('case-fix-save'))
    await waitFor(() => expect(api.fixCase).toHaveBeenCalledWith('C-12', 'Restarted it.', { cause: 'A leak.', prevention: '', text: '' }, true, false))
  })

  it('the Fixed form has no second fix field and the RCA’s fix is what was done (wave 6e)', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ rcaRequired: true }))
    vi.mocked(api.fixCase).mockResolvedValue(kase({ status: 'closed', outcome: 'fix', pending: false }))
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    await closeAs('fix')
    const form = screen.getByTestId('case-fix-form')
    expect(screen.queryByTestId('case-rca-fix')).toBeNull()
    expect(txt(form)).not.toContain('Fix applied')
    expect(form.querySelectorAll('textarea')).toHaveLength(4)
    // The hint: the RCA stays on the case, for the agents; nothing of Mycroft › RCAs (a saved fix is Mycroft › Fixes, wave 6g).
    expect(txt(form)).toContain('the RCA stays on the case, where the agents read it when they analyze a like case later')
    expect(txt(form)).not.toContain('RCAs')
    fireEvent.change(screen.getByTestId('case-fix-text'), { target: { value: 'Raised the limit.' } })
    fireEvent.change(screen.getByTestId('case-rca-cause'), { target: { value: 'Too low a limit.' } })
    fireEvent.change(screen.getByTestId('case-rca-prevention'), { target: { value: 'Load tests.' } })
    fireEvent.change(screen.getByTestId('case-rca-text'), { target: { value: 'Seen at the sale.' } })
    fireEvent.click(screen.getByTestId('case-fix-save'))
    await waitFor(() => expect(api.fixCase).toHaveBeenCalled())
    const [, text, rca] = vi.mocked(api.fixCase).mock.calls[0]!
    expect(text).toBe('Raised the limit.')
    expect(rca).toEqual({ cause: 'Too low a limit.', prevention: 'Load tests.', text: 'Seen at the sale.' })
    expect(rca).not.toHaveProperty('fix')
  })

  it('closes an agent-found case as fixed without validation when the person ticks it (CF-109)', async () => {
    // A case a person opened waits for no run: the option is not offered.
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ allowedOutcomes: HUMAN }))
    const { unmount } = render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    await closeAs('fix')
    expect(screen.queryByTestId('case-no-validation')).toBeNull()
    unmount()
    // An agent-found one: offered, off until ticked.
    vi.mocked(api.fetchCase).mockResolvedValue(found({ allowedOutcomes: HUMAN, stage: 'validation', verification: 'waiting' }))
    vi.mocked(api.fixCase).mockResolvedValue(found({ status: 'closed', outcome: 'fix' }))
    render(<CaseDrawer id="20" onClose={() => undefined} onToast={() => undefined} />)
    await closeAs('fix')
    const box = screen.getByTestId('case-no-validation') as HTMLInputElement
    expect(box.checked).toBe(false)
    expect(txt(screen.getByTestId('case-fix-form'))).toContain('The next run of its job checks that the fix holds')
    expect(txt(screen.getByTestId('case-fix-form'))).toContain('Close without validation No run re-checks the fix, and the case does not wait for one.')
    fireEvent.click(box)
    expect(txt(screen.getByTestId('case-fix-form'))).not.toContain('The next run of its job checks that the fix holds')
    fireEvent.change(screen.getByTestId('case-fix-text'), { target: { value: 'Set a limit.' } })
    fireEvent.click(screen.getByTestId('case-fix-save'))
    await waitFor(() => expect(api.fixCase).toHaveBeenCalledWith('C-20', 'Set a limit.', undefined, true, true))
  })

  it('lets a case closed as fixed stop waiting for its validation run (CF-109)', async () => {
    const closed = { status: 'closed', outcome: 'fix', closedAt: '2026-11-07T04:00:00Z', mayReopen: true, mayChange: true } as const
    // One that does not wait has nothing to skip, and says no run re-checks it.
    vi.mocked(api.fetchCase).mockResolvedValue(found({ ...closed }))
    const { unmount } = render(<CaseDrawer id="20" onClose={() => undefined} onToast={() => undefined} />)
    expect(txt(await screen.findByTestId('case-verified'))).toBe('Closed without validation: no run re-checks the fix.')
    expect(screen.queryByTestId('case-skip-validation')).toBeNull()
    unmount()
    vi.mocked(api.fetchCase).mockResolvedValue(found({ ...closed, stage: 'validation', verifyWait: { since: '2026-11-07T04:00:00Z', unchecked: 0 }, verifyJob: { id: 'alert-diagnosis', label: 'Alert diagnosis' } }))
    vi.mocked(api.skipValidation).mockResolvedValue(found({ ...closed }))
    render(<CaseDrawer id="20" onClose={() => undefined} onToast={() => undefined} />)
    fireEvent.click(await screen.findByTestId('case-skip-validation'))
    expect(txt(screen.getByTestId('case-skip-validation-form'))).toContain('C-20 is closed as fixed and waits for a run of Alert diagnosis to re-check the fix.')
    fireEvent.click(screen.getByTestId('case-skip-validation-save'))
    await waitFor(() => expect(api.skipValidation).toHaveBeenCalledWith('C-20'))
  })

  it('saves a fix by default; Save fix unticked sends saveFix false, and a closed case links the fix it saved (wave 6g)', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ priority: 3 }))
    vi.mocked(api.fixCase).mockResolvedValue(kase({ priority: 3, status: 'closed', outcome: 'fix', pending: false, fix: 'F-3' }))
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    await closeAs('fix')
    const box = screen.getByTestId('case-save-fix') as HTMLInputElement
    expect(box.checked).toBe(true)
    expect(txt(screen.getByTestId('case-fix-form'))).toContain('It becomes a fix in Mycroft › Fixes')
    fireEvent.click(box)
    expect(box.checked).toBe(false)
    fireEvent.change(screen.getByTestId('case-fix-text'), { target: { value: 'Restarted it.' } })
    fireEvent.click(screen.getByTestId('case-fix-save'))
    await waitFor(() => expect(api.fixCase).toHaveBeenCalledWith('C-12', 'Restarted it.', undefined, false, false))
    const saved = await screen.findByTestId('case-saved-fix')
    expect(txt(saved)).toBe('F-3')
    expect(saved.getAttribute('href')).toBe('#/aiops/mycroft/fixes/F-3')
    expect(txt(screen.getByTestId('case-closed'))).toContain('Saved as F-3 in Mycroft › Fixes.')
  })

  it('a draft’s fix fills What was done only when it is empty', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ priority: 3 }))
    vi.mocked(api.draftRCA).mockResolvedValue({ cause: 'The limit.', fix: 'Raised it.', prevention: 'Load tests.', run: 'run-9' })
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    await closeAs('fix')
    expect(val(screen.getByTestId('case-fix-text'))).toBe('')
    fireEvent.click(screen.getByTestId('case-rca-draft'))
    await waitFor(() => expect(val(screen.getByTestId('case-fix-text'))).toBe('Raised it.'))
    expect(val(screen.getByTestId('case-rca-cause'))).toBe('The limit.')
    expect(val(screen.getByTestId('case-rca-prevention'))).toBe('Load tests.')
    // Written by the person: drafting again leaves it as it is.
    vi.mocked(api.draftRCA).mockResolvedValue({ cause: 'Another cause.', fix: 'Something else.', prevention: 'More tests.', run: 'run-10' })
    fireEvent.change(screen.getByTestId('case-fix-text'), { target: { value: 'Raised the limit to 512Mi by hand.' } })
    fireEvent.click(screen.getByTestId('case-rca-draft'))
    await waitFor(() => expect(val(screen.getByTestId('case-rca-cause'))).toBe('Another cause.'))
    expect(val(screen.getByTestId('case-fix-text'))).toBe('Raised the limit to 512Mi by hand.')
  })

  it('a closed case’s RCA: the fix once, cause, prevention and notes, and the cases closed together with it, each linked (wave 6e)', async () => {
    const rca: api.CaseRCA = { case: 'C-12', title: 't', priority: 1, cause: 'The limit.', fix: 'Raised the limit.', prevention: 'Load tests.', text: 'Seen at the sale.', by: 'Olivia', at: '2026-11-06T12:00:00Z', cases: ['C-3', 'C-4'] }
    const closed = { status: 'closed' as const, outcome: 'fix' as const, pending: false, fixApplied: { text: 'Raised the limit.', by: 'Olivia', at: '2026-11-06T12:00:00Z' } }
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ ...closed, rca }))
    const { unmount } = render(<CaseDrawer id="12" onClose={() => undefined} />)
    const sec = await screen.findByTestId('case-rca')
    // Fix applied says what was done above; the RCA does not say it again.
    expect(txt(screen.getByTestId('case-fix-applied'))).toContain('Raised the limit.')
    expect(screen.queryByTestId('case-rca-fix-applied')).toBeNull()
    expect(document.body.textContent?.split('Raised the limit.').length).toBe(2)
    for (const t of ['The limit.', 'Load tests.', 'Seen at the sale.']) expect(txt(sec)).toContain(t)
    expect(txt(screen.getByTestId('case-rca-together'))).toBe('Closed together with C-3, C-4 (one RCA)')
    expect(screen.getAllByTestId('case-rca-together-link').map((a) => a.getAttribute('href'))).toEqual(['#/aiops/inbox/case/3', '#/aiops/inbox/case/4'])
    unmount()
    // An old case: its RCA had an id, which opens nothing now; a fix other than Fix applied's shows in the RCA.
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ ...closed, rca: { ...rca, id: 'RCA-4', fix: 'Raised the limit and the requests.', cases: undefined } }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    const old = await screen.findByTestId('case-rca')
    expect(txt(screen.getByTestId('case-rca-fix-applied'))).toBe('Raised the limit and the requests.')
    expect(old.querySelector('a')).toBeNull()
    expect(txt(old)).not.toContain('Mycroft')
    expect(screen.queryByTestId('case-rca-together')).toBeNull()
  })

  it('closes as no action needed only with an explanation', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    vi.mocked(api.closeCase).mockResolvedValue(kase({ status: 'closed', outcome: 'noAction', pending: false, explanation: 'The shop is slow on purpose at night.' }))
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    await closeAs('noAction')
    expect(disabled('case-close-save')).toBe(true)
    expect(txt(screen.getByTestId('case-close-save'))).toBe('Close as no action needed')
    fireEvent.change(screen.getByTestId('case-close-reason'), { target: { value: 'The shop is slow on purpose at night.' } })
    fireEvent.click(screen.getByTestId('case-close-save'))
    await waitFor(() => expect(api.closeCase).toHaveBeenCalledWith('C-12', { outcome: 'noAction', explanation: 'The shop is slow on purpose at night.' }))
    expect(txt(await screen.findByTestId('case-status'))).toContain('Closed: no action needed')
    expect(txt(screen.getByTestId('case-reason'))).toContain('on purpose at night')
    expect(screen.getByTestId('case-reopen')).toBeTruthy()
  })

  it('checks a duplicate’s original as it is typed, then closes as its duplicate', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    vi.mocked(api.checkDuplicate).mockResolvedValue({ allowed: true, original: 'C-4', status: 'inProgress', offers: [] })
    vi.mocked(api.closeCase).mockResolvedValue(kase({ status: 'closed', outcome: 'duplicate', duplicateOf: 'C-4', pending: false }))
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    await closeAs('duplicate')
    fireEvent.change(screen.getByTestId('case-close-reason'), { target: { value: 'Same checkout problem.' } })
    // No original yet.
    expect(disabled('case-close-save')).toBe(true)
    fireEvent.change(screen.getByTestId('case-close-original'), { target: { value: 'c-5' } })
    // The original's chain ends at C-4, which is open.
    expect(txt(await screen.findByTestId('case-duplicate-ok', {}, { timeout: 2000 }))).toContain('C-4 is in progress')
    expect(api.checkDuplicate).toHaveBeenCalledWith('C-12', 'C-5')
    fireEvent.click(screen.getByTestId('case-close-save'))
    await waitFor(() => expect(api.closeCase).toHaveBeenCalledWith('C-12', { outcome: 'duplicate', explanation: 'Same checkout problem.', original: 'C-4' }))
    expect(await screen.findByTestId('case-duplicate-of')).toBeTruthy()
  })

  it('offers what fits instead when the original is closed', async () => {
    const check: DuplicateCheck = {
      allowed: false,
      original: 'C-4',
      status: 'closed',
      outcome: 'fix',
      reason: 'C-4 was closed as fixed.',
      offers: [
        { kind: 'reopenOriginal', original: 'C-4' },
        { kind: 'regression', original: 'C-4' },
      ],
    }
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    vi.mocked(api.checkDuplicate).mockResolvedValue(check)
    vi.mocked(api.applyOffer).mockResolvedValue(kase({ regressionOf: 'C-4' }))
    const toast = vi.fn()
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={toast} />)
    await closeAs('duplicate')
    fireEvent.change(screen.getByTestId('case-close-original'), { target: { value: 'C-4' } })
    expect(txt(await screen.findByTestId('case-duplicate-refused', {}, { timeout: 2000 }))).toContain('C-4 was closed as fixed.')
    expect(txt(screen.getByTestId('case-offer-reopenOriginal'))).toBe('Reopen C-4: the fix did not hold')
    fireEvent.change(screen.getByTestId('case-close-reason'), { target: { value: 'x' } })
    // A closed original never closes this one as its duplicate.
    expect(disabled('case-close-save')).toBe(true)
    fireEvent.click(screen.getByTestId('case-offer-regression'))
    await waitFor(() => expect(api.applyOffer).toHaveBeenCalledWith('C-12', { kind: 'regression', original: 'C-4' }))
    await waitFor(() => expect(toast).toHaveBeenCalledWith('C-12 stays open as a regression of C-4.'))
    expect(txt(await screen.findByTestId('case-regression'))).toContain('A regression of C-4')
  })

  it('shows the offers again when the server refuses the duplicate at confirm (409)', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    vi.mocked(api.checkDuplicate).mockResolvedValue({ allowed: true, original: 'C-4', status: 'new', offers: [] })
    const check: DuplicateCheck = { allowed: false, original: 'C-4', status: 'closed', outcome: 'knownIssue', offers: [{ kind: 'sameKnownIssue', original: 'C-4', knownIssue: 'KI-2' }] }
    vi.mocked(api.closeCase).mockRejectedValue(new CallError('C-4 was closed meanwhile.', 409, { error: 'C-4 was closed meanwhile.', check }))
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    await closeAs('duplicate')
    fireEvent.change(screen.getByTestId('case-close-original'), { target: { value: 'C-4' } })
    await screen.findByTestId('case-duplicate-ok', {}, { timeout: 2000 })
    fireEvent.change(screen.getByTestId('case-close-reason'), { target: { value: 'Same.' } })
    fireEvent.click(screen.getByTestId('case-close-save'))
    expect(txt(await screen.findByTestId('case-offer-sameKnownIssue'))).toBe('Close as known issue KI-2')
    expect(txt(screen.getByTestId('case-error'))).toContain('C-4 was closed meanwhile.')
  })

  it('refuses a case as its own original without asking the server', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    await closeAs('duplicate')
    fireEvent.change(screen.getByTestId('case-close-original'), { target: { value: 'C-12' } })
    expect(await screen.findByText('A case cannot be its own original.')).toBeTruthy()
    await new Promise((r) => setTimeout(r, 600))
    expect(api.checkDuplicate).not.toHaveBeenCalled()
  })

  it('closes as a known issue: links one, or makes one from the case', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    vi.mocked(api.closeKnown).mockResolvedValue(kase({ status: 'closed', outcome: 'knownIssue', knownIssue: 'KI-4', pending: false }))
    const toast = vi.fn()
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={toast} />)
    await closeAs('knownIssue')
    expect(await screen.findByTestId('case-known-pick')).toBeTruthy()
    fireEvent.change(screen.getByTestId('case-known-workaround'), { target: { value: 'Retry' } })
    fireEvent.click(screen.getByTestId('case-known-create'))
    await waitFor(() => expect(api.closeKnown).toHaveBeenCalledWith('C-12', { create: { title: 'The checkout is slow', symptoms: ['The checkout is slow'], workaround: 'Retry' } }))
    expect(txt(await screen.findByTestId('case-closed-known'))).toBe('KI-4')
    expect(toast).toHaveBeenCalledWith('KI-4 is made from C-12, and C-12 is closed as it.')
  })

  it('accepts an agent-found case as an exception, with an optional last day', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(found())
    vi.mocked(api.closeException).mockResolvedValue(found({ status: 'closed', outcome: 'exception', exception: 'EX-3', explanation: 'Batch jobs run unlimited here.', pending: false }))
    render(<CaseDrawer id="20" onClose={() => undefined} onToast={() => undefined} />)
    await closeAs('exception')
    expect(txt(screen.getByTestId('case-exception-form'))).toContain('pod.no-limits on shop/Deployment/web in prod')
    expect(disabled('case-exception-save')).toBe(true)
    fireEvent.change(screen.getByTestId('case-exception-reason'), { target: { value: 'Batch jobs run unlimited here.' } })
    fireEvent.click(screen.getByTestId('case-exception-save'))
    await waitFor(() => expect(api.closeException).toHaveBeenCalledWith('C-20', 'Batch jobs run unlimited here.', undefined))
    expect(screen.getByTestId('case-closed-exception').getAttribute('href')).toBe('#/aiops/mycroft/exceptions/EX-3')
  })

  it('reopens a closed case only with a reason', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ status: 'closed', outcome: 'noAction', pending: false, explanation: 'Gone by itself.' }))
    vi.mocked(api.reopenCase).mockResolvedValue(kase())
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    expect(screen.queryByTestId('case-close-open')).toBeNull()
    fireEvent.click(await screen.findByTestId('case-reopen'))
    expect(disabled('case-reopen-save')).toBe(true)
    fireEvent.change(screen.getByTestId('case-reopen-reason'), { target: { value: 'Back again this morning.' } })
    fireEvent.click(screen.getByTestId('case-reopen-save'))
    await waitFor(() => expect(api.reopenCase).toHaveBeenCalledWith('C-12', 'Back again this morning.'))
  })

  it('offers no Reopen to a person the server does not allow', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ status: 'closed', outcome: 'fix', pending: false, mayReopen: false }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    await screen.findByTestId('case-closed')
    expect(screen.queryByTestId('case-reopen')).toBeNull()
  })

  it('shows an agent-found case’s key, how often it was seen, its links, the previous close and no SLA', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(
      found({
        possibleDuplicateOf: 'C-9',
        related: [{ id: 'C-3', number: 3, title: 'Shop is slow' }],
        previous: { case: 'C-11', outcome: 'noAction', explanation: 'A one-off during the upgrade.', by: 'olivia', at: '2026-11-01T09:00:00Z' },
        suggestException: true,
        ruleMeaning: 'a container has no CPU or memory limits',
      }),
    )
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-key'))).toContain('pod.no-limits')
    // The key's check is the validation rule, with what it means (build 14).
    expect(txt(screen.getByTestId('case-key').previousElementSibling!)).toBe('Validation rule')
    expect(txt(screen.getByTestId('case-rule-meaning'))).toBe('a container has no CPU or memory limits')
    expect(txt(screen.getByTestId('case-key'))).toContain('on shop/Deployment/web')
    expect(txt(screen.getByTestId('case-seen'))).toContain('×3')
    expect(txt(screen.getByTestId('case-seen-tag'))).toBe('seen ×3')
    expect(txt(screen.getByTestId('case-source-kind'))).toBe('Agent')
    expect(txt(screen.getByTestId('case-kicker'))).toContain('found by Daily health check')
    expect(txt(screen.getByTestId('case-no-sla'))).toBe('no SLA')
    expect(txt(screen.getByTestId('case-sla'))).toContain('No SLA')
    expect(screen.queryByTestId('case-late')).toBeNull()
    expect(txt(screen.getByTestId('case-possible-duplicate'))).toContain('C-9')
    expect(txt(screen.getByTestId('case-related'))).toContain('C-3 Shop is slow')
    expect(txt(screen.getByTestId('case-previous'))).toContain('C-11 was closed as no action needed by olivia')
    expect(txt(screen.getByTestId('case-previous'))).toContain('A one-off during the upgrade.')
    fireEvent.click(screen.getByTestId('case-suggest-exception-open'))
    // Close… opens on Exception.
    expect((screen.getByTestId('case-outcome-exception') as HTMLInputElement).checked).toBe(true)
    expect(screen.getByTestId('case-exception-form')).toBeTruthy()
  })

  it('keeps what is being written in the case’s one draft, and brings it back (CF-59)', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ draft: { fix: 'Half a fix', close: { outcome: 'duplicate', explanation: 'Same', original: 'C-4' }, by: 'Pat', updated: '2026-11-06T10:30:00.123456789Z' } }))
    vi.mocked(api.keepDraft).mockImplementation(async (_id, d) => kase({ draft: { ...d, by: 'Olivia', updated: '2026-11-06T10:40:00Z' } }))
    vi.mocked(api.checkDuplicate).mockResolvedValue({ allowed: true, original: 'C-4', status: 'new', offers: [] })
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    expect(txt(await screen.findByTestId('case-draft-at'))).toContain('Draft last edited by Pat')
    expect(txt(screen.getByTestId('case-keep'))).toBe('Save')
    fireEvent.change(screen.getByTestId('case-reply'), { target: { value: 'A note in progress' } })
    // Close… opens on the outcome the draft picked, with what it said.
    fireEvent.click(screen.getByTestId('case-close-open'))
    expect((screen.getByTestId('case-outcome-duplicate') as HTMLInputElement).checked).toBe(true)
    expect(val(screen.getByTestId('case-close-original'))).toBe('C-4')
    expect(val(screen.getByTestId('case-close-reason'))).toBe('Same')
    fireEvent.click(screen.getByTestId('case-outcome-fix'))
    expect(val(screen.getByTestId('case-fix-text'))).toBe('Half a fix')
    fireEvent.change(screen.getByTestId('case-rca-cause'), { target: { value: 'A typo' } })
    fireEvent.click(screen.getByTestId('case-keep'))
    // Sent with the stamp of the draft it started from, as the server gave it.
    await waitFor(() =>
      expect(api.keepDraft).toHaveBeenCalledWith(
        'C-12',
        expect.objectContaining({ reply: 'A note in progress', fix: 'Half a fix', close: { outcome: 'fix', explanation: 'Same', original: 'C-4' }, rca: { cause: 'A typo', prevention: '', text: '' } }),
        '2026-11-06T10:30:00.123456789Z',
      ),
    )
    // The next save starts from the stamp this one got.
    await waitFor(() => expect(txt(screen.getByTestId('case-draft-at'))).toContain('Olivia'))
    fireEvent.click(screen.getByTestId('case-keep'))
    await waitFor(() => expect(api.keepDraft).toHaveBeenLastCalledWith('C-12', expect.anything(), '2026-11-06T10:40:00Z'))
  })

  it('sets the priority P0 to P4, and the assignee', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    vi.mocked(api.updateCase).mockResolvedValue(kase({ priority: 0 }))
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    const pick = await screen.findByTestId('case-priority-set')
    const options = [...pick.querySelectorAll('option')].map((o) => o.textContent)
    expect(options).toEqual(['P0: Outage', 'P1: Blocking', 'P2: Service degradation', 'P3: Preemptive', 'P4: Question'])
    fireEvent.change(pick, { target: { value: '0' } })
    await waitFor(() => expect(api.updateCase).toHaveBeenCalledWith('C-12', { priority: 0 }))
    fireEvent.change(screen.getByTestId('case-assignee-set'), { target: { value: 'me' } })
    await waitFor(() => expect(api.updateCase).toHaveBeenCalledWith('C-12', { assignee: { id: 'me' } }))
  })
})

describe('New case shows what is like it (wave 6)', () => {
  const similar = (over: Partial<api.Similar> = {}): api.Similar => ({ open: [], known: [], exceptions: [], recentNoProblem: [], recentFixed: [], ...over })
  const typeTitle = async (title: string) => {
    fireEvent.change(await screen.findByTestId('case-new-title'), { target: { value: title } })
  }

  beforeEach(() => {
    vi.mocked(api.fetchCases).mockResolvedValue({ cases: [], clusters: [{ id: 'prod', name: 'Production' }], mayChange: true, me: 'olivia' })
  })

  it('is New case at "new", with any words for what it is about, P0 to P4', async () => {
    window.history.replaceState(null, '', '/#/aiops/inbox/case/new?title=Disk%2090%25&priority=4')
    vi.mocked(api.fetchSimilar).mockResolvedValue(similar())
    vi.mocked(api.openCase).mockResolvedValue(kase({ id: 'C-15', number: 15 }))
    render(<CaseDrawer id="new" onClose={() => undefined} onToast={() => undefined} />)
    expect(val(await screen.findByTestId('case-new-title'))).toBe('Disk 90%')
    expect(val(screen.getByTestId('case-new-priority'))).toBe('4')
    fireEvent.change(screen.getByTestId('case-new-resource'), { target: { value: 'the disk of db-1 at /var' } })
    fireEvent.click(screen.getByTestId('case-new-open'))
    await waitFor(() => expect(api.openCase).toHaveBeenCalledWith({ title: 'Disk 90%', detail: '', cluster: '', resource: 'the disk of db-1 at /var', priority: 4, type: 'other' }))
    await waitFor(() => expect(window.location.hash).toBe('#/aiops/inbox/case/15'))
  })

  it('asks what is like it as the person types, and adds to an open case instead', async () => {
    vi.mocked(api.fetchSimilar).mockResolvedValue(similar({ open: [{ id: 'C-4', number: 4, title: 'Checkout slow', status: 'inProgress', assignee: { id: 'olga', name: 'Olga' }, score: 0.8 }] }))
    vi.mocked(api.joinCase).mockResolvedValue(kase({ id: 'C-4', number: 4 }))
    const toast = vi.fn()
    render(<CaseDrawer id="new" onClose={() => undefined} onToast={toast} />)
    expect(txt(await screen.findByTestId('case-new-similar'))).toContain('as you type')
    await typeTitle('The checkout is slow')
    fireEvent.change(screen.getByTestId('case-new-detail'), { target: { value: 'Since 9:00' } })
    expect(txt(await screen.findByTestId('similar-open-C-4', {}, { timeout: 2000 }))).toContain('In progress')
    expect(api.fetchSimilar).toHaveBeenLastCalledWith({ title: 'The checkout is slow', detail: 'Since 9:00', cluster: undefined, resource: undefined })
    expect(txt(screen.getByTestId('case-new-open'))).toBe('Open a new case anyway')
    fireEvent.click(screen.getByTestId('similar-join-C-4'))
    await waitFor(() => expect(api.joinCase).toHaveBeenCalledWith('C-4', 'The checkout is slow\n\nSince 9:00'))
    await waitFor(() => expect(window.location.hash).toBe('#/aiops/inbox/case/4'))
    expect(toast).toHaveBeenCalledWith('You follow C-4; what you wrote is added to it.')
  })

  it('links a known issue: the case is made closed as it', async () => {
    vi.mocked(api.fetchSimilar).mockResolvedValue(similar({ known: [{ id: 'KI-2', title: 'Checkout times out', status: 'fixInProgress', workaround: 'Retry in a minute', expected: 'today', score: 0.7 }] }))
    vi.mocked(api.openCase).mockResolvedValue(kase({ id: 'C-16', number: 16, status: 'closed', outcome: 'knownIssue', knownIssue: 'KI-2' }))
    render(<CaseDrawer id="new" onClose={() => undefined} onToast={() => undefined} />)
    await typeTitle('Checkout times out')
    expect(txt(await screen.findByTestId('similar-known-KI-2', {}, { timeout: 2000 }))).toContain('Retry in a minute')
    fireEvent.click(screen.getByTestId('similar-known-link-KI-2'))
    await waitFor(() => expect(api.openCase).toHaveBeenCalledWith(expect.objectContaining({ title: 'Checkout times out', knownIssue: 'KI-2' })))
  })

  it('explains an exception, offers to reopen a case closed lately, or to open a regression of a fixed one', async () => {
    vi.mocked(api.fetchSimilar).mockResolvedValue(
      similar({
        exceptions: [{ id: 'EX-1', cluster: 'prod', object: 'shop/Deployment/web', check: 'pod.no-limits', reason: 'Batch jobs', by: 'olga', at: '2026-11-01T00:00:00Z' }],
        recentNoProblem: [{ id: 'C-7', number: 7, title: 'Checkout slow at night', status: 'closed', outcome: 'noAction', explanation: 'Backups run then.', score: 0.6 }],
        recentFixed: [{ id: 'C-8', number: 8, title: 'Checkout slow', status: 'closed', outcome: 'fix', rcaCause: 'The pool was too small.', score: 0.6 }],
      }),
    )
    vi.mocked(api.reopenCase).mockResolvedValue(kase({ id: 'C-7', number: 7 }))
    vi.mocked(api.openCase).mockResolvedValue(kase({ id: 'C-17', number: 17, regressionOf: 'C-8' }))
    render(<CaseDrawer id="new" onClose={() => undefined} onToast={() => undefined} />)
    await typeTitle('Checkout slow')
    expect(txt(await screen.findByTestId('similar-exception', {}, { timeout: 2000 }))).toContain('Batch jobs')
    expect(txt(screen.getByTestId('similar-noproblem-C-7'))).toContain('Backups run then.')
    expect(screen.getByTestId('similar-open-case-C-7').getAttribute('href')).toBe('#/aiops/inbox/case/7')
    // A fixed case's RCA is its cause, as text; the case is the link (wave 6e).
    const fixed = screen.getByTestId('similar-fixed-C-8')
    expect(txt(screen.getByTestId('similar-rca-C-8'))).toBe('RCA: The pool was too small.')
    expect([...fixed.querySelectorAll('a')].map((a) => a.getAttribute('href'))).toEqual(['#/aiops/inbox/case/8'])
    fireEvent.click(screen.getByTestId('similar-regression-C-8'))
    await waitFor(() => expect(api.openCase).toHaveBeenCalledWith(expect.objectContaining({ regressionOf: 'C-8' })))
  })

  it('reopens a case closed lately, with what was typed as the reason', async () => {
    vi.mocked(api.fetchSimilar).mockResolvedValue(similar({ recentNoProblem: [{ id: 'C-7', number: 7, title: 'Disk full', status: 'closed', outcome: 'noAction', score: 0.6 }] }))
    vi.mocked(api.reopenCase).mockResolvedValue(kase({ id: 'C-7', number: 7 }))
    render(<CaseDrawer id="new" onClose={() => undefined} onToast={() => undefined} />)
    await typeTitle('Disk full again')
    fireEvent.click(await screen.findByTestId('similar-reopen-C-7', {}, { timeout: 2000 }))
    await waitFor(() => expect(api.reopenCase).toHaveBeenCalledWith('C-7', 'Reported again: Disk full again'))
  })

  it('opens a case even when matching fails', async () => {
    vi.mocked(api.fetchSimilar).mockRejectedValue(new Error('down'))
    vi.mocked(api.openCase).mockResolvedValue(kase({ id: 'C-18', number: 18 }))
    render(<CaseDrawer id="new" onClose={() => undefined} onToast={() => undefined} />)
    await typeTitle('Something odd')
    await waitFor(() => expect(api.fetchSimilar).toHaveBeenCalled(), { timeout: 2000 })
    fireEvent.click(screen.getByTestId('case-new-open'))
    await waitFor(() => expect(api.openCase).toHaveBeenCalled())
  })
})

describe('the parts around the drawer', () => {
  it('reads a prefill, an object, an alert, an offer', () => {
    expect(prefillOf(new URLSearchParams('title=x&priority=9&cluster=prod'))).toEqual({ title: 'x', detail: undefined, cluster: 'prod', resource: undefined, priority: undefined })
    expect(prefillOf(new URLSearchParams('priority=0')).priority).toBe(0)
    expect(namesObject('shop/Deployment/web')).toBe(true)
    expect(namesObject('the disk of db-1')).toBe(false)
    expect(alertObject({ kind: 'pod', name: 'web-1', namespace: 'shop' })).toBe('shop/Pod/web-1')
    expect(alertObject({ kind: 'node', name: 'w3' })).toBe('Node/w3')
    expect(alertSeverity('failed')).toBe('critical')
    expect(alertSeverity('pending')).toBe('warning')
    expect(api.caseHref('C-12')).toBe('#/aiops/inbox/case/12')
    expect(api.caseNumber('c-12')).toBe(12)
    expect(api.caseNumber('KI-2')).toBe(0)
    expect(api.caseStatusText({ status: 'closed', outcome: 'noAction' })).toBe('Closed: no action needed')
    expect(api.caseStatusText({ status: 'new' })).toBe('New')
    // A case that waits for its reporter is shown so; closed, it is closed (CF-67).
    expect(api.caseStatusText({ status: 'inProgress', askedReporter: true })).toBe('Waiting for the reporter')
    expect(api.caseStatusText({ status: 'closed', outcome: 'noAction', askedReporter: true })).toBe('Closed: no action needed')
    expect(api.caseShownStatus({ status: 'new', askedReporter: true })).toBe('waiting')
    expect(offerText({ kind: 'reopenOriginal', original: 'C-4' }, { outcome: 'noAction' })).toBe('Reopen C-4')
    expect(offerText({ kind: 'removeException', original: 'C-4', exception: 'EX-1' }, {})).toBe('Remove exception EX-1 and keep this case open')
    expect(offerText({ kind: 'sameAs', original: 'C-4' }, {})).toBe('Keep this case open, marked the same as C-4')
  })

  it('sets the SLA, P0 to P4, kept under "0" to "4"', async () => {
    const targets = {
      '0': { responseMinutes: 15, resolveMinutes: 240 },
      '1': { responseMinutes: 60, resolveMinutes: 1440 },
      '2': { responseMinutes: 240, resolveMinutes: 7200 },
      '3': { responseMinutes: 1440, resolveMinutes: 0 },
      '4': { responseMinutes: 2880, resolveMinutes: 0 },
    }
    vi.mocked(api.fetchSLA).mockResolvedValue({ targets, maySet: true })
    vi.mocked(api.saveSLA).mockImplementation(async (t) => ({ targets: t, maySet: true }))
    render(<SLASettings />)
    expect(txt(await screen.findByTestId('sla-P4'))).toContain('P4: Question')
    expect(txt(screen.getByTestId('sla-P0'))).toContain('P0: Outage')
    fireEvent.change(screen.getByLabelText('P0 first response, minutes'), { target: { value: '10' } })
    fireEvent.click(screen.getByTestId('sla-save'))
    await waitFor(() => expect(api.saveSLA).toHaveBeenCalledWith({ ...targets, '0': { responseMinutes: 10, resolveMinutes: 240 } }))
  })

  it('sets Settings › Cases within their ranges, and shows the duplicates counter', async () => {
    const s = { keepAgentClosedDays: 90, lookBackDays: 90, exceptionStaleDays: 90, exceptionsPerAgent: 50, newCasesPerRun: 50, duplicatesOfAgentCases: 7 }
    vi.mocked(api.fetchCasesSettings).mockResolvedValue(s)
    vi.mocked(api.saveCasesSettings).mockImplementation(async (x) => ({ ...x, duplicatesOfAgentCases: 7 }))
    render(<CasesSettings />)
    expect(txt(await screen.findByTestId('cases-duplicates'))).toContain('7')
    fireEvent.change(screen.getByTestId('cases-newCasesPerRun'), { target: { value: '501' } })
    expect(txt(screen.getByTestId('cases-setting-newCasesPerRun'))).toContain('From 1 to 500.')
    fireEvent.click(screen.getByTestId('cases-save'))
    expect(txt(await screen.findByTestId('save-error'))).toContain('Fix the values marked above')
    expect(api.saveCasesSettings).not.toHaveBeenCalled()
    fireEvent.change(screen.getByTestId('cases-newCasesPerRun'), { target: { value: '20' } })
    fireEvent.change(screen.getByTestId('cases-keepAgentClosedDays'), { target: { value: '30' } })
    fireEvent.click(screen.getByTestId('cases-save'))
    await waitFor(() => expect(api.saveCasesSettings).toHaveBeenCalledWith({ keepAgentClosedDays: 30, lookBackDays: 90, exceptionStaleDays: 90, exceptionsPerAgent: 50, newCasesPerRun: 20, criticalAlertMinutes: 15 }))
    expect(rangeError(0, 3650)).toBe('From 1 to 3650.')
    expect(rangeError(3650, 3650)).toBe('')
    expect(rangeError(1.5, 500)).toBe('From 1 to 500.')
  })

  it('shows Settings › Cases read-only to who may not set them', async () => {
    vi.mocked(api.fetchCasesSettings).mockResolvedValue({ keepAgentClosedDays: 90, lookBackDays: 90, exceptionStaleDays: 90, exceptionsPerAgent: 50, newCasesPerRun: 50, maySet: false })
    render(<CasesSettings />)
    expect(((await screen.findByTestId('cases-lookBackDays')) as HTMLInputElement).disabled).toBe(true)
    expect(screen.queryByTestId('cases-save')).toBeNull()
  })
})

// --- Owner's feedback on Cases (Phase 12, wave 6c) ---

describe('a case’s type, incident number, actors and queued analysis (CF-32, CF-35 to CF-37)', () => {
  it('shows the type and the incident number in the header, and who created it', async () => {
    // One who only reads the cases reads the priority and the type as tags.
    vi.mocked(api.fetchCase).mockResolvedValue(found({ type: 'security', incident: 'INC0012345', createdBy: { name: 'Auditor', kind: 'agent' }, mayChange: false }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-type'))).toBe('Security')
    expect(txt(screen.getByTestId('case-priority'))).toContain('P3')
    expect(screen.queryByTestId('case-controls')).toBeNull()
    expect(txt(screen.getByTestId('case-incident'))).toBe('INC0012345')
    expect(txt(screen.getByTestId('case-incident-number'))).toBe('INC0012345')
    expect(txt(screen.getByTestId('case-created-by'))).toBe('Auditor (agent)')
    expect(txt(screen.getByTestId('case-kicker'))).toContain('found by Auditor (Daily health check)')
  })

  it('changes the type from the header, as its single change', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ type: 'other' }))
    vi.mocked(api.updateCase).mockResolvedValue(kase({ type: 'capacity' }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    const pick = (await screen.findByTestId('case-type-set')) as HTMLSelectElement
    expect(pick.value).toBe('other')
    expect([...pick.options].map((o) => o.textContent)).toEqual(['Security', 'Outage', 'Performance', 'Availability', 'Configuration', 'Capacity', 'Compliance', 'Question', 'Other'])
    fireEvent.change(pick, { target: { value: 'capacity' } })
    await waitFor(() => expect(api.updateCase).toHaveBeenCalledWith('C-12', { type: 'capacity' }))
    await waitFor(() => expect(val(screen.getByTestId('case-type-set'))).toBe('capacity'))
  })

  it('sets, checks and clears the incident number in place, sending it only when changed (CF-60)', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ incident: 'INC1' }))
    vi.mocked(api.updateCase).mockResolvedValue(kase({ incident: '' }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    fireEvent.click(await screen.findByTestId('case-incident-edit'))
    expect(val(screen.getByTestId('case-incident-input'))).toBe('INC1')
    // Unchanged, it is not sent.
    fireEvent.keyDown(screen.getByTestId('case-incident-input'), { key: 'Enter' })
    expect(api.updateCase).not.toHaveBeenCalled()
    fireEvent.click(await screen.findByTestId('case-incident-edit'))
    fireEvent.change(screen.getByTestId('case-incident-input'), { target: { value: 'bad\u0007' } })
    fireEvent.keyDown(screen.getByTestId('case-incident-input'), { key: 'Enter' })
    expect(api.updateCase).not.toHaveBeenCalled()
    fireEvent.change(screen.getByTestId('case-incident-input'), { target: { value: '' } })
    fireEvent.keyDown(screen.getByTestId('case-incident-input'), { key: 'Enter' })
    await waitFor(() => expect(api.updateCase).toHaveBeenLastCalledWith('C-12', { incident: '', base: { incident: 'INC1' } }))
  })

  it('names who did each thing in the history, with what they are', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(
      kase({
        history: [
          { at: '2026-11-06T10:00:00Z', what: 'Opened by hand, P1', by: { name: 'Olivia', kind: 'person' } },
          { at: '2026-11-06T10:05:00Z', what: 'Analyzed', by: { name: 'Troubleshooter', kind: 'agent' } },
          { at: '2026-11-06T10:10:00Z', what: 'Closed: the change was verified', by: { name: 'system', kind: 'system' } },
          { at: '2026-11-06T10:11:00Z', what: 'Before wave 6c', by: 'Pat' },
        ],
      }),
    )
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    await screen.findByTestId('case-history')
    // Newest first.
    expect(screen.getAllByTestId('case-history-by').map(txt)).toEqual(['Pat (person) · ', 'System · ', 'Troubleshooter (agent) · ', 'Olivia (person) · '])
  })

  it('says the analysis is queued, and still offers Analyze now', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(found({ analysisQueued: true, stage: 'analysisQueued' }))
    render(<CaseDrawer id="20" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-stage'))).toBe('Waiting for analysis')
    expect(txt(screen.getByTestId('case-analysis-queued-hint'))).toContain('Analyze runs it now')
    expect(screen.getByTestId('case-analyze')).toBeTruthy()
  })

  it('opens a case with a type and an incident number from New case', async () => {
    window.history.replaceState(null, '', '/#/aiops/inbox/case/new?title=Pods%20evicted&type=capacity')
    vi.mocked(api.fetchSimilar).mockResolvedValue({ open: [], known: [], exceptions: [], recentNoProblem: [], recentFixed: [] })
    vi.mocked(api.openCase).mockResolvedValue(kase({ id: 'C-16', number: 16 }))
    render(<CaseDrawer id="new" onClose={() => undefined} onToast={() => undefined} />)
    expect(val(await screen.findByTestId('case-new-type'))).toBe('capacity')
    fireEvent.change(screen.getByTestId('case-new-incident'), { target: { value: 'x'.repeat(65) } })
    expect(disabled('case-new-open')).toBe(true)
    fireEvent.change(screen.getByTestId('case-new-incident'), { target: { value: ' INC0012345 ' } })
    fireEvent.change(screen.getByTestId('case-new-type'), { target: { value: 'security' } })
    fireEvent.click(screen.getByTestId('case-new-open'))
    await waitFor(() => expect(api.openCase).toHaveBeenCalledWith(expect.objectContaining({ title: 'Pods evicted', type: 'security', incident: 'INC0012345' })))
    expect(prefillOf(new URLSearchParams('type=nonsense')).type).toBeUndefined()
  })
})

// --- The case drawer's header, shared draft and fields in place (CF-58 to CF-60) ---

describe('the header’s controls, one shared draft and fields edited in place (CF-58 to CF-60)', () => {
  const CLUSTERS = [
    { id: 'local', name: 'Lakehouse' },
    { id: 'prod', name: 'Production' },
  ]

  it('sets priority, type and assignee in the header, not the footer, with no Ask Sherlock and no Edit…', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    const controls = await screen.findByTestId('case-controls')
    const footer = screen.getByTestId('case-actions')
    for (const id of ['case-priority-set', 'case-type-set', 'case-assignee-set']) {
      expect(controls.contains(screen.getByTestId(id))).toBe(true)
      expect(footer.contains(screen.getByTestId(id))).toBe(false)
    }
    // The footer keeps the actions only.
    expect(footer.contains(screen.getByTestId('case-close-open'))).toBe(true)
    expect(txt(screen.getByTestId('case-keep'))).toBe('Save')
    expect(screen.queryByTestId('case-edit')).toBeNull()
    expect(screen.queryByTestId('case-edit-form')).toBeNull()
    // Sherlock is on the drawer's right already.
    expect(screen.getByTestId('case-drawer').querySelector('[data-testid="ask-sherlock"]')).toBeNull()
  })

  it('shows one who reads the cases the draft, read-only, and the values as tags', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ mayChange: false, cluster: 'local', clusterName: 'Lakehouse', clusterConnected: true, draft: { reply: 'We restart it at noon', by: 'Pat', updated: '2026-11-06T10:30:00Z' } }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-draft-read'))).toContain('We restart it at noon')
    expect(txt(screen.getByTestId('case-draft-at'))).toContain('Pat')
    expect(txt(screen.getByTestId('case-priority'))).toContain('P1')
    expect(screen.queryByTestId('case-keep')).toBeNull()
    expect(screen.queryByTestId('case-title-edit')).toBeNull()
    // The cluster by its name, its id in the hover.
    expect(txt(screen.getByTestId('case-cluster-name'))).toBe('Lakehouse')
    expect(screen.getByTestId('case-cluster-name').getAttribute('title')).toBe('local')
  })

  it('edits the title in place: Enter saves from the value it started with, Escape cancels and leaves the drawer open', async () => {
    const onClose = vi.fn()
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    vi.mocked(api.updateCase).mockResolvedValue(kase({ title: 'Checkout times out' }))
    render(<CaseDrawer id="12" onClose={onClose} onToast={() => undefined} />)
    fireEvent.click(await screen.findByTestId('case-title-edit'))
    fireEvent.change(screen.getByTestId('case-title-input'), { target: { value: 'Something else' } })
    fireEvent.keyDown(screen.getByTestId('case-title-input'), { key: 'Escape' })
    expect(screen.queryByTestId('case-title-input')).toBeNull()
    expect(onClose).not.toHaveBeenCalled()
    expect(api.updateCase).not.toHaveBeenCalled()
    fireEvent.click(screen.getByTestId('case-title-edit'))
    expect(val(screen.getByTestId('case-title-input'))).toBe('The checkout is slow')
    fireEvent.change(screen.getByTestId('case-title-input'), { target: { value: 'Checkout times out' } })
    fireEvent.keyDown(screen.getByTestId('case-title-input'), { key: 'Enter' })
    await waitFor(() => expect(api.updateCase).toHaveBeenCalledWith('C-12', { title: 'Checkout times out', base: { title: 'The checkout is slow' } }))
    await waitFor(() => expect(screen.queryByTestId('case-title-input')).toBeNull())
  })

  it('saves what it is about when the field is left, and edits the detail of a case a person opened, never a report’s words', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ object: undefined, resource: 'the disk of db-1', detail: 'Full at 90%' }))
    vi.mocked(api.updateCase).mockResolvedValue(kase({ resource: 'the disk of db-2' }))
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    fireEvent.click(await screen.findByTestId('case-resource-edit'))
    fireEvent.change(screen.getByTestId('case-resource-input'), { target: { value: 'the disk of db-2' } })
    fireEvent.blur(screen.getByTestId('case-resource-input'))
    await waitFor(() => expect(api.updateCase).toHaveBeenCalledWith('C-12', { resource: 'the disk of db-2', base: { resource: 'the disk of db-1' } }))
    expect(screen.getByTestId('case-detail-edit')).toBeTruthy()
    // A pencil on every field edited in place (build 14), none on a select.
    for (const id of ['case-title', 'case-resource', 'case-incident', 'case-detail']) expect(screen.getByTestId(`${id}-edit`).className).toBe('rk-icon-btn')
    expect(screen.queryByTestId('case-priority-edit')).toBeNull()
    expect(screen.queryByTestId('case-cluster-edit')).toBeNull()
    cleanup()
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ source: { kind: 'report' }, reporter: { id: 'rita', name: 'Rita' }, detail: 'It is down' }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-detail'))).toBe('It is down')
    expect(screen.queryByTestId('case-detail-edit')).toBeNull()
  })

  it('picks the cluster from the connected ones or none, by name, saving on change; one no longer connected stays until another is picked', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ cluster: 'local', clusterName: 'Lakehouse', clusterConnected: true, clusters: CLUSTERS }))
    vi.mocked(api.updateCase).mockResolvedValue(kase({ cluster: 'prod', clusterName: 'Production', clusterConnected: true, clusters: CLUSTERS }))
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    const pick = (await screen.findByTestId('case-cluster-set')) as HTMLSelectElement
    expect(pick.value).toBe('local')
    expect([...pick.options].map((o) => o.textContent)).toEqual(['No cluster', 'Lakehouse', 'Production'])
    fireEvent.change(pick, { target: { value: 'prod' } })
    await waitFor(() => expect(api.updateCase).toHaveBeenCalledWith('C-12', { cluster: 'prod', base: { cluster: 'local' } }))
    cleanup()
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ cluster: 'old', clusterName: 'old', clusterConnected: false, clusters: CLUSTERS }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    const gone = (await screen.findByTestId('case-cluster-set')) as HTMLSelectElement
    expect(gone.value).toBe('old')
    expect(gone.selectedOptions[0]?.textContent).toBe('old (not connected)')
  })

  it('asks when a field changed since the edit started: Discard my changes reads theirs, Overwrite saves over it', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    const stale = () => new CallError('The title changed since you started editing it.', 409, { error: 'The title changed since you started editing it.', field: 'title', current: 'Theirs', by: 'Pat', at: '2026-11-06T14:02:00Z' })
    vi.mocked(api.updateCase)
      .mockRejectedValueOnce(stale())
      .mockResolvedValueOnce(kase({ title: 'Mine' }))
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    fireEvent.click(await screen.findByTestId('case-title-edit'))
    fireEvent.change(screen.getByTestId('case-title-input'), { target: { value: 'Mine' } })
    fireEvent.keyDown(screen.getByTestId('case-title-input'), { key: 'Enter' })
    expect(txt(await screen.findByTestId('confirm'))).toMatch(/Pat changed the title at \d/)
    expect(txt(screen.getByTestId('case-conflict-current'))).toBe('Theirs')
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(api.updateCase).toHaveBeenLastCalledWith('C-12', { title: 'Mine', force: true }))
    // Discard my changes: theirs is read again, nothing is sent.
    vi.mocked(api.updateCase).mockRejectedValueOnce(stale())
    fireEvent.click(await screen.findByTestId('case-title-edit'))
    fireEvent.change(screen.getByTestId('case-title-input'), { target: { value: 'Mine again' } })
    fireEvent.keyDown(screen.getByTestId('case-title-input'), { key: 'Enter' })
    fireEvent.click(await screen.findByTestId('case-conflict-discard'))
    await waitFor(() => expect(api.fetchCase).toHaveBeenCalledTimes(2))
    expect(api.updateCase).toHaveBeenCalledTimes(3)
    await waitFor(() => expect(screen.queryByTestId('case-title-input')).toBeNull())
  })

  it('asks when the draft changed since this editor started from it: Discard my changes loads theirs, Overwrite saves mine with force', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ draft: { reply: 'Ours', by: 'Ann', updated: 'T1' } }))
    const stale = () => new CallError('Pat changed this draft since you started.', 409, { error: 'Pat changed this draft since you started.', draft: { reply: 'Theirs', by: 'Pat', updated: 'T2' } })
    vi.mocked(api.keepDraft).mockRejectedValueOnce(stale())
    render(<CaseDrawer id="12" onClose={() => undefined} onToast={() => undefined} />)
    fireEvent.change(await screen.findByTestId('case-reply'), { target: { value: 'Mine' } })
    fireEvent.click(screen.getByTestId('case-keep'))
    expect(txt(await screen.findByTestId('confirm'))).toContain('Pat changed this draft')
    expect(txt(screen.getByTestId('case-conflict-draft'))).toContain('Theirs')
    expect(api.keepDraft).toHaveBeenCalledWith('C-12', expect.objectContaining({ reply: 'Mine' }), 'T1')
    fireEvent.click(screen.getByTestId('case-conflict-discard'))
    await waitFor(() => expect(val(screen.getByTestId('case-reply'))).toBe('Theirs'))
    // Overwrite: the next conflict saves mine over theirs.
    vi.mocked(api.keepDraft)
      .mockRejectedValueOnce(stale())
      .mockResolvedValueOnce(kase({ draft: { reply: 'Mine', by: 'Ann', updated: 'T3' } }))
    fireEvent.change(screen.getByTestId('case-reply'), { target: { value: 'Mine' } })
    fireEvent.click(screen.getByTestId('case-keep'))
    await waitFor(() => expect(api.keepDraft).toHaveBeenLastCalledWith('C-12', expect.objectContaining({ reply: 'Mine' }), 'T2'))
    fireEvent.click(await screen.findByTestId('confirm-ok'))
    await waitFor(() => expect(api.keepDraft).toHaveBeenLastCalledWith('C-12', expect.objectContaining({ reply: 'Mine' }), 'T2', true))
  })
})

describe('a case opened from an alert links its alert (CF-84)', () => {
  const canal = { key: 'pod:kube-system/rke2-canal-n6fkh', kind: 'pod', severity: 'failed', name: 'rke2-canal-n6fkh', namespace: 'kube-system', status: 'Unknown', statusTone: 'err', node: 'r620-worker', events: [] } as AlertRow
  const fromAlert = (over: Partial<Case> = {}) =>
    kase({
      title: 'Pod status Unknown: node r620-worker stopped reporting (kube-system/Pod/rke2-canal-n6fkh)',
      priority: 0,
      resource: 'kube-system/Pod/rke2-canal-n6fkh',
      object: { namespace: 'kube-system', kind: 'Pod', name: 'rke2-canal-n6fkh' },
      clusterName: 'Production',
      source: { kind: 'alert', alert: 'Pod status Unknown: node r620-worker stopped reporting', severity: 'critical', alertKey: 'pod:kube-system/rke2-canal-n6fkh' },
      ...over,
    })
  const inLive = () =>
    render(
      <LiveContext.Provider value={new LiveStore('/test-events')}>
        <CaseDrawer id="12" onClose={() => undefined} />
      </LiveContext.Provider>,
    )

  afterEach(() => {
    setRouteForTests('#/')
    liveAlerts.rows = []
    liveAlerts.loaded = true
  })

  it('names the alert by its reason and links its cluster’s Alerts tab, filtered to the object, saying it is still going', async () => {
    setRouteForTests('#/c/prod/aiops/inbox/case/12')
    liveAlerts.rows = [canal]
    vi.mocked(api.fetchCase).mockResolvedValue(fromAlert())
    inLive()
    const source = await screen.findByTestId('case-source')
    expect(txt(screen.getByTestId('case-alert-name'))).toBe('Pod status Unknown: node r620-worker stopped reporting')
    expect(txt(source)).toContain('(critical)')
    expect(screen.getByTestId('case-alert-link').getAttribute('href')).toBe('#/c/prod/alerts?q=rke2-canal-n6fkh')
    expect(txt(screen.getByTestId('case-alert-state'))).toBe('still going')
    expect(screen.getByTestId('case-alert-state').getAttribute('data-going')).toBe('true')
  })

  it('says cleared once the alert is gone or recovered', async () => {
    setRouteForTests('#/c/prod/aiops/inbox/case/12')
    liveAlerts.rows = [{ ...canal, severity: 'recovered' }]
    vi.mocked(api.fetchCase).mockResolvedValue(fromAlert())
    inLive()
    expect(txt(await screen.findByTestId('case-alert-state'))).toBe('cleared')
    cleanup()
    liveAlerts.rows = []
    inLive()
    expect(txt(await screen.findByTestId('case-alert-state'))).toBe('cleared')
  })

  it('links the case’s cluster, not the one open, and says nothing of a cluster whose alerts it does not have', async () => {
    setRouteForTests('#/aiops/inbox/case/12')
    liveAlerts.rows = [canal]
    vi.mocked(api.fetchCase).mockResolvedValue(fromAlert())
    inLive()
    expect((await screen.findByTestId('case-alert-link')).getAttribute('href')).toBe('#/c/prod/alerts?q=rke2-canal-n6fkh')
    expect(screen.queryByTestId('case-alert-state')).toBeNull()
  })

  it('shows a case opened before by the name it was given, its alert found by its object', async () => {
    setRouteForTests('#/c/prod/aiops/inbox/case/12')
    liveAlerts.rows = [canal]
    vi.mocked(api.fetchCase).mockResolvedValue(fromAlert({ source: { kind: 'alert', alert: 'Unknown', severity: 'critical' } }))
    inLive()
    expect(txt(await screen.findByTestId('case-alert-name'))).toBe('Unknown')
    expect(screen.getByTestId('case-alert-link').getAttribute('href')).toBe('#/c/prod/alerts?q=rke2-canal-n6fkh')
    expect(txt(screen.getByTestId('case-alert-state'))).toBe('still going')
  })

  it('shows the analysis’s verdict that there is no problem (CF-82)', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(
      fromAlert({ priority: 3, analysis: { at: '2026-11-06T10:01:00Z', run: 'run-19', summary: 'No change needed.', expected: true, expectedWhy: 'r620-worker is annotated nodepower/status: asleep.', fix: {} } }),
    )
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-analysis-expected'))).toBe('No problem: r620-worker is annotated nodepower/status: asleep.')
    expect(txt(screen.getByTestId('case-analysis'))).toContain('No change needed.')
    // Without the live alerts, the link alone.
    expect(screen.getByTestId('case-alert-link')).toBeTruthy()
    expect(screen.queryByTestId('case-alert-state')).toBeNull()
  })
})
