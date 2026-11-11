import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { CallError } from '../../api/call'
import * as api from '../../api/lessons'
import * as fixes from '../../api/fixes'
import { parseHash } from '../../lib/router'
import type { CasesAccess } from '../../lib/cases'
import * as views from './LessonsViews'
import { ExceptionsView } from './LessonsViews'
import { MycroftTab } from './MycroftTab'
import { MYCROFT_TABLE_MIN } from './mycroftTable'

vi.mock('../../api/lessons', async (original) => ({
  ...(await original<typeof import('../../api/lessons')>()),
  fetchExceptions: vi.fn(),
  removeException: vi.fn(),
  createException: vi.fn(),
  reactivateException: vi.fn(),
}))
vi.mock('../../api/fixes', async (original) => ({
  ...(await original<typeof import('../../api/fixes')>()),
  fetchFixes: vi.fn(),
}))

const ex = (id: string, state: api.ExceptionState, title: string, over: Partial<api.AcceptedException> = {}): api.AcceptedException => ({
  id,
  state,
  title,
  cluster: 'prod',
  clusterName: 'Production',
  resource: 'shop/Namespace/shop',
  check: 'netpol.missing',
  ruleMeaning: 'a namespace has no network policy',
  reason: 'Shop sits behind its own firewall.',
  finding: 'run-4.1',
  run: 'run-4',
  job: 'Nightly audit',
  by: 'ann@example.com',
  at: '2026-11-05T10:00:00Z',
  case: 'C-5',
  matches: 3,
  lastMatched: '2026-11-06T06:00:00Z',
  stale: false,
  ...(state === 'removed' ? { removedBy: 'sam', removedAt: '2026-11-06T10:00:00Z' } : {}),
  ...over,
})

describe('AIOps › Mycroft: exceptions (FX-06, MY-01); no RCAs since wave 6e', () => {
  beforeAll(() => {
    Element.prototype.scrollIntoView = () => {}
    // jsdom has no scrollTo on elements; the drawer scrolls its body to the top.
    Element.prototype.scrollTo = () => {}
  })
  beforeEach(() => {
    vi.mocked(api.fetchExceptions).mockResolvedValue({ exceptions: [ex('EX-3', 'active', 'No network policy'), ex('EX-2', 'removed', 'Volume full'), ex('EX-1', 'expired', 'Old tags')], mayChange: true })
    vi.mocked(fixes.fetchFixes).mockResolvedValue({ fixes: [], mayChange: true })
  })
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('lists the active exceptions, the others on asking, and removes one after asking', async () => {
    vi.mocked(api.removeException).mockResolvedValue({ ...ex('EX-3', 'removed', 'No network policy') })
    const toast = vi.fn()
    render(<ExceptionsView open="" onToast={toast} />)
    const cards = await screen.findAllByTestId('exception')
    expect(cards).toHaveLength(1)
    expect(cards[0]!.tagName).toBe('TR')
    expect(cards[0]!.textContent).toContain('Shop sits behind its own firewall.')
    expect(within(cards[0]!).getByTestId('exception-matches').textContent).toBe('3 times')
    // No drawer until a link names one.
    expect(screen.queryByTestId('exception-drawer')).toBeNull()
    fireEvent.click(screen.getByTestId('exceptions-show-all'))
    // Updated, newest first: the removed one changed last.
    expect(screen.getAllByTestId('exception').map((c) => within(c).getByTestId('exception-state').textContent)).toEqual(['Inactive', 'Expired', 'Active'])
    expect(screen.getAllByTestId('exception-remove')).toHaveLength(1)
    fireEvent.click(screen.getByTestId('exception-remove'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(api.removeException).toHaveBeenCalledWith('EX-3'))
    expect(toast).toHaveBeenCalledWith('EX-3 removed: the agents report it again.')
    await waitFor(() => expect(api.fetchExceptions).toHaveBeenCalledTimes(2))
  })

  it('filters the stale ones, never matched first, then the longest unmatched (wave 6)', async () => {
    vi.mocked(api.fetchExceptions).mockResolvedValue({
      exceptions: [
        ex('EX-5', 'active', 'Fresh'),
        ex('EX-6', 'active', 'Quiet a while', { stale: true, matches: 2, lastMatched: '2026-06-01T00:00:00Z' }),
        ex('EX-7', 'active', 'Never matched', { stale: true, matches: 0, lastMatched: undefined }),
        ex('EX-8', 'removed', 'Removed and stale', { stale: true }),
      ],
      mayChange: true,
    })
    render(<ExceptionsView open="" />)
    await screen.findAllByTestId('exception')
    expect(screen.getByTestId('exceptions-show-stale').textContent).toContain('2')
    fireEvent.click(screen.getByTestId('exceptions-show-stale'))
    const cards = screen.getAllByTestId('exception')
    expect(cards.map((c) => c.getAttribute('data-id'))).toEqual(['EX-7', 'EX-6'])
    expect(within(cards[0]!).getByTestId('exception-matches').textContent).toBe('never')
    expect(within(cards[0]!).getByTestId('exception-stale')).toBeTruthy()
  })

  it('shows an exception a link names, even when it no longer applies, and no Remove to a reader', async () => {
    vi.mocked(api.fetchExceptions).mockResolvedValue({ exceptions: [ex('EX-3', 'active', 'No network policy'), ex('EX-2', 'removed', 'Volume full')], mayChange: false })
    render(<ExceptionsView open="EX-2" />)
    const cards = await screen.findAllByTestId('exception')
    expect(cards.map((c) => c.getAttribute('data-id'))).toEqual(['EX-2', 'EX-3'])
    expect(cards[0]!.getAttribute('aria-current')).toBe('true')
    // Everything about it in its drawer.
    const drawer = screen.getByTestId('exception-drawer')
    expect(within(drawer).getByTestId('exception-drawer-inactive').textContent).toMatch(/^turned off by sam, /)
    expect(drawer.textContent).toContain('netpol.missing')
    expect(drawer.textContent).toContain('Shop sits behind its own firewall.')
    expect(within(drawer).getByTestId('exception-case').getAttribute('href')).toBe('#/aiops/inbox/case/5')
    expect(
      within(drawer)
        .getByText(/run run-4/)
        .getAttribute('href'),
    ).toBe('#/aiops/jobs/runs/run-4')
    expect(screen.queryByTestId('exception-remove')).toBeNull()
    expect(screen.queryByTestId('exception-drawer-remove')).toBeNull()
  })

  it('sorts the table by any column, Updated newest first, falling back to when it was removed or made, and shows a duplicate (wave 6g)', async () => {
    vi.mocked(api.fetchExceptions).mockResolvedValue({
      exceptions: [
        ex('EX-1', 'active', 'Old', { at: '2026-11-01T00:00:00Z' }),
        ex('EX-2', 'active', 'New', { updated: '2026-11-07T00:00:00Z' }),
        ex('EX-3', 'removed', 'Middle', { updated: '2026-11-04T00:00:00Z', duplicateOf: 'EX-1', removedBy: 'housekeeping' }),
      ],
      mayChange: true,
    })
    render(<ExceptionsView open="" />)
    await screen.findAllByTestId('exception')
    fireEvent.click(screen.getByTestId('exceptions-show-all'))
    const ids = () => screen.getAllByTestId('exception').map((c) => c.getAttribute('data-id'))
    const head = (col: string) => document.querySelector(`button[data-col="${col}"]`)!
    expect(ids()).toEqual(['EX-2', 'EX-3', 'EX-1'])
    expect(head('updated').closest('th')!.getAttribute('aria-sort')).toBe('descending')
    fireEvent.click(head('updated'))
    expect(ids()).toEqual(['EX-1', 'EX-3', 'EX-2'])
    expect(head('updated').closest('th')!.getAttribute('aria-sort')).toBe('ascending')
    fireEvent.click(head('title'))
    expect(ids()).toEqual(['EX-3', 'EX-2', 'EX-1'])
    const dup = within(screen.getAllByTestId('exception')[0]!).getByTestId('exception-duplicate-of')
    expect(dup.textContent).toBe('duplicate of EX-1')
    expect(dup.querySelector('a')!.getAttribute('href')).toBe('#/aiops/mycroft/exceptions/EX-1')
  })

  it('filters Active, Stale, Expired, Inactive or All; an inactive one says why (build 14)', async () => {
    vi.mocked(api.fetchExceptions).mockResolvedValue({
      exceptions: [
        ex('EX-1', 'active', 'Live'),
        ex('EX-2', 'expired', 'Past', { expires: '2026-11-01T23:59:59Z' }),
        ex('EX-3', 'removed', 'Off', { removedBy: 'sam' }),
        ex('EX-4', 'removed', 'Copy', { duplicateOf: 'EX-1', removedBy: 'housekeeping' }),
      ],
      mayChange: false,
    })
    render(<ExceptionsView open="" />)
    await screen.findAllByTestId('exception')
    const seg = screen.getByTestId('exceptions-show')
    expect([...seg.querySelectorAll('button')].map((b) => b.getAttribute('data-testid'))).toEqual(['exceptions-show-active', 'exceptions-show-stale', 'exceptions-show-expired', 'exceptions-show-inactive', 'exceptions-show-all'])
    expect([...seg.querySelectorAll('button')].map((b) => b.textContent!.replace(/\s*\d+$/, ''))).toEqual(['Active', 'Stale', 'Expired', 'Inactive', 'All'])
    const ids = () => screen.getAllByTestId('exception').map((c) => c.getAttribute('data-id'))
    expect(ids()).toEqual(['EX-1'])
    fireEvent.click(screen.getByTestId('exceptions-show-expired'))
    expect(ids()).toEqual(['EX-2'])
    fireEvent.click(screen.getByTestId('exceptions-show-inactive'))
    expect(ids().sort()).toEqual(['EX-3', 'EX-4'])
    const row = (id: string) => screen.getAllByTestId('exception').find((c) => c.getAttribute('data-id') === id)!
    expect(within(row('EX-3')).getByTestId('exception-state').textContent).toBe('Inactive')
    expect(within(row('EX-3')).getByTestId('exception-inactive-why').textContent).toBe('turned off by sam')
    expect(within(row('EX-4')).getByTestId('exception-duplicate-of').textContent).toBe('duplicate of EX-1')
    fireEvent.click(screen.getByTestId('exceptions-show-all'))
    expect(ids()).toHaveLength(4)
  })

  it('says Justification, Object, Cluster by name, Created and Validation rule with its meaning; never Why, Where, Accepted or Check (build 14)', async () => {
    render(<ExceptionsView open="EX-3" />)
    const table = await screen.findByTestId('exceptions-table')
    const heads = [...table.querySelectorAll('th')].map((h) => h.textContent)
    expect(heads).toEqual(expect.arrayContaining(['Cluster', 'Object', 'Validation rule', 'State', 'Justification']))
    expect(heads).not.toContain('Reason')
    expect(heads).not.toContain('Check')
    const row = within(screen.getAllByTestId('exception')[0]!)
    expect(row.getByTestId('exception-cluster-name').textContent).toBe('Production')
    expect(row.getByTestId('exception-cluster-name').getAttribute('title')).toBe('prod')
    expect(row.getByTestId('exception-rule').textContent).toBe('netpol.missinga namespace has no network policy')
    const drawer = screen.getByTestId('exception-drawer')
    const labels = [...within(drawer).getByTestId('exception-details').children].map((l) => l.firstElementChild?.textContent)
    expect(labels).toEqual(['Justification', 'Cluster', 'Object', 'Validation rule', 'Matched', 'Created', 'Updated', 'From'])
    expect(drawer.textContent).toContain('by ann@example.com')
    expect(drawer.textContent).toContain('created ')
    expect(drawer.textContent).not.toMatch(/accepted|Why|Where/)
    expect(within(drawer).getByTestId('exception-rule').textContent).toBe('netpol.missing · a namespace has no network policy')
    expect(within(drawer).getByTestId('exception-cluster-name').getAttribute('title')).toBe('prod')
  })

  it('makes an inactive exception active again after asking, approvers only, never one past its last day; a refusal is shown (build 14)', async () => {
    const off = ex('EX-2', 'removed', 'Volume full', { removedBy: 'sam' })
    vi.mocked(api.fetchExceptions).mockResolvedValue({ exceptions: [ex('EX-3', 'active', 'No network policy'), off, ex('EX-4', 'removed', 'Old', { expires: '2026-01-01T23:59:59Z' })], mayChange: true })
    vi.mocked(api.reactivateException)
      .mockRejectedValueOnce(new CallError('EX-3 already covers this cluster, object and validation rule', 409))
      .mockResolvedValueOnce({ ...off, state: 'active', removedBy: undefined, removedAt: undefined })
    const toast = vi.fn()
    render(<ExceptionsView open="EX-2" onToast={toast} />)
    const drawer = await screen.findByTestId('exception-drawer')
    expect(within(drawer).queryByTestId('exception-drawer-remove')).toBeNull()
    fireEvent.click(within(drawer).getByTestId('exception-drawer-reactivate'))
    expect(screen.getByTestId('confirm').textContent).toContain('Make EX-2 active again?')
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(screen.getByTestId('confirm').textContent).toContain('EX-3 already covers'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(api.reactivateException).toHaveBeenCalledTimes(2))
    expect(api.reactivateException).toHaveBeenLastCalledWith('EX-2')
    await waitFor(() => expect(toast).toHaveBeenCalledWith('EX-2 is active again: the agents leave it out again.'))
    // In the table: only the inactive one whose last day has not passed.
    fireEvent.click(screen.getByTestId('exceptions-show-inactive'))
    const rows = screen.getAllByTestId('exception')
    expect(rows.filter((r) => within(r).queryByTestId('exception-reactivate')).map((r) => r.getAttribute('data-id'))).toEqual(['EX-2'])
    cleanup()
    // A reader may not.
    vi.mocked(api.fetchExceptions).mockResolvedValue({ exceptions: [off], mayChange: false })
    render(<ExceptionsView open="EX-2" />)
    await screen.findByTestId('exception-drawer')
    expect(screen.queryByTestId('exception-drawer-reactivate')).toBeNull()
    expect(screen.queryByTestId('exception-reactivate')).toBeNull()
  })

  it('creates an exception by hand: a connected cluster by name, an optional object, a rule seen there or typed, a justification and an optional last day (build 14)', async () => {
    vi.mocked(api.fetchExceptions).mockResolvedValue({
      exceptions: [ex('EX-3', 'active', 'No network policy')],
      mayChange: true,
      clusters: [
        { id: 'local', name: 'Lakehouse', rules: [{ id: 'pod.root-user', meaning: 'a container runs as root' }, { id: 'other.odd-thing' }] },
        { id: 'prod', name: 'Production', rules: [] },
      ],
    })
    vi.mocked(api.createException)
      .mockRejectedValueOnce(new CallError('name the object as namespace/Kind/name or Kind/name, not "web"', 400))
      .mockResolvedValueOnce(ex('EX-4', 'active', 'A container runs as root'))
    const toast = vi.fn()
    render(<ExceptionsView open="" onToast={toast} />)
    fireEvent.click(await screen.findByTestId('exceptions-new'))
    const form = screen.getByTestId('exception-new')
    const save = within(form).getByTestId('exception-new-save') as HTMLButtonElement
    expect(save.disabled).toBe(true)
    const cluster = within(form).getByTestId('exception-new-cluster') as HTMLSelectElement
    expect([...cluster.options].map((o) => o.textContent)).toEqual(['Pick a cluster', 'Lakehouse', 'Production'])
    expect(within(form).getByTestId('exception-new-rule-hint').textContent).toContain('Pick the cluster first')
    fireEvent.change(cluster, { target: { value: 'local' } })
    expect([...within(form).getByTestId('exception-new-rules').querySelectorAll('option')].map((o) => [o.getAttribute('value'), o.textContent])).toEqual([
      ['pod.root-user', 'a container runs as root'],
      ['other.odd-thing', 'other.odd-thing'],
    ])
    fireEvent.change(within(form).getByTestId('exception-new-rule'), { target: { value: 'pod.root-user' } })
    expect(within(form).getByTestId('exception-new-rule-hint').textContent).toBe('Means: a container runs as root.')
    expect(save.disabled).toBe(true)
    fireEvent.change(within(form).getByTestId('exception-new-object'), { target: { value: 'web' } })
    fireEvent.change(within(form).getByTestId('exception-new-reason'), { target: { value: 'The image needs root.' } })
    expect(save.disabled).toBe(false)
    fireEvent.click(save)
    expect((await within(form).findByTestId('exception-new-error')).textContent).toContain('name the object')
    fireEvent.change(within(form).getByTestId('exception-new-object'), { target: { value: 'shop/Deployment/web' } })
    fireEvent.change(within(form).getByTestId('exception-new-expires'), { target: { value: '2099-12-31' } })
    fireEvent.click(save)
    await waitFor(() => expect(api.createException).toHaveBeenLastCalledWith({ cluster: 'local', resource: 'shop/Deployment/web', check: 'pod.root-user', reason: 'The image needs root.', expires: '2099-12-31' }))
    await waitFor(() => expect(toast).toHaveBeenCalledWith('EX-4 is created: the agents stop reporting it.'))
    expect(screen.queryByTestId('exception-new')).toBeNull()
    expect(window.location.hash).toBe('#/aiops/mycroft/exceptions/EX-4')
    // A cluster with no rules seen: type one.
    fireEvent.click(screen.getByTestId('exceptions-new'))
    fireEvent.change(screen.getByTestId('exception-new-cluster'), { target: { value: 'prod' } })
    expect(screen.getByTestId('exception-new-rule-hint').textContent).toContain('No validation rules seen on this cluster yet')
    cleanup()
    // A reader has no New exception.
    vi.mocked(api.fetchExceptions).mockResolvedValue({ exceptions: [ex('EX-3', 'active', 'No network policy')], mayChange: false })
    render(<ExceptionsView open="" />)
    await screen.findAllByTestId('exception')
    expect(screen.queryByTestId('exceptions-new')).toBeNull()
  })

  it('is as wide as the other Mycroft tables (build 14)', async () => {
    render(<ExceptionsView open="" />)
    expect((await screen.findByTestId('exceptions-table')).style.minWidth).toBe(`${MYCROFT_TABLE_MIN}px`)
  })

  it('has no RCAs: neither a view nor an API to list them (wave 6e)', () => {
    expect(Object.keys(views).sort()).toEqual(['ExceptionsView', 'exceptionsShown'])
    expect(Object.keys(api).filter((k) => /rca/i.test(k))).toEqual([])
  })

  it('opens Mycroft › Exceptions on an exception’s old and new addresses, with the counts on the sub-tabs', async () => {
    const access: CasesAccess = { show: true, approver: true, operator: false, reporter: false, console: true, superAdmin: true, tabs: ['inbox', 'jobs', 'mycroft'] }
    for (const hash of ['#/aiops/knowledge/exceptions/EX-3', '#/aiops/mycroft/exceptions/EX-3']) {
      render(<MycroftTab route={parseHash(hash)} access={access} />)
      expect(await screen.findByTestId('knowledge-exceptions')).toBeTruthy()
      expect(screen.getByTestId('mycroft-sub-exceptions').getAttribute('aria-pressed')).toBe('true')
      await waitFor(() => expect(screen.getByTestId('mycroft-sub-exceptions').textContent).toBe('Exceptions1'))
      expect(
        screen
          .getAllByTestId('exception')
          .find((c) => c.getAttribute('aria-current') === 'true')
          ?.getAttribute('data-id'),
      ).toBe('EX-3')
      cleanup()
    }
    // No RCAs, which are part of their cases now (wave 6e); Fixes since wave 6g; Validation rules in Jobs since CF-89.
    render(<MycroftTab route={parseHash('#/aiops/mycroft/exceptions')} access={access} />)
    expect(await screen.findByTestId('knowledge-exceptions')).toBeTruthy()
    expect(
      screen
        .getAllByRole('button')
        .map((b) => b.getAttribute('data-testid'))
        .filter((t) => t?.startsWith('mycroft-sub-')),
    ).toEqual(['mycroft-sub-wiki', 'mycroft-sub-known', 'mycroft-sub-exceptions', 'mycroft-sub-fixes'])
  })

  it('tells a role without the records so', async () => {
    vi.mocked(api.fetchExceptions).mockRejectedValue(new CallError('not allowed', 403))
    render(<ExceptionsView open="" />)
    expect((await screen.findByRole('alert')).textContent).toContain('Your role does not include these records')
  })
})
