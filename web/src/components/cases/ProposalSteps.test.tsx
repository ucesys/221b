// A proposed change reads as one line per step, under the heading its kind
// of step takes, and says what started it in words (Phase 12, CF-92).
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as cards from '../../api/cards'
import { startedBy, type CardAction, type CardPlan, type Proposal } from '../../api/cards'
import { forgetSharedForTests } from '../../lib/cases'
import { ProposalDrawer } from './ProposalDrawer'
import { ProposalSteps, stepParts } from './ProposalSteps'

vi.mock('../../api/cards', async (original) => ({ ...(await original<typeof import('../../api/cards')>()), fetchProposal: vi.fn() }))

const txt = (el: Element) => (el.textContent ?? '').replace(/\s+/g, ' ').trim()

const fleet = (name: string, labels: string[]): CardAction => ({
  kind: 'fleet.create',
  args: { name, labels, taints: [] },
  impact: `Adds the fleet ${name} of the nodes and hosts labelled ${labels.join(', ')}; nothing on them changes.`,
  risk: 'low',
})
const fleetPlan = (name: string, labels: string[], now: string): CardPlan => ({
  summary: `Create fleet ${name}`,
  targets: [`fleet ${name}`],
  fingerprint: 'absent',
  impact: [now, `Creates the fleet ${name}: the nodes with labels ${labels.join(', ')}.`],
})
const kubectl: CardAction = { kind: 'kubectl', args: { command: 'kubectl rollout restart deployment/web -n shop' }, impact: 'a rolling restart', risk: 'medium' }
const kubectlPlan: CardPlan = { summary: 'kubectl rollout restart deployment/web -n shop', targets: ['deployment shop/web'], impact: ['3 pods replaced, one at a time'], fingerprint: 'rv-41' }

/** PC-22 as it was: a Mycroft inventory proposal of fleets. */
const pc22 = (over: Partial<Proposal> = {}): Proposal =>
  ({
    id: 'PC-22',
    agent: 'mycroft',
    agentName: 'Mycroft',
    source: { kind: 'knowledge', id: 'sandbox-knowledge/nodes.md', label: 'sandbox-knowledge “nodes.md”', setBy: 'run-38, Rebuild now by Super Admin' },
    cluster: 'local',
    clusterName: 'local',
    title: 'Add 2 fleets from “nodes.md”',
    actions: [fleet('burst pool', ['power=on-demand']), fleet('gpu', ['gpu=true'])],
    plans: [fleetPlan('burst pool', ['power=on-demand'], '3 nodes now: w1, w2, w3'), fleetPlan('gpu', ['gpu=true'], '0 nodes now: no node or host has these labels')],
    condition: { kind: 'none' },
    expires: '2099-11-09T02:00:00Z',
    state: 'queued',
    created: '2026-11-08T02:00:00Z',
    updated: '2026-11-08T02:00:00Z',
    needed: 1,
    canApprove: true,
    ...over,
  }) as Proposal

beforeEach(() => {
  Element.prototype.scrollTo = () => {}
  // The object links look their objects up; here nothing answers.
  vi.stubGlobal('fetch', async () => new Response('{}', { status: 404 }))
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
  forgetSharedForTests()
  window.history.replaceState(null, '', '/')
})

describe('one line per step (CF-92)', () => {
  it('says a fleet.create step on one line, with its members now and its risk, the agent’s words and the repeats folded', () => {
    const p = pc22()
    render(<ProposalSteps actions={p.actions} plans={p.plans} cluster="local" open />)
    const steps = screen.getAllByTestId('proposal-step')
    expect(steps.map(txt)).toEqual(['1. Create fleet burst pool: labels power=on-demand · 3 nodes now · risk low', '2. Create fleet gpu: labels gpu=true · 0 nodes now · risk low'])
    expect(within(steps[0]!).getByText('burst pool').tagName).toBe('STRONG')
    // The members are the fact's tooltip; none is a warning.
    const facts = screen.getAllByTestId('proposal-step-fact')
    expect(facts[0]!.getAttribute('title')).toBe('3 nodes now: w1, w2, w3')
    expect(facts[0]!.className).toBe('')
    expect(facts[1]!.className).toBe('tb-err')
    // Nothing under the line repeats it; the agent's words and Watson's sentence wait in Details.
    expect(screen.queryAllByTestId('proposal-step-note')).toHaveLength(0)
    const details = screen.getAllByTestId('proposal-step-details')[0]!
    expect(details.hasAttribute('open')).toBe(false)
    expect(txt(within(details).getByTestId('proposal-step-agent'))).toBe('The agent says: Adds the fleet burst pool of the nodes and hosts labelled power=on-demand; nothing on them changes.')
    expect(txt(details)).toContain('Creates the fleet burst pool: the nodes with labels power=on-demand.')
    expect(txt(details)).toContain('Acts on: fleet burst pool')
    expect(txt(details)).toContain('Fingerprint when proposed: absent')
    // No Watson command runs: the console makes these changes itself.
    expect(screen.getByRole('heading').textContent).toBe('What it changes in the console')
    expect(screen.queryByText('What Watson will run')).toBeNull()
  })

  it('keeps a warning Watson read under the line', () => {
    const plan = { ...fleetPlan('gpu', ['gpu=true'], '2 nodes now: g1, g2'), impact: ['A fleet gpu is there already: approving fails.', '2 nodes now: g1, g2', 'Creates the fleet gpu: the nodes with labels gpu=true.'] }
    render(<ProposalSteps actions={[fleet('gpu', ['gpu=true'])]} plans={[plan]} cluster="local" open />)
    expect(txt(screen.getByTestId('proposal-step'))).toBe('Create fleet gpu: labels gpu=true · 2 nodes now · risk low')
    const note = screen.getByTestId('proposal-step-note')
    expect(txt(note)).toBe('A fleet gpu is there already: approving fails.')
    expect(note.className).toBe('tb-err')
  })

  it('says a kubectl step as its exact command with its object linked, what Watson read under it, under “What Watson will run”', () => {
    render(<ProposalSteps actions={[kubectl]} plans={[kubectlPlan]} cluster="prod" open />)
    expect(txt(screen.getByTestId('proposal-step'))).toBe('$ kubectl rollout restart deployment/web -n shop · deployment shop/web · risk medium')
    expect(txt(screen.getByTestId('proposal-step-command'))).toBe('$ kubectl rollout restart deployment/web -n shop')
    expect(screen.getByTestId('proposal-targets').querySelector('[data-testid=object-link]')).toBeTruthy()
    expect(screen.getAllByTestId('proposal-step-note').map(txt)).toEqual(['3 pods replaced, one at a time'])
    expect(txt(screen.getByTestId('proposal-step-agent'))).toBe('The agent says: a rolling restart')
    expect(screen.getByRole('heading').textContent).toBe('What Watson will run')
  })

  it('has both sections when a proposal both changes the console and runs commands, each step numbered in its order', () => {
    render(<ProposalSteps actions={[fleet('gpu', ['gpu=true']), kubectl]} plans={[fleetPlan('gpu', ['gpu=true'], '2 nodes now: g1, g2'), kubectlPlan]} cluster="prod" open />)
    const sections = screen.getAllByTestId('proposal-steps')
    expect(sections.map((s) => s.getAttribute('aria-label'))).toEqual(['What it changes in the console', 'What Watson will run'])
    expect(txt(within(sections[0]!).getByTestId('proposal-step'))).toMatch(/^1\. Create fleet gpu/)
    expect(txt(within(sections[1]!).getByTestId('proposal-step'))).toMatch(/^2\. \$ kubectl rollout restart/)
  })

  it('says what a decided proposal was to do', () => {
    render(<ProposalSteps actions={[fleet('gpu', ['gpu=true']), kubectl]} plans={[]} cluster="prod" open={false} />)
    expect(screen.getAllByRole('heading').map((h) => h.textContent)).toEqual(['What it was to change in the console', 'What Watson was to run'])
  })

  it('takes a change’s key arguments from what Watson read, and from its arguments without a plan', () => {
    const label: CardAction = { kind: 'host.label', args: { name: 'r820', set: { zone: 'b' }, remove: ['old'] } }
    expect(stepParts(label, { summary: 'Label host r820', impact: ['zone: a → b', 'removes old=1', 'Fleets that select hosts by these labels gain or lose r820.'] })).toMatchObject({
      verb: 'Label host',
      name: 'r820',
      args: 'zone: a → b, removes old=1',
      notes: ['Fleets that select hosts by these labels gain or lose r820.'],
    })
    expect(stepParts(label).args).toBe('zone=b, removes old')
    const params: CardAction = { kind: 'automation.params', args: { fleet: 'Linux Servers', values: { retries: '3' } } }
    expect(stepParts(params, { summary: 'Set Automation parameters of fleet Linux Servers', impact: ['retries: "2" → "3"', 'The fleet’s next runs use them; nothing runs now.'] })).toMatchObject({
      verb: 'Set Automation parameters of fleet',
      name: 'Linux Servers',
      args: 'retries: "2" → "3"',
      notes: ['The fleet’s next runs use them; nothing runs now.'],
    })
    const update: CardAction = { kind: 'fleet.update', args: { id: 'gpu', addNodes: ['g3'] } }
    expect(stepParts(update, { summary: 'Change fleet GPU', impact: ['adds node g3', 'Afterwards it holds a list of 3 nodes: g1, g2, g3.'] })).toMatchObject({ name: 'GPU', args: 'adds node g3', notes: [], more: ['Afterwards it holds a list of 3 nodes: g1, g2, g3.'] })
    expect(stepParts({ kind: 'host.add', args: { name: 'backup-1', address: '10.0.0.5' } }, { summary: 'Add host backup-1 (10.0.0.5)', impact: ['Adds host backup-1 at 10.0.0.5, port 22, login user root.', 'It stays pending until a person pairs it in Add host.'] })).toMatchObject({
      verb: 'Add host',
      name: 'backup-1',
      args: '10.0.0.5',
      notes: ['It stays pending until a person pairs it in Add host.'],
    })
    expect(stepParts({ kind: 'record', args: { type: 'rule' } }, { summary: 'Turn the rule netpol.missing off' }).verb).toBe('Turn the rule netpol.missing off')
  })
})

describe('Started by reads naturally for every source (CF-92)', () => {
  const src = (source: Proposal['source'], agentName = 'Auditor') => startedBy({ source, agentName })
  it('says each kind of source in words', () => {
    expect(src({ kind: 'knowledge', id: 'sandbox-knowledge/nodes.md', label: 'sandbox-knowledge “nodes.md”', setBy: 'run-38, Rebuild now by Super Admin' }, 'Mycroft').text).toBe(
      'Mycroft, from sandbox-knowledge “nodes.md” (run-38, Rebuild now by Super Admin)',
    )
    expect(src({ kind: 'knowledge', id: 'drift', label: 'Drift check against Server inventory' }, 'Mycroft').text).toBe('Mycroft, drift check against Server inventory')
    expect(src({ kind: 'job', id: 'run-12', label: 'Every day at 02:00, UTC', setBy: 'Sam', job: 'Nightly audit' }).text).toBe('Nightly audit, run-12 (Every day at 02:00, UTC; set up by Sam)')
    expect(src({ kind: 'job', id: 'run-12', label: 'Nightly audit', job: 'Nightly audit' }).text).toBe('Nightly audit, run-12')
    // A card made before the job was named says its run's label.
    expect(src({ kind: 'job', id: 'run-7', label: 'Nightly audit' }).text).toBe('Nightly audit, run-7')
    expect(src({ kind: 'alert', id: 'run-5', label: 'Alert BackOff on shop/web at 03:12', setBy: 'Sam', job: 'Crash triage' }).text).toBe('Alert BackOff on shop/web at 03:12 (Crash triage, run-5; set up by Sam)')
    const c = src({ kind: 'case', id: 'C-7', label: 'Case C-7: web crash-loops', setBy: 'Sam', job: 'Case analysis' })
    expect(c).toEqual({ text: 'Case analysis of C-7: web crash-loops', link: 'C-7', title: 'Case C-7: web crash-loops · set by Sam · Case analysis' })
    expect(src({ kind: 'case', id: 'C-7', label: 'Case C-7' }).text).toBe('Case analysis of C-7')
    expect(src({ kind: 'chat', label: 'Asked by Ann in a chat on prod', setBy: 'Ann' }).text).toBe('Asked by Ann in a chat on prod')
  })

  it('shows it in the drawer with no “trigger set by”, a case linked', async () => {
    const p = pc22()
    vi.mocked(cards.fetchProposal).mockResolvedValue(p)
    render(<ProposalDrawer id={p.id} initial={p} onClose={() => undefined} />)
    await waitFor(() => expect(cards.fetchProposal).toHaveBeenCalled())
    const by = screen.getByTestId('proposal-started-by')
    expect(txt(by)).toBe('Mycroft, from sandbox-knowledge “nodes.md” (run-38, Rebuild now by Super Admin)')
    expect(txt(screen.getByTestId('proposal-drawer'))).not.toContain('trigger set by')
    expect(screen.getAllByTestId('proposal-step')).toHaveLength(2)
    cleanup()

    const k = pc22({ id: 'PC-23', source: { kind: 'case', id: 'C-7', label: 'Case C-7: web crash-loops' }, actions: [kubectl], plans: [kubectlPlan] })
    vi.mocked(cards.fetchProposal).mockResolvedValue(k)
    render(<ProposalDrawer id={k.id} initial={k} onClose={() => undefined} />)
    await waitFor(() => expect(cards.fetchProposal).toHaveBeenCalled())
    const caseBy = screen.getByTestId('proposal-started-by')
    expect(txt(caseBy)).toBe('Case analysis of C-7: web crash-loops')
    expect(within(caseBy).getByTestId('proposal-case').getAttribute('href')).toBe('#/cases/cases/7')
    expect(screen.getByRole('region', { name: 'What Watson will run' })).toBeTruthy()
  })
})
