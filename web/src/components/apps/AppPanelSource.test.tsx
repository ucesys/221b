import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AppView } from '../../api/apps'
import { LiveContext, LiveStore } from '../../api/live'
import type { AppRow } from '../../columns/apps'
import { SourceCard } from './AppPanel'

// A release's Setup (0.12.1, RP-03): its git repository is picked from
// Administration › Settings › Repositories; the release keeps its path and branch or tag,
// and sends the repository's name, never a credential.
describe('the Setup card’s git repository', () => {
  const calls: { method: string; url: string; body?: Record<string, unknown> }[] = []
  beforeEach(() => {
    calls.length = 0
    sessionStorage.clear()
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      const u = String(url)
      calls.push({ method, url: u, body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (u === '/api/v1/credentials') return new Response(JSON.stringify({ credentials: [], mayEdit: false }))
      if (u === '/api/v1/helm-repositories')
        return new Response(
          JSON.stringify({
            repositories: [
              { name: '221b', kind: 'git', url: 'https://github.com/ucesys/221b', usedBy: [] },
              { name: 'mirror', kind: 'git', url: 'https://git.example.com/mirror/221b', defaultBranch: 'stable', usedBy: [] },
            ],
            mayEdit: false,
          }),
        )
      if (u.endsWith('/inspect')) return new Response(JSON.stringify({ error: 'not now' }), { status: 502 })
      if (method === 'PUT') return new Response(JSON.stringify({ ...app, source: { ...app.source, repository: 'mirror' } }))
      return new Response('{}')
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })
  const row = { namespace: '221b', name: 'k8s-console', key: '221b/k8s-console' } as unknown as AppRow
  const app = {
    namespace: '221b',
    name: 'k8s-console',
    source: { repo: 'https://github.com/ucesys/221b', repository: '221b', path: 'deploy/helm/221b', ref: 'main', refKind: 'branch' },
    created: '2026-11-01T00:00:00Z',
    updated: '2026-11-01T00:00:00Z',
  } as unknown as AppView
  const card = (a: AppView = app) =>
    render(
      <LiveContext.Provider value={new LiveStore('/test-events')}>
        <SourceCard clusterId="local" row={row} app={a} canChange onResult={() => {}} now={Date.now()} />
      </LiveContext.Provider>,
    )

  it('shows the repository it names, and moves to another keeping its path and branch', async () => {
    card()
    const sel = screen.getByTestId('app-src-repository-select') as HTMLSelectElement
    await waitFor(() => expect(sel.disabled).toBe(false))
    expect(sel.value).toBe('221b')
    expect([...sel.options].map((o) => o.value)).toEqual(['', '221b', 'mirror'])
    expect(screen.queryByTestId('app-src-repo')).toBeNull()
    expect(screen.queryByTestId('app-src-credential')).toBeNull()
    fireEvent.change(sel, { target: { value: 'mirror' } })
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')).toBeTruthy())
    const put = calls.find((c) => c.method === 'PUT')!
    expect(put.url).toBe('/api/v1/apps/221b/k8s-console')
    expect(put.body).toEqual({ source: { repo: 'https://git.example.com/mirror/221b', repository: 'mirror', path: 'deploy/helm/221b', ref: 'main', refKind: 'branch' }, createNamespace: false })
  })

  it('says what a release of before 0.12.1 keeps until a repository is picked', async () => {
    card({ ...app, source: { ...app.source, repository: undefined } } as AppView)
    await waitFor(() => expect(screen.getByTestId('app-src-repository-hint').textContent).toContain('Kept as https://github.com/ucesys/221b: pick its repository'))
    expect((screen.getByTestId('app-src-repository-select') as HTMLSelectElement).value).toBe('')
  })
})
