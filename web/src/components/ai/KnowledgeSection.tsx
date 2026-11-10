import { useEffect, useRef, useState, type ReactNode } from 'react'
import {
  addSource,
  fetchKnowledge,
  fileBase64,
  moveStore,
  removeDocument,
  removeSource,
  saveHookToken,
  saveSource,
  saveStaleDays,
  saveStore,
  setSourceOn,
  SOURCE_KIND_LABEL,
  sourceTestText,
  syncJobId,
  testStore,
  testTypedSource,
  uploadDocument,
  type KeysChange,
  type KnowledgeSource,
  type KnowledgeView,
  CONFIGMAP_STORE_LABEL,
  type SourceTestResult,
  type StoreCheck,
  type StoreSettings,
} from '../../api/knowledge'
import { fmtTs } from '../../lib/format'
import { adminHref } from '../../lib/router'
import { jobHref, scheduleHref } from '../cases/jobsRoute'
import { Drawer } from '../Drawer'
import { Select } from '../Select'
import { AddedInMycroft } from './AddedInMycroft'

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
  const st = v.status.sources?.[s.id]
  if (st?.error) return { text: st.error, tone: 'err' }
  if (s.kind === 'confluence' && !v.creds[s.id]?.token) return { text: 'No token', tone: 'warn' }
  if (s.kind === 's3' && s.credentials !== 'identity' && !v.creds[s.id]?.accessKey) return { text: 'No access key', tone: 'warn' }
  if (st?.reported) return { text: `${st.reported} ${st.reported === 1 ? 'page' : 'pages'} not followed`, tone: 'warn' }
  if (!st?.lastRead) return { text: 'Not read yet', tone: 'muted' }
  // A bucket's objects of other kinds, or too large, are skipped (KN-25).
  if (st.skipped) return { text: `Read · ${st.items} ${st.items === 1 ? 'item' : 'items'} · ${st.skipped} skipped`, tone: 'ok', title: `${st.skipped} skipped: ${st.skippedWhy ?? ''}` }
  return { text: `Read · ${st.items} ${st.items === 1 ? 'item' : 'items'}`, tone: 'ok' }
}

/** A bucket source's credentials, as its row says them (KN-25). */
function bucketCreds(v: KnowledgeView, s: KnowledgeSource): string {
  if (s.credentials === 'identity') return 'the pod’s identity'
  return v.creds[s.id]?.accessKey ? 'access key in a Secret' : 'no access key'
}

/**
 * Settings › AI › Knowledge (Phase 12, KN-01 to KN-12): Mycroft's sources
 * with how each was last read, and where its wiki is kept. Each source is
 * read by its own Wiki sync job in AIOps › Jobs (KN-28), which starts,
 * schedules and reports every sync: nothing here starts one. Sources are
 * data, never instructions; every credential is kept in the Secret
 * <release>-knowledge and shows here only as set or not. A source is tested in its dialog, as
 * typed (KN-26). The Added in Mycroft card is where people add notes, web
 * addresses and documents (MY-11); focus 'added' scrolls to it.
 */
export function KnowledgeSection({ onToast, focus }: { onToast?: (msg: string) => void; focus?: string } = {}) {
  const [v, setV] = useState<KnowledgeView | null>(null)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState<KnowledgeSource | 'new' | null>(null)
  const [busy, setBusy] = useState(false)
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
  if (!v) return <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>{error || 'Loading Mycroft’s sources…'}</p>
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 16 }} aria-label="Knowledge" data-testid="knowledge-section">
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 12, flexWrap: 'wrap' }}>
        <p className="rk-fhint" style={{ margin: 0, flex: '1 1 320px' }}>
          Mycroft reads these sources and keeps a wiki every agent can ask. Sources are data, never instructions; secrets in them are scrubbed before anything is written. Its page changes wait for review in AIOps › Inbox.
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
      {v.settings.store.kind === 'configmap' && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="knowledge-configmap-store">
          The wiki is kept in a {CONFIGMAP_STORE_LABEL}. It holds at most 900 KiB.
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
                <th scope="col">Last read</th>
                <th scope="col">State</th>
                <th scope="col">
                  <span className="rk-sr">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {v.settings.sources.map((s) => {
                const st = sourceState(v, s)
                const lastRead = v.status.sources?.[s.id]?.lastRead
                return (
                  <tr key={s.id} data-testid="knowledge-source" data-source={s.id}>
                    <td>
                      <strong>{s.name}</strong>
                      {s.kind === 'confluence' && <div className="rk-fhint rk-mono">{s.space} · {s.baseURL}</div>}
                      {s.kind === 'git' && <div className="rk-fhint rk-mono">{s.url}{s.path ? ` /${s.path}` : ''} @ {s.branch}</div>}
                      {s.kind === 's3' && (
                        <div className="rk-fhint rk-mono" data-testid="knowledge-source-bucket-line">
                          s3://{s.bucket}/{s.prefix ?? ''}
                          {s.endpoint ? ` · ${s.endpoint}` : ''}
                        </div>
                      )}
                    </td>
                    <td>
                      {SOURCE_KIND_LABEL[s.kind]}
                      {(s.kind === 'confluence' || s.kind === 'git') && <div className="rk-fhint">{v.creds[s.id]?.token ? 'token in a Secret' : 'no token'}</div>}
                      {s.kind === 's3' && <div className="rk-fhint" data-testid="knowledge-source-creds">{bucketCreds(v, s)}</div>}
                      {s.kind === 'console' && <div className="rk-fhint">Clusters, nodes, hosts, fleets, applications, scripts</div>}
                      {s.kind === 'fixes' && <div className="rk-fhint">Fixes seen in 3 cases or more, offered by the housekeeping for a runbook</div>}
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
                      <a className="rk-link" href={jobHref(syncJobId(s.id))} title={`Its Wiki sync job in AIOps › Jobs: when it is read, Run now, its runs`} data-testid="knowledge-source-job">
                        Job
                      </a>{' '}
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
        {v.settings.sources
          .filter((s) => s.kind === 'upload' && s.id !== ADDED_SOURCE)
          .map((s) => (
            <Documents key={s.id} source={s} v={v} busy={busy} run={run} />
          ))}
        {/* What people add in Mycroft, and the form to add it (MY-11). */}
        {v.settings.sources.some((s) => s.id === ADDED_SOURCE) && <AddedInMycroft onToast={onToast} focus={focus === 'added'} />}
      </section>
      <StoreCard v={v} onSaved={() => void reload()} onToast={onToast} />
      <section aria-label="More" style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
        <h3 className="rk-h" style={{ marginBottom: 4 }}>Freshness and pushes</h3>
        <StaleDays v={v} run={run} busy={busy} />
        <HookToken set={v.hook} run={run} busy={busy} />
        <Row label="Mycroft’s model" hint="Its model, budget and instructions">
          <a className="rk-link" href={adminHref('settings', ['agents'])} data-testid="knowledge-model-link">
            Mycroft, under Agents
          </a>
        </Row>
      </section>
      {editing && (
        <SourceDrawer
          seed={editing === 'new' ? null : editing}
          tokenSet={editing !== 'new' && !!v.creds[editing.id]?.token}
          keySet={editing !== 'new' && !!v.creds[editing.id]?.accessKey}
          identity={v.identity}
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

/** The source of what people add in Mycroft (the server's AddedSourceID). */
const ADDED_SOURCE = 'added'

/** The uploaded documents of a source, and an upload. */
function Documents({ source, v, busy, run }: { source: KnowledgeSource; v: KnowledgeView; busy: boolean; run: (fn: () => Promise<unknown>, msg?: string) => Promise<void> }) {
  const input = useRef<HTMLInputElement>(null)
  const docs = v.documents?.[source.id] ?? []
  return (
    <div className="rk-card" style={{ padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="knowledge-documents">
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <strong style={{ fontSize: 12.5 }}>{source.name}</strong>
        <span className="rk-fhint">Markdown, text, Word (.docx), PDF and HTML, up to 20 MiB. Read once when uploaded; only the excerpts and the pages Mycroft writes are kept.</span>
        <span style={{ flex: '1 1 auto' }} />
        <input
          ref={input}
          type="file"
          accept=".md,.markdown,.txt,.rst,.adoc,.docx,.pdf,.html,.htm,.csv"
          style={{ display: 'none' }}
          data-testid="knowledge-upload-input"
          onChange={(e) => {
            const f = e.target.files?.[0]
            e.target.value = ''
            if (f) void run(async () => uploadDocument(source.id, f.name, await fileBase64(f)), `Mycroft is reading ${f.name}.`)
          }}
        />
        <button className="rk-btn" disabled={busy || !v.settings.store.kind} onClick={() => input.current?.click()} data-testid="knowledge-upload">
          Upload…
        </button>
      </div>
      {docs.length === 0 ? (
        <span className="rk-fhint">No documents yet.</span>
      ) : (
        <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5 }}>
          {docs.map((d) => (
            <li key={d.id}>
              {d.title} <span className="rk-fhint">{d.lastRead ? `read ${fmtTs(d.lastRead)}` : 'waiting to be read'}</span>{' '}
              <button className="rk-link" disabled={busy} onClick={() => void run(() => removeDocument(source.id, d.id), `${d.title} removed.`)}>
                Remove
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

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
        A git host or bucket calls <span className="rk-mono">/api/v1/knowledge/hook/wiki</span> (or <span className="rk-mono">hook/&lt;source id&gt;</span> for a repository of runbooks) with this token: as GitHub’s secret, GitLab’s token, or a bearer token.
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
            <button className="rk-btn" disabled={busy || token.length < 16} onClick={() => void run(() => saveHookToken(token), 'Webhook token saved.').then(() => { setToken(''); setOpen(false) })}>
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
  kind === 'git' ? { kind, url: '', branch: 'main', username: '' } : kind === 's3' ? { kind, endpoint: '', region: '', bucket: '', prefix: 'wiki/', pathStyle: false, credentials: 'keys', sse: '' } : kind === 'configmap' ? { kind } : { kind: '' }

/** Where the wiki is kept: a git repository, an S3 bucket, or the temporary ConfigMap store (CF-57). */
function StoreCard({ v, onSaved, onToast }: { v: KnowledgeView; onSaved: () => void; onToast?: (msg: string) => void }) {
  const saved = v.settings.store
  const [st, setSt] = useState<StoreSettings>(saved.kind ? saved : blankStore('git'))
  const [keys, setKeys] = useState<KeysChange>({})
  const [replace, setReplace] = useState(false)
  const [check, setCheck] = useState<{ ok: boolean; check?: StoreCheck; error?: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const put = (p: Partial<StoreSettings>) => setSt((s) => ({ ...s, ...p }))
  const changed = JSON.stringify(st) !== JSON.stringify(saved) || Object.keys(keys).length > 0
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
      <h3 className="rk-h" style={{ margin: '8px 0' }}>Wiki</h3>
      <Row label="Stored in">
        <div className="rk-seg" role="group" aria-label="Stored in" style={{ alignSelf: 'flex-start' }}>
          <button aria-pressed={st.kind === 'git'} onClick={() => setSt(saved.kind === 'git' ? saved : blankStore('git'))} data-testid="knowledge-store-git">
            Git repository
          </button>
          <button aria-pressed={st.kind === 's3'} onClick={() => setSt(saved.kind === 's3' ? saved : blankStore('s3'))} data-testid="knowledge-store-s3">
            S3 bucket
          </button>
          {(v.configMapStore || saved.kind === 'configmap') && (
            <button aria-pressed={st.kind === 'configmap'} onClick={() => setSt(saved.kind === 'configmap' ? saved : blankStore('configmap'))} data-testid="knowledge-store-configmap">
              ConfigMap (temporary)
            </button>
          )}
        </div>
        <span className="rk-fhint">Pages are kept as pages/&lt;kind&gt;/&lt;name&gt;.md beside manifest.json. With the chart’s network policy on, the store’s address goes in knowledge.egressCIDRs.</span>
      </Row>
      {st.kind === 'configmap' && (
        <Row label="ConfigMap" hint="The chart’s <release>-wiki">
          <span className="rk-callout" data-tone="warn" style={{ fontSize: 12.5 }} data-testid="knowledge-store-configmap-label">
            {CONFIGMAP_STORE_LABEL}
          </span>
          <span className="rk-fhint">Every replica reads it; it holds at most 900 KiB, and a write past that is refused. Choose Git or S3 and Move the wiki here to keep the pages.</span>
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
            <span className="rk-fhint" data-testid="knowledge-store-cred">{credSet.token ? `Token saved${saved.savedBy ? ` by ${saved.savedBy}` : ''}${saved.savedAt ? `, ${fmtTs(saved.savedAt)}` : ''} · never shown again` : 'No token saved.'}</span>
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
            <Select label="Credentials" value={st.credentials ?? 'keys'} options={[['keys', 'An access key in a Secret'], ['identity', v.identity ? 'The pod’s identity' : 'The pod’s identity (none found)']]} onChange={(x) => put({ credentials: x as 'keys' | 'identity' })} width={260} />
            {st.credentials !== 'identity' && (
              <>
                <span className="rk-fhint" data-testid="knowledge-store-cred">{credSet.accessKey ? `Access key saved${saved.savedBy ? ` by ${saved.savedBy}` : ''}${saved.savedAt ? `, ${fmtTs(saved.savedAt)}` : ''} · never shown again` : 'No access key saved.'}</span>
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
            <Select label="Server-side encryption" value={st.sse ?? ''} options={[['', 'The bucket’s default'], ['AES256', 'SSE-S3'], ['aws:kms', 'SSE-KMS']]} onChange={(x) => put({ sse: x as StoreSettings['sse'], kmsKeyId: x === 'aws:kms' ? st.kmsKeyId : '' })} width={220} />
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
      {st.kind !== 'configmap' && (
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
      {check && !check.ok && (
        <p className="rk-callout" data-tone="warn" role="alert" style={{ margin: '8px 0 0', fontSize: 12.5 }} data-testid="knowledge-store-test">
          {check.error}
        </p>
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
          <button className="rk-btn" disabled={busy} onClick={() => void act(async () => { const r = await moveStore(st, keys); onToast?.(`${r.files} files copied.`) }, 'The wiki moved.')} data-testid="knowledge-store-move" title="Copies every page and the manifest from where the wiki is kept now, then keeps it here">
            Move the wiki here
          </button>
        )}
        {!changed && <span className="rk-fhint" style={{ alignSelf: 'center' }}>No changes.</span>}
      </div>
    </section>
  )
}

/** A source being added or changed. */
function SourceDrawer({ seed, tokenSet, keySet = false, identity = false, onClose, onSaved, onRemove }: { seed: KnowledgeSource | null; tokenSet: boolean; keySet?: boolean; identity?: boolean; onClose: () => void; onSaved: (msg: string) => void; onRemove?: () => void }) {
  const [s, setS] = useState<KnowledgeSource>(seed ?? { id: '', name: '', kind: 'confluence', flavor: 'cloud', baseURL: '', email: '', space: '' })
  const [token, setToken] = useState('')
  // A bucket's access key (KN-25): typed here, sent once, never shown again.
  const [keys, setKeys] = useState<KeysChange>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
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
  // Only what was typed is sent: the saved credential stays otherwise.
  const typedKeys = (): KeysChange => {
    const typed: KeysChange = {}
    if (s.credentials !== 'identity' && keys.accessKeyId) typed.accessKeyId = keys.accessKeyId
    if (s.credentials !== 'identity' && keys.secretAccessKey) typed.secretAccessKey = keys.secretAccessKey
    return typed
  }
  const runTest = async () => {
    const mine = ++gen.current
    setTest({ running: true })
    let r: SourceTestResult
    try {
      const src = { ...s, id }
      r = s.kind === 's3' ? await testTypedSource(src, undefined, typedKeys(), seed?.id) : await testTypedSource(src, token || undefined, undefined, seed?.id)
    } catch (e) {
      r = { ok: false, error: errorText(e) }
    }
    if (gen.current === mine) setTest({ running: false, r })
  }
  const save = async () => {
    setBusy(true)
    setError('')
    try {
      const src = { ...s, id }
      if (s.kind === 's3') {
        if (seed) await saveSource(src, undefined, typedKeys())
        else await addSource(src, undefined, typedKeys())
      } else if (seed) await saveSource(src, token || undefined)
      else await addSource(src, token || undefined)
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
          <button className="rk-btn" data-primary="true" disabled={busy || !s.name.trim()} onClick={() => void save()} data-testid="knowledge-source-save">
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
            label="Kind"
            value={s.kind}
            options={[['confluence', 'Confluence space'], ['git', 'Git repository of runbooks'], ['s3', 'S3 bucket of documents']]}
            onChange={(k) => {
              if (k === 's3') {
                setS({ id: s.id, name: s.name, kind: 's3', bucket: '', prefix: '', region: '', endpoint: '', pathStyle: false, credentials: 'keys' })
                changed()
              } else put(k === 'git' ? { kind: 'git', branch: 'main' } : { kind: 'confluence', flavor: 'cloud' })
            }}
            testId="knowledge-source-kind"
          />
        )}
        <Text label="Name" value={s.name} onChange={(x) => put({ name: x })} placeholder={s.kind === 'git' ? 'Runbooks repository' : s.kind === 's3' ? 'Runbooks bucket' : 'Confluence space: Infrastructure'} testId="knowledge-source-name" />
        {s.kind === 'confluence' && (
          <>
            <Select label="Confluence" value={s.flavor ?? 'cloud'} options={[['cloud', 'Confluence Cloud (email and API token)'], ['datacenter', 'Confluence Data Center (personal access token)']]} onChange={(x) => put({ flavor: x as 'cloud' | 'datacenter' })} testId="knowledge-source-flavor" />
            <Text label="Address" value={s.baseURL ?? ''} onChange={(x) => put({ baseURL: x })} placeholder="https://example.atlassian.net" testId="knowledge-source-url" />
            {s.flavor !== 'datacenter' && <Text label="Account email" value={s.email ?? ''} onChange={(x) => put({ email: x })} testId="knowledge-source-email" />}
            <Text label="Space key" value={s.space ?? ''} onChange={(x) => put({ space: x })} placeholder="INFRA" testId="knowledge-source-space" />
          </>
        )}
        {s.kind === 'git' && (
          <>
            <Text label="Repository address (HTTPS)" value={s.url ?? ''} onChange={(x) => put({ url: x })} placeholder="https://git.example.com/ops/runbooks.git" testId="knowledge-source-url" />
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              <Text label="Branch" value={s.branch ?? ''} onChange={(x) => put({ branch: x })} width={160} />
              <Text label="Directory (empty: all)" value={s.path ?? ''} onChange={(x) => put({ path: x })} width={200} />
              <Text label="User" value={s.username ?? ''} onChange={(x) => put({ username: x })} placeholder="x-access-token" width={160} />
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
            <Select
              label="Credentials"
              value={s.credentials ?? 'keys'}
              options={[['keys', 'A read-only access key in a Secret'], ['identity', identity ? 'The pod’s identity' : 'The pod’s identity (none found)']]}
              onChange={(x) => put({ credentials: x as 'keys' | 'identity' })}
              testId="knowledge-source-credentials"
            />
            {s.credentials !== 'identity' && (
              <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                <Text label={keySet ? 'Access key ID (saved; type to replace)' : 'Access key ID'} value={keys.accessKeyId ?? ''} onChange={(x) => { setKeys({ ...keys, accessKeyId: x }); changed() }} width={220} testId="knowledge-source-akid" />
                <Text label={keySet ? 'Secret access key (saved; type to replace)' : 'Secret access key'} type="password" value={keys.secretAccessKey ?? ''} onChange={(x) => { setKeys({ ...keys, secretAccessKey: x }); changed() }} width={240} testId="knowledge-source-secret" />
              </div>
            )}
          </>
        )}
        {builtIn && <span className="rk-fhint" data-testid="knowledge-source-builtin">There from the start: only its name changes here. Its Wiki sync job is renamed with it.</span>}
        {!builtIn && (
          <>
        {s.kind !== 's3' && <Text label={tokenSet ? 'Token (saved; type a new one to replace it)' : s.kind === 'git' ? 'Token (read access)' : 'API token (read-only)'} type="password" value={token} onChange={(x) => { setToken(x); changed() }} testId="knowledge-source-token" />}
        <span className="rk-fhint">Kept in the Secret &lt;release&gt;-knowledge and never shown again. Use a read-only credential: Mycroft never writes to its sources.</span>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)' }}>
          Private CA (optional)
          <textarea className="rk-area" rows={2} spellCheck={false} value={s.caPEM ?? ''} onChange={(e) => put({ caPEM: e.target.value })} />
        </label>
        {/* Test's result, under the fields; its button is in the footer (KN-26). */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, alignItems: 'flex-start' }}>
          <span className="rk-fhint">Test lists what Mycroft would read with the values typed here, without saving them{seed ? '; a credential left blank is the saved one' : ''}.</span>
          {test?.running && (
            <p className="rk-fhint" role="status" style={{ margin: 0 }} data-testid="knowledge-source-test-result" data-state="running">
              Testing…
            </p>
          )}
          {test && !test.running && (
            <p className="rk-t" data-tone={test.r.ok ? 'ok' : 'err'} role={test.r.ok ? 'status' : 'alert'} style={{ margin: 0, fontSize: 12.5, overflowWrap: 'anywhere' }} data-testid="knowledge-source-test-result" data-state={test.r.ok ? 'ok' : 'error'}>
              {sourceTestText(test.r)}
            </p>
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
