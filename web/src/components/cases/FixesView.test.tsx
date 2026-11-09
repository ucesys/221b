import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { CallError } from '../../api/call'
import * as api from '../../api/fixes'
import type { CasesAccess } from '../../lib/cases'
import { parseHash } from '../../lib/router'
import { FixesView, fixPatchOf, fixesShown } from './FixesView'
import { MycroftTab } from './MycroftTab'
import { MYCROFT_TABLE_MIN } from './mycroftTable'

vi.mock('../../api/fixes', async (original) => ({
  ...(await original<typeof import('../../api/fixes')>()),
  fetchFixes: vi.fn(),
  saveFix: vi.fn(),
  deleteFix: vi.fn(),
}))
vi.mock('../../api/lessons', async (original) => ({
  ...(await original<typeof import('../../api/lessons')>()),
  fetchExceptions: vi.fn(() => Promise.resolve({ exceptions: [], mayChange: false })),
}))

const fix = (id: string, over: Partial<api.Fix> = {}): api.Fix => ({
  id,
  title: `Fix ${id}`,
  cluster: 'local',
  resource: 'Namespace/shop',
  symptoms: 'Pods are refused.',
  cause: 'No Pod Security label.',
  steps: 'Label the namespace.',
  commands: ['kubectl label namespace shop pod-security.kubernetes.io/enforce=baseline'],
  prevention: 'Label namespaces when they are made.',
  status: 'active',
  cases: [{ case: 'C-13', at: '2026-11-06T19:00:00Z', cluster: 'local', by: 'Super Admin' }],
  created: '2026-11-06T19:00:00Z',
  updated: '2026-11-06T19:00:00Z',
  createdBy: 'Super Admin',
  updatedBy: 'Super Admin',
  ...over,
})

const LIST: api.Fix[] = [
  fix('F-1', { title: 'Oldest', updated: '2026-11-01T00:00:00Z' }),
  fix('F-2', { title: 'Newest', updated: '2026-11-08T00:00:00Z', cases: [] }),
  fix('F-3', { title: 'Middle', updated: '2026-11-05T00:00:00Z' }),
  fix('F-4', { title: 'Contradicting', status: 'pending', reason: 'contradicts', contradicts: ['F-1'] }),
  fix('F-5', { title: 'Copy', status: 'inactive', reason: 'duplicate', duplicateOf: 'F-3' }),
  fix('F-6', { title: 'Turned off', status: 'inactive', reason: 'person', updatedBy: 'Ann' }),
]

const ids = () => screen.getAllByTestId('fix-row').map((r) => r.getAttribute('data-id'))
const head = (col: string) => document.querySelector(`[data-testid="fixes-table"] button[data-col="${col}"]`)!

describe('Cases › Mycroft › Fixes (wave 6g)', () => {
  beforeAll(() => {
    Element.prototype.scrollIntoView = () => {}
    // jsdom has no scrollTo on elements; the drawer scrolls its body to the top.
    Element.prototype.scrollTo = () => {}
  })
  beforeEach(() => {
    vi.mocked(api.fetchFixes).mockResolvedValue({ fixes: LIST, mayChange: true })
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
    window.history.replaceState(null, '', '/')
  })

  it('lists the active fixes, Updated newest first, with counts on the filters, and the others on asking', async () => {
    render(<FixesView open="" />)
    await waitFor(() => expect(ids()).toEqual(['F-2', 'F-3', 'F-1']))
    expect(head('updated').closest('th')!.getAttribute('aria-sort')).toBe('descending')
    // As wide as Mycroft's other tables (build 14).
    expect(screen.getByTestId('fixes-table').style.minWidth).toBe(`${MYCROFT_TABLE_MIN}px`)
    expect(screen.getByTestId('fixes-show-active').getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByTestId('fixes-show-active').textContent).toBe('Active 3')
    expect(screen.getByTestId('fixes-show-pending').textContent).toBe('Pending 1')
    expect(screen.getByTestId('fixes-show-inactive').textContent).toBe('Inactive 2')
    expect(screen.getByTestId('fixes-show-all').textContent).toBe('All 6')
    const row = screen.getAllByTestId('fix-row')[1]!
    expect(row.textContent).toContain('Middle')
    expect(within(row).getByTestId('fix-cases').textContent).toBe('1')
    expect(within(row).getByTestId('fix-status').textContent).toBe('active')
    // Pending: what it contradicts, linked.
    fireEvent.click(screen.getByTestId('fixes-show-pending'))
    expect(ids()).toEqual(['F-4'])
    const c = screen.getByTestId('fix-contradicts')
    expect(c.textContent).toBe('contradicts F-1')
    expect(c.querySelector('a')!.getAttribute('href')).toBe('#/cases/mycroft/fixes/F-1')
    // Inactive: a duplicate names what it duplicates.
    fireEvent.click(screen.getByTestId('fixes-show-inactive'))
    expect(ids().sort()).toEqual(['F-5', 'F-6'])
    expect(screen.getByTestId('fix-duplicate-of').textContent).toBe('duplicate of F-3')
    fireEvent.click(screen.getByTestId('fixes-show-all'))
    expect(ids()).toHaveLength(6)
  })

  it('sorts by Updated the other way, and by its other headings', async () => {
    render(<FixesView open="" />)
    await waitFor(() => expect(ids()).toEqual(['F-2', 'F-3', 'F-1']))
    fireEvent.click(head('updated'))
    expect(ids()).toEqual(['F-1', 'F-3', 'F-2'])
    expect(head('updated').closest('th')!.getAttribute('aria-sort')).toBe('ascending')
    fireEvent.click(head('title'))
    expect(ids()).toEqual(['F-3', 'F-2', 'F-1'])
    fireEvent.click(head('id'))
    expect(ids()).toEqual(['F-3', 'F-2', 'F-1'])
    fireEvent.click(head('cases'))
    expect(ids()![2]).toBe('F-2')
    // Back to Updated, newest first, for the other tests.
    fireEvent.click(head('updated'))
    expect(ids()).toEqual(['F-2', 'F-3', 'F-1'])
  })

  it('says how fixes are saved when there are none', async () => {
    vi.mocked(api.fetchFixes).mockResolvedValue({ fixes: [], mayChange: true })
    render(<FixesView open="" />)
    expect((await screen.findByTestId('fixes-empty')).textContent).toBe('No fixes yet. A fix is saved when a case is closed as Fixed with Save fix on.')
  })

  it('opens a fix a link names in a drawer with every field and the cases it was seen in', async () => {
    render(<FixesView open="F-3" />)
    const drawer = await screen.findByTestId('fix-drawer')
    for (const t of ['Pods are refused.', 'No Pod Security label.', 'Label the namespace.', '$ kubectl label namespace shop', 'Label namespaces when they are made.', 'Namespace/shop', 'Super Admin']) expect(drawer.textContent).toContain(t)
    const seen = within(drawer).getAllByTestId('fix-seen-case')
    expect(seen).toHaveLength(1)
    expect(seen[0]!.querySelector('a')!.getAttribute('href')).toBe('#/cases/inbox/case/13')
    expect(screen.getAllByTestId('fix-row').find((r) => r.getAttribute('aria-selected') === 'true')?.getAttribute('data-id')).toBe('F-3')
  })

  it('says a pending fix contradicts another and is not given to the agents; Accept makes it active', async () => {
    vi.mocked(api.saveFix).mockResolvedValue(fix('F-4', { status: 'active', updated: '2026-11-08T10:00:00Z' }))
    const toast = vi.fn()
    render(<FixesView open="F-4" onToast={toast} />)
    const banner = await screen.findByTestId('fix-pending')
    expect(banner.textContent).toBe('Pending: it contradicts F-1. The agents are not given it until someone accepts it.')
    expect(screen.queryByTestId('fix-activate')).toBeNull()
    fireEvent.click(screen.getByTestId('fix-accept'))
    await waitFor(() => expect(api.saveFix).toHaveBeenCalledWith('F-4', { status: 'active' }))
    expect(toast).toHaveBeenCalledWith('F-4 accepted: the agents are given it.')
    await waitFor(() => expect(api.fetchFixes).toHaveBeenCalledTimes(2))
  })

  it('says why a fix is inactive; Activate and Deactivate change its status', async () => {
    vi.mocked(api.saveFix).mockResolvedValue(fix('F-5'))
    render(<FixesView open="F-5" />)
    expect((await screen.findByTestId('fix-inactive')).textContent).toBe('Inactive: a duplicate of F-3, which the agents are given instead.')
    expect(screen.queryByTestId('fix-deactivate')).toBeNull()
    fireEvent.click(screen.getByTestId('fix-activate'))
    await waitFor(() => expect(api.saveFix).toHaveBeenCalledWith('F-5', { status: 'active' }))
    cleanup()
    render(<FixesView open="F-6" />)
    expect((await screen.findByTestId('fix-inactive')).textContent).toBe('Inactive: turned off by Ann. The agents are not given it.')
    cleanup()
    vi.mocked(api.saveFix).mockResolvedValue(fix('F-3', { status: 'inactive', reason: 'person' }))
    render(<FixesView open="F-3" />)
    fireEvent.click(await screen.findByTestId('fix-deactivate'))
    await waitFor(() => expect(api.saveFix).toHaveBeenCalledWith('F-3', { status: 'inactive' }))
  })

  it('edits a fix and saves only what changed, commands one a line', async () => {
    vi.mocked(api.saveFix).mockResolvedValue(fix('F-3', { steps: 'Label it, then restart.', updated: '2026-11-08T11:00:00Z' }))
    const toast = vi.fn()
    render(<FixesView open="F-3" onToast={toast} />)
    fireEvent.click(await screen.findByTestId('fix-edit'))
    expect((screen.getByTestId('fix-save') as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByTestId('fix-title') as HTMLInputElement).value).toBe('Middle')
    fireEvent.change(screen.getByTestId('fix-steps'), { target: { value: 'Label it, then restart.' } })
    fireEvent.change(screen.getByTestId('fix-commands'), { target: { value: 'kubectl label ns shop a=b\n\n  kubectl rollout restart deploy/web  ' } })
    fireEvent.click(screen.getByTestId('fix-save'))
    await waitFor(() => expect(api.saveFix).toHaveBeenCalledWith('F-3', { steps: 'Label it, then restart.', commands: ['kubectl label ns shop a=b', 'kubectl rollout restart deploy/web'] }))
    expect(toast).toHaveBeenCalledWith('F-3 saved.')
    await waitFor(() => expect(screen.queryByTestId('fix-editor')).toBeNull())
  })

  it('deletes after asking in the app, never the browser', async () => {
    vi.mocked(api.deleteFix).mockResolvedValue(fix('F-3'))
    const confirm = vi.spyOn(window, 'confirm')
    render(<FixesView open="F-3" />)
    fireEvent.click(await screen.findByTestId('fix-delete'))
    expect(screen.getByTestId('confirm').textContent).toContain('Delete F-3?')
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(api.deleteFix).toHaveBeenCalledWith('F-3'))
    expect(confirm).not.toHaveBeenCalled()
    await waitFor(() => expect(window.location.hash).toBe('#/cases/mycroft/fixes'))
  })

  it('offers no changes to someone who only reads', async () => {
    vi.mocked(api.fetchFixes).mockResolvedValue({ fixes: LIST, mayChange: false })
    render(<FixesView open="F-4" />)
    expect(await screen.findByTestId('fix-pending')).toBeTruthy()
    for (const t of ['fix-edit', 'fix-accept', 'fix-deactivate', 'fix-delete']) expect(screen.queryByTestId(t)).toBeNull()
  })

  it('tells a role without the cases so', async () => {
    vi.mocked(api.fetchFixes).mockRejectedValue(new CallError('not allowed', 403))
    render(<FixesView open="" />)
    expect((await screen.findByRole('alert')).textContent).toContain('Your role does not include the cases')
  })

  it('is Mycroft’s Fixes sub-tab, with the active count', async () => {
    const access: CasesAccess = { show: true, approver: true, operator: true, reporter: false, console: true, tabs: ['inbox', 'jobs', 'mycroft'] }
    render(<MycroftTab route={parseHash('#/cases/mycroft/fixes/F-3')} access={access} />)
    expect(await screen.findByTestId('mycroft-fixes')).toBeTruthy()
    expect(screen.getByTestId('mycroft-sub-fixes').getAttribute('aria-pressed')).toBe('true')
    await waitFor(() => expect(screen.getByTestId('mycroft-sub-fixes').textContent).toBe('Fixes3'))
    expect(await screen.findByTestId('fix-drawer')).toBeTruthy()
  })

  it('keeps a view’s fixes, and saves only the changed fields', () => {
    expect(fixesShown(LIST, 'pending').map((f) => f.id)).toEqual(['F-4'])
    expect(fixesShown(LIST, 'all')).toHaveLength(6)
    const f = fix('F-9')
    expect(fixPatchOf({ title: 'Fix F-9', symptoms: 'Pods are refused.', cause: 'No Pod Security label.', steps: 'Label the namespace.', commands: f.commands!.join('\n'), prevention: 'Label namespaces when they are made.' }, f)).toEqual({})
    expect(fixPatchOf({ title: ' New ', symptoms: '', cause: 'No Pod Security label.', steps: 'Label the namespace.', commands: '', prevention: 'Label namespaces when they are made.' }, f)).toEqual({ title: 'New', symptoms: '', commands: [] })
  })
})
