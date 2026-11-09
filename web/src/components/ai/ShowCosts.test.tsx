import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { testLine } from '../../api/ai'
import { setShowCosts, showCosts } from '../../lib/costs'
import { Hands } from '../Sherlock'
import { AgentsSection } from './AgentsSection'

// --- Show costs (Phase 12, wave 6c, CF-40) ---

const overview = {
  providers: [{ id: 'default', name: 'Anthropic', kind: 'anthropic', model: 'claude-opus-5-5', resolved: { kind: 'anthropic', auth: 'header' }, default: true, locked: false, needsKey: true, key: { set: true, store: 'secret' }, usedBy: ['Sherlock'] }],
  agents: [{ id: 'troubleshooter', name: 'Troubleshooter', purpose: 'Finds causes', tools: [], on: true, settings: { dailyUSD: 15 }, enabled: true, providerId: 'default', providerName: 'Anthropic', model: 'claude-opus-5-5', defaultModel: true, inForce: [], clusters: null, state: 'in-sync', valuesKey: 'agents.troubleshooter.instructions', spend: { dayUSD: 0.5, monthUSD: 3, dayAsked: 2, monthAsked: 9 } }],
  models: [],
  defaultModel: 'claude-opus-5-5',
  dailyUSD: 60,
  monthlyUSD: 600,
  spend: { day: '2026-11-05', dayUSD: 1.25, month: '2026-11', monthUSD: 9.5 },
  singleProvider: false,
  viewControl: false,
  clusters: [],
  running: 0,
  knowledge: 'later',
}

describe('Show costs (CF-40)', () => {
  const calls: { method: string; url: string; body?: unknown }[] = []
  let reply: { status: number; body: unknown } = { status: 200, body: { showCosts: false } }
  beforeEach(() => {
    Element.prototype.scrollTo = () => {}
    calls.length = 0
    reply = { status: 200, body: { showCosts: false } }
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push({ method, url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (String(url) === '/api/v1/settings/ui') return new Response(JSON.stringify(reply.body), { status: reply.status })
      return new Response(JSON.stringify(overview))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    setShowCosts(true)
  })

  it('takes the settings’ value; anything but false shows costs', () => {
    setShowCosts(false)
    expect(showCosts()).toBe(false)
    setShowCosts(undefined)
    expect(showCosts()).toBe(true)
  })

  it('is switched off in Settings › AI › Agents with its own route, and every dollar amount there goes but the budget fields', async () => {
    render(<AgentsSection />)
    const table = await screen.findByTestId('agents-table')
    expect(screen.getByTestId('agents-limit').textContent).toContain('$60.00 a day')
    expect(table.textContent).toContain('$15.00')
    expect(screen.getByTestId('settings-show-costs-on').getAttribute('aria-pressed')).toBe('true')
    fireEvent.click(screen.getByTestId('settings-show-costs-off'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')).toEqual({ method: 'PUT', url: '/api/v1/settings/ui', body: { showCosts: false } }))
    await waitFor(() => expect(screen.getByTestId('settings-show-costs-off').getAttribute('aria-pressed')).toBe('true'))
    expect(screen.getByTestId('settings-show-costs-msg').textContent).toBe('Costs are hidden for everyone.')
    expect(screen.queryByTestId('agents-limit')).toBeNull()
    expect(within(screen.getByTestId('agents-table')).queryByText('Daily cap')).toBeNull()
    expect(screen.getByTestId('agents-table').textContent).not.toContain('$')
    // The budget fields where budgets are set keep their amounts.
    expect((screen.getByTestId('limit-daily') as HTMLInputElement).value).toBe('60')
    expect(screen.getByRole('region', { name: 'Overall limits' }).textContent).not.toContain('Spent this month')
  })

  it('says why it could not be switched (a SuperAdmin’s), and keeps it as it was', async () => {
    reply = { status: 403, body: { error: 'Only a SuperAdmin may change this.' } }
    render(<AgentsSection />)
    fireEvent.click(await screen.findByTestId('settings-show-costs-off'))
    expect((await screen.findByTestId('settings-show-costs-msg')).textContent).toBe('Only a SuperAdmin may change this.')
    expect(showCosts()).toBe(true)
  })

  it('leaves the cost out of Sherlock’s hand-overs and of a test’s line', () => {
    setShowCosts(false)
    render(<Hands admin link={() => null} hands={[{ id: 1, from: 'sherlock', fromName: 'Sherlock', to: 'troubleshooter', toName: 'Troubleshooter', task: 'why', status: 'done', model: 'm', costUSD: 0.12 }]} />)
    expect(screen.getByTestId('sherlock-hand').querySelector('summary')!.textContent).toBe('Sherlock asked the Troubleshooter: why · m')
    const t = { at: '', passed: 2, cases: 2, same: 0, costUSD: 1.4 } as unknown as Parameters<typeof testLine>[0]
    expect(testLine(t)).toBe('2 of 2 recorded cases passed')
    expect(testLine(t, (n) => `$${n.toFixed(2)}`)).toBe('2 of 2 recorded cases passed · about $1.40')
  })
})
