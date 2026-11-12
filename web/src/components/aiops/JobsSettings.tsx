import { useEffect, useState } from 'react'
import { fetchJobsSettings, saveJobsSettings, type JobsSettings as Settings } from '../../api/jobs'
import { fmtTs } from '../../lib/format'
import { Select } from '../Select'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const EDITORS: [string, string][] = [
  ['admin', 'Admin'],
  ['superViewer', 'SuperViewer'],
  ['viewer', 'Viewer'],
]

/**
 * Settings › Jobs (Phase 12, JB-04, JB-07): who may edit jobs besides a
 * SuperAdmin, and the read-only role agents on their own read the
 * clusters with (Viewer by default), for SuperAdmins.
 */
export function JobsSettings() {
  const [saved, setSaved] = useState<Settings | null>(null)
  const [draft, setDraft] = useState<Settings | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  useEffect(() => {
    let stop = false
    fetchJobsSettings().then(
      (s) => {
        if (stop) return
        setSaved(s)
        setDraft(s)
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
      const s = await saveJobsSettings({ editors: draft.editors ?? [], readerRole: draft.readerRole ?? 'viewer', readerGroup: draft.readerGroup ?? '' })
      setSaved(s)
      setDraft(s)
      setNote('Saved.')
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  if (!draft || !saved) {
    return (
      <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }} data-testid="jobs-settings">
        {error || 'Loading…'}
      </p>
    )
  }
  const editors = draft.editors ?? []
  const role = draft.readerRole ?? 'viewer'
  const changed = JSON.stringify({ e: [...editors].sort(), r: role, g: draft.readerGroup ?? '' }) !== JSON.stringify({ e: [...(saved.editors ?? [])].sort(), r: saved.readerRole ?? 'viewer', g: saved.readerGroup ?? '' })
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 18 }} aria-label="Jobs" data-testid="jobs-settings">
      <p className="rk-fhint" style={{ margin: 0 }}>
        Jobs start agents with nobody at the screen, in{' '}
        <a className="rk-link" href="#/aiops/jobs">
          AIOps › Jobs
        </a>
        . Every change to a job is in the audit log with its old and new values.
      </p>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <span style={{ fontSize: 13, fontWeight: 600 }}>Who may edit jobs</span>
        <label className="rk-tcheck" style={{ color: 'var(--fg)' }}>
          <input type="checkbox" checked disabled /> SuperAdmin
        </label>
        {EDITORS.map(([id, name]) => (
          <label key={id} className="rk-tcheck" style={{ color: 'var(--fg)' }}>
            <input type="checkbox" checked={editors.includes(id)} onChange={(e) => setDraft({ ...draft, editors: e.target.checked ? [...editors, id] : editors.filter((x) => x !== id) })} data-testid={`jobs-editor-${id}`} /> {name}
          </label>
        ))}
        <span className="rk-fhint">Who may see the runs and findings: SuperAdmins and SuperViewers, who read across every namespace as agents on their own may, and the roles named here.</span>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <span style={{ fontSize: 13, fontWeight: 600 }}>Agents on their own read as</span>
        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <Select
            label="Role"
            value={role}
            options={[
              ['viewer', 'Viewer'],
              ['superViewer', 'SuperViewer'],
            ]}
            onChange={(v) => setDraft({ ...draft, readerRole: v as Settings['readerRole'] })}
            testId="jobs-reader-role"
          />
          {role === 'viewer' && (
            <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)' }}>
              With the namespaces of the group
              <span className="rk-field" style={{ width: 220 }}>
                <input value={draft.readerGroup ?? ''} placeholder="none" onChange={(e) => setDraft({ ...draft, readerGroup: e.target.value })} aria-label="Group of the role Viewer" data-testid="jobs-reader-group" />
              </span>
            </label>
          )}
        </div>
        <span className="rk-fhint">
          The clusters know them as the user <code className="rk-mono rk-icode">221b:agents</code>. As a Viewer they read the namespaces bound to that user, and those of a group of the role Viewer when one is named; as a SuperViewer, every
          namespace. Never a Secret's values, and nothing that changes anything: a change they want waits in the Inbox.
        </span>
      </div>
      {error && (
        <p className="rk-t" data-tone="err" style={{ margin: 0 }} data-testid="jobs-settings-error">
          {error}
        </p>
      )}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <button className="rk-btn" data-primary="true" disabled={!changed || busy} onClick={() => void save()} data-testid="jobs-settings-save">
          Save
        </button>
        <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>{note || (changed ? 'Not saved yet.' : saved.setBy ? `Set by ${saved.setBy}${saved.setAt ? `, ${fmtTs(saved.setAt)}` : ''}.` : 'No changes.')}</span>
      </div>
    </section>
  )
}
