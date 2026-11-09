import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { WikiPage, WikiState } from '../../api/knowledge'
import { adminHref, parseHash, type Route } from '../../lib/router'
import { KnowledgeTab, pageHref, wikiRoute } from './KnowledgeTab'
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

const route = (path: string[]): Route => ({ cluster: 'local', view: 'cases', tab: 'alerts', path, params: new URLSearchParams() }) as unknown as Route

type Answer = (url: string, init?: RequestInit) => unknown
let answers: Record<string, Answer> = {}
let calls: { url: string; init?: RequestInit }[] = []

function stub(over: Record<string, Answer> = {}) {
  answers = {
    wiki: () => state(),
    pages: () => ({ pages, connected: true }),
    page: () => ({ ...pages[0], body: '# Shop\n\nShop runs on **db-3** (source: “Shop”, version 4, 2026-10-01).', sources: [{ key: 'infra:1', title: 'Shop', version: '4', date: '2026-10-01T00:00:00Z', url: 'https://wiki.example.com/shop' }] }),
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

describe('Cases › Mycroft › Wiki', () => {
  beforeEach(() => stub())
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('browses the approved pages, grouped by kind, with a page’s sources and freshness', async () => {
    render(<KnowledgeTab route={route(['wiki', 'pages', 'application', 'shop.md'])} />)
    const links = await screen.findAllByTestId('knowledge-page-link')
    expect(links.map((l) => l.textContent)).toEqual(['Application: shop', 'Backup policy'])
    expect(links[0]!.getAttribute('href')).toBe('#/cases/mycroft/wiki/pages/application/shop.md')
    const page = await screen.findByTestId('knowledge-page')
    expect(screen.getByTestId('knowledge-page-stale')).toBeTruthy()
    expect(screen.getByTestId('knowledge-page-sources').textContent).toContain('Shop, version 4, 2026-10-01')
    expect([...page.querySelectorAll('.rk-ans-body strong')].map((x) => x.textContent)).toEqual(['Shop', 'db-3'])
  })

  it('searches the pages and the sources’ excerpts', async () => {
    render(<KnowledgeTab route={route(['wiki'])} />)
    fireEvent.change(await screen.findByTestId('knowledge-search'), { target: { value: 'db-3' } })
    await waitFor(() => expect(screen.getByTestId('knowledge-hits').textContent).toContain('Server inventory'))
  })

  it('shows the drafts, and each view has its address', async () => {
    render(<KnowledgeTab route={route(['wiki', 'drafts'])} />)
    expect((await screen.findByTestId('knowledge-drafts')).textContent).toContain('No changes from Mycroft yet.')
    expect(pageHref('pages/runbook/a b.md')).toBe('#/cases/mycroft/wiki/pages/runbook/a%20b.md')
    expect(wikiRoute(parseHash('#/cases/mycroft/wiki/sources'))).toEqual({ view: 'sources', page: '' })
    expect(wikiRoute(parseHash('#/cases/knowledge/pages/system/db.md'))).toEqual({ view: 'pages', page: 'pages/system/db.md' })
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
    expect(within(item).getByText('practice/backup.md').getAttribute('href')).toBe('#/cases/mycroft/wiki/pages/practice/backup.md')
    fireEvent.click(within(item).getByTestId('wiki-added-remove'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(calls.some((c) => c.url === '/api/v1/knowledge/added/note%20Backups.md' && c.init?.method === 'DELETE')).toBe(true))
    await waitFor(() => expect(toast).toHaveBeenCalledWith('“Backups” removed: the agents no longer quote it.'))
  })
})

describe('Cases › Mycroft › Wiki › Add', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })
  const posted = () => calls.filter((c) => c.url === '/api/v1/knowledge/add').map((c) => JSON.parse(String(c.init?.body)) as Record<string, unknown>)

  it('adds a note', async () => {
    stub({ add: (_u, init) => ({ kind: 'text', items: [{ id: 'note Shop.md', title: JSON.parse(String(init?.body)).title, bytes: 20 }], model: true, text: '“Shop” was added. Mycroft reads it now.' }) })
    const toast = vi.fn()
    render(<KnowledgeTab route={route(['wiki', 'add'])} onToast={toast} />)
    const submit = await screen.findByTestId('wiki-add-submit')
    expect(submit).toHaveProperty('disabled', true)
    fireEvent.change(screen.getByTestId('wiki-add-title'), { target: { value: 'Shop' } })
    fireEvent.change(screen.getByTestId('wiki-add-text'), { target: { value: 'The shop runs on db-3.' } })
    fireEvent.click(submit)
    expect((await screen.findByTestId('wiki-add-result')).textContent).toContain('Mycroft reads it now.')
    expect(posted()).toEqual([{ kind: 'text', title: 'Shop', text: 'The shop runs on db-3.' }])
    expect(toast).toHaveBeenCalledWith('“Shop” added to the wiki’s sources.')
  })

  it('reads a web address as deep and as many pages as asked, and says what it left out', async () => {
    stub({
      add: () => ({
        kind: 'url',
        items: [{ id: 'web a', title: 'Docs', url: 'https://docs.example.com/', bytes: 2048 }],
        skipped: [{ url: 'https://docs.example.com/private', why: 'robots.txt asks crawlers not to read it' }],
        model: true,
        text: '1 pages were read and added. 1 were left out.',
      }),
    })
    render(<KnowledgeTab route={route(['wiki', 'add'])} />)
    fireEvent.click(await screen.findByTestId('wiki-add-kind-url'))
    fireEvent.change(screen.getByTestId('wiki-add-url'), { target: { value: 'https://docs.example.com/' } })
    fireEvent.change(screen.getByTestId('wiki-add-depth'), { target: { value: '2' } })
    fireEvent.change(screen.getByTestId('wiki-add-pages'), { target: { value: '5' } })
    fireEvent.click(screen.getByTestId('wiki-add-submit'))
    expect((await screen.findByTestId('wiki-add-skipped')).textContent).toContain('robots.txt')
    expect(posted()).toEqual([{ kind: 'url', url: 'https://docs.example.com/', depth: 2, pages: 5 }])
  })

  it('adds documents one by one, and refuses a kind it does not read before sending', async () => {
    stub({ add: (_u, init) => ({ kind: 'document', items: [{ id: JSON.parse(String(init?.body)).name, title: JSON.parse(String(init?.body)).name, bytes: 3 }], model: true, text: 'added.' }) })
    render(<KnowledgeTab route={route(['wiki', 'add'])} />)
    fireEvent.click(await screen.findByTestId('wiki-add-kind-document'))
    const input = screen.getByTestId('wiki-add-file')
    fireEvent.change(input, { target: { files: [new File(['x'], 'photo.png')] } })
    expect(screen.getByTestId('wiki-add-files').textContent).toContain('not a kind the console reads')
    expect(screen.getByTestId('wiki-add-submit')).toHaveProperty('disabled', true)
    fireEvent.change(input, { target: { files: [new File(['a,b\n1,2'], 'hosts.csv'), new File(['# Runbook'], 'runbook.md')] } })
    fireEvent.click(screen.getByTestId('wiki-add-submit'))
    await waitFor(() => expect(posted().map((p) => p.name)).toEqual(['hosts.csv', 'runbook.md']))
    expect(posted()[0]!.data).toBe(btoa('a,b\n1,2'))
  })

  it('says why nothing can be added: no store, or not an Admin', async () => {
    stub({ wiki: () => state({ store: '' }) })
    render(<KnowledgeTab route={route(['wiki', 'add'])} />)
    expect((await screen.findByTestId('wiki-add-refused')).textContent).toContain('No wiki is kept yet')
    expect(screen.getByTestId('wiki-add-title')).toHaveProperty('disabled', true)
    cleanup()
    stub({ wiki: () => state({ mayAdd: false }) })
    render(<KnowledgeTab route={route(['wiki', 'add'])} />)
    expect((await screen.findByTestId('wiki-add-refused')).textContent).toBe('Admins and SuperAdmins add to the wiki.')
  })

  it('shows the server’s refusal', async () => {
    stub({ add: () => new Response(JSON.stringify({ error: 'nothing was read from https://x.example: it answered 404 Not Found' }), { status: 400 }) })
    render(<KnowledgeTab route={route(['wiki', 'add'])} />)
    fireEvent.click(await screen.findByTestId('wiki-add-kind-url'))
    fireEvent.change(screen.getByTestId('wiki-add-url'), { target: { value: 'https://x.example' } })
    fireEvent.click(screen.getByTestId('wiki-add-submit'))
    expect((await screen.findByTestId('wiki-add-error')).textContent).toContain('404')
  })
})
