// Helm applications (Phase 10, HA-01 to HA-20): what the console keeps
// about a release installed from git, and what the Helm tab reads about
// any release. Changes come back as the Watson card that runs them.

import { apiPath } from '../lib/router'
import { unauthorized } from './auth'
import type { WatsonItem } from './watson'

export type RefKind = 'branch' | 'tag'

export interface AppSource {
  repo: string
  path?: string
  ref: string
  refKind: RefKind
}

export interface AppRecord {
  version: number
  cluster: string
  namespace: string
  name: string
  source: AppSource
  commit?: string
  commitDate?: string
  commitSubject?: string
  chartVersion?: string
  values?: string
  params?: string[]
  createNamespace?: boolean
  /** A second values file in the chart's directory, applied over values.yaml and under the values here (R2-08). */
  valuesFile?: string
  /** Whether the chart's own values.yaml is left out (R2-09). */
  ignoreDefaults?: boolean
  /** The fleet the application is deployed to, by id, and its deploy label when saved: the pods' nodeSelector (R2-11). */
  fleet?: string
  fleetLabel?: string
  /** The deployed revision the source was found to render exactly when it was set: not behind while it runs. */
  matchedRevision?: number
  hasToken?: boolean
  createdBy?: string
  created: string
  updatedBy?: string
  updated: string
}

export type DriftState = 'same' | 'changed' | 'missing' | 'unknown'

export interface DriftObject {
  kind: string
  namespace?: string
  name: string
  state: DriftState
  fields?: string[]
  error?: string
  /** What changed, as a unified diff of the fields the manifest sets (R2-10). */
  diff?: string
}

export interface AppDrift {
  checked: string
  same: number
  changed: number
  missing: number
  unknown: number
  objects: DriftObject[]
}

export interface AppView extends AppRecord {
  drift?: AppDrift
  driftState?: 'in sync' | 'drifted' | 'unknown'
}

/** A chart found in a repository, and what its README's install example says (M-07). */
export interface ChartCandidate {
  path: string
  name: string
  version: string
  appVersion?: string
  description?: string
  /** The other YAML files in the chart's directory and one down (values-prod.yaml), for a second values file (R2-08). */
  valuesFiles?: string[]
}
export interface ReadmeHints {
  file: string
  chart?: string
  name?: string
  namespace?: string
  createNamespace?: boolean
  params?: string[]
  valuesFile?: string
  values?: string
}
export interface RepoInspection {
  charts: ChartCandidate[]
  readme?: ReadmeHints
  ref: string
  refKind: RefKind
  commit: string
}

export interface RepoRefs {
  default?: string
  branches: string[]
  tags: string[]
}

export interface AppRefs extends RepoRefs {
  newerTags: string[]
  moved: boolean
  head?: string
}

export interface RevisionView {
  revision: number
  updated: string
  status: string
  chart: string
  chartVersion: string
  appVersion: string
  description: string
  objects: number
}

export interface AppDiff {
  changed: string[]
  added: string[]
  removed: string[]
  text: string
  same: boolean
  from: string
  to: string
}

export interface AppImage {
  image: string
  registry?: string
  repository: string
  tag?: string
  digest?: string
  kind: string
  name: string
  container: string
  init?: boolean
}

export interface AppInput {
  namespace?: string
  name?: string
  source?: AppSource
  token?: string
  clearToken?: boolean
  values?: string
  params?: string[]
  createNamespace?: boolean
  valuesFile?: string
  ignoreDefaults?: boolean
  fleet?: string
  upgrade?: boolean
  install?: boolean
  ref?: string
  refKind?: RefKind
}

export class AppsError extends Error {
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
    })
  } catch {
    throw new AppsError('The console did not answer. Check the connection and try again.', 0)
  }
  if (unauthorized(res)) throw new AppsError('Your session has ended; sign in again.', 401)
  if (res.status === 204) return undefined as T
  const data = (await res.json().catch(() => null)) as (T & { error?: string }) | null
  if (!res.ok) throw new AppsError(data?.error || `The console answered ${res.status}.`, res.status)
  return data as T
}

const app = (cluster: string, ns: string, name: string, rest = '') => apiPath(`apps/${encodeURIComponent(ns)}/${encodeURIComponent(name)}${rest}`, cluster)

export const fetchApps = (cluster: string) => call<AppView[]>('GET', apiPath('apps', cluster))
export const fetchApp = (cluster: string, ns: string, name: string) => call<AppView>('GET', app(cluster, ns, name))
export const fetchDrift = (cluster: string, ns: string, name: string, refresh = false) => call<AppDrift>('GET', app(cluster, ns, name, '/drift') + (refresh ? '?refresh=1' : ''))
export const fetchAppRefs = (cluster: string, ns: string, name: string) => call<AppRefs>('GET', app(cluster, ns, name, '/refs'))
/** A kept application's repository looked over at its ref, with the saved token (R2-08). */
export const fetchAppInspect = (cluster: string, ns: string, name: string) => call<RepoInspection>('GET', app(cluster, ns, name, '/inspect'))
/** Looks over a repository for the new-application form: its charts and the README's install example (M-07). */
export const inspectRepo = (cluster: string, repo: string, ref: string, refKind: RefKind, token?: string) => call<RepoInspection>('POST', apiPath('apps/inspect', cluster), { repo, ...(ref ? { ref, refKind } : {}), ...(token ? { token } : {}) })

export const fetchRepoRefs = (cluster: string, repo: string, token = '') => call<RepoRefs>('POST', apiPath('apps/refs', cluster), token ? { repo, token } : { repo })
export const fetchRevisions = (cluster: string, ns: string, name: string) => call<RevisionView[]>('GET', app(cluster, ns, name, '/revisions'))
export const fetchImages = (cluster: string, ns: string, name: string, revision?: number) =>
  call<{ revision: number; images: AppImage[] }>('GET', app(cluster, ns, name, '/images') + (revision ? `?revision=${revision}` : ''))

/** Two revisions, or the deployed one against the source (to: 'source', at ref when given). */
export function fetchDiff(cluster: string, ns: string, name: string, q: { from?: number; to: number | 'source'; ref?: string; refKind?: RefKind }): Promise<AppDiff> {
  const params = new URLSearchParams()
  if (q.from) params.set('from', String(q.from))
  params.set('to', String(q.to))
  if (q.ref) {
    params.set('ref', q.ref)
    params.set('refKind', q.refKind ?? 'branch')
  }
  return call<AppDiff>('GET', app(cluster, ns, name, '/diff') + '?' + params.toString())
}

export const createApp = (cluster: string, input: AppInput) => call<WatsonItem>('POST', apiPath('apps', cluster), input)
/** Saves the source, values, parameters or token; with upgrade, deploys too (then the answer is the card). */
export const updateApp = (cluster: string, ns: string, name: string, input: AppInput) => call<AppView | WatsonItem>('PUT', app(cluster, ns, name), input)
export const upgradeApp = (cluster: string, ns: string, name: string, input: AppInput = {}) => call<WatsonItem>('POST', app(cluster, ns, name, '/upgrade'), input)
export const uninstallApp = (cluster: string, ns: string, name: string, keepHistory = false) => call<WatsonItem>('DELETE', app(cluster, ns, name) + (keepHistory ? '?keepHistory=1' : ''))
export const forgetApp = (cluster: string, ns: string, name: string) => call<void>('DELETE', app(cluster, ns, name) + '?record=1')

/** The source as the table shows it: github.com/org/app/charts/app @ v1.2.0. */
export function sourceText(s: AppSource): string {
  let repo = s.repo.replace(/^https?:\/\//, '').replace(/\.git$/, '')
  if (s.path) repo += '/' + s.path
  return `${repo} @ ${s.ref}`
}

/** Whether an answer to a change is a Watson card. */
export function isCard(x: AppView | WatsonItem): x is WatsonItem {
  return 'command' in x && 'status' in x
}
