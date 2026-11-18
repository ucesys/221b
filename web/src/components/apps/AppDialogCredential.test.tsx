import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LiveContext, LiveStore } from '../../api/live'
import { AppDialog } from './AppDialog'

// The new application's credential (0.12.1, CR-10, CR-11): chosen by name
// among those that cover the repository, never typed; with several, one
// must be chosen before Install.
describe('the new application’s credential', () => {
  const calls: { method: string; url: string; body?: Record<string, unknown> }[] = []
  let choices: Record<string, unknown>[] = []
  beforeEach(() => {
    calls.length = 0
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push({ method, url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (String(url).startsWith('/api/v1/credentials/choices')) return new Response(JSON.stringify({ choices }))
      if (String(url).endsWith('/apps/inspect')) return new Response(JSON.stringify({ error: 'not now' }), { status: 502 })
      if (method === 'POST' && String(url).endsWith('/apps')) return new Response(JSON.stringify({ id: 'card-1' }), { status: 202 })
      return new Response('{}')
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  const fill = () => {
    fireEvent.change(screen.getByTestId('app-namespace'), { target: { value: 'web' } })
    fireEvent.change(screen.getByTestId('app-name'), { target: { value: 'shop' } })
    fireEvent.change(screen.getByTestId('app-repo'), { target: { value: 'https://github.com/ucesys/shop' } })
    fireEvent.change(screen.getByTestId('app-ref-text'), { target: { value: 'main' } })
  }

  it('preselects the one credential that covers the repository and sends its id, never a token', async () => {
    choices = [{ id: 'cr-org', name: 'ucesys', kind: 'token', endpoint: 'https://github.com/ucesys' }]
    render(
      <LiveContext.Provider value={new LiveStore('/test-events')}>
        <AppDialog cluster="local" mode="create" namespaces={['web']} onClose={() => {}} onDone={() => {}} />
      </LiveContext.Provider>,
    )
    expect(screen.queryByTestId('app-token')).toBeNull()
    fill()
    await waitFor(() => expect((screen.getByTestId('app-credential-select') as HTMLSelectElement).value).toBe('cr-org'))
    fireEvent.click(screen.getByTestId('app-submit'))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url.endsWith('/apps'))?.body).toMatchObject({ namespace: 'web', name: 'shop', credential: 'cr-org' }))
    expect(JSON.stringify(calls.find((c) => c.method === 'POST' && c.url.endsWith('/apps'))?.body)).not.toContain('token')
  })

  it('makes the person choose among several', async () => {
    choices = [
      { id: 'cr-org', name: 'ucesys', kind: 'token', endpoint: 'https://github.com/ucesys' },
      { id: 'cr-shop', name: 'shop deploy', kind: 'githubapp', endpoint: 'https://github.com/ucesys/shop' },
    ]
    render(
      <LiveContext.Provider value={new LiveStore('/test-events')}>
        <AppDialog cluster="local" mode="create" namespaces={['web']} onClose={() => {}} onDone={() => {}} />
      </LiveContext.Provider>,
    )
    fill()
    await waitFor(() => expect(screen.getByTestId('app-credential-hint').textContent).toContain('choose one'))
    expect(screen.getByTestId('app-submit')).toHaveProperty('disabled', true)
    fireEvent.change(screen.getByTestId('app-credential-select'), { target: { value: 'cr-shop' } })
    expect(screen.getByTestId('app-submit')).toHaveProperty('disabled', false)
    fireEvent.click(screen.getByTestId('app-submit'))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url.endsWith('/apps'))?.body).toMatchObject({ credential: 'cr-shop' }))
  })
})
