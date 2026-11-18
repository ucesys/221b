import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LiveContext, LiveStore } from '../../api/live'
import { AppDialog } from './AppDialog'

// The new application's git repository (0.12.1, RP-03): picked from
// Administration › Settings › Repositories, whose address and credential it is fetched
// with; the application sends the repository's name with its own path and
// branch, never a credential or a token. A SuperAdmin adds one inline;
// the others pick from the list.
describe('the new application’s git repository', () => {
  const calls: { method: string; url: string; body?: Record<string, unknown> }[] = []
  let mayEdit = true
  const listed = [
    { name: '221b', kind: 'git', url: 'https://github.com/ucesys/221b', defaultBranch: 'main', usedBy: [] },
    { name: 'shop', kind: 'git', url: 'https://github.com/ucesys/shop', credential: 'cr-org', usedBy: [] },
    { name: 'bitnami', kind: 'helm', url: 'https://charts.example.com', usedBy: [] },
  ]
  beforeEach(() => {
    calls.length = 0
    mayEdit = true
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      const u = String(url)
      calls.push({ method, url: u, body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (u.startsWith('/api/v1/credentials/choices')) return new Response(JSON.stringify({ choices: [] }))
      if (u === '/api/v1/credentials') return new Response(JSON.stringify({ credentials: [{ id: 'cr-org', name: 'ucesys', kind: 'token', endpoint: 'https://github.com/ucesys', usedBy: [] }], mayEdit: true }))
      if (u === '/api/v1/helm-repositories') return new Response(JSON.stringify({ repositories: listed, mayEdit }))
      if (method === 'PUT' && u.startsWith('/api/v1/helm-repositories/')) {
        listed.push({ name: 'payments', kind: 'git', url: 'https://github.com/org/payments', usedBy: [] })
        return new Response(JSON.stringify({ name: 'payments', kind: 'git', url: 'https://github.com/org/payments', credMigrated: true }))
      }
      if (u.endsWith('/apps/inspect')) return new Response(JSON.stringify({ error: 'not now' }), { status: 502 })
      if (method === 'POST' && u.endsWith('/apps')) return new Response(JSON.stringify({ id: 'card-1' }), { status: 202 })
      return new Response('{}')
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  const open = () =>
    render(
      <LiveContext.Provider value={new LiveStore('/test-events')}>
        <AppDialog cluster="local" mode="create" namespaces={['web']} onClose={() => {}} onDone={() => {}} />
      </LiveContext.Provider>,
    )
  const select = async () => {
    const sel = screen.getByTestId('app-git-repository-select') as HTMLSelectElement
    await waitFor(() => expect(sel.disabled).toBe(false))
    return sel
  }

  it('picks a listed git repository, takes its default branch, and sends its name, never a credential', async () => {
    open()
    expect(screen.queryByTestId('app-repo')).toBeNull()
    expect(screen.queryByTestId('app-credential')).toBeNull()
    fireEvent.change(screen.getByTestId('app-namespace'), { target: { value: 'web' } })
    fireEvent.change(screen.getByTestId('app-name'), { target: { value: 'console' } })
    const sel = await select()
    expect([...sel.options].map((o) => o.value)).toEqual(['', '221b', 'shop', '__add'])
    expect(screen.getByTestId('app-submit')).toHaveProperty('disabled', true)
    fireEvent.change(sel, { target: { value: '221b' } })
    expect((screen.getByTestId('app-ref-text') as HTMLInputElement).value).toBe('main')
    await waitFor(() => expect(screen.getByTestId('app-git-repository-hint').textContent).toContain('Public: no credential'))
    fireEvent.change(screen.getByTestId('app-path'), { target: { value: 'deploy/helm/221b' } })
    fireEvent.click(screen.getByTestId('app-submit'))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url.endsWith('/apps'))).toBeTruthy())
    const body = calls.find((c) => c.method === 'POST' && c.url.endsWith('/apps'))!.body!
    expect(body).toMatchObject({ namespace: 'web', name: 'console', source: { repo: 'https://github.com/ucesys/221b', repository: '221b', path: 'deploy/helm/221b', ref: 'main', refKind: 'branch' } })
    expect(body).not.toHaveProperty('credential')
    // Its look-over goes with the repository's address.
    expect(calls.find((c) => c.url.endsWith('/apps/inspect'))?.body).toMatchObject({ repo: 'https://github.com/ucesys/221b' })
  })

  it('says which credential a private one is fetched with', async () => {
    open()
    fireEvent.change(await select(), { target: { value: 'shop' } })
    await waitFor(() => expect(screen.getByTestId('app-git-repository-hint').textContent).toContain('Fetched with ucesys'))
    expect(calls.find((c) => c.url.endsWith('/apps/inspect'))?.body).toMatchObject({ repo: 'https://github.com/ucesys/shop', credential: 'cr-org' })
  })

  it('lets a SuperAdmin add one inline, and picks it', async () => {
    open()
    fireEvent.change(await select(), { target: { value: '__add' } })
    const form = screen.getByTestId('app-git-repository-new')
    fireEvent.change(within(form).getByTestId('app-git-repository-new-url'), { target: { value: 'https://github.com/org/payments.git' } })
    expect((within(form).getByTestId('app-git-repository-new-name') as HTMLInputElement).value).toBe('payments')
    await waitFor(() => expect((within(form).getByTestId('app-git-repository-new-credential-select') as HTMLSelectElement).value).toBe(''))
    fireEvent.click(within(form).getByTestId('app-git-repository-new-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')).toMatchObject({ url: '/api/v1/helm-repositories/payments', body: { kind: 'git', url: 'https://github.com/org/payments.git' } }))
    await waitFor(() => expect((screen.getByTestId('app-git-repository-select') as HTMLSelectElement).value).toBe('payments'))
  })

  it('offers no Add to those who only pick', async () => {
    mayEdit = false
    open()
    const sel = await select()
    expect([...sel.options].map((o) => o.value)).not.toContain('__add')
  })
})
