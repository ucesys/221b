import { useCallback, useEffect, useState } from 'react'
import {
  createCredential,
  daysLeft,
  deleteCredential,
  dismissMigration,
  expiringSoon,
  fetchCredentials,
  KIND_LABEL,
  METHOD_LABEL,
  saveCredential,
  sendsHTTP,
  sendText,
  testCredential,
  type Credential,
  type CredentialInput,
  type CredentialKind,
  type CredentialSecret,
  type CredentialTest,
  type CredentialsView,
  type SendMethod,
} from '../api/credentials'
import { fmtTs } from '../lib/format'
import { ConfirmDialog } from './ConfirmDialog'
import { Drawer } from './Drawer'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** An expiry in words, as a pill: expired, or in how many days (CR-08). */
export function expiryText(expires: string | undefined, now = Date.now()): { text: string; tone: 'err' | 'warn' | '' } | null {
  const d = daysLeft(expires, now)
  if (d === null) return null
  if (d < 0) return { text: 'Expired', tone: 'err' }
  if (d <= 14) return { text: d === 0 ? 'Expires today' : `Expires in ${d} ${d === 1 ? 'day' : 'days'}`, tone: 'warn' }
  return { text: `Expires ${fmtTs(expires!).split(' ')[0]}`, tone: '' }
}

/**
 * Administration › Credentials (0.12.1, CR-01 to CR-14): who the console is when
 * it signs in somewhere, and where that applies. A SuperAdmin adds,
 * changes, tests and removes them in a drawer with its own Save; a secret
 * is written once and never shown. Applications, Helm repositories and
 * Mycroft's sources pick one by name. What the upgrade made of the
 * secrets kept before is listed until dismissed.
 */
export function CredentialsSettings({ onToast }: { onToast?: (msg: string) => void }) {
  const [view, setView] = useState<CredentialsView | null>(null)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState<{ saved?: Credential } | null>(null)
  const [removing, setRemoving] = useState<Credential | null>(null)
  const [testing, setTesting] = useState('')
  const load = useCallback(() => {
    fetchCredentials().then(
      (v) => {
        setView(v)
        setError('')
      },
      (e: unknown) => setError(errorText(e)),
    )
  }, [])
  useEffect(() => load(), [load])
  const list = view?.credentials ?? []
  const soon = expiringSoon(list)
  const mayEdit = !!view?.mayEdit
  const retest = async (c: Credential) => {
    setTesting(c.id)
    try {
      const t = await testCredential(c.id, '')
      onToast?.(`${c.name}: ${t.text}`)
      load()
    } catch (e) {
      onToast?.(errorText(e))
    } finally {
      setTesting('')
    }
  }
  const migration = view?.migration
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 14 }} aria-label="Credentials" data-testid="credentials-settings">
      <p className="rk-fhint" style={{ margin: 0 }}>
        The tokens, keys and certificates the console signs in with. Each is sent only to the addresses under its endpoint; applications, Helm repositories and Mycroft’s sources choose one by name. A secret is kept in a Kubernetes Secret
        and never shown again.
      </p>
      {error && (
        <p className="rk-t" data-tone="err" style={{ margin: 0 }} data-testid="credentials-error">
          {error}
        </p>
      )}
      {migration && ((migration.created?.length ?? 0) > 0 || (migration.notes?.length ?? 0) > 0) && (
        <div className="rk-card" style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="credentials-migration">
          <strong style={{ fontSize: 13 }}>Created from existing settings</strong>
          {(migration.created ?? []).map((c) => (
            <span key={c.id} style={{ fontSize: 12.5 }}>
              <span className="rk-mono">{c.name}</span> <span style={{ color: 'var(--fg3)' }}>from {c.from.join(', ')}</span>
            </span>
          ))}
          {(migration.notes ?? []).map((n) => (
            <span key={n} style={{ fontSize: 12.5, color: 'var(--warn)' }}>
              {n}
            </span>
          ))}
          <span className="rk-fhint">Rename them as you like. The secrets they were made from stay where they were, no longer read; docs/upgrading.md says how to delete them once these are checked.</span>
          {mayEdit && (
            <button className="rk-btn" data-size="sm" style={{ alignSelf: 'flex-start' }} onClick={() => void dismissMigration().then(load, (e: unknown) => onToast?.(errorText(e)))} data-testid="credentials-migration-dismiss">
              Dismiss
            </button>
          )}
        </div>
      )}
      {soon.length > 0 && (
        <p className="rk-t" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="credentials-expiring">
          {soon.length === 1 ? `${soon[0]!.name} expires within 14 days, or has.` : `${soon.length} credentials expire within 14 days, or have.`} Replace the secret before it does.
        </p>
      )}
      {!view && !error && <p className="rk-fhint">Loading…</p>}
      {view && list.length === 0 && (
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="credentials-empty">
          No credentials yet{mayEdit ? ': add one.' : '; a SuperAdmin adds them.'}
        </p>
      )}
      {list.length > 0 && (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', minWidth: 760, fontSize: 13 }} data-testid="credentials-table">
            <thead>
              <tr>
                {['Name', 'Kind', 'Endpoint', 'Sent as', 'Used by', 'Last test', 'Changed', ''].map((h, i) => (
                  <th key={i} style={th}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {list.map((c) => {
                const exp = expiryText(c.expires)
                return (
                  <tr key={c.id} data-testid="credential-row" data-credential={c.name}>
                    <td style={td}>
                      <span style={{ fontWeight: 600 }}>{c.name}</span>
                      {exp && (
                        <span className="rk-pill" data-tone={exp.tone || undefined} style={{ marginLeft: 6, fontSize: 11 }} data-testid="credential-expiry">
                          {exp.text}
                        </span>
                      )}
                      {c.description && <div style={{ fontSize: 12, color: 'var(--fg3)' }}>{c.description}</div>}
                    </td>
                    <td style={td}>{KIND_LABEL[c.kind]}</td>
                    <td style={{ ...td, overflowWrap: 'anywhere' }} className="rk-mono">
                      {c.endpoint}
                    </td>
                    <td style={td}>{c.how ? sendText(c) : '—'}</td>
                    <td style={td} data-testid="credential-used-by">
                      {c.usedBy.length === 0 ? <span style={{ color: 'var(--fg3)' }}>Nothing</span> : c.usedBy.map((u) => <div key={u.what + (u.where ?? '')}>{u.what}</div>)}
                    </td>
                    <td style={td} data-testid="credential-tested">
                      {c.lastTest ? (
                        <span title={c.lastTest.text} style={{ color: c.lastTest.ok ? undefined : 'var(--err)' }}>
                          {c.lastTest.ok ? 'Answered' : 'Failed'}, {fmtTs(c.lastTest.at)}
                        </span>
                      ) : (
                        <span style={{ color: 'var(--fg3)' }}>Not tested</span>
                      )}
                    </td>
                    <td style={{ ...td, fontSize: 12 }}>{c.changed ? `${c.changedBy ?? ''}, ${fmtTs(c.changed)}` : '—'}</td>
                    <td style={{ ...td, whiteSpace: 'nowrap', textAlign: 'right' }}>
                      {mayEdit && (
                        <span style={{ display: 'inline-flex', gap: 6 }}>
                          <button className="rk-btn" data-size="sm" disabled={testing === c.id} onClick={() => void retest(c)} data-testid="credential-test">
                            {testing === c.id ? 'Testing…' : 'Test'}
                          </button>
                          <button className="rk-btn" data-size="sm" onClick={() => setEditing({ saved: c })} data-testid="credential-edit">
                            Edit
                          </button>
                          <button className="rk-btn" data-size="sm" data-danger="true" onClick={() => setRemoving(c)} data-testid="credential-remove">
                            Remove
                          </button>
                        </span>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {mayEdit && (
        <button className="rk-btn" data-primary="true" style={{ alignSelf: 'flex-start' }} onClick={() => setEditing({})} data-testid="credential-add">
          Add credential
        </button>
      )}
      {editing && (
        <CredentialEditor
          key={editing.saved?.id ?? 'new'}
          saved={editing.saved}
          onClose={() => setEditing(null)}
          onSaved={(c) => {
            setEditing(null)
            onToast?.(`${c.name} saved.`)
            load()
          }}
        />
      )}
      {removing && (
        <RemoveCredential
          credential={removing}
          onCancel={() => setRemoving(null)}
          onRemoved={() => {
            setRemoving(null)
            onToast?.(`${removing.name} removed.`)
            load()
          }}
        />
      )}
    </section>
  )
}

const th: React.CSSProperties = { fontSize: 12, color: 'var(--fg2)', fontWeight: 500, textAlign: 'left', padding: '8px 10px', borderBottom: '1px solid var(--line)' }
const td: React.CSSProperties = { padding: '9px 10px', borderBottom: '1px solid var(--line)', verticalAlign: 'top' }
const fieldLabel: React.CSSProperties = { display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)' }

/** A credential as the editor holds it; the secret's parts are typed, never read. */
interface Draft {
  name: string
  kind: CredentialKind
  endpoint: string
  description: string
  expires: string
  caPEM: string
  method: SendMethod
  sendUser: string
  header: string
  prefix: string
  param: string
  cookie: string
  username: string
  region: string
  hostKey: string
  appId: string
  installationId: string
  tokenURL: string
  clientId: string
  scope: string
  certPEM: string
  secret: CredentialSecret
}

function draftOf(c?: Credential): Draft {
  const sa = c?.sendAs ?? {}
  return {
    name: c?.name ?? '',
    kind: c?.kind ?? 'token',
    endpoint: c?.endpoint ?? '',
    description: c?.description ?? '',
    expires: c?.expires ? c.expires.slice(0, 10) : '',
    caPEM: c?.caPEM ?? '',
    method: sa.method ?? 'bearer',
    sendUser: sa.username ?? '',
    header: sa.header ?? '',
    prefix: sa.prefix ?? '',
    param: sa.param ?? '',
    cookie: sa.cookie ?? '',
    username: c?.username ?? '',
    region: c?.region ?? '',
    hostKey: c?.hostKey ?? '',
    appId: c?.appId ?? '',
    installationId: c?.installationId ?? '',
    tokenURL: c?.tokenURL ?? '',
    clientId: c?.clientId ?? '',
    scope: c?.scope ?? '',
    certPEM: c?.certPEM ?? '',
    secret: {},
  }
}

/** The secret's parts each kind takes: field, label, whether it is optional, whether it is many lines. */
const SECRET_PARTS: Record<CredentialKind, { key: keyof CredentialSecret; label: string; optional?: boolean; multi?: boolean }[]> = {
  token: [{ key: 'token', label: 'Token' }],
  userpass: [{ key: 'password', label: 'Password or token' }],
  accesskey: [
    { key: 'accessKeyId', label: 'Access key ID' },
    { key: 'secretAccessKey', label: 'Secret access key' },
    { key: 'sessionToken', label: 'Session token', optional: true },
  ],
  ssh: [
    { key: 'privateKey', label: 'Private key (OpenSSH or PEM)', multi: true },
    { key: 'passphrase', label: 'Passphrase', optional: true },
  ],
  githubapp: [{ key: 'privateKey', label: 'The App’s private key (.pem)', multi: true }],
  clientcert: [{ key: 'keyPEM', label: 'The certificate’s private key (PEM)', multi: true }],
  oauth2: [{ key: 'clientSecret', label: 'Client secret' }],
  none: [],
}

/** The input a draft sends: the secret only when one of its parts was typed. */
export function inputOf(d: Draft): CredentialInput {
  const typed = Object.values(d.secret).some((v) => !!v)
  const input: CredentialInput = {
    name: d.name.trim(),
    kind: d.kind,
    endpoint: d.endpoint.trim(),
    description: d.description.trim(),
    ...(d.expires ? { expires: `${d.expires}T00:00:00Z` } : {}),
    ...(d.caPEM.trim() ? { caPEM: d.caPEM } : {}),
  }
  if (sendsHTTP(d.kind)) {
    input.sendAs = { method: d.method }
    if (d.method === 'basic') input.sendAs.username = d.sendUser.trim()
    if (d.method === 'header') Object.assign(input.sendAs, { header: d.header.trim(), prefix: d.prefix })
    if (d.method === 'query') input.sendAs.param = d.param.trim()
    if (d.method === 'cookie') input.sendAs.cookie = d.cookie.trim()
  }
  if (d.kind === 'userpass') input.username = d.username.trim()
  if (d.kind === 'accesskey' && d.region.trim()) input.region = d.region.trim()
  if (d.kind === 'ssh') input.hostKey = d.hostKey.trim()
  if (d.kind === 'githubapp') Object.assign(input, { appId: d.appId.trim(), installationId: d.installationId.trim() })
  if (d.kind === 'oauth2') Object.assign(input, { tokenURL: d.tokenURL.trim(), clientId: d.clientId.trim(), scope: d.scope.trim() })
  if (d.kind === 'clientcert') input.certPEM = d.certPEM
  if (typed) input.secret = d.secret
  return input
}

/** Adds or changes a credential, in a drawer with its own Save; a saved one is tested from it (CR-05, CR-07). */
function CredentialEditor({ saved, onClose, onSaved }: { saved?: Credential; onClose: () => void; onSaved: (c: Credential) => void }) {
  const [d, setD] = useState<Draft>(() => draftOf(saved))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [target, setTarget] = useState('')
  const [test, setTest] = useState<CredentialTest | null>(null)
  const set = (p: Partial<Draft>) => setD((x) => ({ ...x, ...p }))
  const setSecret = (k: keyof CredentialSecret, v: string) => setD((x) => ({ ...x, secret: { ...x.secret, [k]: v } }))
  const kindChanged = !!saved && d.kind !== saved.kind
  const parts = SECRET_PARTS[d.kind]
  const typed = parts.every((p) => p.optional || !!d.secret[p.key])
  const ready = d.name.trim() !== '' && d.endpoint.trim() !== '' && (!saved || kindChanged ? typed || parts.length === 0 : true)
  const save = async () => {
    setBusy(true)
    setError('')
    try {
      onSaved(saved ? await saveCredential(saved.id, inputOf(d)) : await createCredential(inputOf(d)))
    } catch (e) {
      setError(errorText(e))
      setBusy(false)
    }
  }
  const runTest = async () => {
    if (!saved) return
    setBusy(true)
    setTest(null)
    try {
      setTest(await testCredential(saved.id, target.trim()))
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const text = (label: string, value: string, on: (v: string) => void, testid: string, placeholder = '', hint?: string) => (
    <label style={fieldLabel}>
      {label}
      <span className="rk-field">
        <input value={value} onChange={(e) => on(e.target.value)} placeholder={placeholder} autoComplete="off" data-testid={testid} />
      </span>
      {hint && <span className="rk-fhint">{hint}</span>}
    </label>
  )
  const area = (label: string, value: string, on: (v: string) => void, testid: string, placeholder = '') => (
    <label style={{ ...fieldLabel, gridColumn: '1 / -1' }}>
      {label}
      <textarea className="rk-mono" rows={4} value={value} onChange={(e) => on(e.target.value)} placeholder={placeholder} spellCheck={false} style={{ width: '100%', fontSize: 12 }} data-testid={testid} />
    </label>
  )
  return (
    <Drawer
      label="Credential"
      kicker="Credential"
      title={saved ? saved.name : 'New credential'}
      copy={false}
      width={720}
      onClose={onClose}
      testId="credential-drawer"
      footer={
        <>
          <button className="rk-btn" data-primary="true" disabled={busy || !ready} onClick={() => void save()} data-testid="credential-save">
            {busy ? 'Saving…' : saved ? 'Save changes' : 'Add credential'}
          </button>
          <button className="rk-btn" disabled={busy} onClick={onClose}>
            Cancel
          </button>
        </>
      }
    >
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: '10px 14px' }}>
        {text('Name', d.name, (v) => set({ name: v }), 'credential-name', 'GitHub ucesys (read only)')}
        <label style={fieldLabel}>
          Kind
          <span className="rk-field">
            <select value={d.kind} onChange={(e) => set({ kind: e.target.value as CredentialKind, secret: {} })} data-testid="credential-kind">
              {(Object.keys(KIND_LABEL) as CredentialKind[]).map((k) => (
                <option key={k} value={k}>
                  {KIND_LABEL[k]}
                </option>
              ))}
            </select>
          </span>
        </label>
        <div style={{ gridColumn: '1 / -1' }}>
          {text('Endpoint', d.endpoint, (v) => set({ endpoint: v }), 'credential-endpoint', 'https://github.com/ucesys', 'The address, or the start of the addresses, it is sent to: never anywhere else. Give the narrowest that serves.')}
        </div>
        <div style={{ gridColumn: '1 / -1' }}>{text('Description', d.description, (v) => set({ description: v }), 'credential-description', 'What it may do, who owns it')}</div>
        <label style={fieldLabel}>
          Expires
          <span className="rk-field">
            <input type="date" value={d.expires} onChange={(e) => set({ expires: e.target.value })} data-testid="credential-expires" />
          </span>
          <span className="rk-fhint">Warned about 14 days before.</span>
        </label>
        {d.kind === 'userpass' && text('User name', d.username, (v) => set({ username: v }), 'credential-username')}
        {d.kind === 'accesskey' && text('Region (optional)', d.region, (v) => set({ region: v }), 'credential-region', 'eu-west-1')}
        {d.kind === 'githubapp' && (
          <>
            {text('App ID', d.appId, (v) => set({ appId: v }), 'credential-app-id', '123456')}
            {text('Installation ID', d.installationId, (v) => set({ installationId: v }), 'credential-installation-id', 'in the installation’s settings address')}
          </>
        )}
        {d.kind === 'oauth2' && (
          <>
            <div style={{ gridColumn: '1 / -1' }}>{text('Token URL', d.tokenURL, (v) => set({ tokenURL: v }), 'credential-token-url', 'https://auth.example.com/oauth/token')}</div>
            {text('Client ID', d.clientId, (v) => set({ clientId: v }), 'credential-client-id')}
            {text('Scope (optional)', d.scope, (v) => set({ scope: v }), 'credential-scope')}
          </>
        )}
        {d.kind === 'ssh' && area('Host key (required: a known_hosts line, or ssh-keyscan HOST’s output)', d.hostKey, (v) => set({ hostKey: v }), 'credential-host-key', 'github.com ssh-ed25519 AAAA…')}
        {d.kind === 'clientcert' && area('Certificate (PEM)', d.certPEM, (v) => set({ certPEM: v }), 'credential-cert', '-----BEGIN CERTIFICATE-----')}
        {sendsHTTP(d.kind) && (
          <div style={{ ...fieldLabel, gridColumn: '1 / -1' }}>
            Send as
            <div className="rk-seg" role="group" aria-label="Send as" style={{ alignSelf: 'flex-start', flexWrap: 'wrap' }}>
              {(Object.keys(METHOD_LABEL) as SendMethod[]).map((m) => (
                <button key={m} type="button" aria-pressed={d.method === m} onClick={() => set({ method: m })} data-testid={`credential-send-${m}`}>
                  {METHOD_LABEL[m]}
                </button>
              ))}
            </div>
            {d.method === 'basic' && text('User sent with the token', d.sendUser, (v) => set({ sendUser: v }), 'credential-basic-user', 'x-access-token', 'x-access-token for GitHub, oauth2 for GitLab; any user for most others.')}
            {d.method === 'header' && (
              <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
                {text('Header', d.header, (v) => set({ header: v }), 'credential-header-name', 'X-API-Key')}
                {text('Prefix (optional)', d.prefix, (v) => set({ prefix: v }), 'credential-header-prefix', 'token ')}
              </div>
            )}
            {d.method === 'query' && (
              <>
                {text('Query parameter', d.param, (v) => set({ param: v }), 'credential-query-param', 'access_token')}
                <span className="rk-t" data-tone="warn" style={{ fontSize: 12 }} data-testid="credential-query-warning">
                  A token in the address ends up in the far end’s logs, and in any proxy’s: use it only where nothing else is accepted.
                </span>
              </>
            )}
            {d.method === 'cookie' && text('Cookie', d.cookie, (v) => set({ cookie: v }), 'credential-cookie-name', 'session')}
          </div>
        )}
        {parts.map((p) =>
          p.multi ? (
            <label key={p.key} style={{ ...fieldLabel, gridColumn: '1 / -1' }}>
              {p.label}
              {p.optional ? ' (optional)' : ''}
              <textarea
                className="rk-mono"
                rows={4}
                value={d.secret[p.key] ?? ''}
                onChange={(e) => setSecret(p.key, e.target.value)}
                placeholder={saved && !kindChanged ? 'kept; paste to replace' : ''}
                spellCheck={false}
                autoComplete="off"
                style={{ width: '100%', fontSize: 12 }}
                data-testid={`credential-secret-${p.key}`}
              />
            </label>
          ) : (
            <label key={p.key} style={fieldLabel}>
              {p.label}
              {p.optional ? ' (optional)' : ''}
              <span className="rk-field">
                <input
                  type="password"
                  autoComplete="new-password"
                  value={d.secret[p.key] ?? ''}
                  onChange={(e) => setSecret(p.key, e.target.value)}
                  placeholder={saved && !kindChanged && saved.set?.includes(p.key) ? '•••••••• kept; type to replace' : ''}
                  data-testid={`credential-secret-${p.key}`}
                />
              </span>
            </label>
          ),
        )}
        {parts.length > 0 && (
          <span className="rk-fhint" style={{ gridColumn: '1 / -1' }}>
            {saved && !kindChanged ? 'Typed, the secret replaces the kept one for every use at once; left empty, the kept one stays. ' : ''}Kept in a Kubernetes Secret, never shown again, never sent to a model.
          </span>
        )}
        {area('Private CA (optional, PEM)', d.caPEM, (v) => set({ caPEM: v }), 'credential-ca', '-----BEGIN CERTIFICATE-----')}
      </div>
      {saved && (
        <div className="rk-card" style={{ marginTop: 14, padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="credential-test-box">
          <strong style={{ fontSize: 13 }}>Test</strong>
          <span className="rk-fhint">
            Against the endpoint, or an address, repository (name.git), bucket or space under it. It says how the credential was sent, never its value, and what the far end answered. The saved credential is tested: save changes first.
          </span>
          <div style={{ display: 'flex', gap: 8 }}>
            <span className="rk-field" style={{ flex: '1 1 auto' }}>
              <input value={target} onChange={(e) => setTarget(e.target.value)} placeholder={saved.endpoint} data-testid="credential-test-target" />
            </span>
            <button className="rk-btn" disabled={busy} onClick={() => void runTest()} data-testid="credential-test-run">
              Test
            </button>
          </div>
          {test && (
            <p className="rk-t" data-tone={test.ok ? 'ok' : 'err'} style={{ margin: 0, fontSize: 12.5 }} data-testid="credential-test-result">
              {test.ok ? '✓ ' : '✗ '}
              {test.text}
              {test.suggest && ` Try Send as ${METHOD_LABEL[test.suggest]}.`}
            </p>
          )}
        </div>
      )}
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: '10px 0 0', fontSize: 12.5 }} data-testid="credential-error">
          {error}
        </p>
      )}
    </Drawer>
  )
}

function RemoveCredential({ credential, onCancel, onRemoved }: { credential: Credential; onCancel: () => void; onRemoved: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const remove = async () => {
    setBusy(true)
    try {
      await deleteCredential(credential.id)
      onRemoved()
    } catch (e) {
      setError(errorText(e))
      setBusy(false)
    }
  }
  return (
    <ConfirmDialog title={`Remove ${credential.name}?`} confirm="Remove" danger busy={busy} error={error} onCancel={onCancel} onConfirm={() => void remove()}>
      <p style={{ margin: 0 }}>{credential.usedBy.length > 0 ? `It is used by ${credential.usedBy.map((u) => u.what).join(', ')}: choose another credential there first.` : 'Its secret is deleted with it. Nothing uses it.'}</p>
    </ConfirmDialog>
  )
}
