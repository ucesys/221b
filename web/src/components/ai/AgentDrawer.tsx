import { useCallback, useEffect, useRef, useState } from 'react'
import {
  ALL_CLUSTERS,
  backToDraft,
  discardDraft,
  estimateTest,
  fetchInstructions,
  fetchModels,
  fetchPilot,
  makeLive,
  MAX_WORDS,
  ratePilot,
  resolveBaseline,
  restoreVersion,
  saveAgent,
  saveDraft,
  startPilot,
  startTest,
  stateLabel,
  stateTone,
  stopPilot,
  testLine,
  tokensText,
  words,
  type AgentChange,
  type CompactMode,
  type AgentView,
  type AIOverview,
  type Instructions,
  type PilotEntry,
  type PromptPart,
  type TestRun,
} from '../../api/ai'
import { usd } from '../../api/sherlock'
import { copyText } from '../../lib/clipboard'
import { lineDiff } from '../../lib/linediff'
import { fmtTs } from '../../lib/format'
import { Card, Drawer } from '../Drawer'
import { Select } from '../Select'
import { AgentJobs } from '../cases/AgentJobs'
import { ModelPicker } from './ModelPicker'
import { useShowCosts } from '../../lib/costs'

/** What each tool lets an agent do, for its checkbox. */
export const TOOL_LABELS: Record<string, string> = {
  cluster_overview: 'Cluster overview',
  list_alerts: 'Alerts',
  list_nodes: 'Nodes',
  get_node: 'Node details',
  list_pods: 'Pods',
  get_pod: 'Pod details',
  pod_logs: 'Logs',
  list_events: 'Events',
  list_objects: 'Objects of a namespace',
  get_object: 'Describe an object',
  get_yaml: 'YAML',
  list_services: 'Services',
  get_service: 'Service details',
  list_helm_releases: 'Helm releases',
  get_helm_release: 'Helm release history',
  top: 'Top (CPU and memory in use)',
  list_fleets: 'Fleets',
  list_storage_classes: 'Storage classes',
  list_volumes: 'Volumes and claims',
  trace_ingress: 'Trace an ingress',
  check_connection: 'Network policy check',
  list_policies: 'Policies (budgets, quotas, Pod Security, webhooks)',
  who_can: 'Who can',
  list_access: 'Access (RBAC)',
  review_cluster: 'Review against best practice',
  node_facts: 'Facts of nodes and hosts',
  automation_runs: 'Automation runs (read only)',
  ask_knowledge: 'Ask knowledge',
  check_command: 'Check commands with Watson',
}

/** Whether the draft serves in a pilot now. */
function piloting(ins: Instructions | null): boolean {
  const d = ins?.draft
  return !!d && d.stage === 'pilot' && !!d.pilot && Date.parse(d.pilot.until) > Date.now()
}

/** Where the draft is on its way to live: the index of STAGES. */
export function stageIndex(ins: Instructions | null): number {
  const d = ins?.draft
  if (!d) return 3
  if (piloting(ins)) return 2
  return d.test && d.test.rev === d.rev ? 1 : 0
}

/** Settings as text to compare, whatever order their keys were set in; an unset key is no key. */
export function canon(o: object): string {
  return JSON.stringify(Object.fromEntries(Object.entries(o).filter(([, v]) => v !== undefined).sort(([a], [b]) => a.localeCompare(b))))
}

/** The values to put the instructions in force into the chart, as YAML. */
export function valuesYAML(id: string, text: string): string {
  const body = text.trim() ? text.trim().split('\n').map((l) => (l ? `      ${l}` : '')).join('\n') : '      '
  return `agents:\n  ${id}:\n    instructions: |\n${body}\n`
}

interface Props {
  agent: AgentView
  ov: AIOverview
  onClose: () => void
  onSaved: (o: AIOverview, msg?: string) => void
  onToast?: (msg: string) => void
}

/**
 * One agent (Phase 12): its model, its instructions in their layers on the
 * way from a draft through a test and a pilot to live, its tools and the
 * agents it may hand work to, what it does on its own, and its budget and
 * clusters.
 */
export function AgentDrawer({ agent, ov, onClose, onSaved, onToast }: Props) {
  const costs = useShowCosts()
  const [set, setSet] = useState<AgentChange>(() => ({ ...agent.settings, ...(agent.id === 'sherlock' ? { sherlockClusters: agent.clusters ?? [], viewControl: ov.viewControl } : {}) }))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [confirmSingle, setConfirmSingle] = useState(false)
  const sherlock = agent.id === 'sherlock'
  const providerId = set.provider || 'default'
  const provider = ov.providers.find((p) => p.id === providerId)
  const author = ov.agents.find((a) => a.id === 'automationAuthor')
  const sameAsAuthor = agent.id === 'scriptReviewer' && (author?.providerId ?? 'default') === providerId && !ov.singleProvider
  const changed = canon(set) !== canon({ ...agent.settings, ...(sherlock ? { sherlockClusters: agent.clusters ?? [], viewControl: ov.viewControl } : {}) })
  const tools = set.tools ?? agent.tools
  const save = async () => {
    setBusy(true)
    setError('')
    try {
      onSaved(await saveAgent(agent.id, { ...set, ...(confirmSingle ? { confirmSingleProvider: true } : {}) }), `Saved the ${agent.name}.`)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const roster = ov.agents.filter((a) => a.id !== agent.id && a.id !== 'sherlock' && a.enabled)
  return (
    <Drawer
      label={`Agent ${agent.name}`}
      kicker="Agent"
      title={agent.name}
      copy={false}
      width={860}
      onClose={onClose}
      testId="agent-drawer"
      facts={
        <>
          <span className="rk-pill" data-tone={agent.enabled ? 'ok' : 'muted'}>
            <span className="rk-dot" />
            {agent.enabled ? 'On' : 'Off'}
          </span>
          <span>{agent.purpose}</span>
        </>
      }
      footer={
        <>
          <button className="rk-btn" data-primary="true" disabled={!changed || busy || (sameAsAuthor && !confirmSingle)} onClick={() => void save()} data-testid="agent-save">
            Save changes
          </button>
          <button className="rk-btn" onClick={onClose} disabled={busy}>
            {changed ? 'Cancel' : 'Close'}
          </button>
          <span className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} role={error ? 'alert' : undefined} style={{ fontSize: 12.5 }} data-testid="agent-save-state">
            {error || (changed ? 'Not saved yet. Instructions are saved as a draft in their own section.' : 'No changes.')}
          </span>
        </>
      }
    >
      <Card title="Model">
        <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600, flex: '1 1 200px' }}>
            Provider
            <Select label="Provider" value={providerId} options={ov.providers.map((p) => [p.id, p.name])} onChange={(v) => setSet({ ...set, provider: v === 'default' ? undefined : v })} testId="agent-provider" width="100%" />
          </label>
          {/* --- AI settings, round 2 (Phase 12, wave 5): the provider's models in a dropdown, and the model's context. --- */}
          <ModelPicker
            label="Model"
            value={set.model || provider?.model || ''}
            disabled={!set.model}
            onChange={(model, listed) => setSet({ ...set, model: model || undefined, contextWindow: listed?.contextWindow || set.contextWindow })}
            load={() => fetchModels(providerId)}
            from={providerId}
            suggestions={ov.models.map((m) => m.id)}
            testId="agent-model"
          />
          {!!set.model && (
            <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
              Context window (tokens)
              <span className="rk-field" style={{ width: 150 }}>
                <input type="number" min={0} step={1024} value={set.contextWindow ?? ''} placeholder="Not known" onChange={(e) => setSet({ ...set, contextWindow: Math.max(0, Math.round(Number(e.target.value) || 0)) || undefined })} data-testid="agent-context" />
              </span>
            </label>
          )}
        </div>
        <label className="rk-tcheck" data-wrap="true" style={{ color: 'var(--fg)' }}>
          <input type="checkbox" checked={!set.model} onChange={(e) => setSet({ ...set, model: e.target.checked ? undefined : provider?.model || ov.defaultModel, contextWindow: e.target.checked ? undefined : set.contextWindow })} data-testid="agent-default-model" />
          <span>
            Use the default model instead ({provider?.model || ov.defaultModel} on {provider?.name ?? 'the default provider'})
          </span>
        </label>
        <ContextLine agent={agent} />
        {sherlock && (
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', fontSize: 12.5 }}>
            <span style={{ fontWeight: 600 }}>Compact form</span>
            <div className="rk-seg" role="group" aria-label="Compact form" data-testid="agent-compact">
              {(
                [
                  ['', 'When the model’s context is small'],
                  ['always', 'Always'],
                  ['never', 'Never'],
                ] as [CompactMode, string][]
              ).map(([v, l]) => (
                <button key={v || 'auto'} aria-pressed={(set.compact ?? '') === v} onClick={() => setSet({ ...set, compact: v || undefined })} data-mode={v || 'auto'}>
                  {l}
                </button>
              ))}
            </div>
            <span className="rk-fhint" style={{ flex: '1 1 100%' }}>
              The compact form is a short core, the essential reads only, no hand-overs, no skills and no view changes: for an in-house model whose context the whole form does not fit.
            </span>
          </div>
        )}
        {agent.unusable && (
          <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }}>
            {agent.unusable}
          </p>
        )}
        {sameAsAuthor && (
          <div className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="agent-same-provider">
            <span>The Script reviewer is a second opinion on what the Automation author wrote: on the same provider, it shares the author’s blind spots. Pick another provider, or confirm this console has only one.</span>
            <label className="rk-tcheck" data-wrap="true" style={{ color: 'var(--fg)' }}>
              <input type="checkbox" checked={confirmSingle} onChange={(e) => setConfirmSingle(e.target.checked)} data-testid="agent-confirm-single" />
              <span>This console has only one provider</span>
            </label>
          </div>
        )}
      </Card>
      <InstructionsCard agent={agent} ov={ov} onToast={onToast} />
      <Card title="Tools and hand-overs">
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(min(220px, 100%), 1fr))', gap: '4px 12px' }} role="group" aria-label={`${agent.name}'s tools`}>
          {agent.tools.map((t) => (
            <label key={t} className="rk-tcheck" data-wrap="true" style={{ color: 'var(--fg)' }}>
              <input
                type="checkbox"
                checked={tools.includes(t)}
                onChange={(e) => {
                  const next = e.target.checked ? agent.tools.filter((x) => x === t || tools.includes(x)) : tools.filter((x) => x !== t)
                  setSet({ ...set, tools: next.length === agent.tools.length ? undefined : next })
                }}
                data-testid="agent-tool"
                data-tool={t}
              />
              <span>{TOOL_LABELS[t] ?? t}</span>
            </label>
          ))}
        </div>
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="agent-handovers">
          Only this purpose’s tools can be given. {roster.length ? `Hands work over to ${roster.map((a) => a.name).join(', ')}` : 'No other agent is on to hand work to'}, at most three deep, each on its own budget.{' '}
          {sherlock ? 'It alone may change the person’s view.' : 'It cannot change your view: it suggests one, and Sherlock applies it.'}
        </p>
        {sherlock && (
          <label className="rk-tcheck" data-wrap="true" style={{ color: 'var(--fg)' }}>
            <input type="checkbox" checked={set.viewControl !== false} onChange={(e) => setSet({ ...set, viewControl: e.target.checked })} data-testid="agent-view-control" />
            <span>Let Sherlock change the asking person’s view: open tabs, set filters, highlight rows. Undo puts it back.</span>
          </label>
        )}
      </Card>
      <Card title="On its own">
        <p style={{ margin: 0, fontSize: 12.5 }}>
          {sherlock ? 'Sherlock answers when someone asks.' : `The ${agent.name} works when another agent hands it a part of a question.`} Jobs that run an agent by themselves, on new alerts or on a schedule, are set in{' '}
          <a className="rk-link" href="#/cases/jobs" data-testid="agent-jobs-link">
            Cases › Jobs
          </a>
          .
        </p>
        {/* --- Jobs (Phase 12) --- */}
        <AgentJobs agent={agent.id} />
      </Card>
      <Card title="Budget and clusters">
        <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
            Daily cap (US$)
            <span className="rk-field" style={{ width: 140 }}>
              <input type="number" min={0} step={1} value={set.dailyUSD ?? 0} onChange={(e) => setSet({ ...set, dailyUSD: Math.max(0, Number(e.target.value) || 0) || undefined })} data-testid="agent-daily" />
            </span>
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
            Monthly cap (US$)
            <span className="rk-field" style={{ width: 140 }}>
              <input type="number" min={0} step={1} value={set.monthlyUSD ?? 0} onChange={(e) => setSet({ ...set, monthlyUSD: Math.max(0, Number(e.target.value) || 0) || undefined })} data-testid="agent-monthly" />
            </span>
          </label>
        </div>
        <p className="rk-fhint" style={{ margin: 0 }}>
          0 is no cap of its own. Its spend counts toward the overall limits too{costs ? ` (${usd(ov.dailyUSD)} a day). Spent: ${usd(agent.spend.dayUSD)} today, ${usd(agent.spend.monthUSD)} this month.` : '.'}
        </p>
        {sherlock ? (
          <div role="group" aria-label="Clusters Sherlock answers about" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            <span style={{ fontSize: 12.5, fontWeight: 600 }}>On for</span>
            {ov.clusters.map((c) => (
              <label key={c.id} className="rk-tcheck" data-wrap="true" style={{ color: 'var(--fg)' }}>
                <input
                  type="checkbox"
                  checked={(set.sherlockClusters ?? []).includes(c.id)}
                  onChange={(e) => setSet({ ...set, sherlockClusters: e.target.checked ? [...(set.sherlockClusters ?? []), c.id] : (set.sherlockClusters ?? []).filter((x) => x !== c.id) })}
                  data-testid="agent-cluster"
                  data-cluster={c.id}
                />
                {c.name}
                {c.env ? <span className="rk-fhint"> · {c.env}</span> : null}
              </label>
            ))}
            <span className="rk-fhint">Nothing is sent to a model for a cluster Sherlock is off for.</span>
          </div>
        ) : (
          <div role="group" aria-label="Clusters it works on" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            <span style={{ fontSize: 12.5, fontWeight: 600 }}>Works on</span>
            <div className="rk-seg" role="group" aria-label="Works on" style={{ alignSelf: 'flex-start' }}>
              <button aria-pressed={!set.clusters?.length} onClick={() => setSet({ ...set, clusters: undefined })} data-testid="agent-all-clusters">
                All clusters
              </button>
              <button aria-pressed={!!set.clusters?.length} onClick={() => setSet({ ...set, clusters: set.clusters?.length ? set.clusters : ov.clusters.slice(0, 1).map((c) => c.id) })} data-testid="agent-some-clusters">
                {set.clusters?.length ? set.clusters.map((id) => ov.clusters.find((c) => c.id === id)?.name ?? id).join(', ') : 'Chosen clusters'}
              </button>
            </div>
            {!!set.clusters?.length &&
              ov.clusters.map((c) => (
                <label key={c.id} className="rk-tcheck" data-wrap="true" style={{ color: 'var(--fg)' }}>
                  <input type="checkbox" checked={set.clusters!.includes(c.id)} onChange={(e) => setSet({ ...set, clusters: e.target.checked ? [...set.clusters!, c.id] : set.clusters!.filter((x) => x !== c.id) })} data-testid="agent-cluster" data-cluster={c.id} />
                  {c.name}
                </label>
              ))}
          </div>
        )}
      </Card>
    </Drawer>
  )
}

/** "about 10,259 tokens (3,449 of instructions, 6,810 for 38 tools)". */
function sizeText(f: { instructions: number; tools: number; toolCount: number }): string {
  return `about ${tokensText(f.instructions + f.tools)} (${f.instructions.toLocaleString('en')} of instructions, ${f.tools.toLocaleString('en')} for ${f.toolCount} tool${f.toolCount === 1 ? '' : 's'})`
}

/**
 * What the agent's instructions and tools take of its model's context
 * (Phase 12, wave 5), and for Sherlock whether it answers in its compact
 * form. As saved: a change shows once it is saved.
 */
export function ContextLine({ agent }: { agent: AgentView }) {
  const f = agent.footprint
  if (!f) return <p className="rk-fhint" style={{ margin: 0 }} data-testid="agent-context-line">How much of the model’s context its instructions take shows once the agent can answer.</p>
  const total = f.instructions + f.tools
  const whole = f.whole.instructions + f.whole.tools
  const of = f.window ? ` of ${agent.model}’s ${tokensText(f.window)} (${Math.round((100 * total) / f.window)}%)` : ''
  const parts = [`Instructions and tools: ${sizeText(f)}${of}.`]
  const tone = !f.compact && f.window && whole > f.share * f.window ? 'warn' : undefined
  if (!f.window) parts.push(agent.defaultModel ? 'The model’s context window is not known: set it on its provider, in Settings › Providers.' : 'The model’s context window is not known: set it above.')
  if (agent.id === 'sherlock') {
    if (f.compact && f.mode === 'always') parts.push(`Sherlock answers in its compact form, as chosen; its whole form is ${sizeText(f.whole)}.`)
    else if (f.compact) parts.push(`Its whole form, ${sizeText(f.whole)}, takes more than ${Math.round(f.share * 100)}% of the context, so Sherlock answers in its compact form.`)
    else if (tone) parts.push(`That is more than ${Math.round(f.share * 100)}% of the context: answers may not fit. Let it use its compact form when the context is small.`)
    else parts.push('Sherlock answers in its whole form.')
  }
  if (f.maxTokens) parts.push(`A reply is bounded at ${tokensText(f.maxTokens)} to fit.`)
  return (
    <p className={tone ? 'rk-callout' : 'rk-fhint'} data-tone={tone} style={{ margin: 0, fontSize: 12.5 }} data-testid="agent-context-line" data-compact={f.compact ? 'true' : 'false'}>
      {parts.join(' ')}
    </p>
  )
}

/**
 * The agent's instructions: the house instructions and the notes per
 * cluster, where they stand against the chart's baseline, and their way
 * from a draft through a test and a pilot to live.
 */
function InstructionsCard({ agent, ov, onToast }: { agent: AgentView; ov: AIOverview; onToast?: (msg: string) => void }) {
  const costs = useShowCosts()
  const [ins, setIns] = useState<Instructions | null>(null)
  const [error, setError] = useState('')
  const [house, setHouse] = useState<string | null>(null)
  const [notes, setNotes] = useState<Record<string, string> | null>(null)
  const [cluster, setCluster] = useState(ALL_CLUSTERS)
  const [compare, setCompare] = useState(false)
  const [busy, setBusy] = useState(false)
  const [panel, setPanel] = useState<'' | 'test' | 'pilot' | 'live'>('')
  const [warnings, setWarnings] = useState<string[]>([])
  // --- AI settings, round 2 (Phase 12, wave 5): the built-in text, pre-filled and editable. ---
  const [parts, setParts] = useState<Record<string, string> | null>(null)
  const [partTab, setPartTab] = useState<PromptPart['name']>('core')
  const [partCompare, setPartCompare] = useState(false)
  const load = useCallback(async () => {
    try {
      const v = await fetchInstructions(agent.id)
      setIns(v)
      return v
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      return null
    }
  }, [agent.id])
  useEffect(() => {
    fetchInstructions(agent.id).then(setIns, (e: Error) => setError(e.message))
  }, [agent.id])
  // While its tests run, the result is read again every few seconds.
  const testing = useRef(false)
  useEffect(() => {
    if (!agent.testing && !testing.current) return
    const t = window.setInterval(() => {
      void load().then((v) => {
        if (v?.draft?.test && v.draft.test.rev === v.draft.rev) testing.current = false
      })
    }, 2000)
    return () => window.clearInterval(t)
  }, [agent.testing, load, panel])
  if (!ins) return <Card title="Instructions">{<p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>{error || 'Loading the instructions…'}</p>}</Card>
  const d = ins.draft
  const text = house ?? d?.house ?? ins.house
  const n = notes ?? d?.notes ?? ins.notes ?? {}
  const partText = (p: PromptPart) => parts?.[p.name] ?? p.draft ?? p.text
  const allParts = () => Object.fromEntries((ins.parts ?? []).map((p) => [p.name, partText(p)]))
  const part = (ins.parts ?? []).find((p) => p.name === partTab) ?? ins.parts?.[0]
  const edited = house !== null || notes !== null || parts !== null
  const act = async (fn: () => Promise<Instructions | void>, msg?: string) => {
    setBusy(true)
    setError('')
    try {
      const v = await fn()
      if (v) setIns(v)
      else await load()
      if (msg) onToast?.(msg)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const clusterName = (id: string) => (id === ALL_CLUSTERS ? 'every cluster' : (ov.clusters.find((c) => c.id === id)?.name ?? id))
  return (
    <Card
      title="Instructions"
      aside={
        <span style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <span className="rk-pill" data-tone={stateTone(ins.state)} data-testid="ins-state">
            {stateLabel(ins.state)}
          </span>
          {d && <span className="rk-pill" data-tone="muted">Draft {d.rev}{ins.live ? ` of version ${ins.live + 1}` : ''}</span>}
        </span>
      }
    >
      <p className="rk-fhint" style={{ margin: 0 }}>
        The built-in instructions are shown as this release ships them; an edit replaces them whole for this agent. Whatever they say, text from the cluster stays data and secrets never leave the server. House instructions and notes are added after them. Facts about systems belong in Knowledge.
      </p>
      {part && <PartEditor agent={agent} part={part} parts={ins.parts} tab={partTab} onTab={setPartTab} text={partText(part)} max={ins.maxPartWords} compare={partCompare} onCompare={() => setPartCompare(!partCompare)} onChange={(t) => setParts({ ...allParts(), [part.name]: t })} />}
      {ins.state === 'baseline-changed' && (
        <div className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="ins-baseline-changed">
          <span>An upgrade brought new baseline instructions from the chart while your edit is in force. Nothing replaces either until you choose: keep your edit, take the baseline, or merge them by editing below and saving a draft.</span>
          <span style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
            <button className="rk-btn" disabled={busy} onClick={() => void act(() => resolveBaseline(agent.id, 'keep'), 'Kept your edit.')} data-testid="ins-keep">
              Keep my edit
            </button>
            <button className="rk-btn" disabled={busy} onClick={() => void act(() => resolveBaseline(agent.id, 'take'), 'Took the chart’s baseline.')} data-testid="ins-take">
              Take the baseline
            </button>
            <button className="rk-btn" disabled={busy} onClick={() => setCompare(true)}>
              Compare
            </button>
          </span>
        </div>
      )}
      <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
        House instructions
        <span className="rk-fhint" style={{ fontWeight: 400 }}>
          Your team’s own, added after the built-in instructions; the chart can set them ({ins.valuesKey}).
        </span>
        <textarea className="rk-area" rows={7} spellCheck value={text} placeholder="Your team’s conventions: runbooks to cite, services to check first" onChange={(e) => setHouse(e.target.value)} data-testid="ins-house" style={{ width: '100%', boxSizing: 'border-box', fontWeight: 400, fontSize: 12.5, lineHeight: 1.5, resize: 'vertical' }} />
        <span className="rk-fhint" style={{ fontWeight: 400 }} data-tone={words(text) > MAX_WORDS ? 'err' : undefined}>
          {words(text)} of {MAX_WORDS.toLocaleString('en')} words.
        </span>
      </label>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <span style={{ display: 'flex', gap: 8, alignItems: 'center', fontSize: 12.5, fontWeight: 600 }}>
          Notes for
          <Select label="Notes for the cluster" value={cluster} options={[[ALL_CLUSTERS, 'Every cluster'], ...ov.clusters.map((c): [string, string] => [c.id, `${c.name} only${n[c.id] ? ' ·' : ''}`])]} onChange={setCluster} testId="ins-notes-cluster" />
        </span>
        <textarea className="rk-area" rows={3} value={n[cluster] ?? ''} placeholder="Maintenance window: Sundays 02:00–04:00" onChange={(e) => setNotes({ ...n, [cluster]: e.target.value })} aria-label={`Notes for ${clusterName(cluster)}`} data-testid="ins-notes" style={{ width: '100%', boxSizing: 'border-box', fontSize: 12.5, lineHeight: 1.5, resize: 'vertical' }} />
      </div>
      {(warnings.length > 0 || (!edited && (ins.warnings?.length ?? 0) > 0)) && (
        <ul className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12, paddingLeft: 24 }} data-testid="ins-warnings">
          {(warnings.length ? warnings : ins.warnings!).map((w) => (
            <li key={w}>{w}</li>
          ))}
        </ul>
      )}
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', fontSize: 12.5 }}>
        <span>
          Helm value: <span className="rk-mono" data-testid="ins-values-key">{ins.valuesKey}</span>
        </span>
        <button className="rk-btn" onClick={() => void copyText(valuesYAML(agent.id, ins.house)).then((ok) => onToast?.(ok ? `Copied the values for ${ins.valuesKey}: paste them into your values file.` : 'The browser refused the clipboard.'))} data-testid="ins-copy">
          Copy
        </button>
        <button className="rk-btn" aria-expanded={compare} onClick={() => setCompare(!compare)} data-testid="ins-compare">
          Compare with baseline
        </button>
      </div>
      {compare && ins.state === 'baseline-changed' && ins.previousBaseline !== undefined && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <Diff label="What the upgrade changed in the baseline" from={ins.previousBaseline} to={ins.baseline} testId="ins-diff-upgrade" />
          <Diff label="Your edit against the new baseline" from={ins.baseline} to={text} testId="ins-diff-edit" />
        </div>
      )}
      {compare && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(260px, 1fr))', gap: 8 }} data-testid="ins-diff">
          <div>
            <div className="rk-fhint">The chart’s baseline</div>
            <pre className="rk-code rk-mono" style={{ margin: 0, padding: '8px 10px', fontSize: 12, whiteSpace: 'pre-wrap', background: 'var(--sunken)', borderRadius: 8, border: '1px solid var(--line)' }}>{ins.baseline || '(none: the chart sets no house instructions)'}</pre>
          </div>
          <div>
            <div className="rk-fhint">{d || edited ? 'Your draft' : 'In force'}</div>
            <pre className="rk-code rk-mono" style={{ margin: 0, padding: '8px 10px', fontSize: 12, whiteSpace: 'pre-wrap', background: 'var(--sunken)', borderRadius: 8, border: '1px solid var(--line)' }}>{text || '(none)'}</pre>
          </div>
        </div>
      )}
      <Stepper
        ins={ins}
        edited={edited}
        busy={busy}
        testing={!!agent.testing}
        panel={panel}
        onPanel={(p) => setPanel(panel === p ? '' : p)}
        onBack={() => void act(() => backToDraft(agent.id), 'The draft is back at its first stage: test it again.')}
        onStopPilot={() => void act(() => stopPilot(agent.id), 'Stopped the pilot.')}
      />
      {d?.test && (
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="ins-last-test">
          Last test {fmtTs(d.test.at)}: {testLine(d.test, costs ? usd : undefined)}
          {d.test.rev !== d.rev ? ' (of an earlier draft)' : ''}
        </p>
      )}
      {d?.test?.results?.some((r) => !r.passed) && d.test.rev === d.rev && <Failures run={d.test} />}
      {ins.baselineCheck && <p className="rk-fhint" style={{ margin: 0 }}>The chart’s new baseline was tested at start, {fmtTs(ins.baselineCheck.at)}: {testLine(ins.baselineCheck, costs ? usd : undefined)}</p>}
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="ins-error">
          {error}
        </p>
      )}
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <button
          className="rk-btn"
          data-primary="true"
          disabled={busy || !edited || words(text) > MAX_WORDS || (ins.parts ?? []).some((p) => words(partText(p)) > ins.maxPartWords)}
          onClick={() =>
            void act(async () => {
              const v = await saveDraft(agent.id, text, n, allParts())
              setHouse(null)
              setNotes(null)
              setParts(null)
              setWarnings(v.warnings ?? [])
              return v
            }, 'Saved the draft.')
          }
          data-testid="ins-save-draft"
        >
          Save draft
        </button>
        {d && (
          <button className="rk-link" disabled={busy} onClick={() => void act(() => discardDraft(agent.id), 'Discarded the draft.')} data-testid="ins-discard" style={{ fontSize: 12.5 }}>
            Discard the draft
          </button>
        )}
        {edited && (
          <button
            className="rk-link"
            onClick={() => {
              setHouse(null)
              setNotes(null)
              setParts(null)
            }}
            style={{ fontSize: 12.5 }}
          >
            Undo edits
          </button>
        )}
      </div>
      {panel === 'test' && (
        <TestPanel
          id={agent.id}
          onStarted={() => {
            testing.current = true
            setPanel('')
            onToast?.('The tests run in the background; their result shows here.')
            void load()
          }}
        />
      )}
      {panel === 'pilot' && d && <PilotPanel id={agent.id} ins={ins} ov={ov} onDone={(v) => { setIns(v); setPanel('') }} />}
      {panel === 'live' && (
        <div className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="ins-live-confirm">
          <span>The draft becomes the {agent.name}’s instructions for everyone, as version {(ins.versions[0]?.n ?? 0) + 1}. The version in force now is kept, to restore in one click.</span>
          <span style={{ display: 'flex', gap: 8 }}>
            <button
              className="rk-btn"
              data-primary="true"
              disabled={busy}
              onClick={() =>
                void act(async () => {
                  const r = await makeLive(agent.id)
                  setPanel('')
                  onToast?.(r.waiting ? `Waiting for another SuperAdmin to approve request ${r.request?.id} on Approvals.` : `Live as version ${r.live?.n}.`)
                })
              }
              data-testid="ins-live-go"
            >
              Make it live
            </button>
            <button className="rk-btn" onClick={() => setPanel('')}>
              Cancel
            </button>
          </span>
        </div>
      )}
      {d?.stage === 'pilot' && d.pilot && <PilotLog id={agent.id} />}
      {ins.versions.length > 0 && (
        <details>
          <summary style={{ fontSize: 12.5, fontWeight: 600, cursor: 'pointer' }}>Versions ({ins.versions.length})</summary>
          <ul style={{ margin: '8px 0 0', padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="ins-versions">
            {ins.versions.map((v) => (
              <li key={v.n} style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', fontSize: 12.5 }}>
                <strong>Version {v.n}</strong>
                <span className="rk-fhint">
                  {v.why ?? 'made live'} by {v.by}, {fmtTs(v.at)}
                </span>
                {v.n === ins.live ? (
                  <span className="rk-pill" data-tone="ok">In force</span>
                ) : (
                  <button className="rk-btn" disabled={busy || edited} onClick={() => void act(async () => {
                    const r = await restoreVersion(agent.id, v.n)
                    onToast?.(r.waiting ? `Version ${v.n} waits for another SuperAdmin on Approvals.` : `Version ${v.n} is live again, as version ${r.live?.n}.`)
                  })} data-testid="ins-restore">
                    Restore
                  </button>
                )}
              </li>
            ))}
          </ul>
        </details>
      )}
    </Card>
  )
}

/**
 * One part of the agent's built-in text (Phase 12, wave 5): pre-filled
 * with the text in force (or the draft's), editable, reset to the built-in
 * text, and compared with it. Sherlock has two: its whole form and its
 * compact form.
 */
function PartEditor({ agent, part, parts, tab, onTab, text, max, compare, onCompare, onChange }: { agent: AgentView; part: PromptPart; parts: PromptPart[]; tab: string; onTab: (t: PromptPart['name']) => void; text: string; max: number; compare: boolean; onCompare: () => void; onChange: (t: string) => void }) {
  const isBuiltIn = text.trim() === part.builtIn.trim()
  const state = isBuiltIn ? 'Built-in' : part.edited && text.trim() === part.text.trim() ? 'Edited' : 'Edited, not saved'
  return (
    <div role="group" aria-label={part.label} style={{ display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="ins-part-group">
      <span style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', fontSize: 12.5, fontWeight: 600 }}>
        Built-in instructions
        <span className="rk-pill" data-tone={isBuiltIn ? 'ok' : 'info'} data-testid="ins-part-state">
          {state}
        </span>
        {parts.length > 1 && (
          <span className="rk-seg" role="group" aria-label="Which form" style={{ marginLeft: 'auto' }}>
            {parts.map((p) => (
              <button key={p.name} aria-pressed={tab === p.name} onClick={() => onTab(p.name)} data-testid="ins-part-tab" data-part={p.name}>
                {p.name === 'compact' ? 'Compact form' : 'Whole form'}
              </button>
            ))}
          </span>
        )}
      </span>
      {part.name === 'compact' && <span className="rk-fhint">What {agent.name} is told on a model whose context is small, in place of its whole form.</span>}
      {part.builtInChanged && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="ins-part-changed">
          The built-in text changed since this edit was made (an upgrade{agent.id === 'sherlock' ? ', or view changes turned on or off' : ''}). Compare them, then keep your edit or reset it.
        </p>
      )}
      <textarea className="rk-area" rows={12} spellCheck value={text} onChange={(e) => onChange(e.target.value)} aria-label={part.label} data-testid="ins-part" data-part={part.name} style={{ width: '100%', boxSizing: 'border-box', fontSize: 12.5, lineHeight: 1.5, resize: 'vertical' }} />
      <span style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        <span className="rk-fhint" data-tone={words(text) > max ? 'err' : undefined}>
          {words(text).toLocaleString('en')} of {max.toLocaleString('en')} words.
        </span>
        <button className="rk-btn" disabled={isBuiltIn} title={isBuiltIn ? 'It is the built-in text' : undefined} onClick={() => onChange(part.builtIn)} data-testid="ins-part-reset">
          Reset to built-in
        </button>
        <button className="rk-btn" aria-expanded={compare} onClick={onCompare} data-testid="ins-part-compare">
          Compare with built-in
        </button>
      </span>
      {compare && <Diff label="Against the built-in text" from={part.builtIn} to={text} testId="ins-part-diff" />}
    </div>
  )
}

/** What each stage on the way to live is, in one line. */
const STAGE_TEXT: Record<string, string> = {
  draft: 'Edits saved as a draft change nothing for anyone yet.',
  test: 'The draft answers the recorded cases and recent questions again; nothing reads the cluster.',
  pilot: 'The tested draft answers for chosen people or clusters for a while; both versions are logged, to rate.',
  live: 'In force for everyone; the version before is kept, to restore in one click. A second SuperAdmin approves where there is one.',
}

/**
 * Draft → Test → Pilot → Live as steps that act (Phase 12, wave 5): each
 * says what it is and offers its action, disabled with the reason when it
 * cannot be done now.
 */
function Stepper({ ins, edited, busy, testing, panel, onPanel, onBack, onStopPilot }: { ins: Instructions; edited: boolean; busy: boolean; testing: boolean; panel: string; onPanel: (p: 'test' | 'pilot' | 'live') => void; onBack: () => void; onStopPilot: () => void }) {
  const d = ins.draft
  const stage = stageIndex(ins)
  const pil = piloting(ins)
  const common = edited ? 'Save your edits first.' : !d ? 'Save a draft first: edit the instructions above.' : ''
  const steps: { name: string; action: string; testId: string; why: string; onClick: () => void; open?: boolean; note?: string }[] = [
    { name: 'Draft', action: 'Back to draft', testId: 'ins-back', why: common || (stage === 0 ? 'The draft is at this stage now.' : ''), onClick: onBack, note: d ? `Draft ${d.rev}, saved by ${d.by}, ${fmtTs(d.at)}.` : undefined },
    { name: 'Test', action: d?.test ? 'Run tests again' : 'Run tests', testId: 'ins-test', why: common || (testing ? 'The tests are running.' : ''), onClick: () => onPanel('test'), open: panel === 'test' },
    { name: 'Pilot', action: pil ? 'Stop pilot' : 'Start pilot', testId: 'ins-pilot', why: common || (stage < 1 ? 'Run the tests first: a pilot serves a tested draft.' : ''), onClick: pil ? onStopPilot : () => onPanel('pilot'), open: panel === 'pilot', note: pil && d?.pilot ? `In pilot until ${fmtTs(d.pilot.until)}.` : undefined },
    {
      name: 'Live',
      action: 'Make live',
      testId: 'ins-live',
      why: edited ? common : !d ? `Nothing waits to go live: ${ins.live ? `version ${ins.live} is in force` : 'the built-in and chart instructions are in force'}. Edit and save a draft.` : d.request ? `Waiting for another SuperAdmin: request ${d.request} on Approvals in Cases › Inbox.` : stage < 1 && !d.restored ? 'Run the tests first.' : '',
      onClick: () => onPanel('live'),
      open: panel === 'live',
    },
  ]
  return (
    <ol aria-label="Draft, test, pilot, live" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(180px, 100%), 1fr))', gap: 8, listStyle: 'none', margin: 0, padding: 0 }} data-testid="ins-stages">
      {steps.map((st, i) => {
        const state = i < stage ? 'done' : i === stage ? 'current' : 'todo'
        return (
          <li
            key={st.name}
            aria-current={state === 'current' ? 'step' : undefined}
            data-stage={st.name.toLowerCase()}
            data-state={state}
            style={{ display: 'flex', flexDirection: 'column', gap: 6, padding: '8px 10px', borderRadius: 8, border: `1px solid ${state === 'current' ? 'var(--accent)' : 'var(--line)'}`, background: state === 'current' ? 'var(--sunken)' : undefined, minWidth: 0, fontSize: 12.5 }}
          >
            <span style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
              <span className="rk-pill" data-tone={state === 'done' ? 'ok' : state === 'current' ? 'info' : 'muted'}>
                {i + 1}
              </span>
              <strong data-testid="ins-step-name">{st.name}</strong>
              {state === 'done' && <span className="rk-fhint">done</span>}
              {state === 'current' && <span className="rk-fhint">now</span>}
            </span>
            <span className="rk-fhint">{STAGE_TEXT[st.name.toLowerCase()]}</span>
            {st.note && <span className="rk-fhint">{st.note}</span>}
            <span>
              <button className="rk-btn" data-primary={!st.why && i === stage + 1 ? 'true' : undefined} disabled={busy || !!st.why} aria-expanded={st.open} title={st.why || undefined} onClick={st.onClick} data-testid={st.testId}>
                {st.action}
              </button>
            </span>
            {st.why && (
              <span className="rk-fhint" data-testid="ins-step-why">
                {st.why}
              </span>
            )}
          </li>
        )
      })}
    </ol>
  )
}

/** A line diff of two texts, as Automation shows a script's versions. */
function Diff({ label, from, to, testId }: { label: string; from: string; to: string; testId: string }) {
  const d = lineDiff(from, to)
  return (
    <div data-testid={testId}>
      <div className="rk-fhint">
        {label}: {d.added || d.removed ? `+${d.added} −${d.removed} lines` : 'no change'}
      </div>
      {d.text && (
        <pre className="rk-code rk-mono" style={{ margin: 0, padding: '8px 10px', fontSize: 12, whiteSpace: 'pre-wrap', background: 'var(--sunken)', borderRadius: 8, border: '1px solid var(--line)' }}>
          {d.text}
        </pre>
      )}
    </div>
  )
}

function Failures({ run }: { run: TestRun }) {
  return (
    <details>
      <summary style={{ fontSize: 12.5, cursor: 'pointer' }}>What failed</summary>
      <ul style={{ margin: '6px 0 0', paddingLeft: 18, fontSize: 12 }} data-testid="ins-failures">
        {run.results!
          .filter((r) => !r.passed)
          .map((r) => (
            <li key={r.id || r.name}>
              <strong>{r.name}</strong>: {(r.why ?? []).join('; ')}
            </li>
          ))}
      </ul>
    </details>
  )
}

/** The cost of a test first, then the test, which runs in the background. */
function TestPanel({ id, onStarted }: { id: string; onStarted: () => void }) {
  const costs = useShowCosts()
  const [replays, setReplays] = useState(10)
  const [est, setEst] = useState<{ estimateUSD: number; cases: number; replays: number } | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    estimateTest(id, replays)
      .then(setEst)
      .catch((e: Error) => setError(e.message))
  }, [id, replays])
  return (
    <div className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="ins-test-panel">
      <span>The draft answers the recorded test cases with their recorded tool results, and the latest real questions again, side by side with what was answered; nothing reads the cluster, and nothing proposed is filed.</span>
      <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        Replay the last
        <Select label="Conversations to replay" value={String(replays)} options={[['0', 'no'], ['5', '5'], ['10', '10'], ['20', '20']]} onChange={(v) => setReplays(Number(v))} />
        conversations
      </label>
      <span data-testid="ins-test-estimate">{error || (est ? `${est.cases} recorded cases and ${est.replays} replays${costs ? `: about ${usd(est.estimateUSD)}, charged to this agent` : ', charged to this agent'}.` : costs ? 'Working out the cost…' : 'Counting the cases…')}</span>
      <span>
        <button className="rk-btn" data-primary="true" disabled={!est} onClick={() => void startTest(id, replays).then(onStarted, (e: Error) => setError(e.message))} data-testid="ins-test-run">
          Run the test
        </button>
      </span>
    </div>
  )
}

function PilotPanel({ id, ins, ov, onDone }: { id: string; ins: Instructions; ov: AIOverview; onDone: (v: Instructions) => void }) {
  const p = ins.draft?.pilot
  const [people, setPeople] = useState((p?.people ?? []).join(', '))
  const [clusters, setClusters] = useState<string[]>(p?.clusters ?? [])
  const [hours, setHours] = useState(72)
  const [error, setError] = useState('')
  const live = piloting(ins)
  return (
    <div className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="ins-pilot-panel">
      {live ? (
        <>
          <span>In pilot until {fmtTs(p!.until)} for {[...(p!.people ?? []), ...(p!.clusters ?? []).map((c) => ov.clusters.find((x) => x.id === c)?.name ?? c)].join(', ')}.</span>
          <span>
            <button className="rk-btn" onClick={() => void stopPilot(id).then(onDone, (e: Error) => setError(e.message))} data-testid="ins-pilot-stop">
              Stop the pilot
            </button>
          </span>
        </>
      ) : (
        <>
          <span>The draft answers for the people or clusters chosen, for a while; the answers of both versions are logged meanwhile, to rate.</span>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            People (their email addresses or user names, separated by commas)
            <span className="rk-field">
              <input value={people} onChange={(e) => setPeople(e.target.value)} data-testid="ins-pilot-people" />
            </span>
          </label>
          <div role="group" aria-label="Clusters" style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
            {ov.clusters.map((c) => (
              <label key={c.id} className="rk-tcheck" data-wrap="true" style={{ color: 'var(--fg)' }}>
                <input type="checkbox" checked={clusters.includes(c.id)} onChange={(e) => setClusters(e.target.checked ? [...clusters, c.id] : clusters.filter((x) => x !== c.id))} data-testid="ins-pilot-cluster" data-cluster={c.id} />
                {c.name}
              </label>
            ))}
          </div>
          <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            For
            <Select label="For how long" value={String(hours)} options={[['24', 'a day'], ['72', 'three days'], ['168', 'a week'], ['336', 'two weeks']]} onChange={(v) => setHours(Number(v))} />
          </label>
          <span>
            <button
              className="rk-btn"
              data-primary="true"
              disabled={!people.trim() && !clusters.length}
              onClick={() =>
                void startPilot(
                  id,
                  people
                    .split(',')
                    .map((x) => x.trim())
                    .filter(Boolean),
                  clusters,
                  hours,
                ).then(onDone, (e: Error) => setError(e.message))
              }
              data-testid="ins-pilot-start"
            >
              Start the pilot
            </button>
          </span>
        </>
      )}
      {error && (
        <span className="rk-t" data-tone="err" role="alert">
          {error}
        </span>
      )}
    </div>
  )
}

/** The answers logged during a pilot, both versions', to rate. */
function PilotLog({ id }: { id: string }) {
  const costs = useShowCosts()
  const [entries, setEntries] = useState<PilotEntry[]>([])
  useEffect(() => {
    fetchPilot(id)
      .then((r) => setEntries(r.entries))
      .catch(() => setEntries([]))
  }, [id])
  if (!entries.length) return <p className="rk-fhint" style={{ margin: 0 }}>No answer logged in this pilot yet.</p>
  const rate = async (e: PilotEntry, r: number) => {
    await ratePilot(e.n, e.rating === r ? 0 : r)
    setEntries(entries.map((x) => (x.n === e.n ? { ...x, rating: e.rating === r ? 0 : r } : x)))
  }
  return (
    <details>
      <summary style={{ fontSize: 12.5, fontWeight: 600, cursor: 'pointer' }}>Answers in this pilot ({entries.length})</summary>
      <ul style={{ margin: '8px 0 0', padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="ins-pilot-log">
        {entries.slice(0, 30).map((e) => (
          <li key={e.n} style={{ fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 4, borderBottom: '1px solid var(--line)', paddingBottom: 6 }}>
            <span className="rk-fhint">
              {fmtTs(e.at)} · {e.who} · {e.version.includes('pilot') ? 'the draft' : 'the live version'} · {e.model}
              {costs ? ` · ${usd(e.costUSD)}` : ''}
            </span>
            <strong>{e.question}</strong>
            <span style={{ whiteSpace: 'pre-wrap' }}>{e.answer}</span>
            <span style={{ display: 'flex', gap: 6 }}>
              <button className="rk-btn" aria-pressed={e.rating === 1} onClick={() => void rate(e, 1)}>
                Good
              </button>
              <button className="rk-btn" aria-pressed={e.rating === -1} onClick={() => void rate(e, -1)}>
                Poor
              </button>
            </span>
          </li>
        ))}
      </ul>
    </details>
  )
}
