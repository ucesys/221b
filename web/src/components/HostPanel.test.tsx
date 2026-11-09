import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanupCommand, fetchHosts, removeHost } from '../api/hosts'
import { row } from '../test/rows'
import { NodePanel } from './NodePanel'

vi.mock('../api/hosts', async (orig) => ({
  ...(await orig<typeof import('../api/hosts')>()),
  fetchHosts: vi.fn(async () => ({ hosts: [], loginUser: 'root', port: 22 })),
  cleanupCommand: vi.fn(async () => ({ command: "sudo sh <<'221B-CLEANUP'\n221B-UNPAIRED\n221B-CLEANUP\n" })),
  removeHost: vi.fn(async () => ({ cleanup: '' })),
}))
vi.mock('../api/automation', async (orig) => ({
  ...(await orig<typeof import('../api/automation')>()),
  fetchFacts: vi.fn(async () => ({ nodes: [], keys: [] })),
  fetchFleets: vi.fn(async () => ({ cluster: 'local', production: false, fleets: [], conflicts: [] })),
}))

const now = Date.parse('2026-11-04T12:00:00Z')
const host = (over: Parameters<typeof row>[1] = {}) =>
  row('backup-1', {
    role: 'host',
    status: 'Paired',
    tone: 'ok',
    statusTip: 'Reached over SSH at 10.0.0.9:22 as root.',
    labels: { role: 'backup' },
    host: { address: '10.0.0.9', port: 22, loginUser: 'root', state: 'paired', fingerprint: 'SHA256:abc', trustedBy: 'Pat', trustedAt: '2026-11-04T11:00:00Z' },
    ...over,
  })

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('the details of a host', () => {
  it('say how the console reaches it and the fingerprint it pinned, with no Kubernetes tools', async () => {
    render(<NodePanel r={host()} metrics={false} metricsNote="" uptimeNote="" now={now} onClose={() => undefined} />)
    const panel = screen.getByTestId('node-panel')
    expect(panel.dataset.host).toBe('true')
    const conn = screen.getByTestId('host-connection').textContent
    expect(conn).toContain('10.0.0.9:22')
    expect(conn).toContain('SHA256:abc')
    expect(screen.queryByText('YAML')).toBeNull()
    expect(screen.queryByTestId('node-pods-link')).toBeNull()
    await waitFor(() => expect(fetchHosts).toHaveBeenCalled())
  })

  it('offer to check a changed key again', async () => {
    render(<NodePanel r={host({ status: 'Key changed', tone: 'err', host: { address: '10.0.0.9', port: 22, loginUser: 'root', state: 'paired', fingerprint: 'SHA256:abc', keyChanged: true } })} metrics={false} metricsNote="" uptimeNote="" now={now} onClose={() => undefined} />)
    expect((await screen.findByTestId('host-next-step')).textContent).toBe('Check the key again…')
  })

  it('show the cleanup command before the host is removed', async () => {
    const onClose = vi.fn()
    render(<NodePanel r={host()} metrics={false} metricsNote="" uptimeNote="" now={now} onClose={onClose} />)
    fireEvent.click(await screen.findByTestId('host-remove'))
    await waitFor(() => expect(screen.getByTestId('host-cleanup').textContent).toContain('221B-CLEANUP'))
    expect(cleanupCommand).toHaveBeenCalledWith('local', 'backup-1')
    expect(removeHost).not.toHaveBeenCalled()
    fireEvent.click(screen.getByTestId('confirm-ok'))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(removeHost).toHaveBeenCalledWith('local', 'backup-1')
  })
})
