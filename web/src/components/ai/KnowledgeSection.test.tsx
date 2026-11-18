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

const uploads = {
  items: [{ source: 'documents', id: 'plan.docx', type: 'document', title: 'plan.docx', by: 'Ann', at: '2026-11-04T10:00:00Z', state: 'read', lastRead: '2026-11-04T10:00:00Z' }],
  limits: { note: 204800, document: 20 << 20, depth: 3, defaultDepth: 1, pages: 50, defaultPages: 20, formats: ['.md'] },
  mayAdd: true,
  maySetup: true,
  store: 'git',
  model: true,
  off: [],
}

/** A credential the pickers offer (0.12.1, CR-10). */
const cred = (id: string, name: string, kind: string, endpoint: string) => ({ id, name, kind, endpoint })

describe('Administration › AI › Knowledge', () => {
  const calls: { method: string; url: string; body?: Record<string, unknown> }[] = []
  let current = view()
  // The credentials each use offers, by use; and the list's names.
  let choices: Record<string, ReturnType<typeof cred>[]> = {}
  /** The answers of Administration › Credentials, or null for another address. */
  const credentialsAnswer = (url: string): Response | null => {
    if (url.startsWith('/api/v1/credentials/choices')) {
      const use = new URL(url, 'http://x').searchParams.get('use') ?? ''
      return new Response(JSON.stringify({ choices: choices[use] ?? [] }))
    }
    if (url === '/api/v1/credentials')
      return new Response(
        JSON.stringify({
          credentials: Object.values(choices)
            .flat()
            .map((c) => ({ ...c, usedBy: [] })),
          mayEdit: true,
        }),
      )
    return null
  }
  beforeEach(() => {
    Element.prototype.scrollTo = () => {}
    calls.length = 0
    current = view()
    choices = {}
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      const c = credentialsAnswer(String(url))
      if (c) return c
      if (String(url).endsWith('/sources') && init?.method === 'POST') return new Response(JSON.stringify({ id: 'ops' }), { status: 201 })
      if (String(url).endsWith('/knowledge/uploads')) return new Response(JSON.stringify(uploads))
      if (String(url) === '/api/v1/helm-repositories')
        return new Response(
          JSON.stringify({
            repositories: [
              { name: 'runbooks', kind: 'git', url: 'https://git.example.com/ops/runbooks', defaultBranch: 'trunk', usedBy: [] },
              { name: 'bitnami', kind: 'helm', url: 'https://charts.example.com', usedBy: [] },
            ],
            mayEdit: true,
          }),
        )
      return new Response(JSON.stringify(current))
    })
  })
  /** The picker's select once its choices came. */
  const picker = async (drawer: HTMLElement) => {
    const sel = within(drawer).getByTestId('knowledge-source-credential-select') as HTMLSelectElement
    await waitFor(() => expect(sel.disabled).toBe(false))
    return sel
  }
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('lists the sources with their state and what was reported', async () => {
    render(<KnowledgeSection />)
    const rows = await screen.findAllByTestId('knowledge-source')
    expect(rows.map((r) => r.dataset.source)).toEqual(['console', 'infra', 'fixes'])
    expect(within(rows[1]!).getByText('1 page not followed')).toBeTruthy()
    await waitFor(() => expect(screen.getByTestId('uploads-section').textContent).toContain('Documents'))
    expect(screen.getByTestId('knowledge-store-cred').textContent).toContain('Token saved by Ann')
    expect(screen.getByTestId('knowledge-model-link').getAttribute('href')).toContain('admin/ai/agents')
  })

  it('adds a Confluence source naming its credential, which the only one that covers it preselects (CR-10)', async () => {
    choices = { 'confluence-cloud': [cred('cr-ops', 'Ops Confluence', 'token', 'https://ops.atlassian.net')] }
    render(<KnowledgeSection />)
    fireEvent.click(await screen.findByTestId('knowledge-add'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-name'), { target: { value: 'Ops space' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-url'), { target: { value: 'https://ops.atlassian.net' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-space'), { target: { value: 'OPS' } })
    // No token or email of its own: the credential holds them.
    expect(within(drawer).queryByTestId('knowledge-source-token')).toBeNull()
    expect(within(drawer).queryByTestId('knowledge-source-email')).toBeNull()
    await waitFor(() => expect((within(drawer).getByTestId('knowledge-source-credential-select') as HTMLSelectElement).value).toBe('cr-ops'))
    expect(calls.find((c) => c.url.startsWith('/api/v1/credentials/choices'))?.url).toContain('use=confluence-cloud')
    fireEvent.click(within(drawer).getByTestId('knowledge-source-save'))
    await waitFor(() =>
      expect(calls.find((c) => c.method === 'POST' && c.url === '/api/v1/knowledge/sources')?.body).toEqual({
        source: { id: 'ops-space', name: 'Ops space', kind: 'confluence', flavor: 'cloud', baseURL: 'https://ops.atlassian.net', email: '', space: 'OPS', credential: 'cr-ops' },
      }),
    )
  })

  // Administration › Settings › Repositories (0.12.1, RP-03): a git source picks a git
  // repository of the list, and keeps its branch and directory; the
  // credential is the repository's, so no picker asks for one.
  it('adds a git source picking a repository of Administration › Settings › Repositories', async () => {
    render(<KnowledgeSection />)
    fireEvent.click(await screen.findByTestId('knowledge-add'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-kind'), { target: { value: 'git' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-name'), { target: { value: 'Runbooks' } })
    expect(within(drawer).queryByTestId('knowledge-source-url')).toBeNull()
    expect(within(drawer).queryByTestId('knowledge-source-credential')).toBeNull()
    expect(within(drawer).getByTestId('knowledge-source-save')).toHaveProperty('disabled', true)
    const sel = within(drawer).getByTestId('knowledge-source-repository-select') as HTMLSelectElement
    await waitFor(() => expect(sel.disabled).toBe(false))
    // Git repositories only, and Add for a SuperAdmin.
    expect([...sel.options].map((o) => o.value)).toEqual(['', 'runbooks', '__add'])
    fireEvent.change(sel, { target: { value: 'runbooks' } })
    fireEvent.click(within(drawer).getByTestId('knowledge-source-save'))
    await waitFor(() =>
      expect(calls.find((c) => c.method === 'POST' && c.url === '/api/v1/knowledge/sources')?.body).toMatchObject({
        source: { id: 'runbooks', name: 'Runbooks', kind: 'git', branch: 'trunk', url: 'https://git.example.com/ops/runbooks', repository: 'runbooks', credential: '' },
      }),
    )
  })

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
  it('adds an S3 bucket of documents with an access key credential for its endpoint and bucket', async () => {
    choices = { s3: [cred('cr-docs', 'Docs key', 'accesskey', 'https://minio.example.internal/ops-docs')] }
    render(<KnowledgeSection />)
    fireEvent.click(await screen.findByTestId('knowledge-add'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-kind'), { target: { value: 's3' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-name'), { target: { value: 'Runbooks bucket' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-bucket'), { target: { value: 'ops-docs' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-prefix'), { target: { value: 'runbooks/' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-region'), { target: { value: 'eu-west-1' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-endpoint'), { target: { value: 'https://minio.example.internal' } })
    fireEvent.click(within(drawer).getByTestId('knowledge-source-pathstyle'))
    expect(within(drawer).queryByTestId('knowledge-source-secret')).toBeNull()
    await waitFor(() => expect((within(drawer).getByTestId('knowledge-source-credential-select') as HTMLSelectElement).value).toBe('cr-docs'))
    expect(decodeURIComponent(calls.filter((c) => c.url.startsWith('/api/v1/credentials/choices')).at(-1)!.url)).toContain('address=https://minio.example.internal/ops-docs')
    fireEvent.click(within(drawer).getByTestId('knowledge-source-save'))
    await waitFor(() =>
      expect(calls.find((c) => c.method === 'POST' && c.url === '/api/v1/knowledge/sources')?.body).toEqual({
        source: {
          id: 'runbooks-bucket',
          name: 'Runbooks bucket',
          kind: 's3',
          bucket: 'ops-docs',
          prefix: 'runbooks/',
          region: 'eu-west-1',
          endpoint: 'https://minio.example.internal',
          pathStyle: true,
          credentials: 'keys',
          credential: 'cr-docs',
        },
      }),
    )
  })

  it('reads a bucket by the pod’s identity, with no credential to choose', async () => {
    current = view({ identity: true })
    render(<KnowledgeSection />)
    fireEvent.click(await screen.findByTestId('knowledge-add'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-kind'), { target: { value: 's3' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-name'), { target: { value: 'Docs' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-bucket'), { target: { value: 'docs' } })
    const sel = await picker(drawer)
    expect([...sel.options].map((o) => o.text)).toEqual(['None (public)', 'The pod’s identity'])
    fireEvent.change(sel, { target: { value: '__identity' } })
    fireEvent.click(within(drawer).getByTestId('knowledge-source-save'))
    await waitFor(() => {
      const body = calls.find((c) => c.method === 'POST' && c.url === '/api/v1/knowledge/sources')?.body
      expect(body).toMatchObject({ source: { kind: 's3', bucket: 'docs', credentials: 'identity', credential: '' } })
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

  it('lists a bucket with its credential and what it skipped; the row has no Test; Edit keeps the credential', async () => {
    choices = { s3: [cred('cr-docs', 'Docs key', 'accesskey', 'https://s3.amazonaws.com/ops-docs')] }
    const bucket = { id: 'docs', name: 'Ops bucket', kind: 's3' as const, bucket: 'ops-docs', prefix: 'ops/', region: 'eu-west-1', credentials: 'keys' as const, credential: 'cr-docs', credMigrated: true }
    const base = view()
    current = view({
      settings: { ...base.settings, sources: [...base.settings.sources, bucket] },
      status: { ...base.status, sources: { ...base.status.sources, docs: { lastRead: '2026-11-05T01:00:00Z', items: 3, skipped: 2, skippedWhy: '1 not a kind Mycroft reads (.png); 1 over 20 MiB' } } },
    })
    render(<KnowledgeSection />)
    const row = (await screen.findAllByTestId('knowledge-source')).find((r) => r.dataset.source === 'docs')!
    expect(within(row).getByTestId('knowledge-source-bucket-line').textContent).toBe('s3://ops-docs/ops/')
    await waitFor(() => expect(within(row).getByTestId('knowledge-source-creds').textContent).toBe('credential Docs key'))
    const state = within(row).getByTestId('knowledge-source-state')
    expect(state.textContent).toBe('Read · 3 items · 2 skipped')
    expect(state.getAttribute('title')).toBe('2 skipped: 1 not a kind Mycroft reads (.png); 1 over 20 MiB')
    // Test is in the dialog now (KN-26).
    expect(within(row).queryByTestId('knowledge-source-test')).toBeNull()

    fireEvent.click(within(row).getByText('Edit…'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    expect((await picker(drawer)).value).toBe('cr-docs')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-prefix'), { target: { value: 'ops/runbooks/' } })
    fireEvent.click(within(drawer).getByTestId('knowledge-source-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT' && c.url === '/api/v1/knowledge/sources/docs')?.body).toEqual({ source: { ...bucket, prefix: 'ops/runbooks/' } }))
  })

  it('tests a new source as typed in its dialog: Testing…, then what it reached, kept until a field changes', async () => {
    let answer: (r: Response) => void = () => {}
    render(<KnowledgeSection />)
    choices = { s3: [cred('cr-minio', 'MinIO key', 'accesskey', 'https://minio.example.com:9000')] }
    vi.stubGlobal('fetch', (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      const c = credentialsAnswer(String(url))
      if (c) return Promise.resolve(c)
      if (String(url).endsWith('/sources/test')) return new Promise<Response>((resolve) => (answer = resolve))
      return Promise.resolve(new Response(JSON.stringify(current)))
    })
    fireEvent.click(await screen.findByTestId('knowledge-add'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-kind'), { target: { value: 's3' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-name'), { target: { value: 'Docs' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-bucket'), { target: { value: 'sim-docs' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-endpoint'), { target: { value: 'https://minio.example.com:9000' } })
    await waitFor(() => expect((within(drawer).getByTestId('knowledge-source-credential-select') as HTMLSelectElement).value).toBe('cr-minio'))
    fireEvent.click(within(drawer).getByTestId('knowledge-source-test'))
    const result = () => within(drawer).getByTestId('knowledge-source-test-result')
    expect(result().textContent).toBe('Testing…')
    expect(within(drawer).getByTestId('knowledge-source-test')).toHaveProperty('disabled', true)
    expect(calls.find((c) => c.url === '/api/v1/knowledge/sources/test')?.body).toEqual({
      source: { id: 'docs', name: 'Docs', kind: 's3', bucket: 'sim-docs', prefix: '', region: '', endpoint: 'https://minio.example.com:9000', pathStyle: false, credentials: 'keys', credential: 'cr-minio' },
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
    fireEvent.change(within(drawer).getByTestId('knowledge-source-prefix'), { target: { value: 'docs/' } })
    expect(within(drawer).queryByTestId('knowledge-source-test-result')).toBeNull()

    // An answer to values changed since it was asked is dropped.
    fireEvent.click(within(drawer).getByTestId('knowledge-source-test'))
    fireEvent.change(within(drawer).getByTestId('knowledge-source-region'), { target: { value: 'eu-west-1' } })
    answer(new Response(JSON.stringify({ ok: true, items: 9 })))
    await new Promise((r) => setTimeout(r, 0))
    expect(within(drawer).queryByTestId('knowledge-source-test-result')).toBeNull()
  })

  it('tests a saved source with what it names: one of before 0.12.1 reads with its own credential until one is chosen, and the source’s id is sent', async () => {
    render(<KnowledgeSection />)
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      const c = credentialsAnswer(String(url))
      if (c) return c
      if (String(url).endsWith('/sources/test')) return new Response(JSON.stringify({ ok: true, items: 1, sample: ['Runbook: restart'] }))
      return new Response(JSON.stringify(current))
    })
    const row = (await screen.findAllByTestId('knowledge-source')).find((r) => r.dataset.source === 'infra')!
    expect(row.textContent).toContain('its own credential, kept before 0.12.1')
    fireEvent.click(within(row).getByText('Edit…'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    expect(within(drawer).getByTestId('knowledge-source-own-credential').textContent).toContain('kept before 0.12.1')
    fireEvent.click(within(drawer).getByTestId('knowledge-source-test'))
    await waitFor(() => expect(within(drawer).getByTestId('knowledge-source-test-result').textContent).toBe('Reached: 1 item, e.g. Runbook: restart.'))
    const body = calls.find((c) => c.url === '/api/v1/knowledge/sources/test')?.body
    expect(body).toEqual({ source: { ...view().settings.sources[1], credential: '' }, saved: 'infra' })
  })

  it('lists only what is synced or looked up: the uploads are the Uploaded documents section after the table, which focus scrolls to', async () => {
    const base = view()
    current = view({ settings: { ...base.settings, sources: [...base.settings.sources, { id: 'added', name: 'Added in Mycroft', kind: 'upload' }] } })
    const scrolled = vi.fn()
    Element.prototype.scrollIntoView = scrolled
    render(<KnowledgeSection focus="added" />)
    const rows = await screen.findAllByTestId('knowledge-source')
    expect(rows.map((r) => r.dataset.source)).toEqual(['console', 'infra', 'fixes'])
    const section = await screen.findByTestId('uploads-section')
    expect((await within(section).findAllByTestId('uploads-tab-notes')).length).toBe(1)
    expect(screen.queryByTestId('knowledge-documents')).toBeNull()
    expect(screen.queryByTestId('knowledge-added')).toBeNull()
    expect(screen.getByTestId('knowledge-items-link').getAttribute('href')).toBe('#/aiops/mycroft/sources')
    await waitFor(() => expect(scrolled).toHaveBeenCalled())
  })

  it('adds a web site: its start address, link depth, page limit, and among several credentials the one chosen: never guessed', async () => {
    choices = { http: [cred('cr-site', 'Docs reader', 'userpass', 'https://docs.example.com'), cred('cr-all', 'Example SSO', 'oauth2', 'https://docs.example.com')] }
    render(<KnowledgeSection />)
    fireEvent.click(await screen.findByTestId('knowledge-add'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-kind'), { target: { value: 'web' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-source-name'), { target: { value: 'Vendor docs' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-web-url'), { target: { value: 'https://docs.example.com/' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-web-depth'), { target: { value: '2' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-web-pages'), { target: { value: '120' } })
    expect(within(drawer).queryByTestId('knowledge-web-auth')).toBeNull()
    await waitFor(() => expect(within(drawer).getByTestId('knowledge-source-credential-hint').textContent).toContain('2 credentials cover this address: choose one.'))
    expect(within(drawer).getByTestId('knowledge-source-save')).toHaveProperty('disabled', true)
    fireEvent.change(within(drawer).getByTestId('knowledge-source-credential-select'), { target: { value: 'cr-site' } })
    expect(within(drawer).getByTestId('knowledge-source-test')).toBeTruthy()
    fireEvent.click(within(drawer).getByTestId('knowledge-source-save'))
    await waitFor(() => {
      const body = calls.find((c) => c.method === 'POST' && c.url === '/api/v1/knowledge/sources')?.body
      expect(body).toEqual({ source: { id: 'vendor-docs', name: 'Vendor docs', kind: 'web', url: 'https://docs.example.com/', depth: 2, pages: 120, auth: '', credential: 'cr-site' } })
    })
  })

  it('lists a web site with its start address, depth, pages and what is excluded, and its Wiki sync job', async () => {
    const base = view()
    const site = { id: 'vendor', name: 'Vendor docs', kind: 'web' as const, url: 'https://docs.example.com/', depth: 1, pages: 20, excluded: [{ pattern: '/blog/*', by: 'Ann', at: '2026-11-10T10:00:00Z' }] }
    current = view({ settings: { ...base.settings, sources: [...base.settings.sources, site] } })
    render(<KnowledgeSection />)
    const row = (await screen.findAllByTestId('knowledge-source')).find((r) => r.dataset.source === 'vendor')!
    expect(within(row).getByTestId('knowledge-source-web-line').textContent).toBe('https://docs.example.com/ · and the pages it links to · at most 20 pages · 1 excluded')
    expect(row.textContent).toContain('Web site')
    expect(row.textContent).toContain('no sign-in')
    expect(within(row).getByTestId('knowledge-source-job').getAttribute('href')).toContain('wiki-sync-vendor')
  })

  // --- Live sources (0.12.1, KN-39, KN-42) ---
  it('adds a live source: its addresses and sign-in, a Test that reads each address, and the list says its mode', async () => {
    render(<KnowledgeSection />)
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      const c = credentialsAnswer(String(url))
      if (c) return c
      if (String(url).endsWith('/sources/test'))
        return new Response(
          JSON.stringify({
            ok: false,
            items: 1,
            error: 'not every address could be read',
            live: [
              { url: 'https://status.example.com/', kind: 'page', ok: true, pages: 1, bytes: 12288, title: 'All known problems' },
              { url: 'https://example.atlassian.net/wiki', kind: 'confluenceSearch', ok: false, error: 'it refused the credential (401 Unauthorized)' },
            ],
          }),
        )
      if (String(url).endsWith('/sources') && init?.method === 'POST') return new Response(JSON.stringify({ id: 'known-problems' }), { status: 201 })
      return new Response(JSON.stringify(current))
    })
    choices = { http: [cred('cr-status', 'Status page', 'token', 'https://status.example.com')] }
    fireEvent.click(await screen.findByTestId('knowledge-add'))
    const drawer = screen.getByTestId('knowledge-source-drawer')
    fireEvent.change(within(drawer).getByTestId('knowledge-source-mode-select'), { target: { value: 'live' } })
    expect(within(drawer).queryByTestId('knowledge-source-kind')).toBeNull()
    // No sign-in of its own: a credential is chosen.
    expect(within(drawer).queryByTestId('knowledge-source-token')).toBeNull()
    expect(within(drawer).queryByTestId('knowledge-live-auth')).toBeNull()
    fireEvent.change(within(drawer).getByTestId('knowledge-source-name'), { target: { value: 'Known problems' } })
    fireEvent.change(within(drawer).getByTestId('knowledge-live-url'), { target: { value: 'https://status.example.com/' } })
    fireEvent.click(within(drawer).getByTestId('knowledge-live-add'))
    const rows = within(drawer).getAllByTestId('knowledge-live-address')
    expect(rows).toHaveLength(2)
    fireEvent.change(within(rows[1]!).getByTestId('knowledge-live-kind'), { target: { value: 'confluenceSearch' } })
    fireEvent.change(within(rows[1]!).getByTestId('knowledge-live-url'), { target: { value: 'https://example.atlassian.net/wiki' } })
    fireEvent.change(within(rows[1]!).getByTestId('knowledge-live-space'), { target: { value: 'OPS' } })
    // A third, left empty, is not sent.
    fireEvent.click(within(drawer).getByTestId('knowledge-live-add'))
    await waitFor(() => expect((within(drawer).getByTestId('knowledge-source-credential-select') as HTMLSelectElement).value).toBe('cr-status'))
    const source = {
      id: 'known-problems',
      name: 'Known problems',
      kind: 'live',
      addresses: [
        { kind: 'page', url: 'https://status.example.com/' },
        { kind: 'confluenceSearch', url: 'https://example.atlassian.net/wiki', space: 'OPS' },
      ],
      auth: '',
      credential: 'cr-status',
    }
    fireEvent.click(within(drawer).getByTestId('knowledge-source-test'))
    await waitFor(() => expect(within(drawer).getByTestId('knowledge-source-test-result').textContent).toBe('1 of 2 addresses read.'))
    expect(calls.find((c) => c.url === '/api/v1/knowledge/sources/test')?.body).toEqual({ source })
    const results = within(drawer).getByTestId('knowledge-source-live-results')
    expect(results.textContent).toContain('https://status.example.com/: OK · 12 KiB · All known problems')
    expect(results.textContent).toContain('https://example.atlassian.net/wiki: it refused the credential (401 Unauthorized)')
    expect(results.querySelectorAll('[data-state="error"]')).toHaveLength(1)
    fireEvent.click(within(drawer).getByTestId('knowledge-source-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url === '/api/v1/knowledge/sources')?.body).toEqual({ source }))
  })

  it('lists a live source as Live, looked up when asked, with no Wiki sync job', async () => {
    current = view({
      settings: {
        ...view().settings,
        sources: [
          ...view().settings.sources,
          {
            id: 'status',
            name: 'Known problems',
            kind: 'live',
            addresses: [
              { kind: 'page', url: 'https://status.example.com/' },
              { kind: 'confluencePage', url: 'https://wiki.example.com/x' },
            ],
            auth: '',
          },
        ],
      },
    })
    render(<KnowledgeSection />)
    const rows = await screen.findAllByTestId('knowledge-source')
    const live = rows.find((r) => r.dataset.source === 'status')!
    expect(within(live).getByTestId('knowledge-source-mode').textContent).toBe('Live')
    expect(within(rows[1]!).getByTestId('knowledge-source-mode').textContent).toBe('Ingest')
    expect(within(live).getByTestId('knowledge-source-state').textContent).toBe('Looked up when asked')
    expect(within(live).getByTestId('knowledge-source-live-line').textContent).toBe('https://status.example.com/ and 1 more')
    expect(within(live).queryByTestId('knowledge-source-job')).toBeNull()
    expect(within(live).getByText('no sign-in')).toBeTruthy()
  })

  it('makes an id from a name', () => {
    expect(idFrom('Confluence space: Infrastructure')).toBe('confluence-space-infrastructure')
    expect(idFrom('!!!')).toBe('source')
  })
})
