import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as clustersApi from '../api/clusters'
import type { ClusterSummary } from '../api/clusters'
import { ClusterEditor, formProblem, toDraft } from './ClusterEditor'

vi.mock('../api/clusters', async (original) => ({ ...(await original<typeof import('../api/clusters')>()), saveCluster: vi.fn() }))

const orig: ClusterSummary = {
  id: 'analytics-dev',
  name: 'analytics-dev',
  env: 'development',
  server: 'https://10.0.0.8:6443',
  ca: 'system',
  auth: 'token',
  mode: 'read',
  cred: { set: '2026-11-20T09:00:00Z' },
  added: '2026-11-20T09:00:00Z',
  updated: '2026-11-20T09:00:00Z',
  state: 'connected',
  nodes: 1,
  pods: 2,
  synced: true,
  rowVersion: 4,
}

beforeEach(() => {
  // jsdom has no scrollTo on elements; the drawer scrolls its body to the top.
  Element.prototype.scrollTo = () => {}
})

afterEach(() => {
  cleanup()
  vi.mocked(clustersApi.saveCluster).mockReset()
})

describe('the connection editor holds an edit to the version it opened (DB-92)', () => {
  it('gives back the version it read', () => {
    const d = toDraft({ ...toDraftForm(), name: 'analytics-dev' }, orig)
    expect(d.rowVersion).toBe(4)
    expect(toDraft({ ...toDraftForm(), name: 'new-one' }, null).rowVersion).toBeUndefined()
  })

  it('says the connection was changed by someone else when the save is refused with 409', async () => {
    vi.mocked(clustersApi.saveCluster).mockRejectedValue(new clustersApi.ClusterError('conflict', 409))
    const onSaved = vi.fn()
    const { rerender } = render(<ClusterEditor orig={orig} now={Date.parse('2026-11-20T10:00:00Z')} onClose={() => undefined} onSaved={onSaved} onRemoved={() => undefined} />)
    // The list is read again while the editor is open, and shows what someone else saved.
    rerender(<ClusterEditor orig={{ ...orig, rowVersion: 5, description: 'theirs' }} now={Date.parse('2026-11-20T10:00:00Z')} onClose={() => undefined} onSaved={onSaved} onRemoved={() => undefined} />)
    fireEvent.click(screen.getByTestId('cluster-save'))
    await waitFor(() => expect(screen.getByTestId('cluster-error').textContent).toBe(clustersApi.CONNECTION_CHANGED))
    expect(vi.mocked(clustersApi.saveCluster).mock.calls[0]![0].rowVersion).toBe(4)
    expect(onSaved).not.toHaveBeenCalled()
  })
})

// Clusters review, finding 2: a server change kept the saved credentials, which the console then sent to the new server.
describe('the connection editor asks for the credentials again for another API server', () => {
  it('asks for them when the scheme, host or port changes', () => {
    const moved = { ...toDraftForm(), name: 'analytics-dev', server: 'https://attacker.example:443' }
    expect(formProblem(moved, orig)).toEqual({ field: 'token', msg: 'Paste the service account token.' })
    expect(toDraft({ ...moved, token: 'eyJ.a.b' }, orig).token).toBe('eyJ.a.b')
  })

  it('keeps them for the same server', () => {
    expect(formProblem({ ...toDraftForm(), name: 'analytics-dev', server: 'https://10.0.0.8:6443/' }, orig)).toBeNull()
  })
})

// A form as the editor starts one for a new connection.
function toDraftForm() {
  return {
    name: '',
    env: 'development' as const,
    description: '',
    server: 'https://10.0.0.8:6443',
    ca: 'system' as const,
    auth: 'token' as const,
    mode: 'read' as const,
    caPem: '',
    token: '',
    kubeconfig: '',
    context: '',
    certPem: '',
    keyPem: '',
    issuer: '',
    clientID: '',
    clientSecret: '',
    scopes: '',
    caAsk: false,
    credAsk: false,
  }
}
