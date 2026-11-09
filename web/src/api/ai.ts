// The AI agents (Phase 12): providers, agents, their instructions on the way
// from a draft to live, test cases and pilots. Settings › AI, for SuperAdmins.

import { SherlockError, type SherlockModel, type SherlockProvider } from './sherlock'
import { unauthorized } from './auth'

export interface KeyInfo {
  set: boolean
  source?: 'console' | 'chart'
  setAt?: string
  setBy?: string
  store: 'secret' | 'memory'
}

/** One place agents' models come from; its credential is never sent here. */
export interface AIProvider extends SherlockProvider {
  id: string
  name: string
  /** The model agents use on it when they name none. */
  model?: string
  priceIn?: number
  priceOut?: number
  /** The context window of its default model in tokens; 0 or none is not known (Phase 12, wave 5). */
  contextWindow?: number
}

/** A provider's last connection test, kept by the console. */
export interface ProviderTest {
  at: string
  ok: boolean
  model?: string
  /** It ran without a credential. */
  keyless?: boolean
  error?: string
}

export interface ProviderView extends AIProvider {
  resolved: SherlockProvider
  default: boolean
  /** The chart fixes it (sherlock.lockProvider). */
  locked: boolean
  needsKey: boolean
  key: KeyInfo
  usedBy?: string[] | null
  tested?: ProviderTest
  /** It needs no key: its last test passed without one, with its settings as they are. */
  keylessOK?: boolean
}

export interface AgentSettings {
  on?: boolean
  provider?: string
  model?: string
  dailyUSD?: number
  monthlyUSD?: number
  clusters?: string[]
  tools?: string[]
  /** The context window of its own model in tokens. */
  contextWindow?: number
  /** Sherlock's compact form: '' when needed, 'always' or 'never'. */
  compact?: CompactMode
}

export type CompactMode = '' | 'always' | 'never'

/** Tokens of an agent's instructions and tool definitions. */
export interface Footprint {
  instructions: number
  tools: number
  toolCount: number
}

/** What an agent's instructions and tools take of its model's context (Phase 12, wave 5). */
export interface AgentFootprint extends Footprint {
  whole: Footprint
  window: number
  windowFrom?: 'agent' | 'provider' | 'model'
  compact: boolean
  mode: CompactMode
  share: number
  maxTokens?: number
}

export interface TestSummary {
  at: string
  cases: number
  passed: number
  replays: number
  same: number
  costUSD: number
  failed?: string[]
  error?: string
}

export type PromptState = 'in-sync' | 'edited' | 'baseline-changed'
export type Stage = 'draft' | 'test' | 'pilot'

export interface AgentView {
  id: string
  name: string
  purpose: string
  tools: string[]
  on: boolean
  viewControl?: boolean
  settings: AgentSettings
  enabled: boolean
  providerId: string
  providerName: string
  model: string
  defaultModel: boolean
  inForce: string[]
  clusters?: string[] | null
  state: PromptState
  stage?: Stage
  lastTest?: TestSummary
  baselineCheck?: TestSummary
  valuesKey: string
  spend: { dayUSD: number; monthUSD: number; dayAsked: number; monthAsked: number }
  unusable?: string
  testing?: boolean
  footprint?: AgentFootprint
}

export interface AIOverview {
  providers: ProviderView[]
  agents: AgentView[]
  models: SherlockModel[]
  defaultModel: string
  dailyUSD: number
  monthlyUSD: number
  spend: { day: string; dayUSD: number; month: string; monthUSD: number }
  singleProvider: boolean
  viewControl: boolean
  clusters: { id: string; name: string; env: string }[]
  running: number
  knowledge: string
}

export interface CaseResult {
  id: string
  name: string
  base?: boolean
  passed: boolean
  why?: string[]
  answer: string
  tools?: string[]
  handOvers?: string[]
  costUSD: number
}

export interface ReplayResult {
  question: string
  cluster: string
  before: string
  after: string
  same: boolean
  costUSD: number
  error?: string
}

export interface TestRun {
  rev: number
  at: string
  by: string
  cases: number
  passed: number
  results?: CaseResult[]
  replays?: ReplayResult[]
  same: number
  costUSD: number
  estimate: number
  error?: string
}

export interface Pilot {
  people?: string[]
  clusters?: string[]
  started: string
  until: string
  by: string
}

export interface Draft {
  rev: number
  house: string
  notes?: Record<string, string>
  stage: Stage
  by: string
  at: string
  test?: TestRun
  pilot?: Pilot
  request?: number
  /** The version it restores, which may go live untested. */
  restored?: number
  /** The edits of the built-in text it makes (Phase 12, wave 5). */
  parts?: Record<string, string>
}

export interface Version {
  n: number
  house: string
  notes?: Record<string, string>
  by: string
  at: string
  why?: string
  parts?: Record<string, string>
}

export interface Instructions {
  agent: string
  core: string
  baseline: string
  /** With the baseline changed under an edit: the baseline the edit was made over. */
  previousBaseline?: string
  house: string
  notes?: Record<string, string>
  state: PromptState
  valuesKey: string
  live: number
  versions: Version[]
  draft?: Draft
  warnings?: string[]
  maxWords: number
  baselineCheck?: TestRun
  /** The agent's built-in text by part, pre-filled and editable (Phase 12, wave 5). */
  parts: PromptPart[]
  maxPartWords: number
}

/** One editable part of an agent's built-in text. */
export interface PromptPart {
  name: 'core' | 'compact'
  label: string
  builtIn: string
  /** In force: its edit, or the built-in text. */
  text: string
  edited: boolean
  /** The built-in text changed since the edit was made. */
  builtInChanged?: boolean
  /** The draft's text of the part. */
  draft?: string
}

export interface PilotEntry {
  n: number
  agent: string
  version: string
  at: string
  who: string
  cluster: string
  question: string
  answer: string
  model: string
  costUSD: number
  rating?: number
}

/** The notes' key for every cluster. */
export const ALL_CLUSTERS = '*'

/** The most words house instructions or one cluster's notes may have. */
export const MAX_WORDS = 2000

export const words = (s: string | undefined) => (s?.trim() ? s.trim().split(/\s+/).length : 0)

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers: body === undefined ? { Accept: 'application/json' } : { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
    throw new SherlockError('The console did not answer. Check the connection and try again.', 0)
  }
  if (unauthorized(res)) throw new SherlockError('Your session has ended; sign in again.', 401)
  const data = (await res.json().catch(() => null)) as (T & { error?: string }) | null
  if (!res.ok) throw new SherlockError(data?.error || `The console answered ${res.status}.`, res.status)
  return data as T
}

const enc = encodeURIComponent

export const fetchAI = () => call<AIOverview>('GET', '/api/v1/ai')
export const saveLimits = (dailyUSD: number, monthlyUSD: number) => call<AIOverview>('PUT', '/api/v1/ai/limits', { dailyUSD, monthlyUSD })
export const addProvider = (p: AIProvider) => call<AIOverview>('POST', '/api/v1/ai/providers', p)
export const saveProvider = (p: AIProvider) => call<AIOverview>('PUT', `/api/v1/ai/providers/${enc(p.id)}`, p)
export const removeProvider = (id: string) => call<AIOverview>('DELETE', `/api/v1/ai/providers/${enc(id)}`)
export const saveProviderKey = (id: string, key: string) => call<AIOverview>('PUT', `/api/v1/ai/providers/${enc(id)}/key`, { key })
export const removeProviderKey = (id: string) => call<AIOverview>('DELETE', `/api/v1/ai/providers/${enc(id)}/key`)
export interface ProviderTestResult {
  ok: boolean
  model: string
  ms: number
  answer?: string
  error?: string
  /** It ran without a credential. */
  keyless?: boolean
}
export const testProvider = (id: string, model?: string) => call<ProviderTestResult>('POST', `/api/v1/ai/providers/${enc(id)}/test`, model ? { model } : {})

// --- AI settings, round 2 (Phase 12, wave 5) ---
/** A model a provider's endpoint lists. */
export interface ListedModel {
  id: string
  contextWindow?: number
  owner?: string
}
export interface ModelList {
  models: ListedModel[]
  /** Why they could not be listed; the model can still be typed. */
  error?: string
}
/** Tests the provider dialog's settings before they are saved; id names the saved provider whose credential goes along to its own endpoint. */
export const tryProvider = (provider: AIProvider, id?: string, model?: string) => call<ProviderTestResult>('POST', '/api/v1/ai/providers/try', { provider, ...(id ? { id } : {}), ...(model ? { model } : {}) })
export const fetchModels = (id: string) => call<ModelList>('GET', `/api/v1/ai/providers/${enc(id)}/models`)
export const fetchModelsFor = (provider: AIProvider, id?: string) => call<ModelList>('POST', '/api/v1/ai/providers/models', { provider, ...(id ? { id } : {}) })
export const backToDraft = (id: string) => call<Instructions>('POST', `/api/v1/ai/agents/${enc(id)}/draft/back`)

/** "16,384 tokens", or "" for none. */
export function tokensText(n?: number): string {
  return n ? `${n.toLocaleString('en')} tokens` : ''
}

export interface AgentChange extends AgentSettings {
  sherlockClusters?: string[]
  viewControl?: boolean
  confirmSingleProvider?: boolean
}
export const saveAgent = (id: string, change: AgentChange) => call<AIOverview>('PUT', `/api/v1/ai/agents/${enc(id)}`, change)
export const fetchInstructions = (id: string) => call<Instructions>('GET', `/api/v1/ai/agents/${enc(id)}/instructions`)
export const fetchPrompt = (id: string, cluster: string, draft: boolean) => call<{ instructions: string; cluster: string; model: string; tools?: string[]; compact?: boolean }>('GET', `/api/v1/ai/agents/${enc(id)}/prompt?cluster=${enc(cluster)}${draft ? '&draft=1' : ''}`)
/** Saves a draft; parts, when given, are the edits of the built-in text (absent keeps those in force). */
export const saveDraft = (id: string, house: string, notes: Record<string, string>, parts?: Record<string, string>) => call<Instructions>('PUT', `/api/v1/ai/agents/${enc(id)}/draft`, { house, notes, ...(parts ? { parts } : {}) })
export const discardDraft = (id: string) => call<Instructions>('DELETE', `/api/v1/ai/agents/${enc(id)}/draft`)
export const resolveBaseline = (id: string, action: 'keep' | 'take') => call<Instructions>('POST', `/api/v1/ai/agents/${enc(id)}/baseline`, { action })
export const estimateTest = (id: string, replays: number, live = false) => call<{ estimateUSD: number; cases: number; replays: number }>('POST', `/api/v1/ai/agents/${enc(id)}/test`, { replays, estimate: true, live })
export const startTest = (id: string, replays: number, live = false) => call<{ estimateUSD: number; started: boolean }>('POST', `/api/v1/ai/agents/${enc(id)}/test`, { replays, live })
export const startPilot = (id: string, people: string[], clusters: string[], hours: number) => call<Instructions>('POST', `/api/v1/ai/agents/${enc(id)}/pilot`, { people, clusters, hours })
export const stopPilot = (id: string) => call<Instructions>('DELETE', `/api/v1/ai/agents/${enc(id)}/pilot`)
export const fetchPilot = (id: string) => call<{ entries: PilotEntry[] }>('GET', `/api/v1/ai/agents/${enc(id)}/pilot`)
export const ratePilot = (n: number, rating: number) => call<{ rated: boolean }>('POST', `/api/v1/ai/pilot/${n}/rate`, { rating })
/** Live at once when no other SuperAdmin could approve; otherwise a request on Approvals. */
export const makeLive = (id: string) => call<{ waiting?: boolean; request?: { id: number }; live?: Version }>('POST', `/api/v1/ai/agents/${enc(id)}/live`)
export const restoreVersion = (id: string, n: number) => call<{ waiting?: boolean; request?: { id: number }; live?: Version }>('POST', `/api/v1/ai/agents/${enc(id)}/versions/${n}/restore`)
/** Saves an answer of the person's own conversation as a test case. */
export const saveTestCase = (cluster: string, message: number, name: string, points: string[], agent?: string) => call<{ id: string }>('POST', '/api/v1/ai/tests', { cluster, message, name, points, ...(agent ? { agent } : {}) })

/** A kind of provider, for people. */
export function kindLabel(kind: string): string {
  return kind === 'openai' ? 'OpenAI-compatible' : kind === 'bedrock' ? 'Amazon Bedrock' : kind === 'vertex' ? 'Google Vertex AI' : 'Anthropic'
}

/** The words for where an agent's instructions stand. */
export function stateLabel(s: PromptState): string {
  return s === 'in-sync' ? 'In sync' : s === 'edited' ? 'Edited in app' : 'Baseline changed'
}

export function stateTone(s: PromptState): string {
  return s === 'in-sync' ? 'ok' : s === 'edited' ? 'info' : 'warn'
}

/** A test run in one line: "12 of 12 cases passed · 18 of 20 replays the same · $1.40"; no cost without `usd`. */
export function testLine(t: TestSummary | TestRun, usd?: (n: number) => string): string {
  const replays = 'replays' in t ? (Array.isArray(t.replays) ? t.replays.length : t.replays) : 0
  const parts = [`${t.passed} of ${t.cases} recorded case${t.cases === 1 ? '' : 's'} passed`]
  if (replays) parts.push(`replay of ${replays} conversation${replays === 1 ? '' : 's'}: ${t.same} the same`)
  // Without a way to say dollars, costs are hidden (CF-40).
  if (usd) parts.push(`about ${usd(t.costUSD)}`)
  if (t.error) parts.push(t.error)
  return parts.join(' · ')
}
