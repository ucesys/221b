// Settings › SLA (Phase 12, wave 5, CU-06; wave 6): the response and
// resolution targets per priority, P0 to P4 (kept under "0" to "4"). They
// apply to every case a person made (reported, by hand, from an alert) at
// every priority, and to an agent-found case only at P0 and P1. A case
// past one shows it as late in the Inbox and its drawer, naming the target
// and linking here. Read by those who work the cases; set by a SuperAdmin.
import { useEffect, useState } from 'react'
import { fetchSLA, saveSLA, type SLATargets } from '../api/casework'
import { PRIORITIES, PRIORITY } from './cases/parts'
import { SaveBar } from './SaveBar'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** A target in words: "4 h", "5 d", "none". */
export function targetWords(m: number): string {
  if (!m || m <= 0) return 'none'
  if (m % 1440 === 0) return `${m / 1440} d`
  if (m % 60 === 0) return `${m / 60} h`
  return `${m} min`
}

export function SLASettings() {
  const [saved, setSaved] = useState<SLATargets['targets'] | null>(null)
  const [draft, setDraft] = useState<SLATargets['targets'] | null>(null)
  const [maySet, setMaySet] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  useEffect(() => {
    let stop = false
    fetchSLA().then(
      (s) => {
        if (stop) return
        setSaved(s.targets)
        setDraft(s.targets)
        setMaySet(s.maySet)
      },
      (e: unknown) => !stop && setError(errorText(e)),
    )
    return () => {
      stop = true
    }
  }, [])
  const save = async () => {
    if (!draft) return
    setBusy(true)
    setError('')
    setNote('')
    try {
      const s = await saveSLA(draft)
      setSaved(s.targets)
      setDraft(s.targets)
      setNote('Saved.')
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  if (!draft) return error ? <p className="rk-t" data-tone="err">{error}</p> : <p className="rk-fhint">Loading…</p>
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="sla-settings">
      <p className="rk-fhint" style={{ margin: 0 }}>
        How soon each priority asks for a first response from a person and for a resolution. They apply to every case a person made (reported, opened by hand, from an alert) and to an agent-found case only at P0 and P1; the others show “no SLA”. A case opened in the console counts as responded to when it is opened. 0 sets no target. A case past a target shows it as late.
      </p>
      <table className="rp-settable">
        <thead>
          <tr>
            <th scope="col">Priority</th>
            <th scope="col">First response, minutes</th>
            <th scope="col">Resolved, minutes</th>
          </tr>
        </thead>
        <tbody>
          {PRIORITIES.map((n) => {
            const key = String(n)
            const p = `P${n}`
            const t = draft[key] ?? { responseMinutes: 0, resolveMinutes: 0 }
            const put = (k: 'responseMinutes' | 'resolveMinutes', v: number) => setDraft({ ...draft, [key]: { ...t, [k]: Math.max(0, Math.round(v) || 0) } })
            return (
              <tr key={key} data-testid={`sla-${p}`}>
                <th scope="row">{PRIORITY[n]}</th>
                <td>
                  <input className="rk-field" type="number" min={0} disabled={!maySet} value={t.responseMinutes} onChange={(e) => put('responseMinutes', Number(e.target.value))} aria-label={`${p} first response, minutes`} />
                  <span className="rk-fhint"> {targetWords(t.responseMinutes)}</span>
                </td>
                <td>
                  <input className="rk-field" type="number" min={0} disabled={!maySet} value={t.resolveMinutes} onChange={(e) => put('resolveMinutes', Number(e.target.value))} aria-label={`${p} resolved, minutes`} />
                  <span className="rk-fhint"> {targetWords(t.resolveMinutes)}</span>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
      {maySet ? (
        <SaveBar changed={JSON.stringify(draft) !== JSON.stringify(saved)} busy={busy} onSave={() => void save()} onUndo={() => setDraft(saved)} error={error} saved={note} testId="sla-save" />
      ) : (
        <p className="rk-fhint">Only a SuperAdmin sets the SLA.</p>
      )}
    </div>
  )
}
