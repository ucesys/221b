import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { WikiPage, WikiState } from '../../api/knowledge'
import { adminHref, parseHash, type Route } from '../../lib/router'
import { KnowledgeTab, PAGE_ROWS, pageHref, pagesShown, wikiRoute } from './KnowledgeTab'
import { MYCROFT_TABLE_MIN } from './mycroftTable'

const pages: WikiPage[] = [
  { path: 'pages/application/shop.md', title: 'Application: shop', kind: 'application', updated: '2026-10-01T00:00:00Z', stale: true },
  { path: 'pages/practice/backup.md', title: 'Backup policy', kind: 'practice', critical: true },
]

const state = (over: Partial<WikiState> = {}): WikiState => ({
  store: 's3',
  model: true,
  pages: 2,
  excerpts: 5,
  running: false,
  mayAdd: true,
  mayReview: true,
  maySetup: false,
  sources: [
    { id: 'console', name: 'This console', kind: 'console', items: 12, lastRead: '2026-11-05T01:00:00Z' },
    { id: 'infra', name: 'Confluence INFRA', kind: 'confluence', items: 0, error: 'the token was refused' },
    { id: 'added', name: 'Added in Mycroft', kind: 'upload', items: 1 },
  ],
  added: [{ source: 'added', id: 'note Backups.md', title: 'Backups', lastRead: '2026-11-05T02:00:00Z', pages: ['pages/practice/backup.md'] }],
  limits: { note: 200 << 10, document: 20 << 20, depth: 3, defaultDepth: 1, pages: 50, defaultPages: 20, formats: ['.pdf', '.docx', '.md', '.markdown', '.html', '.htm', '.txt', '.csv'] },
  ...over,
})

const route = (path: string[]): Route => ({ cluster: 'local', view: 'aiops', tab: 'alerts', path, params: new URLSearchParams() }) as unknown as Route

type Answer = (url: string, init?: RequestInit) => unknown
let answers: Record<string, Answer> = {}
let calls: { url: string; init?: RequestInit }[] = []

function stub(over: Record<string, Answer> = {}) {
  answers = {
    wiki: () => state(),
    pages: () => ({ pages, connected: true }),
    page: () => ({
      ...pages[0],
      body: '# Shop\n\nShop runs on **db-3** (source: “Shop”, version 4, 2026-10-01).',
      sources: [{ key: 'infra:1', title: 'Shop', version: '4', date: '2026-10-01T00:00:00Z', url: 'https://wiki.example.com/shop' }],
    }),
    search: () => ({ hits: [{ kind: 'excerpt', title: 'Server inventory', version: '12', text: 'db-3 | 10.0.0.3', url: 'https://wiki.example.com/inv' }] }),
    drafts: () => ({ runs: [] }),
    ...over,
  }
  calls = []
  vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
    const u = String(url)
    calls.push({ url: u, init })
    const name = u.replace(/^\/api\/v1\/knowledge\//, '').split(/[?/]/)[0] ?? ''
    const fn = answers[name]
    if (!fn) return new Response('{}')
    const out = fn(u, init)
    if (out instanceof Response) return out
    return new Response(JSON.stringify(out))
  })
}

describe('AIOps › Mycroft › Wiki', () => {
  beforeEach(() => stub())
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('browses the approved pages, grouped by kind, with a page’s sources and freshness', async () => {
    render(<KnowledgeTab route={route(['wiki', 'pages', 'application', 'shop.md'])} />)
    const links = await screen.findAllByTestId('knowledge-page-link')
    expect(links.map((l) => l.textContent)).toEqual(['Application: shop', 'Backup policy'])
    expect(links[0]!.getAttribute('href')).toBe('#/aiops/mycroft/wiki/pages/application/shop.md')
    const page = await screen.findByTestId('knowledge-page')
    expect(screen.getByTestId('knowledge-page-stale')).toBeTruthy()
    expect(screen.getByTestId('knowledge-page-sources').textContent).toContain('Shop, version 4, 2026-10-01')
    expect([...page.querySelectorAll('.rk-ans-body strong')].map((x) => x.textContent)).toEqual(['Shop', 'db-3'])
  })

  it('filters the page list by title or path as it is typed; the full-text search is Mycroft’s (KN-36)', async () => {
    render(<KnowledgeTab route={route(['wiki'])} />)
    await screen.findAllByTestId('knowledge-page-link')
    fireEvent.change(screen.getByTestId('knowledge-filter'), { target: { value: 'backup' } })
    expect(screen.getAllByTestId('knowledge-page-link').map((l) => l.textContent)).toEqual(['Backup policy'])
    expect(screen.getByTestId('knowledge-filter-count').textContent).toBe('1 of 2 pages')
    fireEvent.change(screen.getByTestId('knowledge-filter'), { target: { value: 'application/sh' } })
    expect(screen.getAllByTestId('knowledge-page-link').map((l) => l.textContent)).toEqual(['Application: shop'])
    fireEvent.change(screen.getByTestId('knowledge-filter'), { target: { value: 'nothing like it' } })
    expect(screen.getByTestId('knowledge-filter-none').textContent).toContain('search at the top of Mycroft')
    fireEvent.keyDown(screen.getByTestId('knowledge-filter'), { key: 'Escape' })
    expect(screen.getAllByTestId('knowledge-page-link')).toHaveLength(2)
    // The search endpoint of the wiki alone is no longer asked from here.
    expect(calls.some((c) => c.url.includes('/knowledge/search'))).toBe(false)
  })

  it('keeps hundreds of pages usable: folded groups by kind, the first rows of each, stale ones alone, sorted by update (KN-36)', async () => {
    const many: WikiPage[] = Array.from({ length: 300 }, (_, i) => ({
      path: `pages/${i % 3 === 0 ? 'runbook' : 'application'}/svc-${String(i).padStart(3, '0')}.md`,
      title: `svc-${String(i).padStart(3, '0')}`,
      kind: i % 3 === 0 ? 'runbook' : 'application',
      updated: `2026-10-${String(1 + (i % 28)).padStart(2, '0')}T00:00:00Z`,
      stale: i % 50 === 0,
    }))
    stub({ pages: () => ({ pages: many, connected: true }) })
    render(<KnowledgeTab route={route(['wiki', 'pages', 'runbook', 'svc-003.md'])} />)
    const toggles = await screen.findAllByTestId('knowledge-group-toggle')
    expect(toggles.map((t) => t.textContent)).toEqual(['▸Applications 200', '▾Runbooks 100'])
    // Only the group holding the open page is unfolded, and it shows its first rows.
    expect(screen.getAllByTestId('knowledge-page-link')).toHaveLength(PAGE_ROWS)
    fireEvent.click(screen.getByTestId('knowledge-group-more'))
    expect(screen.getAllByTestId('knowledge-page-link')).toHaveLength(100)
    fireEvent.click(toggles[0]!)
    expect(screen.getAllByTestId('knowledge-page-link')).toHaveLength(100 + PAGE_ROWS)
    fireEvent.click(screen.getByTestId('knowledge-stale-only'))
    expect(screen.getAllByTestId('knowledge-page-link').map((l) => l.textContent)).toEqual(['svc-050', 'svc-100', 'svc-200', 'svc-250', 'svc-000', 'svc-150'])
    expect(pagesShown(many, '', false, 'updated')[0]!.updated).toBe('2026-10-28T00:00:00Z')
  })

  it('says when the ConfigMap store is nearly full, and where to move the wiki (KN-37)', async () => {
    stub({ wiki: () => state({ store: 'configmap', maySetup: true, usage: { used: 800 << 10, limit: 900 << 10 } }) })
    render(<KnowledgeTab route={route(['wiki'])} />)
    const room = await screen.findByTestId('wiki-store-room')
    expect(room.textContent).toContain('nearly full: 800 KiB of 900 KiB used (89%)')
    expect(screen.getByTestId('wiki-store-move').getAttribute('href')).toBe(adminHref('settings', ['knowledge']))
    cleanup()
    stub({ wiki: () => state({ store: 'configmap', usage: { used: 899 << 10, limit: 900 << 10 } }) })
    render(<KnowledgeTab route={route(['wiki'])} />)
    expect((await screen.findByTestId('wiki-store-room')).textContent).toContain('is full')
    expect(screen.getByTestId('wiki-store-room').textContent).toContain('A SuperAdmin moves it')
    cleanup()
    stub({ wiki: () => state({ store: 'configmap', usage: { used: 100 << 10, limit: 900 << 10 } }) })
    render(<KnowledgeTab route={route(['wiki'])} />)
    await screen.findAllByTestId('knowledge-page-link')
    expect(screen.queryByTestId('wiki-store-room')).toBeNull()
  })

  it('shows the drafts, and each view has its address', async () => {
    render(<KnowledgeTab route={route(['wiki', 'drafts'])} />)
    expect((await screen.findByTestId('knowledge-drafts')).textContent).toContain('No changes from Mycroft yet.')
    expect(pageHref('pages/runbook/a b.md')).toBe('#/aiops/mycroft/wiki/pages/runbook/a%20b.md')
    expect(wikiRoute(parseHash('#/aiops/mycroft/wiki/sources'))).toEqual({ view: 'sources', page: '' })
    expect(wikiRoute(parseHash('#/aiops/knowledge/pages/system/db.md'))).toEqual({ view: 'pages', page: 'pages/system/db.md' })
  })

  // The bug (MY-02): an answer with no pages left "Loading…" for good.
  it('ends the loading on any answer: no store says so and offers the setup to a SuperAdmin', async () => {
    stub({ wiki: () => state({ store: '', maySetup: true, pages: 0, excerpts: 0, added: [] }), pages: () => ({ pages: null, connected: false }) })
    render(<KnowledgeTab route={route(['wiki'])} />)
    expect((await screen.findByTestId('knowledge-empty')).textContent).toBe('No pages: no wiki is kept yet.')
    const banner = await screen.findByTestId('wiki-no-store')
    expect(banner.textContent).toContain('Settings › Knowledge')
    expect(within(banner).getByTestId('wiki-setup').getAttribute('href')).toBe(adminHref('settings', ['knowledge']))
    expect(screen.queryByText('Loading…')).toBeNull()
    cleanup()
    // Not a SuperAdmin: told who sets it up.
    stub({ wiki: () => state({ store: '', pages: 0 }), pages: () => ({ pages: [], connected: false }) })
    render(<KnowledgeTab route={route(['wiki'])} />)
    expect((await screen.findByTestId('wiki-no-store')).textContent).toContain('A SuperAdmin sets it up there.')
    expect(screen.queryByTestId('wiki-setup')).toBeNull()
  })

  it('says a failure instead of loading for good', async () => {
    stub({ pages: () => new Response(JSON.stringify({ error: 'sign in to the console to read the wiki' }), { status: 403 }) })
    render(<KnowledgeTab route={route(['wiki'])} />)
    expect((await screen.findByTestId('knowledge-pages-error')).textContent).toContain('Your role does not include the wiki')
  })

  it('lists the sources, with no address or credential, and removes what was added', async () => {
    stub({ added: (_u, init) => (init?.method === 'DELETE' ? { removed: true } : {}) })
    const toast = vi.fn()
    render(<KnowledgeTab route={route(['wiki', 'sources'])} onToast={toast} />)
    const rows = await screen.findAllByTestId('wiki-source')
    expect(rows.map((r) => r.getAttribute('data-source'))).toEqual(['console', 'infra', 'added'])
    // As wide as Mycroft's other tables (build 14).
    expect(screen.getByTestId('wiki-sources-table').style.minWidth).toBe(`${MYCROFT_TABLE_MIN}px`)
    expect(rows[1]!.textContent).toContain('Failed: the token was refused')
    const item = screen.getByTestId('wiki-added-item')
    expect(item.textContent).toContain('Backups')
    expect(within(item).getByText('practice/backup.md').getAttribute('href')).toBe('#/aiops/mycroft/wiki/pages/practice/backup.md')
    fireEvent.click(within(item).getByTestId('wiki-added-remove'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(calls.some((c) => c.url === '/api/v1/knowledge/added/note%20Backups.md' && c.init?.method === 'DELETE')).toBe(true))
    await waitFor(() => expect(toast).toHaveBeenCalledWith('“Backups” removed: the agents no longer quote it.'))
  })

  // MY-11: Add is in Settings › Knowledge only.
  it('has no Add view: Sources’ Add… and the empty wiki lead to Settings › Knowledge’s Added in Mycroft card', async () => {
    render(<KnowledgeTab route={route(['wiki', 'sources'])} />)
    expect((await screen.findByTestId('wiki-added-add')).getAttribute('href')).toBe(adminHref('settings', ['knowledge', 'added']))
    expect(screen.queryByTestId('knowledge-view-add')).toBeNull()
    expect(screen.getAllByRole('button').map((b) => b.textContent)).not.toContain('Add')
    cleanup()
    stub({ wiki: () => state({ added: [] }), pages: () => ({ pages: [], connected: true }) })
    render(<KnowledgeTab route={route(['wiki', 'sources'])} />)
    expect((await screen.findByTestId('wiki-added-empty')).querySelector('a')?.getAttribute('href')).toBe(adminHref('settings', ['knowledge', 'added']))
    cleanup()
    render(<KnowledgeTab route={route(['wiki'])} />)
    expect((await screen.findByTestId('knowledge-empty-add')).getAttribute('href')).toBe(adminHref('settings', ['knowledge', 'added']))
    cleanup()
    // A Viewer is told who adds, with no link.
    stub({ wiki: () => state({ added: [], mayAdd: false }) })
    render(<KnowledgeTab route={route(['wiki', 'sources'])} />)
    expect((await screen.findByTestId('wiki-added-empty')).textContent).toContain('Admins and SuperAdmins add')
    expect(screen.queryByTestId('wiki-added-add')).toBeNull()
  })
})

// A page's own actions (KN-23, KN-24): This is outdated…, Retire page…,
// Make active again…, and the retired pages listed apart.
describe('AIOps › Mycroft › Wiki › a page’s actions', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })
  const shop = () => ({ ...pages[0], body: '# Shop\n\nShop runs on db-3.' })
  const body = (name: string) => calls.filter((c) => c.url.startsWith('/api/v1/knowledge/' + name) && c.init?.method === 'POST').map((c) => JSON.parse(String(c.init?.body)) as Record<string, unknown>)

  it('This is outdated… sends a correction of the page, and the page then says a correction waits', async () => {
    let sent = false
    stub({
      page: () => (sent ? { ...shop(), correction: { id: 'correction shop.md', path: pages[0]!.path, title: 'Application: shop', by: 'bob', name: 'Bob', at: '2026-11-09T10:00:00Z', state: 'drafted', run: 'r-1' } } : shop()),
      outdated: () => {
        sent = true
        return { correction: {}, model: true, text: 'The correction was sent. Mycroft redrafts the page with it now; the redraft waits for review under Drafts.' }
      },
    })
    const toast = vi.fn()
    render(<KnowledgeTab route={route(['wiki', 'pages', 'application', 'shop.md'])} onToast={toast} />)
    fireEvent.click(await screen.findByTestId('wiki-page-outdated'))
    expect(screen.getByTestId('wiki-outdated-hint').textContent).toContain('the redraft waits for review')
    expect(screen.getByTestId('confirm-ok')).toHaveProperty('disabled', true)
    fireEvent.change(screen.getByTestId('wiki-outdated-text'), { target: { value: 'The shop runs on db-4 since November.' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(body('outdated')).toEqual([{ path: 'pages/application/shop.md', text: 'The shop runs on db-4 since November.' }]))
    expect((await screen.findByTestId('wiki-page-correction')).textContent).toMatch(/^Correction sent .+ by Bob; the redraft waits for review under Drafts\.$/)
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('Mycroft redrafts the page'))
    expect(screen.queryByTestId('confirm')).toBeNull()
  })

  it('says before sending that no page is written without a model; a reader has no actions', async () => {
    stub({ wiki: () => state({ model: false }), page: shop })
    render(<KnowledgeTab route={route(['wiki', 'pages', 'application', 'shop.md'])} />)
    fireEvent.click(await screen.findByTestId('wiki-page-outdated'))
    expect(screen.getByTestId('wiki-outdated-hint').textContent).toContain('no page is written')
    cleanup()
    stub({ wiki: () => state({ mayAdd: false, mayReview: false }), page: shop })
    render(<KnowledgeTab route={route(['wiki', 'pages', 'application', 'shop.md'])} />)
    await screen.findByTestId('knowledge-page')
    expect(screen.queryByTestId('wiki-page-actions')).toBeNull()
  })

  it('Retire page… asks with a reason; the request waiting is shown in place of the button', async () => {
    let asked = false
    stub({
      page: () => (asked ? { ...shop(), request: { id: 'p-1', action: 'retire', path: pages[0]!.path, title: 'Application: shop', reason: 'The shop moved.', by: 'ann', name: 'Ann', at: '2026-11-09T10:00:00Z', state: 'waiting' } } : shop()),
      requests: () => {
        asked = true
        return { id: 'p-1' }
      },
    })
    render(<KnowledgeTab route={route(['wiki', 'pages', 'application', 'shop.md'])} />)
    fireEvent.click(await screen.findByTestId('wiki-page-retire'))
    expect(screen.getByTestId('confirm-ok')).toHaveProperty('disabled', true)
    fireEvent.change(screen.getByTestId('wiki-request-reason'), { target: { value: 'The shop moved.' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(body('requests')).toEqual([{ action: 'retire', path: 'pages/application/shop.md', reason: 'The shop moved.' }]))
    const waiting = await screen.findByTestId('wiki-page-request')
    expect(waiting.textContent).toContain('Ann asked')
    expect(waiting.textContent).toContain('to retire this page: “The shop moved.”')
    expect(within(waiting).getByText('AIOps › Inbox').getAttribute('href')).toContain('#/aiops/inbox/knowledge/p-1')
    expect(screen.queryByTestId('wiki-page-retire')).toBeNull()
  })

  it('lists the retired pages under Retired; a retired page says why, who asked and approved, and offers Make active again only', async () => {
    const retirement = { reason: 'The NAS was replaced.', askedBy: 'Bob', askedAt: '2026-11-08T09:00:00Z', approvedBy: 'Ann', at: '2026-11-08T10:00:00Z' }
    const old = { path: 'pages/system/nas.md', title: 'NAS backups', kind: 'system', retired: retirement }
    stub({ pages: () => ({ pages, retired: [old], connected: true }), page: () => ({ ...old, body: 'Kept on nas-1.' }) })
    render(<KnowledgeTab route={route(['wiki', 'pages', 'system', 'nas.md'])} />)
    expect((await screen.findAllByTestId('knowledge-page-link')).map((l) => l.textContent)).toEqual(['Application: shop', 'Backup policy'])
    expect(screen.getByTestId('knowledge-pages-retired').textContent).toBe('Retired 1')
    fireEvent.click(screen.getByTestId('knowledge-pages-retired'))
    const link = await screen.findByTestId('knowledge-retired-link')
    expect(link.textContent).toBe('NAS backups')
    expect(screen.queryAllByTestId('knowledge-page-link')).toHaveLength(0)
    const notice = await screen.findByTestId('wiki-page-retired')
    expect(screen.getByTestId('wiki-page-retired-reason').textContent).toBe('The NAS was replaced.')
    expect(notice.textContent).toContain('Asked by Bob')
    expect(notice.textContent).toContain('approved by Ann')
    expect(screen.getByTestId('knowledge-page-retired')).toBeTruthy()
    expect(screen.queryByTestId('wiki-page-outdated')).toBeNull()
    expect(screen.queryByTestId('wiki-page-retire')).toBeNull()
    fireEvent.click(screen.getByTestId('wiki-page-activate'))
    fireEvent.change(screen.getByTestId('wiki-request-reason'), { target: { value: 'The NAS is back.' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(body('requests')).toEqual([{ action: 'activate', path: 'pages/system/nas.md', reason: 'The NAS is back.' }]))
    fireEvent.click(screen.getByTestId('knowledge-pages-active'))
    expect(await screen.findAllByTestId('knowledge-page-link')).toHaveLength(2)
  })
})
