// The issue service's settings and API tokens, for Settings › Issues
// (Phase 12, IS-24, IS-29). The report page has its own calls
// (report/api.ts).
import { call, post } from './call'
// The operators' types build on the reporter's (Cases, Phase 12).
import type { IssueMe, Known, KnownStatus, KnownUpdate, Message, Outcome, Person, Report, Status } from '../report/api'

export interface IssueTarget {
  responseMinutes: number
  resolveMinutes: number
}

export interface IssueSettings {
  company: string
  statusPage: string
  reportURL: string
  matchThreshold: number
  targets: Record<string, IssueTarget>
  retentionDays: number
  ratePerHour: number
  reopenDays: number
  fixedDays: number
  needInfoDays: number
  duplicateHours: number
  bumpP2At: number
  bumpP1At: number
  webhook: { url: string; events?: string[] }
}

export interface IssueToken {
  id: string
  name: string
  account: string
  scopes: string[]
  created: string
  createdBy: { id: string; name?: string }
  lastUsed?: string
}

export const TOKEN_SCOPES = [
  { id: 'reports:create', label: 'File reports' },
  { id: 'reports:read', label: 'Read the reports it filed' },
  { id: 'reports:onBehalf', label: 'File for a person it names' },
] as const

const base = '/api/issues/v1/'

export const fetchIssueSettings = () => call<IssueSettings>(base + 'settings')

export const saveIssueSettings = (s: IssueSettings) => call<IssueSettings>(base + 'settings', { method: 'PUT', body: JSON.stringify(s) })

export const fetchIssueTokens = () => call<{ tokens: IssueToken[] }>(base + 'tokens').then((r) => r.tokens)

export const createIssueToken = (name: string, account: string, scopes: string[]) => call<{ token: IssueToken; secret: string }>(base + 'tokens', post({ name, account, scopes }))

export const revokeIssueToken = (id: string) => call<{ revoked: boolean }>(`${base}tokens/${encodeURIComponent(id)}`, { method: 'DELETE' })

// --- Cases (Phase 12, CS-07 to CS-14) ---
// The operators' calls, for Cases › Inbox, Reports and Known issues: the
// same API the report page uses, as an operator sees it.

export type { IssueMe, KnownStatus, Outcome, Person, Status }

/** A message as an operator sees it: drafts and internal notes too. */
export interface OpMessage extends Message {
  draft?: boolean
  draftBy?: string
  internal?: boolean
  sentBy?: Person
}

/** How a report stands against its priority's targets (IS-05). */
export interface SLA {
  responseDue?: string
  resolveDue?: string
  responseLate?: boolean
  resolveLate?: boolean
}

/** A known issue as an operator sees it. */
export interface OpKnown extends Known {
  symptoms?: string[]
  services?: string[]
  owner?: Person
  updates?: KnownUpdate[]
  source: string
  draft?: boolean
  followers?: Person[]
  created: string
  resolved?: string
  linked?: number[]
  /** Agents still see its problem (wave 6): how often, and last. */
  stillPresent?: { count: number; last: string }
  /** The cases closed as this known issue: "C-12". */
  cases?: string[]
  /** Housekeeping found it a duplicate (wave 6g): not matched to cases until made active again. */
  inactive?: boolean
  /** With inactive: the known issue it duplicates, "KI-2". */
  duplicateOf?: string
}

/** A report as an operator sees it (IS-22's fields included). */
export interface OpReport extends Omit<Report, 'messages' | 'known'> {
  messages?: OpMessage[]
  known?: OpKnown
  owner?: Person
  service?: string
  serviceConfidence?: number
  serviceBy?: string
  matchConfidence?: number
  priorityBy?: string
  rejected?: string[]
  duplicateOf?: number
  similar?: number[]
  watchers?: Person[]
  technical?: string
  cause?: string
  evidence?: string[]
  acknowledged?: string
  firstResponse?: string
  resolved?: string
  closed?: string
  sla?: SLA
}

/** A known issue or service a text may be about (IS-10). */
export interface Candidate {
  id: string
  title: string
  confidence: number
  why?: string[]
}

export interface MatchPreview {
  known: Candidate[]
  services: Candidate[]
  threshold: number
}

export interface RunsOn {
  kind: string
  value: string
  cluster?: string
}

export interface IssueService {
  id: string
  name: string
  description?: string
  runsOn?: RunsOn[]
  contacts?: string[]
  aliases?: string[]
}

/** An operator's change to a report; what is left out stays. */
export interface Triage {
  priority?: number
  owner?: Person
  service?: string
  knownIssue?: string
  duplicateOf?: number
  status?: Status
  outcome?: Outcome
  reason?: string
  askInfo?: boolean
}

/** What a known issue is saved with; what is left out stays. */
export interface KnownInput {
  title?: string
  symptoms?: string[]
  services?: string[]
  status?: KnownStatus
  workaround?: string
  expected?: string
  owner?: Person
  kind?: 'normal' | 'planned'
  window?: { start: string; end: string }
  link?: string
  /** false makes an inactive one active again, and clears its duplicateOf (wave 6g). */
  inactive?: boolean
}

/** The filters of the Reports tab, as the API takes them. */
export interface ReportFilter {
  status?: string
  open?: boolean
  service?: string
  owner?: string
  priority?: string
  known?: string
  q?: string
}

const n = (r: number) => `${base}reports/${r}`
const k = (id: string) => `${base}known/${encodeURIComponent(id)}`
const patch = (body: unknown): RequestInit => ({ method: 'PATCH', body: JSON.stringify(body) })

export const fetchIssueMe = (background = false) => call<IssueMe>(base + 'me', {}, background)

/** Every report the caller may see, with the filters. */
export function fetchReports(f: ReportFilter = {}, background = false): Promise<OpReport[]> {
  const q = new URLSearchParams({ scope: 'all' })
  for (const [key, v] of Object.entries(f)) if (v !== undefined && v !== '' && v !== false) q.set(key, String(v))
  return call<{ reports: OpReport[] }>(`${base}reports?${q.toString()}`, {}, background).then((r) => r.reports)
}

export const fetchReport = (r: number) => call<OpReport>(n(r))
export const triageReport = (r: number, t: Triage) => call<OpReport>(n(r), patch(t))
export const sendMessage = (r: number, text: string, internal = false) => call<OpReport>(n(r) + '/messages', post(internal ? { text, internal } : { text }))
export const draftReply = (r: number, text: string) => call<OpReport>(n(r) + '/drafts', post({ text }))
/** Sends a draft, edited when text is given. */
export const sendDraft = (r: number, id: number, text?: string) => call<OpReport>(`${n(r)}/drafts/${id}/send`, text ? post({ text }) : { method: 'POST' })
export const discardDraft = (r: number, id: number) => call<OpReport>(`${n(r)}/drafts/${id}`, { method: 'DELETE' })
export const watchReport = (r: number, on: boolean) => call<OpReport>(n(r) + '/watch', { method: on ? 'PUT' : 'DELETE' })
export const previewMatch = (text: string) => call<MatchPreview>(base + 'match', post({ text }))

/** The known issues: drafts and resolved ones too for an operator. */
export const fetchKnownAll = (background = false) => call<{ known: OpKnown[] }>(base + 'known?all=true', {}, background).then((r) => r.known)
export const fetchKnownIssue = (id: string) => call<OpKnown>(k(id))
export const createKnown = (in_: KnownInput) => call<OpKnown>(base + 'known', post(in_))
export const saveKnown = (id: string, in_: KnownInput) => call<OpKnown>(k(id), patch(in_))
export const addKnownUpdate = (id: string, text: string, status?: KnownStatus) => call<OpKnown>(k(id) + '/updates', post(status ? { text, status } : { text }))
export const acceptKnown = (id: string) => call<OpKnown>(k(id) + '/accept', { method: 'POST' })
export const followKnownIssue = (id: string, on: boolean) => call<OpKnown>(k(id) + '/follow', { method: on ? 'PUT' : 'DELETE' })

export const fetchServices = (background = false) => call<{ services: IssueService[] }>(base + 'services', {}, background).then((r) => r.services)
// --- end Cases ---
