import { useEffect, useRef, useState } from 'react'
import { AUTH_LABELS, CA_LABELS, ClusterError, credText, ENVS, removeCluster, saveCluster, testDraft, type AccessMode, type AuthMethod, type CAMode, type ClusterSummary, type Draft, type Env, type TestResult } from '../api/clusters'
import { credentialNote, kubeconfigCA, kubeContext, NAME_RE, parseKubeconfig, rbacYAML, URL_RE } from '../lib/credentials'
import { Card, Drawer } from './Drawer'
import { CopyIcon, LockIcon, ShieldIcon, WarnIcon } from './Icons'
import { TestSteps } from './TestSteps'

interface Props {
  /** The connection edited, or null to add one. */
  orig: ClusterSummary | null
  now: number
  onClose: () => void
  onSaved: (c: ClusterSummary, added: boolean) => void
  onRemoved: (c: ClusterSummary) => void
}

interface Form {
  name: string
  env: Env
  description: string
  server: string
  ca: CAMode
  auth: AuthMethod
  mode: AccessMode
  token: string
  kubeconfig: string
  context: string
  certPem: string
  keyPem: string
  caPem: string
  issuer: string
  clientID: string
  clientSecret: string
  scopes: string
  /** Replace the saved CA certificate, or the saved credentials. */
  caAsk: boolean
  credAsk: boolean
}

const AUTHS: AuthMethod[] = ['token', 'kubeconfig', 'oidc', 'cert']
const CAS: CAMode[] = ['pem', 'system', 'skip']

function initial(orig: ClusterSummary | null): Form {
  return {
    name: orig?.name ?? '',
    env: orig?.env ?? 'development',
    description: orig?.description ?? '',
    server: orig?.server ?? '',
    ca: orig?.ca ?? 'pem',
    auth: orig && AUTHS.includes(orig.auth) ? orig.auth : 'token',
    mode: orig?.mode ?? 'read',
    token: '',
    kubeconfig: '',
    context: '',
    certPem: '',
    keyPem: '',
    caPem: '',
    issuer: '',
    clientID: '',
    clientSecret: '',
    scopes: '',
    caAsk: false,
    credAsk: false,
  }
}

/** The draft as the server takes it: credential fields only when they are being replaced. */
export function toDraft(f: Form, orig: ClusterSummary | null): Draft {
  const needCred = !orig || f.credAsk || orig.auth !== f.auth
  const needCA = f.ca === 'pem' && (!orig || f.caAsk || orig.ca !== 'pem')
  const d: Draft = { name: f.name.trim(), env: f.env, description: f.description.trim(), server: f.server.trim(), ca: f.ca, auth: f.auth, mode: f.mode }
  if (orig) d.id = orig.id
  if (needCA && f.caPem.trim()) d.caPem = f.caPem.trim()
  if (needCred) {
    switch (f.auth) {
      case 'token':
        if (f.token.trim()) d.token = f.token.trim()
        break
      case 'kubeconfig':
        if (f.kubeconfig.trim()) d.kubeconfig = f.kubeconfig
        if (f.context.trim()) d.context = f.context.trim()
        break
      case 'cert':
        if (f.certPem.trim()) d.certPem = f.certPem.trim()
        if (f.keyPem.trim()) d.keyPem = f.keyPem.trim()
        break
      case 'oidc':
        if (f.issuer.trim()) d.issuer = f.issuer.trim()
        if (f.clientID.trim()) d.clientID = f.clientID.trim()
        if (f.clientSecret.trim()) d.clientSecret = f.clientSecret.trim()
        if (f.scopes.trim()) d.scopes = f.scopes.trim()
        break
      default:
        break
    }
  }
  return d
}

/** What the form itself can tell is wrong, before the server is asked; "" when nothing. */
export function formProblem(f: Form, orig: ClusterSummary | null): { field: string; msg: string } | null {
  const name = f.name.trim()
  if (!name) return { field: 'name', msg: 'Give the cluster a name.' }
  if (!NAME_RE.test(name)) return { field: 'name', msg: 'Use letters, digits, dots, dashes or underscores, up to 63, starting and ending with a letter or digit.' }
  if (orig?.local) return null
  if (!URL_RE.test(f.server.trim())) return { field: 'server', msg: 'The API server address starts with https://, for example https://k8s-api.example.internal:6443.' }
  const needCA = f.ca === 'pem' && (!orig || f.caAsk || orig.ca !== 'pem')
  if (needCA && !f.caPem.trim()) return { field: 'caPem', msg: 'Paste the cluster’s CA certificate (PEM), or choose the system trust store.' }
  const needCred = !orig || f.credAsk || orig.auth !== f.auth
  if (needCred) {
    const note = credentialNote(toDraft({ ...f, credAsk: true }, null))
    if (!note) {
      const what = f.auth === 'token' ? 'Paste the service account token.' : f.auth === 'kubeconfig' ? 'Paste a kubeconfig.' : f.auth === 'cert' ? 'Paste the client certificate and its key.' : 'Give the issuer URL and the client ID.'
      return { field: f.auth === 'token' ? 'token' : f.auth === 'kubeconfig' ? 'kubeconfig' : f.auth === 'cert' ? 'certPem' : 'issuer', msg: what }
    }
    if (note.tone === 'err') return { field: f.auth === 'token' ? 'token' : f.auth === 'kubeconfig' ? 'kubeconfig' : f.auth === 'cert' ? 'certPem' : 'issuer', msg: note.text }
  }
  return null
}

/** A labelled text field of the form. */
function Field({ label, hint, children }: { label: string; hint?: React.ReactNode; children: React.ReactNode }) {
  return (
    <label className="rk-flabel">
      {label}
      {children}
      {hint && <span className="rk-fhint">{hint}</span>}
    </label>
  )
}

function Seg<T extends string>({ label, options, value, onChange, disabled }: { label: string; options: { id: T; label: string }[]; value: T; onChange: (v: T) => void; disabled?: boolean }) {
  return (
    <div className="rk-seg" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.id} type="button" aria-pressed={value === o.id} onClick={() => onChange(o.id)} disabled={disabled} data-value={o.id}>
          {o.label}
        </button>
      ))}
    </div>
  )
}

/**
 * The connection editor (P3-05): a drawer with the cluster's name and
 * environment, its API server and CA, how the console signs in, what it
 * may do, and a test of the connection before saving (P3-06). Credentials
 * are typed once; a saved one shows only what it says about itself.
 */
export function ClusterEditor({ orig, now, onClose, onSaved, onRemoved }: Props) {
  const [f, setF] = useState<Form>(() => initial(orig))
  const [saving, setSaving] = useState(false)
  const [saveErr, setSaveErr] = useState<{ field?: string; msg: string } | null>(null)
  const [test, setTest] = useState<TestResult | null>(null)
  const [testing, setTesting] = useState(false)
  const [showRbac, setShowRbac] = useState(false)
  const [copied, setCopied] = useState(false)
  const [confirmDel, setConfirmDel] = useState(false)
  const [delText, setDelText] = useState('')
  const [removing, setRemoving] = useState(false)
  const nameRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    nameRef.current?.focus()
  }, [])

  const local = orig?.local ?? false
  const needCred = !orig || f.credAsk || orig.auth !== f.auth
  const needCA = f.ca === 'pem' && (!orig || f.caAsk || orig.ca !== 'pem')
  const isProd = orig?.env === 'production'
  const note = needCred ? credentialNote(toDraft(f, orig), now) : null
  const kc = needCred && f.auth === 'kubeconfig' && f.kubeconfig.trim() ? parseKubeconfig(f.kubeconfig) : null

  // Any change to the connection makes the last test stale.
  // Counts form changes, so a test still running when the form changes
  // does not show its result against values it never tried.
  const edits = useRef(0)
  const upd = (patch: Partial<Form>, keepTest = false) => {
    setF((cur) => ({ ...cur, ...patch }))
    setSaveErr(null)
    if (!keepTest) {
      edits.current++
      setTest(null)
    }
  }
  const onKubeconfig = (text: string) => {
    const k = parseKubeconfig(text)
    const patch: Partial<Form> = { kubeconfig: text }
    const ctx = kubeContext(k, '')
    if (ctx.ctx && !k.contexts.some((x) => x.name === f.context)) patch.context = ctx.name
    if (ctx.cluster?.server && !f.server.trim()) patch.server = ctx.cluster.server
    if (f.ca === 'pem' && !f.caPem.trim() && needCA) {
      const pem = kubeconfigCA(k, patch.context ?? f.context)
      if (pem) patch.caPem = pem
    }
    upd(patch)
  }

  const save = async () => {
    const problem = formProblem(f, orig)
    if (problem) {
      setSaveErr(problem)
      return
    }
    setSaving(true)
    try {
      const sum = await saveCluster(toDraft(f, orig), orig?.id)
      onSaved(sum, !orig)
    } catch (e) {
      setSaveErr(e instanceof ClusterError ? { field: e.field, msg: e.message } : { msg: String(e) })
    } finally {
      setSaving(false)
    }
  }
  const runTest = async () => {
    const problem = formProblem(f, orig)
    if (problem) {
      setSaveErr(problem)
      return
    }
    setTesting(true)
    setTest(null)
    const at = edits.current
    try {
      const res = await testDraft(toDraft(f, orig))
      if (edits.current === at) setTest(res)
    } catch (e) {
      if (edits.current === at) setSaveErr(e instanceof ClusterError ? { field: e.field, msg: e.message } : { msg: String(e) })
    } finally {
      setTesting(false)
    }
  }
  const remove = async () => {
    if (!orig) return
    if (!confirmDel) {
      setConfirmDel(true)
      setDelText('')
      return
    }
    if (isProd && delText.trim() !== orig.name) return
    setRemoving(true)
    try {
      await removeCluster(orig.id)
      onRemoved(orig)
    } catch (e) {
      setSaveErr({ msg: e instanceof Error ? e.message : String(e) })
      setRemoving(false)
    }
  }
  const copyRbac = async () => {
    try {
      await navigator.clipboard.writeText(rbacYAML(f.mode, true))
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // The clipboard is not always available; the text is on screen.
    }
  }
  const bad = (field: string) => (saveErr?.field === field ? { 'aria-invalid': true as const, 'data-invalid': 'true' } : {})

  const title = orig ? orig.name : f.name.trim() || 'New cluster'
  const footer = (
    <>
      <button className="rk-btn" data-on="true" onClick={() => void save()} disabled={saving || removing} style={{ height: 34, padding: '0 16px' }} data-testid="cluster-save">
        {saving ? 'Saving…' : orig ? 'Save changes' : 'Add cluster'}
      </button>
      <button className="rk-btn" onClick={onClose} style={{ height: 34 }}>
        Cancel
      </button>
      {saveErr && (
        <span className="rk-t" data-tone="err" role="alert" style={{ flex: '1 1 220px', fontSize: 12 }} data-testid="cluster-error">
          {saveErr.msg}
        </span>
      )}
      <span style={{ flex: '1 1 auto' }} />
      {orig && !local && (
        <>
          {confirmDel && isProd && (
            <label className="rk-field" style={{ width: 190, height: 34 }}>
              <span className="rk-sr">Type the cluster name to confirm</span>
              <input type="text" value={delText} onChange={(e) => setDelText(e.target.value)} placeholder={`Type ${orig.name}`} spellCheck={false} autoComplete="off" style={{ fontFamily: 'var(--mono)' }} data-testid="cluster-remove-name" />
            </label>
          )}
          <button className="rk-btn" onClick={() => void remove()} disabled={removing || (confirmDel && isProd && delText.trim() !== orig.name)} title="Removes the connection from this console; nothing changes in the cluster" style={{ height: 34, color: 'var(--err)' }} data-testid="cluster-remove">
            {removing ? 'Removing…' : !confirmDel ? 'Remove cluster' : isProd ? 'Remove' : 'Click again to remove'}
          </button>
          {confirmDel && <span style={{ flex: '1 1 100%', fontSize: 11.5, color: 'var(--fg3)', textAlign: 'right' }}>Removes the connection and its stored credentials from this console. Nothing changes in the cluster.</span>}
        </>
      )}
    </>
  )

  return (
    <Drawer label="Cluster connection" kicker={orig ? 'Cluster connection' : 'Add a cluster'} title={title} onClose={onClose} width={680} footer={footer} testId="cluster-editor" copy={false}>
      <Card title="Cluster">
        <Field label="Name" hint="Shown in the console only; the cluster doesn’t know this name.">
          <span className="rk-field">
            <input ref={nameRef} type="text" value={f.name} onChange={(e) => upd({ name: e.target.value }, true)} spellCheck={false} autoComplete="off" placeholder="For example: lakehouse-qa" style={{ fontFamily: 'var(--mono)' }} data-testid="cluster-name" {...bad('name')} />
          </span>
        </Field>
        <div className="rk-flabel">
          Environment
          <Seg label="Environment" options={ENVS} value={f.env} onChange={(env) => upd({ env }, true)} />
          {f.env === 'production' && <span className="rk-fhint">Removing a production cluster asks you to type its name.</span>}
        </div>
        <Field label="Description">
          <span className="rk-field">
            <input type="text" value={f.description} onChange={(e) => upd({ description: e.target.value }, true)} placeholder="What runs there (optional)" data-testid="cluster-description" />
          </span>
        </Field>
      </Card>
      {local && orig ? (
        <Card title="Connection">
          <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>This is the cluster the console runs in, reached with the console’s own ServiceAccount. Its address and credentials come from the installation and cannot be changed here.</span>
          <dl className="rk-kv">
            <dt>API server</dt>
            <dd className="rk-mono">{orig.server}</dd>
            <dt>Sign-in</dt>
            <dd>{credText(orig, now)}</dd>
            <dt>Changes</dt>
            <dd data-testid="cluster-local-mode">{orig.mode === 'write' ? 'Read and change: Watson runs the changes people approve.' : 'Read only. To let Watson run approved changes, install the chart with localCluster.mode=write.'}</dd>
          </dl>
        </Card>
      ) : (
        <>
          <Card title="Connection">
            <Field label="API server" hint="The server: line of a kubeconfig, or the control plane’s load balancer.">
              <span className="rk-field">
                <input type="url" value={f.server} onChange={(e) => upd({ server: e.target.value })} spellCheck={false} autoComplete="off" placeholder="https://k8s-api.example.internal:6443" style={{ fontFamily: 'var(--mono)' }} data-testid="cluster-server" {...bad('server')} />
              </span>
            </Field>
            <div className="rk-flabel">
              Certificate authority
              <Seg label="Certificate authority" options={CAS.map((id) => ({ id, label: CA_LABELS[id] }))} value={f.ca} onChange={(ca) => upd({ ca })} />
            </div>
            {f.ca === 'pem' && !needCA && orig && (
              <div className="rk-saved" data-testid="ca-saved">
                <ShieldIcon />
                <span style={{ flex: '1 1 auto', minWidth: 0 }}>Saved{orig.caInfo ? ': ' + orig.caInfo : ''}</span>
                <button className="rk-link" onClick={() => upd({ caAsk: true })} style={{ fontSize: 12 }}>
                  Replace
                </button>
              </div>
            )}
            {needCA && (
              <Field
                label="CA certificate (PEM)"
                hint={
                  <>
                    /etc/kubernetes/pki/ca.crt on a control-plane node, or certificate-authority-data from a kubeconfig (base64-decoded).
                    {orig && orig.ca === 'pem' && f.caAsk && (
                      <>
                        {' '}
                        <button className="rk-link" onClick={() => upd({ caAsk: false, caPem: '' })} style={{ fontSize: 11.5 }}>
                          Keep the saved one
                        </button>
                      </>
                    )}
                  </>
                }
              >
                <textarea className="rk-area" value={f.caPem} onChange={(e) => upd({ caPem: e.target.value })} spellCheck={false} placeholder="-----BEGIN CERTIFICATE-----" data-testid="cluster-ca-pem" {...bad('caPem')} />
              </Field>
            )}
            {f.ca === 'skip' && (
              <div className="rk-callout" data-tone="warn">
                <WarnIcon />
                <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>Without verification, anything between the console and the API server could read the console’s credentials. Use this only for a throwaway test cluster.</span>
              </div>
            )}
            {f.ca === 'system' && <span className="rk-fhint">Works when the API server’s certificate comes from a CA the console’s image already trusts. Clusters set up with kubeadm sign it with their own CA, which it doesn’t.</span>}
          </Card>
          <Card title="Sign-in">
            <Seg label="How the console signs in" options={AUTHS.map((id) => ({ id, label: AUTH_LABELS[id] }))} value={f.auth} onChange={(auth) => upd({ auth })} />
            {!needCred && orig && (
              <div className="rk-saved" data-testid="cred-saved">
                <LockIcon />
                <span style={{ flex: '1 1 auto', minWidth: 0, overflowWrap: 'anywhere' }}>{credText(orig, now)}</span>
                <button className="rk-link" onClick={() => upd({ credAsk: true })} style={{ fontSize: 12 }} data-testid="cred-replace">
                  Replace
                </button>
              </div>
            )}
            {needCred && f.auth === 'token' && (
              <Field label="Token" hint="kubectl -n kube-system create token cluster-console --duration=8760h">
                <span className="rk-field">
                  <input type="password" value={f.token} onChange={(e) => upd({ token: e.target.value })} spellCheck={false} autoComplete="off" placeholder="eyJhbGciOiJSUzI1NiIs…" style={{ fontFamily: 'var(--mono)' }} data-testid="cluster-token" {...bad('token')} />
                </span>
              </Field>
            )}
            {needCred && f.auth === 'kubeconfig' && (
              <>
                <Field label="Kubeconfig" hint="Pasting one fills in the API server, the context and its CA certificate.">
                  <textarea className="rk-area" value={f.kubeconfig} onChange={(e) => onKubeconfig(e.target.value)} spellCheck={false} placeholder={'apiVersion: v1\nkind: Config\nclusters: …'} style={{ minHeight: 120 }} data-testid="cluster-kubeconfig" {...bad('kubeconfig')} />
                </Field>
                {kc && kc.contexts.length > 1 && (
                  <div className="rk-flabel">
                    Context
                    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                      {kc.contexts.map((x) => (
                        <button key={x.name} className="rk-fchip" aria-pressed={(f.context || kubeContext(kc, '').name) === x.name} onClick={() => upd({ context: x.name ?? '' })} style={{ fontFamily: 'var(--mono)' }} data-testid="cluster-context">
                          {x.name}
                        </button>
                      ))}
                    </div>
                  </div>
                )}
              </>
            )}
            {needCred && f.auth === 'oidc' && (
              <>
                <Field label="Issuer URL">
                  <span className="rk-field">
                    <input type="url" value={f.issuer} onChange={(e) => upd({ issuer: e.target.value })} spellCheck={false} placeholder="https://login.corp.example/realms/data" style={{ fontFamily: 'var(--mono)' }} {...bad('issuer')} />
                  </span>
                </Field>
                <Field label="Client ID" hint="The API server’s --oidc-client-id.">
                  <span className="rk-field">
                    <input type="text" value={f.clientID} onChange={(e) => upd({ clientID: e.target.value })} spellCheck={false} placeholder="kubernetes" {...bad('clientID')} />
                  </span>
                </Field>
                <Field label="Client secret" hint="Only when the provider requires one for this client.">
                  <span className="rk-field">
                    <input type="password" value={f.clientSecret} onChange={(e) => upd({ clientSecret: e.target.value })} spellCheck={false} autoComplete="off" />
                  </span>
                </Field>
                <Field label="Extra scopes">
                  <span className="rk-field">
                    <input type="text" value={f.scopes} onChange={(e) => upd({ scopes: e.target.value })} spellCheck={false} placeholder="groups email (optional)" />
                  </span>
                </Field>
              </>
            )}
            {needCred && f.auth === 'cert' && (
              <>
                <Field label="Client certificate (PEM)">
                  <textarea className="rk-area" value={f.certPem} onChange={(e) => upd({ certPem: e.target.value })} spellCheck={false} placeholder="-----BEGIN CERTIFICATE-----" data-testid="cluster-cert-pem" {...bad('certPem')} />
                </Field>
                <Field label="Private key (PEM)">
                  <textarea className="rk-area rk-secret" value={f.keyPem} onChange={(e) => upd({ keyPem: e.target.value })} spellCheck={false} placeholder="-----BEGIN PRIVATE KEY-----" data-testid="cluster-key-pem" {...bad('keyPem')} />
                </Field>
              </>
            )}
            {note && (
              <span className="rk-t" data-tone={note.tone} style={{ display: 'inline-flex', alignItems: 'flex-start', gap: 6, fontSize: 12 }} data-testid="cred-note">
                <span className="rk-dot" style={{ marginTop: 5 }} />
                <span>{note.text}</span>
              </span>
            )}
            {orig && needCred && orig.auth === f.auth && f.credAsk && (
              <button className="rk-link" onClick={() => upd({ credAsk: false, token: '', kubeconfig: '', certPem: '', keyPem: '', issuer: '', clientID: '', clientSecret: '', scopes: '' })} style={{ alignSelf: 'flex-start', fontSize: 12 }}>
                Keep the saved credentials
              </button>
            )}
            {f.auth === 'oidc' && <span className="rk-fhint">With OIDC the console signs in as itself with a refresh token it renews; each person’s own access is then checked by impersonation, as with the other methods. This method is not available yet: choose a token, a kubeconfig or a certificate.</span>}
          </Card>
          <Card title="What the console may do">
            <Seg
              label="What the console may do"
              options={[
                { id: 'read' as AccessMode, label: 'Read only' },
                { id: 'write' as AccessMode, label: 'Read and change' },
              ]}
              value={f.mode}
              onChange={(mode) => upd({ mode })}
            />
            <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>{f.mode === 'write' ? 'Watson runs the changes people approve: cordon and drain, scale, rollout restarts and undos, pod deletes, labels, annotations and Helm rollbacks. With sign-in on, each runs as the person who approves it, so their own RBAC decides.' : 'The console only reads. Watson refuses anything that would change the cluster before it is sent.'}</span>
            {f.auth !== 'oidc' && (
              <>
                <button className="rk-link" aria-expanded={showRbac} onClick={() => setShowRbac(!showRbac)} style={{ alignSelf: 'flex-start', fontSize: 12 }} data-testid="rbac-toggle">
                  {showRbac ? 'Hide the RBAC' : 'Show the RBAC the console’s account needs'}
                </button>
                {showRbac && (
                  <div className="rk-cblk" data-wrap="false">
                    <pre data-testid="rbac-yaml">{rbacYAML(f.mode, true)}</pre>
                    <div>
                      <button className="rk-btn" onClick={() => void copyRbac()}>
                        <CopyIcon />
                        {copied ? 'Copied' : 'Copy'}
                      </button>
                    </div>
                  </div>
                )}
              </>
            )}
          </Card>
          <Card title="Test the connection" aside={<button className="rk-btn" onClick={() => void runTest()} disabled={testing} style={{ height: 32 }} data-testid="cluster-test">{testing ? 'Testing…' : test ? 'Test again' : 'Test connection'}</button>}>
            {(test || testing) && <TestSteps result={test} running={testing} />}
            <span className="rk-fhint">The console connects from where it runs: the address must resolve and answer from inside its cluster, not only from your machine.</span>
          </Card>
        </>
      )}
      <p style={{ margin: 0, fontSize: 11.5, color: 'var(--fg3)' }}>Connections are kept in this console, in Secrets of its own namespace, not in the cluster. A token, key or kubeconfig is never shown again once saved, only what it says about itself.</p>
    </Drawer>
  )
}
