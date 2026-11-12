import type { ReactNode } from 'react'
import type { ProviderAuth, ProviderKind, SherlockProvider } from '../api/sherlock'

// What every provider form shares (Phase 6 review; since Phase 12 the
// providers of Settings › AI › Providers, where Sherlock's settings were):
// the presets, the kinds of authentication each kind takes, and the
// headers as lines.

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
  {
    id: 'bedrock',
    label: 'Amazon Bedrock',
    p: { kind: 'bedrock', region: 'us-east-1' },
    models: ['us.anthropic.claude-opus-5-5-v1:0', 'us.anthropic.claude-sonnet-5-5-v1:0', 'amazon.nova-pro-v1:0', 'meta.llama4-maverick-17b-instruct-v1:0'],
  },
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

export const ENDPOINT_HINT: Record<string, string> = {
  anthropic: 'https://api.anthropic.com',
  'anthropic-gateway': 'https://ai-gateway.corp.example/anthropic',
  openai: 'https://api.openai.com/v1',
  azure: 'https://RESOURCE.openai.azure.com/openai/deployments/DEPLOYMENT',
  compatible: 'https://ai-gateway.corp.example/v1 or http://ollama.ai:11434/v1',
  bedrock: 'https://bedrock-runtime.REGION.amazonaws.com',
  vertex: 'https://REGION-aiplatform.googleapis.com',
}

export const AUTHS: Record<ProviderKind, [ProviderAuth, string][]> = {
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

export function defaultAuth(kind: ProviderKind): ProviderAuth {
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
export function clean(p: SherlockProvider): SherlockProvider | undefined {
  const out: SherlockProvider = { kind: p.kind }
  for (const k of ['endpoint', 'auth', 'authHeader', 'authPrefix', 'query', 'region', 'project', 'caCert'] as const) {
    const v = p[k]
    if (k === 'authPrefix' ? v !== undefined && p.authHeader : v) (out as unknown as Record<string, unknown>)[k] = v
  }
  if (p.headers && Object.keys(p.headers).length) out.headers = p.headers
  if (out.kind === 'anthropic' && Object.keys(out).length === 1) return undefined
  return out
}

export function Field({ label, hint, children, wide }: { label: string; hint?: string; children: ReactNode; wide?: boolean }) {
  return (
    <label className={wide ? 'rk-ffield rk-fwide' : 'rk-ffield'} style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5, fontWeight: 600, minWidth: 0 }}>
      {label}
      {children}
      {hint && (
        <span className="rk-fhint" style={{ fontWeight: 400 }}>
          {hint}
        </span>
      )}
    </label>
  )
}
