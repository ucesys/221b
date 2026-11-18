import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { HelmReposSettings } from './HelmReposSettings'

// Administration › Settings › Repositories (0.12.1, RP-01 to RP-06; Settings › Applications
// before, HR-02) with Administration › Credentials (CR-11): a repository names a
// credential, chosen by name, and keeps no secret; git repositories are
// listed beside the chart repositories, each with what uses it.
const FP = Array(32).fill('AB').join(':')

describe('Administration › Settings › Repositories', () => {
  const calls: { method: string; url: string; body?: Record<string, unknown> }[] = []
  let migration: Record<string, unknown> | undefined
  beforeEach(() => {
    calls.length = 0
    migration = { created: [{ name: '221b', url: 'https://github.com/ucesys/221b', from: ['application 221b-github/221b-github', 'application 221b/k8s-console'] }], notes: [] }
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push({ method, url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (String(url).startsWith('/api/v1/credentials/choices')) return new Response(JSON.stringify({ choices: [{ id: 'cr-charts', name: 'Charts reader', kind: 'userpass', endpoint: 'https://charts.example.com' }] }))
      if (String(url) === '/api/v1/credentials') return new Response(JSON.stringify({ credentials: [{ id: 'cr-charts', name: 'Charts reader', kind: 'userpass', endpoint: 'https://charts.example.com', usedBy: [] }], mayEdit: true }))
      if (String(url).endsWith('/migration/dismiss')) {
        migration = undefined
        return new Response(null, { status: 204 })
      }
      if (String(url) === '/api/v1/helm-repositories/test')
        return new Response(
          JSON.stringify({
            ok: false,
            text: 'the repository could not be fetched',
            certificate: { subject: 'CN=git.corp.example', issuer: 'CN=Corp CA', notBefore: '2026-01-01T00:00:00Z', notAfter: '2027-01-01T00:00:00Z', sha256: FP, trusted: false, trustable: true },
          }),
        )
      if (method === 'POST' && String(url).endsWith('/trust')) return new Response(JSON.stringify({ pin: { sha256: FP, by: 'ann' }, with: 'repository' }))
      if (String(url).endsWith('/rename')) return new Response(JSON.stringify({ name: 'console', kind: 'git', url: 'https://github.com/ucesys/221b', credMigrated: true }))
      if (method === 'PUT') return new Response(JSON.stringify({ name: 'bitnami', kind: 'helm', url: 'https://charts.example.com/stable', credential: 'cr-charts', credMigrated: true }))
      return new Response(
        JSON.stringify({
          repositories: [
            { name: '221b', kind: 'git', url: 'https://github.com/ucesys/221b', credMigrated: true, usedBy: ['application 221b-github/221b-github', 'application 221b/k8s-console'] },
            { name: 'old', kind: 'helm', url: 'https://old.example.com', auth: 'basic', username: 'u', hasSecret: true, usedBy: [] },
            { name: 'private', kind: 'oci', url: 'oci://ghcr.io/org', credential: 'cr-charts', credMigrated: true, usedBy: [] },
          ],
          mayEdit: true,
          migration,
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
    await waitFor(() => expect(within(rows[2]!).getByTestId('helm-repo-credential').textContent).toBe('Charts reader'))
    expect(within(rows[1]!).getByTestId('helm-repo-credential').textContent).toContain('kept before 0.12.1')
    fireEvent.click(screen.getByTestId('helm-repo-add'))
    const editor = screen.getByTestId('helm-repo-editor')
    expect(within(editor).queryByTestId('helm-repo-secret')).toBeNull()
    fireEvent.click(within(editor).getByTestId('helm-repo-kind-helm'))
    expect(within(editor).queryByTestId('helm-repo-branch')).toBeNull()
    fireEvent.change(within(editor).getByTestId('helm-repo-name'), { target: { value: 'bitnami' } })
    fireEvent.change(within(editor).getByTestId('helm-repo-url'), { target: { value: 'https://charts.example.com/stable' } })
    await waitFor(() => expect((within(editor).getByTestId('helm-repo-credential-picker-select') as HTMLSelectElement).value).toBe('cr-charts'))
    fireEvent.click(within(editor).getByTestId('helm-repo-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ kind: 'helm', url: 'https://charts.example.com/stable', credential: 'cr-charts' }))
    expect(calls.find((c) => c.method === 'PUT')?.url).toBe('/api/v1/helm-repositories/bitnami')
  })

  it('lists a git repository with what uses it, says so before a removal, and shows what the upgrade made until dismissed', async () => {
    render(<HelmReposSettings />)
    const rows = await screen.findAllByTestId('helm-repo-row')
    expect(rows[0]!.dataset.repo).toBe('221b')
    expect(rows[0]!.textContent).toContain('Git repository')
    expect(within(rows[0]!).getByTestId('helm-repo-used-by').textContent).toBe('application 221b-github/221b-github, application 221b/k8s-console')
    expect(within(rows[1]!).getByTestId('helm-repo-used-by').textContent).toBe('Nothing')
    const banner = screen.getByTestId('repos-migration')
    expect(banner.textContent).toContain('Created from existing settings')
    expect(banner.textContent).toContain('221b')
    fireEvent.click(within(rows[0]!).getByTestId('helm-repo-remove'))
    expect(screen.getByTestId('confirm').textContent).toContain('It is used by application 221b-github/221b-github, application 221b/k8s-console')
    fireEvent.click(within(screen.getByTestId('confirm')).getByText('Cancel'))
    fireEvent.click(within(banner).getByTestId('repos-migration-dismiss'))
    await waitFor(() => expect(screen.queryByTestId('repos-migration')).toBeNull())
    expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/helm-repositories/migration/dismiss')).toBe(true)
  })

  it('adds a git repository with its default branch, and renames one, keeping its kind', async () => {
    render(<HelmReposSettings />)
    const rows = await screen.findAllByTestId('helm-repo-row')
    fireEvent.click(screen.getByTestId('helm-repo-add'))
    let editor = screen.getByTestId('helm-repo-editor')
    // Git first.
    expect(within(editor).getByTestId('helm-repo-kind-git').getAttribute('aria-pressed')).toBe('true')
    fireEvent.change(within(editor).getByTestId('helm-repo-name'), { target: { value: 'shop' } })
    fireEvent.change(within(editor).getByTestId('helm-repo-url'), { target: { value: 'https://github.com/org/shop' } })
    fireEvent.change(within(editor).getByTestId('helm-repo-branch'), { target: { value: 'trunk' } })
    await waitFor(() => expect((within(editor).getByTestId('helm-repo-credential-picker-select') as HTMLSelectElement).disabled).toBe(false))
    fireEvent.change(within(editor).getByTestId('helm-repo-credential-picker-select'), { target: { value: '' } })
    fireEvent.click(within(editor).getByTestId('helm-repo-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ kind: 'git', url: 'https://github.com/org/shop', defaultBranch: 'trunk' }))
    expect(calls.find((c) => c.url.startsWith('/api/v1/credentials/choices'))?.url).toContain('use=git')
    // Renamed: the new name first, then the rest.
    calls.length = 0
    fireEvent.click(within(rows[0]!).getByTestId('helm-repo-edit'))
    editor = screen.getByTestId('helm-repo-editor')
    expect(within(editor).queryByTestId('helm-repo-kind-helm')).toBeNull()
    fireEvent.change(within(editor).getByTestId('helm-repo-name'), { target: { value: 'console' } })
    fireEvent.click(within(editor).getByTestId('helm-repo-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')?.url).toBe('/api/v1/helm-repositories/console'))
    expect(calls.find((c) => c.url.endsWith('/rename'))).toMatchObject({ method: 'POST', url: '/api/v1/helm-repositories/221b/rename', body: { name: 'console' } })
  })

  // CR-15, CR-16, CR-19: a saved repository's Test shows a certificate of
  // no known CA; Trust pins it, Forget lets it go; a changed address must
  // be saved first.
  it('trusts the certificate a saved repository presents, and forgets it', async () => {
    render(<HelmReposSettings />)
    const rows = await screen.findAllByTestId('helm-repo-row')
    fireEvent.click(within(rows[0]!).getByTestId('helm-repo-edit'))
    const editor = screen.getByTestId('helm-repo-editor')
    fireEvent.click(within(editor).getByTestId('helm-repo-try'))
    const cert = await within(editor).findByTestId('cert-result')
    expect(within(cert).getByTestId('cert-sha256').textContent).toBe(FP)
    fireEvent.click(within(cert).getByTestId('cert-trust'))
    fireEvent.click(within(await screen.findByTestId('confirm')).getByTestId('confirm-ok'))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url === '/api/v1/helm-repositories/221b/trust')?.body).toEqual({ sha256: FP }))
    const note = await within(editor).findByTestId('cert-pinned-note')
    fireEvent.click(within(note).getByTestId('cert-forget'))
    fireEvent.click(within(await screen.findByTestId('confirm')).getByTestId('confirm-ok'))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/v1/helm-repositories/221b/trust')).toBe(true))
    // Another address: Trust waits for Save.
    fireEvent.change(within(editor).getByTestId('helm-repo-url'), { target: { value: 'https://github.com/ucesys/other' } })
    fireEvent.click(within(editor).getByTestId('helm-repo-try'))
    await within(editor).findByTestId('cert-result')
    expect(within(editor).queryByTestId('cert-trust')).toBeNull()
    expect(within(editor).getByTestId('cert-trust-unavailable').textContent).toContain('Save the repository first')
  })
})
