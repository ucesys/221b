import { useEffect, useState } from 'react'
import {
  describeSchedule,
  fetchBackupFromS3,
  fetchBackupS3,
  listBackupsS3,
  runBackupS3,
  saveBackupS3,
  testBackupS3,
  type BackupFile,
  type S3Backup,
  type S3Keys,
  type S3Run,
  type S3Settings,
  type S3Step,
  type S3View,
  type ScheduleSpec,
} from '../api/admin'
import { fMem, fmtTs } from '../lib/format'
import type { AdminProps } from './AdminPage'
import { Select } from './Select'

const KINDS: [ScheduleSpec['kind'], string][] = [
  ['daily', 'Every day'],
  ['weekdays', 'Weekdays (Monday to Friday)'],
  ['weekly', 'Weekly, on some days'],
  ['hours', 'Every N hours'],
  ['cron', 'Cron expression'],
]

const DAYS: [string, string][] = [
  ['mon', 'Mon'],
  ['tue', 'Tue'],
  ['wed', 'Wed'],
  ['thu', 'Thu'],
  ['fri', 'Fri'],
  ['sat', 'Sat'],
  ['sun', 'Sun'],
]

/** The time zones the browser knows, for the schedule's list. */
function zones(): string[] {
  try {
    return (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.('timeZone') ?? []
  } catch {
    return []
  }
}

/** One row of the card: what it is on the left, its fields on the right. */
function Row({ label, hint, children, testId }: { label: string; hint?: string; children: React.ReactNode; testId?: string }) {
  return (
    <div className="rk-s3row" style={{ display: 'grid', gridTemplateColumns: 'minmax(120px, 150px) 1fr', gap: 14, padding: '12px 0', borderTop: '1px solid var(--line)' }} data-testid={testId}>
      <div style={{ fontSize: 12.5, fontWeight: 600 }}>
        {label}
        {hint && <div style={{ fontWeight: 400, color: 'var(--fg3)', marginTop: 3 }}>{hint}</div>}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8, minWidth: 0, fontSize: 12.5 }}>{children}</div>
    </div>
  )
}

function Labeled({ label, children, width }: { label: string; children: React.ReactNode; width?: number | string }) {
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)', width: width ?? 'auto', minWidth: 0 }}>
      {label}
      {children}
    </label>
  )
}

/** A secret the Secret holds: whether it is set, and a Replace… that opens fields for a new one. Its value never comes back. */
function SecretState({ set, what, open, onOpen, children, testId }: { set: boolean; what: string; open: boolean; onOpen: () => void; children: React.ReactNode; testId: string }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <span className="rk-pill" data-tone={set ? 'ok' : 'warn'} data-testid={`${testId}-state`}>
          {set ? `${what} saved` : `No ${what}`}
        </span>
        <span style={{ color: 'var(--fg3)' }}>Kept in the Secret &lt;release&gt;-backup; never shown here.</span>
        {!open && (
          <button type="button" className="rk-btn" onClick={onOpen} data-testid={`${testId}-replace`}>
            {set ? 'Replace…' : 'Set…'}
          </button>
        )}
      </div>
      {open && children}
    </div>
  )
}

/** What a run says, in a line. */
export function runLine(r: S3Run): string {
  if (r.missed) return `${fmtTs(r.slot ?? r.started)}: ${r.error ?? 'missed'}`
  const by = r.by === 'scheduled backup' ? 'on schedule' : `by ${r.by}`
  if (!r.ok) return `${fmtTs(r.started)}, ${by}: failed — ${r.error ?? ''}`
  return `${fmtTs(r.started)}, ${by}: ${r.key ?? ''}, ${fMem(r.size ?? 0)}${r.pruned ? `, ${r.pruned} old removed` : ''}`
}

/**
 * Administration › Backup › Scheduled backups to S3 (Phase 12, ADR-81):
 * where, with which credentials, when, how long to keep them, how they are
 * encrypted, how the last runs went, and the backups in the bucket to
 * restore from, through the same picking as a file.
 */
export function AdminBackupS3({ onToast, onRestore }: Pick<AdminProps, 'onToast'> & { onRestore: (name: string, backup: BackupFile, source: string) => void }) {
  const [view, setView] = useState<S3View | null>(null)
  const [loadErr, setLoadErr] = useState('')
  const [draft, setDraft] = useState<S3Settings | null>(null)
  const [keys, setKeys] = useState<S3Keys>({})
  const [replaceKey, setReplaceKey] = useState(false)
  const [replacePass, setReplacePass] = useState(false)
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [steps, setSteps] = useState<S3Step[] | null>(null)
  const [words, setWords] = useState<{ description?: string; next?: string[]; error?: string }>({})
  const [list, setList] = useState<S3Backup[] | null>(null)
  const [listErr, setListErr] = useState('')

  const load = async () => {
    try {
      const v = await fetchBackupS3()
      setView(v)
      setDraft(v.settings)
      setLoadErr('')
    } catch (e) {
      setLoadErr(e instanceof Error ? e.message : String(e))
    }
  }
  useEffect(() => {
    let live = true
    fetchBackupS3()
      .then((v) => {
        if (!live) return
        setView(v)
        setDraft(v.settings)
      })
      .catch((e: unknown) => live && setLoadErr(e instanceof Error ? e.message : String(e)))
    return () => {
      live = false
    }
  }, [])
  // The schedule in words and its next times, as it is edited.
  const spec = draft?.schedule
  const specKey = JSON.stringify(spec ?? null)
  useEffect(() => {
    if (!spec) return
    let live = true
    const t = setTimeout(() => {
      describeSchedule(spec)
        .then((w) => live && setWords(w))
        .catch(() => live && setWords({}))
    }, 250)
    return () => {
      live = false
      clearTimeout(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [specKey])

  if (loadErr && !view) {
    return (
      <section className="rk-card" style={{ padding: '16px 18px' }} aria-label="Scheduled backups to S3" data-testid="admin-backup-s3">
        <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>Scheduled backups to S3</h2>
        <p className="tb-err">{loadErr}</p>
      </section>
    )
  }
  if (!view || !draft) return null
  const set = (p: Partial<S3Settings>) => setDraft({ ...draft, ...p })
  const setSched = (p: Partial<ScheduleSpec>) => setDraft({ ...draft, schedule: { ...draft.schedule, ...p } })
  const sentKeys = (): S3Keys => {
    const k: S3Keys = {}
    if (replaceKey) {
      k.accessKeyId = keys.accessKeyId ?? ''
      k.secretAccessKey = keys.secretAccessKey ?? ''
    }
    if (replacePass) k.passphrase = keys.passphrase ?? ''
    return k
  }
  const act = async (what: string, f: () => Promise<void>) => {
    setBusy(what)
    setErr('')
    try {
      await f()
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }
  const save = () =>
    act('save', async () => {
      const v = await saveBackupS3(draft, sentKeys())
      setView(v)
      setDraft(v.settings)
      setKeys({})
      setReplaceKey(false)
      setReplacePass(false)
      onToast('The backups to S3 are saved.')
    })
  const test = () =>
    act('test', async () => {
      setSteps(null)
      setSteps(await testBackupS3(draft, sentKeys()))
    })
  const runNow = () =>
    act('run', async () => {
      const r = await runBackupS3()
      onToast(r.ok ? `The backup is in the bucket: ${r.key ?? ''}.` : 'The backup failed; the status says why.')
      await load()
    })
  const showList = () =>
    act('list', async () => {
      setListErr('')
      try {
        setList(await listBackupsS3())
      } catch (e) {
        setListErr(e instanceof Error ? e.message : String(e))
      }
    })
  const pick = (b: S3Backup) =>
    act('pick:' + b.key, async () => {
      const file = await fetchBackupFromS3(b.key)
      onRestore(b.key.split('/').pop() ?? b.key, file, `s3://${view.settings.bucket}/${b.key}`)
    })

  const sc = draft.schedule
  const r = draft.retention
  const recent = view.status.recent ?? []
  const last = recent.find((x) => !x.missed) ?? recent[0]
  const tz = zones()
  return (
    <section className="rk-card" style={{ padding: '16px 18px', display: 'flex', flexDirection: 'column', gap: 4 }} aria-label="Scheduled backups to S3" data-testid="admin-backup-s3">
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 12, paddingBottom: 8 }}>
        <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>Scheduled backups to S3</h2>
        <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>The same file as the download, put in a bucket on a schedule by the leading replica, each secret a placeholder. Amazon S3 or any S3-compatible store.</span>
        <label style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 6, fontSize: 12.5, fontWeight: 600 }}>
          <input type="checkbox" checked={draft.enabled} onChange={(e) => set({ enabled: e.target.checked })} data-testid="s3-enabled" />
          On schedule
        </label>
      </div>

      <Row label="Destination" hint="The bucket and where in it." testId="s3-destination">
        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
          <Labeled label="Bucket" width={200}>
            <span className="rk-field">
              <input spellCheck={false} value={draft.bucket} placeholder="my-backups" onChange={(e) => set({ bucket: e.target.value })} data-testid="s3-bucket" />
            </span>
          </Labeled>
          <Labeled label="Prefix" width={160}>
            <span className="rk-field">
              <input spellCheck={false} value={draft.prefix ?? ''} placeholder="221b/" onChange={(e) => set({ prefix: e.target.value })} data-testid="s3-prefix" />
            </span>
          </Labeled>
          <Labeled label="Region" width={140}>
            <span className="rk-field">
              <input spellCheck={false} value={draft.region ?? ''} placeholder="us-east-1" onChange={(e) => set({ region: e.target.value })} data-testid="s3-region" />
            </span>
          </Labeled>
          <Labeled label="Endpoint (S3-compatible stores)" width={260}>
            <span className="rk-field">
              <input type="url" spellCheck={false} value={draft.endpoint ?? ''} placeholder="Empty is Amazon S3; https://minio.example:9000" onChange={(e) => set({ endpoint: e.target.value })} data-testid="s3-endpoint" />
            </span>
          </Labeled>
        </div>
        <label style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <input type="checkbox" checked={!!draft.pathStyle} onChange={(e) => set({ pathStyle: e.target.checked })} data-testid="s3-path-style" />
          Path-style addresses (host/bucket/key), as MinIO and most S3-compatible stores want
        </label>
        <details open={!!draft.caPem}>
          <summary style={{ cursor: 'pointer' }}>Private CA</summary>
          <textarea
            rows={3}
            spellCheck={false}
            className="rk-area"
            style={{ width: '100%', marginTop: 6 }}
            value={draft.caPem ?? ''}
            placeholder="-----BEGIN CERTIFICATE-----, for an endpoint whose certificate a private authority signed"
            onChange={(e) => set({ caPem: e.target.value })}
            data-testid="s3-ca"
          />
        </details>
      </Row>

      <Row label="Credentials" testId="s3-credentials">
        <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="radio" name="s3-cred" checked={draft.credentials === 'keys'} onChange={() => set({ credentials: 'keys' })} data-testid="s3-cred-keys" />
            An access key
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="radio" name="s3-cred" checked={draft.credentials === 'identity'} onChange={() => set({ credentials: 'identity' })} data-testid="s3-cred-identity" />
            The pod’s cloud identity (IRSA, EKS Pod Identity)
          </label>
        </div>
        {draft.credentials === 'keys' ? (
          <SecretState set={view.keys.accessKey} what="access key" open={replaceKey} onOpen={() => setReplaceKey(true)} testId="s3-key">
            <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
              <Labeled label="Access key ID" width={220}>
                <span className="rk-field">
                  <input spellCheck={false} autoComplete="off" value={keys.accessKeyId ?? ''} onChange={(e) => setKeys({ ...keys, accessKeyId: e.target.value })} data-testid="s3-key-id" />
                </span>
              </Labeled>
              <Labeled label="Secret access key" width={300}>
                <span className="rk-field">
                  <input type="password" autoComplete="new-password" value={keys.secretAccessKey ?? ''} onChange={(e) => setKeys({ ...keys, secretAccessKey: e.target.value })} data-testid="s3-key-secret" />
                </span>
              </Labeled>
            </div>
          </SecretState>
        ) : (
          <span className="rk-pill" data-tone={view.identity ? 'ok' : 'warn'} style={{ alignSelf: 'flex-start' }} data-testid="s3-identity">
            {view.identity ? 'The pod has an AWS identity' : 'The pod has no AWS identity: annotate the ServiceAccount for IRSA, or use EKS Pod Identity'}
          </span>
        )}
      </Row>

      <Row label="Schedule" hint="Runs on the leading replica. A time missed while the console was down runs once within the hour, else waits." testId="s3-schedule">
        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <Labeled label="Repeat" width={220}>
            <Select
              label="Repeat"
              value={sc.kind}
              options={KINDS}
              onChange={(v) => setSched({ kind: v as ScheduleSpec['kind'], at: sc.at || '03:00', every: sc.every || 6, days: sc.days?.length ? sc.days : ['mon'] })}
              testId="s3-sched-kind"
              width="100%"
            />
          </Labeled>
          {sc.kind === 'hours' && (
            <Labeled label="Every (hours)" width={110}>
              <span className="rk-field">
                <input type="number" min={1} max={23} value={sc.every ?? 6} onChange={(e) => setSched({ every: Number(e.target.value) || 1 })} data-testid="s3-sched-every" />
              </span>
            </Labeled>
          )}
          {sc.kind !== 'cron' ? (
            <Labeled label={sc.kind === 'hours' ? 'From' : 'At'} width={110}>
              <span className="rk-field">
                <input type="time" value={sc.at ?? '03:00'} onChange={(e) => setSched({ at: e.target.value })} data-testid="s3-sched-at" />
              </span>
            </Labeled>
          ) : (
            <Labeled label="Cron (minute hour day month weekday)" width={260}>
              <span className="rk-field">
                <input spellCheck={false} className="rk-mono" value={sc.cron ?? ''} placeholder="0 3 * * *" onChange={(e) => setSched({ cron: e.target.value })} data-testid="s3-sched-cron" />
              </span>
            </Labeled>
          )}
          <Labeled label="Time zone" width={200}>
            <span className="rk-field">
              <input spellCheck={false} list="s3-zones" value={sc.timeZone ?? ''} placeholder="UTC" onChange={(e) => setSched({ timeZone: e.target.value })} data-testid="s3-sched-tz" />
            </span>
            <datalist id="s3-zones">
              {tz.map((z) => (
                <option key={z} value={z} />
              ))}
            </datalist>
          </Labeled>
        </div>
        {sc.kind === 'weekly' && (
          <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }} role="group" aria-label="Days">
            {DAYS.map(([d, l]) => (
              <label key={d} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                <input type="checkbox" checked={!!sc.days?.includes(d)} onChange={(e) => setSched({ days: e.target.checked ? [...(sc.days ?? []), d] : (sc.days ?? []).filter((x) => x !== d) })} data-testid={`s3-day-${d}`} />
                {l}
              </label>
            ))}
          </div>
        )}
        {words.error ? (
          <p className="tb-err" style={{ margin: 0 }} data-testid="s3-sched-error">
            {words.error}
          </p>
        ) : (
          words.description && (
            <p className="tb-hint" style={{ margin: 0 }} data-testid="s3-sched-words">
              {words.description}
              {words.next?.length ? `. Next: ${words.next.map((t) => fmtTs(t)).join(', ')}` : ''}
            </p>
          )
        )}
      </Row>

      <Row label="Retention" hint="After each backup, older ones under the prefix go; other objects there are left alone." testId="s3-retention">
        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <Labeled label="Keep" width={260}>
            <Select
              label="Keep"
              value={r.mode}
              options={[
                ['count', 'The newest backups, this many'],
                ['days', 'Backups this many days old'],
                ['lifecycle', 'Everything: the bucket’s lifecycle rules decide'],
              ]}
              onChange={(v) => set({ retention: { mode: v as S3Settings['retention']['mode'], count: r.count || 14, days: r.days || 30 } })}
              testId="s3-retention-mode"
              width="100%"
            />
          </Labeled>
          {r.mode !== 'lifecycle' && (
            <Labeled label={r.mode === 'count' ? 'Backups' : 'Days'} width={110}>
              <span className="rk-field">
                <input type="number" min={1} value={(r.mode === 'count' ? r.count : r.days) ?? 1} onChange={(e) => set({ retention: { ...r, [r.mode]: Number(e.target.value) || 1 } })} data-testid="s3-retention-n" />
              </span>
            </Labeled>
          )}
        </div>
      </Row>

      <Row label="Encryption" testId="s3-encryption">
        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <Labeled label="In the bucket (server-side)" width={260}>
            <Select
              label="Server-side encryption"
              value={draft.sse ?? ''}
              options={[
                ['', 'The bucket’s default'],
                ['AES256', 'SSE-S3 (keys S3 manages)'],
                ['aws:kms', 'SSE-KMS (a KMS key)'],
              ]}
              onChange={(v) => set({ sse: v as S3Settings['sse'] })}
              testId="s3-sse"
              width="100%"
            />
          </Labeled>
          {draft.sse === 'aws:kms' && (
            <Labeled label="KMS key (empty: the account’s default)" width={300}>
              <span className="rk-field">
                <input spellCheck={false} value={draft.kmsKeyId ?? ''} placeholder="alias/backups or a key ARN" onChange={(e) => set({ kmsKeyId: e.target.value })} data-testid="s3-kms" />
              </span>
            </Labeled>
          )}
        </div>
        <label style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <input type="checkbox" checked={!!draft.encrypt} onChange={(e) => set({ encrypt: e.target.checked })} data-testid="s3-encrypt" />
          Also encrypt each file on the console (AES-256-GCM, with a key from a passphrase you hold); the file ends in .enc
        </label>
        {(draft.encrypt || view.keys.passphrase) && (
          <SecretState set={view.keys.passphrase} what="passphrase" open={replacePass} onOpen={() => setReplacePass(true)} testId="s3-pass">
            <Labeled label="Passphrase (12 characters or more; keep a copy: without it no file opens)" width={420}>
              <span className="rk-field">
                <input type="password" autoComplete="new-password" value={keys.passphrase ?? ''} onChange={(e) => setKeys({ ...keys, passphrase: e.target.value })} data-testid="s3-passphrase" />
              </span>
            </Labeled>
          </SecretState>
        )}
      </Row>

      <Row label="Status" testId="s3-status">
        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'center' }}>
          {view.running && (
            <span className="rk-pill" data-tone="info">
              A backup is running
            </span>
          )}
          {last ? (
            <span className="rk-pill" data-tone={last.ok ? 'ok' : 'err'} data-testid="s3-last">
              {last.ok ? 'Last backup worked' : last.missed ? 'Last time was missed' : 'Last backup failed'}
            </span>
          ) : (
            <span className="rk-pill" data-testid="s3-last">
              No backup yet
            </span>
          )}
          <span style={{ color: 'var(--fg2)' }} data-testid="s3-next">
            {view.saved && view.settings.enabled ? `${view.schedule}. Next: ${view.next ? fmtTs(view.next) : '—'}` : 'Not on a schedule.'}
          </span>
        </div>
        {last && !last.ok && (
          <p className="tb-err" style={{ margin: 0 }} data-testid="s3-failure">
            {runLine(last)}
          </p>
        )}
        {recent.length > 0 && (
          <ul style={{ margin: 0, paddingLeft: 18, color: 'var(--fg2)', display: 'flex', flexDirection: 'column', gap: 2 }} data-testid="s3-recent">
            {recent.slice(0, 5).map((x, i) => (
              <li key={i}>{runLine(x)}</li>
            ))}
          </ul>
        )}
      </Row>

      <Row label="Restore" hint="From a backup in the bucket, with the same choice of parts as a file." testId="s3-restore">
        <div>
          <button type="button" className="rk-btn" onClick={() => void showList()} disabled={!view.saved || !!busy} data-testid="s3-list">
            {busy === 'list' ? 'Listing…' : list ? 'List again' : 'List the backups in the bucket'}
          </button>
        </div>
        {listErr && <p className="tb-err">{listErr}</p>}
        {list && list.length === 0 && (
          <p className="tb-hint" style={{ margin: 0 }}>
            No backup under this prefix yet.
          </p>
        )}
        {list && list.length > 0 && (
          <table className="rk-table" style={{ fontSize: 12.5, borderCollapse: 'collapse' }} data-testid="s3-backups">
            <thead>
              <tr>
                <th style={{ textAlign: 'left', padding: '4px 8px' }}>Taken</th>
                <th style={{ textAlign: 'left', padding: '4px 8px' }}>File</th>
                <th style={{ textAlign: 'right', padding: '4px 8px' }}>Size</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.map((b) => (
                <tr key={b.key} style={{ borderTop: '1px solid var(--line)' }}>
                  <td style={{ padding: '4px 8px' }}>{fmtTs(b.taken)}</td>
                  <td style={{ padding: '4px 8px' }} className="rk-mono">
                    {b.key}
                    {b.encrypted ? ' (encrypted)' : ''}
                  </td>
                  <td style={{ padding: '4px 8px', textAlign: 'right' }}>{fMem(b.size)}</td>
                  <td style={{ padding: '4px 8px' }}>
                    <button type="button" className="rk-btn" onClick={() => void pick(b)} disabled={!!busy} data-testid="s3-restore-pick">
                      {busy === 'pick:' + b.key ? 'Reading…' : 'Restore…'}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Row>

      {err && (
        <p className="tb-err" data-testid="s3-error">
          {err}
        </p>
      )}
      {steps && (
        <ul style={{ margin: 0, paddingLeft: 0, listStyle: 'none', fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 3 }} data-testid="s3-steps">
          {steps.map((s) => (
            <li key={s.name}>
              <span className="rk-t" data-tone={s.ok ? 'ok' : 'err'}>
                {s.ok ? '✓' : '✗'} {s.name}
              </span>
              {s.detail && <span style={{ color: 'var(--fg2)' }}> — {s.detail}</span>}
            </li>
          ))}
        </ul>
      )}
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', paddingTop: 8, borderTop: '1px solid var(--line)' }}>
        <button type="button" className="rk-btn" data-on onClick={() => void save()} disabled={!!busy} data-testid="s3-save">
          {busy === 'save' ? 'Saving…' : 'Save'}
        </button>
        <button type="button" className="rk-btn" onClick={() => void test()} disabled={!!busy} data-testid="s3-test">
          {busy === 'test' ? 'Testing…' : 'Test connection'}
        </button>
        <button type="button" className="rk-btn" onClick={() => void runNow()} disabled={!!busy || !view.saved || view.running} data-testid="s3-run">
          {busy === 'run' ? 'Backing up…' : 'Run now'}
        </button>
      </div>
    </section>
  )
}
