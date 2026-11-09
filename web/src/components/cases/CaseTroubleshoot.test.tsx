// Troubleshooting a case in its drawer (Phase 12, wave 6f, CF-46 to CF-49).
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as caseSherlock from '../../api/caseSherlock'
import * as api from '../../api/casework'
import type { Case } from '../../api/casework'
import type { SherlockState } from '../../api/sherlock'
import * as watson from '../../api/watson'
import type { WatsonItem, WatsonState } from '../../api/watson'
import { forgetSharedForTests } from '../../lib/cases'
import { BulkBar } from './BulkBar'
import { CaseDrawer } from './CaseDrawer'
import { CaseCommands, CaseTroubleshoot } from './CaseTroubleshoot'

vi.mock('../../api/casework', async (original) => ({ ...(await original<typeof import('../../api/casework')>()), fetchCase: vi.fn(), fixCase: vi.fn(), fetchOpenKnown: vi.fn(), bulkCases: vi.fn() }))
vi.mock('../../api/caseSherlock', async (original) => ({ ...(await original<typeof import('../../api/caseSherlock')>()), useCaseSherlock: vi.fn(), askCaseSherlock: vi.fn(), stopCaseSherlock: vi.fn() }))
vi.mock('../../api/watson', async (original) => ({ ...(await original<typeof import('../../api/watson')>()), useWatson: vi.fn(), sendToWatson: vi.fn() }))

const txt = (el: Element) => el.textContent ?? ''
const val = (el: Element) => (el as HTMLInputElement).value

const kase = (over: Partial<Case> = {}): Case => ({
  id: 'C-12',
  number: 12,
  title: 'The checkout is slow',
  priority: 2,
  status: 'inProgress',
  pending: true,
  agentFound: false,
  cluster: 'prod',
  resource: 'shop/Deployment/web',
  source: { kind: 'manual' },
  created: '2026-11-08T10:00:00Z',
  updated: '2026-11-08T10:00:00Z',
  since: '2026-11-08T10:00:00Z',
  proposals: [],
  messages: [],
  history: [{ at: '2026-11-08T10:00:00Z', what: 'Opened by hand, P2', by: 'Olivia' }],
  slaApplies: true,
  mayChange: true,
  related: [],
  allowedOutcomes: ['fix', 'duplicate', 'notAProblem', 'noFixNeeded', 'knownIssue'],
  rcaRequired: false,
  mayReopen: false,
  commands: [],
  fixCommands: [],
  ...over,
})

const status = { available: true, enabled: true, key: true, model: 'claude-opus-5-5', admin: false }
const chat: SherlockState = {
  status,
  live: true,
  conv: {
    id: 'c1',
    busy: false,
    rev: 3,
    messages: [
      { id: 1, role: 'user', text: 'Why does web restart?', asker: 'Pat', at: '2026-11-08T10:05:00Z' },
      { id: 2, role: 'sherlock', text: 'It runs out of memory.\n\n```sh\nkubectl -n shop rollout restart deployment/web\n```', status: 'done', at: '2026-11-08T10:05:00Z' },
      { id: 3, role: 'user', text: 'And the limits?', asker: 'Ann', at: '2026-11-08T10:06:00Z' },
      { id: 4, role: 'sherlock', text: 'They are 256Mi.', status: 'done', at: '2026-11-08T10:06:00Z' },
    ],
  },
}

const item = (over: Partial<WatsonItem>): WatsonItem => ({ id: 'w1', command: 'kubectl -n shop get pods', kind: 'read', from: 'case:C-12', status: 'done', by: 'Pat', mine: true, created: '2026-11-08T10:07:00Z', exit: 0, canApprove: false, lineStart: 0, rev: 1, lines: [{ k: 'out', t: 'web-1 Running' }], ...over })

const watsonState: WatsonState = {
  live: true,
  status: { available: true, readOnly: false, twoPerson: false, env: 'staging', signedIn: true, me: 'Pat', prompt: 'pat@prod:~$', lapseSeconds: 600, reads: ['get'], changes: ['rollout restart'], admin: false },
  items: [item({}), item({ id: 'w2', command: 'kubectl get nodes', from: 'Typed' })],
}

let width = 1024
function setWidth(w: number) {
  width = w
  Object.defineProperty(window, 'innerWidth', { configurable: true, get: () => width })
}

beforeEach(() => {
  Element.prototype.scrollTo = () => {}
  setWidth(1024)
  vi.mocked(caseSherlock.useCaseSherlock).mockReturnValue(chat)
  vi.mocked(caseSherlock.askCaseSherlock).mockResolvedValue(chat.conv!)
  vi.mocked(watson.useWatson).mockReturnValue(watsonState)
  vi.mocked(watson.sendToWatson).mockResolvedValue([])
  vi.mocked(api.fetchOpenKnown).mockResolvedValue([])
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  forgetSharedForTests()
  window.history.replaceState(null, '', '/')
})

describe('the case drawer, near full screen (CF-46)', () => {
  it('runs the suggested fix in the Watson beside the case, with the same Run in Watson as Sherlock’s', async () => {
    setWidth(1400)
    const cmd = 'kubectl set resources deployment/web -n shop --limits=memory=512Mi'
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ analysis: { at: '2026-11-06T10:01:00Z', run: 'run-4', cause: 'The memory limit is too low.', fix: { summary: 'Raise it', commands: [cmd] } } }))
    const opened = vi.fn()
    window.addEventListener('221b-watson-fill', opened)
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    const btn = await screen.findByTestId('case-open-watson')
    expect(txt(btn)).toBe('Run in Watson')
    expect(btn.getAttribute('data-on')).toBe('true')
    fireEvent.click(btn)
    await waitFor(() => expect(watson.sendToWatson).toHaveBeenCalledWith('prod', cmd, { from: 'case:C-12', run: true, why: undefined }))
    expect(watson.sendToWatson).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('case-trouble').getAttribute('data-pane')).toBe('watson')
    expect(opened).not.toHaveBeenCalled()
    window.removeEventListener('221b-watson-fill', opened)
  })

  it('splits the case and Troubleshoot side by side when wide', async () => {
    setWidth(1400)
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    expect(txt(await screen.findByTestId('case-kicker'))).toContain('Case C-12')
    expect(screen.getByTestId('case-drawer').getAttribute('data-size')).toBe('full')
    expect(screen.getByTestId('drawer-split').getAttribute('data-layout')).toBe('split')
    expect(screen.queryByTestId('drawer-split-tabs')).toBeNull()
    // Both parts at once: the case with its actions, and Troubleshoot.
    expect(screen.getByTestId('case-actions')).toBeTruthy()
    expect(screen.getByTestId('drawer-split-side').hidden).toBe(false)
    expect(screen.getByTestId('case-trouble')).toBeTruthy()
    expect(screen.getAllByTestId('sherlock-question')).toHaveLength(2)
  })

  it('shows Case | Troubleshoot as tabs when narrow, and switches between them', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase())
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    await screen.findByTestId('case-kicker')
    expect(screen.getByTestId('drawer-split').getAttribute('data-layout')).toBe('tabs')
    expect(txt(screen.getByTestId('drawer-split-tab-main'))).toBe('Case')
    expect(txt(screen.getByTestId('drawer-split-tab-side'))).toBe('Troubleshoot')
    // The case first; Troubleshoot mounts once chosen.
    expect(screen.queryByTestId('drawer-split-side')).toBeNull()
    fireEvent.click(screen.getByTestId('drawer-split-tab-side'))
    expect(screen.getByTestId('drawer-split-side').hidden).toBe(false)
    const main = () => screen.getByTestId('drawer-split').firstElementChild as HTMLElement
    expect(main().contains(screen.getByTestId('case-actions'))).toBe(true)
    expect(main().style.display).toBe('none')
    fireEvent.click(screen.getByTestId('drawer-split-tab-main'))
    expect(screen.getByTestId('drawer-split-side').hidden).toBe(true)
    expect(main().style.display).toBe('flex')
    // Wider again: side by side.
    act(() => {
      setWidth(1300)
      window.dispatchEvent(new Event('resize'))
    })
    expect(screen.getByTestId('drawer-split').getAttribute('data-layout')).toBe('split')
  })
})

describe('Sherlock and Watson on the case (CF-47, CF-48)', () => {
  it('shows the shared conversation with who asked, asks on the case, and has no New chat', async () => {
    render(<CaseTroubleshoot c={kase()} />)
    expect(caseSherlock.useCaseSherlock).toHaveBeenCalledWith('C-12')
    expect(screen.getAllByTestId('sherlock-asker').map(txt)).toEqual(['Pat asked', 'Ann asked'])
    expect(screen.getByTestId('sherlock').getAttribute('data-shared')).toBe('true')
    expect(screen.queryByTestId('sherlock-new')).toBeNull()
    expect(screen.queryByTestId('sherlock-report-box')).toBeNull()
    fireEvent.change(screen.getByTestId('sherlock-input'), { target: { value: 'Is the node full?' } })
    fireEvent.click(screen.getByTestId('sherlock-send'))
    await waitFor(() => expect(caseSherlock.askCaseSherlock).toHaveBeenCalledWith('C-12', 'Is the node full?'))
  })

  it('Run in Watson hands the command to the case’s Watson, sent from the case, as the dock’s Send to Watson does', async () => {
    render(<CaseTroubleshoot c={kase()} />)
    expect(screen.queryByTestId('sherlock-send-watson')).toBeNull()
    fireEvent.click(screen.getByTestId('sherlock-run-watson'))
    expect(screen.getByTestId('case-trouble').getAttribute('data-pane')).toBe('watson')
    // Not left in the box: Watson's card asks before a change runs.
    await waitFor(() => expect(watson.sendToWatson).toHaveBeenCalledWith('prod', 'kubectl -n shop rollout restart deployment/web', { from: 'case:C-12', run: true, why: 'Sherlock proposed this answering “Why does web restart?”.' }))
    expect(val(screen.getByTestId('watson-input'))).toBe('')
    // Only the commands sent from this case show.
    expect(screen.getAllByTestId('watson-ran').map((e) => e.getAttribute('data-id'))).toEqual(['w1'])
    expect(screen.queryByTestId('watson-clear')).toBeNull()
  })

  it('takes a command handed over from the case’s side once, and says Watson is there', async () => {
    const onWatson = vi.fn()
    const onRan = vi.fn()
    const { rerender } = render(<CaseTroubleshoot c={kase()} onWatson={onWatson} onRan={onRan} run={null} />)
    expect(onWatson).toHaveBeenLastCalledWith(true)
    rerender(<CaseTroubleshoot c={kase()} onWatson={onWatson} onRan={onRan} run={{ command: 'kubectl -n shop rollout restart deployment/web', n: 1 }} />)
    await waitFor(() => expect(watson.sendToWatson).toHaveBeenCalledTimes(1))
    expect(watson.sendToWatson).toHaveBeenCalledWith('prod', 'kubectl -n shop rollout restart deployment/web', { from: 'case:C-12', run: true, why: undefined })
    expect(onRan).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('case-trouble').getAttribute('data-pane')).toBe('watson')
  })

  it('gives Watson the height of the drawer’s side', () => {
    render(<CaseTroubleshoot c={kase()} />)
    expect(screen.getByTestId('watson').parentElement!.className).toBe('cs-tpane')
  })

  it('lets a reader of the cases read the conversation only, with no Watson', () => {
    render(<CaseTroubleshoot c={kase({ mayChange: false })} />)
    expect(txt(screen.getByTestId('sherlock-read-only'))).toContain('only the people who work the cases ask here')
    expect(screen.queryByTestId('sherlock-input')).toBeNull()
    expect(screen.queryByTestId('case-trouble-switch')).toBeNull()
    expect(screen.queryByTestId('watson')).toBeNull()
  })

  it('says a closed case keeps no conversation, and a case with no cluster has nowhere to look', () => {
    const { unmount } = render(<CaseTroubleshoot c={kase({ status: 'closed', outcome: 'fix' })} />)
    expect(txt(screen.getByTestId('case-trouble-closed'))).toContain('deleted when it closed')
    expect(caseSherlock.useCaseSherlock).not.toHaveBeenCalled()
    unmount()
    render(<CaseTroubleshoot c={kase({ cluster: '' })} />)
    expect(screen.getByTestId('case-trouble-none')).toBeTruthy()
  })

  it('lists the commands run from the case, read or change, with how each ended', () => {
    render(
      <CaseCommands
        c={kase({
          commands: [
            { id: 'a', line: 'kubectl -n shop get pods', by: 'Pat', at: '2026-11-08T10:07:00Z', change: false, status: 'done', exit: 0 },
            { id: 'b', line: 'kubectl -n shop rollout restart deployment/web', by: 'Pat', at: '2026-11-08T10:08:00Z', change: true, status: 'done', exit: 0, approver: 'Ann' },
            { id: 'c', line: 'kubectl -n shop scale deployment/web --replicas=9', by: 'Ann', at: '2026-11-08T10:09:00Z', change: true, status: 'pending' },
          ],
        })}
      />,
    )
    const rows = screen.getAllByTestId('case-command')
    expect(rows.map((r) => r.getAttribute('data-kind'))).toEqual(['change', 'change', 'read'])
    expect(txt(rows[0]!)).toContain('waiting for approval')
    expect(txt(rows[1]!)).toContain('approved by Ann')
    expect(txt(screen.getByTestId('case-commands'))).toContain('Commands run from this case')
    cleanup()
    render(<CaseCommands c={kase({ status: 'closed', commands: [{ id: 'b', line: 'kubectl -n shop rollout restart deployment/web', by: 'Pat', at: '2026-11-08T10:08:00Z', change: true, status: 'done', exit: 0 }] })} />)
    expect(txt(screen.getByTestId('case-commands'))).toContain('Changes that fixed it')
  })
})

describe('the fix reused (CF-49)', () => {
  it('prefills What was done from the changes that finished done', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ fixCommands: ['kubectl -n shop rollout restart deployment/web', 'kubectl -n shop set resources deployment/web --limits=memory=512Mi'] }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    fireEvent.click(await screen.findByTestId('case-close-open'))
    fireEvent.click(screen.getByTestId('case-outcome-fix'))
    await waitFor(() => expect(val(screen.getByTestId('case-fix-text'))).toBe('kubectl -n shop rollout restart deployment/web\nkubectl -n shop set resources deployment/web --limits=memory=512Mi'))
    expect(screen.getByTestId('case-fix-prefilled')).toBeTruthy()
  })

  it('never overwrites what the person wrote', async () => {
    vi.mocked(api.fetchCase).mockResolvedValue(kase({ fixCommands: ['kubectl -n shop rollout restart deployment/web'], draft: { fix: 'Restarted it by hand', by: 'Pat', updated: '2026-11-08T10:00:00Z' } }))
    render(<CaseDrawer id="12" onClose={() => undefined} />)
    fireEvent.click(await screen.findByTestId('case-close-open'))
    fireEvent.click(screen.getByTestId('case-outcome-fix'))
    expect(val(screen.getByTestId('case-fix-text'))).toBe('Restarted it by hand')
    expect(screen.queryByTestId('case-fix-prefilled')).toBeNull()
  })

  it('prefills the bulk Fixed from the picked cases, named by case, unless something is typed', async () => {
    vi.mocked(api.fetchCase).mockImplementation(async (id) => (String(id) === 'C-1' ? kase({ id: 'C-1', fixCommands: ['kubectl -n shop rollout restart deployment/web'] }) : kase({ id: 'C-3', fixCommands: [] })))
    const cases = [kase({ id: 'C-1', number: 1 }), kase({ id: 'C-3', number: 3 })]
    render(<BulkBar cases={cases} onClear={() => undefined} onDone={() => undefined} />)
    fireEvent.click(screen.getByTestId('inbox-bulk-close'))
    await waitFor(() => expect(val(screen.getByTestId('inbox-bulk-text'))).toBe('C-1: kubectl -n shop rollout restart deployment/web'))
    expect(screen.getByTestId('inbox-bulk-prefilled')).toBeTruthy()
    cleanup()
    render(<BulkBar cases={cases} onClear={() => undefined} onDone={() => undefined} />)
    fireEvent.click(screen.getByTestId('inbox-bulk-close'))
    fireEvent.change(screen.getByTestId('inbox-bulk-text'), { target: { value: 'Mine' } })
    await waitFor(() => expect(api.fetchCase).toHaveBeenCalledTimes(4))
    await new Promise((r) => setTimeout(r, 0))
    expect(val(screen.getByTestId('inbox-bulk-text'))).toBe('Mine')
  })
})
