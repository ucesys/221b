// Mycroft's knowledge (Phase 12, ADR-94 to ADR-98): Settings › Knowledge,
// the wiki's pages and the drafts in Cases, and the Confluence publisher's
// part of Settings › Issues. Credentials go in and never come back: the
// server says only whether each is set.
import { call, post } from './call'

/** fixes: the fixes proven in cases, offered for a runbook (KN-34); there from the start, renamed only. */
export type SourceKind = 'confluence' | 'git' | 's3' | 'upload' | 'console' | 'fixes'

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
  /** A bucket of documents (KN-25): its name, the prefix read under, region, endpoint (empty for AWS), path-style, and an access key in the Secret or the pod's identity. */
  bucket?: string
  prefix?: string
  region?: string
  endpoint?: string
  pathStyle?: boolean
  credentials?: 'keys' | 'identity'
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
  /** Items the source holds that Mycroft does not read, and why (KN-25). */
  skipped?: number
  skippedWhy?: string
  /** The last read of every item: a Confluence space's Wiki sync reads all once a night (KN-28). */
  lastFull?: string
}

/** A source's Wiki sync job, in AIOps › Jobs (KN-28). */
export const syncJobId = (source: string) => `wiki-sync-${source}`

/** What a source's Test found: its items, the first few, and what it skips (KN-25). */
export interface SourceTestResult {
  ok: boolean
  items?: number
  sample?: string[]
  skipped?: number
  skippedWhy?: string
  error?: string
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
  /** The source a sync of one source read (KN-28). */
  source?: string
  sourceName?: string
  /** The active fixes Mycroft was given, and the pages that contradict one, in words (KN-33). */
  fixes?: string[]
  contradictions?: string[]
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

/** Why a page was retired, who asked, who approved and when (KN-24). */
export interface Retirement {
  reason: string
  askedBy: string
  askedAt?: string
  approvedBy: string
  at: string
  request?: string
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
  /** Set on a retired page: out of the index, no agent reads it (KN-24). */
  retired?: Retirement
}

/** A correction sent with This is outdated… (KN-23): waiting to be read, or its redraft waiting for review in a run. */
export interface Correction {
  id: string
  path: string
  title: string
  by: string
  name: string
  at: string
  state: 'waiting' | 'drafted' | 'done'
  run?: string
  note?: string
}

/** A request to retire a page or make it active again, waiting for one approver other than who asked (KN-24). */
export interface PageRequest {
  id: string
  action: 'retire' | 'activate'
  path: string
  title: string
  reason: string
  by: string
  name: string
  at: string
  state: 'waiting' | 'approved' | 'rejected'
  decidedBy?: string
  decidedName?: string
  decided?: string
  comment?: string
  canApprove?: boolean
  mine?: boolean
}

/** A page as its view reads it: with the correction and the request that wait for it, if any. */
export interface PageView extends WikiPage {
  correction?: Correction
  request?: PageRequest
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
  /** held: a change to a retired page, never published (KN-24). */
  state: 'waiting' | 'published' | 'conflict' | 'rejected' | 'held'
  approvals?: { by: string; name: string; at: string }[]
  reason?: string
  /** The active fixes the page says otherwise than, for its reviewer (KN-33). */
  contradicts?: Contradiction[]
}

/** A fix a drafted page contradicts: its id, title, how many cases it fixed, and Mycroft's why (KN-33). */
export interface Contradiction {
  fix: string
  title: string
  cases: number
  why?: string
}

/** How the review card and the run's report say it: "contradicts F-3, used in 4 cases: why". */
export function contradictionText(c: Contradiction): string {
  return `contradicts ${c.fix}, used in ${c.cases} case${c.cases === 1 ? '' : 's'}` + (c.why ? `: ${c.why}` : '')
}

/** The key a page names a fix by among its sources (KN-33, KN-34). */
export const fixSourceKey = (id: string) => 'fixes:' + id

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
export const addSource = (source: KnowledgeSource, token?: string, keys?: KeysChange) => call<KnowledgeSource>(base + 'sources', post(keys ? { source, keys } : { source, token }))
export const saveSource = (source: KnowledgeSource, token?: string, keys?: KeysChange) => call<KnowledgeSource>(base + 'sources/' + enc(source.id), { method: 'PUT', body: JSON.stringify(keys ? { source, keys } : { source, token }) })
export const removeSource = (id: string) => call<{ removed: boolean }>(base + 'sources/' + enc(id), { method: 'DELETE' })
export const setSourceOn = (id: string, on: boolean) => call<{ on: boolean }>(base + 'sources/' + enc(id) + '/on', post({ on }))
/**
 * Tests a source's values as typed in its dialog, before they are saved
 * (KN-26). saved is the id of the source being edited: a credential left
 * blank is its saved one, read on the server and never sent back.
 */
export const testTypedSource = (source: KnowledgeSource, token?: string, keys?: KeysChange, saved?: string) => call<SourceTestResult>(base + 'sources/test', post({ source, token, keys, saved }))

/** A source's Test, in words: what it reached, the first few items, and what is skipped; or what is wrong. */
export function sourceTestText(r: SourceTestResult): string {
  if (!r.ok) return r.error ?? 'The test failed.'
  const n = r.items ?? 0
  let text = `Reached: ${n} ${n === 1 ? 'item' : 'items'}`
  if (r.sample?.length) text += `, e.g. ${r.sample.join(', ')}`
  text += '.'
  if (r.skipped) text += ` ${r.skipped} skipped${r.skippedWhy ? `: ${r.skippedWhy}` : ''}.`
  return text
}

/** Whether a source's address opens in a browser; a bucket's s3://bucket/key does not (KN-25). */
export const isWebUrl = (url?: string) => !!url && /^https?:\/\//i.test(url)
export const uploadDocument = (source: string, name: string, data: string) => call<{ version: string }>(base + 'sources/' + enc(source) + '/documents', post({ name, data }))
export const removeDocument = (source: string, name: string) => call<{ removed: boolean }>(base + 'sources/' + enc(source) + '/documents/' + enc(name), { method: 'DELETE' })
export const saveStore = (store: StoreSettings, keys: KeysChange) => call<KnowledgeView>(base + 'store', { method: 'PUT', body: JSON.stringify({ store, keys }) })
export const testStore = (store: StoreSettings, keys: KeysChange) => call<{ ok: boolean; check?: StoreCheck; error?: string }>(base + 'store/test', post({ store, keys }))
export const moveStore = (store: StoreSettings, keys: KeysChange) => call<{ files: number }>(base + 'store/move', post({ store, keys }))
export const saveHookToken = (token: string) => call<{ hook: boolean }>(base + 'hook', { method: 'PUT', body: JSON.stringify({ token }) })
export const fetchPages = () => call<{ pages: WikiPage[]; retired?: WikiPage[]; connected: boolean }>(base + 'pages')
export const fetchPage = (path: string) => call<PageView>(base + 'page?path=' + enc(path))
/** Searches the approved pages and the sources' excerpts only; the Mycroft search (findInMycroft) searches the records too. */
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

/** A source as AIOps › Mycroft › Wiki › Sources lists it: never its address or credential. */
export interface SourceBrief {
  id: string
  name: string
  kind: SourceKind
  off?: boolean
  items: number
  lastRead?: string
  error?: string
  reported?: number
  skipped?: number
  skippedWhy?: string
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
  /** The page a correction is for (KN-23), and who added the item. */
  corrects?: string
  by?: string
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
  /** The ConfigMap store's room, in bytes, when the wiki is kept there (KN-37). */
  usage?: { used: number; limit: number }
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

/** A wiki page's address: AIOps › Mycroft › Wiki, #/aiops/mycroft/wiki/pages/<kind>/<name>.md. */
export const pageHref = (path: string) => '#/aiops/mycroft/wiki/' + path.split('/').map(encodeURIComponent).join('/')

/** The Wiki's views' addresses. */
export const wikiHref = (view: 'pages' | 'drafts' | 'sources' = 'pages') => '#/aiops/mycroft/wiki' + (view === 'pages' ? '' : '/' + view)

export const fetchWiki = (background = false) => call<WikiState>(base + 'wiki', {}, background)
export const addKnowledge = (input: AddInput) => call<AddResult>(base + 'add', post(input))
export const removeAdded = (id: string) => call<{ removed: boolean }>(base + 'added/' + enc(id), { method: 'DELETE' })

// --- A wiki page's own actions (KN-23, KN-24) ---

export interface OutdatedResult {
  correction: Correction
  model: boolean
  text: string
}

/** This is outdated…: what is outdated on a page and what is true now, for Mycroft to redraft it (KN-23). */
export const sendOutdated = (path: string, text: string) => call<OutdatedResult>(base + 'outdated', post({ path, text }))
/** The requests to retire a page or make it active again: those waiting, or all (KN-24). */
export const fetchPageRequests = (all = false) => call<{ requests: PageRequest[] }>(base + 'requests' + (all ? '?all=1' : ''))
export const askPageRequest = (action: PageRequest['action'], path: string, reason: string) => call<PageRequest>(base + 'requests', post({ action, path, reason }))
export const approvePageRequest = (id: string) => call<PageRequest>(base + 'requests/' + enc(id) + '/approve', post({}))
export const rejectPageRequest = (id: string, comment: string) => call<PageRequest>(base + 'requests/' + enc(id) + '/reject', post({ comment }))

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
  s3: 'S3 bucket',
  upload: 'Uploaded documents',
  console: 'This console',
  fixes: 'Fixes',
}

// --- One search across Mycroft (Phase 12.1, KN-35) ---

/** What the Mycroft search finds: a wiki page, a source's excerpt, a known issue, a fix or an accepted exception. */
export type FindKind = 'page' | 'excerpt' | 'knownIssue' | 'fix' | 'exception'

/** One hit of the Mycroft search, ranked with the others whatever its kind. */
export interface FindHit {
  kind: FindKind
  title: string
  /** A page's path. */
  path?: string
  /** A record's id: "KI-3", "F-2", "EX-4"; status its state. */
  id?: string
  status?: string
  pageKind?: string
  date?: string
  stale?: boolean
  source?: string
  version?: string
  url?: string
  /** The snippet: one line around the first word matched. */
  text: string
  score: number
  /** The query's words the hit holds, lower case and stemmed, for the snippet to mark. */
  terms?: string[]
}

export interface FindResult {
  hits: FindHit[]
  /** How many hits of each kind match, whichever kinds were asked. */
  counts: Partial<Record<FindKind, number>>
  /** The kinds the person may search. */
  kinds: FindKind[]
}

/** Searches what AIOps › Mycroft holds, as the person may read it (GET /api/v1/aiops/mycroft/search). */
export const findInMycroft = (q: string, kinds: FindKind[] = [], limit = 30) => call<FindResult>('/api/v1/aiops/mycroft/search?q=' + enc(q) + (kinds.length > 0 ? '&kind=' + kinds.map(enc).join(',') : '') + '&limit=' + limit, {}, true)
