import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { WikiState } from '../../api/knowledge'
import { parseHash } from '../../lib/router'
import { AddedInMycroft } from './AddedInMycroft'

// Settings › Knowledge's Added in Mycroft card (Phase 12, MY-11): the form
// that was AIOps › Mycroft › Wiki › Add, with what was added.

const state = (over: Partial<WikiState> = {}): WikiState => ({
  store: 's3',
  model: true,
  pages: 2,
  excerpts: 5,
  running: false,
  mayAdd: true,
  mayReview: true,
  maySetup: false,
  sources: [{ id: 'added', name: 'Added in Mycroft', kind: 'upload', items: 1 }],
  added: [{ source: 'added', id: 'note Backups.md', title: 'Backups', lastRead: '2026-11-05T02:00:00Z', pages: ['pages/practice/backup.md'] }],
  limits: { note: 200 << 10, document: 20 << 20, depth: 3, defaultDepth: 1, pages: 50, defaultPages: 20, formats: ['.pdf', '.docx', '.md', '.markdown', '.html', '.htm', '.txt', '.csv'] },
  ...over,
})

type Answer = (url: string, init?: RequestInit) => unknown
let answers: Record<string, Answer> = {}
let calls: { url: string; init?: RequestInit }[] = []

function stub(over: Record<string, Answer> = {}) {
  answers = { wiki: () => state(), ...over }
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

describe('Settings › Knowledge › Added in Mycroft: the form (MY-04 to MY-06, MY-11)', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })
  const posted = () => calls.filter((c) => c.url === '/api/v1/knowledge/add').map((c) => JSON.parse(String(c.init?.body)) as Record<string, unknown>)

  it('adds a note', async () => {
    stub({ add: (_u, init) => ({ kind: 'text', items: [{ id: 'note Shop.md', title: JSON.parse(String(init?.body)).title, bytes: 20 }], model: true, text: '“Shop” was added. Mycroft reads it now.' }) })
    const toast = vi.fn()
    render(<AddedInMycroft onToast={toast} />)
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
    render(<AddedInMycroft />)
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
    render(<AddedInMycroft />)
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
    render(<AddedInMycroft />)
    expect((await screen.findByTestId('wiki-add-refused')).textContent).toContain('No wiki is kept yet')
    expect(screen.getByTestId('wiki-add-title')).toHaveProperty('disabled', true)
    cleanup()
    stub({ wiki: () => state({ mayAdd: false }) })
    render(<AddedInMycroft />)
    expect((await screen.findByTestId('wiki-add-refused')).textContent).toBe('Admins and SuperAdmins add to the wiki.')
  })

  it('shows the server’s refusal', async () => {
    stub({ add: () => new Response(JSON.stringify({ error: 'nothing was read from https://x.example: it answered 404 Not Found' }), { status: 400 }) })
    render(<AddedInMycroft />)
    fireEvent.click(await screen.findByTestId('wiki-add-kind-url'))
    fireEvent.change(screen.getByTestId('wiki-add-url'), { target: { value: 'https://x.example' } })
    fireEvent.click(screen.getByTestId('wiki-add-submit'))
    expect((await screen.findByTestId('wiki-add-error')).textContent).toContain('404')
  })
})

describe('Settings › Knowledge › Added in Mycroft: the card (MY-07, MY-11)', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('lists what was added, and removes one', async () => {
    stub({ added: (_u, init) => (init?.method === 'DELETE' ? { removed: true } : {}) })
    const toast = vi.fn()
    render(<AddedInMycroft onToast={toast} />)
    const card = await screen.findByTestId('knowledge-added')
    expect(within(card).getByTestId('wiki-add')).toBeTruthy()
    const item = await within(card).findByTestId('wiki-added-item')
    expect(item.textContent).toContain('Backups')
    fireEvent.click(within(item).getByTestId('wiki-added-remove'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(calls.some((c) => c.url === '/api/v1/knowledge/added/note%20Backups.md' && c.init?.method === 'DELETE')).toBe(true))
    await waitFor(() => expect(toast).toHaveBeenCalledWith('“Backups” removed: the agents no longer quote it.'))
    cleanup()
    stub({ wiki: () => state({ added: [] }) })
    render(<AddedInMycroft />)
    expect((await screen.findByTestId('wiki-added-empty')).textContent).toBe('Nothing added yet.')
  })

  it('shows the fallback to someone who may not add, and scrolls to the card when asked', async () => {
    stub({ wiki: () => state({ mayAdd: false }) })
    render(<AddedInMycroft fallback={<p data-testid="refused">Refused</p>} />)
    expect(await screen.findByTestId('refused')).toBeTruthy()
    expect(screen.queryByTestId('knowledge-added')).toBeNull()
    cleanup()
    stub()
    const scrolled = vi.fn()
    Element.prototype.scrollIntoView = scrolled
    render(<AddedInMycroft focus fallback={<p data-testid="refused">Refused</p>} />)
    expect(await screen.findByTestId('wiki-add')).toBeTruthy()
    expect(screen.queryByTestId('refused')).toBeNull()
    await waitFor(() => expect(scrolled).toHaveBeenCalled())
  })

  it('is where the Wiki’s old Add address leads', () => {
    for (const hash of ['#/cases/mycroft/wiki/add', '#/cases/knowledge/add', '#/c/dev/cases/mycroft/wiki/add']) {
      expect(parseHash(hash), hash).toMatchObject({ view: 'admin', cluster: 'local', path: ['settings', 'knowledge', 'added'] })
    }
    expect(parseHash('#/cases/mycroft/wiki/sources')).toMatchObject({ view: 'cases', path: ['mycroft', 'wiki', 'sources'] })
  })
})
