import { useState } from 'react'
import { saveShowCosts } from '../../api/settings'
import { setShowCosts, useShowCosts } from '../../lib/costs'

/**
 * Show costs (Phase 12, wave 6c, CF-40), in Settings › AI › Agents: a
 * SuperAdmin's switch for everyone. Off, no dollar amount shows anywhere
 * but in the budget fields where budgets are set; the budgets still hold.
 * It is saved at once, and every console follows it.
 */
export function ShowCostsSection() {
  const shown = useShowCosts()
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<{ text: string; tone: string } | null>(null)
  const pick = async (on: boolean) => {
    if (on === shown) return
    setBusy(true)
    setMsg(null)
    try {
      const v = await saveShowCosts(on)
      setShowCosts(v.showCosts)
      setMsg({ text: v.showCosts ? 'Costs show again for everyone.' : 'Costs are hidden for everyone.', tone: 'ok' })
    } catch (e) {
      setMsg({ text: e instanceof Error ? e.message : String(e), tone: 'err' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Show costs">
      <h3 className="rk-h">Show costs</h3>
      <div style={{ display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap' }}>
        <div className="rk-seg" role="group" aria-label="Show costs to everyone" style={{ alignSelf: 'flex-start' }} data-testid="settings-show-costs">
          <button aria-pressed={shown} disabled={busy} onClick={() => void pick(true)} data-testid="settings-show-costs-on">
            Show
          </button>
          <button aria-pressed={!shown} disabled={busy} onClick={() => void pick(false)} data-testid="settings-show-costs-off">
            Hide
          </button>
        </div>
        {msg && (
          <span className="rk-t" data-tone={msg.tone} role={msg.tone === 'err' ? 'alert' : undefined} style={{ fontSize: 12.5 }} data-testid="settings-show-costs-msg">
            {msg.text}
          </span>
        )}
      </div>
      <p className="rk-fhint" style={{ margin: 0 }}>
        Hidden, nobody sees a dollar amount: not on runs, jobs, agents, tests or Sherlock’s answers. The budget fields where budgets are set still show them, and every budget still holds.
      </p>
    </section>
  )
}
