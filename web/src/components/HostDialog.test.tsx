import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { addHost, checkHost, PairError, pairHost, parseHosts, parseLabels, scanHost, setupCommand, trustHost } from '../api/hosts'
import { HostDialog } from './HostDialog'

vi.mock('../api/hosts', async (orig) => {
  const real = await orig<typeof import('../api/hosts')>()
  return {
    ...real,
    fetchHosts: vi.fn(async () => ({ hosts: [], loginUser: 'root', port: 22 })),
    addHost: vi.fn(async (_c: string, h: { name: string; address: string; port: number; loginUser: string }) => ({ ...h, state: 'pending', addedAt: '2026-11-04T12:00:00Z' })),
    scanHost: vi.fn(async () => ({ fingerprint: 'SHA256:abc123', keyType: 'ssh-ed25519', command: 'ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub' })),
    trustHost: vi.fn(async () => ({ name: 'backup-1', state: 'pending' })),
    pairHost: vi.fn(),
    setupCommand: vi.fn(async () => ({ command: "sudo sh <<'221B-SETUP'\nset -e\n221B-SETUP\n" })),
    checkHost: vi.fn(async () => ({ name: 'backup-1', loginUser: 'root', state: 'paired' })),
  }
})

const PASSWORD = 'Tr0ub4dor-&3'
const SUDO = 'sudo-pw-9'

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const type = (id: string, value: string) => fireEvent.change(screen.getByTestId(id), { target: { value } })

describe('adding a host', () => {
  it('registers it, reads its key with no credential, and trusts only the fingerprint it showed', async () => {
    render(<HostDialog cluster="local" onClose={() => undefined} />)
    expect(screen.getByTestId('host-dialog').dataset.step).toBe('details')
    type('host-list', 'backup-1=10.0.0.9')
    type('host-labels', 'role=backup, site=fra')
    // The setup command by hand: no credential to give.
    fireEvent.click(screen.getByTestId('host-mode-command'))
    expect(screen.queryByTestId('host-login')).toBeNull()
    fireEvent.click(screen.getByTestId('host-next'))
    await waitFor(() => expect(screen.getByTestId('host-fingerprint').textContent).toBe('SHA256:abc123'))
    expect(addHost).toHaveBeenCalledWith('local', { name: 'backup-1', address: '10.0.0.9', port: 22, loginUser: 'root', labels: { role: 'backup', site: 'fra' } })
    expect(scanHost).toHaveBeenCalledWith('local', 'backup-1')
    expect(screen.getByTestId('host-keygen').textContent).toContain('ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub')
    expect(screen.queryByTestId('host-cred-password')).toBeNull()
    expect(screen.getByTestId('host-trust').textContent).toBe('Trust this key')
    fireEvent.click(screen.getByTestId('host-trust'))
    await waitFor(() => expect(screen.getByTestId('host-dialog').dataset.step).toBe('pair'))
    expect(trustHost).toHaveBeenCalledWith('local', 'backup-1', 'SHA256:abc123')
    expect(pairHost).not.toHaveBeenCalled()
  })

  it('says what is missing before it registers', () => {
    render(<HostDialog cluster="local" onClose={() => undefined} />)
    expect((screen.getByTestId('host-next') as HTMLButtonElement).disabled).toBe(true)
    type('host-list', 'Backup_1')
    expect((screen.getByTestId('host-next') as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByTestId('host-problem').textContent).toContain('write name=address')
    type('host-list', 'backup-1=10.0.0.9')
    type('host-labels', 'role')
    expect((screen.getByTestId('host-next') as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByTestId('host-dialog').textContent).toContain('“role” is not key=value.')
  })
})

describe('pairing a host', () => {
  const host = { name: 'backup-1', address: '10.0.0.9', port: 22 }

  it('sends the credential once and empties its fields at once, whatever the answer', async () => {
    vi.mocked(pairHost).mockRejectedValueOnce(new PairError('Sudo refused the password: check the sudo password, or run the setup command yourself.', ['created the login user ops']))
    render(<HostDialog cluster="local" host={host} start="pair" onClose={() => undefined} />)
    type('host-cred-user', 'admin')
    type('host-cred-password', PASSWORD)
    type('host-cred-sudo', SUDO)
    fireEvent.click(screen.getByTestId('host-pair'))
    expect(pairHost).toHaveBeenCalledWith('local', 'backup-1', { user: 'admin', password: PASSWORD, sudoPassword: SUDO })
    expect((screen.getByTestId('host-cred-password') as HTMLInputElement).value).toBe('')
    expect((screen.getByTestId('host-cred-sudo') as HTMLInputElement).value).toBe('')
    await waitFor(() => expect(screen.getByTestId('host-error').textContent).toContain('Sudo refused the password'))
    expect(screen.getByTestId('host-steps').textContent).toContain('created the login user ops')
    expect(document.body.innerHTML).not.toContain(PASSWORD)
    expect(document.body.innerHTML).not.toContain(SUDO)
  })

  it('pairs with a private key and its passphrase, then says it is done', async () => {
    vi.mocked(pairHost).mockResolvedValueOnce({ host: { name: 'backup-1', address: '10.0.0.9', port: 22, loginUser: 'root', state: 'paired', addedAt: '' }, steps: [] })
    const onDone = vi.fn()
    const onClose = vi.fn()
    render(<HostDialog cluster="local" host={host} start="pair" onClose={onClose} onDone={onDone} />)
    type('host-cred-user', 'admin')
    fireEvent.click(screen.getByTestId('host-cred-how-key'))
    type('host-cred-key', '-----BEGIN OPENSSH PRIVATE KEY-----\nxyz\n-----END OPENSSH PRIVATE KEY-----')
    type('host-cred-passphrase', 'pp')
    fireEvent.click(screen.getByTestId('host-pair'))
    expect(pairHost).toHaveBeenCalledWith('local', 'backup-1', { user: 'admin', privateKey: '-----BEGIN OPENSSH PRIVATE KEY-----\nxyz\n-----END OPENSSH PRIVATE KEY-----', passphrase: 'pp' })
    await waitFor(() => expect(screen.getByTestId('host-done').textContent).toBe('backup-1 is paired. Automation reaches it as a node, as root.'))
    fireEvent.click(screen.getByTestId('host-close'))
    expect(onDone).toHaveBeenCalledWith('backup-1 is paired. Automation reaches it as a node, as root.')
    expect(onClose).toHaveBeenCalled()
  })

  it('offers the setup command to run by hand, then checks the connection', async () => {
    render(<HostDialog cluster="local" host={host} start="pair" onClose={() => undefined} />)
    fireEvent.click(screen.getByTestId('host-mode-command'))
    await waitFor(() => expect(screen.getByTestId('host-setup-command').textContent).toContain("sudo sh <<'221B-SETUP'"))
    expect(setupCommand).toHaveBeenCalledWith('local', 'backup-1')
    fireEvent.click(screen.getByTestId('host-check'))
    await waitFor(() => expect(screen.getByTestId('host-done')).toBeTruthy())
    expect(checkHost).toHaveBeenCalledWith('local', 'backup-1')
    expect(pairHost).not.toHaveBeenCalled()
  })

  it('reads the key again for a host whose key changed', async () => {
    render(<HostDialog cluster="local" host={host} start="key" onClose={() => undefined} />)
    await waitFor(() => expect(screen.getByTestId('host-fingerprint')).toBeTruthy())
    fireEvent.click(screen.getByTestId('host-rescan'))
    await waitFor(() => expect(scanHost).toHaveBeenCalledTimes(2))
  })
})

describe('labels', () => {
  it('are key=value by commas or lines', () => {
    expect(parseLabels('a=1, b=2\nc=')).toEqual({ a: '1', b: '2', c: '' })
    expect(parseLabels('')).toEqual({})
    expect(parseLabels('a=1,a=2')).toBe('a is given twice.')
    expect(parseLabels('=x')).toBe('“=x” is not key=value.')
  })
})

describe('adding several hosts at once', () => {
  it('reads them by spaces, tabs, commas, semicolons and lines, each named after its address or as name=address', () => {
    expect(parseHosts('db1 db2,db3;\tDB4.example.com\nweb=10.0.0.7')).toEqual([
      { name: 'db1', address: 'db1' },
      { name: 'db2', address: 'db2' },
      { name: 'db3', address: 'db3' },
      { name: 'db4.example.com', address: 'DB4.example.com' },
      { name: 'web', address: '10.0.0.7' },
    ])
    expect(parseHosts(' ,; ')).toEqual([])
    expect(parseHosts('db1 db1')).toBe('db1 is in the list twice.')
    expect(parseHosts('fd00::1')).toContain('write name=address')
    expect(parseHosts('=10.0.0.1')).toContain('neither an address nor name=address')
  })

  it('adds each, reads each key, trusts the ticked ones, and pairs them in turn, a failure kept in its row to try again', async () => {
    vi.mocked(addHost).mockImplementation(async (_c, h) => {
      if (h.name === 'db3') throw new Error('db3 is the name of a Kubernetes node of this cluster')
      return { ...h, state: 'pending', addedAt: '2026-11-05T12:00:00Z' }
    })
    vi.mocked(scanHost).mockImplementation(async (_c, name) => ({ fingerprint: `SHA256:${name}`, keyType: 'ssh-ed25519', command: 'ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub' }))
    let tries = 0
    vi.mocked(pairHost).mockImplementation(async (_c, name) => {
      if (name === 'db2' && tries++ === 0) throw new PairError('Sudo refused the password.', ['sudo: 1 incorrect password attempt'])
      return { host: { name, address: name, port: 22, loginUser: 'root', state: 'paired', addedAt: '' }, steps: [] }
    })
    const onDone = vi.fn()
    render(<HostDialog cluster="local" onClose={() => undefined} onDone={onDone} />)
    type('host-list', 'db1 db2,db3;\tweb=10.0.0.7')
    // The account comes with the hosts, and is sent only once their keys are trusted.
    expect((screen.getByTestId('host-next') as HTMLButtonElement).disabled).toBe(true)
    type('host-cred-user', 'ops')
    type('host-cred-password', PASSWORD)
    expect(screen.getByTestId('host-list-count').textContent).toBe('4 hosts: db1, db2, db3, web (10.0.0.7).')
    expect(screen.getByTestId('host-next').textContent).toBe('Add 4 hosts and read their keys')
    fireEvent.click(screen.getByTestId('host-next'))
    await waitFor(() => expect(screen.getAllByTestId('host-fingerprint')).toHaveLength(3))
    expect(addHost).toHaveBeenCalledTimes(4)
    expect(screen.getByTestId('host-refused').textContent).toContain('db3: db3 is the name of a Kubernetes node')
    const rows = screen.getAllByTestId('host-row')
    expect(rows.map((r) => r.dataset.host)).toEqual(['db1', 'db2', 'web'])
    // The key of web did not match: it stays pending.
    fireEvent.click(rows[2]!.querySelector('[data-testid=host-tick]')!)
    expect(pairHost).not.toHaveBeenCalled()
    expect(screen.getByTestId('host-trust').textContent).toBe('Trust 2 keys and pair')
    fireEvent.click(screen.getByTestId('host-trust'))
    await waitFor(() => expect(screen.getByTestId('host-dialog').dataset.step).toBe('pair'))
    expect(vi.mocked(trustHost).mock.calls.map((c) => [c[1], c[2]])).toEqual([
      ['db1', 'SHA256:db1'],
      ['db2', 'SHA256:db2'],
    ])
    await waitFor(() => expect(screen.getByTestId('host-row-error').textContent).toContain('Sudo refused the password.'))
    expect((screen.getByTestId('host-cred-password') as HTMLInputElement).value).toBe('')
    expect(screen.getAllByTestId('host-row-state').map((s) => s.textContent)).toEqual(['paired, as root', 'to pair', 'key not trusted: left pending'])
    // Again, for db2 alone.
    type('host-cred-password', PASSWORD)
    expect(screen.getByTestId('host-pair').textContent).toBe('Pair')
    fireEvent.click(screen.getByTestId('host-pair'))
    await waitFor(() => expect(screen.getByTestId('host-done').textContent).toBe('2 of 3 hosts are paired. Automation reaches them as nodes, as root. 1 waits in its row on the Nodes tab, to take further from there.'))
    expect(vi.mocked(pairHost).mock.calls.map((c) => c[1])).toEqual(['db1', 'db2', 'db2'])
    expect(document.body.textContent).not.toContain(PASSWORD)
    fireEvent.click(screen.getByTestId('host-close'))
    expect(onDone).toHaveBeenCalledWith(expect.stringContaining('2 of 3 hosts are paired'))
  })
})

describe('a host added under its address', () => {
  it('takes its hostname as it pairs, and the dialog says so', async () => {
    vi.mocked(addHost).mockImplementation(async (_c, h) => ({ ...h, state: 'pending', addedAt: '2026-11-05T12:00:00Z' }))
    vi.mocked(scanHost).mockImplementation(async () => ({ fingerprint: 'SHA256:x', keyType: 'ssh-ed25519', command: 'ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub' }))
    vi.mocked(pairHost).mockImplementation(async () => ({ host: { name: 'db-primary', address: '10.0.0.5', port: 22, loginUser: 'root', state: 'paired', addedAt: '' }, steps: [] }))
    render(<HostDialog cluster="local" onClose={() => undefined} />)
    type('host-list', '10.0.0.5')
    type('host-cred-user', 'ops')
    type('host-cred-password', PASSWORD)
    fireEvent.click(screen.getByTestId('host-next'))
    await waitFor(() => expect(screen.getByTestId('host-fingerprint')).toBeTruthy())
    fireEvent.click(screen.getByTestId('host-trust'))
    await waitFor(() => expect(screen.getByTestId('host-done').textContent).toBe('db-primary is paired, named after its hostname (added as 10.0.0.5). Automation reaches it as a node, as root.'))
  })
})
