// The clusters the console shows (P3-04 to P3-09): their connections as
// the server summarizes them, and the administrator's changes to them. A
// credential goes to the server once and never comes back (X-04).

import { useEffect, useSyncExternalStore } from 'react'
import { unauthorized } from './auth'
import type { Tone } from './types'

export type Env = 'production' | 'staging' | 'development' | 'other'
export type CAMode = 'pem' | 'system' | 'skip'
export type AuthMethod = 'token' | 'kubeconfig' | 'cert' | 'oidc' | 'incluster' | 'none'
export type AccessMode = 'read' | 'write'

/** What a saved credential says about itself; never the credential. */
export interface CredInfo {
  who?: string
  context?: string
  cluster?: string
  issuer?: string
  clientID?: string
  expires?: string
  set: string
}

/** One step of a connection test (P3-06). */
export interface TestStep {
  k: string
  tone: Tone
  t: string
}

export interface TestResult {
  steps: TestStep[]
  tone: Tone
  summary: string
  error?: string
  version?: string
  nodes?: number
  nodesMore?: boolean
  who?: string
  impersonate: boolean
}

/** A connection as GET /api/v1/clusters lists it. */
export interface ClusterSummary {
  id: string
  name: string
  env: Env
  description?: string
  server: string
  ca: CAMode
  caInfo?: string
  auth: AuthMethod
  mode: AccessMode
  local?: boolean
  cred: CredInfo
  added: string
  updated: string
  state: 'connecting' | 'connected' | 'disconnected' | 'failed'
  message?: string
  since?: string
  version?: string
  nodes: number
  pods: number
  synced: boolean
  expiring?: boolean
  expired?: boolean
  lastTest?: TestResult
  lastTested?: string
}

/** The editor's form as the server takes it: the spec and, when given, the credential. */
export interface Draft {
  id?: string
  name: string
  env: Env
  description: string
  server: string
  ca: CAMode
  auth: AuthMethod
  mode: AccessMode
  token?: string
  kubeconfig?: string
  context?: string
  certPem?: string
  keyPem?: string
  caPem?: string
  issuer?: string
  clientID?: string
  clientSecret?: string
  scopes?: string
}

export const ENVS: { id: Env; label: string; short: string; tone: Tone }[] = [
  { id: 'production', label: 'Production', short: 'prod', tone: 'warn' },
  { id: 'staging', label: 'Staging', short: 'staging', tone: 'info' },
  { id: 'development', label: 'Development', short: 'dev', tone: 'muted' },
  { id: 'other', label: 'Other', short: 'other', tone: 'muted' },
]

export function envOf(id: string): { id: Env; label: string; short: string; tone: Tone } {
  return ENVS.find((e) => e.id === id) ?? ENVS[3]!
}

export const AUTH_LABELS: Record<AuthMethod, string> = {
  token: 'Service account token',
  kubeconfig: 'Kubeconfig',
  oidc: 'OIDC sign-in',
  cert: 'Client certificate',
  incluster: 'The console’s own ServiceAccount',
  none: 'No credentials',
}

export const CA_LABELS: Record<CAMode, string> = { pem: 'Cluster CA certificate', system: 'System trust store', skip: 'Don’t verify' }

/** A failed request: the server's message and, for a form, the field at fault. */
export class ClusterError extends Error {
  field?: string
  status: number
  constructor(message: string, status: number, field?: string) {
    super(message)
    this.status = status
    this.field = field
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers: body === undefined ? { Accept: 'application/json' } : { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
    throw new ClusterError('The server could not be reached.', 0)
  }
  if (!res.ok) {
    if (unauthorized(res)) throw new ClusterError('Your session has ended; sign in again.', 401)
    let msg = `The server answered ${res.status}.`
    let field: string | undefined
    try {
      const b = (await res.json()) as { error?: string; field?: string }
      if (b.error) msg = b.error
      field = b.field
    } catch {
      // Not JSON: keep the status.
    }
    throw new ClusterError(msg, res.status, field)
  }
  return (await res.json()) as T
}

export function fetchClusters(): Promise<ClusterSummary[]> {
  return call<{ clusters: ClusterSummary[] }>('GET', '/api/v1/clusters').then((r) => r.clusters)
}

export function fetchCluster(id: string): Promise<ClusterSummary> {
  return call<ClusterSummary>('GET', '/api/v1/clusters/' + encodeURIComponent(id))
}

/** Adds a connection, or changes the one with the id. */
export async function saveCluster(d: Draft, id?: string): Promise<ClusterSummary> {
  const body: Draft = { ...d }
  delete body.id
  const sum = id ? await call<ClusterSummary>('PUT', '/api/v1/clusters/' + encodeURIComponent(id), body) : await call<ClusterSummary>('POST', '/api/v1/clusters', body)
  clusterList.refresh()
  return sum
}

export async function removeCluster(id: string): Promise<void> {
  await call<{ removed: boolean }>('DELETE', '/api/v1/clusters/' + encodeURIComponent(id))
  clusterList.refresh()
}

/** Tests a draft before it is saved; credential fields left empty mean the saved ones, for the connection with the id. */
export function testDraft(d: Draft): Promise<TestResult> {
  return call<TestResult>('POST', '/api/v1/clusters/test', d)
}

/** Tests a saved connection; the result is kept for its cluster page. */
export async function testCluster(id: string): Promise<TestResult> {
  const r = await call<TestResult>('POST', '/api/v1/clusters/' + encodeURIComponent(id) + '/test')
  clusterList.refresh()
  return r
}

type Listener = () => void

/**
 * The list of clusters, shared by the cluster menu and the cluster page
 * and read again every few seconds while something shows it, so a
 * connection coming up or going down shows without a reload.
 */
class ClusterList {
  state: { clusters: ClusterSummary[]; loaded: boolean; error: string } = { clusters: [], loaded: false, error: '' }
  private listeners = new Set<Listener>()
  private timer: ReturnType<typeof setInterval> | undefined
  private inflight: Promise<void> | null = null

  constructor(private every = 10_000) {}

  subscribe = (fn: Listener): (() => void) => {
    this.listeners.add(fn)
    if (this.listeners.size === 1) {
      void this.refresh()
      this.timer = setInterval(() => void this.refresh(), this.every)
    }
    return () => {
      this.listeners.delete(fn)
      if (this.listeners.size === 0) {
        clearInterval(this.timer)
        this.timer = undefined
      }
    }
  }

  getState = () => this.state

  refresh(): Promise<void> {
    if (this.inflight) return this.inflight
    this.inflight = fetchClusters()
      .then((clusters) => this.commit({ clusters, loaded: true, error: '' }))
      .catch((e: unknown) => this.commit({ ...this.state, loaded: true, error: e instanceof Error ? e.message : String(e) }))
      .finally(() => {
        this.inflight = null
      })
    return this.inflight
  }

  private commit(next: typeof this.state) {
    this.state = next
    for (const fn of this.listeners) fn()
  }
}

export const clusterList = new ClusterList()

/** The clusters, kept current while a component shows them. */
export function useClusters(): { clusters: ClusterSummary[]; loaded: boolean; error: string } {
  const state = useSyncExternalStore(clusterList.subscribe, clusterList.getState, clusterList.getState)
  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === 'visible') void clusterList.refresh()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [])
  return state
}

/** "expires in 6 days", "expired 2 hours ago", or "" when the credential has no expiry. */
export function expiryText(cred: CredInfo, now: number): string {
  if (!cred.expires) return ''
  const at = new Date(cred.expires).getTime()
  const d = at - now
  const span = spanText(Math.abs(d))
  return d > 0 ? `expires in ${span}` : `expired ${span} ago`
}

/** "3 days", "6 hours", "12 minutes". */
export function spanText(ms: number): string {
  const min = Math.round(ms / 60_000)
  if (min < 60) return `${min} minute${min === 1 ? '' : 's'}`
  const h = Math.round(min / 60)
  if (h < 48) return `${h} hour${h === 1 ? '' : 's'}`
  const d = Math.round(h / 24)
  if (d < 60) return `${d} day${d === 1 ? '' : 's'}`
  const mo = Math.round(d / 30)
  if (mo < 24) return `${mo} month${mo === 1 ? '' : 's'}`
  const y = Math.round(d / 365)
  return `${y} year${y === 1 ? '' : 's'}`
}

/** The saved credential in a sentence, for the cluster page and the editor. */
export function credText(c: Pick<ClusterSummary, 'auth' | 'cred'>, now: number): string {
  const cr = c.cred
  const set = cr.set ? `saved ${ageText(new Date(cr.set).getTime(), now)}` : ''
  const exp = expiryText(cr, now)
  switch (c.auth) {
    case 'token':
      return ['Token' + (cr.who ? ` for ${cr.who}` : ''), set, exp].filter(Boolean).join(' · ')
    case 'kubeconfig':
      return [`Kubeconfig, context ${cr.context || '?'}` + (cr.who ? ` (user ${cr.who})` : ''), set, exp].filter(Boolean).join(' · ')
    case 'oidc':
      return `Issuer ${cr.issuer || '?'}, client ${cr.clientID || '?'}`
    case 'cert':
      return ['Client certificate' + (cr.who ? ` for ${cr.who}` : ''), set, exp].filter(Boolean).join(' · ')
    case 'incluster':
      return 'The console’s own ServiceAccount, in the cluster it runs in'
    default:
      return 'No credentials: the API server is open'
  }
}

/** "just now", "5 minutes ago". */
export function ageText(at: number, now: number): string {
  const ms = now - at
  if (ms < 60_000) return 'just now'
  return `${spanText(ms)} ago`
}

/** The connection's state as the menu and the page show it. */
export function stateOf(c: Pick<ClusterSummary, 'state' | 'synced' | 'expired' | 'expiring'>): { tone: Tone; text: string } {
  if (c.state === 'failed') return { tone: 'err', text: 'Can’t start' }
  if (c.state === 'disconnected') return { tone: 'err', text: 'Can’t connect' }
  if (c.state === 'connecting') return { tone: 'muted', text: 'Connecting' }
  if (!c.synced) return { tone: 'info', text: 'Listing' }
  if (c.expired) return { tone: 'err', text: 'Connected, credentials expired' }
  if (c.expiring) return { tone: 'warn', text: 'Connected, credentials expiring' }
  return { tone: 'ok', text: 'Connected' }
}
