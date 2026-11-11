// AIOps › Inbox as one table (Phase 12, wave 5, IN-01 to IN-06): every
// case, proposed change, approval and draft as one row of one kind, with
// its priority, cluster, object, status, how long it has waited and who
// has it; the table's columns, and the filters on top (Pending | All,
// Kind, Source: People | Agents, Mine | Unassigned, Priority, Cluster).
// Since wave 6 findings are not rows: each becomes a case or joins one.
// Since wave 6c (CF-35 to CF-38) there is no Kind column or filter (a
// row's id and status say what it is); a case shows its type and incident
// number, every row who changed it last, and case rows can be picked for
// one action on many (InboxPicks.tsx, BulkBar.tsx). The rows come from
// their owners' APIs (useInbox.ts); nothing here reads one. Since CF-87 a
// validation rule the housekeeping suggests is a SuperAdmin's row.
import { actorName, actorText, lastChangeText, type Actor } from '../../api/actor'
import type { ApprovalRow } from '../../api/automation'
import { caseOf, sourceText, stateText, stateTone, type Proposal } from '../../api/cards'
import { caseShownStatus, caseStatusText, caseStatusTone, caseTypeText, VERIFICATION, type CaseBrief, type ShownStatus, type Verification } from '../../api/casework'
import type { OpKnown } from '../../api/issues'
import type { ConfluenceConflict, DraftRun, PageRequest } from '../../api/knowledge'
import type { Suggestion } from '../../api/rules'
import { INBOX_KINDS, inboxOrderKey, type InboxKind, type Who } from '../../lib/cases'
import { fmtMinutes, fmtTs, minutesSince } from '../../lib/format'
import { adminHref } from '../../lib/router'
import type { ColumnDef, Term } from '../../lib/table'
import { HeadPickBox, RowPickBox, RowPickSpace } from './InboxPicks'
import { ObjectLink } from './ObjectLink'
import { isPriority, KNOWN_STATUS, PRIORITY, priorityTone, who } from './parts'
import { objectsOf, Resolves, resolvesText } from './ProposalLinks'

/** A case as the Inbox reads it: the Cases package's brief (pending, assignee, waitingSince, late). */
export type InboxCase = CaseBrief

/** Who a row comes from, for the Source filter: people (a report, by hand, an alert, a request) or the agents. */
export type Origin = 'people' | 'agents'

/** One row of the Inbox: what it is, where its drawer opens, and what the columns show. */
export interface InboxRow {
  /** Unique in the table: "case:12". */
  key: string
  kind: InboxKind
  /** Its drawer's address under #/aiops/inbox: ["case", "12"]. */
  open: [string, string]
  /** What it is called: C-12, a card's id, KI-4. */
  ref: string
  title: string
  /** 0 (P0) to 4 (P4), where the kind has one. */
  priority?: number
  cluster?: string
  clusterName?: string
  /** The object it is about, as ObjectLink reads it. */
  resource?: string
  status: string
  tone: string
  /** Waits for a person: a case New or In progress, a card or approval not decided, a draft not reviewed. */
  pending: boolean
  /** Waits for this person to decide: an approval they did not ask for, a card they may approve. */
  forMe?: boolean
  /** Since when it has waited; once done, when it was done. */
  since: string
  /** Whether the kind is assigned to someone: cases and known-issue drafts. */
  assignable: boolean
  assignee?: string
  /** The person's own: assigned to them, asked for by them, or from a job whose trigger they set. */
  mine: boolean
  /** The SLA target that passed, in words (IN-05). */
  late?: string
  /** More words a search finds it by: its source, agent, check; a fixed case's RCA cause, how past RCAs are found (wave 6e). */
  words?: string[]
  origin: Origin
  /** What a proposed change resolves: "C-12", "KI-3" (CF-25). */
  resolves?: string[]
  /** A case's check of the fix a change applied (CF-31). */
  verification?: Verification
  /** The titles of what it resolves, by id (CF-39). */
  resolvesTitles?: Record<string, string>
  /** A case's status as shown, for the Status filter: waiting for the reporter is one (CF-67). */
  shownStatus?: ShownStatus
  /** A case's type (CF-37) and incident number (CF-36). */
  type?: string
  incident?: string
  /** A case waits in the case analysis job's queue (CF-32). */
  analysisQueued?: boolean
  /** Who changed it last, and when (CF-35). */
  lastBy?: Actor | string
  lastAt?: string
}

/** How the person is named: by the cases and jobs (a label), and by the issue service (an id). */
export interface RowContext {
  me: string
  person: string
  names: Map<string, string>
}

const LABEL = Object.fromEntries(INBOX_KINDS.map((k) => [k.id, k.label])) as Record<InboxKind, string>

/** A kind as a row says it: "Proposed change". */
export const kindLabel = (k: InboxKind) => LABEL[k]

// ---------- the rows, kind by kind ----------

/** A case (IN-02): pending while New or In progress; late only where the SLA applies. */
export function caseRow(c: InboxCase, ctx: RowContext): InboxRow {
  const pending = c.pending ?? c.status !== 'closed'
  const late = !c.slaApplies ? undefined : c.late?.response ? 'Its response target passed' : c.late?.resolve ? 'Its resolution target passed' : undefined
  return {
    key: `case:${c.number}`,
    kind: 'case',
    open: ['case', String(c.number)],
    ref: c.id,
    title: c.title,
    priority: c.priority,
    cluster: c.cluster || undefined,
    // The server names the cluster (CF-60); the list's names before it did.
    clusterName: c.cluster ? c.clusterName || (ctx.names.get(c.cluster) ?? c.cluster) : undefined,
    resource: c.resource || undefined,
    status: caseStatusText(c) + (c.seen && c.seen.count > 1 ? ` · seen ×${c.seen.count}` : ''),
    tone: caseStatusTone(caseShownStatus(c), c.outcome),
    shownStatus: caseShownStatus(c),
    pending,
    since: pending ? (c.waitingSince ?? c.since ?? c.created) : (c.closedAt ?? c.updated),
    assignable: true,
    assignee: c.assignee ? c.assignee.name || c.assignee.id : undefined,
    // Mine: assigned to the person, or opened by them while nobody has it.
    mine: c.assignee ? c.assignee.id === ctx.person : !!c.createdBy && actorName(c.createdBy) === ctx.me,
    late,
    words: [
      c.agentFound ? 'agent' : c.source.kind,
      actorName(c.createdBy),
      c.key?.check ?? '',
      c.source.job ?? '',
      c.verification ? VERIFICATION[c.verification].tag : '',
      c.analysisQueued ? 'analysis queued' : '',
      c.rcaCause ?? '',
      c.rcaId ?? '',
    ],
    origin: c.agentFound ? 'agents' : 'people',
    verification: c.verification,
    type: c.type || undefined,
    incident: c.incident || undefined,
    analysisQueued: !!c.analysisQueued,
    lastBy: c.lastBy,
    lastAt: c.lastAt ?? c.updated,
  }
}

/** A proposed change: pending while it waits for approval. */
export function proposalRow(p: Proposal): InboxRow {
  const pending = p.state === 'queued'
  return {
    key: `proposal:${p.id}`,
    kind: 'proposal',
    open: ['proposal', p.id],
    ref: p.id,
    title: p.title,
    cluster: p.cluster || undefined,
    clusterName: p.clusterName || p.cluster || undefined,
    resource: objectsOf(p)[0],
    status: stateText(p),
    tone: stateTone(p.state),
    pending,
    forMe: pending && p.canApprove,
    since: pending ? p.created : p.updated,
    assignable: false,
    mine: !!p.mine,
    words: [p.agentName, sourceText(p), caseOf(p)?.id ?? '', ...(p.resolves ?? []), ...Object.values(p.resolvesTitles ?? {})],
    origin: 'agents',
    resolves: p.resolves?.length ? p.resolves : undefined,
    resolvesTitles: p.resolvesTitles,
    lastBy: p.lastBy ?? p.decidedBy ?? p.proposedBy,
    lastAt: p.lastAt ?? p.updated,
  }
}

export const APPROVAL_KIND = { script: 'Script version', change: 'Variable change', command: 'Watson command', admin: 'Console change' } as const

export const APPROVAL_STATE = {
  pending: { text: 'Pending', tone: 'warn' },
  approved: { text: 'Approved', tone: 'ok' },
  rejected: { text: 'Rejected', tone: 'err' },
  revoked: { text: 'Revoked', tone: 'muted' },
} as const

/** A short, stable name for a text: an approval without an id of its own is found again by it. */
function hash(text: string): string {
  let h = 5381
  for (let i = 0; i < text.length; i++) h = ((h << 5) + h + text.charCodeAt(i)) | 0
  return (h >>> 0).toString(36)
}

/** An approval's id in the Inbox's address: its own, a script version's, or one made of what it is. */
export function approvalId(r: ApprovalRow): string {
  if (r.id) return r.id
  if (r.kind === 'script') return `script-${r.script ?? ''}-v${r.version ?? 0}`
  return `${r.kind}-${hash(`${r.cluster ?? ''}|${r.fleet ?? ''}|${r.what}|${r.requestedAt ?? ''}`)}`
}

/** An approval: pending until decided; for this person when they did not ask for it. */
export function approvalRow(r: ApprovalRow): InboxRow {
  const id = approvalId(r)
  const pending = r.state === 'pending'
  return {
    key: `approval:${id}`,
    kind: 'approval',
    open: ['approval', id],
    ref: APPROVAL_KIND[r.kind],
    title: r.what,
    cluster: r.kind === 'script' ? undefined : r.cluster || undefined,
    clusterName: r.kind === 'script' ? undefined : r.clusterName || r.cluster || undefined,
    status: APPROVAL_STATE[r.state].text,
    tone: APPROVAL_STATE[r.state].tone,
    pending,
    forMe: pending && !r.own,
    since: pending ? (r.requestedAt ?? '') : (r.approvedAt ?? r.requestedAt ?? ''),
    assignable: false,
    mine: !!(r.mine || r.own),
    words: [APPROVAL_KIND[r.kind], r.requestedBy, r.approvedBy ?? ''],
    origin: 'people',
    lastBy: pending ? r.requestedBy : (r.approvedBy ?? r.requestedBy),
    lastAt: pending ? r.requestedAt : (r.approvedAt ?? r.requestedAt),
  }
}

/** A known issue pending: an agent's proposal until an operator accepts it, or one back for its owner because the agents still see it. */
export function knownRow(k: OpKnown, ctx: RowContext): InboxRow {
  return {
    key: `known:${k.id}`,
    kind: 'known',
    open: ['known', k.id],
    ref: k.id,
    title: k.title,
    status: k.draft ? 'Draft' : `${KNOWN_STATUS[k.status]}: still seen`,
    tone: k.draft ? 'info' : 'err',
    pending: true,
    since: k.draft ? k.created : (k.stillPresent?.last ?? k.updated),
    assignable: true,
    assignee: who(k.owner) || undefined,
    mine: !!k.owner && k.owner.id === ctx.person,
    words: [k.source === 'agent' ? 'agent' : k.source],
    origin: k.source === 'agent' ? 'agents' : 'people',
  }
}

const RUN_STATE: Record<DraftRun['state'], [string, string]> = { waiting: ['Waiting for review', 'warn'], partial: ['Partly reviewed', 'warn'], done: ['Reviewed', 'ok'], rejected: ['Rejected', 'muted'] }

/** A run of Mycroft's wiki changes: pending until every page is decided. */
export function draftRunRow(r: DraftRun): InboxRow {
  const pending = r.state === 'waiting' || r.state === 'partial'
  const first = r.pages[0]?.title || r.pages[0]?.path || 'wiki changes'
  const [status, tone] = RUN_STATE[r.state] ?? RUN_STATE.waiting
  return {
    key: `knowledge:${r.id}`,
    kind: 'knowledge',
    open: ['knowledge', r.id],
    ref: r.id,
    title: r.pages.length > 1 ? `${first} and ${r.pages.length - 1} more ${r.pages.length === 2 ? 'page' : 'pages'}` : first,
    status,
    tone,
    pending,
    since: pending ? r.started : (r.decided ?? r.started),
    assignable: false,
    mine: !!r.mine,
    words: [r.store, r.by, ...r.pages.map((p) => p.path)],
    origin: 'agents',
  }
}

const REQUEST_STATE: Record<PageRequest['state'], [string, string]> = { waiting: ['Waiting for approval', 'warn'], approved: ['Approved', 'ok'], rejected: ['Rejected', 'muted'] }

/**
 * A request to retire a wiki page or make it active again (KN-24):
 * pending until one person other than who asked approves it, or anyone
 * rejects it; for that person (the gear's count), not for who asked.
 */
export function pageRequestRow(r: PageRequest): InboxRow {
  const pending = r.state === 'waiting'
  const [status, tone] = pending ? [r.action === 'activate' ? 'Make active again: waits for approval' : 'Retire: waits for approval', 'warn'] : (REQUEST_STATE[r.state] ?? REQUEST_STATE.waiting)
  return {
    key: `knowledge:${r.id}`,
    kind: 'knowledge',
    open: ['knowledge', r.id],
    ref: r.id,
    title: `${r.action === 'activate' ? 'Make active again' : 'Retire'}: ${r.title}`,
    status,
    tone,
    pending,
    forMe: pending && r.canApprove !== false && !r.mine,
    since: pending ? r.at : (r.decided ?? r.at),
    assignable: false,
    mine: !!r.mine,
    words: [r.path, r.name, r.reason, r.action === 'activate' ? 'make active again' : 'retire'],
    origin: 'people',
    lastBy: pending ? r.name : (r.decidedName ?? r.name),
    lastAt: pending ? r.at : (r.decided ?? r.at),
  }
}

/** A Confluence page whose managed part someone edited: pending until kept or replaced. */
export function conflictRow(c: ConfluenceConflict): InboxRow {
  return {
    key: `knowledge:${c.id}`,
    kind: 'knowledge',
    open: ['knowledge', c.id],
    ref: c.issue ?? c.id,
    title: `Edited in Confluence: ${c.title}`,
    status: 'Edited in Confluence',
    tone: 'warn',
    pending: true,
    since: c.at,
    assignable: true,
    assignee: c.ownerName || c.owner || undefined,
    mine: !!c.mine,
    words: ['confluence', c.kind],
    origin: 'people',
  }
}

/** A validation rule the housekeeping suggests (CF-87): pending until a SuperAdmin promotes or dismisses it. */
export function suggestionRow(s: Suggestion): InboxRow {
  return {
    key: `rule:${s.id}`,
    kind: 'rule',
    open: ['rule', s.id],
    ref: s.id,
    title: s.says || s.id,
    status: 'Suggested rule',
    tone: 'info',
    pending: true,
    since: s.created,
    assignable: false,
    mine: false,
    words: ['validation rule', 'suggested', ...s.from, s.why ?? ''],
    origin: 'agents',
    lastBy: 'Housekeeping',
    lastAt: s.created,
  }
}

// ---------- the filters (IN-04) ----------

export type Show = 'pending' | 'all'

export interface InboxFilter {
  show: Show
  who: Who
  /** 0 to 4, or null for any: P0 is 0. */
  priority: number | null
  cluster: string
  /** People's or the agents', or null for both. */
  source: Origin | null
  /** Cases of one type, or null for every row (CF-37). */
  type: string | null
  /** Cases in one status as shown, or null for every row (CF-67). */
  status?: ShownStatus | null
}

/** Whose a row is under Mine and Unassigned: the person's own; nobody's yet, or, for a kind with nobody to assign, what waits. */
export function whoseMatch(r: InboxRow, w: Who): boolean {
  if (w === 'mine') return r.mine
  if (w === 'unassigned') return r.assignable ? !r.assignee : r.pending
  return true
}

/** The rows the filters keep (IN-04); the search is the table's. */
export function filterRows(rows: InboxRow[], f: InboxFilter): InboxRow[] {
  return rows.filter(
    (r) =>
      (f.show === 'all' || r.pending) &&
      whoseMatch(r, f.who) &&
      (f.priority === null || r.priority === f.priority) &&
      (!f.cluster || r.cluster === f.cluster) &&
      (!f.source || r.origin === f.source) &&
      (!f.type || (r.kind === 'case' && r.type === f.type)) &&
      (!f.status || (r.kind === 'case' && r.shownStatus === f.status)),
  )
}

/** How many rows of each kind are pending, under Mine or Unassigned when one is on: the Pending count and the top bar's. */
export function pendingByKind(rows: InboxRow[], w: Who = 'all'): Record<InboxKind, number> {
  const out = Object.fromEntries(INBOX_KINDS.map((k) => [k.id, 0])) as Record<InboxKind, number>
  for (const r of rows) if (r.pending && whoseMatch(r, w)) out[r.kind]++
  return out
}

// ---------- the columns (IN-01, IN-03) ----------

/** What the table's search matches. */
export function rowTerms(r: InboxRow): Term[] {
  const t: Term[] = [
    { kind: 'kind', text: kindLabel(r.kind) },
    { kind: 'name', text: r.ref },
    { kind: 'detail', text: r.title },
    { kind: 'status', text: r.status },
  ]
  if (isPriority(r.priority)) t.push({ kind: 'detail', text: `P${r.priority}` })
  if (r.clusterName) t.push({ kind: 'name', text: r.clusterName })
  if (r.resource) t.push({ kind: 'name', text: r.resource })
  if (r.assignee) t.push({ kind: 'owner', text: r.assignee })
  if (r.type) t.push({ kind: 'detail', text: caseTypeText(r.type) })
  if (r.incident) t.push({ kind: 'name', text: r.incident })
  if (r.lastBy) t.push({ kind: 'owner', text: actorName(r.lastBy) })
  for (const w of r.words ?? []) if (w) t.push({ kind: 'detail', text: w })
  return t
}

/** The row's accessible name: what it is is said here, since no column says it. */
export function rowLabel(r: InboxRow, open: boolean): string {
  return `${kindLabel(r.kind)} ${r.ref}: ${r.title}, ${r.status}${isPriority(r.priority) ? `, P${r.priority}` : ''}${r.type ? `, ${caseTypeText(r.type)}` : ''}${r.incident ? `, incident ${r.incident}` : ''}${r.pending ? ', pending' : ''}${open ? ', open' : ''}`
}

const dash = (
  <span style={{ color: 'var(--fg3)' }} aria-label="none">
    —
  </span>
)

const clip: React.CSSProperties = { overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }

/** How long a row has waited: "3h 12m"; once done, how long ago. */
function waitedText(r: InboxRow, now: number): string {
  if (!r.since) return ''
  const t = fmtMinutes(minutesSince(r.since, now))
  return r.pending ? t : `${t} ago`
}

export const INBOX_COLUMNS: ColumnDef<InboxRow>[] = [
  {
    // The first column, always shown: a case row's box, to pick it for the bar's one action on many (CF-38).
    id: 'priority',
    label: 'Priority',
    width: '108px',
    min: 108,
    hint: 'P0 outage, P1 blocking, P2 service degradation, P3 preemptive, P4 question.',
    sortValue: (r) => r.priority ?? 6,
    head: () => <HeadPickBox />,
    cell: (r) => (
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
        {r.kind === 'case' ? <RowPickBox id={r.ref} /> : <RowPickSpace />}
        {isPriority(r.priority) ? (
          <span className="rk-tag" data-tone={priorityTone(r.priority)} title={PRIORITY[r.priority]} data-testid="inbox-row-priority">
            P{r.priority}
          </span>
        ) : (
          dash
        )}
      </span>
    ),
    export: (r) => [{ head: 'Priority', cell: isPriority(r.priority) ? `P${r.priority}` : null }],
  },
  {
    id: 'title',
    label: 'Title',
    width: 'minmax(240px, 3fr)',
    min: 240,
    sortValue: (r) => r.title.toLowerCase(),
    cell: (r) => (
      <span style={{ ...clip, display: 'inline-flex', gap: 8, alignItems: 'baseline' }} title={r.title}>
        <span className="rk-mono" style={{ fontSize: 11.5, color: 'var(--fg3)', flex: 'none' }}>
          {r.ref}
        </span>
        <span style={clip}>{r.title}</span>
        {r.resolves && (
          <span style={{ flex: 'none', fontSize: 11.5, color: 'var(--fg2)' }}>
            <Resolves ids={r.resolves} titles={r.resolvesTitles} testId="inbox-row-resolves" />
          </span>
        )}
      </span>
    ),
    export: (r) => [
      { head: 'Id', cell: r.ref },
      { head: 'Title', cell: r.resolves ? `${r.title} (resolves ${resolvesText(r.resolves, r.resolvesTitles)})` : r.title },
    ],
  },
  {
    // --- Case type (Phase 12, wave 6c, CF-37) ---
    id: 'type',
    label: 'Type',
    width: 'minmax(104px, 0.8fr)',
    min: 104,
    hint: 'What kind of problem a case is.',
    sortValue: (r) => (r.type ? caseTypeText(r.type) : null),
    cell: (r) =>
      r.type ? (
        <span style={clip} data-testid="inbox-row-type" data-type={r.type}>
          {caseTypeText(r.type)}
        </span>
      ) : (
        dash
      ),
    export: (r) => [{ head: 'Type', cell: r.type ? caseTypeText(r.type) : null }],
  },
  {
    // --- Incident number (Phase 12, wave 6c, CF-36) ---
    id: 'incident',
    label: 'Incident',
    width: 'minmax(110px, 0.9fr)',
    min: 110,
    hint: 'The incident number elsewhere, as a case was given it.',
    sortValue: (r) => r.incident ?? null,
    cell: (r) =>
      r.incident ? (
        <span className="rk-mono" style={{ ...clip, fontSize: 12 }} title={r.incident} data-testid="inbox-row-incident">
          {r.incident}
        </span>
      ) : (
        dash
      ),
    export: (r) => [{ head: 'Incident', cell: r.incident ?? null }],
  },
  {
    id: 'cluster',
    label: 'Cluster',
    width: 'minmax(100px, 1fr)',
    min: 100,
    sortValue: (r) => r.clusterName ?? '',
    cell: (r) => (r.clusterName ? <span style={clip}>{r.clusterName}</span> : dash),
    export: (r) => [{ head: 'Cluster', cell: r.clusterName ?? null }],
  },
  {
    id: 'object',
    label: 'Object',
    hint: 'The object it is about, linked to its page, with how long it has been running.',
    width: 'minmax(180px, 2fr)',
    min: 180,
    sortValue: (r) => r.resource ?? '',
    cell: (r) =>
      r.resource ? (
        <span className="cs-obj-clip" title={r.resource} data-testid="inbox-row-object">
          <ObjectLink cluster={r.cluster ?? ''} resource={r.resource} />
        </span>
      ) : (
        dash
      ),
    export: (r) => [{ head: 'Object', cell: r.resource ?? null }],
  },
  {
    id: 'status',
    label: 'Status',
    width: 'minmax(150px, 1.3fr)',
    min: 150,
    sortValue: (r) => r.status,
    cell: (r) => (
      <span style={{ ...clip, display: 'inline-flex', gap: 4, alignItems: 'center' }}>
        <span className="rk-tag" data-tone={r.tone} data-testid="inbox-row-status">
          {r.status}
        </span>
        {r.analysisQueued && (
          // Waits for the case analysis job, highest priority first (CF-32).
          <span className="rk-tag" data-tone="info" title="The case analysis job analyzes it when its turn comes; Analyze in the case runs it now" data-testid="inbox-row-analysis-queued">
            Analysis queued
          </span>
        )}
        {r.verification && VERIFICATION[r.verification] && (
          // A fix a change applied: waiting for its check, verified, or not held (CF-31).
          <span className="rk-tag" data-tone={VERIFICATION[r.verification].tone} title={VERIFICATION[r.verification].hint} data-testid="inbox-row-verification">
            {VERIFICATION[r.verification].tag}
          </span>
        )}
        {r.late && (
          // Which target passed, and where the targets are set (IN-05).
          <a className="rk-tag" data-tone="err" href={adminHref('settings', ['sla'])} title={`${r.late}: the targets are in Settings › SLA`} data-testid="inbox-row-late">
            late
          </a>
        )}
      </span>
    ),
    export: (r) => [{ head: 'Status', cell: [r.status, r.analysisQueued ? '(analysis queued)' : '', r.verification ? `(${VERIFICATION[r.verification]?.tag ?? r.verification})` : '', r.late ? '(late)' : ''].filter(Boolean).join(' ') }],
  },
  {
    id: 'waiting',
    label: 'Waiting',
    sortName: 'the Inbox’s order',
    hint: 'Sorted by default: what is pending first, then by priority, then the longest waiting; then what was done, newest first.',
    width: '104px',
    min: 104,
    right: true,
    sortValue: (r) => inboxOrderKey({ waiting: r.pending, priority: r.priority, since: r.since }),
    cell: (r, now) => (
      <span className="cs-open" title={r.since ? `${r.pending ? 'Waiting since' : 'Done'} ${fmtTs(r.since)}` : undefined} style={r.pending ? undefined : { opacity: 0.8 }} data-testid="inbox-row-waiting">
        {waitedText(r, now)}
      </span>
    ),
    export: (r) => [{ head: r.pending ? 'Waiting since' : 'Since', cell: r.since || null }],
  },
  {
    id: 'assignee',
    label: 'Assignee',
    width: 'minmax(110px, 1fr)',
    min: 110,
    sortValue: (r) => (r.assignable ? (r.assignee ?? '') : null),
    cell: (r) => (!r.assignable ? dash : r.assignee ? <span style={clip}>{r.assignee}</span> : <span style={{ color: 'var(--fg3)' }}>unassigned</span>),
    export: (r) => [{ head: 'Assignee', cell: r.assignable ? (r.assignee ?? '') : null }],
  },
  {
    // --- Who did it (Phase 12, wave 6c, CF-35) ---
    id: 'last',
    label: 'Last change',
    width: 'minmax(150px, 1.1fr)',
    min: 150,
    hint: 'Who changed it last, a person, an agent or the system, and how long ago.',
    defaultDir: 'desc',
    sortValue: (r) => (r.lastAt ? Date.parse(r.lastAt) : null),
    cell: (r, now) => {
      const text = lastChangeText(r.lastBy, r.lastAt, now)
      return text ? (
        <span style={clip} title={r.lastAt ? fmtTs(r.lastAt) : undefined} data-testid="inbox-row-last">
          {text}
        </span>
      ) : (
        dash
      )
    },
    export: (r) => [
      { head: 'Last change by', cell: actorText(r.lastBy) || null },
      { head: 'Last change', cell: r.lastAt ?? null },
    ],
  },
]
