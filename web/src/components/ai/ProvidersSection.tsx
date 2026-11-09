import { useEffect, useState } from 'react'
import { addProvider, fetchAI, fetchModelsFor, kindLabel, removeProvider, removeProviderKey, saveProvider, saveProviderKey, testProvider, tokensText, tryProvider, type AIOverview, type AIProvider, type ProviderTestResult, type ProviderView } from '../../api/ai'
import type { ProviderAuth } from '../../api/sherlock'
import { fmtTs } from '../../lib/format'
import { Drawer } from '../Drawer'
import { Select } from '../Select'
import { AUTHS, defaultAuth, ENDPOINT_HINT, Field, headersText, parseHeaders, PRESETS, presetOf } from '../SherlockSettings'
import { ModelPicker } from './ModelPicker'

/**
 * Settings › AI › Providers (Phase 12): where the agents' models come from.
 * The first is the provider Sherlock had; any number may be added, each
 * with a credential kept in a Secret of its own and never shown again, and
 * a Test that sends it one short request.
 */
export function ProvidersSection({ onToast }: { onToast?: (msg: string) => void }) {
  const [ov, setOv] = useState<AIOverview | null>(null)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState<ProviderView | 'new' | null>(null)
  useEffect(() => {
    fetchAI()
      .then(setOv)
      .catch((e: Error) => setError(e.message))
  }, [])
  if (!ov) return <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>{error || 'Loading the providers…'}</p>
  return (
    <>
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 12, flexWrap: 'wrap' }}>
        <p className="rk-fhint" style={{ margin: 0, flex: '1 1 320px' }}>
          Where agents’ models come from. A credential is kept in a Secret in the console’s namespace and is never shown again.
        </p>
        <button className="rk-btn" onClick={() => setEditing('new')} data-testid="provider-add">
          Add provider…
        </button>
      </div>
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
          {error}
        </p>
      )}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="providers">
        {ov.providers.map((p) => (
          <ProviderCard key={p.id} p={p} onEdit={() => setEditing(p)} onChange={setOv} onError={setError} />
        ))}
      </div>
      {editing && (
        <ProviderDrawer
          seed={editing === 'new' ? null : editing}
          ov={ov}
          onClose={() => setEditing(null)}
          onSaved={(next, msg) => {
            setOv(next)
            setEditing(null)
            onToast?.(msg)
          }}
        />
      )}
    </>
  )
}

export function keyText(p: ProviderView): { pill: string; tone: string; text: string } {
  const auth = p.resolved.auth ?? 'header'
  if (auth === 'none') return { pill: 'No credential needed', tone: 'ok', text: 'This provider takes no credential.' }
  // --- AI settings, round 2 (Phase 12, wave 5): an endpoint that answered its test without a key needs none.
  if (!p.key.set && p.keylessOK) {
    return { pill: 'No key needed', tone: 'ok', text: `Its test passed without a key${p.tested?.at ? `, ${fmtTs(p.tested.at)}` : ''}: the agents on it answer without one. Save a key here if the endpoint starts asking for one.` }
  }
  if (!p.key.set) {
    return auth === 'header'
      ? { pill: 'No key', tone: 'warn', text: 'No key yet: the agents on this provider answer nothing until one is saved, or until a test passes without one (an in-house endpoint that takes none).' }
      : { pill: 'The pod’s own identity', tone: 'muted', text: auth === 'aws' ? 'Nothing saved: the console uses its ServiceAccount’s IAM role (IRSA or EKS Pod Identity).' : 'Nothing saved: the console uses its ServiceAccount’s Google identity (Workload Identity).' }
  }
  if (p.key.source === 'chart') return { pill: 'Key set by the chart', tone: 'ok', text: 'Set by the chart (sherlock.existingSecret); change it there.' }
  return { pill: 'Key saved', tone: 'ok', text: `Saved${p.key.setBy ? ` by ${p.key.setBy}` : ''}${p.key.setAt ? `, ${fmtTs(p.key.setAt)}` : ''}. ${p.key.store === 'secret' ? 'Kept in a Secret in the console’s namespace.' : 'Kept in memory: it is lost when the console restarts.'} It is never shown again.` }
}

/** A test's result in one line. */
export function testText(r: ProviderTestResult): string {
  if (!r.ok) return `${r.model}: ${r.error ?? 'no answer'}`
  return `${r.model} answered in ${(r.ms / 1000).toFixed(1)} s${r.keyless ? ', without a key' : ''}: “${r.answer ?? ''}”`
}

/** A credential as the server takes it: a key, AWS keys as JSON, or a Google key file. */
function credential(auth: ProviderAuth, key: string, secret: string): string {
  if (auth === 'aws') return key.trim() && secret.trim() ? JSON.stringify({ accessKeyId: key.trim(), secretAccessKey: secret.trim() }) : ''
  return key.trim()
}

function ProviderCard({ p, onEdit, onChange, onError }: { p: ProviderView; onEdit: () => void; onChange: (o: AIOverview) => void; onError: (e: string) => void }) {
  const [test, setTest] = useState<{ busy: boolean; text?: string; ok?: boolean } | null>(null)
  const [key, setKey] = useState('')
  const [secret, setSecret] = useState('')
  const [busy, setBusy] = useState(false)
  const auth = p.resolved.auth ?? 'header'
  const k = keyText(p)
  const run = async (fn: () => Promise<AIOverview>) => {
    setBusy(true)
    onError('')
    try {
      onChange(await fn())
      setKey('')
      setSecret('')
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="rk-card" style={{ padding: '14px 16px', display: 'flex', flexDirection: 'column', gap: 10 }} aria-label={`Provider ${p.name}`} data-testid="provider" data-provider={p.id}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <h3 className="rk-h" style={{ fontSize: 14 }}>{p.name}</h3>
        {p.default && <span className="rk-pill" data-tone="info">Default</span>}
        {p.locked && <span className="rk-pill" data-tone="muted" title="The chart fixes this provider (sherlock.provider, sherlock.lockProvider)">Locked by the chart</span>}
        <span className="rk-pill" data-tone={k.tone} data-testid="provider-key-pill">
          <span className="rk-dot" />
          {k.pill}
        </span>
        <span style={{ flex: '1 1 auto' }} />
        <button
          className="rk-btn"
          disabled={test?.busy}
          aria-busy={test?.busy}
          data-testid="provider-test"
          onClick={async () => {
            setTest({ busy: true })
            try {
              const r = await testProvider(p.id)
              setTest({ busy: false, ok: r.ok, text: testText(r) })
              // The test is kept: a pass without a key makes it usable without one.
              onChange(await fetchAI())
            } catch (e) {
              setTest({ busy: false, ok: false, text: e instanceof Error ? e.message : String(e) })
            }
          }}
        >
          Test
        </button>
        <button className="rk-btn" onClick={onEdit} data-testid="provider-edit">
          Edit…
        </button>
      </div>
      {test && !test.busy && (
        <p className="rk-callout" data-tone={test.ok ? 'info' : 'warn'} role={test.ok ? 'status' : 'alert'} style={{ margin: 0, fontSize: 12.5 }} data-testid="provider-test-result">
          {test.text}
        </p>
      )}
      <dl className="rk-kv">
        <dt>Kind</dt>
        <dd>{kindLabel(p.resolved.kind)}{presetOf(p) === 'anthropic' ? ' — the Claude API' : ''}</dd>
        <dt>Endpoint</dt>
        <dd className="rk-mono">{p.endpoint ? p.endpoint : 'The provider’s own'}</dd>
        {p.region && (
          <>
            <dt>Region</dt>
            <dd className="rk-mono">{p.region}{p.project ? ` · ${p.project}` : ''}</dd>
          </>
        )}
        {p.caCert && (
          <>
            <dt>Private CA</dt>
            <dd>A certificate authority of your own</dd>
          </>
        )}
        <dt>Default model</dt>
        <dd className="rk-mono">{p.model || '—'}{p.contextWindow ? <span className="rk-fhint"> · {tokensText(p.contextWindow)} of context</span> : null}</dd>
        {p.tested && (
          <>
            <dt>Last test</dt>
            <dd data-testid="provider-last-test">
              {p.tested.ok ? 'Passed' : 'Failed'}
              {p.tested.model ? ` with ${p.tested.model}` : ''}
              {p.tested.keyless ? ', without a key' : ''}, {fmtTs(p.tested.at)}
              {!p.tested.ok && p.tested.error ? `: ${p.tested.error}` : ''}
            </dd>
          </>
        )}
        <dt>Used by</dt>
        <dd data-testid="provider-used-by">{p.usedBy?.length ? p.usedBy.join(', ') : 'No agent'}</dd>
      </dl>
      {auth !== 'none' && p.key.source !== 'chart' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }} aria-label="Credential">
          <p className="rk-fhint" style={{ margin: 0 }} data-testid="provider-key-state">
            {k.text}
          </p>
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'flex-end' }}>
            {auth === 'google' ? (
              <label style={{ flex: '1 1 100%', display: 'flex' }}>
                <span className="rk-sr">Service account key</span>
                <textarea className="rk-area" rows={3} spellCheck={false} autoComplete="off" placeholder="{&quot;type&quot;: &quot;service_account&quot;, …} — the key file’s JSON" value={key} onChange={(e) => setKey(e.target.value)} style={{ width: '100%' }} data-testid="provider-key" />
              </label>
            ) : (
              <label className="rk-field" style={{ flex: '1 1 240px' }}>
                <span className="rk-sr">{auth === 'aws' ? 'Access key ID' : 'API key'}</span>
                <input type={auth === 'aws' ? 'text' : 'password'} autoComplete="off" spellCheck={false} placeholder={auth === 'aws' ? 'Access key ID (AKIA…)' : p.key.set ? '•••••••• saved. Paste a new key to replace it' : 'Paste the key'} value={key} onChange={(e) => setKey(e.target.value)} data-testid="provider-key" />
              </label>
            )}
            {auth === 'aws' && (
              <label className="rk-field" style={{ flex: '1 1 240px' }}>
                <span className="rk-sr">Secret access key</span>
                <input type="password" autoComplete="off" spellCheck={false} placeholder="Secret access key" value={secret} onChange={(e) => setSecret(e.target.value)} data-testid="provider-key-secret" />
              </label>
            )}
            <button className="rk-btn" data-primary="true" disabled={busy || !credential(auth, key, secret)} onClick={() => void run(() => saveProviderKey(p.id, credential(auth, key, secret)))} data-testid="provider-key-save">
              Save the {auth === 'aws' ? 'credentials' : 'key'}
            </button>
            {p.key.set && (
              <button className="rk-btn" disabled={busy} onClick={() => void run(() => removeProviderKey(p.id))} data-testid="provider-key-remove">
                Remove the saved {auth === 'aws' ? 'credentials' : 'key'}
              </button>
            )}
          </div>
        </div>
      )}
    </section>
  )
}

/** A provider as the form edits it. */
interface FormState extends AIProvider {
  headersText: string
}

function blank(): FormState {
  return { id: '', name: '', kind: 'anthropic', headersText: '' }
}

/** The provider to save: the fields left empty dropped, the kind kept. */
export function providerFromForm(f: FormState): AIProvider {
  const out: AIProvider = { id: f.id.trim(), name: f.name.trim(), kind: f.kind }
  for (const k of ['endpoint', 'auth', 'authHeader', 'authPrefix', 'query', 'region', 'project', 'caCert', 'model'] as const) {
    const v = f[k]
    if (k === 'authPrefix' ? v !== undefined && f.authHeader : v) (out as unknown as Record<string, unknown>)[k] = v
  }
  const headers = parseHeaders(f.headersText)
  if (headers) out.headers = headers
  if (f.priceIn) out.priceIn = f.priceIn
  if (f.priceOut) out.priceOut = f.priceOut
  if (f.contextWindow) out.contextWindow = f.contextWindow
  return out
}

/** The id a new provider gets from its name: "Second gateway" → second-gateway. */
export function idFromName(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^[^a-z]+|-+$/g, '')
    .slice(0, 31)
}

function ProviderDrawer({ seed, ov, onClose, onSaved }: { seed: ProviderView | null; ov: AIOverview; onClose: () => void; onSaved: (o: AIOverview, msg: string) => void }) {
  const [f, setF] = useState<FormState>(() => (seed ? { ...seed, kind: seed.kind || 'anthropic', headersText: headersText(seed.headers) } : blank()))
  const [preset, setPreset] = useState(() => presetOf(f))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const locked = !!seed?.locked
  const set = (q: Partial<FormState>) => setF({ ...f, ...q })
  const auth = f.auth ?? defaultAuth(f.kind)
  // --- AI settings, round 2 (Phase 12, wave 5): Test before saving, and the saved settings tested again on Save.
  const [test, setTest] = useState<{ busy: boolean; r?: ProviderTestResult; error?: string; form?: string } | null>(null)
  const formKey = JSON.stringify(providerFromForm(f))
  const tested = test?.r?.ok && test.form === formKey
  const runTest = async () => {
    setTest({ busy: true })
    try {
      const r = await tryProvider(providerFromForm(f), seed?.id, f.model)
      setTest({ busy: false, r, form: formKey })
    } catch (e) {
      setTest({ busy: false, error: e instanceof Error ? e.message : String(e) })
    }
  }
  const save = async () => {
    setBusy(true)
    setError('')
    try {
      const p = providerFromForm(seed ? f : { ...f, id: f.id || idFromName(f.name) })
      let next = seed ? await saveProvider(p) : await addProvider(p)
      let msg = seed ? `Saved ${p.name}.` : `Added ${p.name}. Save its credential on its card.`
      // Settings that passed their test here are tested again as saved, so
      // a provider that needs no key is usable at once.
      if (tested) {
        const r = await testProvider(p.id, p.model).catch(() => null)
        next = await fetchAI().catch(() => next)
        msg = r?.ok ? `${seed ? 'Saved' : 'Added'} ${p.name}; its test passed${r.keyless ? ' without a key: the agents on it may use it now' : ''}.` : `${seed ? 'Saved' : 'Added'} ${p.name}, but its test as saved failed: ${r?.error ?? 'no answer'}.`
      }
      onSaved(next, msg)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const remove = async () => {
    if (!seed) return
    setBusy(true)
    setError('')
    try {
      onSaved(await removeProvider(seed.id), `Removed ${seed.name} and its credential.`)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const pr = PRESETS.find((x) => x.id === preset) ?? PRESETS[0]!
  const models = [...pr.models, ...(f.kind === 'anthropic' ? ov.models.map((m) => m.id) : [])]
  return (
    <Drawer
      label="Provider"
      kicker="Provider"
      title={seed ? seed.name : 'New provider'}
      copy={false}
      width={720}
      onClose={onClose}
      testId="provider-drawer"
      footer={
        <>
          <button className="rk-btn" data-primary="true" disabled={busy || !f.name.trim() || (!seed && !(f.id || idFromName(f.name)))} onClick={() => void save()} data-testid="provider-save">
            {seed ? 'Save changes' : 'Add provider'}
          </button>
          <button className="rk-btn" onClick={() => void runTest()} disabled={busy || test?.busy} aria-busy={test?.busy} data-testid="provider-drawer-test">
            {test?.busy ? 'Testing…' : 'Test'}
          </button>
          <button className="rk-btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          {seed && !seed.default && (
            <button className="rk-btn" disabled={busy || !!seed.usedBy?.length} title={seed.usedBy?.length ? 'Move the agents that use it to another provider first' : undefined} onClick={() => void remove()} data-testid="provider-remove" style={{ marginLeft: 'auto' }}>
              Remove
            </button>
          )}
          {error && (
            <span className="rk-t" data-tone="err" role="alert" style={{ fontSize: 12.5, flex: '1 1 100%' }} data-testid="provider-error">
              {error}
            </span>
          )}
        </>
      }
    >
      {locked && (
        <p className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5 }}>
          The chart sets this provider (sherlock.provider, sherlock.lockProvider): its kind and address cannot be changed here. Its name, default model and prices can.
        </p>
      )}
      <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
        <Field label="Name" hint="As the agents’ table shows it.">
          <span className="rk-field">
            <input value={f.name} onChange={(e) => set({ name: e.target.value })} data-testid="provider-name" />
          </span>
        </Field>
        {!seed && (
          <Field label="Id" hint="Lower-case letters, digits and dashes; names its Secret.">
            <span className="rk-field">
              <input spellCheck={false} value={f.id} placeholder={idFromName(f.name) || 'gateway'} onChange={(e) => set({ id: e.target.value.trim() })} data-testid="provider-id" />
            </span>
          </Field>
        )}
      </div>
      <fieldset disabled={locked} style={{ border: 0, margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 12, minWidth: 0 }}>
        <Field label="Kind" hint="Each provider’s own API, or an enterprise gateway in front of one: give the gateway’s address below." wide>
          <Select
            label="Kind"
            value={preset}
            options={PRESETS.map((x) => [x.id, x.label])}
            onChange={(id) => {
              const next = PRESETS.find((x) => x.id === id)!
              setPreset(id)
              setF({ id: f.id, name: f.name, headersText: '', ...next.p, kind: next.p.kind, model: next.models[0] ?? f.model, priceIn: f.priceIn, priceOut: f.priceOut })
            }}
            testId="provider-kind"
            width="100%"
          />
        </Field>
        <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          <Field label="Endpoint" hint="Empty is the provider’s own. For a gateway, its address." wide>
            <span className="rk-field">
              <input type="url" spellCheck={false} placeholder={ENDPOINT_HINT[preset]} value={f.endpoint ?? ''} onChange={(e) => set({ endpoint: e.target.value.trim() || undefined })} data-testid="provider-endpoint" />
            </span>
          </Field>
          {(f.kind === 'bedrock' || f.kind === 'vertex') && (
            <Field label={f.kind === 'bedrock' ? 'AWS region' : 'Location'}>
              <span className="rk-field">
                <input spellCheck={false} value={f.region ?? ''} onChange={(e) => set({ region: e.target.value.trim() || undefined })} data-testid="provider-region" />
              </span>
            </Field>
          )}
          {f.kind === 'vertex' && (
            <Field label="Google Cloud project">
              <span className="rk-field">
                <input spellCheck={false} value={f.project ?? ''} onChange={(e) => set({ project: e.target.value.trim() || undefined })} />
              </span>
            </Field>
          )}
        </div>
        <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          <Field label="Authentication">
            <Select label="Authentication" value={auth} options={AUTHS[f.kind] ?? AUTHS.anthropic} onChange={(v) => set({ auth: v as ProviderAuth, authHeader: undefined, authPrefix: undefined })} testId="provider-auth" width="100%" />
          </Field>
          {auth === 'header' && (
            <>
              <Field label="Header" hint={f.kind === 'anthropic' ? 'Default x-api-key.' : 'Default Authorization.'}>
                <span className="rk-field">
                  <input spellCheck={false} value={f.authHeader ?? ''} placeholder={f.kind === 'anthropic' ? 'x-api-key' : 'Authorization'} onChange={(e) => set({ authHeader: e.target.value.trim() || undefined })} />
                </span>
              </Field>
              <Field label="Value prefix" hint={'Such as "Bearer " before the key.'}>
                <span className="rk-field">
                  <input spellCheck={false} value={f.authPrefix ?? ''} placeholder={f.kind === 'anthropic' ? '(none)' : 'Bearer '} onChange={(e) => set({ authPrefix: e.target.value })} />
                </span>
              </Field>
            </>
          )}
        </div>
        <details open={!!(f.headersText || f.query || f.caCert)}>
          <summary style={{ fontSize: 12.5, fontWeight: 600, cursor: 'pointer' }}>Gateway options: headers, query, CA certificate</summary>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12, marginTop: 10 }}>
            <Field label="Extra headers" hint="One “Name: value” per line, sent with every request. Not secret: keep keys in the credential." wide>
              <textarea className="rk-area" rows={3} spellCheck={false} value={f.headersText} placeholder={'X-Team: platform'} onChange={(e) => set({ headersText: e.target.value })} style={{ width: '100%' }} data-testid="provider-headers" />
            </Field>
            {(f.kind === 'openai' || f.kind === 'anthropic') && (
              <Field label="Query" hint="Added to every request, such as api-version=2024-10-21 for Azure OpenAI." wide>
                <span className="rk-field">
                  <input spellCheck={false} value={f.query ?? ''} onChange={(e) => set({ query: e.target.value.trim() || undefined })} />
                </span>
              </Field>
            )}
            <Field label="CA certificate" hint="PEM, for a gateway whose certificate a private authority signed. Empty trusts the system’s." wide>
              <textarea className="rk-area" rows={3} spellCheck={false} value={f.caCert ?? ''} placeholder="-----BEGIN CERTIFICATE-----" onChange={(e) => set({ caCert: e.target.value.trim() ? e.target.value : undefined })} style={{ width: '100%' }} />
            </Field>
          </div>
        </details>
      </fieldset>
      {test && !test.busy && (
        <p className="rk-callout" data-tone={test.r?.ok ? 'info' : 'warn'} role={test.r?.ok ? 'status' : 'alert'} style={{ margin: 0, fontSize: 12.5 }} data-testid="provider-drawer-test-result">
          {test.r ? testText(test.r) : test.error}
          {test.r?.ok && test.r.keyless && auth === 'header' ? ' It answers without a key: once saved, the agents on it may use it without one.' : ''}
          {test.r && test.form !== formKey ? ' (The settings changed since: test again.)' : ''}
        </p>
      )}
      <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'flex-start' }}>
        <ModelPicker
          label="Default model"
          hint="What agents on this provider use unless they name another. The list comes from the endpoint; another model can be typed."
          value={f.model ?? ''}
          onChange={(model, listed) => set({ model: model || undefined, ...(listed?.contextWindow ? { contextWindow: listed.contextWindow } : {}) })}
          load={() => fetchModelsFor(providerFromForm(f), seed?.id)}
          auto={!!seed}
          from={`${f.kind} ${f.endpoint ?? ''} ${f.region ?? ''}`}
          suggestions={models}
          testId="provider-model"
        />
        <Field label="Context window (tokens)" hint="Of the default model: from the endpoint’s list when it says, else as its server is set (vLLM’s --max-model-len). Empty is not known.">
          <span className="rk-field" style={{ width: 160 }}>
            <input type="number" min={0} step={1024} value={f.contextWindow ?? ''} placeholder="Not known" onChange={(e) => set({ contextWindow: Math.max(0, Math.round(Number(e.target.value) || 0)) || undefined })} data-testid="provider-context" />
          </span>
        </Field>
      </div>
      <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
        <Field label="Input price (US$ per million tokens)" hint="For the spending caps. 0 is the listed Claude price, or the highest.">
          <span className="rk-field" style={{ width: 160 }}>
            <input type="number" min={0} step={0.01} value={f.priceIn ?? 0} onChange={(e) => set({ priceIn: Math.max(0, Number(e.target.value) || 0) || undefined })} data-testid="provider-price-in" />
          </span>
        </Field>
        <Field label="Output price (US$ per million tokens)">
          <span className="rk-field" style={{ width: 160 }}>
            <input type="number" min={0} step={0.01} value={f.priceOut ?? 0} onChange={(e) => set({ priceOut: Math.max(0, Number(e.target.value) || 0) || undefined })} data-testid="provider-price-out" />
          </span>
        </Field>
      </div>
      {!seed && <p className="rk-fhint" style={{ margin: 0 }}>Once it is added, save its credential on its card and press Test.</p>}
    </Drawer>
  )
}

