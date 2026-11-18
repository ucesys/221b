import { useEffect, useState } from 'react'
import { fetchAI, saveLimits, type AIOverview, type Caps } from '../../api/ai'
import { usd } from '../../api/sherlock'
import { useShowCosts } from '../../lib/costs'
import { aiHref } from '../../lib/router'
import { SaveBar } from '../SaveBar'

/** Where the total limits are on Administration › AI › Limits (AI-37). */
export const LIMITS_TOTAL_ID = 'limits-total'

/** The address of Administration › AI › Limits. */
export const LIMITS_HREF = aiHref('limits')

/** The words beside every limit field: 0 is no limit. */
export const NO_LIMIT = '0 means no limit.'

const capsOf = (c?: Caps | null): Caps => ({ dailyUSD: c?.dailyUSD ?? 0, monthlyUSD: c?.monthlyUSD ?? 0 })

const amount = (v: string) => Math.max(0, Number(v) || 0)

/**
 * Administration › AI › Limits (Phase 12.1, AI-37): what the agents may spend. The
 * total counts every agent on every provider; each provider's counts the
 * agents while on it; each agent's own cap is set in its drawer and only
 * shown here. The amounts spent show only when costs show (CF-40); the
 * limit fields are there either way.
 */
export function LimitsSection({ onToast }: { onToast?: (msg: string) => void }) {
  const [ov, setOv] = useState<AIOverview | null>(null)
  const [error, setError] = useState('')
  const [total, setTotal] = useState<Caps | null>(null)
  const [providers, setProviders] = useState<Record<string, Caps>>({})
  const [busy, setBusy] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [saved, setSaved] = useState('')
  const costs = useShowCosts()
  useEffect(() => {
    fetchAI()
      .then(setOv)
      .catch((e: Error) => setError(e.message))
  }, [])
  if (!ov)
    return (
      <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>
        {error || 'Loading the limits…'}
      </p>
    )
  const savedTotal: Caps = { dailyUSD: ov.dailyUSD, monthlyUSD: ov.monthlyUSD }
  const shownTotal = total ?? savedTotal
  const savedOf = (id: string) => capsOf(ov.providers.find((p) => p.id === id)?.limit)
  const shownOf = (id: string) => providers[id] ?? savedOf(id)
  const same = (a: Caps, b: Caps) => a.dailyUSD === b.dailyUSD && a.monthlyUSD === b.monthlyUSD
  const changedProviders = Object.keys(providers).filter((id) => !same(providers[id]!, savedOf(id)))
  const changed = !same(shownTotal, savedTotal) || changedProviders.length > 0
  const undo = () => {
    setTotal(null)
    setProviders({})
    setSaveError('')
  }
  const save = async (): Promise<boolean> => {
    setBusy(true)
    setSaveError('')
    setSaved('')
    try {
      const next = await saveLimits(shownTotal.dailyUSD, shownTotal.monthlyUSD, changedProviders.length ? Object.fromEntries(changedProviders.map((id) => [id, providers[id]!])) : undefined)
      setOv(next)
      setTotal(null)
      setProviders({})
      setSaved('Saved.')
      onToast?.('Limits saved')
      return true
    } catch (e) {
      setSaveError(e instanceof Error ? e.message : String(e))
      return false
    } finally {
      setBusy(false)
    }
  }
  const field = (label: string, value: number, set: (n: number) => void, testId: string, extra: Record<string, string> = {}) => (
    <span className="rk-field" style={{ width: 120 }}>
      <input type="number" min={0} step={1} value={value} aria-label={label} onChange={(e) => set(amount(e.target.value))} data-testid={testId} {...extra} />
    </span>
  )
  return (
    <>
      <p className="rk-fhint" style={{ margin: 0 }}>
        What the agents may spend in US dollars, a day and a month (UTC). {NO_LIMIT}
      </p>
      <section id={LIMITS_TOTAL_ID} style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Total" data-testid="limits-total">
        <h3 className="rk-h">Total</h3>
        <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
            A day (US$)
            {field('Total a day (US$)', shownTotal.dailyUSD, (n) => setTotal({ ...shownTotal, dailyUSD: n }), 'limit-daily')}
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
            A month (US$)
            {field('Total a month (US$)', shownTotal.monthlyUSD, (n) => setTotal({ ...shownTotal, monthlyUSD: n }), 'limit-monthly')}
          </label>
        </div>
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="limits-total-hint">
          All agents on all providers together, checked first, then the provider’s limit, then the agent’s own cap; once one is reached, the agents it covers stop until the next day or month. {NO_LIMIT}
          {costs ? ` Spent: ${usd(ov.spend.dayUSD)} today, ${usd(ov.spend.monthUSD)} this month.` : ''}
        </p>
      </section>
      <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Providers">
        <h3 className="rk-h">Providers</h3>
        <div style={{ overflowX: 'auto' }}>
          <table className="tb-table" data-testid="limits-providers" style={{ width: '100%' }}>
            <thead>
              <tr>
                <th scope="col">Provider</th>
                <th scope="col">A day (US$)</th>
                <th scope="col">A month (US$)</th>
                {costs && <th scope="col">Spent today</th>}
                {costs && <th scope="col">Spent this month</th>}
              </tr>
            </thead>
            <tbody>
              {ov.providers.map((p) => {
                const c = shownOf(p.id)
                const set = (next: Caps) => setProviders({ ...providers, [p.id]: next })
                return (
                  <tr key={p.id} data-testid="limits-provider" data-provider={p.id}>
                    <td>
                      <span style={{ fontWeight: 600 }}>{p.name}</span>
                      {p.default && (
                        <span className="rk-fhint" style={{ display: 'block', fontSize: 11 }}>
                          default
                        </span>
                      )}
                    </td>
                    <td>{field(`${p.name} a day (US$)`, c.dailyUSD, (n) => set({ ...c, dailyUSD: n }), 'provider-limit-daily', { 'data-provider': p.id })}</td>
                    <td>{field(`${p.name} a month (US$)`, c.monthlyUSD, (n) => set({ ...c, monthlyUSD: n }), 'provider-limit-monthly', { 'data-provider': p.id })}</td>
                    {costs && <td className="rk-num">{usd(p.spend?.dayUSD ?? 0)}</td>}
                    {costs && <td className="rk-num">{usd(p.spend?.monthUSD ?? 0)}</td>}
                  </tr>
                )
              })}
            </tbody>
            <tfoot>
              <ProvidersTotal ov={ov} total={shownTotal} costs={costs} />
            </tfoot>
          </table>
        </div>
        <p className="rk-fhint" style={{ margin: 0 }}>
          Each provider’s limit counts what every agent spends while on it, and its connection tests. {NO_LIMIT}
        </p>
      </section>
      <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Agents">
        <h3 className="rk-h">Agents</h3>
        <div style={{ overflowX: 'auto' }}>
          <table className="tb-table" data-testid="limits-agents" style={{ width: '100%' }}>
            <thead>
              <tr>
                <th scope="col">Agent</th>
                <th scope="col">Provider</th>
                <th scope="col">Own cap a day</th>
                <th scope="col">Own cap a month</th>
                {costs && <th scope="col">Spent today</th>}
                {costs && <th scope="col">Spent this month</th>}
              </tr>
            </thead>
            <tbody>
              {ov.agents.map((a) => (
                <tr key={a.id} data-testid="limits-agent" data-agent={a.id}>
                  <td>
                    <a className="rk-link" style={{ fontWeight: 600 }} href={aiHref('agents', [a.id])} data-testid="limits-agent-link">
                      {a.name}
                    </a>
                  </td>
                  <td style={{ fontSize: 12.5 }}>{a.providerName}</td>
                  <td className="rk-num">{capText(a.settings.dailyUSD, costs)}</td>
                  <td className="rk-num">{capText(a.settings.monthlyUSD, costs)}</td>
                  {costs && <td className="rk-num">{usd(a.spend.dayUSD)}</td>}
                  {costs && <td className="rk-num">{usd(a.spend.monthUSD)}</td>}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="rk-fhint" style={{ margin: 0 }}>
          An agent’s own cap is set in its drawer on Administration › AI › Agents: open an agent to change it. {NO_LIMIT}
        </p>
      </section>
      <SaveBar what="Administration › AI › Limits" changed={changed} busy={busy} onSave={() => void save()} save={save} onUndo={undo} error={saveError} saved={changed ? '' : saved} testId="limits-save" />
    </>
  )
}

/** What the providers' spend leaves out of the ledger's total: spent before the upgrade counted per provider, or on a provider since removed (AI-42). */
export function uncounted(ov: Pick<AIOverview, 'spend' | 'providers'>): { day: number; month: number } {
  const sum = (k: 'dayUSD' | 'monthUSD') => ov.providers.reduce((n, p) => n + (p.spend?.[k] ?? 0), 0)
  // Cents: the sums of floats differ by less.
  const left = (all: number, k: 'dayUSD' | 'monthUSD') => {
    const d = all - sum(k)
    return d >= 0.005 ? d : 0
  }
  return { day: left(ov.spend.dayUSD, 'dayUSD'), month: left(ov.spend.monthUSD, 'monthUSD') }
}

/**
 * The Providers table's Total row (AI-42): the total limits, set under
 * Total above, and the spend today and this month from the ledger, which
 * is the true total; where the providers' rows add up to less, it says how
 * much was not counted per provider. No amount spent while costs are hidden.
 */
function ProvidersTotal({ ov, total, costs }: { ov: AIOverview; total: Caps; costs: boolean }) {
  const left = uncounted(ov)
  const limit = (v: number) => (
    <a
      className="rk-link"
      href={`#${LIMITS_TOTAL_ID}`}
      onClick={(e) => {
        e.preventDefault()
        document.getElementById(LIMITS_TOTAL_ID)?.scrollIntoView?.({ block: 'start' })
      }}
      title="Set under Total, above"
    >
      {v ? v : 'no limit'}
    </a>
  )
  const spent = (v: number, x: number, testId: string) => (
    <td className="rk-num" data-testid={testId}>
      <strong>{usd(v)}</strong>
      {x > 0 && (
        <span className="rk-fhint" style={{ display: 'block', fontSize: 11, fontWeight: 400, whiteSpace: 'normal' }} data-testid="limits-providers-uncounted">
          includes {usd(x)} not counted per provider (before the upgrade, or on a provider since removed)
        </span>
      )}
    </td>
  )
  return (
    <tr data-testid="limits-providers-total" style={{ borderTop: '2px solid var(--line)' }}>
      <th scope="row" style={{ textAlign: 'left' }}>
        Total
      </th>
      <td style={{ fontSize: 12.5 }}>{limit(total.dailyUSD)}</td>
      <td style={{ fontSize: 12.5 }}>{limit(total.monthlyUSD)}</td>
      {costs && spent(ov.spend.dayUSD, left.day, 'limits-providers-total-day')}
      {costs && spent(ov.spend.monthUSD, left.month, 'limits-providers-total-month')}
    </tr>
  )
}

/** An agent's own cap: its amount, "none" for 0, or only whether there is one while costs are hidden. */
export function capText(v: number | undefined, costs: boolean): string {
  if (!v) return 'none'
  return costs ? usd(v) : 'set'
}
