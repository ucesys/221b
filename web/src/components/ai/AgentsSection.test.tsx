import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AgentView, AIOverview, Instructions, TestRun } from '../../api/ai'
import { AgentsSection, worksOn } from './AgentsSection'
import { overallHints, stageIndex, valuesYAML } from './AgentDrawer'
import { Hands, agentRef } from '../Sherlock'

const agent = (id: string, name: string, extra: Partial<AgentView> = {}): AgentView => ({
  id,
  name,
  purpose: `${name}'s purpose`,
  tools: ['list_pods', 'pod_logs', 'top'],
  on: true,
  settings: {},
  enabled: true,
  providerId: 'default',
  providerName: 'Anthropic',
  model: 'claude-opus-5-5',
  defaultModel: true,
  inForce: ['list_pods', 'pod_logs', 'top'],
  clusters: null,
  state: 'in-sync',
  valuesKey: `agents.${id}.instructions`,
  spend: { dayUSD: 0.5, monthUSD: 3, dayAsked: 2, monthAsked: 9 },
  ...extra,
})

function overview(): AIOverview {
  return {
    providers: [
      {
        id: 'default',
        name: 'Anthropic',
        kind: 'anthropic',
        model: 'claude-opus-5-5',
        resolved: { kind: 'anthropic', auth: 'header' },
        default: true,
        locked: false,
        needsKey: true,
        key: { set: true, store: 'secret' },
        usedBy: ['Sherlock'],
        // A9: as an upgrade has it, every listed model offered and Auto among the three Claude models.
        offers: [
          { model: 'claude-haiku-5-5', tier: 'light' },
          { model: 'claude-sonnet-5-5', tier: 'standard' },
          { model: 'claude-opus-5-5', tier: 'strong' },
        ],
        offersAll: true,
      },
      { id: 'gw', name: 'Gateway', kind: 'openai', model: 'gpt-5', resolved: { kind: 'openai', auth: 'header' }, default: false, locked: false, needsKey: true, key: { set: true, store: 'secret' }, usedBy: [], offers: [], offersAll: true },
    ],
    agents: [
      agent('sherlock', 'Sherlock', { viewControl: true, clusters: ['local'] }),
      agent('troubleshooter', 'Troubleshooter', { state: 'edited', stage: 'test', settings: { dailyUSD: 15 } }),
      agent('automationAuthor', 'Automation author'),
      agent('scriptReviewer', 'Script reviewer'),
      agent('summarizer', 'Summarizer', { enabled: false, on: false, state: 'baseline-changed' }),
    ],
    models: [{ id: 'claude-opus-5-5', label: 'Claude Opus 5.5', in: 4, out: 20 }],
    defaultModel: 'claude-opus-5-5',
    dailyUSD: 60,
    monthlyUSD: 600,
    spend: { day: '2026-11-05', dayUSD: 1.25, month: '2026-11', monthUSD: 9.5 },
    singleProvider: false,
    viewControl: true,
    clusters: [
      { id: 'local', name: 'Here', env: '' },
      { id: 'prod', name: 'prod-eu', env: 'production' },
    ],
    running: 0,
    knowledge: 'later',
  }
}

const instructions = (extra: Partial<Instructions> = {}): Instructions => ({
  agent: 'troubleshooter',
  core: 'You are the Troubleshooter.',
  baseline: 'Cite our runbooks.',
  house: 'Cite our runbooks by name.',
  notes: { '*': 'Every cluster runs RKE2.' },
  state: 'edited',
  valuesKey: 'agents.troubleshooter.instructions',
  live: 2,
  versions: [
    { n: 2, house: 'Cite our runbooks by name.', by: 'sam', at: '2026-11-04T10:00:00Z', why: 'made live' },
    { n: 1, house: 'Cite our runbooks.', by: 'ann', at: '2026-11-03T10:00:00Z' },
  ],
  maxWords: 2000,
  parts: [{ name: 'core', label: 'Built-in instructions', builtIn: 'You are the Troubleshooter.', text: 'You are the Troubleshooter.', edited: false }],
  maxPartWords: 6000,
  ...extra,
})

describe('the Agents section', () => {
  const calls: { method: string; url: string; body?: unknown }[] = []
  let ins = instructions()
  beforeEach(() => {
    // jsdom has no scrollTo on elements; the drawer scrolls its body to the top.
    Element.prototype.scrollTo = () => {}
    calls.length = 0
    ins = instructions()
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      calls.push({ method, url: String(url), body })
      if (String(url).endsWith('/instructions')) return new Response(JSON.stringify(ins))
      if (String(url).endsWith('/history'))
        return new Response(
          JSON.stringify({
            entries: [
              { kind: 'live', n: 2, inForce: true },
              { kind: 'live', n: 1 },
            ],
          }),
        )
      if (/\/history\/live\/\d$/.test(String(url))) return new Response(JSON.stringify({ kind: 'live', house: String(url).endsWith('2') ? 'b' : 'a', parts: [] }))
      if (String(url).endsWith('/baseline')) {
        ins = { ...ins, state: 'edited', previousBaseline: undefined }
        return new Response(JSON.stringify(ins))
      }
      if (String(url).endsWith('/draft')) {
        ins = {
          ...ins,
          draft: { rev: 3, house: body.house, notes: body.notes, stage: 'draft', by: 'ann', at: '2026-11-05T10:00:00Z' },
          warnings: ['Line 1 looks like a fact about your infrastructure (db at 10.0.0.1): facts belong in Knowledge, where they are kept current.'],
        }
        return new Response(JSON.stringify(ins))
      }
      if (String(url).includes('/api/v1/ai/agents/scriptReviewer') && method === 'PUT' && !body.confirmSingleProvider) {
        return new Response(JSON.stringify({ error: 'the Script reviewer is on the same provider' }), { status: 409 })
      }
      return new Response(JSON.stringify(overview()))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('lists every agent with its model, effort, cap, clusters, state and on/off', async () => {
    // The Automation author left to Auto, at least Low; the others on the model's own effort.
    const withEffort = overview()
    withEffort.agents[2] = { ...withEffort.agents[2]!, settings: { ...withEffort.agents[2]!.settings, effort: 'auto', floorEffort: 'low' } }
    const served = globalThis.fetch
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => (String(url) === '/api/v1/ai' ? new Response(JSON.stringify(withEffort)) : served(url, init)))
    render(<AgentsSection />)
    const table = await screen.findByTestId('agents-table')
    const rows = within(table).getAllByTestId('agent-row')
    expect(rows.map((r) => r.dataset.agent)).toEqual(['sherlock', 'troubleshooter', 'automationAuthor', 'scriptReviewer', 'summarizer'])
    expect(screen.getByTestId('agents-summary').textContent).toContain('4 of 5 on')
    expect(screen.getByTestId('agents-limit').textContent).toContain('$60.00 a day')
    expect(rows[0]!.textContent).toContain('can change your view')
    expect(rows[0]!.textContent).toContain('Here')
    expect(rows[1]!.textContent).toContain('$15.00')
    expect(rows[1]!.textContent).toContain('Edited in app')
    expect(rows[1]!.textContent).toContain('Draft tested')
    // The effort column: the model's own default, a level, or Auto with its floor (AI-43).
    expect(within(rows[0]!).getByTestId('agent-effort').textContent).toBe('Default')
    expect(within(rows[2]!).getByTestId('agent-effort').textContent).toBe('Autoat least low')
    expect(rows[4]!.textContent).toContain('Baseline changed')
    expect(rows[4]!.dataset.on).toBe('false')
    // Sherlock has no switch: its clusters say where it is on.
    expect(within(rows[0]!).queryByTestId('agent-on')).toBeNull()
    fireEvent.click(within(rows[4]!).getByTestId('agent-on'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')).toMatchObject({ url: '/api/v1/ai/agents/summarizer', body: { on: true } }))
  })

  it('opens an agent in a drawer with its instructions, values key, stages and versions', async () => {
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    await waitFor(() => expect(within(drawer).getByTestId('ins-state').textContent).toBe('Edited in app'))
    expect(within(drawer).getByTestId('ins-values-key').textContent).toBe('agents.troubleshooter.instructions')
    expect((within(drawer).getByTestId('ins-house') as HTMLTextAreaElement).value).toBe('Cite our runbooks by name.')
    expect((within(drawer).getByTestId('ins-notes') as HTMLTextAreaElement).value).toBe('Every cluster runs RKE2.')
    const stages = within(drawer).getByTestId('ins-stages')
    expect(stages.querySelector('[aria-current=step] [data-testid=ins-step-name]')?.textContent).toBe('Live')
    expect(within(drawer).getAllByTestId('ins-restore')).toHaveLength(1)
    // Each version's Diff… opens the diff on it against the one before it (AI-41).
    const diffs = within(drawer).getAllByTestId('ins-version-diff')
    expect(diffs).toHaveLength(2)
    fireEvent.click(diffs[0]!)
    const dlg = await screen.findByTestId('history-dialog')
    await waitFor(() => expect((within(dlg).getByTestId('history-to') as HTMLSelectElement).value).toBe('live:2'))
    expect((within(dlg).getByTestId('history-from') as HTMLSelectElement).value).toBe('live:1')
    await waitFor(() => expect(within(dlg).getByTestId('app-diff').textContent).toContain('Version 1 → Version 2: 1 changed'))
    fireEvent.click(within(dlg).getByTestId('history-close'))
    expect(screen.queryByTestId('history-dialog')).toBeNull()
    expect(within(drawer).getByTestId('agent-jobs-link').getAttribute('href')).toBe('#/aiops/jobs')
    expect(within(drawer).getByTestId('agent-handovers').textContent).toContain('Hands work over to Automation author, Script reviewer')
    expect(within(drawer).getByTestId('agent-handovers').textContent).toContain('cannot change your view')
    // Compare shows the chart's baseline beside what is in force.
    fireEvent.click(within(drawer).getByTestId('ins-compare'))
    expect(within(drawer).getByTestId('ins-diff').textContent).toContain('Cite our runbooks.')
    // An edit is saved as a draft, with the facts it holds warned about.
    fireEvent.change(within(drawer).getByTestId('ins-house'), { target: { value: 'The db is at 10.0.0.1.' } })
    fireEvent.click(within(drawer).getByTestId('ins-save-draft'))
    await waitFor(() => expect(within(drawer).getByTestId('ins-warnings').textContent).toContain('looks like a fact'))
    expect(calls.find((c) => c.url.endsWith('/draft'))?.body).toEqual({ house: 'The db is at 10.0.0.1.', notes: { '*': 'Every cluster runs RKE2.' }, parts: { core: 'You are the Troubleshooter.' } })
    expect(within(drawer).getByTestId('ins-stages').querySelector('[aria-current=step] [data-testid=ins-step-name]')?.textContent).toBe('Draft')
    // Untested, the draft cannot go live or be piloted yet.
    expect((within(drawer).getByTestId('ins-live') as HTMLButtonElement).disabled).toBe(true)
    expect((within(drawer).getByTestId('ins-pilot') as HTMLButtonElement).disabled).toBe(true)
  })

  it('shows both diffs when an upgrade changed the baseline under an edit, and keeps or takes it on request', async () => {
    ins = instructions({ state: 'baseline-changed', baseline: 'Cite our runbooks and the wiki.', previousBaseline: 'Cite our runbooks.' })
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    await waitFor(() => expect(within(drawer).getByTestId('ins-state').textContent).toBe('Baseline changed'))
    expect(within(drawer).getByTestId('ins-baseline-changed').textContent).toContain('Nothing replaces either until you choose')
    fireEvent.click(within(drawer).getByTestId('ins-compare'))
    expect(within(drawer).getByTestId('ins-diff-upgrade').textContent).toContain('+Cite our runbooks and the wiki.')
    expect(within(drawer).getByTestId('ins-diff-edit').textContent).toContain('+Cite our runbooks by name.')
    fireEvent.click(within(drawer).getByTestId('ins-keep'))
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/baseline'))?.body).toEqual({ action: 'keep' }))
  })

  it('saves only the tools of its purpose, its caps and clusters', async () => {
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    const boxes = within(drawer).getAllByTestId('agent-tool')
    expect(boxes.map((b) => b.dataset.tool)).toEqual(['list_pods', 'pod_logs', 'top'])
    fireEvent.click(boxes[1]!)
    fireEvent.change(within(drawer).getByTestId('agent-daily'), { target: { value: '7' } })
    fireEvent.click(within(drawer).getByTestId('agent-some-clusters'))
    fireEvent.click(within(drawer).getByTestId('agent-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')).toMatchObject({ url: '/api/v1/ai/agents/troubleshooter', body: { tools: ['list_pods', 'top'], dailyUSD: 7, clusters: ['local'] } }))
  })

  it('sets an effort beside the model, Auto for the model where the provider has tiers, and the floor under Auto (AI-43, AI-44, AI-47, A9)', async () => {
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    const effort = within(drawer).getByTestId('agent-effort') as HTMLSelectElement
    expect([...effort.options].map((o) => o.textContent)).toEqual(['Default', 'Low', 'Medium', 'High', 'Extra high', 'Max', 'Auto'])
    expect(effort.value).toBe('')
    expect(within(drawer).queryByTestId('agent-auto')).toBeNull()
    fireEvent.change(effort, { target: { value: 'auto' } })
    fireEvent.change(within(drawer).getByTestId('agent-model-list'), { target: { value: 'auto' } })
    expect((within(drawer).getByTestId('agent-model-list') as HTMLSelectElement).value).toBe('auto')
    expect([...(within(drawer).getByTestId('agent-floor-model') as HTMLSelectElement).options].map((o) => o.textContent)).toEqual(['None', 'Claude Haiku 5.5 · light', 'Claude Sonnet 5.5 · standard', 'Claude Opus 5.5 · strong'])
    const line = within(drawer).getByTestId('agent-auto-line').textContent
    expect(line).toContain('the effort for every message and the model when a conversation or a run starts')
    expect(line).toContain('Never xhigh or max without an open P0 or P1 case')
    expect(line).toContain('off: medium on Claude Sonnet 5.5')
    fireEvent.change(within(drawer).getByTestId('agent-floor-effort'), { target: { value: 'medium' } })
    fireEvent.change(within(drawer).getByTestId('agent-floor-model'), { target: { value: 'claude-sonnet-5-5' } })
    fireEvent.click(within(drawer).getByTestId('agent-save'))
    await waitFor(() =>
      expect(calls.find((c) => c.method === 'PUT')).toMatchObject({
        url: '/api/v1/ai/agents/troubleshooter',
        body: { effort: 'auto', model: 'auto', floorEffort: 'medium', floorModel: 'claude-sonnet-5-5' },
      }),
    )
  })

  it('offers no Auto model on a provider with no tiered model, and says what its effort means there (AI-43, A9)', async () => {
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    fireEvent.change(within(drawer).getByTestId('agent-model-list'), { target: { value: 'auto' } })
    fireEvent.change(within(drawer).getByTestId('agent-provider'), { target: { value: 'gw' } })
    const model = within(drawer).getByTestId('agent-model-list') as HTMLSelectElement
    expect(model.value).toBe('')
    expect([...model.options].map((o) => o.value)).not.toContain('auto')
    expect(model.options[0]!.textContent).toBe('Default (gpt-5 on Gateway)')
    fireEvent.change(within(drawer).getByTestId('agent-effort'), { target: { value: 'low' } })
    expect(within(drawer).getByTestId('agent-effort-hint').textContent).toContain('Low turns the model’s thinking off')
    fireEvent.click(within(drawer).getByTestId('agent-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ provider: 'gw', effort: 'low' }))
    expect((calls.find((c) => c.method === 'PUT')?.body as { model?: string }).model).toBeUndefined()
  })

  it('asks for the single-provider confirmation before the Script reviewer shares the author’s provider', async () => {
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[3]!)
    const drawer = await screen.findByTestId('agent-drawer')
    expect(within(drawer).getByTestId('agent-same-provider').textContent).toContain('second opinion')
    fireEvent.change(within(drawer).getByTestId('agent-daily'), { target: { value: '5' } })
    expect((within(drawer).getByTestId('agent-save') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(within(drawer).getByTestId('agent-confirm-single'))
    fireEvent.click(within(drawer).getByTestId('agent-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ dailyUSD: 5, confirmSingleProvider: true }))
  })

  it('shows Sherlock’s clusters and view control in its drawer', async () => {
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[0]!)
    const drawer = await screen.findByTestId('agent-drawer')
    const clusters = within(drawer).getAllByTestId('agent-cluster')
    expect(clusters.map((c) => [c.dataset.cluster, (c as HTMLInputElement).checked])).toEqual([
      ['local', true],
      ['prod', false],
    ])
    fireEvent.click(clusters[1]!)
    fireEvent.click(within(drawer).getByTestId('agent-view-control'))
    fireEvent.click(within(drawer).getByTestId('agent-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ sherlockClusters: ['local', 'prod'], viewControl: false }))
  })
})

describe('the agents’ helpers', () => {
  it('say what the total and provider limits mean beside an agent’s own caps, with or without the amounts (AI-28, AI-39)', () => {
    const ov = { dailyUSD: 20, monthlyUSD: 0 }
    const money = (n: number) => `$${n.toFixed(2)}`
    expect(overallHints({}, ov, money)).toEqual([{ which: 'daily', higher: false, text: 'The total limit of $20.00 a day still applies to all agents together.' }])
    expect(overallHints({ dailyUSD: 100 }, ov)).toEqual([{ which: 'daily', higher: true, text: 'Its daily cap is higher than the total daily limit, which counts all agents together, and stops it first.' }])
    expect(overallHints({ dailyUSD: 20 }, ov)).toEqual([])
    expect(overallHints({}, { dailyUSD: 0, monthlyUSD: 0 })).toEqual([])
    // The provider's limit too, when it has one.
    const gw = { name: 'Gateway', limit: { dailyUSD: 5, monthlyUSD: 50 } }
    expect(overallHints({}, { dailyUSD: 0, monthlyUSD: 0 }, money, gw)).toEqual([
      { which: 'daily', higher: false, text: 'The limit of $5.00 a day of its provider Gateway still applies to every agent on Gateway.' },
      { which: 'monthly', higher: false, text: 'The limit of $50.00 a month of its provider Gateway still applies to every agent on Gateway.' },
    ])
    expect(overallHints({ dailyUSD: 30 }, ov, money, gw)[0]).toEqual({
      which: 'daily',
      higher: true,
      text: 'Its daily cap of $30.00 is higher than the total limit of $20.00 a day, which counts all agents together, and the limit of $5.00 a day of its provider Gateway, which counts every agent on Gateway, and stops it first.',
    })
    expect(overallHints({ dailyUSD: 10 }, ov, undefined, gw)[0]!.text).toBe('Its daily cap is higher than the daily limit of its provider Gateway, which counts every agent on Gateway, and stops it first.')
    expect(overallHints({}, { dailyUSD: 0, monthlyUSD: 0 }, money, { name: 'Gateway', limit: { dailyUSD: 0, monthlyUSD: 0 } })).toEqual([])
  })
  it('say where a draft stands, and how to put instructions into the chart', () => {
    expect(stageIndex(instructions())).toBe(3)
    expect(stageIndex(instructions({ draft: { rev: 2, house: '', stage: 'draft', by: 'a', at: '' } }))).toBe(0)
    expect(stageIndex(instructions({ draft: { rev: 2, house: '', stage: 'test', by: 'a', at: '', test: { rev: 2, at: '', by: '', cases: 1, passed: 1, same: 0, costUSD: 0, estimate: 0 } } }))).toBe(1)
    // Only a run that ran every case to the end tests the draft.
    expect(stageIndex(instructions({ draft: { rev: 2, house: '', stage: 'draft', by: 'a', at: '', test: { rev: 2, at: '', by: '', cases: 1, passed: 1, same: 0, costUSD: 0, estimate: 0, state: 'stopped' } } }))).toBe(0)
    expect(stageIndex(instructions({ draft: { rev: 2, house: '', stage: 'draft', by: 'a', at: '', test: { rev: 2, at: '', by: '', cases: 1, passed: 1, same: 0, costUSD: 0, estimate: 0, state: 'failed', error: 'cap' } } }))).toBe(0)
    const until = new Date(Date.now() + 3600_000).toISOString()
    expect(stageIndex(instructions({ draft: { rev: 2, house: '', stage: 'pilot', by: 'a', at: '', pilot: { started: '', until, by: '' }, test: { rev: 2, at: '', by: '', cases: 1, passed: 1, same: 0, costUSD: 0, estimate: 0 } } }))).toBe(2)
    expect(valuesYAML('troubleshooter', 'Line one.\n\nLine two.')).toBe('agents:\n  troubleshooter:\n    instructions: |\n      Line one.\n\n      Line two.\n')
    const ov = overview()
    expect(worksOn(ov.agents[1]!, ov)).toBe('All clusters')
    expect(worksOn({ ...ov.agents[1]!, clusters: ['prod'] }, ov)).toBe('prod-eu')
    expect(agentRef('Mycroft')).toBe('Mycroft')
    expect(agentRef('Change planner')).toBe('the Change planner')
  })
})

describe('hand-overs in an answer', () => {
  afterEach(cleanup)
  it('read “Sherlock asked the Troubleshooter”, and open to what came back, nested', () => {
    render(
      <Hands
        admin
        link={() => null}
        hands={[
          {
            id: 1,
            from: 'sherlock',
            fromName: 'Sherlock',
            to: 'troubleshooter',
            toName: 'Troubleshooter',
            task: 'find why web crash-loops',
            status: 'done',
            text: 'The database refused the password.',
            model: 'claude-opus-5-5',
            costUSD: 0.12,
            hands: [{ id: 2, from: 'troubleshooter', fromName: 'Troubleshooter', to: 'mycroft', toName: 'Mycroft', task: 'what does shop depend on?', status: 'refused', error: 'its budget is spent' }],
          },
        ]}
      />,
    )
    const hands = screen.getAllByTestId('sherlock-hand')
    expect(hands).toHaveLength(2)
    expect(hands[0]!.querySelector('summary')!.textContent).toBe('Sherlock asked the Troubleshooter: find why web crash-loops · claude-opus-5-5 · $0.12')
    expect(hands[1]!.querySelector('summary')!.textContent).toContain('The Troubleshooter asked Mycroft: what does shop depend on?')
    expect(hands[1]!.textContent).toContain('its budget is spent')
    expect(screen.getByTestId('sherlock-hand-text').textContent).toContain('The database refused the password.')
  })
})

// --- AI settings, round 2 (Phase 12, wave 5) ---
describe('an agent’s built-in text, stages and context', () => {
  const calls: { method: string; url: string; body?: unknown }[] = []
  let ins = instructions()
  let ov = overview()
  let run: TestRun | null = null
  beforeEach(() => {
    Element.prototype.scrollTo = () => {}
    calls.length = 0
    ins = instructions()
    ov = overview()
    run = null
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      const u = String(url)
      calls.push({ method, url: u, body })
      if (u.endsWith('/instructions')) return new Response(JSON.stringify(ins))
      if (u.endsWith('/tests/run')) return run ? new Response(JSON.stringify(run)) : new Response(JSON.stringify({ error: 'No test of this agent has run yet.' }), { status: 404 })
      if (u.endsWith('/live')) return new Response(JSON.stringify({ waiting: true, request: { id: 7 } }), { status: 202 })
      if (u.endsWith('/test')) return new Response(JSON.stringify(body.estimate ? { estimateUSD: 0.5, cases: 2, replays: 0 } : { estimateUSD: 0.5, cases: 2, replays: 0, started: true }), { status: body.estimate ? 200 : 202 })
      if (u.endsWith('/draft/back')) {
        ins = { ...ins, draft: { ...ins.draft!, rev: ins.draft!.rev + 1, stage: 'draft', test: undefined } }
        return new Response(JSON.stringify(ins))
      }
      if (u.endsWith('/draft')) {
        ins = { ...ins, draft: { rev: 3, house: body.house, notes: body.notes, stage: 'draft', by: 'ann', at: '2026-11-05T10:00:00Z', parts: body.parts } }
        return new Response(JSON.stringify(ins))
      }
      if (u.endsWith('/models'))
        return new Response(
          JSON.stringify({
            models: [
              { id: 'qwen3.8-27b', contextWindow: 16384 },
              { id: 'llama-4-scout-17b', contextWindow: 8192 },
            ],
          }),
        )
      return new Response(JSON.stringify(ov))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })
  const setLocal = (env: string) => {
    ov = { ...ov, clusters: ov.clusters.map((c) => (c.id === 'local' ? { ...c, env } : c)) }
  }

  it('shows the built-in text pre-filled, saves an edit of it as a draft, resets it and compares it', async () => {
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    const part = (await within(drawer).findByTestId('ins-part')) as HTMLTextAreaElement
    expect(part.value).toBe('You are the Troubleshooter.')
    expect(within(drawer).getByTestId('ins-part-state').textContent).toBe('Built-in')
    expect((within(drawer).getByTestId('ins-part-reset') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(part, { target: { value: 'You are the Troubleshooter of Acme.' } })
    expect(within(drawer).getByTestId('ins-part-state').textContent).toBe('Edited, not saved')
    fireEvent.click(within(drawer).getByTestId('ins-part-compare'))
    expect(within(drawer).getByTestId('ins-part-diff').textContent).toContain('+You are the Troubleshooter of Acme.')
    fireEvent.click(within(drawer).getByTestId('ins-part-reset'))
    expect(part.value).toBe('You are the Troubleshooter.')
    fireEvent.change(part, { target: { value: 'You are the Troubleshooter of Acme.' } })
    fireEvent.click(within(drawer).getByTestId('ins-save-draft'))
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/draft'))?.body).toMatchObject({ parts: { core: 'You are the Troubleshooter of Acme.' } }))
  })

  it('offers each stage’s action, or says why it cannot be done, and goes back to draft', async () => {
    const test = { rev: 3, at: '2026-11-05T11:00:00Z', by: 'ann', cases: 2, passed: 2, same: 0, costUSD: 0.1, estimate: 0.1 }
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    let drawer = await screen.findByTestId('agent-drawer')
    // No draft: Live is in force, and every action says what to do first.
    await waitFor(() => expect(within(drawer).getByTestId('ins-stages').querySelector('[aria-current=step] [data-testid=ins-step-name]')?.textContent).toBe('Live'))
    const why = () =>
      within(drawer)
        .getAllByTestId('ins-step-why')
        .map((x) => x.textContent)
    expect(why()).toContain('Nothing waits to go live: version 2 is in force. Edit and save a draft.')
    expect((within(drawer).getByTestId('ins-test') as HTMLButtonElement).disabled).toBe(true)
    cleanup()
    // A tested draft: pilot and live may go; back to draft drops the test.
    ins = instructions({ draft: { rev: 3, house: 'x', stage: 'test', by: 'ann', at: '2026-11-05T10:00:00Z', test } })
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    drawer = await screen.findByTestId('agent-drawer')
    await waitFor(() => expect(within(drawer).getByTestId('ins-stages').querySelector('[aria-current=step] [data-testid=ins-step-name]')?.textContent).toBe('Test'))
    expect((within(drawer).getByTestId('ins-pilot') as HTMLButtonElement).disabled).toBe(false)
    expect((within(drawer).getByTestId('ins-live') as HTMLButtonElement).disabled).toBe(false)
    expect(within(drawer).getByTestId('ins-test').textContent).toBe('Run tests again')
    fireEvent.click(within(drawer).getByTestId('ins-back'))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/draft/back'))).toBe(true))
    await waitFor(() => expect((within(drawer).getByTestId('ins-pilot') as HTMLButtonElement).disabled).toBe(true))
    expect(why()).toContain('Run the tests first: a pilot serves a tested draft.')
  })

  it('shows the steps before live as not started while no draft waits (Phase 12.1)', async () => {
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    await waitFor(() => expect(within(drawer).getByTestId('ins-stages').querySelector('[data-stage=live]')?.getAttribute('data-state')).toBe('current'))
    const stages = within(drawer).getByTestId('ins-stages')
    for (const name of ['draft', 'test', 'pilot']) {
      const step = stages.querySelector(`[data-stage=${name}]`)!
      expect(step.getAttribute('data-state')).toBe('todo')
      expect(step.textContent).toContain('not started')
      expect(step.textContent).not.toContain('done')
    }
    expect(stages.querySelector('[data-stage=live]')!.textContent).toContain('now')
    expect(within(drawer).queryByTestId('ins-live-untested')).toBeNull()
  })

  it('makes an untested draft live only with a reason, and offers it only for an untested draft (Phase 12.1)', async () => {
    const test = { rev: 3, at: '2026-11-05T11:00:00Z', by: 'ann', cases: 2, passed: 2, same: 0, costUSD: 0.1, estimate: 0.1 }
    ins = instructions({ draft: { rev: 3, house: 'x', stage: 'draft', by: 'ann', at: '2026-11-05T10:00:00Z' } })
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    let drawer = await screen.findByTestId('agent-drawer')
    const untested = await within(drawer).findByTestId('ins-live-untested')
    expect((within(drawer).getByTestId('ins-live') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(untested)
    const confirm = await screen.findByTestId('confirm')
    const ok = within(confirm).getByTestId('confirm-ok') as HTMLButtonElement
    expect(ok.disabled).toBe(true)
    const reason = within(confirm).getByTestId('ins-untested-reason')
    fireEvent.change(reason, { target: { value: '   ' } })
    expect(ok.disabled).toBe(true)
    fireEvent.change(reason, { target: { value: ' The outage needs it now. ' } })
    expect(ok.disabled).toBe(false)
    fireEvent.click(ok)
    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url.endsWith('/live'))?.body).toEqual({ untested: true, reason: 'The outage needs it now.' }))
    await waitFor(() => expect(screen.queryByTestId('confirm')).toBeNull())
    cleanup()
    // A tested draft goes live the usual way, with no untested action.
    ins = instructions({ draft: { rev: 3, house: 'x', stage: 'test', by: 'ann', at: '2026-11-05T10:00:00Z', test } })
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    drawer = await screen.findByTestId('agent-drawer')
    await waitFor(() => expect((within(drawer).getByTestId('ins-live') as HTMLButtonElement).disabled).toBe(false))
    expect(within(drawer).queryByTestId('ins-live-untested')).toBeNull()
    cleanup()
    // A run stopped before every case ran leaves the draft untested; with
    // the console's own cluster other, it waits for a second SuperAdmin.
    setLocal('other')
    ins = instructions({ draft: { rev: 3, house: 'x', stage: 'draft', by: 'ann', at: '2026-11-05T10:00:00Z', test: { ...test, state: 'stopped', error: 'stopped' } } })
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    drawer = await screen.findByTestId('agent-drawer')
    fireEvent.click(await within(drawer).findByTestId('ins-live-untested'))
    expect((within(drawer).getByTestId('ins-live') as HTMLButtonElement).disabled).toBe(true)
    expect(within(drawer).getByTestId('ins-test-incomplete').textContent).toContain('still untested')
    expect(within(await screen.findByTestId('confirm')).queryByTestId('ins-untested-direct')).toBeNull()
    cleanup()
    setLocal('staging')
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    drawer = await screen.findByTestId('agent-drawer')
    fireEvent.click(await within(drawer).findByTestId('ins-live-untested'))
    expect(within(await screen.findByTestId('confirm')).getByTestId('ins-untested-direct').textContent).toContain('staging, not production')
  })

  it('opens the last test’s results in the test dialog, read-only, with the failures counted (Phase 12.1)', async () => {
    const test: TestRun = {
      rev: 3,
      at: '2026-11-05T11:00:00Z',
      by: 'ann',
      cases: 2,
      passed: 1,
      same: 0,
      costUSD: 0.1,
      estimate: 0.1,
      state: 'done',
      results: [
        { id: 't1', name: 'passes', passed: true, answer: 'Fine.', costUSD: 0.05 },
        { id: 't2', name: 'fails', passed: false, why: ['it did not call pod_logs'], answer: 'No.', costUSD: 0.05 },
      ],
    }
    ins = instructions({ draft: { rev: 3, house: 'x', stage: 'test', by: 'ann', at: '2026-11-05T10:00:00Z', test } })
    run = test
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    fireEvent.click(await within(drawer).findByTestId('ins-test-results'))
    expect(within(drawer).getByTestId('ins-test-failed').textContent).toContain('1 failed')
    expect(within(drawer).queryByTestId('ins-failures')).toBeNull()
    const dlg = await screen.findByTestId('test-dialog')
    await waitFor(() => expect(within(dlg).getByTestId('test-dialog-state').textContent).toBe('Done'))
    expect(within(dlg).queryByTestId('test-dialog-stop')).toBeNull()
    expect(within(dlg).getAllByTestId('test-case-row')[0]!.textContent).toContain('fails')
    fireEvent.click(within(dlg).getByTestId('test-dialog-close'))
    expect(screen.queryByTestId('test-dialog')).toBeNull()
    expect(screen.getByTestId('agent-drawer')).toBeTruthy()
  })

  it('has Show results, History and Discard the draft as buttons in one row with Save draft (AI-31), History opening its dialog (AI-32)', async () => {
    const test: TestRun = { rev: 3, at: '2026-11-05T11:00:00Z', by: 'ann', cases: 1, passed: 1, same: 0, costUSD: 0.1, estimate: 0.1, state: 'done', results: [] }
    ins = instructions({ draft: { rev: 3, house: 'x', stage: 'test', by: 'ann', at: '2026-11-05T10:00:00Z', test } })
    run = test
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    const results = await within(drawer).findByTestId('ins-test-results')
    const row = within(drawer).getByTestId('ins-save-draft').parentElement!
    for (const id of ['ins-test-results', 'ins-history', 'ins-discard']) {
      const b = within(drawer).getByTestId(id)
      expect(b.parentElement).toBe(row)
      expect(b.className).toBe('rk-btn')
    }
    expect(results.textContent).toBe('Show results')
    expect(within(drawer).getByTestId('ins-last-test').querySelector('button')).toBeNull()
    fireEvent.click(within(drawer).getByTestId('ins-history'))
    expect(await screen.findByTestId('history-dialog')).toBeTruthy()
    await waitFor(() => expect(calls.some((c) => /\/api\/v1\/ai\/agents\/[a-zA-Z]+\/history$/.test(c.url))).toBe(true))
    fireEvent.click(screen.getByTestId('history-close'))
    expect(screen.queryByTestId('history-dialog')).toBeNull()
  })

  it('opens the test dialog as the test starts, and shows a run in flight in the drawer to open again (Phase 12.1)', async () => {
    ins = instructions({ draft: { rev: 3, house: 'x', stage: 'draft', by: 'ann', at: '2026-11-05T10:00:00Z' } })
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    fireEvent.click(await within(drawer).findByTestId('ins-test'))
    await waitFor(() => expect(within(drawer).getByTestId('ins-test-estimate').textContent).toContain('2 recorded cases'))
    run = {
      rev: 3,
      at: '2026-11-05T11:00:00Z',
      by: 'ann',
      cases: 1,
      passed: 1,
      same: 0,
      costUSD: 0.05,
      estimate: 0.5,
      state: 'running',
      planned: [
        { id: 't1', name: 'passes', state: 'passed' },
        { id: 't2', name: 'runs', state: 'running' },
      ],
      results: [{ id: 't1', name: 'passes', passed: true, answer: 'Fine.', costUSD: 0.05 }],
    }
    fireEvent.click(within(drawer).getByTestId('ins-test-run'))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/test') && !(c.body as { estimate?: boolean }).estimate)).toBe(true))
    const dlg = await screen.findByTestId('test-dialog')
    await within(dlg).findByTestId('test-dialog-stop')
    fireEvent.keyDown(dlg, { key: 'Escape' })
    expect(screen.queryByTestId('test-dialog')).toBeNull()
    expect(calls.some((c) => c.url.endsWith('/tests/stop'))).toBe(false)
    await waitFor(() => expect(within(drawer).getByTestId('ins-test-running').textContent).toContain('Tests running: 1 of 2 cases…'))
    fireEvent.click(within(drawer).getByTestId('ins-test-show'))
    expect(await screen.findByTestId('test-dialog')).toBeTruthy()
  })

  it('says how much of its model’s context Sherlock takes, and saves its compact form’s choice and a model from the endpoint', async () => {
    ov.agents[0] = {
      ...ov.agents[0]!,
      model: 'qwen3.8-27b',
      footprint: { instructions: 736, tools: 1732, toolCount: 12, whole: { instructions: 3449, tools: 6810, toolCount: 38 }, window: 16384, windowFrom: 'provider', compact: true, mode: '', share: 0.5, maxTokens: 4096 },
    }
    ins = instructions({ agent: 'sherlock', parts: [ins.parts[0]!, { name: 'compact', label: 'Compact form', builtIn: 'You are Sherlock, briefly.', text: 'You are Sherlock, briefly.', edited: false }] })
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[0]!)
    const drawer = await screen.findByTestId('agent-drawer')
    const line = within(drawer).getByTestId('agent-context-line')
    expect(line.dataset.compact).toBe('true')
    expect(line.textContent).toContain('about 2,468 tokens (736 of instructions, 1,732 for 12 tools) of qwen3.8-27b’s 16,384 tokens (15%)')
    expect(line.textContent).toContain('Its whole form, about 10,259 tokens (3,449 of instructions, 6,810 for 38 tools), takes more than 50% of the context, so Sherlock answers in its compact form.')
    expect(line.textContent).toContain('A reply is bounded at 4,096 tokens')
    // Both forms of its built-in text.
    const tabs = await within(drawer).findAllByTestId('ins-part-tab')
    fireEvent.click(tabs[1]!)
    expect((within(drawer).getByTestId('ins-part') as HTMLTextAreaElement).value).toBe('You are Sherlock, briefly.')
    // A model of its own, picked from the endpoint's list, brings its context window.
    const list = (await within(drawer).findByTestId('agent-model-list')) as HTMLSelectElement
    await waitFor(() => expect([...list.options].map((o) => o.textContent)).toContain('qwen3.8-27b · 16,384 tokens'))
    fireEvent.change(list, { target: { value: 'llama-4-scout-17b' } })
    fireEvent.click(drawer.querySelector('[data-testid=agent-compact] [data-mode=always]')!)
    fireEvent.click(within(drawer).getByTestId('agent-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ model: 'llama-4-scout-17b', contextWindow: 8192, compact: 'always' }))
    expect(calls.some((c) => c.url === '/api/v1/ai/providers/default/models')).toBe(true)
  })

  // --- FU-01: the agent's model is a dropdown of its provider's models ---
  it('lists the provider’s models as the drawer opens, again when the provider changes, keeps a model not listed, and lets one be typed only when the list fails', async () => {
    ov.agents[1] = { ...ov.agents[1]!, settings: { model: 'my-old-model' } }
    const models = (id: string) => (id === 'gw' ? { models: [{ id: 'gpt-5' }, { id: 'gpt-5-mini' }] } : { models: [{ id: 'qwen3.8-27b', contextWindow: 16384 }] })
    let fail = false
    const base = globalThis.fetch
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const m = /\/providers\/([^/]+)\/models$/.exec(String(url))
      if (m) {
        calls.push({ method: 'GET', url: String(url) })
        if (fail) return new Response(JSON.stringify({ error: 'the endpoint did not answer' }), { status: 502 })
        return new Response(JSON.stringify(models(m[1]!)))
      }
      return base(url, init)
    })
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    // Listed as it opens, with no button pressed; the saved model stays chosen though not listed.
    await waitFor(() => expect(within(drawer).getByTestId('agent-model-picker').dataset.state).toBe('listed'))
    let list = within(drawer).getByTestId('agent-model-list') as HTMLSelectElement
    expect(list.value).toBe('my-old-model')
    expect([...list.options].map((o) => o.textContent)).toEqual(['Default (claude-opus-5-5 on Anthropic)', 'Auto', 'my-old-model (not listed by the provider)', 'qwen3.8-27b · 16,384 tokens'])
    expect(within(drawer).queryByTestId('agent-model')).toBeNull()
    expect([...list.options].some((o) => o.textContent === 'Another model…')).toBe(false)
    // Another provider: its models.
    fireEvent.change(within(drawer).getByTestId('agent-provider'), { target: { value: 'gw' } })
    await waitFor(() => expect(calls.some((c) => c.url === '/api/v1/ai/providers/gw/models')).toBe(true))
    await waitFor(() => expect([...(within(drawer).getByTestId('agent-model-list') as HTMLSelectElement).options].map((o) => o.value)).toContain('gpt-5-mini'))
    list = within(drawer).getByTestId('agent-model-list') as HTMLSelectElement
    fireEvent.change(list, { target: { value: 'gpt-5-mini' } })
    fireEvent.click(within(drawer).getByTestId('agent-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ provider: 'gw', model: 'gpt-5-mini' }))
    cleanup()
    // The list cannot be had: the model is typed, and why is said.
    fail = true
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const again = await screen.findByTestId('agent-drawer')
    await waitFor(() => expect(within(again).getByTestId('agent-model-picker').dataset.state).toBe('typed'))
    expect((within(again).getByTestId('agent-model') as HTMLInputElement).value).toBe('my-old-model')
    expect(within(again).getByTestId('agent-model-hint').textContent).toContain('Type the model’s name.')
  })
})

describe('the agent’s one Model dropdown (A9)', () => {
  const calls: { method: string; url: string; body?: unknown }[] = []
  let ov = overview()
  beforeEach(() => {
    Element.prototype.scrollTo = () => {}
    calls.length = 0
    ov = overview()
    // The gateway's offered models were chosen: two with tiers, one without.
    ov.providers[1] = {
      ...ov.providers[1]!,
      offeredChosen: true,
      offered: [{ model: 'qwen-small', tier: 'light' }, { model: 'qwen-big', tier: 'strong' }, { model: 'embed-1' }],
      offers: [{ model: 'qwen-small', tier: 'light' }, { model: 'qwen-big', tier: 'strong' }, { model: 'embed-1' }, { model: 'gpt-5' }],
      offersAll: false,
    }
    ov.agents[1] = { ...ov.agents[1]!, settings: { provider: 'gw', model: 'qwen-old' }, providerId: 'gw', providerName: 'Gateway', notOffered: true }
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push({ method, url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (String(url).endsWith('/instructions')) return new Response(JSON.stringify(instructions()))
      return new Response(JSON.stringify(ov))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('offers Default, Auto and the provider’s offered models, says a model is no longer offered, and saves each choice', async () => {
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    expect(within(drawer).queryByTestId('agent-default-model')).toBeNull()
    expect(within(drawer).queryByTestId('agent-auto-model')).toBeNull()
    const model = within(drawer).getByTestId('agent-model-list') as HTMLSelectElement
    expect(within(drawer).getByTestId('agent-model-picker').dataset.state).toBe('listed')
    expect([...model.options].map((o) => o.textContent)).toEqual(['Default (gpt-5 on Gateway)', 'Auto', 'qwen-old (no longer offered)', 'qwen-small · light', 'qwen-big · strong', 'embed-1', 'gpt-5'])
    expect(model.value).toBe('qwen-old')
    expect(within(drawer).getByTestId('agent-not-offered').textContent).toContain('Gateway no longer offers qwen-old: the Troubleshooter answers on it until you choose another model.')
    // No request for the endpoint's list: the offered models were chosen.
    expect(calls.some((c) => c.url.endsWith('/models'))).toBe(false)
    // A fixed model has its context window; Auto has the floor among the tiered models.
    fireEvent.change(model, { target: { value: 'qwen-big' } })
    expect(within(drawer).queryByTestId('agent-not-offered')).toBeNull()
    expect(within(drawer).getByTestId('agent-context')).toBeTruthy()
    fireEvent.change(model, { target: { value: 'auto' } })
    expect(within(drawer).queryByTestId('agent-context')).toBeNull()
    expect([...(within(drawer).getByTestId('agent-floor-model') as HTMLSelectElement).options].map((o) => o.value)).toEqual(['', 'qwen-small', 'qwen-big'])
    fireEvent.change(within(drawer).getByTestId('agent-floor-model'), { target: { value: 'qwen-small' } })
    fireEvent.click(within(drawer).getByTestId('agent-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ provider: 'gw', model: 'auto', floorModel: 'qwen-small' }))
    // Default: no model of its own.
    calls.length = 0
    fireEvent.change(within(drawer).getByTestId('agent-model-list'), { target: { value: '' } })
    fireEvent.click(within(drawer).getByTestId('agent-save'))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT')).toBeTruthy())
    const body = calls.find((c) => c.method === 'PUT')!.body as { model?: string; floorModel?: string }
    expect(body.model).toBeUndefined()
    expect(body.floorModel).toBeUndefined()
  })

  it('says Auto cannot pick the model when the provider’s models lost their tiers', async () => {
    ov.providers[1] = { ...ov.providers[1]!, offered: [{ model: 'embed-1' }], offers: [{ model: 'embed-1' }, { model: 'gpt-5' }] }
    ov.agents[1] = { ...ov.agents[1]!, settings: { provider: 'gw', model: 'auto' }, autoNoTier: true, notOffered: false }
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    expect(within(drawer).getByTestId('agent-auto-no-tier').textContent).toContain('None of the models Gateway offers has a tier')
  })
})

describe('the drawer’s pilot history, discarded drafts and the limits', () => {
  const calls: { method: string; url: string; body?: unknown }[] = []
  let ins = instructions()
  let ov = overview()
  const test = { rev: 3, at: '2026-11-05T11:00:00Z', by: 'ann', cases: 2, passed: 2, same: 0, costUSD: 0.1, estimate: 0.1 }
  const pilot = { people: ['dev@corp.example'], started: '2026-11-05T12:00:00Z', until: '2099-01-01T00:00:00Z', by: 'ann' }
  beforeEach(() => {
    Element.prototype.scrollTo = () => {}
    calls.length = 0
    ins = instructions()
    ov = overview()
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      const u = String(url)
      calls.push({ method, url: u, body })
      if (u.endsWith('/instructions')) return new Response(JSON.stringify(ins))
      if (u.endsWith('/tests/run')) return new Response(JSON.stringify({ error: 'No test of this agent has run yet.' }), { status: 404 })
      if (u.endsWith('/pilot'))
        return new Response(
          JSON.stringify({ entries: [3, 2, 1].map((n) => ({ n, agent: 'troubleshooter', version: 'live', at: '2026-11-05T13:00:00Z', who: 'dev', cluster: 'local', question: `Q${n}?`, answer: `A${n}.`, model: 'm', costUSD: 0 })) }),
        )
      if (u.endsWith('/draft')) {
        ins = {
          ...ins,
          draft: { rev: 4, house: body.house, notes: body.notes, stage: 'draft', by: 'ann', at: '2026-11-05T14:00:00Z' },
          discarded: { drafts: [3], pilotEnded: true, requests: [1], notice: 'Earlier draft 3 was discarded; the pilot of draft 3 ended; request 1 was withdrawn.' },
        }
        return new Response(JSON.stringify(ins))
      }
      return new Response(JSON.stringify(ov))
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('opens the pilot’s answers in a dialog of their own from the Pilot step, with their count (AI-27)', async () => {
    ins = instructions({ draft: { rev: 3, house: 'x', stage: 'pilot', by: 'ann', at: '2026-11-05T10:00:00Z', test, pilot } })
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    const show = await within(drawer).findByTestId('pilot-history-show')
    expect(show.closest('[data-stage]')?.getAttribute('data-stage')).toBe('pilot')
    await waitFor(() => expect(show.textContent).toBe('Show pilot history (3)'))
    // The answers are no longer listed in the drawer itself.
    expect(within(drawer).queryByTestId('pilot-history-entry')).toBeNull()
    fireEvent.click(show)
    const dlg = await screen.findByTestId('pilot-history-dialog')
    await waitFor(() => expect(within(dlg).getAllByTestId('pilot-history-entry')).toHaveLength(3))
    fireEvent.click(within(dlg).getByTestId('pilot-history-close'))
    expect(screen.queryByTestId('pilot-history-dialog')).toBeNull()
  })

  it('says which earlier drafts a new draft discarded, the pilot that ended and the request withdrawn (AI-26)', async () => {
    const toasts: string[] = []
    ins = instructions({ draft: { rev: 3, house: 'x', stage: 'pilot', by: 'ann', at: '2026-11-05T10:00:00Z', test, pilot, request: 1 } })
    render(<AgentsSection onToast={(m) => toasts.push(m)} />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[1]!)
    const drawer = await screen.findByTestId('agent-drawer')
    fireEvent.change(await within(drawer).findByTestId('ins-house'), { target: { value: 'Cite our runbooks, briefly.' } })
    fireEvent.click(within(drawer).getByTestId('ins-save-draft'))
    await waitFor(() => expect(within(drawer).getByTestId('ins-discarded').textContent).toBe('Earlier draft 3 was discarded; the pilot of draft 3 ended; request 1 was withdrawn.'))
    expect(toasts).toContain('Saved draft 4. Earlier draft 3 was discarded; the pilot of draft 3 ended; request 1 was withdrawn.')
  })

  it('says the total limit still applies beside a cap of 0, warns of a cap above it, and leads to Administration › AI › Limits (AI-28, AI-39)', async () => {
    ov = { ...ov, dailyUSD: 20, monthlyUSD: 200, agents: ov.agents.map((a) => (a.id === 'sherlock' ? { ...a, settings: { dailyUSD: 100 } } : a)) }
    render(<AgentsSection />)
    fireEvent.click((await screen.findAllByTestId('agent-open'))[0]!)
    let drawer = await screen.findByTestId('agent-drawer')
    let hints = within(drawer).getAllByTestId('agent-overall-hint')
    expect(hints.map((h) => `${h.dataset.which}:${h.dataset.tone}`)).toEqual(['daily:warn', 'monthly:info'])
    expect(hints[0]!.textContent).toContain('Its daily cap of $100.00 is higher than the total limit of $20.00 a day, which counts all agents together, and stops it first.')
    expect(hints[1]!.textContent).toContain('The total limit of $200.00 a month still applies to all agents together.')
    expect(hints[0]!.textContent).toContain('Go to Administration › AI › Limits')
    // A cap below the total limit needs no word; an unsaved edit must be saved before leaving.
    fireEvent.change(within(drawer).getByTestId('agent-daily'), { target: { value: '5' } })
    hints = within(drawer).getAllByTestId('agent-overall-hint')
    expect(hints.map((h) => h.dataset.which)).toEqual(['monthly'])
    expect((within(hints[0]!).getByTestId('agent-overall-link') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(within(drawer).getByTestId('agent-daily'), { target: { value: '100' } })
    drawer = screen.getByTestId('agent-drawer')
    fireEvent.click(within(drawer).getAllByTestId('agent-overall-link')[0]!)
    await waitFor(() => expect(screen.queryByTestId('agent-drawer')).toBeNull())
    expect(window.location.hash).toBe('#/admin/ai/limits')
  })
})
