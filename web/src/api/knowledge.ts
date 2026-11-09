// Mycroft's knowledge (Phase 12, ADR-94 to ADR-98): Settings › Knowledge,
// the wiki's pages and the drafts in Cases, and the Confluence publisher's
// part of Settings › Issues. Credentials go in and never come back: the
// server says only whether each is set.
import { call, post } from './call'

export type SourceKind = 'confluence' | 'git' | 'upload' | 'console'

export interface KnowledgeSource {
  id: string
  name: string
  kind: SourceKind
  off?: boolean
  baseURL?: string
  flavor?: 'cloud' | 'datacenter'
  email?: string
  space?: string
  url?: string
  branch?: string
  path?: string
  username?: string
  caPEM?: string
  added?: string
  addedBy?: string
}

/** The temporary ConfigMap store's name wherever a person sees it (CF-57). */
export const CONFIGMAP_STORE_LABEL = 'ConfigMap (temporary, for testing — will be removed; move to Git or S3 before relying on it)'

export interface StoreSettings {
  /** configmap is the temporary store (CF-57): the default while none is saved, when the chart made its ConfigMap. */
  kind: '' | 'git' | 's3' | 'configmap'
  url?: string
  branch?: string
  username?: string
  endpoint?: string
  region?: string
  bucket?: string
  prefix?: string
  pathStyle?: boolean
  credentials?: 'keys' | 'identity'
  sse?: '' | 'AES256' | 'aws:kms'
  kmsKeyId?: string
  caPEM?: string
  refreshMinutes?: number
  savedBy?: string
  savedAt?: string
}

export interface CredState {
  token: boolean
  accessKey: boolean
}

export interface SourceStatus {
  lastRead?: string
  items: number
  error?: string
  reported?: number
}

export interface SyncResult {
  run?: string
  started: string
  finished?: string
  by: string
  full: boolean
  read: number
  unchanged: number
  gone?: number
  pages: number
  proposals: number
  reported?: string[]
  errors?: string[]
  text: string
}

export interface ItemState {
  source: string
  id: string
  title: string
  version?: string
  modified?: string
  lastRead?: string
  lastSeen?: string
}

export interface KnowledgeView {
  settings: { sources: KnowledgeSource[]; store: StoreSettings; staleDays?: number }
  creds: Record<string, CredState>
  storeCreds: CredState
  hook: boolean
  status: { running: boolean; last?: SyncResult; sources?: Record<string, SourceStatus>; history?: SyncResult[] }
  pages: number
  excerpts: number
  pulled?: string
  documents?: Record<string, ItemState[]>
  waiting: number
  identity: boolean
  model: boolean
  console: boolean
  /** Whether the temporary ConfigMap store can be chosen (CF-57). */
  configMapStore?: boolean
}

export interface StoreCheck {
  versioning?: string
  warnings?: string[]
  files: number
}

/** A credential change: a value replaces, "" removes, absent keeps. */
export interface KeysChange {
  token?: string
  accessKeyId?: string
  secretAccessKey?: string
}

export interface SourceRef {
  key: string
  title: string
  version?: string
  date?: string
  url?: string
}

export interface WikiPage {
  path: string
  title: string
  kind: string
  critical?: boolean
  sources?: SourceRef[]
  updated?: string
  body?: string
  stale?: boolean
}

export interface SearchHit {
  kind: 'page' | 'excerpt'
  title: string
  path?: string
  pageKind?: string
  date?: string
  stale?: boolean
  source?: string
  version?: string
  url?: string
  text: string
}

export interface DraftPage {
  path: string
  title: string
  kind: string
  critical?: boolean
  new?: boolean
  diff: string
  added: number
  removed: number
  clipped?: boolean
  sources?: SourceRef[]
  state: 'waiting' | 'published' | 'conflict' | 'rejected'
  approvals?: { by: string; name: string; at: string }[]
  reason?: string
}

export interface DraftRun {
  id: string
  started: string
  by: string
  state: 'waiting' | 'partial' | 'done' | 'rejected'
  store: string
  pages: DraftPage[]
  reported?: string[]
  decisions?: { by: string; name: string; at: string; action: string; comment?: string }[]
  decided?: string
  startedBy?: string
  canApprove?: boolean
  mine?: boolean
}

export interface ConfluenceSettings {
  on: boolean
  baseURL: string
  flavor: 'cloud' | 'datacenter'
  email?: string
  caPEM?: string
  space: string
  root?: string
  wiki?: boolean
  wikiSpace?: string
  wikiRoot?: string
  checkAt?: string
  savedBy?: string
  savedAt?: string
}

export interface ConfluenceView {
  settings: ConfluenceSettings
  token: boolean
  outbox: number
  failing: number
  lastError?: string
  lastWrite?: string
  lastCheck?: string
  nextCheck?: string
  checkReport?: string[]
  conflicts: number
  pages: number
  wikiPages: number
}

export interface ConfluenceConflict {
  id: string
  kind: 'known' | 'wiki' | 'status' | 'tree'
  issue?: string
  title: string
  url?: string
  their: string
  at: string
  owner?: string
  ownerName?: string
  mine?: boolean
}

const base = '/api/v1/knowledge/'
const enc = encodeURIComponent

export const fetchKnowledge = () => call<KnowledgeView>(base + 'settings')
export const saveStaleDays = (staleDays: number) => call<KnowledgeView>(base + 'settings', { method: 'PUT', body: JSON.stringify({ staleDays }) })
export const addSource = (source: KnowledgeSource, token?: string) => call<KnowledgeSource>(base + 'sources', post({ source, token }))
export const saveSource = (source: KnowledgeSource, token?: string) => call<KnowledgeSource>(base + 'sources/' + enc(source.id), { method: 'PUT', body: JSON.stringify({ source, token }) })
export const removeSource = (id: string) => call<{ removed: boolean }>(base + 'sources/' + enc(id), { method: 'DELETE' })
export const setSourceOn = (id: string, on: boolean) => call<{ on: boolean }>(base + 'sources/' + enc(id) + '/on', post({ on }))
export const testSource = (id: string) => call<{ ok: boolean; items?: number; error?: string }>(base + 'sources/' + enc(id) + '/test', post({}))
export const uploadDocument = (source: string, name: string, data: string) => call<{ version: string }>(base + 'sources/' + enc(source) + '/documents', post({ name, data }))
export const removeDocument = (source: string, name: string) => call<{ removed: boolean }>(base + 'sources/' + enc(source) + '/documents/' + enc(name), { method: 'DELETE' })
export const saveStore = (store: StoreSettings, keys: KeysChange) => call<KnowledgeView>(base + 'store', { method: 'PUT', body: JSON.stringify({ store, keys }) })
export const testStore = (store: StoreSettings, keys: KeysChange) => call<{ ok: boolean; check?: StoreCheck; error?: string }>(base + 'store/test', post({ store, keys }))
export const moveStore = (store: StoreSettings, keys: KeysChange) => call<{ files: number }>(base + 'store/move', post({ store, keys }))
export const saveHookToken = (token: string) => call<{ hook: boolean }>(base + 'hook', { method: 'PUT', body: JSON.stringify({ token }) })
export const syncNow = (full: boolean, source?: string) => call<{ started: boolean }>(base + 'sync', post({ full, source }))
export const fetchPages = () => call<{ pages: WikiPage[]; connected: boolean }>(base + 'pages')
export const fetchPage = (path: string) => call<WikiPage>(base + 'page?path=' + enc(path))
export const searchKnowledge = (q: string) => call<{ hits: SearchHit[] }>(base + 'search?q=' + enc(q))
export const fetchDrafts = (all = false) => call<{ runs: DraftRun[] }>(base + 'drafts' + (all ? '?all=1' : ''))
export const fetchDraftsCount = () => call<{ count: number }>(base + 'drafts/count', {}, true)
export const approveDraft = (id: string) => call<DraftRun>(base + 'drafts/' + enc(id) + '/approve', post({}))
export const rejectDraft = (id: string, comment: string) => call<DraftRun>(base + 'drafts/' + enc(id) + '/reject', post({ comment }))
export const fetchConfluence = () => call<ConfluenceView>(base + 'confluence')
export const saveConfluence = (settings: ConfluenceSettings, token?: string) => call<ConfluenceView>(base + 'confluence', { method: 'PUT', body: JSON.stringify({ settings, token }) })
export const testConfluence = (settings: ConfluenceSettings, token?: string) => call<{ ok: boolean; spaces?: string[]; error?: string }>(base + 'confluence/test', post({ settings, token }))
export const checkConfluence = () => call<{ started: boolean }>(base + 'confluence/check', post({}))
export const fetchConflicts = () => call<{ conflicts: ConfluenceConflict[] }>(base + 'confluence/conflicts')
export const resolveConflict = (id: string, action: 'keep' | 'replace') => call<{ resolved: boolean }>(base + 'confluence/conflicts/' + enc(id), post({ action }))

// --- Mycroft tab (Phase 12, wave 5, MY-01 to MY-10) ---

/** A source as Cases › Mycroft › Wiki › Sources lists it: never its address or credential. */
export interface SourceBrief {
  id: string
  name: string
  kind: SourceKind
  off?: boolean
  items: number
  lastRead?: string
  error?: string
  reported?: number
}

/** One item people added in Mycroft: a note, a web page or a document. */
export interface AddedItem {
  source: string
  id: string
  title: string
  version?: string
  url?: string
  modified?: string
  lastRead?: string
  lastSeen?: string
  pages?: string[]
  reported?: string
}

export interface AddLimits {
  note: number
  document: number
  depth: number
  defaultDepth: number
  pages: number
  defaultPages: number
  formats: string[]
}

/** What the Wiki needs first: whether a wiki is kept, what the person may do, the sources and what was added. */
export interface WikiState {
  store: '' | 'git' | 's3' | 'configmap' | string
  model: boolean
  pages: number
  excerpts: number
  running: boolean
  mayAdd: boolean
  mayReview: boolean
  maySetup: boolean
  sources: SourceBrief[]
  added: AddedItem[]
  limits: AddLimits
}

export type AddKind = 'text' | 'url' | 'document'

export interface AddInput {
  kind: AddKind
  title?: string
  text?: string
  url?: string
  depth?: number
  pages?: number
  name?: string
  /** A document's bytes, base64. */
  data?: string
}

export interface AddResult {
  kind: AddKind
  items: { id: string; title: string; url?: string; bytes: number }[]
  skipped?: { url: string; why: string }[]
  more?: boolean
  model: boolean
  text: string
}

/** A wiki page's address: Cases › Mycroft › Wiki, #/cases/mycroft/wiki/pages/<kind>/<name>.md. */
export const pageHref = (path: string) => '#/cases/mycroft/wiki/' + path.split('/').map(encodeURIComponent).join('/')

/** The Wiki's views' addresses. */
export const wikiHref = (view: 'pages' | 'drafts' | 'sources' | 'add' = 'pages') => '#/cases/mycroft/wiki' + (view === 'pages' ? '' : '/' + view)

export const fetchWiki = (background = false) => call<WikiState>(base + 'wiki', {}, background)
export const addKnowledge = (input: AddInput) => call<AddResult>(base + 'add', post(input))
export const removeAdded = (id: string) => call<{ removed: boolean }>(base + 'added/' + enc(id), { method: 'DELETE' })

/** A file's bytes as base64, for an upload. */
export async function fileBase64(f: Blob): Promise<string> {
  const buf = new Uint8Array(await f.arrayBuffer())
  let bin = ''
  for (let i = 0; i < buf.length; i += 0x8000) bin += String.fromCharCode(...buf.subarray(i, i + 0x8000))
  return btoa(bin)
}

export const SOURCE_KIND_LABEL: Record<SourceKind, string> = {
  confluence: 'Confluence',
  git: 'Git repository',
  upload: 'Uploaded documents',
  console: 'This console',
}
