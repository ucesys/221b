// Mycroft's knowledge (Phase 12, ADR-94 to ADR-98): Administration › AI › Knowledge,
// the wiki's pages and the drafts in Cases. Credentials go in and never
// come back: the server says only whether each is set.
import { call, post } from './call'
import type { CertPin, PinnedWith, PresentedCert } from './certificates'

/** fixes: the fixes proven in cases, offered for a runbook (KN-34); there from the start, renamed only. live: never copied, looked up when the agents troubleshoot (KN-39). web: a web site crawled from a start address (0.12.1). */
export type SourceKind = 'confluence' | 'git' | 's3' | 'upload' | 'console' | 'fixes' | 'live' | 'web'

/**
 * An item, or items, a SuperAdmin left out of a syncable source (0.12.1):
 * its id (a web page's path, a file's path, an object's key, a Confluence
 * page's id), a glob with * (/blog/*), or a prefix ending in / (a folder,
 * an S3 prefix); under, for Confluence, is the page and its children.
 */
export interface Exclusion {
  pattern: string
  under?: boolean
  title?: string
  reason?: string
  by: string
  at: string
}

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
  /** A git source's repository of Administration › Settings › Repositories, by name (0.12.1, RP-03): url and credential are its own then. */
  repository?: string
  /** A bucket of documents (KN-25): its name, the prefix read under, region, endpoint (empty for AWS), path-style, and an access key in the Secret or the pod's identity. */
  bucket?: string
  prefix?: string
  region?: string
  endpoint?: string
  pathStyle?: boolean
  credentials?: 'keys' | 'identity'
  caPEM?: string
  /** The source's certificate a SuperAdmin trusted, for one without a credential (CR-16); set by Trust only. */
  pinnedCert?: CertPin
  /** A live source's addresses and how it signs in (KN-39): none, a bearer token, or the user (username) with a password or API token. A web site signs in the same way. */
  addresses?: LiveAddress[]
  auth?: '' | 'bearer' | 'basic'
  /** A web site (0.12.1): its start address is url; how many links away it is read (0–3) and at most how many pages (1–200). */
  depth?: number
  pages?: number
  /** What a SuperAdmin left out of the source, in AIOps › Mycroft › Sources; kept by the server on Edit. */
  excluded?: Exclusion[]
  /** The web items of Added in Mycroft this web site was made from at the upgrade. */
  migratedFrom?: string[]
  added?: string
  addedBy?: string
  /** The credential of Administration › Credentials it signs in with, by id (0.12.1, CR-11); none when absent, or a bucket's pod identity. */
  credential?: string
  /** It names its credential; before the upgrade moves it, the one kept in the knowledge Secret is used. */
  credMigrated?: boolean
}

/** One address of a live source: a web page, a Confluence page, or a search of a Confluence space (its site, with /wiki for Cloud, and the space's key). */
export interface LiveAddress {
  kind: 'page' | 'confluencePage' | 'confluenceSearch'
  url: string
  space?: string
}

export const LIVE_ADDRESS_LABEL: Record<LiveAddress['kind'], string> = {
  page: 'Web page',
  confluencePage: 'Confluence page',
  confluenceSearch: 'Confluence space search',
}

/** Whether a source is read into the wiki (Ingest) or looked up when the agents troubleshoot (Live, KN-39). */
export const sourceMode = (s: Pick<KnowledgeSource, 'kind'>): 'Ingest' | 'Live' => (s.kind === 'live' ? 'Live' : 'Ingest')

/** One address of a live source as its Test read it (KN-42). */
export interface LiveCheck {
  url: string
  kind: LiveAddress['kind']
  ok: boolean
  pages?: number
  bytes?: number
  title?: string
  cut?: boolean
  error?: string
}

/** The database store's name wherever a person sees it (DB-96). */
export const DATABASE_STORE_LABEL = 'The console’s database'

export interface StoreSettings {
  /** database is the console's database (DB-96): the default while none is saved. */
  kind: '' | 'git' | 's3' | 'database'
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
  /** The store's certificate a SuperAdmin trusted (CR-16); set by Trust only. */
  pinnedCert?: CertPin
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
  /** A live source's addresses, each read once (KN-42). */
  live?: LiveCheck[]
  /** The certificate the source's address presented, when it is not trusted or is the pinned one (CR-15). */
  certificate?: PresentedCert
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

/** The embedding model of the semantic search (KN-55): a provider of Administration › AI and its model; none by default. */
export interface EmbeddingSettings {
  provider?: string
  model?: string
}

/** A provider an embedding model may be chosen on: an OpenAI-compatible one of Administration › AI. */
export interface EmbeddingProvider {
  id: string
  name: string
  endpoint?: string
}

/** The search index in the database (KN-53 to KN-58). */
export interface IndexView {
  documents: number
  passages: number
  /** The database has pg_trgm (fuzzy matching) and pgvector (semantic search). */
  fuzzy: boolean
  semantic: boolean
  /** Passages with an embedding by the chosen model; why the last pass stopped. */
  embedded: number
  embedError?: string
  embeddedAt?: string
  providers: EmbeddingProvider[]
  /** Each source's trust, what it is about and what the index holds of it (KN-56 to KN-58). */
  sources: SourceIndexView[]
  pageSize?: SourceSize
  /** The database's size, its volume's as the chart says (none: not known), and whether it holds 80 % of it or more. */
  databaseBytes: number
  volumeBytes?: number
  full?: boolean
  /** The versions the clusters run, major.minor. */
  clusterVersions: string[]
}

export type TrustLevel = 'high' | 'normal' | 'low'

export interface SourceSize {
  id: string
  documents: number
  passages: number
  embedded: number
  bytes: number
}

export interface SourceIndexView extends SourceSize {
  name: string
  kind: SourceKind
  trust: TrustLevel
  /** The trust is its kind's. */
  trustDefault?: boolean
  product?: string
  productVersion?: string
  followsClusterVersions?: boolean
}

/** A source's index settings, as a SuperAdmin sets them (KN-56, KN-57): trust '' is its kind's. */
export interface SourceIndexSettings {
  trust: '' | TrustLevel
  product: string
  productVersion: string
  followsClusterVersions: boolean
}

export interface KnowledgeView {
  settings: { sources: KnowledgeSource[]; store: StoreSettings; staleDays?: number; embeddings?: EmbeddingSettings }
  /** The search index in the database (KN-53). */
  index?: IndexView
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
  /** Whether the wiki can be kept in the console's database (DB-96). */
  databaseStore?: boolean
  /** The certificates pinned for the sources, by id: their own, their credential's or their repository's (CR-19). */
  pins?: Record<string, PinnedWith>
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

const base = '/api/v1/knowledge/'
const enc = encodeURIComponent

export const fetchKnowledge = () => call<KnowledgeView>(base + 'settings')
export const saveStaleDays = (staleDays: number) => call<KnowledgeView>(base + 'settings', { method: 'PUT', body: JSON.stringify({ staleDays }) })
/** Sets a source's trust and what it is about (KN-56, KN-57). */
export const saveSourceIndex = (id: string, s: SourceIndexSettings) => call<KnowledgeSource>(base + 'sources/' + enc(id) + '/index', { method: 'PUT', body: JSON.stringify(s) })
/** Chooses the embedding model of the search, or none (KN-55). */
export const saveEmbeddings = (e: EmbeddingSettings) => call<KnowledgeView>(base + 'embeddings', { method: 'PUT', body: JSON.stringify(e) })
/** Adds a source, naming its credential (CR-11): no secret goes with it. */
export const addSource = (source: KnowledgeSource) => call<KnowledgeSource>(base + 'sources', post({ source }))
export const saveSource = (source: KnowledgeSource) => call<KnowledgeSource>(base + 'sources/' + enc(source.id), { method: 'PUT', body: JSON.stringify({ source }) })
export const removeSource = (id: string) => call<{ removed: boolean }>(base + 'sources/' + enc(id), { method: 'DELETE' })
export const setSourceOn = (id: string, on: boolean) => call<{ on: boolean }>(base + 'sources/' + enc(id) + '/on', post({ on }))
/**
 * Tests a source's values as typed in its dialog, before they are saved
 * (KN-26), with the credential it names. saved is the id of the source
 * being edited.
 */
export const testTypedSource = (source: KnowledgeSource, saved?: string) => call<SourceTestResult>(base + 'sources/test', post({ source, saved }))

/** A source's Test, in words: what it reached, the first few items, and what is skipped; or what is wrong. */
export function sourceTestText(r: SourceTestResult): string {
  if (r.live) {
    const read = r.live.filter((c) => c.ok).length
    return `${read} of ${r.live.length} ${r.live.length === 1 ? 'address' : 'addresses'} read.`
  }
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
export const testStore = (store: StoreSettings, keys: KeysChange) => call<{ ok: boolean; check?: StoreCheck; error?: string; certificate?: PresentedCert }>(base + 'store/test', post({ store, keys }))
/**
 * Trusts the certificate a source's address presents, when it is the one
 * its Test showed (CR-16): with its credential when it names one,
 * otherwise with the saved source.
 */
export const trustSourceCert = (sha256: string, source: KnowledgeSource, saved?: string) => call<PinnedWith>(base + 'sources/trust', post({ sha256, source, saved }))
/** Lets a source's pinned certificate go: its own, or its credential's (CR-19). */
export const forgetSourceCert = (id: string) => call<{ forgotten: boolean }>(base + 'sources/' + enc(id) + '/trust', { method: 'DELETE' })
/** Trusts the certificate the saved store presents (CR-16). */
export const trustStoreCert = (sha256: string) => call<PinnedWith>(base + 'store/trust', post({ sha256 }))
/** Lets the store's pinned certificate go (CR-19). */
export const forgetStoreCert = () => call<{ forgotten: boolean }>(base + 'store/trust', { method: 'DELETE' })
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
  store: '' | 'git' | 's3' | 'database' | string
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
  /** The database store's room, in bytes, when the wiki is kept there (KN-37, DB-96). */
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
export const wikiHref = (view: 'pages' | 'drafts' = 'pages') => '#/aiops/mycroft/wiki' + (view === 'pages' ? '' : '/' + view)

/** The Wiki's page list showing only these pages: Review those pages, after a source item was removed or excluded. */
export const wikiPagesHref = (paths: string[]) => '#/aiops/mycroft/wiki?pages=' + paths.map(encodeURIComponent).join(',')

/** AIOps › Mycroft › Sources: the sources, one row each (0.12.1; one row a source since phase-13.1-ai, KN-50). */
export const SOURCES_HREF = '#/aiops/mycroft/sources'

/** A source opened in AIOps › Mycroft › Sources: its items. */
export const sourceHref = (id: string) => `${SOURCES_HREF}/${enc(id)}`

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
  live: 'Live pages',
  web: 'Web site',
}

/** A live address's Test, in words: "OK · 12 KiB · Title", or what was wrong. */
export function liveCheckText(c: LiveCheck): string {
  if (!c.ok) return c.error ?? 'not read'
  const kib = Math.max(1, Math.round((c.bytes ?? 0) / 1024))
  let text = `OK · ${kib} KiB`
  if ((c.pages ?? 1) > 1) text += ` · ${c.pages} pages`
  if (c.title) text += ` · ${c.title}`
  if (c.cut) text += ' · read up to 200 KiB'
  return text
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

// --- Uploaded documents, Mycroft › Sources and exclusions (0.12.1) ---

/** A wiki page, by its path and title. */
export interface PageBrief {
  path: string
  title: string
}

/** A note or a document people uploaded: in the built-in sources 'documents' and 'added'. */
export interface UploadItem {
  source: string
  id: string
  type: 'note' | 'document'
  title: string
  by?: string
  at?: string
  state: 'reading' | 'read' | 'error'
  error?: string
  lastRead?: string
  /** The wiki pages Mycroft wrote that drew on it. */
  pages?: string[]
  /** The page a correction (This is outdated…) is for. */
  corrects?: string
  reported?: string
}

export interface UploadsView {
  items: UploadItem[]
  limits: AddLimits
  mayAdd: boolean
  maySetup: boolean
  store: string
  model: boolean
  /** The built-in sources of uploads switched off: nothing in them is read until a SuperAdmin turns them on. */
  off: { id: string; name: string }[]
}

export const fetchUploads = (background = false) => call<UploadsView>(base + 'uploads', {}, background)
export const removeUpload = (source: string, name: string) => call<{ removed: boolean; pages?: PageBrief[] }>(base + 'uploads/' + enc(source) + '/' + enc(name), { method: 'DELETE' })

/** The types of a source's item. */
export type ItemType = 'note' | 'document' | 'webPage' | 'confluencePage' | 'gitFile' | 's3Object' | 'record' | 'fix' | 'liveAddress'
export type ItemStateName = 'reading' | 'read' | 'error' | 'skipped' | 'excluded' | 'live'

export const ITEM_TYPE_LABEL: Record<ItemType, string> = {
  note: 'Note',
  document: 'Document',
  webPage: 'Web page',
  confluencePage: 'Confluence page',
  gitFile: 'Git file',
  s3Object: 'S3 object',
  record: 'Console record',
  fix: 'Fix',
  liveAddress: 'Live address',
}

export const ITEM_STATE_LABEL: Record<ItemStateName, string> = {
  reading: 'Reading',
  read: 'Read',
  error: 'Error',
  skipped: 'Skipped',
  excluded: 'Excluded',
  live: 'Looked up when asked',
}

/** One item a source holds, as AIOps › Mycroft › Sources lists it. */
export interface SourceItem {
  source: string
  sourceName: string
  kind: SourceKind
  type: ItemType
  id: string
  title: string
  url?: string
  by?: string
  at?: string
  lastRead?: string
  state: ItemStateName
  why?: string
  /** How many wiki pages drew on it. */
  pages?: number
  /** The rule that leaves it out, when it is excluded. */
  exclusion?: Exclusion
  /** Its source is a web site, a Confluence space, a git repository or an S3 bucket. */
  excludable?: boolean
  /**
   * Where it is in its source, relative to the bucket's prefix, the repository's path or the web site's
   * start address (KN-49); shown after the source's name. Absent for what is shown by its title.
   */
  path?: string
  /** The exclusion's pattern as shown, with the source's name, when it is a pattern. */
  rule?: string
  /** The full pattern of everything under it, and that in words relative to its source, for Exclude everything under …; none at the source's root. */
  under?: string
  underLabel?: string
}

/** A source as the Sources filter offers it; 'uploads' is both built-in sources of uploads. */
export interface ItemSource {
  id: string
  name: string
  kind: SourceKind | 'uploads'
  off?: boolean
}

export interface ItemsPage {
  items: SourceItem[]
  total: number
  offset: number
  limit: number
  sources: ItemSource[]
  mayExclude: boolean
  mayRemove: boolean
}

export interface ItemsQuery {
  source?: string
  type?: string
  state?: string
  q?: string
  offset?: number
  limit?: number
}

/** The address of a page of items: only what is set is sent. */
export function itemsUrl(q: ItemsQuery): string {
  const p = new URLSearchParams()
  for (const k of ['source', 'type', 'state', 'q'] as const) if (q[k]) p.set(k, q[k])
  if (q.offset) p.set('offset', String(q.offset))
  if (q.limit) p.set('limit', String(q.limit))
  const qs = p.toString()
  return base + 'items' + (qs ? '?' + qs : '')
}

export const fetchItems = (q: ItemsQuery) => call<ItemsPage>(itemsUrl(q))

/** When a source's last sync finished, and how it went (KN-50). */
export interface SourceSync {
  at: string
  /** How many changed items it read; absent for a sync of every source, or a read no kept sync names. */
  changed?: number
  failed?: boolean
  error?: string
  /** A sync of every source, not of this one alone. */
  every?: boolean
}

/** A source as AIOps › Mycroft › Sources lists it (KN-50). */
export interface SourceSummary {
  id: string
  name: string
  kind: SourceKind
  off?: boolean
  items: number
  failed?: number
  excluded?: number
  /** Absent for a live source, and for one never read. */
  lastSync?: SourceSync
}

export interface SourcesView {
  sources: SourceSummary[]
  mayExclude: boolean
  mayRemove: boolean
}

export const fetchSources = () => call<SourcesView>(base + 'sources')

export interface ItemDetail {
  item: SourceItem
  excerpts: string[]
  excerptCount: number
  pages: PageBrief[]
  /** Whether the wiki records which pages drew on the item. */
  provenance: boolean
}

export const fetchItemDetail = (source: string, id: string) => call<ItemDetail>(base + 'items/detail?source=' + enc(source) + '&id=' + enc(id))
/** What asking Mycroft for a page from an item did (KN-59). */
export interface PageRequestResult {
  run?: string
  pages: number
  text: string
}
/** Has Mycroft write the wiki's pages from one item, whatever its source's trust (KN-59): they wait for review. */
export const writePage = (source: string, id: string) => call<PageRequestResult>(base + 'items/page', post({ source, id }))

export interface ExcludeResult {
  exclusion: Exclusion
  items: number
  pages: PageBrief[]
  text: string
}

export const excludeItems = (source: string, input: { item?: string; pattern?: string; under?: boolean; reason?: string }) => call<ExcludeResult>(base + 'sources/' + enc(source) + '/exclusions', post(input))
export const includeAgain = (source: string, pattern: string, under = false) =>
  call<{ included: boolean; text: string }>(base + 'sources/' + enc(source) + '/exclusions?pattern=' + enc(pattern) + (under ? '&under=1' : ''), { method: 'DELETE' })

/** Ask Mycroft to rewrite without it: one correction per page, each redraft waiting for review. */
export const askRewrite = (pages: string[], title: string, why: 'removed' | 'excluded') => call<{ sent: number; failed?: string[]; text: string }>(base + 'rewrite', post({ pages, title, why }))
