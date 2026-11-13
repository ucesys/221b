import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import type { CasesAccess } from '../../lib/cases'
import { forgetSharedForTests } from '../../lib/cases'
import { parseHash } from '../../lib/router'
import { MycroftTab, type MycroftSub } from './MycroftTab'
import { MYCROFT_TABLE_MIN } from './mycroftTable'

vi.mock('../../api/knowledge', async (original) => ({
  ...(await original<typeof import('../../api/knowledge')>()),
  fetchWiki: vi.fn(() => new Promise(() => undefined)),
  fetchPages: vi.fn(() => Promise.resolve({ pages: [], connected: true })),
  fetchDrafts: vi.fn(() => Promise.resolve({ runs: [] })),
  searchKnowledge: vi.fn(() => Promise.resolve({ hits: [] })),
  findInMycroft: vi.fn(() => Promise.resolve({ hits: [{ kind: 'fix', id: 'F-2', title: 'Raise the memory', status: 'active', text: 'OOMKilled', score: 1 }], counts: { fix: 1 }, kinds: ['page', 'fix'] })),
}))
vi.mock('../../api/issues', async (original) => ({
  ...(await original<typeof import('../../api/issues')>()),
  fetchIssueMe: vi.fn(() => new Promise(() => undefined)),
  fetchKnownAll: vi.fn(() => Promise.resolve([])),
  fetchServices: vi.fn(() => Promise.resolve([])),
}))
vi.mock('../../api/lessons', async (original) => ({
  ...(await original<typeof import('../../api/lessons')>()),
  fetchExceptions: vi.fn(() => Promise.resolve({ exceptions: [], mayChange: true })),
}))
vi.mock('../../api/fixes', async (original) => ({
  ...(await original<typeof import('../../api/fixes')>()),
  fetchFixes: vi.fn(() => Promise.resolve({ fixes: [], mayChange: true })),
}))
vi.mock('../../api/rules', async (original) => ({
  ...(await original<typeof import('../../api/rules')>()),
  fetchRules: vi.fn(() => Promise.resolve({ rules: [], found: [], suggestions: [], jobs: [], types: [], mayChange: true })),
}))

const access: CasesAccess = { show: true, approver: true, operator: true, reporter: false, console: true, superAdmin: true, tabs: ['inbox', 'jobs', 'mycroft'] }

// Each sub-tab: its page, the filter first in its toolbar, its main
// action last (Fixes has none, so the spacer is), and its table.
const SUBS: [MycroftSub, string, string, string | null, string | null][] = [
  ['wiki', 'knowledge-tab', 'knowledge-view-pages', null, null],
  ['known', 'cases-known', 'known-show-open', 'known-new', 'known-table'],
  ['exceptions', 'knowledge-exceptions', 'exceptions-show-active', 'exceptions-new', 'exceptions-table'],
  ['fixes', 'mycroft-fixes', 'fixes-show-active', null, 'fixes-table'],
]

// Each sub-tab's intro, in the owner's words (CF-55).
const LEADS: [MycroftSub, string, string][] = [
  ['wiki', 'knowledge-tab', 'Mycroft’s notes on how your system works: how-tos, runbooks and good practices, written from your documents or public sources. Agents use only approved pages.'],
  ['known', 'cases-known', 'Known problems. A case can be closed as a Known issue. Agents will keep checking whether the problem still happens but will stop reporting on it.'],
  ['exceptions', 'knowledge-exceptions', 'Issues that have been accepted as-is. While an exception is active, agents stop creating cases for that object. Closing a case as an exception will create a new exception.'],
  [
    'fixes',
    'mycroft-fixes',
    'Actions that fixed a problem before. Created when a case is closed as Fixed. Agents will reuse it when the same problem comes back. Duplicates are cleaned up. A fix that contradicts another one waits for approval.',
  ],
]

describe('AIOps › Mycroft: one column and one page layout for every sub-tab (CF-55)', () => {
  beforeAll(() => {
    Element.prototype.scrollIntoView = () => {}
  })
  afterEach(() => {
    cleanup()
    forgetSharedForTests()
  })

  it.each(SUBS)('%s is a MycroftPage in the Mycroft column: intro, then one toolbar row with the filter first and the action last', async (sub, page, filter, action, table) => {
    render(<MycroftTab route={parseHash(`#/aiops/mycroft/${sub}`)} access={access} />)
    const root = screen.getByTestId('mycroft-tab')
    expect(root.classList.contains('cs-col')).toBe(true)
    const el = await screen.findByTestId(page)
    expect(el.parentElement).toBe(root)
    expect(el.classList.contains('cs-col')).toBe(true)
    const bar = within(el).getByTestId('mycroft-toolbar')
    expect(el.children[0]!.classList.contains('cs-lead')).toBe(true)
    expect(el.children[1]).toBe(bar)
    expect(bar.classList.contains('cs-bar')).toBe(true)
    const first = bar.firstElementChild!
    expect(first.classList.contains('rk-seg')).toBe(true)
    expect(within(first as HTMLElement).getByTestId(filter)).toBeTruthy()
    if (action) await waitFor(() => expect(bar.lastElementChild!.getAttribute('data-testid')).toBe(action))
    else expect(bar.lastElementChild!.classList.contains('cs-grow')).toBe(true)
    if (!table) return
    // The same card and table in all three; empty, it keeps its header and says so in one row.
    const t = await within(el).findByTestId(table)
    expect(t.classList.contains('tb-table')).toBe(true)
    expect(t.style.minWidth).toBe(`${MYCROFT_TABLE_MIN}px`)
    expect(t.parentElement!.classList.contains('rk-card')).toBe(true)
    expect(t.querySelector('thead th')).toBeTruthy()
    await waitFor(() => expect(t.querySelectorAll('tbody tr')).toHaveLength(1))
    expect(t.querySelector('tbody td')!.getAttribute('colspan')).toBeTruthy()
  })

  it.each(LEADS)('%s says plainly what it holds', async (sub, page, lead) => {
    render(<MycroftTab route={parseHash(`#/aiops/mycroft/${sub}`)} access={access} />)
    const el = await screen.findByTestId(page)
    expect(el.querySelector('.cs-lead')!.textContent).toBe(lead)
  })

  it('has no Validation rules, which are in Jobs since CF-89', () => {
    render(<MycroftTab route={parseHash('#/aiops/mycroft/wiki')} access={access} />)
    expect(
      screen
        .getAllByRole('button')
        .map((b) => b.getAttribute('data-testid'))
        .filter((t) => t?.startsWith('mycroft-sub-')),
    ).toEqual(['mycroft-sub-wiki', 'mycroft-sub-known', 'mycroft-sub-exceptions', 'mycroft-sub-fixes'])
    expect(screen.queryByTestId('rules-page')).toBeNull()
  })

  it('searches every sub-tab from one box beside them: the results take the sub-tab’s place until a sub-tab is picked (KN-35)', async () => {
    render(<MycroftTab route={parseHash('#/aiops/mycroft/known')} access={access} />)
    expect(await screen.findByTestId('cases-known')).toBeTruthy()
    fireEvent.change(screen.getByTestId('mycroft-search'), { target: { value: 'memory' } })
    expect((await screen.findByTestId('mycroft-search-hit')).textContent).toContain('F-2')
    expect(screen.queryByTestId('cases-known')).toBeNull()
    expect(screen.getByTestId('mycroft-sub-known').getAttribute('aria-pressed')).toBe('false')
    fireEvent.click(screen.getByTestId('mycroft-sub-known'))
    expect(await screen.findByTestId('cases-known')).toBeTruthy()
    expect((screen.getByTestId('mycroft-search') as HTMLInputElement).value).toBe('')
  })
})
