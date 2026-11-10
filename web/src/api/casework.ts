// The cases (Phase 12, CW-01 to CW-14; since wave 5, CU-01 to CU-14; since
// wave 6, one flow for findings and cases): under /api/v1/cases/cases. A
// case is the one record of every problem, whatever opened it: a person's
// report (the report page, the avatar menu, Sherlock's "Report this", a
// system through the issue API), an agent's finding, an alert, or a
// person by hand. One numbering, C-<n>; priorities P0 (outage) to P4
// (question). New (nobody has taken it), In progress (someone owns it),
// Closed with exactly one outcome; a person may reopen a closed case,
// with a reason.
import { call, post } from './call'
import type { KnownStatus, Outcome, Person, Status } from '../report/api'
import type { Actor, ActorLike } from './actor'

export type { Actor, Outcome, Person }

// --- Owner's feedback on Cases (Phase 12, wave 6c, CF-35 to CF-38) ---

/** What kind of problem a case is (CF-37). */
export type CaseType = 'security' | 'outage' | 'performance' | 'availability' | 'configuration' | 'capacity' | 'compliance' | 'question' | 'other'

/** The case types in words, in the order the pickers offer them. */
export const CASE_TYPES: Record<CaseType, string> = {
  security: 'Security',
  outage: 'Outage',
  performance: 'Performance',
  availability: 'Availability',
  configuration: 'Configuration',
  capacity: 'Capacity',
  compliance: 'Compliance',
  question: 'Question',
  other: 'Other',
}

export const CASE_TYPE_IDS = Object.keys(CASE_TYPES) as CaseType[]

/** A type in words; one the console does not know reads as it came, none as "". */
export const caseTypeText = (t: string | undefined) => (t ? (CASE_TYPES[t as CaseType] ?? t) : '')

/** An incident number: 1 to 64 printable characters (CF-36). */
export const INCIDENT_MAX = 64

/** Why an incident number cannot be kept, or "" when it can ("" clears it). */
export function incidentError(v: string): string {
  const t = v.trim()
  if (!t) return ''
  if (t.length > INCIDENT_MAX) return `At most ${INCIDENT_MAX} characters.`
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(t)) return 'Only printable characters.'
  return ''
}

/** P0 an outage, P1 blocking, P2 a service degraded, P3 preemptive, P4 a question. */
export type CasePriority = 0 | 1 | 2 | 3 | 4

/** What a new case starts from: an alert, or what a person typed. */
export interface NewCaseInput {
  fromAlert?: CaseAlertInput
  title?: string
  detail?: string
  cluster?: string
  /** What it is about, in any words ("shop/Deployment/web", "the disk of db-1 at 90%"), or nothing. */
  resource?: string
  priority?: CasePriority
  /** A regression of a case closed as fixed: "C-12". */
  regressionOf?: string
  /** A known issue it is: the case is made already closed as that known issue ("KI-4"). */
  knownIssue?: string
  /** What kind of problem it is; the server's default is other (CF-37). */
  type?: CaseType
  /** The incident number elsewhere, e.g. "INC0012345" (CF-36). */
  incident?: string
}

/**
 * The alert a case is opened from: its priority follows its severity
 * (critical P0, warning P2). Since CF-84 its name is its reason
 * (alertReason), and key its key on the Alerts tab, which the case keeps
 * to link the alert.
 */
export interface CaseAlertInput {
  cluster: string
  key?: string
  name: string
  severity: string
  object?: string
  summary?: string
}

export interface CaseRef {
  id: string
  number: number
  title: string
}

/** New, In progress or Closed: the issue service's statuses, for every case. */
export type CaseStatus = Status

/** What opened a case; "finding" is an agent's (agent-found). */
export type CaseSourceKind = 'report' | 'finding' | 'manual' | 'alert'

/** What an agent's repeats of a problem are matched by: the cluster, the object ("" cluster-wide) and the check. */
export interface CaseKey {
  cluster: string
  object: string
  check: string
}

/** How often the agents saw an agent-found case's problem, first and last. */
export interface CaseSeen {
  count: number
  first: string
  last: string
}

/** How a case like this one was closed before: shown on a repeat after no action needed, and on a regression. */
export interface PreviousClose {
  case: string
  outcome: Outcome
  explanation: string
  by: string
  at: string
}

export interface CaseSource {
  kind: CaseSourceKind
  finding?: string
  run?: string
  job?: string
  check?: string
  severity?: string
  /** The alert's name: since CF-84 its reason. */
  alert?: string
  /** The alert's key on the Alerts tab ("pod:shop/web-1"), for a case opened from one since CF-84. */
  alertKey?: string
}

/** The Kubernetes object a case's words name, when they name one: the console links it. */
export interface CaseObject {
  namespace?: string
  kind: string
  name: string
}

/** The SLA's targets a case has passed (CU-06). */
export interface CaseLate {
  response?: boolean
  resolve?: boolean
}

/** The agent's suggested fix: what to do, and the Watson commands a person runs as themselves. */
export interface CaseFix {
  summary?: string
  commands?: string[]
}

export interface CaseAnalysis {
  run?: string
  at: string
  summary?: string
  cause?: string
  evidence?: string[]
  fix: CaseFix
  /**
   * The analysis's verdict that there is no problem, the system working as
   * intended, with why (CF-82); it lowers a case the console or an agent
   * opened to P3 (CF-83).
   */
  expected?: boolean
  expectedWhy?: string
  /** Why it did not finish. */
  error?: string
}

/** The RCA a case was fixed with: part of the case, no record of its own (wave 6e). */
export interface CaseRCA {
  /** Only on a case fixed before wave 6e, when RCAs had ids ("RCA-4"); it opens nothing now. */
  id?: string
  case: string
  title: string
  cluster?: string
  resource?: string
  finding?: string
  priority?: number
  cause: string
  /** What was done: the case's fix note, the one description of the fix. */
  fix: string
  prevention: string
  text?: string
  by: string
  at: string
  /** The other cases closed as fixed together with this one, with the same RCA: ["C-3", "C-4"]. */
  cases?: string[]
}

export interface CaseEvent {
  at: string
  what: string
  /** Who did it, with what they are (CF-35). */
  by?: ActorLike
}

/** A case as a list row shows it. */
export interface CaseBrief extends CaseRef {
  priority: CasePriority
  status: CaseStatus
  outcome?: Outcome
  /** Waits for the people who work the cases: New or In progress (or a known issue the person owns). */
  pending: boolean
  /** Opened from an agent's finding. */
  agentFound: boolean
  key?: CaseKey
  seen?: CaseSeen
  cluster?: string
  /** Its cluster's name (Settings › Clusters), and false when the console no longer has that cluster (CF-60). */
  clusterName?: string
  clusterConnected?: boolean
  /** What it is about, in the words given. */
  resource?: string
  object?: CaseObject
  source: CaseSource
  /** Who opened it, or reported it: the reporter, or the agent for an agent-found case (CF-35). */
  createdBy?: ActorLike
  /** What kind of problem it is (CF-37). */
  type?: CaseType | string
  /** The incident number elsewhere (CF-36). */
  incident?: string
  /** It waits in the queue of the case analysis job (CF-32). */
  analysisQueued?: boolean
  /** Who changed it last, and when (CF-35). */
  lastBy?: Actor
  lastAt?: string
  created: string
  updated: string
  /** When it took its status. */
  since: string
  /** For a pending case: since when it has waited. */
  waitingSince?: string
  assignee?: Person
  late?: CaseLate
  service?: string
  analyzing?: boolean
  /** The agent suggested a fix that waits for a person. */
  fixProposed?: boolean
  run?: string
  /** The RCA's id: only on a case fixed before wave 6e. */
  rcaId?: string
  /** The cause in the RCA it was fixed with (at most 300 characters): what the Inbox search finds past RCAs by. */
  rcaCause?: string
  /** It waits for the reporter's answer to a question. */
  askedReporter?: boolean
  /** "C-12": a case closed as fixed this one is the problem of again. */
  regressionOf?: string
  /** "C-12": an open case on the same object with a like title, for a person to confirm. */
  possibleDuplicateOf?: string
  /** "C-12": the same problem as a case kept apart (an original closed as no action needed). */
  sameAs?: string
  /** "C-12": the original of a case closed as duplicate. */
  duplicateOf?: string
  /** The known issue a case closed as known issue is ("KI-4"). */
  knownIssue?: string
  /** The exception a case closed as exception made ("EX-3"). */
  exception?: string
  /** The explanation it was closed with; for an exception, its reason. */
  explanation?: string
  previous?: PreviousClose
  /** Whether the SLA's targets apply: every human-created case, an agent-found one at P0 or P1. */
  slaApplies: boolean
  closedAt?: string
  /** When a later run no longer saw the problem of a case closed as fixed. */
  verified?: string
  /** The same check was closed as no action needed three times: an exception may fit. */
  suggestException?: boolean
  /** Where the check of a fix applied by a change stands (CF-31). */
  verification?: Verification
}

/**
 * Where the check of a fix a change applied stands (CF-29 to CF-31):
 * waiting for the next run of the job that found it, verified at P0 or P1
 * (the RCA closes it), or found again by that run.
 */
export type Verification = 'waiting' | 'verified' | 'notHeld'

/** A change that resolves a case, with its card's state now ("gone" once let go of). */
export interface CaseCard {
  id: string
  title?: string
  state: string
  cluster?: string
  updated?: string
  replacedBy?: string
  /** Who linked it to the case: "agent" (the one that proposed it), or the person's name (CF-28). */
  linkedBy: 'agent' | string
  /** When it was applied. */
  appliedAt?: string
  /** Who proposed it, and who decided it (CF-35), when the server says. */
  proposedBy?: Actor
  decidedBy?: Actor
}

/** One entry of a case's conversation, as the people who work the cases see it. */
export interface CaseMessage {
  id: number
  at: string
  kind: 'reporter' | 'operator' | 'system'
  from: Person
  text: string
  draft?: boolean
  draftBy?: string
  internal?: boolean
  sentBy?: Person
}

/** A known issue as a case shows it. */
export interface CaseKnown {
  id: string
  title: string
  status: KnownStatus
  workaround?: string
  expected?: string
}

/** How a case stands against its priority's targets. */
export interface CaseSLA {
  target: { responseMinutes: number; resolveMinutes: number }
  responseDue?: string
  resolveDue?: string
  responseLate?: boolean
  resolveLate?: boolean
}

/** The outcomes the close route takes; Fixed, Known issue and Exception have their own routes, all under one Close… (CF-42). */
export type CloseOutcome = 'duplicate' | 'noAction'

/** Close: the outcome, the explanation (always) and, for a duplicate, the original. */
export interface Closing {
  outcome: CloseOutcome
  explanation: string
  /** The original of a duplicate: "C-12". */
  original?: string
}

/** A person's RCA: required to fix a case at P0 and P1. Its fix is what was done, sent beside it. */
export interface Completion {
  cause: string
  prevention: string
  text?: string
}

/**
 * What is being written on a case and has not been sent: the case's one
 * draft, shared by everyone who works it (CF-59); those who only read the
 * cases read it, a reporter never sees it. Save keeps it for everyone.
 */
export interface CaseDraft {
  reply?: string
  fix?: string
  /** Close… being written: any outcome picked (CF-42), the explanation, and a duplicate's original ("C-12"). */
  close?: { outcome: Outcome | ''; explanation: string; original?: string }
  rca?: Completion
  /** Who saved it last, or sent a part of it. */
  by?: string
  /** When, as the server stamped it: what the next save starts from (its base). */
  updated?: string
  /** Who wrote each part (reply, fix, close, rca). */
  drafters?: Record<string, string[]>
}

/** A save of the draft refused: someone changed it since this editor started from it; 409 with the draft as it is (CF-59). */
export interface DraftConflict {
  error: string
  draft: CaseDraft
}

/** The fields edited in place, as an edit names them (CF-60). */
export type CaseField = 'title' | 'detail' | 'cluster' | 'resource' | 'incident'

/** An edit in place refused: the field changed since the edit started; 409 with its value now, and who changed the case last (CF-60). */
export interface FieldConflict {
  error: string
  field: CaseField
  current: string
  by?: string
  at?: string
}

/** What a person does instead when a duplicate's original is closed. */
export interface Offer {
  kind: 'reopenOriginal' | 'regression' | 'sameKnownIssue' | 'noAction' | 'removeException' | 'sameAs'
  original: string
  knownIssue?: string
  exception?: string
  explanation?: string
}

/** Whether a case may close as the duplicate of an original (at the end of its chain), and what fits instead. */
export interface DuplicateCheck {
  allowed: boolean
  original: string
  status: CaseStatus
  outcome?: Outcome
  offers: Offer[]
  reason?: string
}

/** A case like the one being written, for New Case's panel. */
export interface SimilarCase {
  id: string
  number: number
  title: string
  status: CaseStatus
  outcome?: Outcome
  assignee?: Person
  explanation?: string
  /** The RCA's id: only on a case fixed before wave 6e. */
  rcaId?: string
  /** The cause in the RCA it was fixed with. */
  rcaCause?: string
  closedAt?: string
  score: number
}

export interface SimilarKnown {
  id: string
  title: string
  status: KnownStatus
  workaround?: string
  expected?: string
  score: number
}

export interface SimilarException {
  id: string
  cluster: string
  object: string
  check: string
  reason: string
  by: string
  at: string
}

/** What matches a case being written: open cases, known issues, exceptions, and cases closed lately. */
export interface Similar {
  open: SimilarCase[]
  known: SimilarKnown[]
  exceptions: SimilarException[]
  recentNoProblem: SimilarCase[]
  recentFixed: SimilarCase[]
}

/** Settings › Cases: how long things are kept and how much a run may do. */
export interface CasesSettings {
  keepAgentClosedDays: number
  lookBackDays: number
  exceptionStaleDays: number
  exceptionsPerAgent: number
  newCasesPerRun: number
  /**
   * After how many minutes a critical alert still going that no case covers
   * opens a case by itself (CF-78); 0 is never. Left out, the server keeps
   * what it has.
   */
  criticalAlertMinutes?: number
  /** How many agent-found cases people closed as duplicates (read only). */
  duplicatesOfAgentCases?: number
  /** Whether this person may change them (a SuperAdmin). */
  maySet?: boolean
}

/** One case, whole. */
export interface Case extends CaseBrief {
  detail?: string
  scrubbed?: boolean
  pruned?: boolean
  reporter?: Person
  filedBy?: Person
  openedBy?: Person
  serviceName?: string
  serviceBy?: string
  serviceConfidence?: number
  known?: CaseKnown
  matchConfidence?: number
  rejected?: string[]
  similar?: number[]
  messages: CaseMessage[]
  /** What the report triage job found on a reported case. */
  investigation?: { technical?: string; plain?: string; cause?: string; evidence?: string[] }
  sla?: CaseSLA
  acknowledged?: string
  firstResponse?: string
  resolved?: string
  analysis?: CaseAnalysis
  runs?: string[]
  /** Every change that resolves the case, whoever linked it (CF-28). */
  proposals: CaseCard[]
  /** The fix applied: by a person's Fix, or by a change (its card's id) once applied (CF-29). */
  fixApplied?: { text: string; by: string; at: string; change?: string }
  /** The job whose next run checks the fix a change applied. */
  verifyJob?: { id: string; label: string }
  rca?: CaseRCA
  history: CaseEvent[]
  draft?: CaseDraft
  /** Whether this person works the cases: opens, changes, fixes and closes them. */
  mayChange: boolean
  /** Agent-found cases on the same object, or open human-created ones an agent-found case is about. */
  related: CaseRef[]
  /** The outcomes the server allows this case now, for this person. */
  allowedOutcomes: Outcome[]
  /** Fixing it needs an RCA: P0 and P1. */
  rcaRequired: boolean
  /** Whether this person may reopen it. */
  mayReopen: boolean
  /**
   * The commands run from the case's drawer through Watson (CF-48): every one
   * while it is open, the changes that finished done once it is closed.
   */
  commands?: CaseCommand[]
  /** Those changes' lines: what closing as Fixed starts from (CF-49). */
  fixCommands?: string[]
  /** What the validation rule (key.check) of an agent-found case means, when the catalogue says (build 14). */
  ruleMeaning?: string
  /** The fix saved when it was closed as Fixed with Save fix on: "F-3" (wave 6g). */
  fix?: string
  /** For those who work it: the clusters it may be put on, as New Case lists them (CF-60). */
  clusters?: { id: string; name: string }[]
}

/** A command run from a case's drawer (CF-48): never its output. */
export interface CaseCommand {
  /** Watson's item. */
  id: string
  line: string
  by: string
  at: string
  change: boolean
  status: string
  exit?: number
  approver?: string
  finished?: string
}

/** Where a command sent from a case says it came from: "case:C-12" (CF-48). */
export const caseFrom = (id: string) => `case:${id.toUpperCase().startsWith('C-') ? id.toUpperCase() : `C-${id}`}`

/** What closing as Fixed starts from: the case's changes that finished done, one a line (CF-49). */
export function fixPrefill(c: Pick<Case, 'fixCommands'>): string {
  return (c.fixCommands ?? []).join('\n')
}

export interface CasesView {
  cases: CaseBrief[]
  clusters: { id: string; name: string }[]
  mayChange: boolean
  /** How the cases name this person (their user name), for Mine. */
  me: string
}

export interface CaseQuery {
  status?: CaseStatus | ''
  outcome?: Outcome | ''
  /** Only the pending ones (CU-08). */
  pending?: boolean
  cluster?: string
  priority?: number
  /** People's cases (reported, by hand, from an alert) or the agents'. */
  source?: 'people' | 'agents' | ''
  /** Of one type (CF-37). */
  type?: string
  /** Those assigned to this person. */
  mine?: boolean
  /** Those nobody is assigned. */
  unassigned?: boolean
  q?: string
}

/** What a person changes on a case; what is left out stays. */
export interface CaseChange {
  title?: string
  detail?: string
  priority?: CasePriority
  cluster?: string
  resource?: string
  /** An id of "" leaves it to nobody. */
  assignee?: Person
  service?: string
  type?: CaseType
  /** "" clears it. */
  incident?: string
  /** What an edit in place started from, for each field it changes: a field changed since is refused with 409 (CF-60). */
  base?: Partial<Record<CaseField, string>>
  /** Saves over a newer value (Overwrite); the history says so. */
  force?: boolean
}

/** The agent's draft of an RCA, which a person edits; its fix fills What was done when that is empty. */
export interface RCADraft {
  cause: string
  fix: string
  prevention: string
  run?: string
}

/** A known issue a case may be linked to. */
export interface OpenKnown {
  id: string
  title: string
  status: KnownStatus
}

export interface SLATargets {
  targets: Record<string, { responseMinutes: number; resolveMinutes: number }>
  maySet: boolean
}

const base = '/api/v1/cases/cases'
const enc = encodeURIComponent
const put = (body: unknown): RequestInit => ({ method: 'PUT', body: JSON.stringify(body) })
const one = (id: string | number) => `${base}/${enc(String(id))}`

export function fetchCases(q: CaseQuery = {}, background = false): Promise<CasesView> {
  const p = new URLSearchParams()
  if (q.status) p.set('status', q.status)
  if (q.outcome) p.set('outcome', q.outcome)
  if (q.pending) p.set('pending', '1')
  if (q.cluster) p.set('cluster', q.cluster)
  if (q.priority !== undefined) p.set('priority', String(q.priority))
  if (q.source) p.set('source', q.source)
  if (q.type) p.set('type', q.type)
  if (q.mine) p.set('mine', '1')
  if (q.unassigned) p.set('unassigned', '1')
  if (q.q) p.set('q', q.q)
  const qs = p.toString()
  return call<CasesView>(base + (qs ? '?' + qs : ''), {}, background)
}

export const fetchCase = (id: string | number, background = false) => call<Case>(one(id), {}, background)

/** Opens a case; the case analysis job starts on it when it is on. */
export async function createCase(input: NewCaseInput): Promise<CaseRef> {
  return call<Case>(base, post(input))
}

/** Opens a case and returns it whole. */
export const openCase = (input: NewCaseInput) => call<Case>(base, post(input))
export const updateCase = (id: string, change: CaseChange) => call<Case>(one(id), put(change))
/** A reply to the reporter, an internal note, or a question that waits for the answer (ask). */
export const sayOnCase = (id: string, text: string, how: { internal?: boolean; ask?: boolean } = {}) => call<Case>(one(id) + '/messages', post({ text, ...how }))
/** Sends a drafted reply (the investigator's), edited when text is given. */
export const sendCaseDraft = (id: string, msg: number, text?: string) => call<Case>(`${one(id)}/drafts/${msg}/send`, text ? post({ text }) : { method: 'POST' })
export const discardCaseDraft = (id: string, msg: number) => call<Case>(`${one(id)}/drafts/${msg}`, { method: 'DELETE' })
/**
 * Save: keeps the case's one draft for everyone who works it (CF-59). base
 * is the draft's updated stamp this editor started from ("" for none); a
 * draft changed since is refused with 409 (DraftConflict), unless force
 * saves over it.
 */
export const keepDraft = (id: string, d: CaseDraft, base = '', force = false) =>
  call<Case>(one(id) + '/draft', put({ reply: d.reply, fix: d.fix, close: d.close, rca: d.rca, ...(base ? { base } : {}), ...(force ? { force } : {}) }))
export const dropDraft = (id: string, base = '', force = false) => {
  const q = new URLSearchParams()
  if (base) q.set('base', base)
  if (force) q.set('force', '1')
  const qs = q.toString()
  return call<Case>(one(id) + '/draft' + (qs ? '?' + qs : ''), { method: 'DELETE' })
}
/** Closes the case as a known issue: an existing one (its id), or one made from it. */
export const closeKnown = (id: string, link: { id?: string; create?: { title: string; symptoms?: string[]; workaround?: string; services?: string[] } }) => call<Case>(one(id) + '/known', post(link))
/**
 * Fix: what was done, with an RCA (required at P0 and P1) whose fix is that
 * text; the case closes as fixed. saveFix false keeps it from becoming a
 * fix in Mycroft › Fixes; left out, the server saves one (wave 6g).
 */
export const fixCase = (id: string, text: string, rca?: Completion, saveFix?: boolean) =>
  call<Case>(one(id) + '/fix', post({ text, ...(rca ? { rca } : {}), ...(saveFix === undefined ? {} : { saveFix }) }))
/** Close as duplicate or no action needed; a duplicate whose original does not allow it answers 409 with the check. */
export const closeCase = (id: string, c: Closing) => call<Case>(one(id) + '/close', post(c))
/** Whether the case may close as a duplicate of the original, and what fits instead. */
export const checkDuplicate = (id: string, original: string) => call<DuplicateCheck>(`${one(id)}/duplicate-check?original=${enc(original)}`)
/** Does what a duplicate check offered instead. */
export const applyOffer = (id: string, offer: Offer) => call<Case>(one(id) + '/offer', post(offer))
/** Closes an agent-found case as an accepted exception: the agents stop reporting it; a last day is optional. */
export const closeException = (id: string, reason: string, expires?: string) => call<Case>(one(id) + '/exception', post(expires ? { reason, expires } : { reason }))
export const reopenCase = (id: string, reason: string) => call<Case>(one(id) + '/reopen', post({ reason }))
/** Add to a case instead of opening another: the person follows it, and the text is added. */
export const joinCase = (id: string, text: string) => call<Case>(one(id) + '/join', post({ text }))
/** What matches a case being written (no agent runs). */
export const fetchSimilar = (q: { title: string; detail?: string; cluster?: string; resource?: string }) => call<Similar>('/api/v1/cases/similar', post(q))
export const analyzeCase = (id: string) => call<Case>(one(id) + '/analyze', post({}))
export const draftRCA = (id: string) => call<RCADraft>(one(id) + '/rca-draft', post({}))
export const fetchOpenKnown = () => call<{ known: OpenKnown[] }>('/api/v1/cases/known-open').then((r) => r.known)
export const fetchSLA = () => call<SLATargets>('/api/v1/cases/sla')
export const saveSLA = (targets: SLATargets['targets']) => call<SLATargets>('/api/v1/cases/sla', put({ targets }))
/** Links a change to the case: it resolves it too (CF-28); 409 when the card is on another cluster, linked already, or the case is closed. */
export const linkChange = (id: string, card: string) => call<Case>(one(id) + '/changes', post({ card }))
/** Unlinks a change a person linked; an agent's own link stays (409). */
export const unlinkChange = (id: string, card: string) => call<Case>(`${one(id)}/changes/${enc(card)}`, { method: 'DELETE' })
// --- Many cases at once (Phase 12, wave 6c, CF-38) ---

/** What Inbox's bar does to every case picked. */
export type BulkAction = 'assign' | 'priority' | 'type' | 'incident' | 'analyze' | 'close' | 'fix' | 'known' | 'duplicate' | 'exception'

/** The fields of the one action, as its single route takes them. */
export interface BulkFields {
  assignee?: Person
  priority?: CasePriority
  type?: CaseType
  incident?: string
  outcome?: CloseOutcome
  explanation?: string
  original?: string
  text?: string
  rca?: Completion
  id?: string
  reason?: string
  expires?: string
  /** Fixed: whether one fix is saved for all the cases fixed together; left out, it is (wave 6g). */
  saveFix?: boolean
}

/** How one case took it: done, or refused with why (and, for a duplicate, what fits instead). */
export interface BulkResult {
  id: string
  ok: boolean
  error?: string
  offers?: Offer[]
}

/** The most cases one request takes. */
export const BULK_MAX = 100

/** One action for up to 100 cases, each checked by the rules of its single route; the answer says how each took it. */
export const bulkCases = (cases: string[], action: BulkAction, fields: BulkFields = {}) => call<{ results: BulkResult[] }>(base + '/bulk', post({ cases, action, ...fields }))

export const fetchCasesSettings = () => call<CasesSettings>('/api/v1/cases/settings')
export const saveCasesSettings = (s: CasesSettings) => call<CasesSettings>('/api/v1/cases/settings', put(s))

/** An open case that covers an alert (CF-77). */
export interface CoverCase {
  id: string
  number: number
  title: string
  priority: number
}

/**
 * The open cases that may cover a cluster's alerts, newest first (CF-77): by
 * the object they are about, lower case ("shop/pod/web-1"), and by the key of
 * the alert whose diagnosis run sent its findings to them.
 */
export interface AlertCovers {
  objects: Record<string, CoverCase[]>
  alerts: Record<string, CoverCase[]>
}

export const fetchAlertCovers = (cluster: string, background = false) => call<AlertCovers>(`/api/v1/cases/alert-cases?cluster=${enc(cluster)}`, {}, background)

/** The open cases that cover one alert, newest first: by its key, then by its object. */
export function coversOf(c: AlertCovers | null | undefined, key: string, object: string): CoverCase[] {
  if (!c) return []
  const out: CoverCase[] = []
  for (const x of [...(c.alerts[key] ?? []), ...(c.objects[object.toLowerCase()] ?? [])]) if (!out.some((y) => y.number === x.number)) out.push(x)
  return out.sort((a, b) => b.number - a.number)
}

/** Where a case opens: its drawer in the Inbox, #/cases/inbox/case/12. */
export const caseHref = (number: number | string) => `#/cases/inbox/case/${enc(String(number).replace(/^C-/i, ''))}`

export const CASE_STATUS: Record<CaseStatus, string> = {
  new: 'New',
  inProgress: 'In progress',
  closed: 'Closed',
}

/** The outcomes, as a case says them after "Closed: ". */
export const OUTCOMES: Record<Outcome, string> = {
  fix: 'Fixed',
  duplicate: 'Duplicate',
  noAction: 'No action needed',
  knownIssue: 'Known issue',
  exception: 'Exception',
}

export const SOURCE: Record<CaseSourceKind, string> = {
  report: 'Report',
  finding: 'Agent',
  manual: 'Manual',
  alert: 'Alert',
}

/** The check of a fix a change applied, as a tag says it, with a line that says more (CF-31). */
export const VERIFICATION: Record<Verification, { tag: string; tone: string; hint: string }> = {
  waiting: { tag: 'fix applied', tone: 'info', hint: 'A change applied its fix; the next run of the job that found it checks that it holds' },
  verified: { tag: 'fix verified', tone: 'ok', hint: 'The next run no longer saw it; the RCA closes it' },
  notHeld: { tag: 'fix did not hold', tone: 'err', hint: 'The next run found it again after the change was applied' },
}

/** A case's status as people see it (CF-67): an open case that waits for its reporter's answer is shown "Waiting for the reporter", though kept In progress. */
export type ShownStatus = CaseStatus | 'waiting'

/** The statuses as the Inbox's Status filter offers them, in their usual order. */
export const SHOWN_STATUS: Record<ShownStatus, string> = {
  new: 'New',
  inProgress: 'In progress',
  waiting: 'Waiting for the reporter',
  closed: 'Closed',
}

/** Which status a case is shown in (CF-67). */
export const caseShownStatus = (c: Pick<CaseBrief, 'status' | 'askedReporter'>): ShownStatus => (c.askedReporter && c.status !== 'closed' ? 'waiting' : c.status)

export const caseStatusTone = (s: ShownStatus, outcome?: Outcome) => (s === 'new' ? 'warn' : s === 'inProgress' ? 'info' : s === 'waiting' ? 'muted' : outcome === 'fix' ? 'ok' : 'muted')

/** A case's status in words: a closed one with its outcome, an open one that waits for its reporter as that (CF-67). */
export function caseStatusText(c: Pick<CaseBrief, 'status' | 'outcome' | 'askedReporter'>): string {
  const shown = caseShownStatus(c)
  return shown === 'closed' && c.outcome ? `Closed: ${OUTCOMES[c.outcome]?.toLowerCase() ?? c.outcome}` : (SHOWN_STATUS[shown] ?? String(c.status))
}

/** Whether a case waits for the people who work the cases (CU-08). */
export const waiting = (c: { pending?: boolean }) => !!c.pending

/** A case's number from "C-12", "12" or 12; 0 for anything else. */
export const caseNumber = (s: string | number) => {
  const n = Number(String(s).trim().replace(/^c-/i, '').replace(/^#/, ''))
  return Number.isInteger(n) && n > 0 ? n : 0
}
