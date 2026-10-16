import { useEffect, useState } from 'react'
import { deleteSherlockKey, fetchSherlockConfig, priceText, saveSherlockConfig, saveSherlockKey, usd, type SherlockConfig, type SherlockSettings as Settings } from '../api/sherlock'
import { fmtTs } from '../lib/format'
import { Select } from './Select'

/**
 * Sherlock's settings, for administrators (P5-05): on or off per cluster,
 * the Claude API key (kept in a Secret, never shown again), the model,
 * and the daily and monthly spending caps, with what has been spent.
 */
export function SherlockSettings({ open }: { open: boolean }) {
  const [cfg, setCfg] = useState<SherlockConfig | null>(null)
  const [draft, setDraft] = useState<Settings | null>(null)
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [saved, setSaved] = useState('')

  useEffect(() => {
    if (!open) return
    let stop = false
    fetchSherlockConfig()
      .then((c) => {
        if (stop) return
        setCfg(c)
        setDraft(c.settings)
        setError('')
      })
      .catch((e: unknown) => {
        if (!stop) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      stop = true
    }
  }, [open])

  if (!cfg || !draft) {
    return (
      <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Sherlock">
        <h3 className="rk-h">Sherlock</h3>
        <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>
          {error || 'Loading…'}
        </p>
      </section>
    )
  }

  const run = async (what: string, fn: () => Promise<SherlockConfig>, done: string) => {
    setBusy(what)
    setError('')
    setSaved('')
    try {
      const c = await fn()
      setCfg(c)
      setDraft(c.settings)
      setSaved(done)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }
  const changed = JSON.stringify(draft) !== JSON.stringify(cfg.settings)
  const model = cfg.models.find((m) => m.id === draft.model)
  const options: [string, string][] = cfg.models.map((m) => [m.id, `${m.label} — ${priceText(m)}`])
  if (!model) options.push([draft.model, `${draft.model} (not listed: counted at the highest price)`])
  const k = cfg.key
  const keyText = !k.set
    ? 'No key yet. Sherlock answers nothing until one is saved.'
    : k.source === 'chart'
      ? 'Set by the chart (sherlock.existingSecret); change it there.'
      : `Saved${k.setBy ? ` by ${k.setBy}` : ''}${k.setAt ? `, ${fmtTs(k.setAt)}` : ''}. ${k.store === 'secret' ? 'Kept in a Secret in the console’s namespace.' : 'Kept in memory: it is lost when the console restarts.'} It is never shown again.`
  const cap = (n: number) => (n > 0 ? usd(n) : 'no cap')
  const num = (v: string) => {
    const n = Number(v)
    return Number.isFinite(n) && n >= 0 ? Math.round(n * 100) / 100 : 0
  }
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 12 }} aria-label="Sherlock" data-testid="sherlock-settings">
      <h3 className="rk-h">Sherlock</h3>
      <p className="rk-fhint" style={{ margin: 0 }}>
        Sherlock answers questions about a cluster through the Claude API, reading only what the person asking may read. Nothing is sent to the API for a cluster where it is off.
      </p>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <span style={{ fontSize: 12.5, fontWeight: 600 }}>Claude API key</span>
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="sherlock-key-state">
          {keyText}
        </p>
        {k.source !== 'chart' && (
          <form
            style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}
            onSubmit={(e) => {
              e.preventDefault()
              const v = key
              void run('key', () => saveSherlockKey(v), 'The key is saved.').then(() => setKey(''))
            }}
          >
            <label className="rk-field" style={{ flex: '1 1 240px' }}>
              <span className="rk-sr">Claude API key</span>
              <input type="password" autoComplete="off" spellCheck={false} placeholder={k.set ? 'Paste a new key to replace it' : 'Paste the key (sk-ant-…)'} value={key} onChange={(e) => setKey(e.target.value)} data-testid="sherlock-key" />
            </label>
            <button className="rk-btn" type="submit" disabled={!key.trim() || busy !== ''} data-on="true" data-testid="sherlock-key-save">
              Save key
            </button>
            {k.set && (
              <button className="rk-btn" type="button" disabled={busy !== ''} onClick={() => void run('key', deleteSherlockKey, 'The key is removed.')} data-testid="sherlock-key-remove">
                Remove
              </button>
            )}
          </form>
        )}
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <span style={{ fontSize: 12.5, fontWeight: 600 }}>On for</span>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }} role="group" aria-label="Clusters Sherlock answers about">
          {cfg.clusters.map((c) => (
            <label key={c.id} className="rk-tcheck" style={{ color: 'var(--fg)' }}>
              <input type="checkbox" checked={!!draft.clusters?.[c.id]} onChange={(e) => setDraft({ ...draft, clusters: { ...(draft.clusters ?? {}), [c.id]: e.target.checked } })} data-testid="sherlock-cluster" data-cluster={c.id} />
              <span>{c.name}</span>
            </label>
          ))}
        </div>
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <label className="rk-tcheck" style={{ color: 'var(--fg)' }}>
          <input type="checkbox" checked={draft.viewControl !== false} onChange={(e) => setDraft({ ...draft, viewControl: e.target.checked })} data-testid="sherlock-view-control" aria-describedby="sherlock-view-control-hint" />
          <span>Let Sherlock change the view</span>
        </label>
        <p className="rk-fhint" style={{ margin: 0 }} id="sherlock-view-control-hint">
          Sherlock may open tabs and details, set filters, search, sort, show columns and highlight rows in the asking person’s own window while it answers. It never changes a cluster or what others see. Off, it still reads what the person sees.
        </p>
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <span style={{ fontSize: 12.5, fontWeight: 600 }}>Model</span>
        <Select label="Model" value={draft.model} options={options} onChange={(v) => setDraft({ ...draft, model: v })} testId="sherlock-model" width="100%" />
        {model?.note && (
          <p className="rk-fhint" style={{ margin: 0 }}>
            {model.note}
          </p>
        )}
      </div>

      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
          Daily cap (US$)
          <span className="rk-field" style={{ width: 140 }}>
            <input type="number" min={0} step={1} value={draft.dailyUSD} onChange={(e) => setDraft({ ...draft, dailyUSD: num(e.target.value) })} data-testid="sherlock-daily" aria-describedby="sherlock-caps-hint" />
          </span>
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
          Monthly cap (US$)
          <span className="rk-field" style={{ width: 140 }}>
            <input type="number" min={0} step={1} value={draft.monthlyUSD} onChange={(e) => setDraft({ ...draft, monthlyUSD: num(e.target.value) })} data-testid="sherlock-monthly" aria-describedby="sherlock-caps-hint" />
          </span>
        </label>
      </div>
      <p className="rk-fhint" style={{ margin: 0 }} id="sherlock-caps-hint" data-testid="sherlock-spend">
        Spent today {usd(cfg.spend.dayUSD)} of {cap(cfg.settings.dailyUSD)} ({cfg.spend.dayAsked} {cfg.spend.dayAsked === 1 ? 'question' : 'questions'}); this month {usd(cfg.spend.monthUSD)} of {cap(cfg.settings.monthlyUSD)}. Counted from the tokens each answer used, at the model’s list price; 0 is no cap. At a cap, new questions are refused until the day or month ends (UTC).
      </p>

      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <button className="rk-btn" data-on="true" disabled={!changed || busy !== ''} onClick={() => void run('settings', () => saveSherlockConfig(draft), 'Saved.')} data-testid="sherlock-save">
          Save Sherlock settings
        </button>
        {changed && (
          <button className="rk-link" onClick={() => setDraft(cfg.settings)} style={{ fontSize: 12.5 }}>
            Undo changes
          </button>
        )}
        {error ? (
          <span className="rk-t" data-tone="err" role="alert" style={{ fontSize: 12.5 }} data-testid="sherlock-settings-error">
            {error}
          </span>
        ) : (
          saved && (
            <span className="rk-t" data-tone="ok" role="status" style={{ fontSize: 12.5 }}>
              {saved}
            </span>
          )
        )}
      </div>
    </section>
  )
}
