import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { itemsUrl, type ItemsPage, type SourceItem, type SourcesView } from '../../api/knowledge'
import { fmtTs } from '../../lib/format'
import { lastSyncedText, MycroftSources } from './MycroftSources'
import { MYCROFT_TABLE_MIN } from './mycroftTable'

// AIOps › Mycroft › Sources (0.12.1): one row a source since
// phase-13.1-ai (KN-50); an opened source's items, paged on the server,
// with the exclusions.

const site: SourceItem = {
  source: 'vendor',
  sourceName: 'Vendor docs',
  kind: 'web',
  type: 'webPage',
  id: '/blog/old.html',
  title: 'Old post',
  url: 'https://docs.example.com/blog/old.html',
  state: 'read',
  pages: 2,
  excludable: true,
  path: '/blog/old.html',
  under: '/blog/*',
  underLabel: 'Vendor docs › /blog/',
}
// A bucket read under 221b/: its objects are shown relative to the prefix (KN-49).
const object = (key: string, under?: string, underLabel?: string): SourceItem => ({
  source: 'sandbox-knowledge',
  sourceName: 'sandbox-knowledge',
  kind: 's3',
  type: 's3Object',
  id: '221b/' + key,
  title: key.split('/').pop()!,
  path: key,
  state: 'read',
  excludable: true,
  under,
  underLabel,
})
const note: SourceItem = { source: 'added', sourceName: 'Uploaded documents', kind: 'upload', type: 'note', id: 'note Backups.md', title: 'Backups', by: 'Ann', state: 'read', pages: 0 }
const live: SourceItem = { source: 'status', sourceName: 'Status page', kind: 'live', type: 'liveAddress', id: 'https://status.example.com/', title: 'https://status.example.com/', state: 'live' }
const excluded: SourceItem = {
  ...site,
  id: '/blog/a.html',
  path: '/blog/a.html',
  title: 'A post',
  state: 'excluded',
  rule: 'Vendor docs › /blog/*',
  exclusion: { pattern: '/blog/*', by: 'Ann', at: '2026-11-10T10:00:00Z', reason: 'stale posts' },
}

const page = (over: Partial<ItemsPage> = {}): ItemsPage => ({
  items: [site, note, live],
  total: 3,
  offset: 0,
  limit: 50,
  sources: [
    { id: 'uploads', name: 'Uploaded documents', kind: 'uploads' },
    { id: 'vendor', name: 'Vendor docs', kind: 'web' },
    { id: 'status', name: 'Status page', kind: 'live' },
  ],
  mayExclude: true,
  mayRemove: true,
  ...over,
})

type Answer = (url: string, init?: RequestInit) => unknown
let answers: Record<string, Answer> = {}
let calls: { url: string; init?: RequestInit }[] = []

function stub(over: Record<string, Answer> = {}) {
  answers = { items: () => page(), ...over }
  calls = []
  vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
    const u = String(url)
    calls.push({ url: u, init })
    const rest = u.replace(/^\/api\/v1\/knowledge\//, '')
    const name = rest.startsWith('items/detail') ? 'detail' : rest.includes('/exclusions') ? 'exclusions' : (rest.split(/[?/]/)[0] ?? '')
    const fn = answers[name]
    if (!fn) return new Response('{}')
    const out = fn(u, init)
    if (out instanceof Response) return out
    return new Response(JSON.stringify(out))
  })
}

const itemCalls = () => calls.filter((c) => c.url.startsWith('/api/v1/knowledge/items?') || c.url === '/api/v1/knowledge/items').map((c) => c.url)

describe('AIOps › Mycroft › Sources', () => {
  beforeAll(() => {
    Element.prototype.scrollTo = () => {}
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('lists one row a source: its kind, its items, on or off, and its last sync; a row opens the source', async () => {
    const view: SourcesView = {
      sources: [
        { id: 'vendor', name: 'Vendor docs', kind: 'web', items: 120, failed: 2, excluded: 3, lastSync: { at: '2026-11-22T01:00:00Z', changed: 2 } },
        { id: 'repo', name: 'Runbooks', kind: 'git', items: 40, lastSync: { at: '2026-11-22T01:00:00Z', changed: 0, failed: true, error: 'authentication required' } },
        { id: 'bucket', name: 'Bucket', kind: 's3', off: true, items: 0 },
        { id: 'added', name: 'Added in Mycroft', kind: 'upload', items: 1, lastSync: { at: '2026-11-22T01:00:00Z', every: true } },
        { id: 'console', name: 'This console', kind: 'console', items: 1, lastSync: { at: '2026-11-22T01:00:00Z' } },
        { id: 'status', name: 'Status page', kind: 'live', items: 1 },
        { id: 'wiki', name: 'Wiki space', kind: 'confluence', items: 5, lastSync: { at: '2026-11-22T01:00:00Z', changed: 1 } },
      ],
      mayExclude: true,
      mayRemove: true,
    }
    stub({ sources: () => view })
    render(<MycroftSources />)
    const rows = await screen.findAllByTestId('sources-row')
    expect(rows.map((r) => r.dataset.id)).toEqual(['vendor', 'repo', 'bucket', 'added', 'console', 'status', 'wiki'])
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/knowledge/sources'])
    expect(itemCalls()).toEqual([])
    const cell = (i: number, id: string) => within(rows[i]!).getByTestId(id).textContent
    expect(rows.map((_, i) => cell(i, 'sources-row-kind'))).toEqual(['Web site', 'Git repository', 'S3 bucket', 'Uploaded documents', 'This console', 'Live', 'Confluence'])
    expect(cell(0, 'sources-row-items')).toBe('120 items2 failed · 3 excluded')
    expect(cell(1, 'sources-row-items')).toBe('40 items')
    expect(cell(3, 'sources-row-items')).toBe('1 item')
    expect(cell(0, 'sources-row-state')).toBe('On')
    expect(cell(2, 'sources-row-state')).toBe('Off')
    const at = fmtTs('2026-11-22T01:00:00Z')
    expect(cell(0, 'sources-row-synced')).toBe(`${at} · 2 changed`)
    expect(cell(1, 'sources-row-synced')).toBe(`${at} · failed`)
    expect(within(rows[1]!).getByTestId('sources-row-synced').getAttribute('title')).toBe('authentication required')
    expect(cell(2, 'sources-row-synced')).toBe('Never')
    expect(cell(3, 'sources-row-synced')).toBe(`${at} · with every source`)
    expect(cell(4, 'sources-row-synced')).toBe(at)
    expect(cell(5, 'sources-row-synced')).toBe('Looked up when asked')
    expect(within(rows[0]!).getByTestId('sources-row-open').getAttribute('href')).toBe('#/aiops/mycroft/sources/vendor')
    expect(screen.getByTestId('sources-table').style.minWidth).toBe(`${MYCROFT_TABLE_MIN}px`)
    expect(screen.getByTestId('sources-settings-link').getAttribute('href')).toBe('#/admin/ai/knowledge')
    expect(screen.queryByTestId('sources-item')).toBeNull()
    // The search narrows the rows by name or kind, in the browser.
    fireEvent.change(screen.getByTestId('sources-list-search'), { target: { value: 'git' } })
    expect(screen.getAllByTestId('sources-row').map((r) => r.dataset.id)).toEqual(['repo'])
    fireEvent.change(screen.getByTestId('sources-list-search'), { target: { value: 'nothing like it' } })
    expect(screen.getByTestId('sources-empty').textContent).toBe('No source matches these words.')
  })

  it('says nothing changed, and tells a Viewer who sees the sources', async () => {
    expect(lastSyncedText({ id: 'r', name: 'R', kind: 'git', items: 1, lastSync: { at: '2026-11-22T01:00:00Z', changed: 0 } })).toBe(`${fmtTs('2026-11-22T01:00:00Z')} · nothing changed`)
    stub({ sources: () => new Response(JSON.stringify({ error: 'only an Admin or a SuperAdmin' }), { status: 403 }) })
    render(<MycroftSources />)
    expect((await screen.findByTestId('sources-denied')).textContent).toBe('Admins and SuperAdmins see the sources and their items.')
  })

  it('builds the items’ address from what is set', () => {
    expect(itemsUrl({})).toBe('/api/v1/knowledge/items')
    expect(itemsUrl({ source: 'uploads', type: '', state: 'excluded', q: 'blog post', offset: 50, limit: 50 })).toBe('/api/v1/knowledge/items?source=uploads&state=excluded&q=blog+post&offset=50&limit=50')
  })

  it('lists the items in a Mycroft table; live sources are their addresses, never copied', async () => {
    stub()
    render(<MycroftSources open="vendor" />)
    const rows = await screen.findAllByTestId('sources-item')
    expect(rows.map((r) => r.dataset.id)).toEqual(['/blog/old.html', 'note Backups.md', 'https://status.example.com/'])
    expect(screen.getByTestId('sources-items-table').style.minWidth).toBe(`${MYCROFT_TABLE_MIN}px`)
    expect(rows[2]!.textContent).toContain('Looked up when asked')
    expect(rows[2]!.textContent).toContain('never copied')
    expect(within(rows[2]!).queryByTestId('sources-item-exclude')).toBeNull()
    expect(within(rows[1]!).queryByTestId('sources-item-exclude')).toBeNull()
    expect(within(rows[1]!).getByTestId('sources-item-remove')).toBeTruthy()
    expect(screen.getByTestId('sources-count').textContent).toBe('1–3 of 3')
    expect(screen.getByTestId('sources-settings-link').getAttribute('href')).toBe('#/admin/ai/knowledge')
  })

  it('lists the opened source’s items alone, filtered on the server by type, state and words, and paged', async () => {
    stub({ items: (u) => page({ total: 120, offset: Number(new URL(u, 'http://x').searchParams.get('offset') ?? 0) }) })
    render(<MycroftSources open="vendor" />)
    await screen.findAllByTestId('sources-item')
    expect(itemCalls()[0]).toBe('/api/v1/knowledge/items?source=vendor&limit=50')
    expect(screen.queryByTestId('sources-filter-source')).toBeNull()
    expect(screen.getByTestId('sources-open-name').textContent).toBe('Vendor docs')
    expect(screen.getByTestId('sources-back').getAttribute('href')).toBe('#/aiops/mycroft/sources')
    fireEvent.change(screen.getByTestId('sources-filter-type'), { target: { value: 'webPage' } })
    fireEvent.change(screen.getByTestId('sources-filter-state'), { target: { value: 'read' } })
    await waitFor(() => expect(itemCalls().at(-1)).toBe('/api/v1/knowledge/items?source=vendor&type=webPage&state=read&limit=50'))
    fireEvent.change(screen.getByTestId('sources-search'), { target: { value: 'old' } })
    await waitFor(() => expect(itemCalls().at(-1)).toBe('/api/v1/knowledge/items?source=vendor&type=webPage&state=read&q=old&limit=50'))
    fireEvent.click(screen.getByTestId('sources-next'))
    await waitFor(() => expect(itemCalls().at(-1)).toBe('/api/v1/knowledge/items?source=vendor&type=webPage&state=read&q=old&offset=50&limit=50'))
    expect(screen.getByTestId('sources-prev')).toHaveProperty('disabled', false)
  })

  it('opens an item: its first excerpts, the pages that drew on it, and the original', async () => {
    stub({ detail: () => ({ item: site, excerpts: ['First part.', 'Second part.'], excerptCount: 5, pages: [{ path: 'pages/runbook/a.md', title: 'Runbook A' }], provenance: true }) })
    render(<MycroftSources open="vendor" />)
    fireEvent.click(within((await screen.findAllByTestId('sources-item'))[0]!).getByTestId('sources-item-open'))
    const drawer = await screen.findByTestId('sources-item-drawer')
    expect((await within(drawer).findByTestId('sources-item-excerpts')).textContent).toContain('The first 2 of 5 excerpts.')
    expect(within(drawer).getByTestId('sources-item-pages').textContent).toContain('Runbook A')
    expect(within(drawer).getByTestId('sources-item-original').getAttribute('href')).toBe(site.url)
    expect(calls.some((c) => c.url === '/api/v1/knowledge/items/detail?source=vendor&id=%2Fblog%2Fold.html')).toBe(true)
  })

  it('asks Mycroft for a page from an item, whatever its source’s trust (KN-59)', async () => {
    stub({
      detail: () => ({ item: site, excerpts: ['First part.'], excerptCount: 1, pages: [], provenance: true }),
      items: (_u, init) => (init?.method === 'POST' ? { run: 'r-1', pages: 1, text: '1 page change waits for review in AIOps › Inbox (run r-1).' } : page()),
    })
    render(<MycroftSources open="vendor" />)
    fireEvent.click(within((await screen.findAllByTestId('sources-item'))[0]!).getByTestId('sources-item-open'))
    const drawer = await screen.findByTestId('sources-item-drawer')
    fireEvent.click(await within(drawer).findByTestId('sources-item-write-page'))
    expect((await within(drawer).findByTestId('sources-item-write-result')).textContent).toContain('run r-1')
    const sent = calls.find((c) => c.url === '/api/v1/knowledge/items/page')
    expect(sent?.init?.method).toBe('POST')
    expect(JSON.parse(String(sent?.init?.body))).toEqual({ source: 'vendor', id: '/blog/old.html' })
  })

  it('excludes everything under an item with a reason, says which pages drew on it, and includes it again from Excluded', async () => {
    stub({
      exclusions: (_u, init) =>
        init?.method === 'DELETE'
          ? { included: true, text: 'Included again: it is read at the next sync of Vendor docs, or its Run now.' }
          : { exclusion: { pattern: '/blog/*', by: 'Ann', at: '2026-11-17T10:00:00Z' }, items: 4, pages: [{ path: 'pages/runbook/a.md', title: 'Runbook A' }], text: '4 items excluded from Vendor docs: their excerpts are emptied.' },
      items: (u) => (u.includes('state=excluded') ? page({ items: [excluded], total: 1 }) : page()),
    })
    render(<MycroftSources open="vendor" />)
    fireEvent.click(within((await screen.findAllByTestId('sources-item'))[0]!).getByTestId('sources-item-exclude'))
    const dialog = screen.getByTestId('sources-exclude-dialog')
    expect(dialog.textContent).toContain('Exclude this: Old post (Vendor docs › /blog/old.html)')
    expect(dialog.textContent).toContain('Exclude everything under Vendor docs › /blog/')
    fireEvent.click(within(dialog).getByTestId('sources-exclude-under'))
    fireEvent.change(within(dialog).getByTestId('sources-exclude-reason'), { target: { value: 'stale posts' } })
    fireEvent.click(screen.getByTestId('confirm-ok'))
    const impact = await screen.findByTestId('source-impact')
    expect(impact.textContent).toContain('4 items excluded')
    expect(within(impact).getByTestId('source-impact-review').getAttribute('href')).toBe('#/aiops/mycroft/wiki?pages=pages%2Frunbook%2Fa.md')
    const post = calls.find((c) => c.url === '/api/v1/knowledge/sources/vendor/exclusions' && c.init?.method === 'POST')!
    expect(JSON.parse(String(post.init?.body))).toEqual({ pattern: '/blog/*', reason: 'stale posts' })
    fireEvent.change(screen.getByTestId('sources-filter-state'), { target: { value: 'excluded' } })
    const row = await screen.findByTestId('sources-item')
    expect(screen.getByTestId('sources-excluded-hint').textContent).toContain('next sync')
    expect(within(row).getByTestId('sources-item-exclusion').textContent).toContain('rule Vendor docs › /blog/*')
    fireEvent.click(within(row).getByTestId('sources-item-include'))
    expect((await screen.findByTestId('sources-notice')).textContent).toContain('next sync')
    expect(calls.some((c) => c.url === '/api/v1/knowledge/sources/vendor/exclusions?pattern=%2Fblog%2F*' && c.init?.method === 'DELETE')).toBe(true)
  })

  it('shows a bucket’s objects relative to its prefix, the source’s name leading, and offers nothing under the prefix itself', async () => {
    const deep = object('docs/backup-windows.csv', '221b/docs/', 'sandbox-knowledge › docs/')
    const top = object('runbook.md')
    stub({ items: () => page({ items: [deep, top], total: 2 }), exclusions: () => ({ exclusion: { pattern: '221b/docs/', by: 'Ann', at: '2026-11-18T10:00:00Z' }, items: 1, pages: [], text: '1 item excluded.' }) })
    render(<MycroftSources open="vendor" />)
    const rows = await screen.findAllByTestId('sources-item')
    expect(within(rows[0]!).getByTestId('sources-item-path').textContent).toBe('sandbox-knowledge › docs/backup-windows.csv')
    expect(within(rows[1]!).getByTestId('sources-item-path').textContent).toBe('sandbox-knowledge › runbook.md')
    expect(rows[0]!.getAttribute('data-id')).toBe('221b/docs/backup-windows.csv')
    // At the prefix: Exclude this only.
    fireEvent.click(within(rows[1]!).getByTestId('sources-item-exclude'))
    let dialog = screen.getByTestId('sources-exclude-dialog')
    expect(dialog.textContent).toContain('Exclude this: runbook.md (sandbox-knowledge › runbook.md)')
    expect(within(dialog).queryByTestId('sources-exclude-under')).toBeNull()
    expect(dialog.textContent).not.toContain('221b/')
    fireEvent.click(within(screen.getByTestId('confirm')).getByText('Cancel'))
    // Below it: everything under the folder, in words relative to the prefix, by its full key.
    fireEvent.click(within(rows[0]!).getByTestId('sources-item-exclude'))
    dialog = screen.getByTestId('sources-exclude-dialog')
    expect(dialog.textContent).toContain('Exclude everything under sandbox-knowledge › docs/')
    fireEvent.click(within(dialog).getByTestId('sources-exclude-under'))
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await screen.findByTestId('source-impact')
    const post = calls.find((c) => c.url === '/api/v1/knowledge/sources/sandbox-knowledge/exclusions' && c.init?.method === 'POST')!
    expect(JSON.parse(String(post.init?.body))).toEqual({ pattern: '221b/docs/' })
  })

  it('sets an uploaded document’s trust on its row, its source’s by default; an Admin sees it (KN-61)', async () => {
    const doc: SourceItem = { source: 'added', sourceName: 'Added in Mycroft', kind: 'upload', type: 'document', id: 'vendor.pdf', title: 'vendor.pdf', state: 'read', sourceTrust: 'high' }
    const low: SourceItem = { ...doc, id: 'old.pdf', title: 'old.pdf', trust: 'low' }
    const uploads = (over: Partial<ItemsPage> = {}) => page({ items: [doc, low, note], total: 3, sources: [{ id: 'added', name: 'Added in Mycroft', kind: 'upload' }], mayTrust: true, ...over })
    stub({
      items: () => uploads(),
      sources: (_u, init) => (init?.method === 'PUT' ? { source: 'added', id: 'vendor.pdf', trust: 'normal', sourceTrust: 'high', level: 'normal' } : {}),
    })
    render(<MycroftSources open="added" />)
    const rows = await screen.findAllByTestId('sources-item')
    const pick = within(rows[0]!).getByTestId('sources-item-trust') as HTMLSelectElement
    expect([...pick.options].map((o) => o.textContent)).toEqual(['Source’s (High)', 'High', 'Normal', 'Low'])
    expect(pick.value).toBe('')
    expect((within(rows[1]!).getByTestId('sources-item-trust') as HTMLSelectElement).value).toBe('low')
    expect(within(rows[2]!).queryByTestId('sources-item-trust')).toBeNull()
    fireEvent.change(pick, { target: { value: 'normal' } })
    expect((await screen.findByTestId('sources-notice')).textContent).toBe('The trust of “vendor.pdf” is normal.')
    const sent = calls.find((c) => c.url === '/api/v1/knowledge/sources/added/documents/vendor.pdf/trust')
    expect(sent?.init?.method).toBe('PUT')
    expect(JSON.parse(String(sent?.init?.body))).toEqual({ trust: 'normal' })
    cleanup()
    stub({ items: () => uploads({ mayTrust: false }) })
    render(<MycroftSources open="added" />)
    const shown = await screen.findAllByTestId('sources-item')
    expect(within(shown[0]!).queryByTestId('sources-item-trust')).toBeNull()
    expect(within(shown[0]!).getByTestId('sources-item-trust-text').textContent).toBe('High (its source’s)')
    expect(within(shown[1]!).getByTestId('sources-item-trust-text').textContent).toBe('Low')
    // Another source's items have no trust column.
    cleanup()
    stub()
    render(<MycroftSources open="vendor" />)
    await screen.findAllByTestId('sources-item')
    expect(screen.queryByText('Trust')).toBeNull()
  })

  it('offers no Exclude or Include again to an Admin, and tells a Viewer who sees the items', async () => {
    stub({ items: () => page({ items: [site, excluded], mayExclude: false }) })
    render(<MycroftSources open="vendor" />)
    await screen.findAllByTestId('sources-item')
    expect(screen.queryByTestId('sources-item-exclude')).toBeNull()
    expect(screen.queryByTestId('sources-item-include')).toBeNull()
    expect(screen.queryByTestId('sources-settings-link')).toBeNull()
    cleanup()
    stub({ items: () => new Response(JSON.stringify({ error: 'only an Admin or a SuperAdmin' }), { status: 403 }) })
    render(<MycroftSources open="vendor" />)
    expect((await screen.findByTestId('sources-denied')).textContent).toBe('Admins and SuperAdmins see the sources and their items.')
  })
})
