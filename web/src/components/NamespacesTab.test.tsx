// New namespace… and New Secret… on the Namespaces tab on a cluster that
// takes no change (RB-36): hidden on a read-only connection, off with the
// reason where strict refuses the cluster, on otherwise.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { NamespaceRow } from '../api/types'
import type { SecretsConfig } from '../api/secrets'
import { parseHash } from '../lib/router'
import { NamespacesTab } from './NamespacesTab'

const web = { name: 'web', status: 'Active', created: '2026-11-01T00:00:00Z', group: 'apps', counts: {}, objects: 0, kinds: 0, pods: 0, podsNotRunning: 0, podsFailing: 0, releases: [], labels: {} } as NamespaceRow

vi.mock('../api/live', () => ({
  useView: (view: string | null) => ({ rows: view === 'namespaces' ? [web] : [], usage: new Map(), version: 1, loaded: true }),
  useLiveMeta: () => ({ settings: {} }),
}))

let config: SecretsConfig | null = null
vi.mock('../api/secrets', async (original) => ({
  ...(await original<typeof import('../api/secrets')>()),
  useSecretsConfig: () => config,
}))

const route = parseHash('#/namespaces/web')
const show = (c: SecretsConfig) => {
  config = c
  render(<NamespacesTab cluster={null} now={Date.parse('2026-11-24T00:00:00Z')} onToast={vi.fn()} route={route} />)
}
const readOnly = 'Here is connected read-only, so the console changes nothing there.'
const strict = 'Changes are refused on this cluster because rbac.enforce is strict.'

describe('NamespacesTab', () => {
  afterEach(cleanup)

  it('hides New namespace… and New Secret… on a read-only connection', () => {
    show({ read: true, reveal: false, edit: true, readOnly: true, refused: readOnly })
    expect(screen.queryByTestId('namespace-new')).toBeNull()
    expect(screen.queryByTestId('secret-new')).toBeNull()
  })

  it('turns them off with the reason where strict refuses the cluster', () => {
    show({ read: true, reveal: false, edit: true, refused: strict })
    for (const id of ['namespace-new', 'secret-new']) {
      expect((screen.getByTestId(id) as HTMLButtonElement).disabled).toBe(true)
      expect(screen.getByTestId(`${id}-off`).textContent).toBe(strict)
    }
  })

  it('offers them on a connection that takes changes', () => {
    show({ read: true, reveal: false, edit: true })
    for (const id of ['namespace-new', 'secret-new']) {
      expect((screen.getByTestId(id) as HTMLButtonElement).disabled).toBe(false)
      expect(screen.queryByTestId(`${id}-off`)).toBeNull()
    }
  })
})
