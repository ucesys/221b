// What the Inbox lists (Phase 12, wave 5, IN-01, IN-02, IN-06): every
// source read once for the page, shared by the table, the top bar's counts
// and its bell (CF-80), and read again every half minute while any of them
// shows. Each source answers only for the people who may see it; a source
// the person may not see, or that is off, lists nothing, quietly. Since
// CF-87 the validation rules the housekeeping suggests are rows of a
// SuperAdmin's, who decides them; the ids found outside the list are not.
import { useEffect, useMemo, useRef } from 'react'
import { fetchApprovals, type Approvals } from '../../api/automation'
import { CallError } from '../../api/call'
import { fetchProposals, type Proposals } from '../../api/cards'
import { fetchCases, type CasesView } from '../../api/casework'
import { fetchConflicts, fetchDrafts, fetchPageRequests, type ConfluenceConflict, type DraftRun, type PageRequest } from '../../api/knowledge'
import { fetchSuggestions, type Suggestion } from '../../api/rules'
import { INBOX_KINDS, knownPending, useIssueMe, useShared, type CasesAccess, type InboxKind } from '../../lib/cases'
import { useAllKnown } from './count'
import { approvalRow, caseRow, conflictRow, draftRunRow, knownRow, pageRequestRow, pendingByKind, proposalRow, suggestionRow, type InboxCase, type InboxRow, type RowContext } from './inboxRows'

/**
 * Who sees which kind (IN-02): cases to anyone with a case to work or
 * follow (approvers, the console-wide roles, operators and reporters),
 * proposed changes to approvers and the console-wide roles, approvals to
 * approvers, known issues pending (drafts, and those back for their
 * owner) to operators, knowledge drafts to the console-wide roles,
 * suggested validation rules to a SuperAdmin, who may promote them
 * (CF-87). The server decides again.
 */
export function kindsFor(a: CasesAccess): InboxKind[] {
  return INBOX_KINDS.map((k) => k.id).filter((k) => {
    switch (k) {
      case 'case':
        return a.approver || a.console || a.operator || a.reporter
      case 'proposal':
        return a.approver || a.console
      case 'approval':
        return a.approver
      case 'known':
        return a.operator
      case 'knowledge':
        return a.console
      case 'rule':
        return a.approver
    }
  })
}

/** A source the person may not see, or that is off, answers 4xx (or 503 when off): nothing to list. */
function quiet<T>(empty: T): (e: unknown) => T {
  return (e) => {
    if (e instanceof CallError && e.status >= 400 && e.status < 500) return empty
    if (e instanceof CallError && e.status === 503) return empty
    throw e
  }
}

export const INBOX_CASES = 'cases.inbox.cases'
const loadCases = (background: boolean) => fetchCases({}, background).catch(quiet<CasesView>({ cases: [], clusters: [], mayChange: false, me: '' }))
export const INBOX_PROPOSALS = 'cases.inbox.proposals'
const loadProposals = (background: boolean) => fetchProposals({}, background).catch(quiet<Proposals>({ proposals: [], waiting: 0, mayApprove: false }))
export const INBOX_APPROVALS = 'cases.approvals'
const loadApprovals = (background: boolean) => fetchApprovals(background).catch(quiet<Approvals>({ items: [] }))

/** The wiki's rows: Mycroft's runs, Confluence edits, and requests to retire a page or make it active again (KN-24). */
type Knowledge = { runs: DraftRun[]; conflicts: ConfluenceConflict[]; requests: PageRequest[] }
const loadKnowledge = (all: boolean) => () =>
  Promise.all([
    fetchDrafts(all).then((r) => r.runs, quiet<DraftRun[]>([])),
    fetchConflicts().then((r) => r.conflicts, quiet<ConfluenceConflict[]>([])),
    fetchPageRequests(all).then((r) => r.requests ?? [], quiet<PageRequest[]>([])),
  ]).then(([runs, conflicts, requests]): Knowledge => ({ runs, conflicts, requests }))
export const INBOX_RULES = 'cases.inbox.rules'
const loadSuggestions = (background: boolean) => fetchSuggestions(background).catch(quiet<{ suggestions: Suggestion[] }>({ suggestions: [] }))
export const INBOX_KNOWLEDGE_OPEN = 'cases.inbox.knowledge.open'
const loadOpenKnowledge = loadKnowledge(false)
export const INBOX_KNOWLEDGE_ALL = 'cases.inbox.knowledge.all'
const loadAllKnowledge = loadKnowledge(true)

export interface Inbox {
  /** Every row the person may see: what is pending, and under All what was done too. */
  rows: InboxRow[]
  /** Whether every source the person may see has answered once. */
  loaded: boolean
  /** What a source could not read, by kind. */
  errors: string[]
  /** The cases' view: the clusters, and whether the person works the cases. */
  cases: CasesView | null
  proposals: Proposals | null
  approvals: Approvals | null
  knowledge: Knowledge | null
  /** The suggested validation rules, for a SuperAdmin (CF-87). */
  suggestions: Suggestion[] | null
  /** Reads a kind again at once, after a change; every kind without one. */
  reload: (kind?: InboxKind) => void
}

/**
 * The Inbox's rows (IN-02), for this person: `all` adds what was closed,
 * completed or decided to what is pending. `fresh` reads every list again
 * as the page opens, rather than showing what the top bar last read.
 */
export function useInbox(access: CasesAccess, all = false, fresh = false): Inbox {
  const kinds = kindsFor(access)
  const on = (k: InboxKind) => kinds.includes(k)
  const im = useIssueMe()
  const cases = useShared<CasesView>(on('case') ? INBOX_CASES : null, loadCases)
  const proposals = useShared<Proposals>(on('proposal') ? INBOX_PROPOSALS : null, loadProposals)
  const approvals = useShared<Approvals>(on('approval') ? INBOX_APPROVALS : null, loadApprovals)
  const known = useAllKnown(on('known'))
  const rules = useShared<{ suggestions: Suggestion[] }>(on('rule') ? INBOX_RULES : null, loadSuggestions)
  const knowledgeOpen = useShared<Knowledge>(on('knowledge') && !all ? INBOX_KNOWLEDGE_OPEN : null, loadOpenKnowledge)
  const knowledgeAll = useShared<Knowledge>(on('knowledge') && all ? INBOX_KNOWLEDGE_ALL : null, loadAllKnowledge)
  const knowledge = all ? knowledgeAll : knowledgeOpen

  const cv = cases.value
  const rows = useMemo(() => {
    const names = new Map((cv?.clusters ?? []).map((c) => [c.id, c.name || c.id]))
    for (const p of proposals.value?.proposals ?? []) if (p.cluster && p.clusterName && !names.has(p.cluster)) names.set(p.cluster, p.clusterName)
    const ctx: RowContext = { me: cv?.me ?? '', person: im?.person.id ?? '', names }
    const out: InboxRow[] = []
    for (const c of (cv?.cases ?? []) as InboxCase[]) out.push(caseRow(c, ctx))
    for (const p of proposals.value?.proposals ?? []) out.push(proposalRow(p))
    for (const a of approvals.value?.items ?? []) out.push(approvalRow(a))
    for (const k of knownPending(known.value ?? [], ctx.person)) out.push(knownRow(k, ctx))
    for (const r of knowledge.value?.runs ?? []) out.push(draftRunRow(r))
    for (const c of knowledge.value?.conflicts ?? []) out.push(conflictRow(c))
    for (const r of knowledge.value?.requests ?? []) out.push(pageRequestRow(r))
    for (const s of rules.value?.suggestions ?? []) out.push(suggestionRow(s))
    // Each row once: a source read again may overlap another's last answer.
    const seen = new Set<string>()
    return out.filter((r) => !seen.has(r.key) && !!seen.add(r.key))
  }, [cv, proposals.value, approvals.value, known.value, knowledge.value, rules.value, im])

  const parts: [InboxKind, boolean, { value: unknown; error: string }][] = [
    ['case', on('case'), cases],
    ['proposal', on('proposal'), proposals],
    ['approval', on('approval'), approvals],
    ['known', on('known'), known],
    ['knowledge', on('knowledge'), knowledge],
    ['rule', on('rule'), rules],
  ]
  const live = parts.filter((p) => p[1])
  const loaded = live.every((p) => p[2].value !== null || p[2].error !== '')
  const errors = live.filter((p) => p[2].error).map((p) => `${INBOX_KINDS.find((k) => k.id === p[0])?.plural}: ${p[2].error}`)
  const reload = (kind?: InboxKind) => {
    if (!kind || kind === 'case') {
      cases.reload()
    }
    if (!kind || kind === 'proposal') proposals.reload()
    if (!kind || kind === 'approval') approvals.reload()
    if (!kind || kind === 'known') known.reload()
    if (!kind || kind === 'knowledge') knowledge.reload()
    if (!kind || kind === 'rule') rules.reload()
  }
  // The lists the top bar keeps are read every half minute: the page reads
  // them again as it opens, so what was just done elsewhere is there.
  const reloadRef = useRef(reload)
  useEffect(() => {
    reloadRef.current = reload
  })
  useEffect(() => {
    if (fresh) reloadRef.current()
  }, [fresh])
  return {
    rows,
    loaded,
    errors,
    cases: cv,
    proposals: proposals.value,
    approvals: approvals.value,
    knowledge: knowledge.value,
    suggestions: rules.value?.suggestions ?? null,
    reload,
  }
}

export interface InboxCounts {
  /** Every pending row: the area's and the Inbox tab's count, and what the bell stands for (CF-80). */
  total: number
  byKind: Record<InboxKind, number>
  /** The pending cases at P0 and at P1: the bell is red while any waits (CF-80). */
  p0: number
  p1: number
}

/** What is pending for this person, counted from the Inbox's rows (IN-07, CF-80). */
export function inboxCounts(rows: InboxRow[]): InboxCounts {
  const byKind = pendingByKind(rows)
  const total = Object.values(byKind).reduce((n, v) => n + v, 0)
  const at = (p: number) => rows.filter((r) => r.pending && r.kind === 'case' && r.priority === p).length
  return { total, byKind, p0: at(0), p1: at(1) }
}

/** What is pending for this person, for the top bar: the Cases count and the bell. */
export function useInboxCounts(access: CasesAccess): InboxCounts {
  const { rows } = useInbox(access)
  return useMemo(() => inboxCounts(rows), [rows])
}
