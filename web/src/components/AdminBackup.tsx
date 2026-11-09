import { useEffect, useRef, useState } from 'react'
import { AdminError, backupSealing, fetchBackup, fetchBackupMeta, partCount, readBackup, RESTORE_PARTS, restoreBackup, restoreParts, SECRET_PLACEHOLDER, type BackupFile, type BackupKeyInfo, type PartInfo, type RestorePart, type RestoreReport } from '../api/admin'
import { fmtTs } from '../lib/format'
import { download } from '../lib/xlsx'
import type { AdminProps } from './AdminPage'
import { AdminBackupS3 } from './AdminBackupS3'
import { ConfirmDialog } from './ConfirmDialog'
import { DownloadIcon } from './Icons'

const HELD = ['the settings: refresh rate, time format, fleets, acknowledged alerts, Sherlock’s and Watson’s settings, grace period, banners', 'the local users made in the console: name, email, groups, super roles, password', 'the cluster connections, with their credentials', 'the Helm applications: source, values and parameters as typed, commands, what was deployed from where', 'the Toolbox’s session records', 'Sherlock’s spend ledger', 'the audit log the console keeps']

const LEFT_OUT = ['any secret in clear: passwords, tokens, kubeconfigs, client keys, OIDC secrets, API keys and the SSH authority’s key are each sealed with this install’s backup key', 'sign-in sessions', 'Watson’s cards and Sherlock’s conversations, which live in memory', 'the Toolbox’s session recordings, which are in the transcript store']

/**
 * Administration › Backup (asked October 30, 2026): one gzipped JSON file
 * with everything the console keeps of its own, each secret sealed with
 * the install's backup key (ADR-137, asked November 9, 2026; a placeholder
 * without one, asked November 4, 2026); and a restore of the parts picked
 * from such a file, which never replaces a secret the console has. A file
 * sealed with another key is restored without its secrets only once the
 * SuperAdmin says so, and the report lists each secret to set again.
 * Since Phase 12 the parts are what every package registers (ADR-80), and
 * backups go to S3 on a schedule (ADR-81), restored through the same
 * picking as a file.
 */
export function AdminBackup({ onToast }: AdminProps) {
  // Every part the server has, the other packages' too, for the labels.
  const [serverParts, setServerParts] = useState<PartInfo[]>([])
  const [key, setKey] = useState<BackupKeyInfo | null>(null)
  useEffect(() => {
    let live = true
    fetchBackupMeta()
      .then((m) => {
        if (!live) return
        setServerParts(m.parts)
        setKey(m.key)
      })
      .catch(() => undefined)
    return () => {
      live = false
    }
  }, [])
  const optional = serverParts.filter((p) => p.optional)
  const [withOptional, setWithOptional] = useState(false)
  const held = [...HELD, ...serverParts.filter((p) => !p.optional && !RESTORE_PARTS.some((x) => x.id === p.name) && p.name !== 'sherlockKey' && p.name !== 'sshAuthority').map((p) => p.label)]
  const [taking, setTaking] = useState(false)
  const [takeErr, setTakeErr] = useState('')
  const [last, setLast] = useState<BackupFile | null>(null)
  const take = async () => {
    setTaking(true)
    setTakeErr('')
    try {
      const { bytes, name, backup } = await fetchBackup(withOptional)
      download(bytes, name, 'application/gzip')
      setLast(backup)
      onToast('The backup is downloading.')
    } catch (e) {
      setTakeErr(e instanceof Error ? e.message : String(e))
    } finally {
      setTaking(false)
    }
  }

  const file = useRef<HTMLInputElement>(null)
  const [picked, setPicked] = useState<{ name: string; backup: BackupFile; source?: string } | null>(null)
  const restoreCard = useRef<HTMLElement>(null)
  const [pickErr, setPickErr] = useState('')
  const [parts, setParts] = useState<RestorePart[]>([])
  const [confirm, setConfirm] = useState(false)
  // The server's 409: the file's secrets are sealed with another key, which
  // the page did not know of (a key changed since it loaded).
  const [otherKey, setOtherKey] = useState(false)
  const [busy, setBusy] = useState(false)
  const [restoreErr, setRestoreErr] = useState('')
  const [report, setReport] = useState<RestoreReport | null>(null)
  const onFile = async (f: File | undefined) => {
    setReport(null)
    setPickErr('')
    setPicked(null)
    if (!f) return
    try {
      choose(f.name, await readBackup(f))
    } catch (e) {
      setPickErr(e instanceof Error ? e.message : String(e))
    }
  }
  // A file from the computer or a backup from the bucket: the same picking.
  const choose = (name: string, b: BackupFile, source?: string) => {
    setReport(null)
    setPickErr('')
    setOtherKey(false)
    setPicked({ name, backup: b, source })
    setParts(restoreParts(serverParts, b).filter((p) => partCount(b, p.id) > 0).map((p) => p.id))
    restoreCard.current?.scrollIntoView?.({ behavior: 'smooth', block: 'start' })
  }
  const offered = restoreParts(serverParts, picked?.backup)
  const sealing = picked ? (otherKey ? 'other' : backupSealing(picked.backup, key)) : 'placeholders'
  const restore = async () => {
    if (!picked) return
    setBusy(true)
    setRestoreErr('')
    try {
      const rep = await restoreBackup(picked.backup, parts, picked.source ?? picked.name, sealing === 'other')
      setReport(rep)
      setConfirm(false)
      onToast('Restored.')
    } catch (e) {
      if (e instanceof AdminError && e.status === 409 && sealing !== 'other') {
        // Asked now, in the same dialog.
        setOtherKey(true)
      } else {
        setRestoreErr(e instanceof Error ? e.message : String(e))
      }
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
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>One gzipped JSON file with everything the console keeps of its own, each secret sealed with this install’s backup key. Keep it where you keep secrets anyway: it names people, clusters and releases.</span>
          <button className="rk-btn" data-on style={{ marginLeft: 'auto' }} onClick={() => void take()} disabled={taking} data-testid="backup-take">
            <DownloadIcon />
            {taking ? 'Taking…' : 'Download backup'}
          </button>
        </div>
        {optional.length > 0 && (
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12.5 }}>
            <input type="checkbox" checked={withOptional} onChange={(e) => setWithOptional(e.target.checked)} data-testid="backup-optional" />
            Include {optional.map((p) => p.label.toLowerCase()).join(' and ')} (off by default)
          </label>
        )}
        {takeErr && <p className="tb-err">{takeErr}</p>}
        {key && (
          <p className="tb-hint" style={{ margin: 0 }} data-testid="backup-key">
            {key.fingerprint ? (
              <>
                Secrets are sealed with this install’s backup key, fingerprint <span className="rk-mono">{key.fingerprint.slice(0, 16)}</span>, kept in the Kubernetes Secret <span className="rk-mono">{key.secret}</span>. The console never shows the key: keep a copy from that Secret where you keep secrets. Without it a backup’s secrets cannot be recovered.
              </>
            ) : key.error ? (
              <span className="tb-err">The backup key cannot be read, so no backup can be taken: {key.error}</span>
            ) : (
              <>This install keeps no backup key (its connections are kept in memory): each secret in a backup is “{SECRET_PLACEHOLDER}”.</>
            )}
          </p>
        )}
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))', gap: 16, fontSize: 12.5 }}>
          <div>
            <h3 className="rk-h" style={{ margin: '0 0 6px' }}>In the backup</h3>
            <ul style={{ margin: 0, paddingLeft: 18, color: 'var(--fg2)', display: 'flex', flexDirection: 'column', gap: 3 }}>
              {held.map((x) => (
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
      <AdminBackupS3 onToast={onToast} onRestore={choose} />
      <section ref={restoreCard} className="rk-card" style={{ padding: '16px 18px', display: 'flex', flexDirection: 'column', gap: 12 }} aria-label="Restore from a backup" data-testid="admin-restore">
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 12 }}>
          <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>Restore from a backup</h2>
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>Pick a backup file, choose what to bring back, and read what is left to do by hand. A secret this console has is never replaced by a backup’s placeholder.</span>
          <input ref={file} type="file" accept="application/gzip,.gz,application/json,.json" style={{ display: 'none' }} onChange={(e) => {
              const f = e.target.files?.[0]
              // Emptied, so the same file chosen again is a change too.
              e.target.value = ''
              void onFile(f)
            }} data-testid="restore-file" />
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
            <p style={{ margin: 0, color: sealing === 'other' ? 'var(--warn)' : 'var(--fg2)' }} data-testid="restore-sealing" data-sealing={sealing}>
              {sealing === 'same' && 'Its secrets are sealed with this install’s backup key: they come back where this console has none.'}
              {sealing === 'other' && (b.secrets?.key ? `Its secrets are sealed with another backup key (fingerprint ${b.secrets.key.slice(0, 16)}), which this install does not have: they cannot be restored. You can restore the rest, and set each secret again.` : 'Its secrets are sealed with another backup key: they cannot be restored here.')}
              {sealing === 'placeholders' && 'It holds a placeholder for each secret: each is listed afterwards, to set again.'}
            </p>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              {offered.map((p) => {
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
              {offered.filter((p) => report.restored[p.id] !== undefined).map((p) => (
                <li key={p.id}>
                  {p.label}: {report.restored[p.id]}
                </li>
              ))}
              {Object.keys(report.restored).length === 0 && <li>nothing</li>}
            </ul>
            {(report.secrets?.length ?? 0) > 0 && (
              <>
                <strong>Secrets to set again:</strong>
                <ul style={{ margin: 0, paddingLeft: 18 }} data-testid="restore-secrets">
                  {report.secrets?.map((x, i) => (
                    <li key={i} data-testid="restore-secret">
                      {x.name} <span style={{ color: 'var(--fg2)' }}>({x.part})</span>: set it in <strong>{x.where}</strong>
                    </li>
                  ))}
                </ul>
              </>
            )}
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
        <ConfirmDialog
          title={`Restore ${parts.length === 1 ? 'one part' : `${parts.length} parts`} from ${picked.name}?`}
          confirm={sealing === 'other' ? 'Restore without the secrets' : 'Restore'}
          busy={busy}
          error={restoreErr}
          onCancel={() => {
            setConfirm(false)
            setOtherKey(false)
          }}
          onConfirm={() => void restore()}
        >
          {sealing === 'other' && (
            <p className="rk-callout" data-tone="warn" style={{ margin: '0 0 10px' }} data-testid="restore-without-secrets">
              This backup’s secrets are sealed with another backup key than this install’s, so they cannot be restored. Restore the rest without the secrets? Afterwards each secret is listed with where to set it.
            </p>
          )}
          <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4 }}>
            {offered.filter((p) => parts.includes(p.id)).map((p) => (
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
