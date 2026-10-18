import { useEffect, useState } from 'react'
import { deleteSherlockKey, fetchSherlockConfig, priceText, saveSherlockConfig, saveSherlockKey, usd, type ProviderAuth, type ProviderKind, type SherlockConfig, type SherlockProvider, type SherlockSettings as Settings } from '../api/sherlock'
import { fmtTs } from '../lib/format'
import { SaveBar } from './SaveBar'
import { Select } from './Select'

// ---------- the provider ----------

interface Preset {
  id: string
  label: string
  /** The provider it starts from; endpoint placeholders show what to fill in. */
  p: SherlockProvider
  models: string[]
}

/**
 * Where the model may run (Phase 6 review): each provider's own API, or an
 * enterprise gateway in front of one, at any address.
 */
export const PRESETS: Preset[] = [
  { id: 'anthropic', label: 'Anthropic — the Claude API', p: { kind: 'anthropic' }, models: [] },
  { id: 'openai', label: 'OpenAI', p: { kind: 'openai' }, models: ['gpt-5', 'gpt-5-mini', 'gpt-4.1'] },
  { id: 'azure', label: 'Azure OpenAI', p: { kind: 'openai', endpoint: '', authHeader: 'api-key', authPrefix: '', query: 'api-version=2024-10-21' }, models: [] },
  { id: 'bedrock', label: 'Amazon Bedrock', p: { kind: 'bedrock', region: 'us-east-1' }, models: ['us.anthropic.claude-opus-5-5-v1:0', 'us.anthropic.claude-sonnet-5-5-v1:0', 'amazon.nova-pro-v1:0', 'meta.llama4-maverick-17b-instruct-v1:0'] },
  { id: 'vertex', label: 'Google Vertex AI', p: { kind: 'vertex', region: 'us-east5', project: '' }, models: ['claude-opus-5-5', 'claude-sonnet-5-5', 'google/gemini-2.5-pro', 'google/gemini-2.5-flash'] },
  { id: 'compatible', label: 'Another OpenAI-compatible API or gateway (vLLM, Ollama, LiteLLM, Portkey, Kong, …)', p: { kind: 'openai', endpoint: '' }, models: [] },
  { id: 'anthropic-gateway', label: 'A gateway in front of the Claude API', p: { kind: 'anthropic', endpoint: '' }, models: [] },
]

/** Which preset a saved provider came from. */
export function presetOf(p: SherlockProvider | undefined): string {
  if (!p || (p.kind === 'anthropic' && (!p.endpoint || p.endpoint === 'https://api.anthropic.com'))) return 'anthropic'
  if (p.kind === 'anthropic') return 'anthropic-gateway'
  if (p.kind === 'bedrock' || p.kind === 'vertex') return p.kind
  if ((p.endpoint ?? '').includes('.openai.azure.com') || p.authHeader === 'api-key') return 'azure'
  if (!p.endpoint || p.endpoint.startsWith('https://api.openai.com')) return 'openai'
  return 'compatible'
}

const ENDPOINT_HINT: Record<string, string> = {
  anthropic: 'https://api.anthropic.com',
  'anthropic-gateway': 'https://ai-gateway.corp.example/anthropic',
  openai: 'https://api.openai.com/v1',
  azure: 'https://RESOURCE.openai.azure.com/openai/deployments/DEPLOYMENT',
  compatible: 'https://ai-gateway.corp.example/v1 or http://ollama.ai:11434/v1',
  bedrock: 'https://bedrock-runtime.REGION.amazonaws.com',
  vertex: 'https://REGION-aiplatform.googleapis.com',
}

const AUTHS: Record<ProviderKind, [ProviderAuth, string][]> = {
  anthropic: [
    ['header', 'An API key in a header'],
    ['none', 'None (the gateway authenticates otherwise)'],
  ],
  openai: [
    ['header', 'An API key in a header'],
    ['none', 'None (the gateway authenticates otherwise)'],
  ],
  bedrock: [
    ['aws', 'AWS credentials (SigV4): saved keys, or the pod’s IAM role'],
    ['header', 'A gateway key in a header'],
    ['none', 'None (the gateway authenticates otherwise)'],
  ],
  vertex: [
    ['google', 'A Google identity: a service account key, or Workload Identity'],
    ['header', 'A gateway key in a header'],
    ['none', 'None (the gateway authenticates otherwise)'],
  ],
}

function defaultAuth(kind: ProviderKind): ProviderAuth {
  return kind === 'bedrock' ? 'aws' : kind === 'vertex' ? 'google' : 'header'
}

/** Extra headers as the form edits them: one "Name: value" per line. */
export function headersText(h: Record<string, string> | undefined): string {
  return Object.entries(h ?? {})
    .map(([k, v]) => `${k}: ${v}`)
    .join('\n')
}

export function parseHeaders(text: string): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const i = line.indexOf(':')
    if (i <= 0) continue
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim()
  }
  return Object.keys(out).length ? out : undefined
}

/** Drops the fields left empty, so the settings keep only what was given. */
function clean(p: SherlockProvider): SherlockProvider | undefined {
  const out: SherlockProvider = { kind: p.kind }
  for (const k of ['endpoint', 'auth', 'authHeader', 'authPrefix', 'query', 'region', 'project', 'caCert'] as const) {
    const v = p[k]
    if (k === 'authPrefix' ? v !== undefined && p.authHeader : v) (out as unknown as Record<string, unknown>)[k] = v
  }
  if (p.headers && Object.keys(p.headers).length) out.headers = p.headers
  if (out.kind === 'anthropic' && Object.keys(out).length === 1) return undefined
  return out
}

function Field({ label, hint, children, wide }: { label: string; hint?: string; children: React.ReactNode; wide?: boolean }) {
  return (
    <label className={wide ? 'rk-ffield rk-fwide' : 'rk-ffield'} style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600, minWidth: 0 }}>
      {label}
      {children}
      {hint && <span className="rk-fhint" style={{ fontWeight: 400 }}>{hint}</span>}
    </label>
  )
}

function ProviderForm({ draft, setDraft, cfg }: { draft: Settings; setDraft: (s: Settings) => void; cfg: SherlockConfig }) {
  const locked = cfg.providerLocked
  // Locked, or not set in the settings, the chart's provider is in force.
  const p: SherlockProvider = locked ? cfg.provider : (draft.provider ?? (cfg.provider.kind !== 'anthropic' ? cfg.provider : { kind: 'anthropic' }))
  const [preset, setPreset] = useState(() => presetOf(p))
  const [headers, setHeaders] = useState(() => headersText(p.headers))
  const set = (q: Partial<SherlockProvider>) => setDraft({ ...draft, provider: clean({ ...p, ...q }) })
  const auth = p.auth ?? defaultAuth(p.kind)
  const pr = PRESETS.find((x) => x.id === preset) ?? PRESETS[0]!
  const listed = cfg.models.some((m) => m.id === draft.model)
  const [other, setOther] = useState(!listed && !!draft.model)
  return (
    <fieldset disabled={locked} style={{ border: 0, margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 12, minWidth: 0 }} data-testid="sherlock-provider">
      {locked && (
        <p className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5 }} data-testid="sherlock-provider-locked">
          The chart sets the provider (sherlock.provider, sherlock.lockProvider), so it cannot be changed here.
        </p>
      )}
      <Field label="Provider" hint="Each provider’s own API, or an enterprise gateway in front of one: give the gateway’s address below.">
        <Select
          label="Provider"
          value={preset}
          options={PRESETS.map((x) => [x.id, x.label])}
          onChange={(id) => {
            const next = PRESETS.find((x) => x.id === id)!
            setPreset(id)
            setHeaders('')
            const model = next.models[0] ?? (next.p.kind === 'anthropic' ? (cfg.models[0]?.id ?? draft.model) : draft.model)
            setOther(next.p.kind !== 'anthropic' && !next.models.length)
            setDraft({ ...draft, provider: clean({ ...next.p }), model, priceIn: undefined, priceOut: undefined })
          }}
          testId="sherlock-provider-kind"
          width="100%"
        />
      </Field>
      <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
        <Field label="Endpoint" hint={`Empty is the provider’s own${p.kind === 'anthropic' && !p.endpoint ? ` (${cfg.provider.kind === 'anthropic' ? cfg.provider.endpoint : 'https://api.anthropic.com'})` : ''}. For a gateway, its address.`} wide>
          <span className="rk-field">
            <input type="url" spellCheck={false} placeholder={ENDPOINT_HINT[preset]} value={p.endpoint ?? ''} onChange={(e) => set({ endpoint: e.target.value.trim() || undefined })} data-testid="sherlock-endpoint" />
          </span>
        </Field>
        {(p.kind === 'bedrock' || p.kind === 'vertex') && (
          <Field label={p.kind === 'bedrock' ? 'AWS region' : 'Location'}>
            <span className="rk-field">
              <input spellCheck={false} value={p.region ?? ''} placeholder={p.kind === 'bedrock' ? 'us-east-1' : 'us-east5'} onChange={(e) => set({ region: e.target.value.trim() || undefined })} data-testid="sherlock-region" />
            </span>
          </Field>
        )}
        {p.kind === 'vertex' && (
          <Field label="Google Cloud project">
            <span className="rk-field">
              <input spellCheck={false} value={p.project ?? ''} onChange={(e) => set({ project: e.target.value.trim() || undefined })} data-testid="sherlock-project" />
            </span>
          </Field>
        )}
      </div>
      <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
        <Field label="Authentication">
          <Select label="Authentication" value={auth} options={AUTHS[p.kind]} onChange={(v) => set({ auth: v as ProviderAuth, authHeader: undefined, authPrefix: undefined })} testId="sherlock-auth" width="100%" />
        </Field>
        {auth === 'header' && (
          <>
            <Field label="Header" hint={p.kind === 'anthropic' ? 'Default x-api-key.' : 'Default Authorization.'}>
              <span className="rk-field">
                <input spellCheck={false} value={p.authHeader ?? ''} placeholder={p.kind === 'anthropic' ? 'x-api-key' : 'Authorization'} onChange={(e) => set({ authHeader: e.target.value.trim() || undefined })} data-testid="sherlock-auth-header" />
              </span>
            </Field>
            <Field label="Value prefix" hint={'Such as "Bearer " before the key.'}>
              <span className="rk-field">
                <input spellCheck={false} value={p.authPrefix ?? ''} placeholder={p.kind === 'anthropic' ? '(none)' : 'Bearer '} onChange={(e) => set({ authPrefix: e.target.value })} data-testid="sherlock-auth-prefix" />
              </span>
            </Field>
          </>
        )}
      </div>
      <details open={!!(p.headers || p.query || p.caCert)}>
        <summary style={{ fontSize: 12.5, fontWeight: 600, cursor: 'pointer' }}>Gateway options: headers, query, CA certificate</summary>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12, marginTop: 10 }}>
          <Field label="Extra headers" hint="One “Name: value” per line, sent with every request. Not secret: keep keys in the credential below." wide>
            <span style={{ display: 'flex' }}>
              <textarea
                rows={3}
                spellCheck={false}
                value={headers}
                placeholder={'X-Team: platform\nX-Cost-Center: 4410'}
                onChange={(e) => {
                  setHeaders(e.target.value)
                  set({ headers: parseHeaders(e.target.value) })
                }}
                data-testid="sherlock-headers"
                className="rk-area" style={{ width: '100%' }}
              />
            </span>
          </Field>
          {(p.kind === 'openai' || p.kind === 'anthropic') && (
            <Field label="Query" hint="Added to every request, such as api-version=2024-10-21 for Azure OpenAI." wide>
              <span className="rk-field">
                <input spellCheck={false} value={p.query ?? ''} onChange={(e) => set({ query: e.target.value.trim() || undefined })} data-testid="sherlock-query" />
              </span>
            </Field>
          )}
          <Field label="CA certificate" hint="PEM, for a gateway whose certificate a private authority signed. Empty trusts the system’s." wide>
            <span style={{ display: 'flex' }}>
              <textarea rows={3} spellCheck={false} value={p.caCert ?? ''} placeholder="-----BEGIN CERTIFICATE-----" onChange={(e) => set({ caCert: e.target.value.trim() ? e.target.value : undefined })} data-testid="sherlock-ca" className="rk-area" style={{ width: '100%' }} />
            </span>
          </Field>
        </div>
      </details>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        {p.kind === 'anthropic' && !other ? (
          <Field label="Model" wide>
            <Select
              label="Model"
              value={draft.model}
              options={[...cfg.models.map((m): [string, string] => [m.id, `${m.label} — ${priceText(m)}`]), ['*other', 'Another model…']]}
              onChange={(v) => (v === '*other' ? setOther(true) : setDraft({ ...draft, model: v }))}
              testId="sherlock-model"
              width="100%"
            />
          </Field>
        ) : (
          <Field label="Model" hint={p.kind === 'bedrock' ? 'A model id or inference profile, such as us.anthropic.claude-sonnet-5-5-v1:0.' : p.kind === 'vertex' ? 'A Claude model (claude-…) or a publisher model (google/gemini-2.5-pro).' : 'As the API or gateway names it: for Azure, the deployment’s model.'} wide>
            <span className="rk-field">
              <input spellCheck={false} list="sherlock-models" value={draft.model} onChange={(e) => setDraft({ ...draft, model: e.target.value.trim() })} data-testid="sherlock-model-name" />
            </span>
            {p.kind === 'anthropic' && (
              <button
                type="button"
                className="rk-link"
                style={{ alignSelf: 'flex-start', fontSize: 12, fontWeight: 400 }}
                onClick={() => {
                  setOther(false)
                  if (!listed) setDraft({ ...draft, model: cfg.models[0]?.id ?? draft.model, priceIn: undefined, priceOut: undefined })
                }}
              >
                Pick from the list
              </button>
            )}
            <datalist id="sherlock-models">
              {[...pr.models, ...cfg.models.map((m) => m.id)].map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>
          </Field>
        )}
        {cfg.models.find((m) => m.id === draft.model)?.note && (
          <p className="rk-fhint" style={{ margin: 0 }}>
            {cfg.models.find((m) => m.id === draft.model)?.note}
          </p>
        )}
        {!listed && (
          <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            <Field label="Input price (US$ per million tokens)" hint="For the spending caps. 0 counts it at the highest listed Claude price.">
              <span className="rk-field" style={{ width: 160 }}>
                <input type="number" min={0} step={0.01} value={draft.priceIn ?? 0} onChange={(e) => setDraft({ ...draft, priceIn: Math.max(0, Number(e.target.value) || 0) || undefined })} data-testid="sherlock-price-in" />
              </span>
            </Field>
            <Field label="Output price (US$ per million tokens)">
              <span className="rk-field" style={{ width: 160 }}>
                <input type="number" min={0} step={0.01} value={draft.priceOut ?? 0} onChange={(e) => setDraft({ ...draft, priceOut: Math.max(0, Number(e.target.value) || 0) || undefined })} data-testid="sherlock-price-out" />
              </span>
            </Field>
          </div>
        )}
      </div>
    </fieldset>
  )
}

// ---------- the credential ----------

/** A credential being typed: an API key, AWS keys, or a Google key file. */
interface Typed {
  key: string
  secret: string
  token: string
}

const NOTHING_TYPED: Typed = { key: '', secret: '', token: '' }

/** What a credential's saved state is, in words, and its tone. */
export function keyState(cfg: SherlockConfig): { text: string; short: string; tone: string } {
  const auth = cfg.provider.auth ?? 'header'
  const k = cfg.key
  const ambient = auth === 'aws' ? 'Without saved keys, the console uses its ServiceAccount’s IAM role (IRSA or EKS Pod Identity).' : auth === 'google' ? 'Without a saved key, the console uses its ServiceAccount’s Google identity (Workload Identity).' : ''
  if (auth === 'none') return { text: 'This provider takes no credential.', short: 'No credential needed', tone: 'ok' }
  if (!k.set) {
    return auth === 'header' ? { text: 'No key yet. Sherlock answers nothing until one is saved.', short: `No ${credentialName(cfg)}`, tone: 'warn' } : { text: `Nothing saved. ${ambient}`, short: 'The pod’s own identity', tone: 'muted' }
  }
  if (k.source === 'chart') return { text: 'Set by the chart (sherlock.existingSecret); change it there.', short: `${credentialName(cfg)} set by the chart`, tone: 'ok' }
  return {
    text: `Saved${k.setBy ? ` by ${k.setBy}` : ''}${k.setAt ? `, ${fmtTs(k.setAt)}` : ''}. ${k.store === 'secret' ? 'Kept in a Secret in the console’s namespace.' : 'Kept in memory: it is lost when the console restarts.'} It is never shown again.`,
    short: `${credentialName(cfg)} saved`,
    tone: 'ok',
  }
}

/** "Claude API key", "API key", "AWS credentials" or "Google service account key". */
function credentialName(cfg: SherlockConfig): string {
  const auth = cfg.provider.auth ?? 'header'
  if (auth === 'aws') return 'AWS credentials'
  if (auth === 'google') return 'Google service account key'
  return cfg.provider.kind === 'anthropic' && presetOf(cfg.provider) === 'anthropic' ? 'Claude API key' : 'API key'
}

/** What is typed, as the server takes it, or '' while it is incomplete. */
function typedValue(cfg: SherlockConfig, t: Typed): string {
  const auth = cfg.provider.auth ?? 'header'
  if (auth === 'aws') return t.key.trim() && t.secret.trim() ? JSON.stringify({ accessKeyId: t.key.trim(), secretAccessKey: t.secret.trim(), ...(t.token.trim() ? { sessionToken: t.token.trim() } : {}) }) : ''
  return t.key.trim() ? t.key : ''
}

function Credential({ cfg, draft, busy, typed, setTyped, remove }: { cfg: SherlockConfig; draft: Settings; busy: boolean; typed: Typed; setTyped: (t: Typed) => void; remove: () => void }) {
  const auth = cfg.provider.auth ?? 'header'
  const draftAuth = draft.provider?.auth ?? defaultAuth(draft.provider?.kind ?? 'anthropic')
  const pending = JSON.stringify(draft.provider ?? null) !== JSON.stringify(cfg.settings.provider ?? null)
  const k = cfg.key
  const state = keyState(cfg)
  const label = credentialName(cfg)
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Credential" data-testid="sherlock-credential">
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <h3 className="rk-h">{label}</h3>
        <span className="rk-pill" data-tone={state.tone} data-testid="sherlock-key-pill">
          <span className="rk-dot" />
          {k.set ? 'Saved' : auth === 'none' ? 'Not needed' : auth === 'header' ? 'Missing' : 'Not saved'}
        </span>
      </div>
      {pending && draftAuth !== auth ? (
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="sherlock-credential-pending">
          Save the provider first; then enter its credential here.
        </p>
      ) : (
        <>
          <p className="rk-fhint" style={{ margin: 0 }} data-testid="sherlock-key-state">
            {state.text}
          </p>
          {auth !== 'none' && k.source !== 'chart' && (
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'flex-end' }}>
              {auth === 'google' ? (
                <label style={{ flex: '1 1 100%', display: 'flex', flexDirection: 'column' }}>
                  <span className="rk-sr">{label}</span>
                  <textarea rows={4} spellCheck={false} autoComplete="off" placeholder={k.set ? 'A key is saved. Paste a new key file’s JSON to replace it' : '{"type": "service_account", …} — the key file’s JSON'} value={typed.key} onChange={(e) => setTyped({ ...typed, key: e.target.value })} data-testid="sherlock-key" className="rk-area" style={{ width: '100%' }} />
                </label>
              ) : (
                <label className="rk-field" style={{ flex: '1 1 240px' }}>
                  <span className="rk-sr">{auth === 'aws' ? 'Access key ID' : label}</span>
                  <input type={auth === 'aws' ? 'text' : 'password'} autoComplete="off" spellCheck={false} placeholder={auth === 'aws' ? 'Access key ID (AKIA…)' : k.set ? '•••••••• saved. Paste a new key to replace it' : 'Paste the key'} value={typed.key} onChange={(e) => setTyped({ ...typed, key: e.target.value })} data-testid="sherlock-key" />
                </label>
              )}
              {auth === 'aws' && (
                <>
                  <label className="rk-field" style={{ flex: '1 1 240px' }}>
                    <span className="rk-sr">Secret access key</span>
                    <input type="password" autoComplete="off" spellCheck={false} placeholder="Secret access key" value={typed.secret} onChange={(e) => setTyped({ ...typed, secret: e.target.value })} data-testid="sherlock-key-secret" />
                  </label>
                  <label className="rk-field" style={{ flex: '1 1 240px' }}>
                    <span className="rk-sr">Session token (optional)</span>
                    <input type="password" autoComplete="off" spellCheck={false} placeholder="Session token (optional)" value={typed.token} onChange={(e) => setTyped({ ...typed, token: e.target.value })} data-testid="sherlock-key-token" />
                  </label>
                </>
              )}
              {k.set && (
                <button className="rk-btn" type="button" disabled={busy} onClick={remove} data-testid="sherlock-key-remove">
                  Remove the saved {label === 'AWS credentials' ? 'credentials' : 'key'}
                </button>
              )}
            </div>
          )}
          {auth !== 'none' && k.source !== 'chart' && (
            <p className="rk-fhint" style={{ margin: 0 }}>
              {k.set && auth !== 'header' ? 'Remove it to use the pod’s identity instead. ' : ''}A new {auth === 'aws' ? 'credential' : 'key'} is saved with Save changes, below.
            </p>
          )}
        </>
      )}
    </section>
  )
}

// ---------- the whole ----------

/**
 * Sherlock's settings, for administrators (P5-05): on or off per cluster
 * and the credential first, as most visits are for them (6.3 review), then
 * the provider and model, view changes, and the daily and monthly spending
 * caps, with what has been spent. One Save saves it all; the credential is
 * kept in a Secret and never shown again.
 */
export function SherlockSettings({ open }: { open: boolean }) {
  const [cfg, setCfg] = useState<SherlockConfig | null>(null)
  const [draft, setDraft] = useState<Settings | null>(null)
  const [typed, setTyped] = useState<Typed>(NOTHING_TYPED)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState('')
  const [formKey, setFormKey] = useState(0)

  useEffect(() => {
    if (!open) return
    let stop = false
    fetchSherlockConfig()
      .then((c) => {
        if (stop) return
        setCfg(c)
        setDraft(c.settings)
        setError('')
      })
      .catch((e: unknown) => {
        if (!stop) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      stop = true
    }
  }, [open])

  if (!cfg || !draft) {
    return (
      <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Sherlock">
        <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} style={{ margin: 0 }}>
          {error || 'Loading…'}
        </p>
      </section>
    )
  }

  const settingsChanged = JSON.stringify(draft) !== JSON.stringify(cfg.settings)
  const value = typedValue(cfg, typed)
  const changed = settingsChanged || value !== ''
  const run = async (fn: () => Promise<string>) => {
    setBusy(true)
    setError('')
    setSaved('')
    try {
      setSaved(await fn())
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const took = (c: SherlockConfig) => {
    setCfg(c)
    setDraft(c.settings)
    setFormKey((n) => n + 1)
  }
  const save = () =>
    run(async () => {
      if (settingsChanged) took(await saveSherlockConfig(draft))
      if (value) {
        took(await saveSherlockKey(value))
        setTyped(NOTHING_TYPED)
        return settingsChanged ? 'Saved, with the new key.' : 'Saved the new key.'
      }
      return 'Saved.'
    })
  const remove = () =>
    run(async () => {
      took(await deleteSherlockKey())
      return 'Removed the saved key.'
    })
  const cap = (n: number) => (n > 0 ? usd(n) : 'no cap')
  const num = (v: string) => {
    const n = Number(v)
    return Number.isFinite(n) && n >= 0 ? Math.round(n * 100) / 100 : 0
  }
  const on = cfg.clusters.filter((c) => cfg.settings.clusters?.[c.id])
  const state = keyState(cfg)
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 24 }} data-testid="sherlock-settings">
      <p className="rk-fhint" style={{ margin: 0 }}>
        Sherlock answers questions about a cluster through the AI model chosen here, reading only what the person asking may read. Nothing is sent to the model for a cluster where Sherlock is off. Watson, which runs commands, is on where Sherlock is.
      </p>

      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }} data-testid="sherlock-summary" aria-label="Sherlock now">
        <span className="rk-pill" data-tone={on.length ? 'ok' : 'muted'}>
          <span className="rk-dot" />
          {on.length ? `On for ${on.map((c) => c.name).join(', ')}` : 'Off for every cluster'}
        </span>
        <span className="rk-pill" data-tone={state.tone}>
          <span className="rk-dot" />
          {state.short}
        </span>
        <span className="rk-pill" data-tone="muted">
          {cfg.models.find((m) => m.id === cfg.settings.model)?.label ?? cfg.settings.model}
        </span>
      </div>

      <section style={{ display: 'flex', flexDirection: 'column', gap: 6 }} aria-label="Clusters">
        <h3 className="rk-h">On for</h3>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }} role="group" aria-label="Clusters Sherlock answers about">
          {cfg.clusters.map((c) => (
            <label key={c.id} className="rk-tcheck" style={{ color: 'var(--fg)' }}>
              <input type="checkbox" checked={!!draft.clusters?.[c.id]} onChange={(e) => setDraft({ ...draft, clusters: { ...(draft.clusters ?? {}), [c.id]: e.target.checked } })} data-testid="sherlock-cluster" data-cluster={c.id} />
              <span>{c.name}</span>
            </label>
          ))}
        </div>
      </section>

      <Credential cfg={cfg} draft={draft} busy={busy} typed={typed} setTyped={setTyped} remove={() => void remove()} />

      <section style={{ display: 'flex', flexDirection: 'column', gap: 12 }} aria-label="AI provider">
        <h3 className="rk-h">AI provider and model</h3>
        <ProviderForm key={formKey} draft={draft} setDraft={setDraft} cfg={cfg} />
      </section>

      <section style={{ display: 'flex', flexDirection: 'column', gap: 4 }} aria-label="View changes">
        <h3 className="rk-h">The person’s view</h3>
        <label className="rk-tcheck" style={{ color: 'var(--fg)' }}>
          <input type="checkbox" checked={draft.viewControl !== false} onChange={(e) => setDraft({ ...draft, viewControl: e.target.checked })} data-testid="sherlock-view-control" aria-describedby="sherlock-view-control-hint" />
          <span>Let Sherlock change the view</span>
        </label>
        <p className="rk-fhint" style={{ margin: 0 }} id="sherlock-view-control-hint">
          Sherlock may open tabs and details, set filters, search, sort, show columns and highlight rows in the asking person’s own window while it answers. It never changes a cluster or what others see. Off, it still reads what the person sees.
        </p>
      </section>

      <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Spending caps">
        <h3 className="rk-h">Spending caps</h3>
        <div className="rk-frow" style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
            Daily cap (US$)
            <span className="rk-field" style={{ width: 140 }}>
              <input type="number" min={0} step={1} value={draft.dailyUSD} onChange={(e) => setDraft({ ...draft, dailyUSD: num(e.target.value) })} data-testid="sherlock-daily" aria-describedby="sherlock-caps-hint" />
            </span>
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600 }}>
            Monthly cap (US$)
            <span className="rk-field" style={{ width: 140 }}>
              <input type="number" min={0} step={1} value={draft.monthlyUSD} onChange={(e) => setDraft({ ...draft, monthlyUSD: num(e.target.value) })} data-testid="sherlock-monthly" aria-describedby="sherlock-caps-hint" />
            </span>
          </label>
        </div>
        <p className="rk-fhint" style={{ margin: 0 }} id="sherlock-caps-hint" data-testid="sherlock-spend">
          Spent today {usd(cfg.spend.dayUSD)} of {cap(cfg.settings.dailyUSD)} ({cfg.spend.dayAsked} {cfg.spend.dayAsked === 1 ? 'question' : 'questions'}); this month {usd(cfg.spend.monthUSD)} of {cap(cfg.settings.monthlyUSD)}. Counted from the tokens each answer used, at the model’s price; 0 is no cap. At a cap, new questions are refused until the day or month ends (UTC).
        </p>
      </section>

      <SaveBar
        changed={changed}
        busy={busy}
        onSave={() => void save()}
        onUndo={() => {
          setDraft(cfg.settings)
          setTyped(NOTHING_TYPED)
          setFormKey((n) => n + 1)
        }}
        error={error}
        saved={saved}
        testId="sherlock-save"
      />
    </div>
  )
}
