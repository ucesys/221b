import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AIOverview } from '../../api/ai'
import { setShowCosts } from '../../lib/costs'
import { LimitsSection } from './LimitsSection'

// --- Administration › AI › Limits (Phase 12.1, AI-37) ---

const agent = (id: string, name: string, providerId: string, providerName: string, settings = {}) => ({
  id,
  name,
  purpose: '',
  tools: [],
  on: true,
  settings,
  enabled: true,
  providerId,
  providerName,
  model: 'm',
  defaultModel: true,
  inForce: [],
  clusters: null,
  state: 'in-sync' as const,
  valuesKey: '',
  spend: { dayUSD: 0.5, monthUSD: 3, dayAsked: 2, monthAsked: 9 },
})

function overview(): AIOverview {
  return {
    providers: [
      {
        id: 'default',
        name: 'Anthropic',
        kind: 'anthropic',
        resolved: { kind: 'anthropic', auth: 'header' },
        default: true,
        locked: false,
        needsKey: true,
        key: { set: true, store: 'secret' },
        limit: { dailyUSD: 0, monthlyUSD: 0 },
        spend: { dayUSD: 1.25, monthUSD: 9.5, dayAsked: 3, monthAsked: 12 },
      },
      {
        id: 'gw',
        name: 'Gateway',
        kind: 'openai',
        resolved: { kind: 'openai', auth: 'header' },
        default: false,
        locked: false,
        needsKey: true,
        key: { set: true, store: 'secret' },
        limit: { dailyUSD: 5, monthlyUSD: 50 },
        spend: { dayUSD: 0.75, monthUSD: 4, dayAsked: 1, monthAsked: 4 },
      },
    ],
    agents: [agent('sherlock', 'Sherlock', 'default', 'Anthropic', { dailyUSD: 15 }), agent('auditor', 'Auditor', 'gw', 'Gateway')],
    models: [],
    defaultModel: 'claude-opus-5-5',
    dailyUSD: 20,
    monthlyUSD: 200,
    spend: { day: '2026-11-17', dayUSD: 2, month: '2026-11', monthUSD: 13.5 },
    singleProvider: false,
    viewControl: false,
    clusters: [],
    running: 0,
    knowledge: 'later',
  } as unknown as AIOverview
}

describe('Administration › AI › Limits (AI-37)', () => {
  const calls: { method: string; url: string; body?: unknown }[] = []
  let ov = overview()
  beforeEach(() => {
    calls.length = 0
    ov = overview()
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      calls.push({ method, url: String(url), body })
      if (method === 'PUT') {
        const b = body as { dailyUSD: number; monthlyUSD: number; providers?: Record<string, { dailyUSD: number; monthlyUSD: number }> }
        ov = { ...ov, dailyUSD: b.dailyUSD, monthlyUSD: b.monthlyUSD, providers: ov.providers.map((p) => (b.providers?.[p.id] ? { ...p, limit: b.providers[p.id] } : p)) }
      }
      return new Response(JSON.stringify(ov))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    setShowCosts(true)
  })

  it('sets the total and each provider’s limits in one save, 0 meaning no limit, and shows what each has spent', async () => {
    const toasts: string[] = []
    render(<LimitsSection onToast={(m) => toasts.push(m)} />)
    const daily = (await screen.findByTestId('limit-daily')) as HTMLInputElement
    expect(daily.value).toBe('20')
    expect(screen.getByTestId('limits-total-hint').textContent).toBe(
      'All agents on all providers together, checked first, then the provider’s limit, then the agent’s own cap; once one is reached, the agents it covers stop until the next day or month. 0 means no limit. Spent: $2.00 today, $13.50 this month.',
    )
    const rows = screen.getAllByTestId('limits-provider')
    expect(rows.map((r) => r.dataset.provider)).toEqual(['default', 'gw'])
    expect(rows[1]!.textContent).toContain('$0.75')
    expect(rows[1]!.textContent).toContain('$4.00')
    const gwDaily = within(rows[1]!).getByTestId('provider-limit-daily') as HTMLInputElement
    expect(gwDaily.value).toBe('5')
    // Every limit says 0 is none.
    expect(screen.getAllByText(/0 means no limit\./).length).toBeGreaterThanOrEqual(4)
    expect((screen.getByTestId('limits-save') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(daily, { target: { value: '0' } })
    fireEvent.change(gwDaily, { target: { value: '0' } })
    fireEvent.change(within(rows[0]!).getByTestId('provider-limit-monthly'), { target: { value: '80' } })
    fireEvent.click(screen.getByTestId('limits-save'))
    await waitFor(() =>
      expect(calls.find((c) => c.method === 'PUT')).toEqual({ method: 'PUT', url: '/api/v1/ai/limits', body: { dailyUSD: 0, monthlyUSD: 200, providers: { gw: { dailyUSD: 0, monthlyUSD: 50 }, default: { dailyUSD: 0, monthlyUSD: 80 } } } }),
    )
    await waitFor(() => expect((screen.getByTestId('limits-save') as HTMLButtonElement).disabled).toBe(true))
    expect((screen.getByTestId('limit-daily') as HTMLInputElement).value).toBe('0')
    expect(toasts).toEqual(['Limits saved'])
  })

  it('sends the total alone when no provider’s limit changed, and says why a save failed', async () => {
    render(<LimitsSection />)
    fireEvent.change(await screen.findByTestId('limit-monthly'), { target: { value: '300' } })
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      return new Response(JSON.stringify({ error: 'the daily cap is above the monthly cap' }), { status: 400 })
    })
    fireEvent.click(screen.getByTestId('limits-save'))
    expect((await screen.findByTestId('save-error')).textContent).toBe('the daily cap is above the monthly cap')
    expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ dailyUSD: 20, monthlyUSD: 300 })
  })

  it('lists each agent’s own cap, read only, with a link to its drawer where it is changed', async () => {
    render(<LimitsSection />)
    const rows = await screen.findAllByTestId('limits-agent')
    expect(rows.map((r) => r.dataset.agent)).toEqual(['sherlock', 'auditor'])
    expect(rows[0]!.textContent).toContain('$15.00')
    expect(rows[1]!.textContent).toContain('none')
    expect(rows[1]!.textContent).toContain('Gateway')
    expect(within(rows[1]!).getByTestId('limits-agent-link').getAttribute('href')).toBe('#/admin/ai/agents/auditor')
    expect(within(screen.getByTestId('limits-agents')).queryByRole('spinbutton')).toBeNull()
  })

  it('hides every amount spent while costs are hidden, the limit fields staying editable (CF-40)', async () => {
    setShowCosts(false)
    render(<LimitsSection />)
    const daily = (await screen.findByTestId('limit-daily')) as HTMLInputElement
    expect(daily.value).toBe('20')
    expect(screen.getByTestId('limits-total-hint').textContent).not.toContain('$')
    expect(screen.getByTestId('limits-providers').textContent).not.toContain('Spent')
    expect(screen.getByTestId('limits-providers').querySelector('tbody')!.textContent).not.toContain('$')
    const total = screen.getByTestId('limits-providers-total')
    expect([...total.querySelectorAll('th, td')].map((c) => c.textContent)).toEqual(['Total', '20', '200'])
    expect(screen.getByTestId('limits-agents').textContent).not.toContain('$')
    expect(screen.getAllByTestId('limits-agent')[0]!.textContent).toContain('set')
    fireEvent.change(screen.getAllByTestId('provider-limit-daily')[1]!, { target: { value: '7' } })
    expect((screen.getByTestId('limits-save') as HTMLButtonElement).disabled).toBe(false)
  })

  it('ends the providers with a Total row: the total limits and the ledger’s spend, saying what was not counted per provider (AI-42)', async () => {
    render(<LimitsSection />)
    const total = await screen.findByTestId('limits-providers-total')
    expect(total.parentElement!.tagName).toBe('TFOOT')
    expect([...total.querySelectorAll('th, td')].map((c) => c.textContent)).toEqual(['Total', '20', '200', '$2.00', '$13.50'])
    expect(screen.queryByTestId('limits-providers-uncounted')).toBeNull()
    // Typed limits show at once; 0 is no limit.
    fireEvent.change(screen.getByTestId('limit-daily'), { target: { value: '0' } })
    expect(total.querySelectorAll('td')[0]!.textContent).toBe('no limit')
    cleanup()
    // Spent before the providers were counted: the ledger's total, and the part not in their rows.
    ov = { ...ov, spend: { ...ov.spend, monthUSD: 20 } }
    render(<LimitsSection />)
    expect((await screen.findByTestId('limits-providers-total-month')).textContent).toBe('$20.00includes $6.50 not counted per provider (before the upgrade, or on a provider since removed)')
    expect(screen.getByTestId('limits-providers-total-day').textContent).toBe('$2.00')
  })
})
