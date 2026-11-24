import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { StarPrompt, starClosedKey } from './StarPrompt'

// Star us on GitHub (CU-21): the dialog the server says to show, once a
// session; Never show this again turns the setting off.

let calls: { url: string; method: string }[] = []
function stub(answer: unknown) {
  calls = []
  vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
    calls.push({ url: String(url), method: init?.method ?? 'GET' })
    return new Response(JSON.stringify(String(url).endsWith('/never') ? { show: false } : answer))
  })
}

describe('Star us on GitHub', () => {
  beforeEach(() => window.localStorage.clear())
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('asks with a link to the repository in a new tab, and closed it stays closed for the session', async () => {
    stub({ show: true, session: 's1' })
    render(<StarPrompt />)
    const link = await screen.findByTestId('star-prompt-link')
    expect(link.getAttribute('href')).toBe('https://github.com/ucesys/221b')
    expect(link.getAttribute('target')).toBe('_blank')
    expect(link.getAttribute('rel')).toContain('noopener')
    expect((screen.getByTestId('star-prompt-never') as HTMLInputElement).checked).toBe(false)
    fireEvent.click(screen.getByText('Close'))
    await waitFor(() => expect(screen.queryByTestId('star-prompt')).toBeNull())
    expect(calls.map((c) => c.url)).toEqual(['/api/v1/star-prompt'])
    expect(window.localStorage.getItem(starClosedKey('s1'))).toBe('true')
    // A page load in the same session: not again.
    cleanup()
    render(<StarPrompt />)
    await waitFor(() => expect(calls).toHaveLength(2))
    expect(screen.queryByTestId('star-prompt')).toBeNull()
    // The next sign-in is another session: asked again.
    cleanup()
    stub({ show: true, session: 's2' })
    render(<StarPrompt />)
    expect(await screen.findByTestId('star-prompt')).toBeTruthy()
  })

  it('turns the setting off with Never show this again', async () => {
    stub({ show: true, session: 's1' })
    render(<StarPrompt />)
    fireEvent.click(await screen.findByTestId('star-prompt-never'))
    fireEvent.click(screen.getByText('Close'))
    await waitFor(() => expect(screen.queryByTestId('star-prompt')).toBeNull())
    expect(calls.at(-1)).toEqual({ url: '/api/v1/star-prompt/never', method: 'POST' })
  })

  it('shows nothing when the server does not ask', async () => {
    stub({ show: false })
    render(<StarPrompt />)
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(screen.queryByTestId('star-prompt')).toBeNull()
  })
})
