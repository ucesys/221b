import { unauthorized } from './auth'
import { apiPath } from '../lib/router'

/**
 * Hosts outside Kubernetes (Phase 12, docs/automation.md): machines the
 * console reaches by SSH and Automation treats as nodes once paired. Every
 * call is a SuperAdmin's. The server never answers a host key, only its
 * fingerprint, and never keeps the credential a pairing is given.
 */

export interface Host {
  name: string
  address: string
  port: number
  loginUser: string
  labels?: Record<string, string>
  state: 'pending' | 'paired'
  fingerprint?: string
  keyChanged?: boolean
  addedBy?: string
  addedAt: string
  trustedBy?: string
  trustedAt?: string
  pairedBy?: string
  pairedAt?: string
}

export interface HostList {
  hosts: Host[]
  /** The chart's login user and port, offered for a new host. */
  loginUser: string
  port: number
}

/** What the host showed of its key: the fingerprint and how to print it there. */
export interface KeyOffer {
  fingerprint: string
  keyType: string
  command: string
}

/** A bootstrap credential: a password or a private key, used once and never kept. */
export interface Credential {
  user: string
  password?: string
  privateKey?: string
  passphrase?: string
  sudoPassword?: string
}

/** A failure that carries the setup's own lines. */
export class PairError extends Error {
  steps: string[]
  constructor(msg: string, steps: string[]) {
    super(msg)
    this.steps = steps
  }
}

async function call<T>(url: string, init: RequestInit = {}): Promise<T> {
  let res: Response
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (init.body !== undefined) headers['Content-Type'] = 'application/json'
  try {
    res = await fetch(url, { ...init, headers })
  } catch {
    throw new Error('The server could not be reached.')
  }
  if (!res.ok) {
    if (unauthorized(res)) throw new Error('Your session has ended; sign in again.')
    let msg = `The server answered ${res.status}.`
    let steps: string[] = []
    try {
      const body = (await res.json()) as { error?: string; steps?: string[] }
      if (body.error) msg = body.error
      steps = body.steps ?? []
    } catch {
      // Not JSON: keep the status.
    }
    throw new PairError(msg, steps)
  }
  return (await res.json()) as T
}

const post = (body: unknown): RequestInit => ({ method: 'POST', body: JSON.stringify(body) })
const hostPath = (cluster: string, name: string, rest = '') => apiPath(`hosts/${encodeURIComponent(name)}${rest}`, cluster)

export const fetchHosts = (cluster: string) => call<HostList>(apiPath('hosts', cluster))
export const addHost = (cluster: string, h: { name: string; address: string; port: number; loginUser: string; labels: Record<string, string> }) => call<Host>(apiPath('hosts', cluster), post(h))
export const scanHost = (cluster: string, name: string) => call<KeyOffer>(hostPath(cluster, name, '/scan'), post({}))
export const trustHost = (cluster: string, name: string, fingerprint: string) => call<Host>(hostPath(cluster, name, '/trust'), post({ fingerprint }))
export const pairHost = (cluster: string, name: string, cred: Credential) => call<{ host: Host; steps: string[] }>(hostPath(cluster, name, '/pair'), post(cred))
export const setupCommand = (cluster: string, name: string) => call<{ command: string }>(hostPath(cluster, name, '/setup'))
export const cleanupCommand = (cluster: string, name: string) => call<{ command: string }>(hostPath(cluster, name, '/cleanup'))
export const checkHost = (cluster: string, name: string) => call<Host>(hostPath(cluster, name, '/check'), post({}))
export const removeHost = (cluster: string, name: string) => call<{ cleanup: string }>(hostPath(cluster, name), { method: 'DELETE' })

/** Reads "key=value" pairs, by commas or lines, into labels; says what is wrong instead. */
export function parseLabels(text: string): Record<string, string> | string {
  const out: Record<string, string> = {}
  for (const part of text.split(/[,\n]/)) {
    const t = part.trim()
    if (!t) continue
    const i = t.indexOf('=')
    if (i <= 0) return `“${t}” is not key=value.`
    const k = t.slice(0, i).trim()
    const v = t.slice(i + 1).trim()
    if (k in out) return `${k} is given twice.`
    out[k] = v
  }
  return out
}

// A host's name is a node's: as nodessh's hostNameRE.
const HOST_NAME_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/

/**
 * Reads the hosts to add, by spaces, tabs, commas, semicolons or lines:
 * each an address, named after it (lower-cased, as a node name), or
 * name=address; says what is wrong instead.
 */
export function parseHosts(text: string): { name: string; address: string }[] | string {
  const out: { name: string; address: string }[] = []
  const seen = new Set<string>()
  for (const t of text.split(/[\s,;]+/)) {
    if (!t) continue
    const i = t.indexOf('=')
    const name = (i < 0 ? t : t.slice(0, i)).toLowerCase()
    const address = i < 0 ? t : t.slice(i + 1)
    if (!name || !address) return `“${t}” is neither an address nor name=address.`
    if (name.length > 253 || !HOST_NAME_RE.test(name)) return `“${name}” cannot be a node name (lower-case letters, digits, dashes and dots): write name=address.`
    if (seen.has(name)) return `${name} is in the list twice.`
    seen.add(name)
    out.push({ name, address })
  }
  return out
}
