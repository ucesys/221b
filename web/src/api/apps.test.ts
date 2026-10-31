import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useApps, type AppView } from './apps'

const app = (name: string) => ({ namespace: 'web', name }) as AppView

describe('useApps', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  function reads() {
    const asked: ((names: string[]) => Promise<void>)[] = []
    vi.stubGlobal(
      'fetch',
      () =>
        new Promise<Response>((resolve) => {
          asked.push(async (names) => {
            await act(async () => resolve(new Response(JSON.stringify(names.map(app)), { status: 200, headers: { 'Content-Type': 'application/json' } })))
          })
        }),
    )
    return asked
  }

  it('shows nothing of the last cluster once the cluster changes', async () => {
    const asked = reads()
    const { result, rerender } = renderHook(({ cluster }) => useApps(cluster, 0, 0), { initialProps: { cluster: 'a' } })
    await asked[0]!(['from-a'])
    await waitFor(() => expect(result.current.apps.map((x) => x.name)).toEqual(['from-a']))
    rerender({ cluster: 'b' })
    expect(result.current.apps).toEqual([])
    await asked[1]!(['from-b'])
    await waitFor(() => expect(result.current.apps.map((x) => x.name)).toEqual(['from-b']))
  })

  it('drops a slow answer from the cluster it left', async () => {
    const asked = reads()
    const { result, rerender } = renderHook(({ cluster }) => useApps(cluster, 0, 0), { initialProps: { cluster: 'a' } })
    rerender({ cluster: 'b' })
    await asked[1]!(['from-b'])
    await waitFor(() => expect(result.current.apps.map((x) => x.name)).toEqual(['from-b']))
    await asked[0]!(['from-a'])
    expect(result.current.apps.map((x) => x.name)).toEqual(['from-b'])
  })

  it('takes an answer that overtook none, even while newer reads are out', async () => {
    const asked = reads()
    const { result, rerender } = renderHook(({ tick }) => useApps('a', tick, 0), { initialProps: { tick: 0 } })
    rerender({ tick: 1 })
    await asked[0]!(['first'])
    await waitFor(() => expect(result.current.apps.map((x) => x.name)).toEqual(['first']))
  })
})
