import { useRef, useState } from 'react'
import { BACKUP_FORMAT, fetchBackup, partCount, RESTORE_PARTS, restoreBackup, type BackupFile, type RestorePart, type RestoreReport } from '../api/admin'
import { fmtTs } from '../lib/format'
import { download } from './AdminAudit'
import type { AdminProps } from './AdminPage'
import { ConfirmDialog } from './ConfirmDialog'
import { DownloadIcon } from './Icons'

const HELD = ['the settings: refresh rate, time format, fleets, acknowledged alerts, Sherlock’s and Watson’s settings, grace period, banners', 'the local users made in the console: name, email, groups, super roles', 'the cluster connections, without their credentials', 'the Helm applications: source, values and parameters as typed, commands, what was deployed from where', 'the Toolbox’s session records', 'Sherlock’s spend ledger', 'the audit log the console keeps']

const LEFT_OUT = ['passwords, tokens, kubeconfigs, certificates, OIDC secrets, Sherlock’s API key: no credential of any kind', 'sign-in sessions', 'Watson’s cards and Sherlock’s conversations, which live in memory', 'the Toolbox’s session recordings, which are in the transcript store']

/**
 * Administration › Backup (asked October 30, 2026): one JSON file with
 * everything the console keeps of its own, and never a credential; and a
 * restore of the parts picked from such a file.
 */
export function AdminBackup({ onToast }: AdminProps) {
  const [taking, setTaking] = useState(false)
  const [takeErr, setTakeErr] = useState('')
  const [last, setLast] = useState<BackupFile | null>(null)
  const take = async () => {
    setTaking(true)
    setTakeErr('')
    try {
      const f = await fetchBackup()
      download(`221b-backup-${f.taken.slice(0, 19).replace(/[:T]/g, '-')}.json`, JSON.stringify(f, null, 2))
      setLast(f)
      onToast('The backup is downloading.')
    } catch (e) {
      setTakeErr(e instanceof Error ? e.message : String(e))
    } finally {
      setTaking(false)
    }
  }

  const file = useRef<HTMLInputElement>(null)
  const [picked, setPicked] = useState<{ name: string; backup: BackupFile } | null>(null)
  const [pickErr, setPickErr] = useState('')
  const [parts, setParts] = useState<RestorePart[]>([])
  const [confirm, setConfirm] = useState(false)
  const [busy, setBusy] = useState(false)
  const [restoreErr, setRestoreErr] = useState('')
  const [report, setReport] = useState<RestoreReport | null>(null)
  const onFile = async (f: File | undefined) => {
    setReport(null)
    setPickErr('')
    setPicked(null)
    if (!f) return
    try {
      const parsed = JSON.parse(await f.text()) as Partial<BackupFile>
      if (!parsed || parsed.format !== BACKUP_FORMAT) throw new Error(`This is not a 221B backup: its format is ${JSON.stringify(parsed?.format ?? null)}, not ${BACKUP_FORMAT}.`)
      const b = parsed as BackupFile
      setPicked({ name: f.name, backup: b })
      setParts(RESTORE_PARTS.filter((p) => partCount(b, p.id) > 0).map((p) => p.id))
    } catch (e) {
      setPickErr(e instanceof Error ? e.message : String(e))
    }
  }
  const restore = async () => {
    if (!picked) return
    setBusy(true)
    setRestoreErr('')
    try {
      const rep = await restoreBackup(picked.backup, parts)
      setReport(rep)
      setConfirm(false)
      onToast('Restored.')
    } catch (e) {
      setRestoreErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const b = picked?.backup
  return (
    <>
      <section className="rk-card" style={{ padding: '16px 18px', display: 'flex', flexDirection: 'column', gap: 12 }} aria-label="Take a backup" data-testid="admin-backup">
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 12 }}>
          <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>Take a backup</h2>
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>One JSON file with everything the console keeps of its own. Keep it where you keep secrets anyway: it names people, clusters and releases.</span>
          <button className="rk-btn" data-on style={{ marginLeft: 'auto' }} onClick={() => void take()} disabled={taking} data-testid="backup-take">
            <DownloadIcon />
            {taking ? 'Taking…' : 'Download backup'}
          </button>
        </div>
        {takeErr && <p className="tb-err">{takeErr}</p>}
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))', gap: 16, fontSize: 12.5 }}>
          <div>
            <h3 className="rk-h" style={{ margin: '0 0 6px' }}>In the backup</h3>
            <ul style={{ margin: 0, paddingLeft: 18, color: 'var(--fg2)', display: 'flex', flexDirection: 'column', gap: 3 }}>
              {HELD.map((x) => (
                <li key={x}>{x}</li>
              ))}
            </ul>
          </div>
          <div>
            <h3 className="rk-h" style={{ margin: '0 0 6px' }}>Never in it</h3>
            <ul style={{ margin: 0, paddingLeft: 18, color: 'var(--fg2)', display: 'flex', flexDirection: 'column', gap: 3 }}>
              {(last?.leftOut ?? LEFT_OUT).map((x) => (
                <li key={x}>{x}</li>
              ))}
            </ul>
          </div>
        </div>
        {last && (
          <p className="tb-hint" style={{ margin: 0 }} data-testid="backup-last">
            Last backup: taken {fmtTs(last.taken)} by {last.by || 'you'} from version {last.version}; {last.users?.length ?? 0} users, {last.connections?.length ?? 0} connections, {last.apps?.length ?? 0} applications, {last.toolboxSessions?.length ?? 0} session records, {last.audit?.length ?? 0} audit events.
          </p>
        )}
      </section>
      <section className="rk-card" style={{ padding: '16px 18px', display: 'flex', flexDirection: 'column', gap: 12 }} aria-label="Restore from a backup" data-testid="admin-restore">
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 12 }}>
          <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>Restore from a backup</h2>
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>Pick a backup file, choose what to bring back, and read what is left to do by hand. Connections are never restored: their credentials are not in a backup.</span>
          <input ref={file} type="file" accept="application/json,.json" style={{ display: 'none' }} onChange={(e) => void onFile(e.target.files?.[0])} data-testid="restore-file" />
          <button className="rk-btn" style={{ marginLeft: 'auto' }} onClick={() => file.current?.click()} data-testid="restore-pick">
            Choose a backup file…
          </button>
        </div>
        {pickErr && (
          <p className="tb-err" data-testid="restore-error">
            {pickErr}
          </p>
        )}
        {b && picked && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10, fontSize: 12.5 }} data-testid="restore-preview">
            <p style={{ margin: 0 }}>
              <span className="rk-mono">{picked.name}</span>: taken {fmtTs(b.taken)}
              {b.by ? ` by ${b.by}` : ''} from version {b.version}.
            </p>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              {RESTORE_PARTS.map((p) => {
                const n = partCount(b, p.id)
                return (
                  <label key={p.id} style={{ display: 'flex', alignItems: 'flex-start', gap: 8, color: n ? 'var(--fg)' : 'var(--fg3)' }}>
                    <input type="checkbox" checked={parts.includes(p.id)} disabled={n === 0} onChange={(e) => setParts((cur) => (e.target.checked ? [...cur, p.id] : cur.filter((x) => x !== p.id)))} data-testid={`restore-part-${p.id}`} style={{ marginTop: 2 }} />
                    <span>
                      <strong>{p.label}</strong> {n === 0 ? '— none in this backup' : p.id === 'settings' || p.id === 'sherlockSpend' ? '' : `(${n})`} <span style={{ color: 'var(--fg2)' }}>{p.how}</span>
                    </span>
                  </label>
                )
              })}
            </div>
            {b.connections && b.connections.filter((c) => !c.local).length > 0 && (
              <p style={{ margin: 0, color: 'var(--fg2)' }}>
                Connections in the backup, to add again on the Clusters page with their credentials: {b.connections.filter((c) => !c.local).map((c) => `${c.name} (${c.server})`).join(', ')}.
              </p>
            )}
            <div>
              <button className="rk-btn" data-on onClick={() => setConfirm(true)} disabled={parts.length === 0} data-testid="restore-start">
                Restore…
              </button>
            </div>
          </div>
        )}
        {report && (
          <div className="rk-callout" data-tone="info" style={{ fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="restore-report">
            <strong>Restored:</strong>
            <ul style={{ margin: 0, paddingLeft: 18 }}>
              {RESTORE_PARTS.filter((p) => report.restored[p.id] !== undefined).map((p) => (
                <li key={p.id}>
                  {p.label}: {report.restored[p.id]}
                </li>
              ))}
              {Object.keys(report.restored).length === 0 && <li>nothing</li>}
            </ul>
            {report.notes.length > 0 && (
              <>
                <strong>Left to do, or not done:</strong>
                <ul style={{ margin: 0, paddingLeft: 18 }}>
                  {report.notes.map((n, i) => (
                    <li key={i}>{n}</li>
                  ))}
                </ul>
              </>
            )}
          </div>
        )}
      </section>
      {confirm && picked && (
        <ConfirmDialog title={`Restore ${parts.length === 1 ? 'one part' : `${parts.length} parts`} from ${picked.name}?`} confirm="Restore" busy={busy} error={restoreErr} onCancel={() => setConfirm(false)} onConfirm={() => void restore()}>
          <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4 }}>
            {RESTORE_PARTS.filter((p) => parts.includes(p.id)).map((p) => (
              <li key={p.id}>
                <strong>{p.label}</strong> {p.how}.
              </li>
            ))}
          </ul>
          <p style={{ margin: '10px 0 0' }}>Every open console sees the settings at once. The restore is recorded in the audit log.</p>
        </ConfirmDialog>
      )}
    </>
  )
}
