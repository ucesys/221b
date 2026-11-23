import { useEffect, useState } from 'react'
import { currentRoute, navigate, withParams } from '../../lib/router'
import { EFFORTS, fetchAI, saveAgent, stateLabel, stateTone, type AgentView, type AIOverview } from '../../api/ai'
import { usd } from '../../api/sherlock'
import { useShowCosts } from '../../lib/costs'
import { AgentDrawer } from './AgentDrawer'
import { LIMITS_HREF } from './LimitsSection'
import { ShowCostsSection } from './ShowCosts'

/**
 * Administration › AI › Agents (Phase 12): every agent with its purpose, model,
 * budget, clusters and where its instructions stand; one opens in a drawer
 * to change it. The spending limits, the total (Sherlock's caps of before)
 * and each provider's, are on Administration › AI › Limits (AI-37).
 */
export function AgentsSection({ onToast, initial }: { onToast?: (msg: string) => void; initial?: string }) {
  const [ov, setOv] = useState<AIOverview | null>(null)
  const [error, setError] = useState('')
  const [open, setOpen] = useState<string | null>(initial ?? null)
  // An agent named in the address (#/admin/ai/agents/sherlock), as
  // Sherlock's view_admin opens it (SR-12), opens its drawer.
  const [opened, setOpened] = useState(initial)
  if (initial !== opened) {
    setOpened(initial)
    if (initial) setOpen(initial)
  }
  const close = () => {
    setOpen(null)
    const r = currentRoute()
    if (r.view === 'admin' && r.path.length > 2) navigate(withParams({ ...r, path: r.path.slice(0, 2) }, {}), true)
  }
  const costs = useShowCosts()
  useEffect(() => {
    fetchAI()
      .then(setOv)
      .catch((e: Error) => setError(e.message))
  }, [])
  if (!ov)
    return (
      <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>
        {error || 'Loading the agents…'}
      </p>
    )
  const on = ov.agents.filter((a) => a.enabled).length
  const def = ov.providers.find((p) => p.default)
  const agent = ov.agents.find((a) => a.id === open)
  const toggle = async (a: AgentView) => {
    setError('')
    try {
      setOv(await saveAgent(a.id, { ...a.settings, on: !a.enabled }))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }
  return (
    <>
      <p className="rk-fhint" style={{ margin: 0 }}>
        Each agent has one purpose and its own model; every change it asks for goes to Watson for approval.
      </p>
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }} aria-label="Agents now" data-testid="agents-summary">
        <span className="rk-pill" data-tone={on ? 'ok' : 'muted'}>
          <span className="rk-dot" />
          {on} of {ov.agents.length} on
        </span>
        <span className="rk-pill" data-tone="muted">
          Default model: <span className="rk-mono">{ov.defaultModel}</span> on {def?.name ?? 'the default provider'}
        </span>
        {costs && (
          <span className="rk-pill" data-tone="muted" data-testid="agents-limit">
            Total limit {ov.dailyUSD ? `${usd(ov.dailyUSD)} a day` : 'none a day'}
            {ov.monthlyUSD ? `, ${usd(ov.monthlyUSD)} a month` : ''} · spent {usd(ov.spend.dayUSD)} today
          </span>
        )}
      </div>
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="agents-error">
          {error}
        </p>
      )}
      <div style={{ overflowX: 'auto' }}>
        <table className="tb-table" data-testid="agents-table" style={{ width: '100%' }}>
          <thead>
            <tr>
              <th scope="col">Agent</th>
              <th scope="col">Purpose</th>
              <th scope="col">Model</th>
              <th scope="col">Effort</th>
              {costs && <th scope="col">Daily cap</th>}
              <th scope="col">Works on</th>
              <th scope="col">Instructions</th>
              <th scope="col">On</th>
            </tr>
          </thead>
          <tbody>
            {ov.agents.map((a) => (
              <tr key={a.id} data-testid="agent-row" data-agent={a.id} data-on={a.enabled ? 'true' : 'false'} onClick={() => setOpen(a.id)} style={{ cursor: 'pointer' }}>
                <td>
                  <button
                    className="rk-link"
                    style={{ fontWeight: 600 }}
                    onClick={(e) => {
                      e.stopPropagation()
                      setOpen(a.id)
                    }}
                    data-testid="agent-open"
                  >
                    {a.name}
                  </button>
                  {a.viewControl && ov.viewControl && (
                    <span className="rk-fhint" style={{ display: 'block', fontSize: 11 }}>
                      can change your view
                    </span>
                  )}
                </td>
                <td style={{ fontSize: 12.5, color: 'var(--fg2)' }}>{a.purpose}</td>
                <td>
                  <span className="rk-mono" style={{ fontSize: 12 }}>
                    {a.model}
                  </span>
                  {a.providerId !== 'default' && (
                    <span className="rk-fhint" style={{ display: 'block', fontSize: 11 }}>
                      on {a.providerName}
                    </span>
                  )}
                  {a.unusable && a.enabled && (
                    <span className="rk-t" data-tone="warn" style={{ display: 'block', fontSize: 11 }} title={a.unusable}>
                      cannot answer now
                    </span>
                  )}
                </td>
                <td style={{ fontSize: 12.5 }} data-testid="agent-effort">
                  {effortLabel(a.settings.effort)}
                  {a.settings.effort === 'auto' && a.settings.floorEffort && (
                    <span className="rk-fhint" style={{ display: 'block', fontSize: 11 }}>
                      at least {effortLabel(a.settings.floorEffort).toLowerCase()}
                    </span>
                  )}
                </td>
                {costs && <td className="rk-num">{a.settings.dailyUSD ? usd(a.settings.dailyUSD) : '—'}</td>}
                <td style={{ fontSize: 12.5 }}>{worksOn(a, ov)}</td>
                <td>
                  <span className="rk-pill" data-tone={stateTone(a.state)} data-testid="agent-state">
                    {stateLabel(a.state)}
                  </span>
                  {a.stage && (
                    <span className="rk-fhint" style={{ display: 'block', fontSize: 11 }} data-testid="agent-stage">
                      {a.stage === 'pilot' ? 'Draft in pilot' : a.stage === 'test' ? 'Draft tested' : 'Draft'}
                    </span>
                  )}
                  {a.baselineCheck && a.baselineCheck.passed < a.baselineCheck.cases && (
                    <span className="rk-t" data-tone="warn" style={{ display: 'block', fontSize: 11 }} data-testid="agent-baseline-failed">
                      New baseline fails {a.baselineCheck.cases - a.baselineCheck.passed} test{a.baselineCheck.cases - a.baselineCheck.passed === 1 ? '' : 's'}
                    </span>
                  )}
                </td>
                <td onClick={(e) => e.stopPropagation()}>
                  {a.id === 'sherlock' ? (
                    <span className="rk-fhint" style={{ fontSize: 12 }}>
                      {a.enabled ? 'On' : 'Off'}
                    </span>
                  ) : (
                    <label className="rk-tcheck">
                      <input type="checkbox" checked={a.enabled} onChange={() => void toggle(a)} aria-label={`${a.name} on`} data-testid="agent-on" />
                    </label>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="rk-fhint" style={{ margin: 0 }}>
        An agent that is off is not asked: Sherlock does that part itself, or says it cannot. Sherlock is on for the clusters ticked in its drawer. Open an agent to change its model, instructions, tools, budget and clusters.
      </p>
      <p className="rk-fhint" style={{ margin: 0 }} data-testid="agents-limits-link">
        Spending limits are in{' '}
        <a className="rk-link" href={LIMITS_HREF}>
          Administration › AI › Limits
        </a>
        .
      </p>
      <ShowCostsSection />
      {agent && (
        <AgentDrawer
          agent={agent}
          ov={ov}
          onClose={close}
          onSaved={(next, msg) => {
            setOv(next)
            if (msg) onToast?.(msg)
          }}
          onToast={onToast}
        />
      )}
    </>
  )
}

/** The clusters an agent works on, in words. */
export function worksOn(a: AgentView, ov: AIOverview): string {
  const name = (id: string) => ov.clusters.find((c) => c.id === id)?.name ?? id
  if (a.id === 'sherlock') return a.clusters?.length ? a.clusters.map(name).join(', ') : 'No cluster'
  return a.clusters?.length ? a.clusters.map(name).join(', ') : 'All clusters'
}

/** An agent's effort as the table says it: Default (the model's own), a level, or Auto. */
export function effortLabel(effort?: string): string {
  return EFFORTS.find((e) => e.id === (effort ?? ''))?.label ?? effort ?? 'Default'
}
