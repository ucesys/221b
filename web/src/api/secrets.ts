// Secret editing (Phase 10, SE-01 to SE-06): what the person may do with
// Secrets here, one value shown on request where the chart allows it, and
// the edits, written as the person.

import { apiPath } from '../lib/router'
import { unauthorized } from './auth'

export interface SecretsConfig {
  read: boolean
  reveal: boolean
  edit: boolean
}

export interface SecretValue {
  key: string
  value: string
  /** The value is binary, given as base64. */
  base64?: boolean
  bytes: number
}

export interface SecretEdit {
  set?: Record<string, string>
  /** The keys of set whose values are typed as base64. */
  base64?: string[]
  remove?: string[]
  resourceVersion?: string
  name?: string
  type?: string
}

export class SecretsError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers: body === undefined ? { Accept: 'application/json' } : { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      cache: 'no-store',
    })
  } catch {
    throw new SecretsError('The console did not answer. Check the connection and try again.', 0)
  }
  if (unauthorized(res)) throw new SecretsError('Your session has ended; sign in again.', 401)
  if (res.status === 204 || res.status === 201) return undefined as T
  const data = (await res.json().catch(() => null)) as (T & { error?: string }) | null
  if (!res.ok) throw new SecretsError(data?.error || `The console answered ${res.status}.`, res.status)
  return data as T
}

export const fetchSecretsConfig = () => call<SecretsConfig>('GET', apiPath('secrets/config'))
export const revealSecretValue = (ns: string, name: string, key: string) => call<SecretValue>('GET', apiPath(`secrets/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/value`) + '?key=' + encodeURIComponent(key))
export const updateSecret = (ns: string, name: string, edit: SecretEdit) => call<void>('PUT', apiPath(`secrets/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`), edit)
export const createSecret = (ns: string, edit: SecretEdit) => call<void>('POST', apiPath(`secrets/${encodeURIComponent(ns)}`), edit)
