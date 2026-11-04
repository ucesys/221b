import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useSecretsConfig } from './secrets'

const answer = (cfg: object) => new Response(JSON.stringify(cfg), { status: 200, headers: { 'Content-Type': 'application/json' } })

describe('useSecretsConfig', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('reads each cluster’s own, and a late answer from the last one is dropped', async () => {
    const asked: Record<string, (r: Response) => void> = {}
    vi.stubGlobal('fetch', (url: string) => new Promise<Response>((resolve) => (asked[url] = resolve)))
    const on = { read: true, reveal: true, edit: true }
    const off = { read: false, reveal: false, edit: false }
    const { result, rerender } = renderHook(({ cluster }) => useSecretsConfig(cluster), { initialProps: { cluster: 'a' } })
    expect(result.current).toBeNull()
    await act(async () => asked['/api/v1/clusters/a/secrets/config']!(answer(on)))
    await waitFor(() => expect(result.current).toEqual(on))
    // Another cluster: nothing until it answers, not the last one's.
    rerender({ cluster: 'b' })
    expect(result.current).toBeNull()
    rerender({ cluster: 'a' })
    rerender({ cluster: 'b' })
    await act(async () => asked['/api/v1/clusters/b/secrets/config']!(answer(off)))
    await waitFor(() => expect(result.current).toEqual(off))
    await act(async () => asked['/api/v1/clusters/a/secrets/config']!(answer(on)))
    expect(result.current).toEqual(off)
  })
})
