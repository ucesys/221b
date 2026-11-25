import { useEffect, useRef, useState, type ReactNode } from 'react'
import {
  addSource,
  fetchKnowledge,
  forgetSourceCert,
  forgetStoreCert,
  LIVE_ADDRESS_LABEL,
  liveCheckText,
  moveStore,
  removeSource,
  saveHookToken,
  saveSource,
  saveStaleDays,
  saveStore,
  setSourceOn,
  SOURCE_KIND_LABEL,
  sourceMode,
  sourceTestText,
  syncJobId,
  testStore,
  testTypedSource,
  trustSourceCert,
  trustStoreCert,
  type KeysChange,
  type KnowledgeSource,
  type LiveAddress,
  type KnowledgeView,
  DATABASE_STORE_LABEL,
  SOURCES_HREF,
  type SourceTestResult,
  type StoreCheck,
  type StoreSettings,
} from '../../api/knowledge'
import type { PinnedWith, PresentedCert } from '../../api/certificates'
import { fmtTs } from '../../lib/format'
import { CertificateResult, PinnedCertNote } from '../CertificateTrust'
import { aiHref } from '../../lib/router'
import { jobHref, scheduleHref } from '../aiops/jobsRoute'
import { CredentialPicker } from '../CredentialPicker'
import { GitRepoPicker } from '../apps/GitRepoPicker'
import { fetchCredentials, type CredentialUse } from '../../api/credentials'
import { Drawer } from '../Drawer'
import { Select } from '../Select'
import { UploadedDocuments } from './UploadedDocuments'
import { SearchCard, SourceIndexCell } from './KnowledgeSearch'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** One row of a card: what it is on the left, its fields on the right. */
function Row({ label, hint, children, testId }: { label: string; hint?: string; children: ReactNode; testId?: string }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(120px, 160px) 1fr', gap: 14, padding: '12px 0', borderTop: '1px solid var(--line)' }} data-testid={testId}>
      <div style={{ fontSize: 12.5, fontWeight: 600 }}>
        {label}
        {hint && <div style={{ fontWeight: 400, color: 'var(--fg3)', marginTop: 3 }}>{hint}</div>}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8, minWidth: 0, fontSize: 12.5 }}>{children}</div>
    </div>
  )
}

function Text({ label, value, onChange, placeholder, testId, type, width }: { label: string; value: string; onChange: (v: string) => void; placeholder?: string; testId?: string; type?: string; width?: number | string }) {
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)', width: width ?? 'auto', minWidth: 0 }}>
      {label}
      <span className="rk-field">
        <input type={type ?? 'text'} autoComplete="off" spellCheck={false} value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} data-testid={testId} />
      </span>
    </label>
  )
}

/** Where a source was last read, and how; title says more when there is more. */
function sourceState(v: KnowledgeView, s: KnowledgeSource): { text: string; tone: string; title?: string } {
  if (s.off) return { text: 'Off', tone: 'muted' }
  // A live source is never read into the wiki: looked up when asked (KN-39).
  if (s.kind === 'live') return !s.credMigrated && s.auth && !v.creds[s.id]?.token ? { text: 'No token', tone: 'warn' } : { text: 'Looked up when asked', tone: 'ok' }
  const st = v.status.sources?.[s.id]
  if (st?.error) return { text: st.error, tone: 'err' }
  // A source names its credential (0.12.1, CR-11); one of before keeps its own until the upgrade moves it.
  const own = !s.credMigrated && !s.credential
  if (s.kind === 'web' && own && s.auth && !v.creds[s.id]?.token) return { text: 'No token', tone: 'warn' }
  if (s.kind === 'confluence' && (own ? !v.creds[s.id]?.token : !s.credential)) return { text: 'No credential', tone: 'warn' }
  if (s.kind === 's3' && s.credentials !== 'identity' && (own ? !v.creds[s.id]?.accessKey : !s.credential)) return { text: 'No access key', tone: 'warn' }
  if (st?.reported) return { text: `${st.reported} ${st.reported === 1 ? 'page' : 'pages'} not followed`, tone: 'warn' }
  if (!st?.lastRead) return { text: 'Not read yet', tone: 'muted' }
  // A bucket's objects of other kinds, or too large, are skipped (KN-25).
  if (st.skipped) return { text: `Read · ${st.items} ${st.items === 1 ? 'item' : 'items'} · ${st.skipped} skipped`, tone: 'ok', title: `${st.skipped} skipped: ${st.skippedWhy ?? ''}` }
  return { text: `Read · ${st.items} ${st.items === 1 ? 'item' : 'items'}`, tone: 'ok' }
}

/** A bucket source's credentials, as its row says them (KN-25, CR-11). */
function bucketCreds(v: KnowledgeView, s: KnowledgeSource, names: Record<string, string>): string {
  if (s.credentials === 'identity') return 'the pod’s identity'
  return credLine(v, s, names)
}

/** A source's credential, as its row says it: by name (CR-11), or the one kept before 0.12.1. */
function credLine(v: KnowledgeView, s: KnowledgeSource, names: Record<string, string>): string {
  if (s.credential) return `credential ${names[s.credential] ?? s.credential}`
  if (!s.credMigrated && (v.creds[s.id]?.token || v.creds[s.id]?.accessKey)) return 'its own credential, kept before 0.12.1'
  return 'no credential'
}

/**
 * Administration › AI › Knowledge (Phase 12, KN-01 to KN-12): Mycroft's sources
 * with how each was last read, and where its wiki is kept. Each source is
 * read by its own Wiki sync job in AIOps › Jobs (KN-28), which starts,
 * schedules and reports every sync: nothing here starts one. Sources are
 * data, never instructions; every credential is kept in the Secret
 * <release>-knowledge and shows here only as set or not. A source is tested in its dialog, as
 * typed (KN-26). The Sources table holds only what is synced or looked up
 * (0.12.1): a web site is a source of its own (a start address, how many
 * links away and how many pages), and what people upload or note is not a
 * source to sync again but the Uploaded documents section after the table
 * (UploadedDocuments.tsx); focus 'added' or 'uploads' scrolls to it. What
 * a source holds, and what is excluded from it, is AIOps › Mycroft ›
 * Sources.
 */
export function KnowledgeSection({ onToast, focus }: { onToast?: (msg: string) => void; focus?: string } = {}) {
  const [v, setV] = useState<KnowledgeView | null>(null)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState<KnowledgeSource | 'new' | null>(null)
  const [busy, setBusy] = useState(false)
  // The credentials' names, for the rows (CR-11).
  const [credNames, setCredNames] = useState<Record<string, string>>({})
  useEffect(() => {
    fetchCredentials().then(
      (c) => setCredNames(Object.fromEntries((c.credentials ?? []).map((x) => [x.id, x.name]))),
      () => undefined,
    )
  }, [v])
  const reload = () =>
    fetchKnowledge()
      .then((x) => {
        setV(x)
        setError('')
      })
      .catch((e: unknown) => setError(errorText(e)))
  useEffect(() => {
    void reload()
  }, [])
  // While a source's job reads it, look again every few seconds.
  useEffect(() => {
    if (!v?.status.running) return
    const t = setInterval(() => void reload(), 3000)
    return () => clearInterval(t)
  }, [v?.status.running])
  const run = async (fn: () => Promise<unknown>, msg?: string) => {
    setBusy(true)
    setError('')
    try {
      await fn()
      if (msg) onToast?.(msg)
      await reload()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  if (!v)
    return (
      <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>
        {error || 'Loading Mycroft’s sources…'}
      </p>
    )
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 16 }} aria-label="Knowledge" data-testid="knowledge-section">
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 12, flexWrap: 'wrap' }}>
        <p className="rk-fhint" style={{ margin: 0, flex: '1 1 320px' }}>
          Mycroft reads these sources into a wiki every agent can ask; its page changes wait for review in the Inbox.
        </p>
        <button className="rk-btn" onClick={() => setEditing('new')} data-testid="knowledge-add">
          Add source…
        </button>
      </div>
      <p className="rk-fhint" style={{ margin: 0 }} data-testid="knowledge-jobs-line">
        Each source is read by its Wiki sync job in{' '}
        <a className="rk-link" href={scheduleHref} data-testid="knowledge-jobs-link">
          AIOps › Jobs
        </a>
        .
      </p>
      {!v.settings.store.kind && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="knowledge-no-store">
          Choose where the wiki is kept, below, before Mycroft reads the sources.
        </p>
      )}
      {v.settings.store.kind === 'database' && (
        <p className="rk-callout" style={{ margin: 0, fontSize: 12.5 }} data-testid="knowledge-database-store">
          The wiki is kept in the console’s database, with its other records, up to 256 MiB. Choose Git or S3 below to keep every page’s history there instead.
        </p>
      )}
      {!v.model && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }}>
          Mycroft has no model here: sources are read and their excerpts kept, but no pages are written.
        </p>
      )}
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="knowledge-error">
          {error}
        </p>
      )}
      <section aria-label="Sources" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h3 className="rk-h">Sources</h3>
        <div style={{ overflowX: 'auto' }}>
          <table className="tb-table" style={{ width: '100%' }} data-testid="knowledge-sources">
            <thead>
              <tr>
                <th scope="col">Source</th>
                <th scope="col">Kind</th>
                <th scope="col">Mode</th>
                <th
                  scope="col"
                  title="Ranks its passages in the search: high above normal above low; Mycroft writes pages from a source of high trust by itself. Under it, the product and version its documents are about, where their front matter or path says nothing"
                >
                  Trust
                </th>
                <th scope="col">Last read</th>
                <th scope="col">State</th>
                <th scope="col">
                  <span className="rk-sr">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {v.settings.sources
                .filter((s) => s.kind !== 'upload')
                .map((s) => {
                  const st = sourceState(v, s)
                  const lastRead = v.status.sources?.[s.id]?.lastRead
                  const ix = v.index?.sources.find((x) => x.id === s.id)
                  return (
                    <tr key={s.id} data-testid="knowledge-source" data-source={s.id}>
                      <td>
                        <strong>{s.name}</strong>
                        {s.kind === 'confluence' && (
                          <div className="rk-fhint rk-mono">
                            {s.space} · {s.baseURL}
                          </div>
                        )}
                        {s.kind === 'git' && (
                          <div className="rk-fhint rk-mono" data-testid="knowledge-source-git-line">
                            {s.repository ? `${s.repository} · ` : ''}
                            {s.url}
                            {s.path ? ` /${s.path}` : ''} @ {s.branch}
                          </div>
                        )}
                        {s.kind === 'live' && (
                          <div className="rk-fhint rk-mono" data-testid="knowledge-source-live-line">
                            {s.addresses?.[0]?.url}
                            {(s.addresses?.length ?? 0) > 1 ? ` and ${(s.addresses?.length ?? 0) - 1} more` : ''}
                          </div>
                        )}
                        {s.kind === 'web' && (
                          <div className="rk-fhint" data-testid="knowledge-source-web-line">
                            <span className="rk-mono">{s.url}</span> · {depthWords(s.depth ?? 1)} · at most {s.pages ?? 20} pages
                            {(s.excluded?.length ?? 0) > 0 && ` · ${s.excluded!.length} excluded`}
                          </div>
                        )}
                        {s.kind !== 'web' && (s.excluded?.length ?? 0) > 0 && (
                          <div className="rk-fhint" data-testid="knowledge-source-excluded">
                            {s.excluded!.length} excluded
                          </div>
                        )}
                        {s.kind === 's3' && (
                          <div className="rk-fhint rk-mono" data-testid="knowledge-source-bucket-line">
                            s3://{s.bucket}/{s.prefix ?? ''}
                            {s.endpoint ? ` · ${s.endpoint}` : ''}
                          </div>
                        )}
                      </td>
                      <td>
                        {SOURCE_KIND_LABEL[s.kind]}
                        {(s.kind === 'confluence' || s.kind === 'git') && <div className="rk-fhint">{credLine(v, s, credNames)}</div>}
                        {s.kind === 's3' && (
                          <div className="rk-fhint" data-testid="knowledge-source-creds">
                            {bucketCreds(v, s, credNames)}
                          </div>
                        )}
                        {s.kind === 'console' && <div className="rk-fhint">Clusters, nodes, hosts, fleets, applications, scripts</div>}
                        {s.kind === 'fixes' && <div className="rk-fhint">Fixes seen in 3 cases or more, offered by the housekeeping for a runbook</div>}
                        {(s.kind === 'live' || s.kind === 'web') && <div className="rk-fhint">{!s.credential && (s.credMigrated || !s.auth) ? 'no sign-in' : credLine(v, s, credNames)}</div>}
                      </td>
                      <td data-testid="knowledge-source-mode" title={s.kind === 'live' ? 'Looked up when the agents troubleshoot, never copied' : 'Read into the wiki'}>
                        {sourceMode(s)}
                      </td>
                      <td data-testid="knowledge-source-trust">
                        {/* Its trust and what it is about (KN-56, KN-57): a live source is never indexed. */}
                        {ix && v.index ? <SourceIndexCell key={`${ix.trust}${ix.trustDefault}${ix.product}${ix.productVersion}${ix.followsClusterVersions}`} x={ix} clusters={v.index.clusterVersions} run={run} busy={busy} /> : '—'}
                      </td>
                      <td>{s.kind === 'console' && lastRead ? `Live · ${fmtTs(lastRead)}` : lastRead ? fmtTs(lastRead) : '—'}</td>
                      <td style={{ maxWidth: 320 }}>
                        {st.tone === 'err' ? (
                          // An error is said whole, wrapped, never cut (KN-28).
                          <span className="rk-t" data-tone="err" style={{ fontSize: 12, overflowWrap: 'anywhere' }} data-testid="knowledge-source-state">
                            {st.text}
                          </span>
                        ) : (
                          <span className="rk-pill" data-tone={st.tone} title={st.title ?? st.text} data-testid="knowledge-source-state">
                            {st.text}
                          </span>
                        )}
                      </td>
                      <td style={{ whiteSpace: 'nowrap' }}>
                        {/* Test is in the source's dialog, with the values as typed (KN-26). */}
                        {s.kind !== 'live' && (
                          <>
                            <a className="rk-link" href={jobHref(syncJobId(s.id))} title={`Its Wiki sync job in AIOps › Jobs: when it is read, Run now, its runs`} data-testid="knowledge-source-job">
                              Job
                            </a>{' '}
                          </>
                        )}
                        <button className="rk-link" disabled={busy} onClick={() => void run(() => setSourceOn(s.id, !!s.off))} data-testid="knowledge-source-onoff">
                          {s.off ? 'Turn on' : 'Turn off'}
                        </button>{' '}
                        <button className="rk-link" onClick={() => setEditing(s)} data-testid="knowledge-source-edit">
                          Edit…
                        </button>
                      </td>
                    </tr>
                  )
                })}
            </tbody>
          </table>
        </div>
        <UploadsTrust v={v} run={run} busy={busy} />
        <p className="rk-fhint" style={{ margin: 0 }}>
          What each source holds, and what is excluded from it, is in{' '}
          <a className="rk-link" href={SOURCES_HREF} data-testid="knowledge-items-link">
            AIOps › Mycroft › Sources
          </a>
          .
        </p>
      </section>
      {/* What people upload or note: not a source to sync again (0.12.1). */}
      <UploadedDocuments onToast={onToast} focus={focus === 'added' || focus === 'uploads'} />
      <StoreCard v={v} onSaved={() => void reload()} onToast={onToast} />
      <SearchCard v={v} run={run} busy={busy} />
      <section aria-label="More" style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
        <h3 className="rk-h" style={{ marginBottom: 4 }}>
          Freshness and pushes
        </h3>
        <StaleDays v={v} run={run} busy={busy} />
        <HookToken set={v.hook} run={run} busy={busy} />
        <Row label="Mycroft’s model" hint="Its model, budget and instructions">
          <a className="rk-link" href={aiHref('agents')} data-testid="knowledge-model-link">
            Mycroft, under Agents
          </a>
        </Row>
      </section>
      {editing && (
        <SourceDrawer
          seed={editing === 'new' ? null : editing}
          ownCredential={editing !== 'new' && !editing.credMigrated && !editing.credential && !!(v.creds[editing.id]?.token || v.creds[editing.id]?.accessKey)}
          identity={v.identity}
          pinned={editing === 'new' ? undefined : v.pins?.[editing.id]}
          onPinChanged={() => void reload()}
          onClose={() => setEditing(null)}
          onRemove={editing === 'new' || editing.kind === 'upload' || editing.kind === 'console' || editing.kind === 'fixes' ? undefined : () => void run(() => removeSource(editing.id), 'Source removed.').then(() => setEditing(null))}
          onSaved={(msg) => {
            setEditing(null)
            onToast?.(msg)
            void reload()
          }}
        />
      )}
    </section>
  )
}

/**
 * The trust of the sources of uploads (KN-57), not rows of the Sources table: an uploaded document takes it
 * unless it has its own (KN-61), chosen at its upload or on its row in AIOps › Mycroft › Sources.
 */
function UploadsTrust({ v, run, busy }: { v: KnowledgeView; run: (fn: () => Promise<unknown>, msg?: string) => Promise<void>; busy: boolean }) {
  const ups = (v.index?.sources ?? []).filter((x) => x.kind === 'upload')
  if (!v.index || ups.length === 0) return null
  return (
    <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start', flexWrap: 'wrap', fontSize: 12.5 }} data-testid="knowledge-uploads-trust">
      <span className="rk-fhint" style={{ flex: '1 1 260px' }}>
        Uploaded documents and notes take the trust of their source, unless a document has its own, chosen at its upload or on its row in AIOps › Mycroft › Sources.
      </span>
      {ups.map((x) => (
        <span key={x.id} style={{ display: 'flex', gap: 6, alignItems: 'flex-start' }}>
          <span style={{ paddingTop: 6 }}>{x.name}</span>
          <SourceIndexCell key={`${x.trust}${x.trustDefault}`} x={x} clusters={v.index!.clusterVersions} run={run} busy={busy} about={false} />
        </span>
      ))}
    </div>
  )
}

/** A web site's link depth, in words. */
export const DEPTH_WORDS = ['only the start page', 'and the pages it links to', 'two links away', 'three links away']
const depthWords = (d: number) => DEPTH_WORDS[d] ?? `${d} links away`

function StaleDays({ v, run, busy }: { v: KnowledgeView; run: (fn: () => Promise<unknown>, msg?: string) => Promise<void>; busy: boolean }) {
  const [days, setDays] = useState(String(v.settings.staleDays || 30))
  return (
    <Row label="Stale after" hint="A page whose sources were not seen this long is marked stale">
      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <span className="rk-field" style={{ width: 90 }}>
          <input type="number" min={1} max={365} value={days} onChange={(e) => setDays(e.target.value)} aria-label="Days" data-testid="knowledge-stale-days" />
        </span>
        <span className="rk-fhint">days</span>
        <button className="rk-btn" disabled={busy || Number(days) === (v.settings.staleDays || 30)} onClick={() => void run(() => saveStaleDays(Number(days)), 'Saved.')}>
          Save
        </button>
      </div>
    </Row>
  )
}

function HookToken({ set, run, busy }: { set: boolean; run: (fn: () => Promise<unknown>, msg?: string) => Promise<void>; busy: boolean }) {
  const [open, setOpen] = useState(false)
  const [token, setToken] = useState('')
  return (
    <Row label="Webhook" hint="Pushes and bucket notifications read the wiki again at once">
      <span className="rk-fhint">
        A git host or bucket calls <span className="rk-mono">/api/v1/knowledge/hook/wiki</span> (or <span className="rk-mono">hook/&lt;source id&gt;</span> for a repository of runbooks) with this token: as GitHub’s secret, GitLab’s token,
        or a bearer token.
      </span>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        <span className="rk-pill" data-tone={set ? 'ok' : 'muted'} data-testid="knowledge-hook-state">
          {set ? 'Token set' : 'No token'}
        </span>
        {!open ? (
          <button className="rk-link" onClick={() => setOpen(true)}>
            {set ? 'Replace…' : 'Set…'}
          </button>
        ) : (
          <>
            <span className="rk-field" style={{ width: 260 }}>
              <input type="password" autoComplete="off" value={token} placeholder="16 characters or more" onChange={(e) => setToken(e.target.value)} aria-label="Webhook token" data-testid="knowledge-hook-token" />
            </span>
            <button
              className="rk-btn"
              disabled={busy || token.length < 16}
              onClick={() =>
                void run(() => saveHookToken(token), 'Webhook token saved.').then(() => {
                  setToken('')
                  setOpen(false)
                })
              }
            >
              Save
            </button>
            {set && (
              <button className="rk-btn" disabled={busy} onClick={() => void run(() => saveHookToken(''), 'Webhook token removed.').then(() => setOpen(false))}>
                Remove
              </button>
            )}
          </>
        )}
      </div>
    </Row>
  )
}

const blankStore = (kind: StoreSettings['kind']): StoreSettings =>
  kind === 'git'
    ? { kind, url: '', branch: 'main', username: '' }
    : kind === 's3'
      ? { kind, endpoint: '', region: '', bucket: '', prefix: 'wiki/', pathStyle: false, credentials: 'keys', sse: '' }
      : kind === 'database'
        ? { kind }
        : { kind: '' }

/** Where the wiki is kept: a git repository, an S3 bucket, or the console's database (DB-96). */
function StoreCard({ v, onSaved, onToast }: { v: KnowledgeView; onSaved: () => void; onToast?: (msg: string) => void }) {
  const saved = v.settings.store
  const [st, setSt] = useState<StoreSettings>(saved.kind ? saved : blankStore('git'))
  const [keys, setKeys] = useState<KeysChange>({})
  const [replace, setReplace] = useState(false)
  const [check, setCheck] = useState<{ ok: boolean; check?: StoreCheck; error?: string; certificate?: PresentedCert } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const put = (p: Partial<StoreSettings>) => setSt((s) => ({ ...s, ...p }))
  // The pinned certificate is Trust's and Forget's, not the form's (CR-16).
  const changed = JSON.stringify({ ...st, pinnedCert: undefined }) !== JSON.stringify({ ...saved, pinnedCert: undefined }) || Object.keys(keys).length > 0
  const moving = !!saved.kind && (st.kind !== saved.kind || st.url !== saved.url || st.bucket !== saved.bucket || st.prefix !== saved.prefix || st.endpoint !== saved.endpoint)
  const act = async (fn: () => Promise<unknown>, msg: string) => {
    setBusy(true)
    setError('')
    try {
      await fn()
      setKeys({})
      setReplace(false)
      onToast?.(msg)
      onSaved()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const credSet = v.storeCreds
  return (
    <section className="rk-card" style={{ padding: '6px 16px 14px', display: 'flex', flexDirection: 'column' }} aria-label="Wiki" data-testid="knowledge-store">
      <h3 className="rk-h" style={{ margin: '8px 0' }}>
        Wiki
      </h3>
      <Row label="Stored in">
        <div className="rk-seg" role="group" aria-label="Stored in" style={{ alignSelf: 'flex-start' }}>
          <button aria-pressed={st.kind === 'git'} onClick={() => setSt(saved.kind === 'git' ? saved : blankStore('git'))} data-testid="knowledge-store-git">
            Git repository
          </button>
          <button aria-pressed={st.kind === 's3'} onClick={() => setSt(saved.kind === 's3' ? saved : blankStore('s3'))} data-testid="knowledge-store-s3">
            S3 bucket
          </button>
          {(v.databaseStore || saved.kind === 'database') && (
            <button aria-pressed={st.kind === 'database'} onClick={() => setSt(saved.kind === 'database' ? saved : blankStore('database'))} data-testid="knowledge-store-database">
              Database
            </button>
          )}
        </div>
        <span className="rk-fhint">Pages are kept as pages/&lt;kind&gt;/&lt;name&gt;.md beside manifest.json. With the chart’s network policy on, the store’s address goes in knowledge.egressCIDRs.</span>
      </Row>
      {st.kind === 'database' && (
        <Row label="Database" hint="Where the console keeps its records">
          <span className="rk-callout" style={{ fontSize: 12.5 }} data-testid="knowledge-store-database-label">
            {DATABASE_STORE_LABEL}
          </span>
          <span className="rk-fhint">
            Every replica reads it, and the database’s backups hold it. It holds at most 256 MiB, pages, excerpts and waiting changes together; a write past that is refused. Choose Git or S3 and Move the wiki here to keep every page’s
            history.
          </span>
        </Row>
      )}
      {st.kind === 'git' && (
        <>
          <Row label="Repository" hint="By HTTPS">
            <Text label="Address" value={st.url ?? ''} onChange={(x) => put({ url: x })} placeholder="https://git.example.com/ops/wiki.git" testId="knowledge-store-url" />
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              <Text label="Branch" value={st.branch ?? ''} onChange={(x) => put({ branch: x })} width={160} />
              <Text label="User the token goes with" value={st.username ?? ''} onChange={(x) => put({ username: x })} placeholder="x-access-token" width={200} />
            </div>
          </Row>
          <Row label="Credentials" hint="A token that can read and write the repository">
            <span className="rk-fhint" data-testid="knowledge-store-cred">
              {credSet.token ? `Token saved${saved.savedBy ? ` by ${saved.savedBy}` : ''}${saved.savedAt ? `, ${fmtTs(saved.savedAt)}` : ''} · never shown again` : 'No token saved.'}
            </span>
            {replace || !credSet.token ? (
              <Text label="Token" type="password" value={keys.token ?? ''} onChange={(x) => setKeys({ ...keys, token: x })} testId="knowledge-store-token" />
            ) : (
              <button className="rk-link" style={{ alignSelf: 'flex-start' }} onClick={() => setReplace(true)}>
                Replace…
              </button>
            )}
          </Row>
          <Row label="History">Every commit; Mycroft’s changes are a branch merged on approval, and a person’s commit first is rebased over.</Row>
        </>
      )}
      {st.kind === 's3' && (
        <>
          <Row label="Bucket" hint="AWS S3, or any S3-compatible store by its endpoint">
            <Text label="Endpoint (empty for AWS)" value={st.endpoint ?? ''} onChange={(x) => put({ endpoint: x })} placeholder="https://minio.example.internal" testId="knowledge-store-endpoint" />
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              <Text label="Bucket" value={st.bucket ?? ''} onChange={(x) => put({ bucket: x })} width={180} testId="knowledge-store-bucket" />
              <Text label="Prefix" value={st.prefix ?? ''} onChange={(x) => put({ prefix: x })} width={140} />
              <Text label="Region" value={st.region ?? ''} onChange={(x) => put({ region: x })} placeholder="us-east-1" width={130} />
            </div>
            <label className="rk-tcheck">
              <input type="checkbox" checked={!!st.pathStyle} onChange={(e) => put({ pathStyle: e.target.checked })} />
              Path-style addresses (most S3-compatible stores)
            </label>
          </Row>
          <Row label="Credentials">
            <Select
              label="Credentials"
              value={st.credentials ?? 'keys'}
              options={[
                ['keys', 'An access key in a Secret'],
                ['identity', v.identity ? 'The pod’s identity' : 'The pod’s identity (none found)'],
              ]}
              onChange={(x) => put({ credentials: x as 'keys' | 'identity' })}
              width={260}
            />
            {st.credentials !== 'identity' && (
              <>
                <span className="rk-fhint" data-testid="knowledge-store-cred">
                  {credSet.accessKey ? `Access key saved${saved.savedBy ? ` by ${saved.savedBy}` : ''}${saved.savedAt ? `, ${fmtTs(saved.savedAt)}` : ''} · never shown again` : 'No access key saved.'}
                </span>
                {replace || !credSet.accessKey ? (
                  <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                    <Text label="Access key ID" value={keys.accessKeyId ?? ''} onChange={(x) => setKeys({ ...keys, accessKeyId: x })} width={220} testId="knowledge-store-akid" />
                    <Text label="Secret access key" type="password" value={keys.secretAccessKey ?? ''} onChange={(x) => setKeys({ ...keys, secretAccessKey: x })} width={260} testId="knowledge-store-secret" />
                  </div>
                ) : (
                  <button className="rk-link" style={{ alignSelf: 'flex-start' }} onClick={() => setReplace(true)}>
                    Replace…
                  </button>
                )}
              </>
            )}
          </Row>
          <Row label="Encryption">
            <Select
              label="Server-side encryption"
              value={st.sse ?? ''}
              options={[
                ['', 'The bucket’s default'],
                ['AES256', 'SSE-S3'],
                ['aws:kms', 'SSE-KMS'],
              ]}
              onChange={(x) => put({ sse: x as StoreSettings['sse'], kmsKeyId: x === 'aws:kms' ? st.kmsKeyId : '' })}
              width={220}
            />
            {st.sse === 'aws:kms' && <Text label="KMS key (empty: the account’s default)" value={st.kmsKeyId ?? ''} onChange={(x) => put({ kmsKeyId: x })} />}
          </Row>
          <Row label="Versioning" hint="Every page keeps its history">
            {check?.check ? (
              <span className="rk-pill" data-tone={check.check.versioning === 'Enabled' ? 'ok' : 'warn'} style={{ alignSelf: 'flex-start' }} data-testid="knowledge-store-versioning">
                {check.check.versioning === 'Enabled' ? 'On' : 'Off: turn it on in the bucket'}
              </span>
            ) : (
              <span className="rk-fhint">Test to read it.</span>
            )}
          </Row>
        </>
      )}
      {st.kind !== 'database' && (
        <>
          <Row label="Refresh" hint={st.kind === 's3' ? 'A bucket notification, and a poll' : 'Each push, and a poll'}>
            <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
              <span className="rk-fhint">Every</span>
              <span className="rk-field" style={{ width: 80 }}>
                <input type="number" min={0} max={1440} value={st.refreshMinutes ?? 0} onChange={(e) => put({ refreshMinutes: Number(e.target.value) })} aria-label="Refresh minutes" />
              </span>
              <span className="rk-fhint">minutes (0: the default, {st.kind === 's3' ? 5 : 15})</span>
            </div>
          </Row>
          <Row label="Private CA" hint="Optional">
            <textarea className="rk-area" rows={2} spellCheck={false} placeholder="-----BEGIN CERTIFICATE-----" value={st.caPEM ?? ''} onChange={(e) => put({ caPEM: e.target.value })} aria-label="Private CA" />
          </Row>
        </>
      )}
      {saved.pinnedCert && st.kind === saved.kind && (
        <div style={{ marginTop: 8 }}>
          <PinnedCertNote
            pinned={{ pin: saved.pinnedCert, with: 'store' }}
            mayForget
            onForget={async () => {
              await forgetStoreCert()
              setCheck(null)
              onToast?.('The pinned certificate is forgotten.')
              onSaved()
            }}
          />
        </div>
      )}
      {check && !check.ok && (
        <p className="rk-callout" data-tone="warn" role="alert" style={{ margin: '8px 0 0', fontSize: 12.5 }} data-testid="knowledge-store-test">
          {check.error}
        </p>
      )}
      {check?.certificate && (
        <div style={{ marginTop: 8 }}>
          <CertificateResult
            cert={check.certificate}
            mayTrust
            onTrust={
              changed
                ? undefined
                : async (sha256) => {
                    await trustStoreCert(sha256)
                    onSaved()
                  }
            }
            unavailable="Save the store first: its certificate is trusted with it."
          />
        </div>
      )}
      {check?.ok && (
        <p className="rk-callout" data-tone={check.check?.warnings?.length ? 'warn' : 'info'} role="status" style={{ margin: '8px 0 0', fontSize: 12.5 }} data-testid="knowledge-store-test">
          Reached: {check.check?.files ?? 0} files of the wiki. {check.check?.warnings?.join(' ')}
        </p>
      )}
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: '8px 0 0', fontSize: 12.5 }}>
          {error}
        </p>
      )}
      <div style={{ display: 'flex', gap: 8, marginTop: 12, flexWrap: 'wrap' }}>
        <button
          className="rk-btn"
          disabled={busy}
          onClick={async () => {
            setBusy(true)
            try {
              setCheck(await testStore(st, keys))
            } catch (e) {
              setCheck({ ok: false, error: errorText(e) })
            } finally {
              setBusy(false)
            }
          }}
          data-testid="knowledge-store-test-btn"
        >
          Test
        </button>
        <button className="rk-btn" data-primary="true" disabled={busy || !changed} onClick={() => void act(() => saveStore(st, keys), 'Saved where the wiki is kept.')} data-testid="knowledge-store-save">
          Save changes
        </button>
        {moving && (
          <button
            className="rk-btn"
            disabled={busy}
            onClick={() =>
              void act(async () => {
                const r = await moveStore(st, keys)
                onToast?.(`${r.files} files copied.`)
              }, 'The wiki moved.')
            }
            data-testid="knowledge-store-move"
            title="Copies every page and the manifest from where the wiki is kept now, then keeps it here"
          >
            Move the wiki here
          </button>
        )}
        {!changed && (
          <span className="rk-fhint" style={{ alignSelf: 'center' }}>
            No changes.
          </span>
        )}
      </div>
    </section>
  )
}

/** A source being added or changed. */
function SourceDrawer({
  seed,
  ownCredential = false,
  identity = false,
  pinned,
  onPinChanged,
  onClose,
  onSaved,
  onRemove,
}: {
  seed: KnowledgeSource | null
  /** The certificate pinned for it: its own, its credential's, its repository's (CR-19). */
  pinned?: PinnedWith
  onPinChanged?: () => void
  /** It keeps its own credential of before 0.12.1, used until one is chosen. */
  ownCredential?: boolean
  identity?: boolean
  onClose: () => void
  onSaved: (msg: string) => void
  onRemove?: () => void
}) {
  const [s, setS] = useState<KnowledgeSource>(seed ?? { id: '', name: '', kind: 'confluence', flavor: 'cloud', baseURL: '', email: '', space: '' })
  // The credential of Administration › Credentials, by id (CR-10, CR-11): the
  // saved one, or none; null on a new source until chosen.
  const [cred, setCred] = useState<string | null>(seed ? (seed.credential ?? (seed.kind === 's3' && seed.credentials === 'identity' ? IDENTITY : '')) : null)
  // --- Administration › Settings › Repositories (0.12.1, RP-03): a git source picks a git
  // repository of the list, whose address and credential it reads with;
  // the branch and the directory stay its own.
  const [gitRepo, setGitRepo] = useState(seed?.repository ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [pin, setPin] = useState<PinnedWith | undefined>(pinned)
  // Test's state (KN-26): running, or what it found; kept until a field
  // changes or Test runs again. gen counts the changes, so a test's answer
  // to values changed since is dropped.
  const [test, setTest] = useState<{ running: true } | { running: false; r: SourceTestResult } | null>(null)
  const gen = useRef(0)
  const changed = () => {
    gen.current++
    setTest(null)
  }
  const put = (p: Partial<KnowledgeSource>) => {
    setS((x) => ({ ...x, ...p }))
    changed()
  }
  const id = seed ? seed.id : s.id || idFrom(s.name)
  // The console's own sources (its records, the fixes) and the documents
  // people upload or add are there from the start: only their name
  // changes here.
  const builtIn = s.kind === 'upload' || s.kind === 'console' || s.kind === 'fixes'
  // The source as sent: it names its credential, or a bucket the pod's identity.
  const withCred = (): KnowledgeSource => {
    const src: KnowledgeSource = { ...s, id }
    if (s.kind === 'live') src.addresses = liveAddresses(s)
    if (s.kind === 's3') {
      src.credentials = cred === IDENTITY ? 'identity' : 'keys'
      src.credential = cred === IDENTITY ? '' : (cred ?? '')
    } else if (s.kind === 'git') {
      src.repository = gitRepo
      src.credential = ''
    } else if (signsIn(s.kind)) src.credential = cred ?? ''
    // The sign-in of before 0.12.1 is the credential's now.
    if (s.kind === 'live' || s.kind === 'web') src.auth = ''
    return src
  }
  const runTest = async () => {
    const mine = ++gen.current
    setTest({ running: true })
    let r: SourceTestResult
    try {
      r = await testTypedSource(withCred(), seed?.id)
    } catch (e) {
      r = { ok: false, error: errorText(e) }
    }
    if (gen.current === mine) setTest({ running: false, r })
  }
  const save = async () => {
    setBusy(true)
    setError('')
    try {
      const src = withCred()
      if (seed) await saveSource(src)
      else await addSource(src)
      onSaved(seed ? `${s.name} saved.` : `${s.name} added.`)
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Drawer
      label="Source"
      kicker={seed ? 'Mycroft’s source' : 'Add a source'}
      title={seed ? seed.name : 'New source'}
      copy={false}
      onClose={onClose}
      testId="knowledge-source-drawer"
      footer={
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          <button className="rk-btn" data-primary="true" disabled={busy || !s.name.trim() || (s.kind === 'git' ? !gitRepo : signsIn(s.kind) && cred === null)} onClick={() => void save()} data-testid="knowledge-source-save">
            {seed ? 'Save' : 'Add source'}
          </button>
          <button className="rk-btn" onClick={onClose}>
            Cancel
          </button>
          {onRemove && (
            <button className="rk-btn" disabled={busy} onClick={onRemove} data-testid="knowledge-source-remove">
              Remove
            </button>
          )}
          {/* Test, apart from the rest: the values as typed, before they are saved (KN-26). */}
          {!builtIn && (
            <button className="rk-btn" style={{ marginLeft: 24 }} disabled={busy || !!test?.running} onClick={() => void runTest()} data-testid="knowledge-source-test">
              Test
            </button>
          )}
        </div>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12, padding: '4px 0' }}>
        {!seed && (
          <Select
            label="Mode"
            value={s.kind === 'live' ? 'live' : 'ingest'}
            options={[
              ['ingest', 'Ingest (read into the wiki)'],
              ['live', 'Live (looked up when troubleshooting, not copied)'],
            ]}
            onChange={(m) => {
              setS(m === 'live' ? { id: s.id, name: s.name, kind: 'live', addresses: [{ kind: 'page', url: '' }], auth: '' } : { id: s.id, name: s.name, kind: 'confluence', flavor: 'cloud', baseURL: '', email: '', space: '' })
              changed()
            }}
            testId="knowledge-source-mode-select"
          />
        )}
        {!seed && s.kind !== 'live' && (
          <Select
            label="Kind"
            value={s.kind}
            options={[
              ['confluence', 'Confluence space'],
              ['git', 'Git repository of runbooks'],
              ['s3', 'S3 bucket of documents'],
              ['web', 'Web site'],
            ]}
            onChange={(k) => {
              if (k === 's3') {
                setS({ id: s.id, name: s.name, kind: 's3', bucket: '', prefix: '', region: '', endpoint: '', pathStyle: false, credentials: 'keys' })
                changed()
              } else if (k === 'web') {
                setS({ id: s.id, name: s.name, kind: 'web', url: '', depth: 1, pages: 20, auth: '' })
                changed()
              } else put(k === 'git' ? { kind: 'git', branch: 'main' } : { kind: 'confluence', flavor: 'cloud' })
            }}
            testId="knowledge-source-kind"
          />
        )}
        <Text
          label="Name"
          value={s.name}
          onChange={(x) => put({ name: x })}
          placeholder={s.kind === 'git' ? 'Runbooks repository' : s.kind === 's3' ? 'Runbooks bucket' : s.kind === 'live' ? 'Known problems' : s.kind === 'web' ? 'Vendor documentation' : 'Confluence space: Infrastructure'}
          testId="knowledge-source-name"
        />
        {s.kind === 'confluence' && (
          <>
            <Select
              label="Confluence"
              value={s.flavor ?? 'cloud'}
              options={[
                ['cloud', 'Confluence Cloud (email and API token)'],
                ['datacenter', 'Confluence Data Center (personal access token)'],
              ]}
              onChange={(x) => put({ flavor: x as 'cloud' | 'datacenter' })}
              testId="knowledge-source-flavor"
            />
            <Text label="Address" value={s.baseURL ?? ''} onChange={(x) => put({ baseURL: x })} placeholder="https://example.atlassian.net" testId="knowledge-source-url" />
            <Text label="Space key" value={s.space ?? ''} onChange={(x) => put({ space: x })} placeholder="INFRA" testId="knowledge-source-space" />
          </>
        )}
        {s.kind === 'git' && (
          <>
            <GitRepoPicker
              label="Repository"
              value={gitRepo}
              keptAddress={gitRepo ? undefined : s.url || undefined}
              onChange={(r) => {
                setGitRepo(r?.name ?? '')
                put({ url: r?.url ?? '', repository: r?.name ?? '', ...(r?.defaultBranch && (!s.branch || s.branch === 'main') ? { branch: r.defaultBranch } : {}) })
              }}
              testId="knowledge-source-repository"
            />
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              <Text label="Branch" value={s.branch ?? ''} onChange={(x) => put({ branch: x })} width={160} />
              <Text label="Directory (empty: all)" value={s.path ?? ''} onChange={(x) => put({ path: x })} width={200} />
            </div>
          </>
        )}
        {s.kind === 's3' && (
          <>
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              <Text label="Bucket" value={s.bucket ?? ''} onChange={(x) => put({ bucket: x })} width={200} testId="knowledge-source-bucket" />
              <Text label="Prefix (empty: all)" value={s.prefix ?? ''} onChange={(x) => put({ prefix: x })} placeholder="runbooks/" width={160} testId="knowledge-source-prefix" />
              <Text label="Region" value={s.region ?? ''} onChange={(x) => put({ region: x })} placeholder="us-east-1" width={130} testId="knowledge-source-region" />
            </div>
            <Text label="Endpoint (empty for AWS)" value={s.endpoint ?? ''} onChange={(x) => put({ endpoint: x })} placeholder="https://minio.example.internal" testId="knowledge-source-endpoint" />
            <label className="rk-tcheck">
              <input type="checkbox" checked={!!s.pathStyle} onChange={(e) => put({ pathStyle: e.target.checked })} data-testid="knowledge-source-pathstyle" />
              Path-style addresses (most S3-compatible stores)
            </label>
            <span className="rk-fhint">Mycroft reads Markdown, text, Word (.docx), PDF, HTML and CSV under the prefix, up to 20 MiB each and 1000 objects a read; anything else is skipped and counted.</span>
          </>
        )}
        {s.kind === 'live' && <LiveFields s={s} put={put} />}
        {s.kind === 'web' && <WebFields s={s} put={put} />}
        {builtIn && (
          <span className="rk-fhint" data-testid="knowledge-source-builtin">
            There from the start: only its name changes here. Its Wiki sync job is renamed with it.
          </span>
        )}
        {!builtIn && (
          <>
            {s.kind !== 'git' && (
              <CredentialPicker
                use={sourceUse(s)}
                address={sourceAddress(s)}
                value={cred}
                onChange={(v) => {
                  setCred(v)
                  changed()
                }}
                label={
                  s.kind === 's3'
                    ? 'Credentials: a read-only access key, or the pod’s identity'
                    : s.kind === 'confluence'
                      ? s.flavor === 'datacenter'
                        ? 'Credential: a personal access token sent as Bearer'
                        : 'Credential: the account’s email and an API token, sent as Basic'
                      : 'Credential (any way of sending)'
                }
                extra={s.kind === 's3' ? [{ value: IDENTITY, label: identity ? 'The pod’s identity' : 'The pod’s identity (none found)' }] : []}
                testId="knowledge-source-credential"
              />
            )}
            {ownCredential && cred === '' && s.kind !== 'git' && (
              <span className="rk-fhint" data-testid="knowledge-source-own-credential">
                It reads with its own credential, kept before 0.12.1, until one is chosen here.
              </span>
            )}
            <span className="rk-fhint">The credential is sent only to the addresses under its endpoint, never to a model{s.kind === 'live' ? ': each address is checked' : ''}. Use a read-only one: Mycroft never writes to its sources.</span>
            <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)' }}>
              Private CA (optional)
              <textarea className="rk-area" rows={2} spellCheck={false} value={s.caPEM ?? ''} onChange={(e) => put({ caPEM: e.target.value })} />
            </label>
            {/* Test's result, under the fields; its button is in the footer (KN-26). */}
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6, alignItems: 'flex-start' }}>
              <span className="rk-fhint">
                {s.kind === 'live'
                  ? 'Test reads each address once with the values typed here, without saving them'
                  : s.kind === 'web'
                    ? 'Test reads the site as typed here and lists the pages Mycroft would read, without saving them'
                    : 'Test lists what Mycroft would read with the values typed here, without saving them'}
                .
              </span>
              {test?.running && (
                <p className="rk-fhint" role="status" style={{ margin: 0 }} data-testid="knowledge-source-test-result" data-state="running">
                  Testing…
                </p>
              )}
              {test && !test.running && (
                <p
                  className="rk-t"
                  data-tone={test.r.ok ? 'ok' : 'err'}
                  role={test.r.ok ? 'status' : 'alert'}
                  style={{ margin: 0, fontSize: 12.5, overflowWrap: 'anywhere' }}
                  data-testid="knowledge-source-test-result"
                  data-state={test.r.ok ? 'ok' : 'error'}
                >
                  {sourceTestText(test.r)}
                </p>
              )}
              {test && !test.running && test.r.certificate && (
                <CertificateResult
                  cert={test.r.certificate}
                  mayTrust
                  onTrust={
                    seed || (s.kind !== 'git' && cred && cred !== IDENTITY)
                      ? async (sha256) => {
                          setPin(await trustSourceCert(sha256, withCred(), seed?.id))
                          onPinChanged?.()
                        }
                      : undefined
                  }
                  unavailable="Save the source first: without a credential, its certificate is trusted with the source."
                />
              )}
              {pin && (
                <PinnedCertNote
                  pinned={pin}
                  mayForget={!!seed && pin.with !== 'repository'}
                  onForget={
                    seed
                      ? async () => {
                          await forgetSourceCert(seed.id)
                          setPin(undefined)
                          setTest(null)
                          onPinChanged?.()
                        }
                      : undefined
                  }
                />
              )}
              {test && !test.running && test.r.live && (
                <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5 }} data-testid="knowledge-source-live-results">
                  {test.r.live.map((c, i) => (
                    <li key={i} data-state={c.ok ? 'ok' : 'error'} style={{ overflowWrap: 'anywhere' }}>
                      <span className="rk-mono">{c.url}</span>:{' '}
                      <span className="rk-t" data-tone={c.ok ? 'ok' : 'err'}>
                        {liveCheckText(c)}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </>
        )}
        {error && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="knowledge-source-error">
            {error}
          </p>
        )}
      </div>
    </Drawer>
  )
}

/** A live source's addresses as saved: the empty rows left out. */
function liveAddresses(s: KnowledgeSource): LiveAddress[] {
  return (s.addresses ?? []).filter((a) => a.url.trim() !== '').map((a) => (a.kind === 'confluenceSearch' ? a : { kind: a.kind, url: a.url }))
}

/** A live source's addresses and sign-in (KN-39): never copied, read when the agents troubleshoot. */
function LiveFields({ s, put }: { s: KnowledgeSource; put: (p: Partial<KnowledgeSource>) => void }) {
  const list = s.addresses ?? []
  const set = (i: number, p: Partial<LiveAddress>) => put({ addresses: list.map((a, j) => (j === i ? { ...a, ...p } : a)) })
  return (
    <>
      <span className="rk-fhint">
        Never copied into the wiki: each address is read when an agent troubleshoots (up to 200 KiB a page, kept five minutes), and a report no known issue explains is checked against it. The console reads them from its pod: with the
        network policy on, their hosts must be listed under the chart’s knowledge.egressCIDRs.
      </span>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="knowledge-live-addresses">
        {list.map((a, i) => (
          <div key={i} style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'flex-end' }} data-testid="knowledge-live-address">
            <Select label={`Address ${i + 1}`} value={a.kind} options={Object.entries(LIVE_ADDRESS_LABEL) as [LiveAddress['kind'], string][]} onChange={(k) => set(i, { kind: k as LiveAddress['kind'] })} testId="knowledge-live-kind" />
            <Text
              label={a.kind === 'confluenceSearch' ? 'Confluence site (with /wiki for Cloud)' : 'Address (https)'}
              value={a.url}
              onChange={(x) => set(i, { url: x })}
              placeholder={a.kind === 'confluenceSearch' ? 'https://example.atlassian.net/wiki' : a.kind === 'confluencePage' ? 'https://example.atlassian.net/wiki/spaces/OPS/pages/123/Known+problems' : 'https://status.example.com/'}
              width={320}
              testId="knowledge-live-url"
            />
            {a.kind === 'confluenceSearch' && <Text label="Space key" value={a.space ?? ''} onChange={(x) => set(i, { space: x })} placeholder="OPS" width={110} testId="knowledge-live-space" />}
            {list.length > 1 && (
              <button className="rk-link" onClick={() => put({ addresses: list.filter((_, j) => j !== i) })} data-testid="knowledge-live-remove">
                Remove
              </button>
            )}
          </div>
        ))}
        {list.length < 20 && (
          <button className="rk-btn" style={{ alignSelf: 'flex-start' }} onClick={() => put({ addresses: [...list, { kind: 'page', url: '' }] })} data-testid="knowledge-live-add">
            Add an address
          </button>
        )}
      </div>
    </>
  )
}

/** The select's value for a bucket read with the pod's identity. */
const IDENTITY = '__identity'

/** Whether a kind of source signs in with a credential (CR-11). */
const signsIn = (k: KnowledgeSource['kind']) => k === 'confluence' || k === 'git' || k === 's3' || k === 'web' || k === 'live'

/** What a source's credential is for: the picker offers only what serves it. */
function sourceUse(s: KnowledgeSource): CredentialUse {
  if (s.kind === 'git') return 'git'
  if (s.kind === 's3') return 's3'
  if (s.kind === 'confluence') return s.flavor === 'datacenter' ? 'confluence-dc' : 'confluence-cloud'
  return 'http'
}

/** The address a source's credential must cover: its site, repository, bucket, start address, or first address. */
export function sourceAddress(s: KnowledgeSource): string {
  if (s.kind === 'confluence') return s.baseURL ?? ''
  if (s.kind === 'git' || s.kind === 'web') return s.url ?? ''
  if (s.kind === 's3') return s.bucket ? `${(s.endpoint ?? '').trim().replace(/\/+$/, '') || 'https://s3.amazonaws.com'}/${s.bucket}` : ''
  if (s.kind === 'live') return s.addresses?.find((a) => a.url.trim())?.url ?? ''
  return ''
}

/** The most pages a web site's read takes. */
export const MAX_SITE_PAGES = 200

/** A web site (0.12.1): its start address, how many links away and at most how many pages are read, and its sign-in. */
function WebFields({ s, put }: { s: KnowledgeSource; put: (p: Partial<KnowledgeSource>) => void }) {
  return (
    <>
      <Text label="Start address" value={s.url ?? ''} onChange={(x) => put({ url: x })} placeholder="https://docs.example.com/runbooks/" testId="knowledge-web-url" />
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'flex-end' }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)' }}>
          Link depth
          <Select label="Link depth" value={String(s.depth ?? 1)} options={DEPTH_WORDS.map((w, i): [string, string] => [String(i), `${i}: ${w}`])} onChange={(x) => put({ depth: Number(x) })} testId="knowledge-web-depth" />
        </label>
        <Text label={`Page limit (1–${MAX_SITE_PAGES})`} type="number" value={String(s.pages ?? 20)} onChange={(x) => put({ pages: Number(x) })} width={150} testId="knowledge-web-pages" />
      </div>
      <span className="rk-fhint">
        Only pages of the same site are read, one at a time; robots.txt and pages that ask not to be indexed are respected, and what is excluded in AIOps › Mycroft › Sources is neither read nor followed. The console reads the site from its
        pod: with the network policy on, its host must be listed under the chart’s knowledge.egressCIDRs.
      </span>
    </>
  )
}

/** A source's id from its name: lower case, digits and dashes. */
export function idFrom(name: string): string {
  return (
    name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .slice(0, 40) || 'source'
  )
}
