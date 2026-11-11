import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ageText, forgetObjectsForTests, objectApi, objectHref, ObjectLink, parseResource } from './ObjectLink'

describe('what a resource names (IB-06)', () => {
  it('reads namespace/kind/name and kind/name by every spelling', () => {
    expect(parseResource('shop/deployment/web')).toEqual({ kind: 'Deployment', namespace: 'shop', name: 'web', known: true })
    expect(parseResource('shop/deploy/web')).toMatchObject({ kind: 'Deployment' })
    expect(parseResource('shop/Pods/web-7f9')).toMatchObject({ kind: 'Pod', namespace: 'shop', name: 'web-7f9' })
    expect(parseResource('node/worker-1')).toEqual({ kind: 'Node', namespace: '', name: 'worker-1', known: true })
    expect(parseResource('ClusterRoleBinding/admins')).toMatchObject({ kind: 'ClusterRoleBinding', known: true })
    expect(parseResource('host/backup-1')).toEqual({ kind: 'host', namespace: '', name: 'backup-1', known: true })
    // A kind 221B does not read, or a namespaced one without its namespace.
    expect(parseResource('shop/widget/w1')).toMatchObject({ kind: 'widget', known: false })
    expect(parseResource('deployment/web')).toMatchObject({ kind: 'Deployment', known: false })
    expect(parseResource('web')).toBeNull()
    expect(parseResource('a//b')).toBeNull()
  })

  it('links each object to its page in the Kubernetes area, a host to Automation’s nodes', () => {
    const at = (r: string) => objectHref('prod', parseResource(r)!)
    expect(at('shop/pod/web-1')).toBe('#/c/prod/pods?open=shop%2Fweb-1')
    expect(at('shop/service/web')).toBe('#/c/prod/services?open=shop%2Fweb')
    expect(at('shop/deployment/web')).toBe('#/c/prod/namespaces/shop?open=Deployment%2Fweb')
    expect(at('node/worker-1')).toBe('#/c/prod/nodes?open=worker-1')
    expect(at('host/backup-1')).toBe('#/c/prod/nodes?open=backup-1')
    expect(at('namespace/shop')).toBe('#/c/prod/namespaces/shop')
    expect(at('shop/pvc/data')).toBe('#/c/prod/storage?peek=claim%3Ashop%2Fdata')
    expect(objectHref('local', parseResource('shop/pod/web-1')!)).toBe('#/pods?open=shop%2Fweb-1')
    expect(at('shop/widget/w1')).toBeNull()
  })

  it('reads each object where its drawer reads it', () => {
    const api = (r: string) => objectApi('prod', parseResource(r)!)
    expect(api('shop/pod/web-1')).toBe('/api/v1/clusters/prod/pods/shop/web-1')
    expect(api('node/worker-1')).toBe('/api/v1/clusters/prod/nodes/worker-1')
    expect(api('shop/deployment/web')).toBe('/api/v1/clusters/prod/objects/deployments/shop/web')
    expect(api('ClusterRole/edit')).toBe('/api/v1/clusters/prod/objects/clusterroles/-/edit')
    expect(api('host/backup-1')).toBeNull()
  })

  it('says how long it has been there', () => {
    const now = Date.parse('2026-11-06T12:00:00Z')
    expect(ageText('Pod', '2026-11-03T08:00:00Z', now)).toBe('running 3d 4h')
    expect(ageText('ConfigMap', '2026-11-06T11:48:00Z', now)).toBe('age 12m')
  })
})

describe('the link on the page', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    forgetObjectsForTests()
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('shows a pod’s age from when it started, read once for every link that names it', async () => {
    const started = new Date(Date.now() - (26 * 60 + 5) * 60_000).toISOString()
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ started, created: '2020-01-01T00:00:00Z' }), { status: 200 }))
    render(
      <>
        <ObjectLink cluster="prod" resource="shop/pod/web-1" />
        <ObjectLink cluster="prod" resource="shop/pod/web-1" />
      </>,
    )
    await waitFor(() => expect(screen.getAllByTestId('object-age').map((e) => e.textContent)).toEqual(['running 1d 2h', 'running 1d 2h']))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(screen.getAllByTestId('object-link')[0]!.getAttribute('data-state')).toBe('live')
  })

  it('says "gone" once the object no longer exists', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ error: 'no Deployment web in shop' }), { status: 404 }))
    render(<ObjectLink cluster="prod" resource="shop/deployment/web" />)
    expect(await screen.findByTestId('object-gone')).toBeTruthy()
    expect(screen.queryByTestId('object-age')).toBeNull()
  })

  it('shows what it cannot read as text, and reads nothing for it', () => {
    render(<ObjectLink cluster="prod" resource="shop/widget/w1" />)
    expect(screen.getByTestId('object-link').getAttribute('data-state')).toBe('text')
    expect(screen.getByTestId('object-link').querySelector('a')).toBeNull()
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
