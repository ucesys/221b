import { useEffect, useState } from 'react'
import { fetchIssueSettings, saveIssueSettings, type IssueSettings as Settings } from '../api/issues'
import { SaveBar } from './SaveBar'
import { aiHref } from '../lib/router'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/**
 * The issue service's settings (Phase 12, IS-29), for SuperAdmins: the
 * match threshold, the timers and the webhook. The targets are Administration › AI ›
 * SLA's, the company or IT name Administration › Settings › General's, and the report
 * page's address the chart's issues.reportURL (0.12.1).
 */
export function IssueSettings({ open }: { open: boolean }) {
  const [saved, setSaved] = useState<Settings | null>(null)
  const [draft, setDraft] = useState<Settings | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  useEffect(() => {
    if (!open) return
    let stop = false
    fetchIssueSettings().then(
      (s) => {
        if (stop) return
        setSaved(s)
        setDraft(s)
      },
      (e: unknown) => {
        if (!stop) setError(errorText(e))
      },
    )
    return () => {
      stop = true
    }
  }, [open])
  const save = async (): Promise<boolean> => {
    if (!draft) return false
    setBusy(true)
    setError('')
    setNote('')
    try {
      const s = await saveIssueSettings(draft)
      setSaved(s)
      setDraft(s)
      setNote('Saved.')
      return true
    } catch (e) {
      setError(errorText(e))
      return false
    } finally {
      setBusy(false)
    }
  }
  const set = <K extends keyof Settings>(k: K, v: Settings[K]) => setDraft((d) => (d ? { ...d, [k]: v } : d))
  const num = (k: keyof Settings, label: string, unit: string) => (
    <label className="rp-setrow">
      <span>{label}</span>
      <span className="rk-field" style={{ width: 110 }}>
        <input type="number" min={0} value={Number(draft?.[k] ?? 0)} onChange={(e) => set(k, Number(e.target.value) as never)} aria-label={label} />
      </span>
      <span className="rk-fhint">{unit}</span>
    </label>
  )
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 16 }} aria-label="Issues" data-testid="issue-settings">
      <p className="rk-fhint" style={{ margin: 0 }}>
        People report problems at /report, or the chart’s <span className="rk-mono">issues.reportURL</span>; each is matched to the known issues.
      </p>
      {!draft || !saved ? (
        <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>
          {error || 'Loading…'}
        </p>
      ) : (
        <>
          <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Matching and limits">
            <h3 className="rk-h">Matching and limits</h3>
            {num('matchThreshold', 'Link to a known issue at', '% confidence or more; the reporter is told its workaround and expected fix')}
            {num('ratePerHour', 'Reports one person may send', 'an hour')}
            {num('retentionDays', 'Keep a closed report’s text', 'days')}
            {num('duplicateHours', 'Alike reports on one service are similar within', 'hours')}
            {num('bumpP2At', 'Raise to P2 at', 'open reports on one known issue (0: never)')}
            {num('bumpP1At', 'Raise to P1 at', 'open reports on one known issue (0: never)')}
          </section>
          <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Timers">
            <h3 className="rk-h">Timers</h3>
            {num('reopenDays', 'A closed report reopens within', 'days')}
            {num('fixedDays', 'A fix nobody answers closes after', 'days')}
            {num('needInfoDays', 'A question nobody answers closes after', 'days')}
          </section>
          {/* --- Cases replace reports (Phase 12, wave 5): the response targets are Administration › AI › SLA's now. */}
          <p className="rk-fhint" style={{ margin: 0 }} data-testid="issue-settings-sla">
            The response and resolution targets per priority are in{' '}
            <a className="rk-link" href={aiHref('sla')}>
              Administration › AI › SLA
            </a>
            , for every case.
          </p>
          <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Webhook">
            <h3 className="rk-h">Webhook</h3>
            <span className="rk-field">
              <input value={draft.webhook.url} placeholder="https://hooks.slack.com/services/…" onChange={(e) => set('webhook', { ...draft.webhook, url: e.target.value })} aria-label="Webhook address" />
            </span>
            <span className="rk-fhint">Told of every change as JSON with a text field Slack shows, carrying summaries only: never a report’s text. With the network policy on, its address must be allowed in the chart.</span>
          </section>
          <SaveBar changed={JSON.stringify(draft) !== JSON.stringify(saved)} busy={busy} onSave={() => void save()} save={save} onUndo={() => setDraft(saved)} error={error} saved={note} testId="issue-settings-save" />
        </>
      )}
    </section>
  )
}
