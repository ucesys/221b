import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AIOverview } from '../../api/ai'
import { idFromName, providerFromForm, ProvidersSection } from './ProvidersSection'

function overview(): AIOverview {
  return {
    providers: [
      // The default provider as an upgrade from 0.11 has it: no kind saved.
      {
        id: 'default',
        name: 'Anthropic',
        kind: '' as 'anthropic',
        model: 'claude-opus-5-5',
        resolved: { kind: 'anthropic', auth: 'header' },
        default: true,
        locked: false,
        needsKey: true,
        key: { set: false, store: 'secret' },
        usedBy: ['Sherlock'],
      },
    ],
    agents: [],
    models: [{ id: 'claude-opus-5-5', label: 'Claude Opus 5.5', in: 4, out: 20 }],
    defaultModel: 'claude-opus-5-5',
    dailyUSD: 20,
    monthlyUSD: 200,
    spend: { day: '', dayUSD: 0, month: '', monthUSD: 0 },
    singleProvider: false,
    viewControl: true,
    clusters: [{ id: 'local', name: 'Here', env: '' }],
    running: 0,
    knowledge: '',
  }
}

describe('the Providers section', () => {
  const calls: { method: string; url: string; body?: unknown }[] = []
  beforeEach(() => {
    Element.prototype.scrollTo = () => {}
    calls.length = 0
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      return new Response(JSON.stringify(overview()))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('edits the default provider, kind saved or not, into a gateway', async () => {
    render(<ProvidersSection />)
    const card = await screen.findByTestId('provider')
    expect(within(card).getByTestId('provider-key-pill').textContent).toBe('No key')
    fireEvent.click(within(card).getByTestId('provider-edit'))
    const drawer = screen.getByTestId('provider-drawer')
    fireEvent.change(within(drawer).getByTestId('provider-kind'), { target: { value: 'compatible' } })
    fireEvent.change(within(drawer).getByTestId('provider-endpoint'), { target: { value: 'https://gw.example/v1' } })
    fireEvent.change(within(drawer).getByTestId('provider-model'), { target: { value: 'gpt-5' } })
    fireEvent.click(within(drawer).getByTestId('provider-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')).toMatchObject({ url: '/api/v1/ai/providers/default', body: { id: 'default', kind: 'openai', endpoint: 'https://gw.example/v1', model: 'gpt-5' } }))
  })

  it('saves a key on the card, never showing it again', async () => {
    render(<ProvidersSection />)
    const card = await screen.findByTestId('provider')
    fireEvent.change(within(card).getByTestId('provider-key'), { target: { value: 'sk-test-0123456789' } })
    fireEvent.click(within(card).getByTestId('provider-key-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')).toMatchObject({ url: '/api/v1/ai/providers/default/key', body: { key: 'sk-test-0123456789' } }))
    await waitFor(() => expect((within(card).getByTestId('provider-key') as HTMLInputElement).value).toBe(''))
  })

  it('lets Remove be pressed on a provider in use, and shows why the server refused (KI-2)', async () => {
    const why = 'Anthropic cannot be removed: Sherlock uses it. Move it to another provider first.'
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url: String(url) })
      if (init?.method === 'DELETE') return new Response(JSON.stringify({ error: why }), { status: 400 })
      return new Response(JSON.stringify(overview()))
    })
    render(<ProvidersSection />)
    fireEvent.click(within(await screen.findByTestId('provider')).getByTestId('provider-edit'))
    const drawer = screen.getByTestId('provider-drawer')
    // The default has no Make default of its own.
    expect(within(drawer).queryByTestId('provider-make-default')).toBeNull()
    const remove = within(drawer).getByTestId('provider-remove') as HTMLButtonElement
    expect(remove.disabled).toBe(false)
    fireEvent.click(remove)
    await waitFor(() => expect(within(drawer).getByTestId('provider-error').textContent).toBe(why))
    expect(calls.find((c) => c.method === 'DELETE')?.url).toBe('/api/v1/ai/providers/default')
  })

  it('makes another provider the default', async () => {
    const two = overview()
    two.providers.push({ ...two.providers[0]!, id: 'gw', name: 'Gateway', kind: 'openai', default: false, usedBy: [] })
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url: String(url) })
      return new Response(JSON.stringify(two))
    })
    render(<ProvidersSection />)
    const cards = await screen.findAllByTestId('provider')
    fireEvent.click(within(cards[1]!).getByTestId('provider-edit'))
    fireEvent.click(within(screen.getByTestId('provider-drawer')).getByTestId('provider-make-default'))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/ai/providers/gw/default')).toBe(true))
  })

  it('makes an id from a name and keeps only what was given', () => {
    expect(idFromName('Second gateway!')).toBe('second-gateway')
    expect(idFromName('42 Corp AI')).toBe('corp-ai')
    expect(providerFromForm({ id: 'gw', name: 'GW', kind: 'openai', endpoint: '', headersText: 'X-Team: a', priceIn: 0 })).toEqual({ id: 'gw', name: 'GW', kind: 'openai', headers: { 'X-Team': 'a' } })
  })
})

// --- AI settings, round 2 (Phase 12, wave 5) ---
describe('a provider without a credential, tested in its dialog', () => {
  const calls: { method: string; url: string; body?: unknown }[] = []
  const a5000 = {
    id: 'a5000',
    name: 'A5000',
    kind: 'openai' as const,
    endpoint: 'http://a5000.lan:8000/v1',
    model: 'qwen3.8-27b',
    resolved: { kind: 'openai' as const, auth: 'header' as const },
    default: false,
    locked: false,
    needsKey: true,
    key: { set: false, store: 'secret' as const },
    usedBy: [],
  }
  let tested = false
  beforeEach(() => {
    Element.prototype.scrollTo = () => {}
    calls.length = 0
    tested = false
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const u = String(url)
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      calls.push({ method: init?.method ?? 'GET', url: u, body })
      if (u.endsWith('/providers/try')) return new Response(JSON.stringify({ ok: true, model: body.model, ms: 800, answer: 'I can hear the console.', keyless: true }))
      if (u.endsWith('/models'))
        return new Response(
          JSON.stringify({
            models: [
              { id: 'qwen3.8-27b', contextWindow: 16384 },
              { id: 'llama-4-scout-17b', contextWindow: 8192 },
            ],
          }),
        )
      if (u.endsWith('/a5000/test')) {
        tested = true
        return new Response(JSON.stringify({ ok: true, model: 'qwen3.8-27b', ms: 700, answer: 'Heard.', keyless: true }))
      }
      const ov = overview()
      ov.providers.push(tested ? { ...a5000, keylessOK: true, tested: { at: '2026-11-06T10:00:00Z', ok: true, model: 'qwen3.8-27b', keyless: true }, contextWindow: 16384 } : a5000)
      return new Response(JSON.stringify(ov))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('tests the settings before saving, fills the model from the endpoint with its context window, and is usable without a key once saved and tested', async () => {
    render(<ProvidersSection />)
    const card = (await screen.findAllByTestId('provider'))[1]!
    expect(card.dataset.provider).toBe('a5000')
    expect(within(card).getByTestId('provider-key-pill').textContent).toBe('No key')
    fireEvent.click(within(card).getByTestId('provider-edit'))
    const drawer = screen.getByTestId('provider-drawer')
    // A saved provider's models are listed as the dialog opens.
    const list = (await within(drawer).findByTestId('provider-model-list')) as HTMLSelectElement
    expect(list.value).toBe('qwen3.8-27b')
    fireEvent.change(list, { target: { value: 'llama-4-scout-17b' } })
    expect((within(drawer).getByTestId('provider-context') as HTMLInputElement).value).toBe('8192')
    fireEvent.change(list, { target: { value: 'qwen3.8-27b' } })
    expect((within(drawer).getByTestId('provider-context') as HTMLInputElement).value).toBe('16384')
    fireEvent.click(within(drawer).getByTestId('provider-drawer-test'))
    await waitFor(() => expect(within(drawer).getByTestId('provider-drawer-test-result').textContent).toContain('qwen3.8-27b answered in 0.8 s, without a key'))
    expect(calls.find((c) => c.url.endsWith('/providers/try'))?.body).toMatchObject({ id: 'a5000', model: 'qwen3.8-27b', provider: { kind: 'openai', endpoint: 'http://a5000.lan:8000/v1', contextWindow: 16384 } })
    expect(within(drawer).getByTestId('provider-drawer-test-result').textContent).toContain('the agents on it may use it without one')
    // Saved after a passing test, it is tested again as saved.
    fireEvent.click(within(drawer).getByTestId('provider-save'))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/a5000/test'))).toBe(true))
    await waitFor(() => expect(screen.getAllByTestId('provider')[1]!.querySelector('[data-testid=provider-key-pill]')!.textContent).toBe('No key needed'))
    expect(screen.getAllByTestId('provider')[1]!.querySelector('[data-testid=provider-key-pill]')!.getAttribute('data-tone')).toBe('ok')
    expect(screen.getAllByTestId('provider')[1]!.textContent).toContain('Passed with qwen3.8-27b, without a key')
  })
})
