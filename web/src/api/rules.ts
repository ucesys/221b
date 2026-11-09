// The validation rules (Phase 12, ADR-138), listed in Cases › Mycroft ›
// Validation rules: the check ids the agents name their findings by. The
// built-in ones ship in code; a SuperAdmin adds rules, changes their
// meaning, case type and jobs, retires them (the agents are no longer
// offered them; what is keyed by them still matches) or replaces one by
// another (the old id kept as an alias). Ids never change. The ids the
// agents made up (other.<slug>) are listed with how often they were
// found, and promoted to a rule; the daily housekeeping suggests rules
// from them, and promotes an exact repeat by itself. Since CF-85 every
// row carries the lists its counts stand for (its cases, the runs that
// found it, its latest findings, its exceptions), for its drawer; an id
// an agent made up carries the agents' descriptions of it (CF-86); and
// the suggestions are Inbox rows for a SuperAdmin (CF-87).
import { call } from './call'

export type RuleState = 'active' | 'retired' | 'replaced'

/** A case keyed by a row's id: its status, and whether it is open. */
export interface RuleCase {
  id: string
  title?: string
  status: string
  outcome?: string
  priority: number
  open: boolean
}

/** One finding kept under its id: its run, where, and what became of it. */
export interface FoundFinding {
  run: string
  id?: string
  at: string
  /** The id the agent named, when an alias of the row's. */
  check?: string
  cluster?: string
  object?: string
  title?: string
  severity?: string
  /** opened, seen, known, excepted, reopened, regression, capped, invalid, ignored, expected. */
  routing?: string
  case?: string
  knownIssue?: string
  exception?: string
}

/** An agent's description of an id it made up (CF-86): what it means and why no rule fitted. */
export interface Description {
  /** The id described, in a suggestion's list. */
  id?: string
  job: string
  kind?: string
  text: string
  run: string
  at: string
}

/** What a row's counts stand for, listed for its drawer (CF-85). */
export interface Behind {
  caseList: RuleCase[]
  /** The runs that found it, the newest first. */
  runIds: string[]
  /** Its latest findings, the newest first. */
  latest: FoundFinding[]
  /** Its active exceptions. */
  exceptionIds: string[]
  titles?: { text: string; n: number }[]
  /** The kinds of the objects its findings are on, with how many. */
  kinds?: Record<string, number>
  descriptions?: Description[]
}

/** A validation rule as it stands, with what is keyed by it (its aliases' too); the lists behind its counts (CF-85) are absent from a server before them. */
export interface Rule extends Partial<Behind> {
  id: string
  says: string
  /** The type of the case a finding of it opens. */
  type: string
  /** The kinds of jobs whose agents are offered it: "nightly-audit". */
  jobs: string[]
  /** A standard's control it stands for (CIS, Pod Security), when someone said. */
  control?: string
  builtIn: boolean
  /** A built-in someone changed. */
  edited?: boolean
  /** Promoted by the housekeeping, as an exact repeat. */
  auto?: boolean
  state: RuleState
  replacedBy?: string
  updated?: string
  by?: string
  /** The ids it replaced. */
  aliases?: string[]
  openCases: number
  cases: number
  /** Active exceptions. */
  exceptions: number
  findings: number
  lastFound?: string
  /** A case, known issue, exception or run holds it: it is retired, never deleted. */
  used: boolean
  /** How many runs found it, on which clusters, and when first (CF-85). */
  runs?: number
  clusters?: string[]
  firstFound?: string
}

/** An id an agent made up that is not on the list, with the lists behind its counts (CF-85). */
export interface FoundId extends Partial<Behind> {
  id: string
  findings: number
  runs: number
  clusters: string[]
  jobs: string[]
  cases: number
  openCases: number
  exceptions: number
  /** Its findings' most frequent title. */
  title?: string
  /** Its cases' most frequent type. */
  type: string
  first?: string
  last?: string
  /** The suggested rule that groups it, by its id. */
  suggestion?: string
  /** Dismissed by a person: not suggested again. */
  dismissed?: boolean
}

/** A rule the housekeeping drafted, for a person to promote, edit or dismiss. */
export interface Suggestion {
  /** The id suggested. */
  id: string
  says: string
  type: string
  jobs: string[]
  /** The other.<slug> ids it groups, the most found first: the first names it. */
  from: string[]
  runs: number
  clusters: number
  created: string
  /** Why it was suggested, from facts (CF-86). */
  why?: string
  /** The agents' descriptions of the ids it groups. */
  descriptions?: Description[]
}

export interface RuleJob {
  id: string
  label: string
}

export interface RulesPage {
  rules: Rule[]
  found: FoundId[]
  suggestions: Suggestion[]
  jobs: RuleJob[]
  types: string[]
  /** The clusters' names by id: the rows name a cluster by its id, the page shows its name. */
  clusterNames?: Record<string, string>
  /** A SuperAdmin: changes the rules. */
  mayChange: boolean
}

export interface RuleInput {
  id: string
  says: string
  type: string
  jobs: string[]
  control?: string
}

/** What Save changes; what is left out stays. There is no id: ids never change. */
export interface RulePatch {
  says?: string
  type?: string
  jobs?: string[]
  control?: string
}

/** Promotes other.<slug> ids: kept as the first (id empty), or under a clean id, the others aliases of it. */
export interface PromoteInput {
  from: string[]
  id?: string
  says: string
  type: string
  jobs: string[]
  control?: string
}

const enc = encodeURIComponent
const base = '/api/v1/cases/rules'
const send = (url: string, method: string, body?: unknown) => call<RulesPage>(url, { method, ...(body === undefined ? {} : { body: JSON.stringify(body) }) })

/** The page; every change answers it as it stands. */
export const fetchRules = (background = false) => call<RulesPage>(base, {}, background)
export const addRule = (in_: RuleInput) => send(base, 'POST', in_)
export const editRule = (id: string, patch: RulePatch) => send(`${base}/${enc(id)}`, 'PATCH', patch)
export const retireRule = (id: string) => send(`${base}/${enc(id)}/retire`, 'POST')
export const activateRule = (id: string) => send(`${base}/${enc(id)}/activate`, 'POST')
export const replaceRule = (id: string, by: string) => send(`${base}/${enc(id)}/replace`, 'POST', { by })
export const deleteRule = (id: string) => send(`${base}/${enc(id)}`, 'DELETE')
export const promoteRule = (in_: PromoteInput) => send(`${base}/promote`, 'POST', in_)
/** Dismisses a suggestion by its first id, or an id found outside the list alone: neither is suggested again. */
export const dismissSuggestion = (id: string) => send(`${base}/suggestions/${enc(id)}`, 'DELETE')
/** The suggested rules, for a SuperAdmin's Inbox (CF-87); anyone else is refused. */
export const fetchSuggestions = (background = false) => call<{ suggestions: Suggestion[] }>(`${base}/suggestions`, {}, background)

/** Where a rule opens: Cases › Mycroft › Validation rules. */
export const ruleHref = (id: string) => `#/cases/mycroft/rules/${enc(id)}`
