import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { IndexView, KnowledgeView } from '../../api/knowledge'
import { modesText, SearchCard } from './KnowledgeSearch'

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
    index: { documents: 120, passages: 2400, fuzzy: true, semantic: true, embedded: 0, providers: [{ id: 'a5000', name: 'A5000 vLLM', endpoint: 'http://a5000:8000/v1' }], ...index },
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
    fireEvent.change(screen.getByTestId('knowledge-embedding-model'), { target: { value: 'Qwen/Qwen3-Embedding-0.6B' } })
    expect(save.disabled).toBe(false)
    fireEvent.click(save)
    await vi.waitFor(() => expect(fetched).toHaveLength(1))
    expect(fetched[0]).toEqual({ url: '/api/v1/knowledge/embeddings', body: { provider: 'a5000', model: 'Qwen/Qwen3-Embedding-0.6B' } })
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
  it('shows nothing without the index in the database', () => {
    const v = view()
    delete v.index
    render(<SearchCard v={v} run={vi.fn()} busy={false} />)
    expect(screen.queryByTestId('knowledge-search')).toBeNull()
  })
})
