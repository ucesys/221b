import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { UploadItem, UploadsView } from '../../api/knowledge'
import { UPLOAD_ROWS, UploadedDocuments, uploadsShown } from './UploadedDocuments'

// Administration › AI › Knowledge › Uploaded documents (0.12.1): notes and documents
// from both built-in sources of uploads, in two tabs.

const item = (over: Partial<UploadItem>): UploadItem => ({ source: 'added', id: 'x', type: 'note', title: 'x', state: 'read', ...over })

const view = (over: Partial<UploadsView> = {}): UploadsView => ({
  items: [
    item({ id: 'note Backups.md', title: 'Backups', by: 'Ann', at: '2026-11-05T02:00:00Z', pages: ['pages/practice/backup.md'] }),
    item({ id: 'note Shop.md', title: 'Shop', by: 'Bob', at: '2026-11-06T02:00:00Z', state: 'reading' }),
    item({ source: 'documents', id: 'plan.docx', type: 'document', title: 'plan.docx', by: 'Ann', at: '2026-11-01T00:00:00Z', state: 'error', error: 'it is not a Word document' }),
  ],
  limits: { note: 200 << 10, document: 20 << 20, depth: 3, defaultDepth: 1, pages: 50, defaultPages: 20, formats: ['.pdf', '.docx', '.md', '.txt', '.csv'] },
  mayAdd: true,
  maySetup: true,
  store: 'git',
  model: true,
  off: [],
  ...over,
})

type Answer = (url: string, init?: RequestInit) => unknown
let answers: Record<string, Answer> = {}
let calls: { url: string; init?: RequestInit }[] = []

function stub(over: Record<string, Answer> = {}) {
  answers = { uploads: () => view(), ...over }
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

describe('Administration › AI › Knowledge › Uploaded documents', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })
  const body = (u: string) => calls.filter((c) => c.url === u).map((c) => JSON.parse(String(c.init?.body)) as Record<string, unknown>)

  it('lists notes and documents in two tabs, newest first, with who added them, when, and their state', async () => {
    stub()
    render(<UploadedDocuments />)
    const rows = await screen.findAllByTestId('uploads-row')
    expect(rows.map((r) => r.dataset.id)).toEqual(['note Shop.md', 'note Backups.md'])
    expect(rows[0]!.textContent).toContain('Bob')
    expect(within(rows[0]!).getByTestId('uploads-state').dataset.state).toBe('reading')
    expect(screen.getByTestId('uploads-tab-notes').textContent).toContain('2')
    fireEvent.click(screen.getByTestId('uploads-tab-documents'))
    const docs = screen.getAllByTestId('uploads-row')
    expect(docs.map((r) => r.dataset.id)).toEqual(['plan.docx'])
    expect(within(docs[0]!).getByTestId('uploads-state').textContent).toBe('Error: it is not a Word document')
  })

  it('filters by title or who added it, and shows 25 rows until Show more', async () => {
    const many = Array.from({ length: 30 }, (_, i) => item({ id: `note n${i}.md`, title: `Note ${i}`, by: i % 2 ? 'Ann' : 'Bob', at: `2026-11-01T00:00:${String(i).padStart(2, '0')}Z` }))
    stub({ uploads: () => view({ items: many }) })
    render(<UploadedDocuments />)
    expect(await screen.findAllByTestId('uploads-row')).toHaveLength(UPLOAD_ROWS)
    fireEvent.click(screen.getByTestId('uploads-more'))
    expect(screen.getAllByTestId('uploads-row')).toHaveLength(30)
    expect(screen.queryByTestId('uploads-more')).toBeNull()
    fireEvent.change(screen.getByTestId('uploads-filter'), { target: { value: 'ann' } })
    expect(screen.getAllByTestId('uploads-row')).toHaveLength(15)
    fireEvent.change(screen.getByTestId('uploads-filter'), { target: { value: 'note 29' } })
    expect(screen.getAllByTestId('uploads-row').map((r) => r.dataset.id)).toEqual(['note n29.md'])
    fireEvent.change(screen.getByTestId('uploads-filter'), { target: { value: 'nothing' } })
    expect(screen.getByTestId('uploads-empty').textContent).toContain('No note has that title')
    expect(uploadsShown(many, 'document', '')).toEqual([])
  })

  it('adds a note: a title and a text', async () => {
    stub({ add: (_u, init) => ({ kind: 'text', items: [{ id: 'note Shop.md', title: JSON.parse(String(init?.body)).title, bytes: 20 }], model: true, text: '“Shop” was added.' }) })
    const toast = vi.fn()
    render(<UploadedDocuments onToast={toast} />)
    fireEvent.click(await screen.findByTestId('uploads-note-open'))
    const submit = screen.getByTestId('uploads-note-submit')
    expect(submit).toHaveProperty('disabled', true)
    fireEvent.change(screen.getByTestId('uploads-note-title'), { target: { value: 'Shop' } })
    fireEvent.change(screen.getByTestId('uploads-note-text'), { target: { value: 'The shop runs on db-3.' } })
    fireEvent.click(submit)
    await waitFor(() => expect(toast).toHaveBeenCalledWith('“Shop” was added.'))
    expect(body('/api/v1/knowledge/add')).toEqual([{ kind: 'text', title: 'Shop', text: 'The shop runs on db-3.' }])
    expect(screen.queryByTestId('uploads-note-form')).toBeNull()
  })

  it('uploads documents one request each, and refuses a kind it does not read before sending', async () => {
    stub({ add: () => ({ kind: 'document', items: [], model: true, text: '' }) })
    render(<UploadedDocuments />)
    fireEvent.click(await screen.findByTestId('uploads-tab-documents'))
    const input = screen.getByTestId('uploads-file') as HTMLInputElement
    fireEvent.change(input, { target: { files: [new File(['x'], 'a.exe')] } })
    expect((await screen.findByTestId('uploads-upload-error')).textContent).toContain('a.exe is not a kind the console reads')
    expect(body('/api/v1/knowledge/add')).toEqual([])
    fireEvent.change(input, { target: { files: [new File(['# a'], 'a.md'), new File(['b'], 'b.txt')] } })
    await waitFor(() => expect(body('/api/v1/knowledge/add').map((b) => b.name)).toEqual(['a.md', 'b.txt']))
    expect(body('/api/v1/knowledge/add')[0]).toMatchObject({ kind: 'document', data: btoa('# a') })
  })

  it('uploads with the trust a SuperAdmin chose, its source’s by default; an Admin chooses none (KN-61)', async () => {
    stub({ add: () => ({ kind: 'document', items: [], model: true, text: '' }), uploads: () => view({ sourceTrust: 'high' }) })
    render(<UploadedDocuments />)
    fireEvent.click(await screen.findByTestId('uploads-tab-documents'))
    const trust = screen.getByTestId('uploads-trust') as HTMLSelectElement
    expect([...trust.options].map((o) => o.textContent)).toEqual(['Source’s (High)', 'High', 'Normal', 'Low'])
    expect(trust.value).toBe('')
    const input = screen.getByTestId('uploads-file') as HTMLInputElement
    fireEvent.change(input, { target: { files: [new File(['# a'], 'a.md')] } })
    await waitFor(() => expect(body('/api/v1/knowledge/add')).toHaveLength(1))
    expect(body('/api/v1/knowledge/add')[0]).toMatchObject({ name: 'a.md', trust: '' })
    fireEvent.change(trust, { target: { value: 'low' } })
    fireEvent.change(input, { target: { files: [new File(['# b'], 'b.md')] } })
    await waitFor(() => expect(body('/api/v1/knowledge/add')).toHaveLength(2))
    expect(body('/api/v1/knowledge/add')[1]).toMatchObject({ name: 'b.md', trust: 'low' })
    cleanup()
    stub({ add: () => ({ kind: 'document', items: [], model: true, text: '' }), uploads: () => view({ maySetup: false }) })
    render(<UploadedDocuments />)
    fireEvent.click(await screen.findByTestId('uploads-tab-documents'))
    expect(screen.queryByTestId('uploads-trust')).toBeNull()
    fireEvent.change(screen.getByTestId('uploads-file'), { target: { files: [new File(['# c'], 'c.md')] } })
    await waitFor(() => expect(body('/api/v1/knowledge/add')).toHaveLength(1))
    expect(body('/api/v1/knowledge/add')[0]).not.toHaveProperty('trust')
  })

  it('removes an item: the dialog names the pages that drew on it, then offers Review those pages and Ask Mycroft to rewrite', async () => {
    stub({
      uploads: (_u, init) => (init?.method === 'DELETE' ? { removed: true, pages: [{ path: 'pages/practice/backup.md', title: 'Backup policy' }] } : view()),
      rewrite: () => ({ sent: 1, text: 'Mycroft rewrites 1 page; the redraft waits for review under Drafts.' }),
    })
    render(<UploadedDocuments />)
    const row = (await screen.findAllByTestId('uploads-row')).find((r) => r.dataset.id === 'note Backups.md')!
    fireEvent.click(within(row).getByTestId('uploads-remove'))
    expect(screen.getByTestId('uploads-remove-pages').textContent).toContain('practice/backup.md')
    fireEvent.click(screen.getByTestId('confirm-ok'))
    const impact = await screen.findByTestId('source-impact')
    expect(calls.some((c) => c.url === '/api/v1/knowledge/uploads/added/note%20Backups.md' && c.init?.method === 'DELETE')).toBe(true)
    expect(within(impact).getByTestId('source-impact-pages').textContent).toBe('Backup policy')
    expect(within(impact).getByTestId('source-impact-review').getAttribute('href')).toBe('#/aiops/mycroft/wiki?pages=pages%2Fpractice%2Fbackup.md')
    fireEvent.click(within(impact).getByTestId('source-impact-rewrite'))
    expect((await within(impact).findByTestId('source-impact-sent')).textContent).toContain('Mycroft rewrites 1 page')
    expect(body('/api/v1/knowledge/rewrite')).toEqual([{ pages: ['pages/practice/backup.md'], title: 'Backups', why: 'removed' }])
  })

  it('says a built-in source is off, and a SuperAdmin turns it on', async () => {
    stub({ uploads: () => view({ off: [{ id: 'added', name: 'Added in Mycroft' }] }), sources: () => ({ on: true }) })
    render(<UploadedDocuments />)
    expect((await screen.findByTestId('uploads-off')).textContent).toContain('Added in Mycroft is switched off')
    fireEvent.click(screen.getByTestId('uploads-turn-on'))
    await waitFor(() => expect(body('/api/v1/knowledge/sources/added/on')).toEqual([{ on: true }]))
  })

  it('shows the fallback to someone who may not add', async () => {
    stub({ uploads: () => new Response(JSON.stringify({ error: 'only an Admin or a SuperAdmin' }), { status: 403 }) })
    render(<UploadedDocuments fallback={<p data-testid="fallback">no</p>} />)
    expect(await screen.findByTestId('fallback')).toBeTruthy()
    expect(screen.queryByTestId('uploads-section')).toBeNull()
  })
})
