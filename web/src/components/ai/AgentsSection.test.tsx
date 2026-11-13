import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AgentView, AIOverview, Instructions } from '../../api/ai'
import { AgentsSection, worksOn } from './AgentsSection'
import { stageIndex, valuesYAML } from './AgentDrawer'
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
      },
      { id: 'gw', name: 'Gateway', kind: 'openai', model: 'gpt-5', resolved: { kind: 'openai', auth: 'header' }, default: false, locked: false, needsKey: true, key: { set: true, store: 'secret' }, usedBy: [] },
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

  it('lists every agent with its model, cap, clusters, state and on/off', async () => {
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
  it('say where a draft stands, and how to put instructions into the chart', () => {
    expect(stageIndex(instructions())).toBe(3)
    expect(stageIndex(instructions({ draft: { rev: 2, house: '', stage: 'draft', by: 'a', at: '' } }))).toBe(0)
    expect(stageIndex(instructions({ draft: { rev: 2, house: '', stage: 'test', by: 'a', at: '', test: { rev: 2, at: '', by: '', cases: 1, passed: 1, same: 0, costUSD: 0, estimate: 0 } } }))).toBe(1)
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
    fireEvent.click(within(drawer).getByTestId('agent-default-model'))
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
    expect([...list.options].map((o) => o.textContent)).toEqual(['my-old-model (not listed by the provider)', 'qwen3.8-27b · 16,384 tokens'])
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
