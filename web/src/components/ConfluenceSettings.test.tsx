import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ConfluenceView } from '../api/knowledge'
import { ConfluenceSettings } from './ConfluenceSettings'

const view: ConfluenceView = {
  settings: { on: true, baseURL: 'https://example.atlassian.net', flavor: 'cloud', email: 'pub@example.com', space: 'STATUS', root: '', wiki: false },
  token: true,
  outbox: 2,
  failing: 1,
  lastError: 'ki:KI-3: Confluence answered 503',
  lastCheck: '2026-11-05T03:30:00Z',
  checkReport: ['KI-3’s page was edited in its managed part: its owner is asked to keep or replace the edit'],
  conflicts: 1,
  pages: 4,
  wikiPages: 0,
}

describe('Settings › Issues › Confluence', () => {
  const calls: { method: string; url: string; body?: Record<string, unknown> }[] = []
  beforeEach(() => {
    calls.length = 0
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (String(url).endsWith('/test')) return new Response(JSON.stringify({ ok: true, spaces: ['STATUS: Status'] }))
      return new Response(JSON.stringify(view))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('shows the outbox and the check, and saves the wiki tree with a new token', async () => {
    render(<ConfluenceSettings open />)
    expect((await screen.findByTestId('confluence-state')).textContent).toContain('2 waiting, 1 being tried again')
    expect(screen.getByTestId('confluence-report').textContent).toContain('managed part')
    expect(screen.getByTestId('confluence-token-state').textContent).toContain('never shown again')
    fireEvent.click(screen.getByTestId('confluence-test'))
    expect((await screen.findByTestId('confluence-note')).textContent).toContain('STATUS: Status')
    fireEvent.click(screen.getByTestId('confluence-wiki'))
    fireEvent.change(screen.getByTestId('confluence-wikiSpace'), { target: { value: 'INFRA' } })
    fireEvent.click(screen.getByText('Replace…'))
    fireEvent.change(screen.getByTestId('confluence-token'), { target: { value: 'write-token' } })
    fireEvent.click(screen.getByTestId('confluence-save'))
    await waitFor(() => {
      const put = calls.find((c) => c.method === 'PUT')
      expect(put?.url).toBe('/api/v1/knowledge/confluence')
      expect(put?.body).toMatchObject({ settings: { on: true, space: 'STATUS', wiki: true, wikiSpace: 'INFRA' }, token: 'write-token' })
    })
  })
})
