import { useEffect, useState } from 'react'
import { currentRoute, navigate, withParams } from '../../lib/router'
import { fetchAI, saveAgent, saveLimits, stateLabel, stateTone, type AgentView, type AIOverview } from '../../api/ai'
import { usd } from '../../api/sherlock'
import { useShowCosts } from '../../lib/costs'
import { AgentDrawer } from './AgentDrawer'
import { ShowCostsSection } from './ShowCosts'

/**
 * Settings › AI › Agents (Phase 12): every agent with its purpose, model,
 * budget, clusters and where its instructions stand; one opens in a drawer
 * to change it. The overall limits, every agent's spend together, are
 * Sherlock's spending caps of before.
 */
export function AgentsSection({ onToast, initial }: { onToast?: (msg: string) => void; initial?: string }) {
  const [ov, setOv] = useState<AIOverview | null>(null)
  const [error, setError] = useState('')
  const [open, setOpen] = useState<string | null>(initial ?? null)
  // An agent named in the address (#/admin/settings/agents/sherlock), as
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
        Each agent has one purpose and its own model, from any provider. Agents hand work to each other; every change goes to Watson as a card a person approves.
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
            Overall limit {ov.dailyUSD ? `${usd(ov.dailyUSD)} a day` : 'none a day'}
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
      <Limits ov={ov} onSaved={setOv} />
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

/** The overall limits: what every agent may spend together, a day and a month. */
function Limits({ ov, onSaved }: { ov: AIOverview; onSaved: (o: AIOverview) => void }) {
  const [daily, setDaily] = useState(ov.dailyUSD)
  const [monthly, setMonthly] = useState(ov.monthlyUSD)
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<{ text: string; tone: string } | null>(null)
  const changed = daily !== ov.dailyUSD || monthly !== ov.monthlyUSD
  const costs = useShowCosts()
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Overall limits">
      <h3 className="rk-h">Overall limits</h3>
      <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'flex-end' }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
          A day (US$)
          <span className="rk-field" style={{ width: 140 }}>
            <input type="number" min={0} step={1} value={daily} onChange={(e) => setDaily(Math.max(0, Number(e.target.value) || 0))} data-testid="limit-daily" />
          </span>
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
          A month (US$)
          <span className="rk-field" style={{ width: 140 }}>
            <input type="number" min={0} step={1} value={monthly} onChange={(e) => setMonthly(Math.max(0, Number(e.target.value) || 0))} data-testid="limit-monthly" />
          </span>
        </label>
        <button
          className="rk-btn"
          data-primary="true"
          disabled={!changed || busy}
          data-testid="limits-save"
          onClick={async () => {
            setBusy(true)
            try {
              onSaved(await saveLimits(daily, monthly))
              setMsg({ text: 'Saved.', tone: 'ok' })
            } catch (e) {
              setMsg({ text: e instanceof Error ? e.message : String(e), tone: 'err' })
            } finally {
              setBusy(false)
            }
          }}
        >
          Save limits
        </button>
        {msg && (
          <span className="rk-t" data-tone={msg.tone} style={{ fontSize: 12.5 }}>
            {msg.text}
          </span>
        )}
      </div>
      <p className="rk-fhint" style={{ margin: 0 }}>
        Every agent’s spend counts here, besides its own caps: once a limit is reached no agent answers until it lifts, tomorrow or next month (UTC). 0 is no limit.{costs ? ` Spent this month: ${usd(ov.spend.monthUSD)}.` : ''}
      </p>
    </section>
  )
}
