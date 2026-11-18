import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CredentialPicker } from './CredentialPicker'
import { CredentialsSettings, expiryText } from './CredentialsSettings'

// --- Administration › Credentials (0.12.1, CR-01 to CR-14) ---

const day = 86_400_000
const FP = 'AB:CD:' + Array(30).fill('EF').join(':')

describe('Administration › Credentials', () => {
  const calls: { method: string; url: string; body?: Record<string, unknown> }[] = []
  let list: Record<string, unknown>[] = []
  let migration: Record<string, unknown> | undefined
  let removeAnswer: Response | null = null
  let testAnswer: Record<string, unknown> | null = null
  let mayEdit = true
  beforeEach(() => {
    mayEdit = true
    Element.prototype.scrollTo = () => {}
    calls.length = 0
    removeAnswer = null
    testAnswer = null
    migration = { created: [{ id: 'cr-1', name: 'github.com/ucesys (from app 221b/k8s-console)', from: ['app 221b/k8s-console'] }] }
    list = [
      {
        id: 'cr-1',
        name: 'github.com/ucesys (from app 221b/k8s-console)',
        kind: 'token',
        endpoint: 'https://github.com/ucesys',
        sendAs: { method: 'basic', username: 'x-access-token' },
        set: ['token'],
        expires: new Date(Date.now() + 5 * day).toISOString(),
        usedBy: [{ what: 'app 221b/k8s-console' }],
        how: 'a token as Basic for x-access-token',
        changed: '2026-11-17T09:00:00Z',
        changedBy: 'the upgrade to 0.12.1',
      },
    ]
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push({ method, url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (method === 'DELETE' && removeAnswer) return removeAnswer
      if (method === 'DELETE' || String(url).endsWith('/migration/dismiss')) return new Response(null, { status: 204 })
      if (method === 'POST' && String(url) === '/api/v1/credentials') return new Response(JSON.stringify({ ...(JSON.parse(String(init!.body)) as object), id: 'cr-2', usedBy: [] }), { status: 201 })
      if (method === 'POST' && String(url).endsWith('/trust')) return new Response(JSON.stringify({ pinnedCert: { sha256: FP, subject: 'CN=git.corp.example', by: 'ann', at: '2026-11-18T09:00:00Z' } }))
      if (String(url).endsWith('/test') && testAnswer) return new Response(JSON.stringify(testAnswer))
      if (String(url).endsWith('/test'))
        return new Response(JSON.stringify({ at: '2026-11-17T10:00:00Z', ok: false, status: 401, text: 'Sent an Authorization header with Bearer to github.com; it answered 401 Unauthorized.', suggest: 'basic' }))
      return new Response(JSON.stringify({ credentials: list, mayEdit, ...(migration ? { migration } : {}) }))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('lists the credentials with their uses, expiry and what the upgrade made, never a secret', async () => {
    render(<CredentialsSettings />)
    const row = await screen.findByTestId('credential-row')
    expect(row.textContent).toContain('https://github.com/ucesys')
    expect(row.textContent).toContain('Basic as x-access-token')
    expect(within(row).getByTestId('credential-used-by').textContent).toBe('app 221b/k8s-console')
    expect(within(row).getByTestId('credential-expiry').textContent).toMatch(/Expires in [45] days/)
    expect(screen.getByTestId('credentials-expiring').textContent).toContain('expires within 14 days')
    const notice = screen.getByTestId('credentials-migration')
    expect(notice.textContent).toContain('Created from existing settings')
    expect(notice.textContent).toContain('from app 221b/k8s-console')
    fireEvent.click(within(notice).getByTestId('credentials-migration-dismiss'))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/credentials/migration/dismiss')).toBe(true))
  })

  it('adds a token sent in a custom header; a query parameter warns that it lands in logs', async () => {
    list = []
    migration = undefined
    render(<CredentialsSettings />)
    fireEvent.click(await screen.findByTestId('credential-add'))
    const drawer = screen.getByTestId('credential-drawer')
    fireEvent.change(within(drawer).getByTestId('credential-name'), { target: { value: 'Grafana' } })
    fireEvent.change(within(drawer).getByTestId('credential-endpoint'), { target: { value: 'https://grafana.example.com' } })
    fireEvent.click(within(drawer).getByTestId('credential-send-query'))
    expect(within(drawer).getByTestId('credential-query-warning').textContent).toContain('logs')
    fireEvent.click(within(drawer).getByTestId('credential-send-header'))
    fireEvent.change(within(drawer).getByTestId('credential-header-name'), { target: { value: 'X-API-Key' } })
    expect(within(drawer).getByTestId('credential-save')).toHaveProperty('disabled', true)
    const secret = within(drawer).getByTestId('credential-secret-token') as HTMLInputElement
    expect(secret.type).toBe('password')
    fireEvent.change(secret, { target: { value: 'glsa_secret' } })
    fireEvent.click(within(drawer).getByTestId('credential-save'))
    await waitFor(() =>
      expect(calls.find((c) => c.method === 'POST' && c.url === '/api/v1/credentials')?.body).toEqual({
        name: 'Grafana',
        kind: 'token',
        endpoint: 'https://grafana.example.com',
        description: '',
        sendAs: { method: 'header', header: 'X-API-Key', prefix: '' },
        secret: { token: 'glsa_secret' },
      }),
    )
  })

  it('edits without sending the kept secret; tests and shows how it was sent and what to try', async () => {
    render(<CredentialsSettings />)
    fireEvent.click(await screen.findByTestId('credential-edit'))
    const drawer = screen.getByTestId('credential-drawer')
    expect((within(drawer).getByTestId('credential-secret-token') as HTMLInputElement).placeholder).toContain('kept')
    fireEvent.change(within(drawer).getByTestId('credential-test-target'), { target: { value: '221b.git' } })
    fireEvent.click(within(drawer).getByTestId('credential-test-run'))
    await waitFor(() => expect(within(drawer).getByTestId('credential-test-result').textContent).toContain('Try Send as Basic'))
    expect(calls.find((c) => c.url === '/api/v1/credentials/cr-1/test')?.body).toEqual({ target: '221b.git' })
    fireEvent.change(within(drawer).getByTestId('credential-description'), { target: { value: 'read only' } })
    fireEvent.click(within(drawer).getByTestId('credential-save'))
    await waitFor(() => {
      const put = calls.find((c) => c.method === 'PUT' && c.url === '/api/v1/credentials/cr-1')?.body
      expect(put).toMatchObject({ name: 'github.com/ucesys (from app 221b/k8s-console)', description: 'read only', sendAs: { method: 'basic', username: 'x-access-token' } })
      expect(put).not.toHaveProperty('secret')
    })
  })

  // CR-15, CR-16, CR-19: Test shows the certificate a server presented; a
  // SuperAdmin trusts it, after confirming its fingerprint, and forgets it.
  it('shows an untrusted certificate, trusts it and forgets it', async () => {
    testAnswer = {
      at: '2026-11-18T09:00:00Z',
      ok: false,
      text: 'Sent an Authorization header with Bearer to git.corp.example, which could not be reached: x509: certificate signed by unknown authority',
      certificate: { subject: 'CN=git.corp.example', issuer: 'CN=git.corp.example', notBefore: '2026-01-01T00:00:00Z', notAfter: '2027-01-01T00:00:00Z', sha256: FP, selfSigned: true, trusted: false, trustable: true },
    }
    render(<CredentialsSettings />)
    fireEvent.click(await screen.findByTestId('credential-edit'))
    const drawer = screen.getByTestId('credential-drawer')
    expect(within(drawer).queryByTestId('cert-pinned-note')).toBeNull()
    fireEvent.change(within(drawer).getByTestId('credential-test-target'), { target: { value: 'https://git.corp.example/x.git' } })
    fireEvent.click(within(drawer).getByTestId('credential-test-run'))
    const cert = await within(drawer).findByTestId('cert-result')
    expect(within(cert).getByTestId('cert-headline').textContent).toContain('self-signed')
    expect(within(cert).getByTestId('cert-subject').textContent).toBe('CN=git.corp.example')
    expect(within(cert).getByTestId('cert-sha256').textContent).toBe(FP)
    expect(within(cert).getByTestId('cert-validity').textContent).toBe('2026-01-01 to 2027-01-01')
    fireEvent.click(within(cert).getByTestId('cert-trust'))
    const dialog = await screen.findByTestId('confirm')
    expect(dialog.textContent).toContain(FP)
    fireEvent.click(within(dialog).getByTestId('confirm-ok'))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url === '/api/v1/credentials/cr-1/trust')?.body).toEqual({ sha256: FP, target: 'https://git.corp.example/x.git' }))
    const note = await within(drawer).findByTestId('cert-pinned-note')
    expect(within(note).getByTestId('cert-pinned-sha256').textContent).toContain(FP)
    expect(note.textContent).toContain('trusted by ann')
    fireEvent.click(within(note).getByTestId('cert-forget'))
    fireEvent.click(within(await screen.findByTestId('confirm')).getByTestId('confirm-ok'))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/v1/credentials/cr-1/trust')).toBe(true))
    await waitFor(() => expect(within(drawer).queryByTestId('cert-pinned-note')).toBeNull())
  })

  it('says by whom a credential in use is used, and the server’s refusal', async () => {
    removeAnswer = new Response(JSON.stringify({ error: 'github.com/ucesys is used by app 221b/k8s-console; choose another credential there first' }), { status: 409 })
    render(<CredentialsSettings />)
    fireEvent.click(await screen.findByTestId('credential-remove'))
    const dialog = await screen.findByTestId('confirm')
    expect(dialog.textContent).toContain('It is used by app 221b/k8s-console')
    fireEvent.click(within(dialog).getByTestId('confirm-ok'))
    await waitFor(() => expect(dialog.textContent).toContain('choose another credential there first'))
  })

  // CR-06: what uses a credential is a SuperAdmin's to see.
  it('shows others the names, kinds and endpoints, without Used by', async () => {
    mayEdit = false
    migration = undefined
    list = [{ id: 'cr-1', name: 'GitHub', kind: 'token', endpoint: 'https://github.com/ucesys', usedBy: [] }]
    render(<CredentialsSettings />)
    const row = await screen.findByTestId('credential-row')
    expect(row.textContent).toContain('https://github.com/ucesys')
    expect(within(row).queryByTestId('credential-used-by')).toBeNull()
    expect(screen.getByTestId('credentials-table').textContent).not.toContain('Used by')
    expect(screen.queryByTestId('credential-add')).toBeNull()
  })

  // CR-04: an OAuth2 client's token host is said beside its endpoint.
  it('says where an OAuth2 client’s secret goes besides its endpoint', async () => {
    list = [{ id: 'cr-3', name: 'API', kind: 'oauth2', endpoint: 'https://api.example.com', tokenURL: 'https://auth.example.org/token', tokenHost: 'auth.example.org', clientId: 'me', usedBy: [] }]
    render(<CredentialsSettings />)
    const row = await screen.findByTestId('credential-row')
    expect(within(row).getByTestId('credential-token-host').textContent).toBe('token from auth.example.org')
    expect(screen.getByTestId('credentials-settings').textContent).toContain('an OAuth2 client’s secret to its token URL')
  })

  // CR-02: an SSH key's host key is shown by its type, fingerprint and the
  // key cut short; a change without it keeps it, a new SSH key needs it.
  it('shows an SSH key’s host key by its fingerprint and keeps it when not given', async () => {
    migration = undefined
    list = [
      {
        id: 'cr-4',
        name: 'Deploy key',
        kind: 'ssh',
        endpoint: 'ssh://github.com/ucesys',
        set: ['privateKey'],
        hostKeyInfo: { type: 'ssh-ed25519', fingerprint: 'SHA256:abcDEF123', short: 'ssh-ed25519 AAAAC3NzaC1l…Qx7v9a2E' },
        usedBy: [],
      },
    ]
    render(<CredentialsSettings />)
    fireEvent.click(await screen.findByTestId('credential-edit'))
    const drawer = screen.getByTestId('credential-drawer')
    expect(within(drawer).getByTestId('credential-host-key-short').textContent).toBe('ssh-ed25519 AAAAC3NzaC1l…Qx7v9a2E')
    expect(within(drawer).getByTestId('credential-host-key-fingerprint').textContent).toBe('SHA256:abcDEF123')
    expect((within(drawer).getByTestId('credential-host-key') as HTMLTextAreaElement).value).toBe('')
    fireEvent.change(within(drawer).getByTestId('credential-name'), { target: { value: 'Deploy key (ro)' } })
    fireEvent.click(within(drawer).getByTestId('credential-save'))
    await waitFor(() => {
      const put = calls.find((c) => c.method === 'PUT' && c.url === '/api/v1/credentials/cr-4')?.body
      expect(put).toMatchObject({ name: 'Deploy key (ro)', kind: 'ssh' })
      expect(put).not.toHaveProperty('hostKey')
    })
  })

  it('needs the host key of a new SSH key', async () => {
    list = []
    migration = undefined
    render(<CredentialsSettings />)
    fireEvent.click(await screen.findByTestId('credential-add'))
    const drawer = screen.getByTestId('credential-drawer')
    fireEvent.change(within(drawer).getByTestId('credential-name'), { target: { value: 'Deploy key' } })
    fireEvent.change(within(drawer).getByTestId('credential-kind'), { target: { value: 'ssh' } })
    fireEvent.change(within(drawer).getByTestId('credential-endpoint'), { target: { value: 'git@github.com:ucesys' } })
    fireEvent.change(within(drawer).getByTestId('credential-secret-privateKey'), { target: { value: '-----BEGIN OPENSSH PRIVATE KEY-----' } })
    expect(within(drawer).queryByTestId('credential-host-key-kept')).toBeNull()
    expect(within(drawer).getByTestId('credential-save')).toHaveProperty('disabled', true)
    fireEvent.change(within(drawer).getByTestId('credential-host-key'), { target: { value: 'github.com ssh-ed25519 AAAA' } })
    expect(within(drawer).getByTestId('credential-save')).toHaveProperty('disabled', false)
  })

  it('says an expiry in words', () => {
    const now = Date.parse('2026-11-17T09:00:00Z')
    expect(expiryText('2026-11-10T00:00:00Z', now)?.text).toBe('Expired')
    expect(expiryText('2026-11-20T09:00:00Z', now)?.text).toBe('Expires in 3 days')
    expect(expiryText('2027-01-20T00:00:00Z', now)?.tone).toBe('')
    expect(expiryText(undefined, now)).toBeNull()
  })
})

describe('CredentialPicker', () => {
  let choices: Record<string, unknown>[] = []
  beforeEach(() => {
    vi.stubGlobal('fetch', async () => new Response(JSON.stringify({ choices })))
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('preselects the only one, makes the person choose among several, and offers none', async () => {
    choices = [{ id: 'cr-1', name: 'org', kind: 'token', endpoint: 'https://github.com/org' }]
    const seen: (string | null)[] = []
    const { rerender } = render(<CredentialPicker use="git" address="https://github.com/org/a" value={null} onChange={(v) => seen.push(v)} />)
    await waitFor(() => expect(seen).toEqual(['cr-1']))
    choices = [
      { id: 'cr-1', name: 'org', kind: 'token', endpoint: 'https://github.com/org' },
      { id: 'cr-2', name: 'repo', kind: 'token', endpoint: 'https://github.com/org/b' },
    ]
    seen.length = 0
    rerender(<CredentialPicker use="git" address="https://github.com/org/b" value={null} onChange={(v) => seen.push(v)} />)
    await waitFor(() => expect(screen.getByTestId('credential-picker-hint').textContent).toContain('2 credentials cover this address: choose one.'))
    expect(seen).toEqual([])
    const sel = screen.getByTestId('credential-picker-select') as HTMLSelectElement
    expect([...sel.options].map((o) => o.text)).toEqual(['Choose one of 2…', 'None (public)', 'org · Token · https://github.com/org', 'repo · Token · https://github.com/org/b'])
    fireEvent.change(sel, { target: { value: 'cr-2' } })
    expect(seen).toEqual(['cr-2'])
  })

  it('takes none where nothing covers the address', async () => {
    choices = []
    const seen: (string | null)[] = []
    render(<CredentialPicker use="helm" address="https://charts.example.com" value={null} onChange={(v) => seen.push(v)} />)
    await waitFor(() => expect(seen).toEqual(['']))
  })
})
