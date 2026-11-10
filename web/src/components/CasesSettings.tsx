// Settings › Cases (Phase 12, wave 6): how long closed agent-found cases
// are kept, how far back New Case looks for cases closed lately, when an
// exception is stale, how many exceptions an agent is told about, and how
// many new cases one run may open; after how many minutes a critical alert
// that no case covers opens one by itself (CF-78, 0 is never); and how many
// agent-found cases people closed as duplicates (read only). Set by a
// SuperAdmin, read by the console-wide roles.
import { useEffect, useState } from 'react'
import { fetchCasesSettings, saveCasesSettings, type CasesSettings as Settings } from '../api/casework'
import { SaveBar } from './SaveBar'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

type Field = 'keepAgentClosedDays' | 'lookBackDays' | 'exceptionStaleDays' | 'exceptionsPerAgent' | 'newCasesPerRun' | 'criticalAlertMinutes'

/** After how many minutes a critical alert opens a case by itself, until a SuperAdmin says otherwise (CF-78). */
export const CRITICAL_ALERT_MINUTES = 15

/** Each setting: its words, its unit, what it does, and its range (from 1 unless min says otherwise). */
export const FIELDS: { id: Field; label: string; unit: string; hint: string; max: number; min?: number }[] = [
  { id: 'keepAgentClosedDays', label: 'Keep closed agent-found cases', unit: 'days', hint: 'Then they are deleted. Cases people made keep the issue service’s retention.', max: 3650 },
  { id: 'lookBackDays', label: 'New Case looks back', unit: 'days', hint: 'For cases closed as fixed or no action needed, to show beside a new one.', max: 3650 },
  { id: 'exceptionStaleDays', label: 'An exception is stale after', unit: 'days', hint: 'Without a match: Mycroft › Exceptions shows it as stale, to review.', max: 3650 },
  { id: 'exceptionsPerAgent', label: 'Exceptions an agent is told', unit: 'at most', hint: 'The most recently matched first. The console still drops every excepted finding.', max: 500 },
  { id: 'newCasesPerRun', label: 'New cases one run may open', unit: 'at most', hint: 'The rest are listed in the run’s report as capped.', max: 500 },
  {
    id: 'criticalAlertMinutes',
    label: 'Open a case for a critical alert still going after',
    unit: 'minutes',
    hint: 'Only when no open case covers it and no person acknowledged it: the alert diagnosis job is off, failed or found nothing. P0, opened by the console. 0 turns this off.',
    max: 1440,
    min: 0,
  },
]

/** Why a value is out of range, or "" when it is fine: 1 to 3650 days, 1 to 500 for counts, 0 to 1440 minutes. */
export function rangeError(v: number, max: number, min = 1): string {
  return Number.isInteger(v) && v >= min && v <= max ? '' : `From ${min} to ${max}.`
}

/** The settings as the page edits them: the safety net's minutes always said. */
const withMinutes = (s: Settings): Settings => ({ ...s, criticalAlertMinutes: s.criticalAlertMinutes ?? CRITICAL_ALERT_MINUTES })

export function CasesSettings() {
  const [saved, setSaved] = useState<Settings | null>(null)
  const [draft, setDraft] = useState<Settings | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  useEffect(() => {
    let stop = false
    fetchCasesSettings().then(
      (s) => {
        if (stop) return
        setSaved(withMinutes(s))
        setDraft(withMinutes(s))
      },
      (e: unknown) => !stop && setError(errorText(e)),
    )
    return () => {
      stop = true
    }
  }, [])
  if (!draft || !saved) {
    return (
      <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }} data-testid="cases-settings">
        {error || 'Loading…'}
      </p>
    )
  }
  const maySet = saved.maySet !== false
  const bad = FIELDS.some((f) => rangeError(draft[f.id] ?? NaN, f.max, f.min))
  const changed = FIELDS.some((f) => draft[f.id] !== saved[f.id])
  const save = async () => {
    if (bad) {
      setError('Fix the values marked above, then save.')
      return
    }
    setBusy(true)
    setError('')
    setNote('')
    try {
      const out: Settings = {
        keepAgentClosedDays: draft.keepAgentClosedDays,
        lookBackDays: draft.lookBackDays,
        exceptionStaleDays: draft.exceptionStaleDays,
        exceptionsPerAgent: draft.exceptionsPerAgent,
        newCasesPerRun: draft.newCasesPerRun,
        criticalAlertMinutes: draft.criticalAlertMinutes,
      }
      const s = withMinutes(await saveCasesSettings(out))
      setSaved(s)
      setDraft(s)
      setNote('Saved.')
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }} data-testid="cases-settings">
      <p className="rk-fhint" style={{ margin: 0 }}>
        How the console keeps the cases the agents find, how much one run may do, and when a critical alert opens a case by itself. Every problem is a case, whether an agent found it or a person reported it.
      </p>
      {FIELDS.map((f) => {
        const v = draft[f.id] ?? NaN
        const why = rangeError(v, f.max, f.min)
        return (
          <label key={f.id} style={{ display: 'flex', flexDirection: 'column', gap: 4 }} data-testid={`cases-setting-${f.id}`}>
            <span style={{ fontSize: 12.5, fontWeight: 500 }}>{f.label}</span>
            <span style={{ display: 'inline-flex', gap: 8, alignItems: 'center' }}>
              <input className="rk-field" type="number" min={f.min ?? 1} max={f.max} style={{ width: 110 }} disabled={!maySet || busy} value={Number.isFinite(v) ? v : ''} onChange={(e) => setDraft({ ...draft, [f.id]: e.target.value === '' ? NaN : Number(e.target.value) })} aria-invalid={why ? true : undefined} data-testid={`cases-${f.id}`} />
              <span className="rk-fhint">{f.unit}</span>
            </span>
            {why ? (
              <span className="rk-t" data-tone="err" style={{ fontSize: 12 }} role="alert">
                {why}
              </span>
            ) : (
              <span className="rk-fhint">{f.hint}</span>
            )}
          </label>
        )
      })}
      <p className="rk-fhint" style={{ margin: 0 }} data-testid="cases-duplicates">
        Agent-found cases people closed as duplicates: <strong>{saved.duplicatesOfAgentCases ?? 0}</strong>. Many mean the agents name the same problem differently; their validation rules may need work.
      </p>
      {maySet ? (
        <SaveBar changed={changed} busy={busy} onSave={() => void save()} onUndo={() => { setDraft(saved); setError('') }} error={error} saved={note} testId="cases-save" />
      ) : (
        <p className="rk-fhint">Only a SuperAdmin changes these.</p>
      )}
    </div>
  )
}
