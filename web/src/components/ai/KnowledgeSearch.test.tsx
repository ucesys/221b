import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { IndexView, KnowledgeView } from '../../api/knowledge'
import { bytesText, modesText, SearchCard, SourceIndexCell } from './KnowledgeSearch'

function view(index: Partial<IndexView> = {}, over: Partial<KnowledgeView['settings']> = {}): KnowledgeView {
  return {
    settings: {
      sources: [
        { id: 'console', name: 'This console', kind: 'console' },
        { id: 'runbooks', name: 'Runbooks', kind: 'git', url: 'https://git.example.com/ops/runbooks', branch: 'main' },
        { id: 'rke2', name: 'RKE2 docs', kind: 'web', url: 'https://docs.rke2.io/' },
      ],
      store: { kind: 'database' },
      ...over,
    },
    index: {
      documents: 120,
      passages: 2400,
      fuzzy: true,
      semantic: true,
      embedded: 0,
      providers: [{ id: 'a5000', name: 'A5000 vLLM', endpoint: 'http://a5000:8000/v1' }],
      sources: [
        { id: 'runbooks', name: 'Runbooks', kind: 'git', trust: 'high', trustDefault: true, documents: 40, passages: 300, embedded: 0, bytes: 3 << 20 },
        { id: 'rke2', name: 'RKE2 docs', kind: 'web', trust: 'normal', trustDefault: true, documents: 80, passages: 2100, embedded: 0, bytes: 40 << 20 },
      ],
      databaseBytes: 2 * 2 ** 30,
      volumeBytes: 10 * 2 ** 30,
      clusterVersions: ['1.33'],
      ...index,
    },
    creds: {},
    storeCreds: { token: false, accessKey: false },
    hook: false,
    status: { running: false },
    pages: 0,
    excerpts: 0,
    waiting: 0,
    identity: false,
    model: true,
    console: true,
  }
}

describe('Administration › AI › Knowledge › Search', () => {
  afterEach(cleanup)
  it('says how the index searches, and chooses the embedding model (KN-54, KN-55)', async () => {
    const run = vi.fn(async (fn: () => Promise<unknown>) => {
      await fn()
    })
    const fetched: { url: string; body?: unknown }[] = []
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      fetched.push({ url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      return new Response(JSON.stringify(view()))
    })
    render(<SearchCard v={view()} run={run} busy={false} />)
    expect(screen.getByTestId('knowledge-index-line').textContent).toContain('120 pages and excerpts')
    expect(screen.getByTestId('knowledge-index-line').textContent).toContain('semantic search is off')
    fireEvent.change(screen.getByTestId('knowledge-embedding-provider'), { target: { value: 'a5000' } })
    const save = screen.getByTestId('knowledge-embedding-save') as HTMLButtonElement
    expect(save.disabled).toBe(true)
    // The name as the endpoint knows it: the A5000 serves qwen3-embedding-0.6b, not the Hugging Face name.
    const model = screen.getByTestId('knowledge-embedding-model') as HTMLInputElement
    expect(model.placeholder).toBe('The model’s name, as its /v1/models lists it')
    expect(screen.getByTestId('knowledge-embeddings').textContent).toContain('Type the model’s name as the endpoint knows it')
    fireEvent.change(model, { target: { value: 'qwen3-embedding-0.6b' } })
    expect(save.disabled).toBe(false)
    fireEvent.click(save)
    await vi.waitFor(() => expect(fetched).toHaveLength(1))
    expect(fetched[0]).toEqual({ url: '/api/v1/knowledge/embeddings', body: { provider: 'a5000', model: 'qwen3-embedding-0.6b' } })
  })
  it('says what is embedded, and what the database lacks', () => {
    const on = view({ embedded: 1200, embedError: 'embedding with m: the endpoint does not answer' }, { embeddings: { provider: 'a5000', model: 'm' } })
    expect(modesText(on)).toBe('It searches by keyword, fuzzy matching and meaning, ranked together.')
    render(<SearchCard v={on} run={vi.fn()} busy={false} />)
    expect(screen.getByTestId('knowledge-embedding-state').textContent).toContain('1,200 of 2,400 passages embedded with m')
    expect(screen.getByTestId('knowledge-embedding-error').textContent).toContain('does not answer')
    cleanup()
    const bare = view({ fuzzy: false, semantic: false, providers: [] }, { embeddings: { provider: 'a5000', model: 'm' } })
    expect(modesText(bare)).toBe('It searches by keyword and spellings matched; semantic search is off.')
    render(<SearchCard v={bare} run={vi.fn()} busy={false} />)
    expect(screen.getByTestId('knowledge-no-trgm')).toBeTruthy()
    expect(screen.getByTestId('knowledge-embedding-state').textContent).toContain('no pgvector')
    expect(screen.getByTestId('knowledge-embedding-none')).toBeTruthy()
  })
  it('says what the index holds of each source; its trust is set in the Sources table (KN-58)', () => {
    render(<SearchCard v={view()} run={vi.fn()} busy={false} />)
    expect(screen.getByTestId('knowledge-volume').textContent).toContain('2.0 GiB of its volume’s 10 GiB (20 %)')
    expect(screen.queryByTestId('knowledge-volume-full')).toBeNull()
    const rows = screen.getAllByTestId('knowledge-index-source')
    expect(rows).toHaveLength(2)
    expect(rows[1]!.querySelector('[data-testid="knowledge-index-size"]')!.textContent).toBe('80 documents · 2,100 passages · 40 MiB')
    expect(screen.queryByTestId('knowledge-index-trust')).toBeNull()
    expect(screen.queryByTestId('knowledge-index-product')).toBeNull()
    expect(screen.getByTestId('knowledge-index-sources').textContent).not.toContain('Trust')
  })
  it('sets a source’s trust and versions in one cell, saved together (KN-56, KN-57)', async () => {
    const fetched: { url: string; body?: unknown }[] = []
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      fetched.push({ url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      return new Response(JSON.stringify({ id: 'rke2' }))
    })
    const run = vi.fn(async (fn: () => Promise<unknown>) => {
      await fn()
    })
    const ix = view().index!
    render(<SourceIndexCell x={ix.sources[1]!} clusters={ix.clusterVersions} run={run} busy={false} />)
    const cell = screen.getByTestId('knowledge-index-cell')
    expect(cell.textContent).toContain('Normal (its kind’s)')
    expect(screen.queryByTestId('knowledge-index-save')).toBeNull()
    fireEvent.change(screen.getByTestId('knowledge-index-trust'), { target: { value: 'low' } })
    fireEvent.change(screen.getByTestId('knowledge-index-product'), { target: { value: 'rke2' } })
    fireEvent.click(screen.getByTestId('knowledge-index-follows'))
    expect(cell.textContent).toContain('Only the versions the clusters run (1.33)')
    fireEvent.click(screen.getByTestId('knowledge-index-save'))
    await vi.waitFor(() => expect(fetched).toHaveLength(1))
    expect(fetched[0]).toEqual({ url: '/api/v1/knowledge/sources/rke2/index', body: { trust: 'low', product: 'rke2', productVersion: '', followsClusterVersions: true } })
    cleanup()
    render(<SourceIndexCell x={{ ...ix.sources[0]!, product: 'ops' }} clusters={[]} run={run} busy={false} about={false} />)
    expect(screen.queryByTestId('knowledge-index-product')).toBeNull()
    fireEvent.change(screen.getByTestId('knowledge-index-trust'), { target: { value: 'normal' } })
    fireEvent.click(screen.getByTestId('knowledge-index-save'))
    await vi.waitFor(() => expect(fetched).toHaveLength(2))
    expect(fetched[1]!.body).toEqual({ trust: 'normal', product: 'ops', productVersion: '', followsClusterVersions: false })
  })
  it('warns at 80 % of the volume, and says when its size is not known', () => {
    render(<SearchCard v={view({ databaseBytes: 9 * 2 ** 30, full: true })} run={vi.fn()} busy={false} />)
    expect(screen.getByTestId('knowledge-volume-full').textContent).toContain('90 % of its volume')
    cleanup()
    render(<SearchCard v={view({ volumeBytes: undefined })} run={vi.fn()} busy={false} />)
    expect(screen.getByTestId('knowledge-volume').textContent).toContain('not known')
    expect(bytesText(1536)).toBe('1.5 KiB')
  })
  it('shows nothing without the index in the database', () => {
    const v = view()
    delete v.index
    render(<SearchCard v={v} run={vi.fn()} busy={false} />)
    expect(screen.queryByTestId('knowledge-search')).toBeNull()
  })
})
