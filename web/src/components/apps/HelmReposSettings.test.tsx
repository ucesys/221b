import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { HelmReposSettings } from './HelmReposSettings'

// Settings › Applications (HR-02) with Settings › Credentials (0.12.1, CR-11):
// a repository names a credential, chosen by name, and keeps no secret.
describe('Settings › Applications', () => {
  const calls: { method: string; url: string; body?: Record<string, unknown> }[] = []
  beforeEach(() => {
    calls.length = 0
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push({ method, url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (String(url).startsWith('/api/v1/credentials/choices')) return new Response(JSON.stringify({ choices: [{ id: 'cr-charts', name: 'Charts reader', kind: 'userpass', endpoint: 'https://charts.example.com' }] }))
      if (String(url) === '/api/v1/credentials') return new Response(JSON.stringify({ credentials: [{ id: 'cr-charts', name: 'Charts reader', kind: 'userpass', endpoint: 'https://charts.example.com', usedBy: [] }], mayEdit: true }))
      if (method === 'PUT') return new Response(JSON.stringify({ name: 'bitnami', kind: 'helm', url: 'https://charts.example.com/stable', credential: 'cr-charts', credMigrated: true }))
      return new Response(
        JSON.stringify({
          repositories: [
            { name: 'old', kind: 'helm', url: 'https://old.example.com', auth: 'basic', username: 'u', hasSecret: true },
            { name: 'private', kind: 'oci', url: 'oci://ghcr.io/org', credential: 'cr-charts', credMigrated: true },
          ],
          mayEdit: true,
        }),
      )
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('lists each repository’s credential by name, and adds one naming the credential that covers it', async () => {
    render(<HelmReposSettings />)
    const rows = await screen.findAllByTestId('helm-repo-row')
    await waitFor(() => expect(within(rows[1]!).getByTestId('helm-repo-credential').textContent).toBe('Charts reader'))
    expect(within(rows[0]!).getByTestId('helm-repo-credential').textContent).toContain('kept before 0.12.1')
    fireEvent.click(screen.getByTestId('helm-repo-add'))
    const editor = screen.getByTestId('helm-repo-editor')
    expect(within(editor).queryByTestId('helm-repo-secret')).toBeNull()
    fireEvent.change(within(editor).getByTestId('helm-repo-name'), { target: { value: 'bitnami' } })
    fireEvent.change(within(editor).getByTestId('helm-repo-url'), { target: { value: 'https://charts.example.com/stable' } })
    await waitFor(() => expect((within(editor).getByTestId('helm-repo-credential-picker-select') as HTMLSelectElement).value).toBe('cr-charts'))
    fireEvent.click(within(editor).getByTestId('helm-repo-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ kind: 'helm', url: 'https://charts.example.com/stable', credential: 'cr-charts' }))
    expect(calls.find((c) => c.method === 'PUT')?.url).toBe('/api/v1/helm-repositories/bitnami')
  })
})
