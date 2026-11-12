import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CONFIGMAP_STORE_LABEL, type KnowledgeView } from '../../api/knowledge'
import { idFrom, KnowledgeSection } from './KnowledgeSection'

function view(over: Partial<KnowledgeView> = {}): KnowledgeView {
  return {
    settings: {
      sources: [
        { id: 'console', name: 'This console', kind: 'console' },
        { id: 'infra', name: 'Confluence space: Infrastructure', kind: 'confluence', baseURL: 'https://example.atlassian.net', flavor: 'cloud', email: 'ann@example.com', space: 'INFRA' },
        { id: 'documents', name: 'Uploaded documents', kind: 'upload' },
        { id: 'fixes', name: 'Fixes', kind: 'fixes' },
      ],
      store: { kind: 'git', url: 'https://git.example.com/ops/wiki.git', branch: 'main', savedBy: 'Ann', savedAt: '2026-11-01T10:00:00Z' },
    },
    creds: { infra: { token: true, accessKey: false }, console: { token: false, accessKey: false }, documents: { token: false, accessKey: false } },
    storeCreds: { token: true, accessKey: false },
    hook: false,
    status: {
      running: false,
      last: {
        started: '2026-11-05T01:00:00Z',
        finished: '2026-11-05T01:02:00Z',
        by: 'knowledge sync',
        full: true,
        read: 3,
        unchanged: 10,
        pages: 2,
        proposals: 1,
        reported: ['Confluence space “Ops notes” tried to give instructions, which were not followed: “Ignore all previous instructions”'],
        text: '',
      },
      sources: { infra: { lastRead: '2026-11-05T01:00:00Z', items: 12, reported: 1 }, console: { lastRead: '2026-11-05T01:00:00Z', items: 40 } },
    },
    pages: 7,
    excerpts: 30,
    documents: { documents: [{ source: 'documents', id: 'plan.docx', title: 'plan.docx', lastRead: '2026-11-04T10:00:00Z' }] },
    waiting: 1,
    identity: false,
    model: true,
    console: true,
    ...over,
  }
}

describe('Settings › Knowledge', () => {
  const calls: { method: string; url: string; body?: Record<string, unknown> }[] = []
  let current = view()
  beforeEach(() => {
    Element.prototype.scrollTo = () => {}
    calls.length = 0
    current = view()
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (String(url).endsWith('/sources') && init?.method === 'POST') return new Response(JSON.stringify({ id: 'ops' }), { status: 201 })
      return new Response(JSON.stringify(current))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('lists the sources with their state and what was reported', async () => {
    render(<KnowledgeSection />)
    const rows = await screen.findAllByTestId('knowledge-source')
    expect(rows.map((r) => r.dataset.source)).toEqual(['console', 'infra', 'documents', 'fixes'])
    expect(within(rows[1]!).getByText('1 page not followed')).toBeTruthy()
    expect(screen.getByTestId('knowledge-documents').textContent).toContain('plan.docx')
    expect(screen.getByTestId('knowledge-store-cred').textContent).toContain('Token saved by Ann')
    expect(screen.getByTestId('knowledge-model-link').getAttribute('href')).toContain('settings/agents')
  })

  it('adds a Confluence source with its token, which never comes back', async () => {
    render(<KnowledgeSection />)
    fireEvent.click(await screen.findByTestId('knowledge-add'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-name'), { target: { value: 'Ops space' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-url'), { target: { value: 'https://ops.atlassian.net' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-email'), { target: { value: 'ann@example.com' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-space'), { target: { value: 'OPS' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-token'), { target: { value: 'read-only-token' } })
    fireEvent.click(within(drawer).getByTestId('knowledge-source-save'))
    await waitFor(() =>
      expect(calls.find((c) => c.method === 'POST' && c.url === '/api/v1/knowledge/sources')?.body).toEqual({
        source: { id: 'ops-space', name: 'Ops space', kind: 'confluence', flavor: 'cloud', baseURL: 'https://ops.atlassian.net', email: 'ann@example.com', space: 'OPS' },
        token: 'read-only-token',
      }),
    )
  })

  // --- A Wiki sync job per source (KN-28) ---
  it('starts no sync: each source links its Wiki sync job, and one line says where the syncs are', async () => {
    current = view({
      status: {
        ...view().status,
        sources: { ...view().status.sources, infra: { items: 0, error: 'the Confluence site answered 401 Unauthorized: the read token was refused; check that it belongs to ann@example.com and may read the space INFRA' } },
      },
    })
    render(<KnowledgeSection />)
    const rows = await screen.findAllByTestId('knowledge-source')
    expect(screen.queryByTestId('knowledge-sync')).toBeNull()
    expect(screen.queryByTestId('knowledge-status')).toBeNull()
    expect(screen.queryByTestId('knowledge-last')).toBeNull()
    expect(screen.queryByText('Sync')).toBeNull()
    expect(screen.queryByText(/Sync now|Synced|Never synced/)).toBeNull()
    const line = screen.getByTestId('knowledge-jobs-line')
    expect(line.textContent).toBe('Each source is read by its Wiki sync job in AIOps › Jobs.')
    expect(screen.getByTestId('knowledge-jobs-link').getAttribute('href')).toBe('#/aiops/jobs')
    expect(within(rows[1]!).getByTestId('knowledge-source-job').getAttribute('href')).toBe('#/aiops/jobs/wiki-sync-infra')
    expect(within(rows[0]!).getByTestId('knowledge-source-job').getAttribute('href')).toBe('#/aiops/jobs/wiki-sync-console')
    // A source's error is said whole, not cut at 60 characters.
    expect(within(rows[1]!).getByTestId('knowledge-source-state').textContent).toBe('the Confluence site answered 401 Unauthorized: the read token was refused; check that it belongs to ann@example.com and may read the space INFRA')
    await new Promise((r) => setTimeout(r, 0))
    expect(calls.some((c) => c.url.endsWith('/sync'))).toBe(false)
  })

  it('saves where the wiki is kept with a new key', async () => {
    render(<KnowledgeSection />)
    fireEvent.click(await screen.findByTestId('knowledge-store-s3'))
    fireEvent.change(screen.getByTestId('knowledge-store-bucket'), { target: { value: 'wiki' } })
    fireEvent.change(screen.getByTestId('knowledge-store-akid'), { target: { value: 'AKIAEXAMPLE' } })
    fireEvent.change(screen.getByTestId('knowledge-store-secret'), { target: { value: 's3cret' } })
    expect(screen.getByTestId('knowledge-store-move')).toBeTruthy()
    fireEvent.click(screen.getByTestId('knowledge-store-save'))
    await waitFor(() => {
      const put = calls.find((c) => c.method === 'PUT' && c.url === '/api/v1/knowledge/store')
      expect(put?.body).toMatchObject({ store: { kind: 's3', bucket: 'wiki', prefix: 'wiki/', credentials: 'keys' }, keys: { accessKeyId: 'AKIAEXAMPLE', secretAccessKey: 's3cret' } })
    })
  })

  it('asks for a store before the first read', async () => {
    current = view({ settings: { ...view().settings, store: { kind: '' } }, status: { running: false } })
    render(<KnowledgeSection />)
    expect((await screen.findByTestId('knowledge-no-store')).textContent).toContain('before Mycroft reads the sources')
  })

  // --- The temporary ConfigMap store (CF-57) ---
  it('shows the temporary ConfigMap store as selected while none is saved, labelled so, and moves the wiki out of it', async () => {
    current = view({ settings: { ...view().settings, store: { kind: 'configmap' } }, storeCreds: { token: false, accessKey: false }, configMapStore: true })
    render(<KnowledgeSection />)
    expect((await screen.findByTestId('knowledge-configmap-store')).textContent).toContain(CONFIGMAP_STORE_LABEL)
    expect(CONFIGMAP_STORE_LABEL).toBe('ConfigMap (temporary, for testing — will be removed; move to Git or S3 before relying on it)')
    expect(screen.queryByTestId('knowledge-no-store')).toBeNull()
    expect(screen.getByTestId('knowledge-store-configmap').getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByTestId('knowledge-store-configmap-label').textContent).toBe(CONFIGMAP_STORE_LABEL)
    expect(screen.queryByTestId('knowledge-store-move')).toBeNull()
    fireEvent.click(screen.getByTestId('knowledge-store-git'))
    fireEvent.change(screen.getByTestId('knowledge-store-url'), { target: { value: 'https://git.example.com/ops/wiki.git' } })
    fireEvent.change(screen.getByTestId('knowledge-store-token'), { target: { value: 'git-token' } })
    fireEvent.click(screen.getByTestId('knowledge-store-move'))
    await waitFor(() => {
      const move = calls.find((c) => c.method === 'POST' && c.url === '/api/v1/knowledge/store/move')
      expect(move?.body).toMatchObject({ store: { kind: 'git', url: 'https://git.example.com/ops/wiki.git' }, keys: { token: 'git-token' } })
    })
  })

  it('offers no ConfigMap store where the chart made none', async () => {
    render(<KnowledgeSection />)
    expect(await screen.findByTestId('knowledge-store-git')).toBeTruthy()
    expect(screen.queryByTestId('knowledge-store-configmap')).toBeNull()
    expect(screen.queryByTestId('knowledge-configmap-store')).toBeNull()
  })

  // --- A bucket of documents (KN-25) ---
  it('adds an S3 bucket of documents with a read-only access key, which never comes back', async () => {
    render(<KnowledgeSection />)
    fireEvent.click(await screen.findByTestId('knowledge-add'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-kind'), { target: { value: 's3' } })
    expect(within(drawer).queryByTestId('knowledge-source-token')).toBeNull()
    fireEvent.change(within(drawer).getByTestId('knowledge-source-name'), { target: { value: 'Runbooks bucket' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-bucket'), { target: { value: 'ops-docs' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-prefix'), { target: { value: 'runbooks/' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-region'), { target: { value: 'eu-west-1' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-endpoint'), { target: { value: 'https://minio.example.internal' } })
    fireEvent.click(within(drawer).getByTestId('knowledge-source-pathstyle'))
    expect((within(drawer).getByTestId('knowledge-source-secret') as HTMLInputElement).type).toBe('password')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-akid'), { target: { value: 'AKIAREADONLY' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-secret'), { target: { value: 'bucket-secret' } })
    fireEvent.click(within(drawer).getByTestId('knowledge-source-save'))
    await waitFor(() =>
      expect(calls.find((c) => c.method === 'POST' && c.url === '/api/v1/knowledge/sources')?.body).toEqual({
        source: { id: 'runbooks-bucket', name: 'Runbooks bucket', kind: 's3', bucket: 'ops-docs', prefix: 'runbooks/', region: 'eu-west-1', endpoint: 'https://minio.example.internal', pathStyle: true, credentials: 'keys' },
        keys: { accessKeyId: 'AKIAREADONLY', secretAccessKey: 'bucket-secret' },
      }),
    )
  })

  it('reads a bucket by the pod’s identity, with no key to type', async () => {
    current = view({ identity: true })
    render(<KnowledgeSection />)
    fireEvent.click(await screen.findByTestId('knowledge-add'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-kind'), { target: { value: 's3' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-name'), { target: { value: 'Docs' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-bucket'), { target: { value: 'docs' } })
    const creds = within(drawer).getByTestId('knowledge-source-credentials') as HTMLSelectElement
    expect([...creds.options].map((o) => o.text)).toEqual(['A read-only access key in a Secret', 'The pod’s identity'])
    fireEvent.change(creds, { target: { value: 'identity' } })
    expect(within(drawer).queryByTestId('knowledge-source-akid')).toBeNull()
    expect(within(drawer).queryByTestId('knowledge-source-secret')).toBeNull()
    fireEvent.click(within(drawer).getByTestId('knowledge-source-save'))
    await waitFor(() => {
      const body = calls.find((c) => c.method === 'POST' && c.url === '/api/v1/knowledge/sources')?.body
      expect(body).toMatchObject({ source: { kind: 's3', bucket: 'docs', credentials: 'identity' }, keys: {} })
    })
  })

  it('edits the name of a source there from the start, and nothing else: no connection, no Test, no Remove', async () => {
    render(<KnowledgeSection />)
    const row = (await screen.findAllByTestId('knowledge-source')).find((r) => r.dataset.source === 'console')!
    fireEvent.click(within(row).getByTestId('knowledge-source-edit'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    expect(within(drawer).getByTestId('knowledge-source-builtin').textContent).toContain('only its name changes here')
    expect((within(drawer).getByTestId('knowledge-source-name') as HTMLInputElement).value).toBe('This console')
    for (const id of ['knowledge-source-token', 'knowledge-source-test', 'knowledge-source-remove', 'knowledge-source-kind']) expect(within(drawer).queryByTestId(id)).toBeNull()
  })

  it('lists the source Fixes, there from the start: what it offers, and only its name to change (KN-34)', async () => {
    render(<KnowledgeSection />)
    const row = (await screen.findAllByTestId('knowledge-source')).find((r) => r.dataset.source === 'fixes')!
    expect(row.textContent).toContain('Fixes seen in 3 cases or more, offered by the housekeeping for a runbook')
    fireEvent.click(within(row).getByTestId('knowledge-source-edit'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    expect(within(drawer).getByTestId('knowledge-source-builtin').textContent).toContain('only its name changes here')
    for (const id of ['knowledge-source-token', 'knowledge-source-test', 'knowledge-source-remove', 'knowledge-source-kind']) expect(within(drawer).queryByTestId(id)).toBeNull()
  })

  it('lists a bucket with its key’s state and what it skipped; the row has no Test; the saved key is not sent again', async () => {
    const bucket = { id: 'docs', name: 'Ops bucket', kind: 's3' as const, bucket: 'ops-docs', prefix: 'ops/', region: 'eu-west-1', credentials: 'keys' as const }
    const base = view()
    current = view({
      settings: { ...base.settings, sources: [...base.settings.sources, bucket] },
      creds: { ...base.creds, docs: { token: false, accessKey: true } },
      status: { ...base.status, sources: { ...base.status.sources, docs: { lastRead: '2026-11-05T01:00:00Z', items: 3, skipped: 2, skippedWhy: '1 not a kind Mycroft reads (.png); 1 over 20 MiB' } } },
    })
    render(<KnowledgeSection />)
    const row = (await screen.findAllByTestId('knowledge-source')).find((r) => r.dataset.source === 'docs')!
    expect(within(row).getByTestId('knowledge-source-bucket-line').textContent).toBe('s3://ops-docs/ops/')
    expect(within(row).getByTestId('knowledge-source-creds').textContent).toBe('access key in a Secret')
    const state = within(row).getByTestId('knowledge-source-state')
    expect(state.textContent).toBe('Read · 3 items · 2 skipped')
    expect(state.getAttribute('title')).toBe('2 skipped: 1 not a kind Mycroft reads (.png); 1 over 20 MiB')
    // Test is in the dialog now (KN-26).
    expect(within(row).queryByTestId('knowledge-source-test')).toBeNull()

    // Edited: the key is saved, so its fields start empty and nothing is sent unless typed.
    fireEvent.click(within(row).getByText('Edit…'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    expect((within(drawer).getByTestId('knowledge-source-secret') as HTMLInputElement).value).toBe('')
    expect(drawer.textContent).toContain('Secret access key (saved; type to replace)')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-prefix'), { target: { value: 'ops/runbooks/' } })
    fireEvent.click(within(drawer).getByTestId('knowledge-source-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT' && c.url === '/api/v1/knowledge/sources/docs')?.body).toEqual({ source: { ...bucket, prefix: 'ops/runbooks/' }, keys: {} }))
  })

  // --- Test in the source dialog (KN-26) ---
  it('tests a new source as typed in its dialog: Testing…, then what it reached, kept until a field changes', async () => {
    let answer: (r: Response) => void = () => {}
    render(<KnowledgeSection />)
    vi.stubGlobal('fetch', (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (String(url).endsWith('/sources/test')) return new Promise<Response>((resolve) => (answer = resolve))
      return Promise.resolve(new Response(JSON.stringify(current)))
    })
    fireEvent.click(await screen.findByTestId('knowledge-add'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-kind'), { target: { value: 's3' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-name'), { target: { value: 'Docs' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-bucket'), { target: { value: 'sim-docs' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-endpoint'), { target: { value: 'https://minio.example.com:9000' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-akid'), { target: { value: 'AKIATYPED' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-secret'), { target: { value: 'typed-secret' } })
    fireEvent.click(within(drawer).getByTestId('knowledge-source-test'))
    const result = () => within(drawer).getByTestId('knowledge-source-test-result')
    expect(result().textContent).toBe('Testing…')
    expect(within(drawer).getByTestId('knowledge-source-test')).toHaveProperty('disabled', true)
    expect(calls.find((c) => c.url === '/api/v1/knowledge/sources/test')?.body).toEqual({
      source: { id: 'docs', name: 'Docs', kind: 's3', bucket: 'sim-docs', prefix: '', region: '', endpoint: 'https://minio.example.com:9000', pathStyle: false, credentials: 'keys' },
      keys: { accessKeyId: 'AKIATYPED', secretAccessKey: 'typed-secret' },
    })
    answer(new Response(JSON.stringify({ ok: true, items: 2, sample: ['docs/a.md', 'docs/b.csv'], skipped: 1, skippedWhy: '1 not a kind Mycroft reads (.png)' })))
    await waitFor(() => expect(result().textContent).toBe('Reached: 2 items, e.g. docs/a.md, docs/b.csv. 1 skipped: 1 not a kind Mycroft reads (.png).'))
    expect(result().dataset.state).toBe('ok')
    // Nothing was saved, and the result stays while nothing changes.
    expect(calls.some((c) => c.url === '/api/v1/knowledge/sources' && c.method === 'POST')).toBe(false)
    fireEvent.click(within(drawer).getByTestId('knowledge-source-pathstyle'))
    expect(within(drawer).queryByTestId('knowledge-source-test-result')).toBeNull()

    // Again, failing: the error in red, under the button, until a field changes.
    fireEvent.click(within(drawer).getByTestId('knowledge-source-test'))
    answer(new Response(JSON.stringify({ ok: false, error: 'Could not connect to minio.example.com:9001 within 10 s. Check the address and port (for MinIO, the S3 API is not the 9001 web console).' })))
    await waitFor(() => expect(result().dataset.state).toBe('error'))
    expect(result().getAttribute('data-tone')).toBe('err')
    expect(result().textContent).toContain('within 10 s')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-secret'), { target: { value: 'typed-secret-2' } })
    expect(within(drawer).queryByTestId('knowledge-source-test-result')).toBeNull()

    // An answer to values changed since it was asked is dropped.
    fireEvent.click(within(drawer).getByTestId('knowledge-source-test'))
    fireEvent.change(within(drawer).getByTestId('knowledge-source-region'), { target: { value: 'eu-west-1' } })
    answer(new Response(JSON.stringify({ ok: true, items: 9 })))
    await new Promise((r) => setTimeout(r, 0))
    expect(within(drawer).queryByTestId('knowledge-source-test-result')).toBeNull()
  })

  it('tests a saved source with its saved credential: a blank token is not sent, the source’s id is', async () => {
    render(<KnowledgeSection />)
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (String(url).endsWith('/sources/test')) return new Response(JSON.stringify({ ok: true, items: 1, sample: ['Runbook: restart'] }))
      return new Response(JSON.stringify(current))
    })
    const row = (await screen.findAllByTestId('knowledge-source')).find((r) => r.dataset.source === 'infra')!
    fireEvent.click(within(row).getByText('Edit…'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    expect(drawer.textContent).toContain('a credential left blank is the saved one')
    fireEvent.click(within(drawer).getByTestId('knowledge-source-test'))
    await waitFor(() => expect(within(drawer).getByTestId('knowledge-source-test-result').textContent).toBe('Reached: 1 item, e.g. Runbook: restart.'))
    const body = calls.find((c) => c.url === '/api/v1/knowledge/sources/test')?.body
    expect(body).toEqual({ source: view().settings.sources[1], saved: 'infra' })
  })

  // --- Add only in Settings (MY-11) ---
  it('has the Added in Mycroft card with the form to add a note, a web address or documents, and scrolls to it when asked', async () => {
    const base = view()
    current = view({ settings: { ...base.settings, sources: [...base.settings.sources, { id: 'added', name: 'Added in Mycroft', kind: 'upload' }] } })
    const wiki = {
      store: 'git',
      model: true,
      pages: 0,
      excerpts: 0,
      running: false,
      mayAdd: true,
      mayReview: true,
      maySetup: true,
      sources: [],
      added: [],
      limits: { note: 204800, document: 20 << 20, depth: 3, defaultDepth: 1, pages: 50, defaultPages: 20, formats: ['.md'] },
    }
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (String(url).endsWith('/knowledge/wiki')) return new Response(JSON.stringify(wiki))
      return new Response(JSON.stringify(current))
    })
    const scrolled = vi.fn()
    Element.prototype.scrollIntoView = scrolled
    render(<KnowledgeSection focus="added" />)
    const card = await screen.findByTestId('knowledge-added')
    expect(await within(card).findByTestId('wiki-add-kind-text')).toBeTruthy()
    expect(within(card).getByTestId('wiki-add-kind-url')).toBeTruthy()
    expect(within(card).getByTestId('wiki-add-kind-document')).toBeTruthy()
    // The uploaded documents keep their card; Added in Mycroft is not one of them.
    expect(screen.getAllByTestId('knowledge-documents')).toHaveLength(1)
    await waitFor(() => expect(scrolled).toHaveBeenCalled())
  })

  it('makes an id from a name', () => {
    expect(idFrom('Confluence space: Infrastructure')).toBe('confluence-space-infrastructure')
    expect(idFrom('!!!')).toBe('source')
  })
})
