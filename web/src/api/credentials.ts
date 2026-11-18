// Administration › Credentials (0.12.1, CR-01 to CR-14): who the console is when
// it signs in somewhere, and where that applies. A secret is written once
// and never read back: no answer holds one, and the page keeps none.

import { unauthorized } from './auth'
import type { CertPin, PresentedCert } from './certificates'

export type CredentialKind = 'token' | 'userpass' | 'accesskey' | 'ssh' | 'githubapp' | 'clientcert' | 'oauth2' | 'none'
export type SendMethod = 'bearer' | 'basic' | 'header' | 'query' | 'cookie'
/** What a picker takes a credential for: each offers only what it supports. */
export type CredentialUse = 'git' | 'helm' | 's3' | 'confluence-cloud' | 'confluence-dc' | 'http'

export interface SendAs {
  method?: SendMethod
  username?: string
  header?: string
  prefix?: string
  param?: string
  cookie?: string
}

export interface CredentialTest {
  at: string
  ok: boolean
  target?: string
  method?: string
  status?: number
  text: string
  suggest?: SendMethod
  by?: string
  /** The certificate the address presented, when it is not trusted or is the pinned one (CR-15). */
  certificate?: PresentedCert
}

export interface CredentialRef {
  what: string
  where?: string
}

/** A credential as the page sees it: never its secret. Others than SuperAdmins see id, name, kind, endpoint and usedBy only. */
export interface Credential {
  id: string
  name: string
  kind: CredentialKind
  endpoint: string
  description?: string
  expires?: string
  caPEM?: string
  /** The endpoint's certificate a SuperAdmin trusted (CR-16); set by Trust only. */
  pinnedCert?: CertPin
  sendAs?: SendAs
  username?: string
  region?: string
  hostKey?: string
  appId?: string
  installationId?: string
  tokenURL?: string
  clientId?: string
  scope?: string
  certPEM?: string
  /** The secret parts kept, by name. */
  set?: string[]
  lastTest?: CredentialTest
  created?: string
  createdBy?: string
  changed?: string
  changedBy?: string
  from?: string[]
  usedBy: CredentialRef[]
  expiring?: boolean
  how?: string
}

/** A credential's secret part, sent once and never shown again. */
export interface CredentialSecret {
  token?: string
  password?: string
  accessKeyId?: string
  secretAccessKey?: string
  sessionToken?: string
  privateKey?: string
  passphrase?: string
  clientSecret?: string
  keyPEM?: string
}

export type CredentialInput = Omit<Credential, 'id' | 'usedBy' | 'set' | 'lastTest' | 'created' | 'createdBy' | 'changed' | 'changedBy' | 'from' | 'expiring' | 'how' | 'pinnedCert'> & { secret?: CredentialSecret }

export interface Migration {
  at?: string
  created?: { id: string; name: string; from: string[] }[]
  notes?: string[]
}

export interface CredentialsView {
  credentials: Credential[]
  mayEdit: boolean
  migration?: Migration
}

export interface CredentialChoice {
  id: string
  name: string
  kind: CredentialKind
  endpoint: string
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers: body === undefined ? { Accept: 'application/json' } : { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
    throw new Error('The console did not answer. Check the connection and try again.')
  }
  if (unauthorized(res)) throw new Error('Your session has ended; sign in again.')
  if (res.status === 204) return undefined as T
  const data = (await res.json().catch(() => null)) as (T & { error?: string }) | null
  if (!res.ok) throw new Error(data?.error || `The console answered ${res.status}.`)
  return data as T
}

const base = '/api/v1/credentials'
export const fetchCredentials = () => req<CredentialsView>('GET', base)
/** The credentials that serve a use at an address: a picker preselects the only one, and makes the person choose among several. */
export const fetchCredentialChoices = (use: CredentialUse, address: string) => req<{ choices: CredentialChoice[] }>('GET', `${base}/choices?use=${encodeURIComponent(use)}&address=${encodeURIComponent(address)}`).then((v) => v.choices ?? [])
export const createCredential = (input: CredentialInput) => req<Credential>('POST', base, input)
/** Changes a credential; a secret given replaces the kept one for every use. */
export const saveCredential = (id: string, input: CredentialInput) => req<Credential>('PUT', `${base}/${encodeURIComponent(id)}`, input)
export const deleteCredential = (id: string) => req<void>('DELETE', `${base}/${encodeURIComponent(id)}`)
/** Tests a saved credential against its endpoint, or an address or name under it. */
export const testCredential = (id: string, target: string) => req<CredentialTest>('POST', `${base}/${encodeURIComponent(id)}/test`, target ? { target } : {})
export const dismissMigration = () => req<void>('POST', `${base}/migration/dismiss`)
/** Trusts the certificate the address (target, as tested) presents, when it is the one a Test showed (CR-16). */
export const trustCredentialCert = (id: string, sha256: string, target: string) => req<{ pinnedCert: CertPin; certificate: PresentedCert }>('POST', `${base}/${encodeURIComponent(id)}/trust`, { sha256, ...(target ? { target } : {}) })
/** Lets the pinned certificate go (CR-19). */
export const forgetCredentialCert = (id: string) => req<void>('DELETE', `${base}/${encodeURIComponent(id)}/trust`)

export const KIND_LABEL: Record<CredentialKind, string> = {
  token: 'Token',
  userpass: 'User + password/token',
  accesskey: 'Access key',
  ssh: 'SSH key',
  githubapp: 'GitHub App',
  clientcert: 'Client certificate',
  oauth2: 'OAuth2 client credentials',
  none: 'None / pod identity',
}

export const METHOD_LABEL: Record<SendMethod, string> = {
  bearer: 'Bearer',
  basic: 'Basic (user:token)',
  header: 'Custom header',
  query: 'Query parameter',
  cookie: 'Cookie',
}

/** Whether a kind's token goes on HTTP requests the way Send as says. */
export const sendsHTTP = (k: CredentialKind) => k === 'token' || k === 'githubapp' || k === 'oauth2'

/** How a credential signs in, in words, never a value. */
export function sendText(c: Pick<Credential, 'kind' | 'sendAs' | 'username'>): string {
  if (c.kind === 'userpass') return `Basic as ${c.username ?? ''}`
  if (!sendsHTTP(c.kind)) return KIND_LABEL[c.kind]
  const sa = c.sendAs ?? {}
  switch (sa.method) {
    case 'basic':
      return `Basic as ${sa.username ?? ''}`
    case 'header':
      return `Header ${sa.header ?? ''}${sa.prefix ? ` (“${sa.prefix}…”)` : ''}`
    case 'query':
      return `Query parameter ${sa.param ?? ''}`
    case 'cookie':
      return `Cookie ${sa.cookie ?? ''}`
  }
  return 'Bearer'
}

/** Days until a credential expires (negative when it has), or null without an expiry. */
export function daysLeft(expires: string | undefined, now = Date.now()): number | null {
  if (!expires) return null
  const t = Date.parse(expires)
  if (Number.isNaN(t)) return null
  return Math.floor((t - now) / 86_400_000)
}

/** The credentials expiring within 14 days, or expired (CR-08). */
export const expiringSoon = (list: Credential[], now = Date.now()) =>
  list.filter((c) => {
    const d = daysLeft(c.expires, now)
    return d !== null && d <= 14
  })
