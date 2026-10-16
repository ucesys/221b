import { useEffect, useState } from 'react'
import { fetchWatsonConfig, saveWatsonConfig, type WatsonSettings as Settings } from '../api/watson'

/**
 * Watson's settings, for administrators (P6-07): whether changes on
 * clusters tagged production need a second person's approval. Which
 * clusters may be changed at all is each connection's own setting.
 */
export function WatsonSettings({ open }: { open: boolean }) {
  const [cfg, setCfg] = useState<Settings | null>(null)
  const [draft, setDraft] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState('')
  useEffect(() => {
    if (!open) return
    let stop = false
    fetchWatsonConfig()
      .then((c) => {
        if (stop) return
        setCfg(c)
        setDraft(c.twoPerson)
        setError('')
      })
      .catch((e: unknown) => {
        if (!stop) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      stop = true
    }
  }, [open])
  const save = async () => {
    setBusy(true)
    setError('')
    setSaved('')
    try {
      const c = await saveWatsonConfig({ twoPerson: draft })
      setCfg(c)
      setDraft(c.twoPerson)
      setSaved('Saved.')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Watson" data-testid="watson-settings">
      <h3 className="rk-h">Watson</h3>
      <p className="rk-fhint" style={{ margin: 0 }}>
        Watson runs kubectl and helm commands. Reads run at once; every change waits for an approval and runs as the person who approved it. Clusters connected read-only refuse changes: that is each connection’s own setting.
      </p>
      {!cfg ? (
        <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>
          {error || 'Loading…'}
        </p>
      ) : (
        <>
          <label className="rk-tcheck" style={{ color: 'var(--fg)', whiteSpace: 'normal' }}>
            <input type="checkbox" checked={draft} onChange={(e) => setDraft(e.target.checked)} data-testid="watson-two-person" />
            <span>A second person approves every change on clusters tagged production; nobody approves their own.</span>
          </label>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <button className="rk-btn" data-on="true" disabled={draft === cfg.twoPerson || busy} onClick={() => void save()} data-testid="watson-settings-save">
              Save Watson settings
            </button>
            {(error || saved) && (
              <span className="rk-t" data-tone={error ? 'err' : 'ok'} role="status" style={{ fontSize: 12.5 }}>
                {error || saved}
              </span>
            )}
          </div>
        </>
      )}
    </section>
  )
}
