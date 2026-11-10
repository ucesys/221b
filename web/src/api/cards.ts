// The agents' proposals as durable Watson cards (Phase 12, ADR-99 to
// ADR-102): listed in AIOps › Inbox, approved or rejected with a reason,
// checked again; and a SuperAdmin's standing approvals and notification
// channels, in Settings › Watson.
import { call, CallError, post } from './call'
import { fmtTs, localTimes } from '../lib/format'
import type { Actor } from './actor'
import type { CaseCommand } from './casework'

export interface CardPerson {
  key: string
  name: string
}

export interface CardDecision {
  by: CardPerson
  at: string
  comment?: string
  /** The standing approval that gave it. */
  standing?: string
}

export interface CardEvidence {
  kind: string
  text: string
  ref?: string
  at?: string
}

export interface CardAction {
  kind: string
  args: Record<string, unknown>
  impact?: string
  risk?: '' | 'low' | 'medium' | 'high'
}

/** What Watson read of an action when it was proposed. */
export interface CardPlan {
  summary: string
  targets?: string[]
  impact?: string[]
  fingerprint?: string
  unread?: string
  // warning is what approving would do that is likely not meant: a fleet
  // with no node now (KN-30).
  warning?: string
}

export interface CardRecheck {
  at: string
  ok: boolean
  why?: string
  checked?: string[]
  // warnings are the actions' warnings at the check, when it holds.
  warnings?: string[]
}

export interface CardResult {
  action: number
  ok: boolean
  summary: string
  detail?: string
  ref?: string
  at: string
}

export type CardState = 'queued' | 'approved' | 'rejected' | 'expired' | 'replaced' | 'applied' | 'failed'

export type SourceKind = 'job' | 'alert' | 'report' | 'knowledge' | 'chat' | 'case'

export interface Proposal {
  id: string
  agent: string
  /** What started it; `job` names the job whose run made it (CF-92). */
  source: { kind: SourceKind; id?: string; label: string; setBy?: string; job?: string }
  cluster: string
  title: string
  reasoning?: string
  evidence?: CardEvidence[]
  actions: CardAction[]
  condition: { kind: 'none' | 'alert-firing' | 'resource-version'; args?: Record<string, string> }
  dedupKey?: string
  expires: string
  model?: string
  state: CardState
  created: string
  updated: string
  /** The person who asked in a chat: one of the people a change needs. */
  requester?: CardPerson
  production?: boolean
  needed: number
  approvals?: CardDecision[]
  rejection?: CardDecision
  recheck?: CardRecheck
  plans: CardPlan[]
  results?: CardResult[]
  reason?: string
  replacedBy?: string
  replaces?: string
  standing?: string
  watsonItem?: string
  /** What happened to it; `actor` says who with what they are (CF-35), `by` is the name as before. */
  history?: { at: string; what: string; by?: string; actor?: Actor }[]
  /** The cases ("C-12") and known issues ("KI-3") it resolves (CF-25). */
  resolves?: string[]
  /** Who linked each of them: "agent", or the person's name (CF-28). */
  resolvesBy?: Record<string, 'agent' | string>
  /** The title of each case and known issue it resolves, by id (CF-39). */
  resolvesTitles?: Record<string, string>
  /** Who proposed it: the agent, or the person in a chat (CF-35). */
  proposedBy?: Actor
  /** Who decided it: the approver who completed it, the rejecter, or the system (an automatic reject, expiry). */
  decidedBy?: Actor
  /** Who changed it last, and when. */
  lastBy?: Actor
  lastAt?: string
  clusterName: string
  agentName: string
  canApprove: boolean
  /** Why this person cannot approve it now. */
  cannot?: string
  mine?: boolean
  /**
   * The commands run from its drawer, and its own actions once approved
   * (CF-61): every one while it waits, the changes that finished done once
   * decided. Never their output.
   */
  commands?: CaseCommand[]
  /** This person asks Sherlock and runs commands in its drawer while it waits (CF-61). */
  mayAsk?: boolean
}

/** "Needs a second approval": two are needed and one was given (CF-62); "" otherwise. */
export function secondText(p: Pick<Proposal, 'state' | 'needed' | 'approvals'>): string {
  return p.state === 'queued' && p.needed === 2 && (p.approvals?.length ?? 0) === 1 ? 'Needs a second approval' : ''
}

export interface Proposals {
  proposals: Proposal[]
  /** How many this person may approve now. */
  waiting: number
  mayApprove: boolean
}

export interface ProposalQuery {
  state?: CardState
  cluster?: string
  agent?: string
  mine?: boolean
}

const base = '/api/v1/cases/'
const enc = encodeURIComponent

export function fetchProposals(q: ProposalQuery = {}, background = false): Promise<Proposals> {
  const p = new URLSearchParams()
  if (q.state) p.set('state', q.state)
  if (q.cluster) p.set('cluster', q.cluster)
  if (q.agent) p.set('agent', q.agent)
  if (q.mine) p.set('mine', '1')
  const s = p.toString()
  return call<Proposals>(base + 'proposals' + (s ? '?' + s : ''), {}, background)
}

export const fetchProposal = (id: string) => call<Proposal>(`${base}proposals/${enc(id)}`)

/**
 * A proposed change's id written the one way (CF-56): "c16", "C16",
 * "pc-16" and "PC-16" are all "PC-16", so an address of before, c<n>,
 * still opens the card. A case's "C-16" and anything else stay as they are.
 */
export function proposalId(id: string): string {
  const m = /^(?:pc-?|c)([1-9]\d{0,8})$/i.exec(id.trim())
  return m ? `PC-${m[1]}` : id
}

/**
 * A decision that came too late or does not hold (IB-08): the server's 409
 * with the card as it is now, so the drawer shows who decided it and when
 * without reading it again.
 */
export class ProposalConflict extends Error {
  constructor(
    message: string,
    readonly proposal: Proposal,
  ) {
    super(message)
    this.name = 'ProposalConflict'
  }
}

/** A 409 that carries the card becomes a ProposalConflict; anything else is thrown as it came. */
async function deciding(p: Promise<Proposal>): Promise<Proposal> {
  try {
    return await p
  } catch (e) {
    const card = e instanceof CallError && e.status === 409 ? (e.body?.proposal as Proposal | undefined) : undefined
    if (card && typeof card === 'object' && card.id) throw new ProposalConflict(conflictText(card, e instanceof Error ? e.message : ''), card)
    throw e
  }
}

export const approveProposal = (id: string, comment = '') => deciding(call<Proposal>(`${base}proposals/${enc(id)}/approve`, post({ comment })))

export const rejectProposal = (id: string, reason: string) => deciding(call<Proposal>(`${base}proposals/${enc(id)}/reject`, post({ reason })))

/**
 * Why a decision on a card came too late, in this browser's time: "Already
 * approved by Ann Lee at 2026-11-06 2:03pm PST". A card that still waits
 * keeps the server's words (it does not hold as it stands, or it is the
 * person's second approval).
 */
export function conflictText(p: Proposal, server = ''): string {
  const names = (p.approvals ?? []).map((d) => (d.standing ? 'a standing approval' : d.by.name))
  const last = p.approvals?.[p.approvals.length - 1]
  switch (p.state) {
    case 'approved':
    case 'applied':
    case 'failed':
      if (last) return `Already approved by ${[...new Set(names)].join(' and ')} at ${fmtTs(last.at)}${p.state === 'applied' ? '; it was applied' : p.state === 'failed' ? '; it failed' : ''}.`
      break
    case 'rejected':
      if (p.rejection) return `Already rejected by ${p.rejection.by.name} at ${fmtTs(p.rejection.at)}.`
      break
    case 'expired':
      return `It expired at ${fmtTs(p.updated)}.`
    case 'replaced':
      return `A newer proposal${p.replacedBy ? `, ${p.replacedBy},` : ''} replaced it at ${fmtTs(p.updated)}.`
  }
  return server ? localTimes(server) : 'It cannot be decided now.'
}

export const recheckProposal = (id: string) => call<Proposal>(`${base}proposals/${enc(id)}/recheck`, { method: 'POST' })

// ---------- standing approvals ----------

export interface Standing {
  id?: string
  name: string
  on: boolean
  agent?: string
  clusters: string[]
  /** A console action's kind, or the start of a kubectl or helm command. */
  actions: string[]
  namespaces?: string[]
  condition?: '' | 'alert-firing' | 'resource-version'
  perHour: number
  by?: CardPerson
  created?: string
  updated?: string
  applied?: string[]
}

export const fetchStanding = () => call<{ standing: Standing[] }>(base + 'standing').then((r) => r.standing)

export const saveStanding = (st: Standing) =>
  st.id ? call<Standing>(`${base}standing/${enc(st.id)}`, { method: 'PUT', body: JSON.stringify(st) }) : call<Standing>(base + 'standing', post(st))

export const deleteStanding = (id: string) => call<{ removed: boolean }>(`${base}standing/${enc(id)}`, { method: 'DELETE' })

// ---------- notifications ----------

export type ChannelKind = 'webhook' | 'slack' | 'teams'

export interface Channel {
  id: string
  kind: ChannelKind
  name: string
  on: boolean
  /** Whether an address is kept; the address itself never comes back. */
  urlSet: boolean
  lastAt?: string
  lastError?: string
}

export interface Notifications {
  link: string
  channels: Channel[]
  /** The addresses are kept in a Secret; false, in memory only. */
  inSecret: boolean
}

export interface ChannelInput {
  id?: string
  kind: ChannelKind
  name: string
  on: boolean
  /** A new address; empty keeps the one kept. */
  url?: string
  clearUrl?: boolean
}

export const fetchNotifications = () => call<Notifications>(base + 'notifications')

export const saveNotifications = (link: string, channels: ChannelInput[]) => call<Notifications>(base + 'notifications', { method: 'PUT', body: JSON.stringify({ link, channels }) })

export const testChannel = (id: string) => call<{ sent: boolean }>(`${base}notifications/${enc(id)}/test`, { method: 'POST' })

// ---------- what waits ----------

/** The kinds of action, in words. */
export const ACTION_KINDS: Record<string, string> = {
  kubectl: 'kubectl command',
  helm: 'helm command',
  'host.add': 'Add a host',
  'host.label': 'Label a host',
  'host.remove': 'Remove a host',
  'fleet.create': 'Create a fleet',
  'fleet.update': 'Change a fleet',
  'automation.params': 'Set Automation parameters',
  record: 'Change a console record',
}

/** Where a proposal came from, after the agent's name: "on an alert". */
export function sourceText(p: Pick<Proposal, 'source'>): string {
  switch (p.source.kind) {
    case 'alert':
      return 'on an alert'
    case 'job':
      return 'on its own'
    case 'report':
      return 'on a report'
    case 'knowledge':
      return 'from the knowledge sources'
    case 'chat':
      return p.source.setBy ? `in a chat with ${p.source.setBy}` : 'in a chat'
    case 'case':
      return 'for a case'
  }
  return ''
}

/** A card's status in words: "Waiting · 0 of 2". */
export function stateText(p: Pick<Proposal, 'state' | 'needed' | 'approvals'>): string {
  switch (p.state) {
    case 'queued':
      // A count only where it tells something: the second of two approvals.
      return p.needed > 1 && (p.approvals?.length ?? 0) > 0 ? 'Waiting · needs a second approval' : 'Waiting'
    case 'approved':
      return 'Approved, running'
    case 'applied':
      return 'Applied'
    case 'failed':
      return 'Failed'
    case 'rejected':
      return 'Rejected'
    case 'expired':
      return 'Expired'
    case 'replaced':
      return 'Replaced'
  }
}

export const stateTone = (s: CardState): 'warn' | 'ok' | 'err' | 'muted' | 'info' =>
  s === 'queued' ? 'warn' : s === 'applied' ? 'ok' : s === 'failed' || s === 'rejected' ? 'err' : s === 'approved' ? 'info' : 'muted'

/** "in 5 hours", "in 12 minutes", or "now" for what has passed. */
export function inWords(at: string, now = Date.now()): string {
  const ms = Date.parse(at) - now
  if (!(ms > 0)) return 'now'
  const min = Math.round(ms / 60_000)
  if (min < 60) return `in ${Math.max(min, 1)} ${min === 1 ? 'minute' : 'minutes'}`
  const h = Math.round(min / 60)
  return `in ${h} ${h === 1 ? 'hour' : 'hours'}`
}

/** Whether an action is a command Watson runs (kubectl, helm), not a change the console makes itself (CF-92). */
export const isWatsonCommand = (a: Pick<CardAction, 'kind'>) => a.kind === 'kubectl' || a.kind === 'helm'

/**
 * "Started by" in words, the same way for every kind of source (CF-92):
 * "Mycroft, from sandbox-knowledge “nodes.md” (run-38, Rebuild now by
 * Super Admin)", "Nightly audit, run-12 (Every day at 02:00, UTC; set up
 * by Sam)", "Case analysis of C-7: web crash-loops", or a chat's own
 * words. `link` is the case id in the text, which the drawer links;
 * `title` is what the source said as it came.
 */
export function startedBy(p: Pick<Proposal, 'source' | 'agentName'>): { text: string; link?: string; title: string } {
  const { kind, id = '', label = '', setBy = '', job = '' } = p.source
  const title = [label, setBy && kind !== 'chat' ? `set by ${setBy}` : ''].filter(Boolean).join(' · ')
  const agent = p.agentName || 'The agent'
  const paren = (parts: string[]) => {
    const x = parts.filter(Boolean).join('; ')
    return x ? ` (${x})` : ''
  }
  const setUp = setBy ? `set up by ${setBy}` : ''
  switch (kind) {
    case 'knowledge':
      // The drift check names itself; a page or document is where it came from.
      if (id === 'drift' && label) return { text: `${agent}, ${label.charAt(0).toLowerCase()}${label.slice(1)}`, title }
      return { text: `${agent}, from ${label || 'the knowledge sources'}${paren([setBy])}`, title }
    case 'job': {
      const name = job || label || `${agent}’s job`
      const run = id ? `, ${id}` : ''
      return { text: `${name}${run}${paren([job && label !== job ? label : '', setUp])}`, title }
    }
    case 'alert':
    case 'report':
      return { text: `${label || (kind === 'alert' ? 'An alert' : 'A report')}${paren([[job, id].filter(Boolean).join(', '), setUp])}`, title }
    case 'case': {
      const c = /^C-\d+$/i.test(id) ? id.toUpperCase() : ''
      if (!c) return { text: label || 'A case', title }
      // "Case C-7: web crash-loops" says the rest after the case's id.
      const m = new RegExp(`^case ${c}\\b`, 'i').exec(label)
      const rest = m ? label.slice(m[0].length) : label ? ` (${label})` : ''
      return { text: `Case analysis of ${c}${rest}`, link: c, title: [title, job].filter(Boolean).join(' · ') }
    }
    case 'chat':
      return { text: label || (setBy ? `A chat with ${setBy}` : 'A chat'), title }
  }
  return { text: label || kind, title }
}

/** A kubectl or helm action's command line, or "". */
export function commandOf(a: CardAction): string {
  return (a.kind === 'kubectl' || a.kind === 'helm') && typeof a.args.command === 'string' ? a.args.command : ''
}

/** What a row says a proposal does: its one command, or its title. */
export function whatOf(p: Pick<Proposal, 'actions' | 'title'>): { text: string; mono: boolean } {
  const only = p.actions.length === 1 ? commandOf(p.actions[0]!) : ''
  return only ? { text: only, mono: true } : { text: p.title, mono: false }
}

// --- Inbox (Phase 12, IB-04) ---

/** The case a card was made for, from its source ("C-12"), or null. */
export function caseOf(p: Pick<Proposal, 'source'>): { id: string; number: number; label: string } | null {
  if (p.source.kind !== 'case' || !p.source.id) return null
  const m = /^C-(\d+)$/i.exec(p.source.id)
  if (!m) return null
  return { id: p.source.id.toUpperCase(), number: Number(m[1]), label: p.source.label || p.source.id }
}

/**
 * The object an action acts on, as a resource the Object link reads:
 * Watson's targets ("deployment shop/web", "node worker-1") and a host
 * card's ("host backup-1"). Null for what is no object (a fleet, a record).
 */
export function targetResource(target: string): string | null {
  const m = /^([a-z][a-z0-9.-]*) (\S+)$/i.exec(target.trim())
  if (!m) return null
  const [, kind, name] = m as unknown as [string, string, string]
  if (kind === 'fleet' || kind === 'parameters') return null
  const slash = name.indexOf('/')
  return slash > 0 ? `${name.slice(0, slash)}/${kind}/${name.slice(slash + 1)}` : `${kind}/${name}`
}

// --- What a change resolves (Phase 12, wave 6b, CF-25, CF-28) ---

/** How long an applied card may still be linked to a case, in days. */
export const LINK_APPLIED_DAYS = 30

const LINKABLE = new Set<CardState>(['queued', 'approved', 'applied'])

/**
 * The cards a person may link to a case (CF-28): on the case's cluster,
 * waiting, approved, or applied in the last 30 days, not linked to it
 * yet, and matching what was typed by title or id; the newest first.
 */
export function linkable(list: Proposal[], o: { cluster?: string; linked?: string[]; q?: string; now?: number }): Proposal[] {
  const since = (o.now ?? Date.now()) - LINK_APPLIED_DAYS * 86_400_000
  const q = (o.q ?? '').trim().toLowerCase()
  return list
    .filter((p) => LINKABLE.has(p.state) && (p.state !== 'applied' || Date.parse(p.updated) >= since))
    .filter((p) => !o.cluster || p.cluster === o.cluster)
    .filter((p) => !(o.linked ?? []).includes(p.id))
    .filter((p) => !q || p.title.toLowerCase().includes(q) || p.id.toLowerCase().includes(q) || proposalId(q) === p.id)
    .sort((a, b) => Date.parse(b.updated) - Date.parse(a.updated))
}
