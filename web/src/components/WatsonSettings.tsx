import { useEffect, useState } from 'react'
import { APPROVE_ALL_CHOICES, fetchWatsonConfig, saveWatsonConfig, type WatsonSettings as Settings } from '../api/watson'
import { adminHref } from '../lib/router'
import { SaveBar } from './SaveBar'

/**
 * Watson's settings, for administrators (P6-07): where it runs, which is
 * Sherlock's and each connection's to say (6.3 review), and whether
 * changes on clusters tagged production need a second person's approval.
 */
export function WatsonSettings({ open }: { open: boolean }) {
  const [cfg, setCfg] = useState<Settings | null>(null)
  const [draft, setDraft] = useState(false)
  const [approveAll, setApproveAll] = useState(0)
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
        setApproveAll(c.approveAllMinutes ?? 0)
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
      const c = await saveWatsonConfig({ twoPerson: draft, approveAllMinutes: approveAll })
      setCfg(c)
      setDraft(c.twoPerson)
      setApproveAll(c.approveAllMinutes ?? 0)
      setSaved('Saved.')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 12 }} aria-label="Watson" data-testid="watson-settings">
      <p className="rk-fhint" style={{ margin: 0 }}>
        Watson runs kubectl and helm commands. Reads run at once; every change waits for an approval and runs as the person who approved it.
      </p>
      <section style={{ display: 'flex', flexDirection: 'column', gap: 6 }} aria-label="Where Watson runs" data-testid="watson-where">
        <h3 className="rk-h">Where it runs</h3>
        <ul className="rk-fhint" style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4 }}>
          <li>
            Only on clusters where Sherlock is on: turn it on per cluster in{' '}
            <a className="rk-link" href={adminHref('settings', ['sherlock'])}>
              Sherlock’s settings
            </a>
            .
          </li>
          <li>On the cluster the console runs in, changes are refused unless the chart is installed with localCluster.mode=write; reads still run.</li>
          <li>On other clusters, changes follow each connection’s access: Read only, or Read and change.</li>
        </ul>
      </section>
      {!cfg ? (
        <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>
          {error || 'Loading…'}
        </p>
      ) : (
        <>
          <section style={{ display: 'flex', flexDirection: 'column', gap: 6 }} aria-label="Approvals">
            <h3 className="rk-h">Approvals</h3>
            <label className="rk-tcheck" style={{ color: 'var(--fg)', whiteSpace: 'normal' }}>
              <input type="checkbox" checked={draft} onChange={(e) => setDraft(e.target.checked)} data-testid="watson-two-person" />
              <span>A second person approves every change on clusters tagged production; nobody approves their own.</span>
            </label>
          </section>
          <section style={{ display: 'flex', flexDirection: 'column', gap: 6 }} aria-label="Standing approvals">
            <h3 className="rk-h">Standing approvals</h3>
            <label style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, fontSize: 12.5, color: 'var(--fg)' }}>
              <span>A person may approve everything for up to</span>
              <span className="rk-field" style={{ width: 150 }}>
                <select value={approveAll} onChange={(e) => setApproveAll(Number(e.target.value))} data-testid="watson-approve-all-max">
                  <option value={0}>nothing (off)</option>
                  {APPROVE_ALL_CHOICES.map((m) => (
                    <option key={m} value={m}>
                      {m < 60 ? `${m} minutes` : m === 60 ? '1 hour' : `${m / 60} hours`}
                    </option>
                  ))}
                </select>
              </span>
            </label>
            <p className="rk-fhint" style={{ margin: 0 }}>
              With it on, Watson offers <i>Approve all for…</i>: for that long, every card the person could approve is approved as soon as it is ready, their own and, with the two-person rule, other people’s. Every other rule still applies, and the audit log names them as the approver.
            </p>
          </section>
          <SaveBar changed={draft !== cfg.twoPerson || approveAll !== (cfg.approveAllMinutes ?? 0)} busy={busy} onSave={() => void save()} onUndo={() => (setDraft(cfg.twoPerson), setApproveAll(cfg.approveAllMinutes ?? 0))} error={error} saved={saved} testId="watson-settings-save" />
        </>
      )}
    </section>
  )
}
