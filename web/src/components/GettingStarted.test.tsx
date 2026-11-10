import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { leftText, shortWhere, type SetupStep, type SetupView } from '../api/setup'
import { GettingStartedCard, GettingStartedSection } from './GettingStarted'

const ids = ['signin', 'clusters', 'provider', 'agents', 'knowledge', 'jobs', 'fleets', 'hosts', 'backup']
const where: Record<string, [string, string]> = {
  knowledge: ['#/admin/settings/knowledge', 'Administration › Settings › Knowledge'],
  jobs: ['#/cases/jobs', 'AIOps › Jobs, and Administration › Settings › Watson for the notification channels'],
}

function viewOf(states: Record<string, SetupStep['state']>): SetupView {
  const steps: SetupStep[] = ids.map((id, i) => ({
    id,
    n: i + 1,
    title: `Step ${id}`,
    state: states[id] ?? 'done',
    detail: `What ${id} has`,
    why: 'Because.',
    link: where[id]?.[0] ?? '#/nodes',
    where: where[id]?.[1] ?? 'Kubernetes › Nodes',
    action: 'the field',
    docs: 'docs/getting-started.md#' + id,
    ...(states[id] === 'dismissed' ? { dismissedBy: 'ann@corp.example', dismissedAt: '2026-11-05T10:00:00Z' } : {}),
  }))
  const open = steps.filter((s) => s.state === 'open')
  return { steps, done: steps.filter((s) => s.state === 'done').length, dismissed: steps.filter((s) => s.state === 'dismissed').length, open: open.length, next: open[0]?.id, complete: open.length === 0, kept: true }
}

describe('Getting started', () => {
  const calls: { method: string; url: string; body?: { steps: string[]; dismiss: boolean } }[] = []
  let current: SetupView
  beforeEach(() => {
    calls.length = 0
    current = viewOf({ knowledge: 'open', jobs: 'open', fleets: 'dismissed' })
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const body = init?.body ? (JSON.parse(String(init.body)) as { steps: string[]; dismiss: boolean }) : undefined
      calls.push({ method: init?.method ?? 'GET', url: String(url), body })
      if (body) {
        const states: Record<string, SetupStep['state']> = {}
        for (const s of current.steps) states[s.id] = s.state
        for (const id of body.steps) states[id] = body.dismiss ? 'dismissed' : 'open'
        current = viewOf(states)
      }
      return new Response(JSON.stringify(current))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('shows each step as the server checked it, the next one leading to its screen', async () => {
    render(<GettingStartedSection />)
    const steps = await screen.findAllByTestId('setup-step')
    expect(steps).toHaveLength(9)
    expect(screen.getByTestId('setup-progress').textContent).toBe('6 of 9 done')
    const knowledge = steps[4]!
    expect(knowledge.getAttribute('data-state')).toBe('open')
    expect(knowledge.getAttribute('aria-current')).toBe('step')
    const open = within(knowledge).getByTestId('setup-open')
    expect(open.textContent).toBe('Open Knowledge')
    expect(open.getAttribute('href')).toBe('#/admin/settings/knowledge')
    expect(within(steps[5]!).getByTestId('setup-open').textContent).toBe('Open')
    expect(within(steps[0]!).getByText('Done')).toBeTruthy()
    // Nothing is ticked by hand: a done step has no control at all.
    expect(within(steps[0]!).queryByRole('button')).toBeNull()
    expect(within(steps[6]!).getByText('Dismissed by ann@corp.example')).toBeTruthy()
  })

  it('dismisses a step, brings one back, and dismisses the rest', async () => {
    render(<GettingStartedSection />)
    const steps = await screen.findAllByTestId('setup-step')
    fireEvent.click(within(steps[4]!).getByTestId('setup-dismiss'))
    await waitFor(() => expect(calls.at(-1)?.body).toEqual({ steps: ['knowledge'], dismiss: true }))
    await waitFor(() => expect(screen.getAllByTestId('setup-step')[4]!.getAttribute('data-state')).toBe('dismissed'))
    expect(screen.getAllByTestId('setup-step')[5]!.getAttribute('aria-current')).toBe('step')
    fireEvent.click(within(screen.getAllByTestId('setup-step')[6]!).getByTestId('setup-restore'))
    await waitFor(() => expect(calls.at(-1)?.body).toEqual({ steps: ['fleets'], dismiss: false }))
    fireEvent.click(await screen.findByTestId('setup-dismiss-rest'))
    await waitFor(() => expect(calls.at(-1)?.body).toEqual({ steps: ['jobs', 'fleets'], dismiss: true }))
    await waitFor(() => expect(screen.queryByTestId('setup-dismiss-rest')).toBeNull())
    expect(calls.filter((c) => c.method === 'POST').every((c) => c.url === '/api/v1/setup/dismiss')).toBe(true)
  })

  it('puts a card on the home page until every step is done or dismissed', async () => {
    render(<GettingStartedCard />)
    expect((await screen.findByTestId('home-setup-next')).textContent).toBe('Next: Step knowledge')
    expect(screen.getByTestId('home-setup-open').getAttribute('href')).toBe('#/admin/settings/knowledge')
    expect(screen.getByTestId('home-setup-all').getAttribute('href')).toBe('#/admin/settings/start')
    expect(screen.getByTestId('home-setup-all').textContent).toBe('2 steps left')
    cleanup()
    current = viewOf({ knowledge: 'dismissed' })
    render(<GettingStartedCard />)
    await waitFor(() => expect(calls.filter((c) => c.method === 'GET')).toHaveLength(2))
    expect(screen.queryByTestId('home-setup')).toBeNull()
  })

  it('words what is left and where a step is done', () => {
    expect(leftText(null)).toBe('What is left to set up')
    expect(leftText(viewOf({ jobs: 'open' }))).toBe('1 step left')
    expect(leftText(viewOf({}))).toBe('Every step done or dismissed')
    expect(shortWhere('AIOps › Jobs, and Administration › Settings › Watson')).toBe('Jobs')
    expect(shortWhere('Home › Clusters')).toBe('Clusters')
  })
})
