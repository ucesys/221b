import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
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

const access: CasesAccess = { show: true, approver: true, operator: true, reporter: false, console: true, tabs: ['inbox', 'jobs', 'mycroft'] }

// Each sub-tab: its page, the filter first in its toolbar, its main
// action last (Fixes has none, so the spacer is), and its table.
const SUBS: [MycroftSub, string, string, string | null, string | null][] = [
  ['wiki', 'knowledge-tab', 'knowledge-view-pages', null, null],
  ['known', 'cases-known', 'known-show-open', 'known-new', 'known-table'],
  ['exceptions', 'knowledge-exceptions', 'exceptions-show-active', 'exceptions-new', 'exceptions-table'],
  ['fixes', 'mycroft-fixes', 'fixes-show-active', null, 'fixes-table'],
  ['rules', 'mycroft-rules', 'rules-show-active', 'rule-add', 'rules-table'],
]

describe('Cases › Mycroft: one column and one page layout for every sub-tab (CF-55)', () => {
  beforeAll(() => {
    Element.prototype.scrollIntoView = () => {}
  })
  afterEach(() => {
    cleanup()
    forgetSharedForTests()
  })

  it.each(SUBS)('%s is a MycroftPage in the Mycroft column: intro, then one toolbar row with the filter first and the action last', async (sub, page, filter, action, table) => {
    render(<MycroftTab route={parseHash(`#/cases/mycroft/${sub}`)} access={access} />)
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
    // The same card and table in all four; empty, it keeps its header and says so in one row.
    const t = await within(el).findByTestId(table)
    expect(t.classList.contains('tb-table')).toBe(true)
    expect(t.style.minWidth).toBe(`${MYCROFT_TABLE_MIN}px`)
    expect(t.parentElement!.classList.contains('rk-card')).toBe(true)
    expect(t.querySelector('thead th')).toBeTruthy()
    await waitFor(() => expect(t.querySelectorAll('tbody tr')).toHaveLength(1))
    expect(t.querySelector('tbody td')!.getAttribute('colspan')).toBeTruthy()
  })

  it('keeps the rules’ job select and search between the filter and Add rule', async () => {
    render(<MycroftTab route={parseHash('#/cases/mycroft/rules')} access={access} />)
    const bar = within(await screen.findByTestId('mycroft-rules')).getByTestId('mycroft-toolbar')
    await waitFor(() => expect(within(bar).getByTestId('rule-add')).toBeTruthy())
    const order = [...bar.querySelectorAll('[data-testid]')].map((x) => x.getAttribute('data-testid')).filter((x) => ['rules-show', 'rules-job', 'rules-search', 'rule-add'].includes(x!))
    expect(order).toEqual(['rules-show', 'rules-job', 'rules-search', 'rule-add'])
  })
})
