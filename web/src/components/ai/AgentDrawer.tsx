import { useCallback, useEffect, useState } from 'react'
import {
  ALL_CLUSTERS,
  AUTO,
  EFFORTS,
  modelLabel,
  tieredOf,
  backToDraft,
  discardDraft,
  estimateTest,
  fetchInstructions,
  fetchModels,
  fetchPilot,
  fetchTestRun,
  makeLive,
  MAX_UNTESTED_REASON,
  MAX_WORDS,
  resolveBaseline,
  restoreVersion,
  saveAgent,
  saveDraft,
  startPilot,
  startTest,
  stateLabel,
  stateTone,
  stopPilot,
  testComplete,
  testLine,
  tokensText,
  words,
  type AgentChange,
  type CompactMode,
  type Effort,
  type AgentView,
  type AIOverview,
  type Instructions,
  type PromptPart,
  type TestRun,
} from '../../api/ai'
import { usd } from '../../api/sherlock'
import { copyText } from '../../lib/clipboard'
import { fmtTs } from '../../lib/format'
import { Card, Drawer } from '../Drawer'
import { Select } from '../Select'
import { AgentJobs } from '../aiops/AgentJobs'
import { ModelChoice } from './ModelChoice'
import { TestDialog } from './TestDialog'
import { PilotHistoryDialog } from './PilotHistoryDialog'
import { HistoryDialog } from './HistoryDialog'
import { TextDiff } from './TextDiff'
import { ConfirmDialog } from '../ConfirmDialog'
import { useShowCosts } from '../../lib/costs'
import { Unsaved, useUnsaved } from '../../lib/unsaved'
import { navigate } from '../../lib/router'
import { LIMITS_HREF } from './LimitsSection'

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
  lookup_live_sources: 'Look up live sources',
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
  return d.test && d.test.rev === d.rev && testComplete(d.test) ? 1 : 0
}

/** Settings as text to compare, whatever order their keys were set in; an unset key is no key. */
export function canon(o: object): string {
  return JSON.stringify(
    Object.fromEntries(
      Object.entries(o)
        .filter(([, v]) => v !== undefined)
        .sort(([a], [b]) => a.localeCompare(b)),
    ),
  )
}

/** The values to put the instructions in force into the chart, as YAML. */
export function valuesYAML(id: string, text: string): string {
  const body = text.trim()
    ? text
        .trim()
        .split('\n')
        .map((l) => (l ? `      ${l}` : ''))
        .join('\n')
    : '      '
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
  useUnsaved(changed, 'the agent drawer')
  const tools = set.tools ?? agent.tools
  // Effort and Auto (phase-13.1-ai, AI-43, AI-44), Auto among the provider's tiered models (A9).
  const anthropic = !provider?.kind || provider.kind === 'anthropic'
  const autoModel = set.model === AUTO
  const autoEffort = set.effort === AUTO
  const tiered = tieredOf(provider)
  const standard = tiered.find((o) => o.tier === 'standard')?.model ?? tiered[0]?.model
  // The model saved is one its provider no longer offers (A9): it keeps answering on it until changed.
  const offersAll = provider?.offersAll ?? !provider?.offeredChosen
  const notOffered = !!set.model && set.model !== AUTO && !offersAll && !(provider?.offers ?? []).some((o) => o.model === set.model)
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
        {/* Provider, model and the refresh icon on one line, their fields at one height: the labels above them are alike. */}
        <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'flex-start' }} data-testid="agent-model-row">
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600, flex: '0 0 200px' }}>
            Provider
            <Select label="Provider" value={providerId} options={ov.providers.map((p) => [p.id, p.name])} onChange={(v) => {
                const next = ov.providers.find((p) => p.id === v)
                // A9: Auto stays only where the provider has a tiered model; a fixed model only where it is offered (or every listed model is).
                const keepsAuto = tieredOf(next).length > 0
                const keepsModel = !!set.model && set.model !== AUTO && ((next?.offersAll ?? !next?.offeredChosen) || (next?.offers ?? []).some((o) => o.model === set.model))
                const model = autoModel ? (keepsAuto ? AUTO : undefined) : keepsModel ? set.model : undefined
                setSet({ ...set, provider: v === 'default' ? undefined : v, model, ...(model === AUTO ? {} : { floorModel: undefined }), ...(model ? {} : { contextWindow: undefined }) })
              }}
              testId="agent-provider"
              width={200}
            />
          </label>
          {/* --- A9: one Model dropdown: Default, Auto (on a provider with a tiered model), then the models the provider offers. --- */}
          <ModelChoice
            provider={provider}
            defaultText={`${provider?.model || ov.defaultModel} on ${provider?.name ?? 'the default provider'}`}
            value={set.model ?? ''}
            onChange={(model, listed) =>
              setSet({
                ...set,
                model: model || undefined,
                contextWindow: !model || model === AUTO ? undefined : listed?.contextWindow || set.contextWindow,
                ...(model === AUTO ? {} : { floorModel: undefined }),
              })
            }
            load={() => fetchModels(providerId)}
            testId="agent-model"
          />
          {/* --- Effort (phase-13.1-ai, AI-43): beside the model. --- */}
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600, flex: '0 0 150px' }}>
            Effort
            <Select
              label="Effort"
              value={set.effort ?? ''}
              options={EFFORTS.map((e) => [e.id, e.label] as [string, string])}
              onChange={(v) => setSet({ ...set, effort: (v || undefined) as Effort | undefined, ...(v === AUTO || autoModel ? {} : { floorEffort: undefined }) })}
              testId="agent-effort"
              width={150}
            />
          </label>
          {!!set.model && !autoModel && (
            <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
              Context window (tokens)
              <span className="rk-field" style={{ width: 150 }}>
                <input
                  type="number"
                  min={0}
                  step={1024}
                  value={set.contextWindow ?? ''}
                  placeholder="Not known"
                  onChange={(e) => setSet({ ...set, contextWindow: Math.max(0, Math.round(Number(e.target.value) || 0)) || undefined })}
                  data-testid="agent-context"
                />
              </span>
            </label>
          )}
        </div>
        {notOffered && (
          <span className="rk-t" data-tone="warn" style={{ fontSize: 12.5 }} data-testid="agent-not-offered">
            {provider?.name ?? 'Its provider'} no longer offers {set.model}: the {agent.name} answers on it until you choose another model.
          </span>
        )}
        {autoModel && tiered.length === 0 && (
          <span className="rk-t" data-tone="warn" style={{ fontSize: 12.5 }} data-testid="agent-auto-no-tier">
            None of the models {provider?.name ?? 'its provider'} offers has a tier, so Auto cannot pick the model: the {agent.name} answers on the default model. Give the models tiers in Administration › AI ›
            Providers, or choose a model.
          </span>
        )}
        {(autoModel || autoEffort) && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="agent-auto">
            <span className="rk-fhint" data-testid="agent-auto-line">
              Auto picks {autoEffort ? 'the effort for every message' : ''}
              {autoEffort && autoModel ? ' and ' : ''}
              {autoModel ? 'the model when a conversation or a run starts, moving it up once at most' : ''}: a job’s run by what started it (a P0 or P1 case, a production cluster, an alert, a schedule),
              a conversation by the classifier ({ov.classifier ? (ov.providers.find((p) => p.id === ov.classifier)?.name ?? ov.classifier) : `off: medium${autoModel && standard ? ` on ${modelLabel(standard)}` : ''}`}). Never xhigh or max without an open P0 or P1 case;
              one level lower past 80% of the day’s budget. Each pick is shown with the run or the answer.
            </span>
            <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
              {autoEffort && (
                <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
                  Lowest effort Auto may pick
                  <Select
                    label="Lowest effort Auto may pick"
                    value={set.floorEffort ?? ''}
                    options={[['', 'None'], ['low', 'Low'], ['medium', 'Medium'], ['high', 'High']]}
                    onChange={(v) => setSet({ ...set, floorEffort: v || undefined })}
                    testId="agent-floor-effort"
                    width={150}
                  />
                </label>
              )}
              {autoModel && (
                <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
                  Lowest model Auto may pick
                  <Select
                    label="Lowest model Auto may pick"
                    value={set.floorModel ?? ''}
                    options={[['', 'None'], ...(set.floorModel && !tiered.some((o) => o.model === set.floorModel) ? [[set.floorModel, `${set.floorModel} (no longer offered with a tier)`] as [string, string]] : []), ...tiered.map((o) => [o.model, `${modelLabel(o.model)} · ${o.tier}`] as [string, string])]}
                    onChange={(v) => setSet({ ...set, floorModel: v || undefined })}
                    testId="agent-floor-model"
                    width={190}
                  />
                </label>
              )}
            </div>
          </div>
        )}
        {!!set.effort && set.effort !== AUTO && !anthropic && (
          <span className="rk-fhint" data-testid="agent-effort-hint">
            {provider?.kind === 'openai' ? 'On an OpenAI-compatible provider, Low turns the model’s thinking off and any other level on.' : 'Sent where the model takes it; a model that takes no effort is sent none.'}
          </span>
        )}
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
          <a className="rk-link" href="#/aiops/jobs" data-testid="agent-jobs-link">
            AIOps › Jobs
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
          0 means no cap of its own. Its spend counts toward the total limits and its provider’s too, set in Administration › AI › Limits.{costs ? ` Spent: ${usd(agent.spend.dayUSD)} today, ${usd(agent.spend.monthUSD)} this month.` : ''}
        </p>
        {/* --- AI-28, AI-39: the total and the provider's limits stop every agent under them, whatever its own cap says. --- */}
        {overallHints(set, ov, costs ? usd : undefined, set.provider ? ov.providers.find((p) => p.id === set.provider) : ov.providers.find((p) => p.default)).map((h) => (
          <p
            key={h.which}
            className="rk-callout"
            data-tone={h.higher ? 'warn' : 'info'}
            style={{ margin: 0, fontSize: 12.5, display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}
            data-testid="agent-overall-hint"
            data-which={h.which}
          >
            <span>{h.text}</span>
            <button className="rk-link" style={{ fontSize: 12.5 }} disabled={changed} title={changed ? 'Save or cancel your changes first.' : undefined} onClick={() => toOverallLimits(onClose)} data-testid="agent-overall-link">
              Go to Administration › AI › Limits
            </button>
          </p>
        ))}
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
                  <input
                    type="checkbox"
                    checked={set.clusters!.includes(c.id)}
                    onChange={(e) => setSet({ ...set, clusters: e.target.checked ? [...set.clusters!, c.id] : set.clusters!.filter((x) => x !== c.id) })}
                    data-testid="agent-cluster"
                    data-cluster={c.id}
                  />
                  {c.name}
                </label>
              ))}
          </div>
        )}
      </Card>
    </Drawer>
  )
}

/**
 * What the drawer says of the limits above the agent's own caps (AI-28,
 * AI-39): the total, all agents together, and its provider's, every agent
 * on it. With no cap of its own they still apply; with a cap above one of
 * them, that one stops it first. The amounts show only with money, as
 * costs are shown (CF-40).
 */
export function overallHints(
  set: { dailyUSD?: number; monthlyUSD?: number },
  ov: { dailyUSD: number; monthlyUSD: number },
  money?: (n: number) => string,
  provider?: { name: string; limit?: { dailyUSD: number; monthlyUSD: number } | null },
): { which: 'daily' | 'monthly'; higher: boolean; text: string }[] {
  const out: { which: 'daily' | 'monthly'; higher: boolean; text: string }[] = []
  const cap = (s: string) => `${s[0]!.toUpperCase()}${s.slice(1)}`
  const one = (which: 'daily' | 'monthly', own: number, total: number, ofProvider: number, per: string) => {
    const limits: { amount: number; name: string; who: string }[] = []
    if (total > 0) limits.push({ amount: total, name: money ? `the total limit of ${money(total)} a ${per}` : `the total ${which} limit`, who: 'all agents together' })
    if (ofProvider > 0 && provider)
      limits.push({ amount: ofProvider, name: money ? `the limit of ${money(ofProvider)} a ${per} of its provider ${provider.name}` : `the ${which} limit of its provider ${provider.name}`, who: `every agent on ${provider.name}` })
    if (!limits.length) return
    if (own <= 0) {
      out.push({ which, higher: false, text: limits.map((l) => `${cap(l.name)} still applies to ${l.who}.`).join(' ') })
      return
    }
    const lower = limits.filter((l) => own > l.amount)
    if (lower.length) out.push({ which, higher: true, text: `Its ${which} cap${money ? ` of ${money(own)}` : ''} is higher than ${lower.map((l) => `${l.name}, which counts ${l.who}`).join(', and ')}, and stops it first.` })
  }
  one('daily', set.dailyUSD ?? 0, ov.dailyUSD, provider?.limit?.dailyUSD ?? 0, 'day')
  one('monthly', set.monthlyUSD ?? 0, ov.monthlyUSD, provider?.limit?.monthlyUSD ?? 0, 'month')
  return out
}

/** Closes the drawer and opens Administration › AI › Limits, where the total and the providers' limits are changed (AI-39). */
export function toOverallLimits(close: () => void) {
  close()
  navigate(LIMITS_HREF)
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
  if (!f)
    return (
      <p className="rk-fhint" style={{ margin: 0 }} data-testid="agent-context-line">
        How much of the model’s context its instructions take shows once the agent can answer.
      </p>
    )
  const total = f.instructions + f.tools
  const whole = f.whole.instructions + f.whole.tools
  const of = f.window ? ` of ${agent.model}’s ${tokensText(f.window)} (${Math.round((100 * total) / f.window)}%)` : ''
  const parts = [`Instructions and tools: ${sizeText(f)}${of}.`]
  const tone = !f.compact && f.window && whole > f.share * f.window ? 'warn' : undefined
  if (!f.window) parts.push(agent.defaultModel ? 'The model’s context window is not known: set it on its provider, in Administration › AI › Providers.' : 'The model’s context window is not known: set it above.')
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
  const [panel, setPanel] = useState<'' | 'test' | 'pilot' | 'live' | 'untested'>('')
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
  // While its tests run, their progress is read every few seconds (Phase
  // 12.1); when they end, the instructions are read again for the result.
  const [watch, setWatch] = useState(!!agent.testing)
  const [running, setRunning] = useState<TestRun | null>(null)
  const [dialog, setDialog] = useState(false)
  // The pilot's answers open in a dialog of their own (AI-27); the drawer
  // shows how many there are.
  const [history, setHistory] = useState(false)
  const [historyCount, setHistoryCount] = useState<number | null>(null)
  // The versions and drafts, to compare (AI-32): open on a version's Diff…, else on the draft against the version in force (AI-41).
  const [diffOf, setDiffOf] = useState<{ version?: number } | null>(null)
  const piloted = ins?.draft?.pilot?.started
  useEffect(() => {
    if (!piloted) return
    let live = true
    fetchPilot(agent.id).then(
      (r) => {
        if (live) setHistoryCount(r.entries?.length ?? 0)
      },
      () => {
        if (live) setHistoryCount(null)
      },
    )
    return () => {
      live = false
    }
  }, [agent.id, piloted])
  // What the last save discarded (AI-26), said until the next action.
  const [discarded, setDiscarded] = useState('')
  useEffect(() => {
    if (!watch) return
    let live = true
    const tick = () =>
      fetchTestRun(agent.id).then(
        (r) => {
          if (!live) return
          if (r.state === 'running') {
            setRunning(r)
            return
          }
          setRunning(null)
          setWatch(false)
          void load()
        },
        () => {
          if (live) setWatch(false)
        },
      )
    void tick()
    const t = window.setInterval(() => void tick(), 2000)
    return () => {
      live = false
      window.clearInterval(t)
    }
  }, [watch, agent.id, load])
  if (!ins)
    return (
      <Card title="Instructions">
        {
          <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>
            {error || 'Loading the instructions…'}
          </p>
        }
      </Card>
    )
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
    setDiscarded('')
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
          <Unsaved when={edited} what="the agent’s instructions" />
          <span className="rk-pill" data-tone={stateTone(ins.state)} data-testid="ins-state">
            {stateLabel(ins.state)}
          </span>
          {d && (
            <span className="rk-pill" data-tone="muted">
              Draft {d.rev}
              {ins.live ? ` of version ${ins.live + 1}` : ''}
            </span>
          )}
        </span>
      }
    >
      <p className="rk-fhint" style={{ margin: 0 }}>
        The built-in instructions are shown as this release ships them; an edit replaces them whole for this agent. Whatever they say, text from the cluster stays data and secrets never leave the server. House instructions and notes are
        added after them. Facts about systems belong in Knowledge.
      </p>
      {part && (
        <PartEditor
          agent={agent}
          part={part}
          parts={ins.parts}
          tab={partTab}
          onTab={setPartTab}
          text={partText(part)}
          max={ins.maxPartWords}
          compare={partCompare}
          onCompare={() => setPartCompare(!partCompare)}
          onChange={(t) => setParts({ ...allParts(), [part.name]: t })}
        />
      )}
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
        <textarea
          className="rk-area"
          rows={7}
          spellCheck
          value={text}
          placeholder="Your team’s conventions: runbooks to cite, services to check first"
          onChange={(e) => setHouse(e.target.value)}
          data-testid="ins-house"
          style={{ width: '100%', boxSizing: 'border-box', fontWeight: 400, fontSize: 12.5, lineHeight: 1.5, resize: 'vertical' }}
        />
        <span className="rk-fhint" style={{ fontWeight: 400 }} data-tone={words(text) > MAX_WORDS ? 'err' : undefined}>
          {words(text)} of {MAX_WORDS.toLocaleString('en')} words.
        </span>
      </label>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <span style={{ display: 'flex', gap: 8, alignItems: 'center', fontSize: 12.5, fontWeight: 600 }}>
          Notes for
          <Select
            label="Notes for the cluster"
            value={cluster}
            options={[[ALL_CLUSTERS, 'Every cluster'], ...ov.clusters.map((c): [string, string] => [c.id, `${c.name} only${n[c.id] ? ' ·' : ''}`])]}
            onChange={setCluster}
            testId="ins-notes-cluster"
          />
        </span>
        <textarea
          className="rk-area"
          rows={3}
          value={n[cluster] ?? ''}
          placeholder="Maintenance window: Sundays 02:00–04:00"
          onChange={(e) => setNotes({ ...n, [cluster]: e.target.value })}
          aria-label={`Notes for ${clusterName(cluster)}`}
          data-testid="ins-notes"
          style={{ width: '100%', boxSizing: 'border-box', fontSize: 12.5, lineHeight: 1.5, resize: 'vertical' }}
        />
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
          Helm value:{' '}
          <span className="rk-mono" data-testid="ins-values-key">
            {ins.valuesKey}
          </span>
        </span>
        <button
          className="rk-btn"
          onClick={() => void copyText(valuesYAML(agent.id, ins.house)).then((ok) => onToast?.(ok ? `Copied the values for ${ins.valuesKey}: paste them into your values file.` : 'The browser refused the clipboard.'))}
          data-testid="ins-copy"
        >
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
            <pre className="rk-code rk-mono" style={{ margin: 0, padding: '8px 10px', fontSize: 12, whiteSpace: 'pre-wrap', background: 'var(--sunken)', borderRadius: 8, border: '1px solid var(--line)' }}>
              {ins.baseline || '(none: the chart sets no house instructions)'}
            </pre>
          </div>
          <div>
            <div className="rk-fhint">{d || edited ? 'Your draft' : 'In force'}</div>
            <pre className="rk-code rk-mono" style={{ margin: 0, padding: '8px 10px', fontSize: 12, whiteSpace: 'pre-wrap', background: 'var(--sunken)', borderRadius: 8, border: '1px solid var(--line)' }}>
              {text || '(none)'}
            </pre>
          </div>
        </div>
      )}
      <Stepper
        ins={ins}
        edited={edited}
        busy={busy}
        testing={watch}
        panel={panel}
        onPanel={(p) => setPanel(panel === p ? '' : p)}
        onBack={() => void act(() => backToDraft(agent.id), 'The draft is back at its first stage: test it again.')}
        onStopPilot={() => void act(() => stopPilot(agent.id), 'Stopped the pilot.')}
        onUntested={() => setPanel('untested')}
        history={d?.pilot ? { count: historyCount, onShow: () => setHistory(true) } : undefined}
      />
      {discarded && (
        <p className="rk-callout" data-tone="info" role="status" style={{ margin: 0, fontSize: 12.5 }} data-testid="ins-discarded">
          {discarded}
        </p>
      )}
      {running && (
        <p className="rk-fhint" style={{ margin: 0, display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }} data-testid="ins-test-running">
          <span aria-live="polite">
            Tests running: {running.cases} of {running.planned?.length ?? running.cases} cases…
          </span>
        </p>
      )}
      {d?.test && (
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="ins-last-test">
          Last test {fmtTs(d.test.at)}: {testLine(d.test, costs ? usd : undefined)}
          {d.test.rev !== d.rev ? ' (of an earlier draft)' : ''}
          {d.test.rev === d.rev && !testComplete(d.test) && <span data-testid="ins-test-incomplete"> · it did not run every case, so the draft is still untested: run the tests again, or make it live untested</span>}
          {d.test.rev === d.rev && d.test.cases > d.test.passed && (
            <span data-tone="err" data-testid="ins-test-failed">
              {' '}
              · {d.test.cases - d.test.passed} failed
            </span>
          )}
        </p>
      )}
      {dialog && (
        <TestDialog
          agentId={agent.id}
          agentName={agent.name}
          draftRev={d?.rev}
          onClose={() => setDialog(false)}
          onEnded={() => {
            setRunning(null)
            void load()
          }}
        />
      )}
      {ins.baselineCheck && (
        <p className="rk-fhint" style={{ margin: 0 }}>
          The chart’s new baseline was tested at start, {fmtTs(ins.baselineCheck.at)}: {testLine(ins.baselineCheck, costs ? usd : undefined)}
        </p>
      )}
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
              // A new draft discards the earlier ones (AI-26): say which, and the pilot and requests that went with them.
              setDiscarded(v.discarded?.notice ?? '')
              onToast?.(v.discarded ? `Saved draft ${v.draft?.rev}. ${v.discarded.notice}` : 'Saved the draft.')
              return v
            })
          }
          data-testid="ins-save-draft"
        >
          Save draft
        </button>
        {running ? (
          <button className="rk-btn" aria-haspopup="dialog" onClick={() => setDialog(true)} data-testid="ins-test-show">
            Show tests
          </button>
        ) : (
          d?.test && (
            <button className="rk-btn" aria-haspopup="dialog" onClick={() => setDialog(true)} data-testid="ins-test-results">
              Show results
            </button>
          )
        )}
        <button className="rk-btn" aria-haspopup="dialog" onClick={() => setDiffOf({})} data-testid="ins-history">
          History
        </button>
        {d && (
          <button className="rk-btn" disabled={busy} onClick={() => void act(() => discardDraft(agent.id), 'Discarded the draft.')} data-testid="ins-discard">
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
            // The run shows in its dialog at once; closing it leaves the run going.
            setPanel('')
            setWatch(true)
            setDialog(true)
          }}
        />
      )}
      {panel === 'pilot' && d && (
        <PilotPanel
          id={agent.id}
          ins={ins}
          ov={ov}
          onDone={(v) => {
            setIns(v)
            setPanel('')
          }}
        />
      )}
      {panel === 'live' && (
        <div className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="ins-live-confirm">
          <span>
            The draft becomes the {agent.name}’s instructions for everyone, as version {(ins.versions[0]?.n ?? 0) + 1}. The version in force now is kept, to restore in one click.
          </span>
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
      {panel === 'untested' && d && (
        <UntestedConfirm
          agentName={agent.name}
          version={(ins.versions[0]?.n ?? 0) + 1}
          busy={busy}
          error={error}
          localEnv={ov.clusters.find((c) => c.id === 'local')?.env}
          onCancel={() => {
            setPanel('')
            setError('')
          }}
          onConfirm={(reason) =>
            void act(async () => {
              const r = await makeLive(agent.id, { reason })
              setPanel('')
              onToast?.(r.waiting ? `Waiting for another SuperAdmin to approve request ${r.request?.id} on Approvals; it goes live untested.` : `Live, untested, as version ${r.live?.n}.`)
            })
          }
        />
      )}
      {history && <PilotHistoryDialog agentId={agent.id} agentName={agent.name} onClose={() => setHistory(false)} onCount={setHistoryCount} />}
      {diffOf && (
        <HistoryDialog agentId={agent.id} agentName={agent.name} clusterName={(id) => (id === ALL_CLUSTERS ? 'every cluster' : (ov.clusters.find((c) => c.id === id)?.name ?? id))} version={diffOf.version} onClose={() => setDiffOf(null)} />
      )}
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
                <button className="rk-btn" aria-haspopup="dialog" title="What this version changed against the one before it" onClick={() => setDiffOf({ version: v.n })} data-testid="ins-version-diff">
                  Diff…
                </button>
                {v.n === ins.live ? (
                  <span className="rk-pill" data-tone="ok">
                    In force
                  </span>
                ) : (
                  <button
                    className="rk-btn"
                    disabled={busy || edited}
                    title={edited ? 'Save or undo your edits first.' : busy ? 'Wait for the change under way.' : undefined}
                    onClick={() =>
                      void act(async () => {
                        const r = await restoreVersion(agent.id, v.n)
                        onToast?.(r.waiting ? `Version ${v.n} waits for another SuperAdmin on Approvals.` : `Version ${v.n} is live again, as version ${r.live?.n}.`)
                      })
                    }
                    data-testid="ins-restore"
                  >
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
function PartEditor({
  agent,
  part,
  parts,
  tab,
  onTab,
  text,
  max,
  compare,
  onCompare,
  onChange,
}: {
  agent: AgentView
  part: PromptPart
  parts: PromptPart[]
  tab: string
  onTab: (t: PromptPart['name']) => void
  text: string
  max: number
  compare: boolean
  onCompare: () => void
  onChange: (t: string) => void
}) {
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
      <textarea
        className="rk-area"
        rows={12}
        spellCheck
        value={text}
        onChange={(e) => onChange(e.target.value)}
        aria-label={part.label}
        data-testid="ins-part"
        data-part={part.name}
        style={{ width: '100%', boxSizing: 'border-box', fontSize: 12.5, lineHeight: 1.5, resize: 'vertical' }}
      />
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
  pilot: 'The tested draft answers for chosen people or clusters for a while; both versions are logged, to compare.',
  live: 'In force for everyone; the version before is kept, to restore in one click. A second SuperAdmin approves where there is one.',
}

/**
 * Draft → Test → Pilot → Live as steps that act (Phase 12, wave 5): each
 * says what it is and offers its action, disabled with the reason when it
 * cannot be done now.
 */
function Stepper({
  ins,
  edited,
  busy,
  testing,
  panel,
  onPanel,
  onBack,
  onStopPilot,
  onUntested,
  history,
}: {
  ins: Instructions
  edited: boolean
  busy: boolean
  testing: boolean
  panel: string
  onPanel: (p: 'test' | 'pilot' | 'live') => void
  onBack: () => void
  onStopPilot: () => void
  /** Opens the confirmation to make an untested draft live, with a reason (Phase 12.1). */
  onUntested: () => void
  /** The pilot's answers, to open in their dialog (AI-27); none before a pilot. */
  history?: { count: number | null; onShow: () => void }
}) {
  const d = ins.draft
  const stage = stageIndex(ins)
  const pil = piloting(ins)
  const common = edited ? 'Save your edits first.' : !d ? 'Save a draft first: edit the instructions above.' : ''
  // A SuperAdmin may make a draft live without its test, giving a reason
  // (Phase 12.1): offered only while the draft is untested.
  const untested = !!d && !edited && !d.request && !d.restored && stage < 1
  const steps: { name: string; action: string; testId: string; why: string; onClick: () => void; open?: boolean; note?: string; extra?: { label: string; testId: string; onClick: () => void; open?: boolean; enabled?: boolean } }[] = [
    { name: 'Draft', action: 'Back to draft', testId: 'ins-back', why: common || (stage === 0 ? 'The draft is at this stage now.' : ''), onClick: onBack, note: d ? `Draft ${d.rev}, saved by ${d.by}, ${fmtTs(d.at)}.` : undefined },
    { name: 'Test', action: d?.test ? 'Run tests again' : 'Run tests', testId: 'ins-test', why: common || (testing ? 'The tests are running.' : ''), onClick: () => onPanel('test'), open: panel === 'test' },
    {
      name: 'Pilot',
      action: pil ? 'Stop pilot' : 'Start pilot',
      testId: 'ins-pilot',
      why: common || (stage < 1 ? 'Run the tests first: a pilot serves a tested draft.' : ''),
      onClick: pil ? onStopPilot : () => onPanel('pilot'),
      open: panel === 'pilot',
      note: pil && d?.pilot ? `In pilot until ${fmtTs(d.pilot.until)}.` : undefined,
      extra: history ? { label: history.count === null ? 'Show pilot history' : `Show pilot history (${history.count})`, testId: 'pilot-history-show', onClick: history.onShow, enabled: true } : undefined,
    },
    {
      name: 'Live',
      action: 'Make live',
      testId: 'ins-live',
      why: edited
        ? common
        : !d
          ? `Nothing waits to go live: ${ins.live ? `version ${ins.live} is in force` : 'the built-in and chart instructions are in force'}. Edit and save a draft.`
          : d.request
            ? `Waiting for another SuperAdmin: request ${d.request} on Approvals in AIOps › Inbox.`
            : stage < 1 && !d.restored
              ? 'Run the tests first.'
              : '',
      onClick: () => onPanel('live'),
      open: panel === 'live',
      extra: untested ? { label: 'Make live untested', testId: 'ins-live-untested', onClick: onUntested, open: panel === 'untested' } : undefined,
    },
  ]
  return (
    <ol aria-label="Draft, test, pilot, live" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(180px, 100%), 1fr))', gap: 8, listStyle: 'none', margin: 0, padding: 0 }} data-testid="ins-stages">
      {steps.map((st, i) => {
        // With no draft, nothing on the way to live has started: only Live
        // is where things stand.
        const state = i === stage ? 'current' : i < stage && d ? 'done' : 'todo'
        return (
          <li
            key={st.name}
            aria-current={state === 'current' ? 'step' : undefined}
            data-stage={st.name.toLowerCase()}
            data-state={state}
            style={{
              display: 'flex',
              flexDirection: 'column',
              gap: 6,
              padding: '8px 10px',
              borderRadius: 8,
              border: `1px solid ${state === 'current' ? 'var(--accent)' : 'var(--line)'}`,
              background: state === 'current' ? 'var(--sunken)' : undefined,
              minWidth: 0,
              fontSize: 12.5,
            }}
          >
            <span style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
              <span className="rk-pill" data-tone={state === 'done' ? 'ok' : state === 'current' ? 'info' : 'muted'}>
                {i + 1}
              </span>
              <strong data-testid="ins-step-name">{st.name}</strong>
              {state === 'done' && <span className="rk-fhint">done</span>}
              {state === 'current' && <span className="rk-fhint">now</span>}
              {state === 'todo' && !d && <span className="rk-fhint">not started</span>}
            </span>
            <span className="rk-fhint">{STAGE_TEXT[st.name.toLowerCase()]}</span>
            {st.note && <span className="rk-fhint">{st.note}</span>}
            <span style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
              <button className="rk-btn" data-primary={!st.why && i === stage + 1 ? 'true' : undefined} disabled={busy || !!st.why} aria-expanded={st.open} title={st.why || undefined} onClick={st.onClick} data-testid={st.testId}>
                {st.action}
              </button>
              {st.extra && (
                <button className="rk-btn" disabled={busy && !st.extra.enabled} aria-haspopup="dialog" onClick={st.extra.onClick} data-testid={st.extra.testId}>
                  {st.extra.label}
                </button>
              )}
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

/** A line diff of two texts, as Helm › History shows a revision's (AI-32). */
function Diff({ label, from, to, testId }: { label: string; from: string; to: string; testId: string }) {
  return <TextDiff label={label} from={from} to={to} testId={testId} />
}

/**
 * Making an untested draft live (Phase 12.1): a SuperAdmin says why it
 * cannot wait for a test. Its version and the audit log keep the reason;
 * a second SuperAdmin approves it where there is one, as any other, unless
 * the console's own cluster is not production (AI-34): then it goes live
 * at once.
 */
function UntestedConfirm({
  agentName,
  version,
  busy,
  error,
  localEnv,
  onConfirm,
  onCancel,
}: {
  agentName: string
  version: number
  busy: boolean
  error: string
  /** The environment of the console's own cluster, when known. */
  localEnv?: string
  onConfirm: (reason: string) => void
  onCancel: () => void
}) {
  // Only a cluster chosen as staging or development is not production;
  // other counts as production.
  const offProduction = localEnv === 'staging' || localEnv === 'development'
  const [reason, setReason] = useState('')
  return (
    <ConfirmDialog title="Make the draft live untested?" confirm="Make live untested" danger busy={busy} disabled={!reason.trim()} error={error} onConfirm={() => onConfirm(reason.trim())} onCancel={onCancel}>
      <span>
        The draft becomes the {agentName}’s instructions for everyone, as version {version}, without a test. Its version and the audit log say it went live untested, with your reason.{' '}
        {offProduction ? (
          <span data-testid="ins-untested-direct">The console’s own cluster is {localEnv}, not production, so it goes live at once: no second SuperAdmin approves it.</span>
        ) : (
          'Where there is another SuperAdmin, they approve it first.'
        )}
      </span>
      <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontWeight: 600 }}>
        Why it cannot wait for a test
        <textarea
          className="rk-area"
          rows={3}
          required
          aria-required="true"
          maxLength={MAX_UNTESTED_REASON}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          data-testid="ins-untested-reason"
          style={{ width: '100%', boxSizing: 'border-box', fontWeight: 400, fontSize: 12.5, resize: 'vertical' }}
        />
        <span className="rk-fhint" style={{ fontWeight: 400 }}>
          Required; {reason.length} of {MAX_UNTESTED_REASON} characters.
        </span>
      </label>
    </ConfirmDialog>
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
        <Select
          label="Conversations to replay"
          value={String(replays)}
          options={[
            ['0', 'no'],
            ['5', '5'],
            ['10', '10'],
            ['20', '20'],
          ]}
          onChange={(v) => setReplays(Number(v))}
        />
        conversations
      </label>
      <span data-testid="ins-test-estimate">
        {error || (est ? `${est.cases} recorded cases and ${est.replays} replays${costs ? `: about ${usd(est.estimateUSD)}, charged to this agent` : ', charged to this agent'}.` : costs ? 'Working out the cost…' : 'Counting the cases…')}
      </span>
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
          <span>
            In pilot until {fmtTs(p!.until)} for {[...(p!.people ?? []), ...(p!.clusters ?? []).map((c) => ov.clusters.find((x) => x.id === c)?.name ?? c)].join(', ')}.
          </span>
          <span>
            <button className="rk-btn" onClick={() => void stopPilot(id).then(onDone, (e: Error) => setError(e.message))} data-testid="ins-pilot-stop">
              Stop the pilot
            </button>
          </span>
        </>
      ) : (
        <>
          <span>The draft answers for the people or clusters chosen, for a while; the answers of both versions are logged meanwhile, to compare.</span>
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
            <Select
              label="For how long"
              value={String(hours)}
              options={[
                ['24', 'a day'],
                ['72', 'three days'],
                ['168', 'a week'],
                ['336', 'two weeks'],
              ]}
              onChange={(v) => setHours(Number(v))}
            />
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
