// The accepted exceptions (Phase 12, FX-03 to FX-11), listed in Cases ›
// Mycroft. Since wave 6 an exception is made by closing an agent-found case
// as an exception (api/casework.ts), and since build 14 by hand with New
// exception; an inactive one (removed) may be made active again. Since
// wave 6e an RCA is no record of its own: it is part of the case it was
// fixed with (CaseRCA), and past RCAs are found by searching the closed
// cases.
import { call, post } from './call'

/** The state as the server keeps it: "removed" is shown as Inactive (build 14). */
export type ExceptionState = 'active' | 'expired' | 'removed'

export interface AcceptedException {
  id: string
  cluster?: string
  resource?: string
  /** The cluster's display name; its id when the console does not know it. */
  clusterName?: string
  /** The validation rule (kept as check): the finding's check id, or "title:" and its title made plain. */
  check: string
  /** What the validation rule means, when the catalogue says. */
  ruleMeaning?: string
  title: string
  detail?: string
  finding?: string
  run?: string
  job?: string
  reason: string
  by: string
  at: string
  expires?: string
  removedBy?: string
  removedAt?: string
  state: ExceptionState
  /** How many findings it left out, and when last. */
  matches: number
  lastMatched?: string
  /** The case closed as this exception: "C-12". */
  case?: string
  /** Never matched, or not for Settings › Cases' exceptionStaleDays. */
  stale: boolean
  /** When it last changed (wave 6g); older records: removedAt or at. */
  updated?: string
  /** Housekeeping removed it as a duplicate of this one: "EX-2". */
  duplicateOf?: string
}

/** A validation rule New exception offers: one seen on the cluster, with what it means. */
export interface RuleOption {
  id: string
  meaning?: string
}

/** A cluster a new exception may be on, by its name, with the rules seen on it. */
export interface ExceptionCluster {
  id: string
  name: string
  rules: RuleOption[]
}

export interface ExceptionsList {
  exceptions: AcceptedException[]
  mayChange: boolean
  /** For who may create one: the clusters New exception offers. */
  clusters?: ExceptionCluster[]
}

/** What New exception sends: the object empty for the cluster as a whole, the rule an id or a finding's title, the last day YYYY-MM-DD or none. */
export interface NewExceptionInput {
  cluster: string
  resource?: string
  check: string
  reason: string
  expires?: string
}

/** An object New exception suggests as it is typed: as the exception names it (namespace/Kind/name or Kind/name), with its kind; "cases" when a case or an exception names it, "cluster" when the console keeps it. */
export interface CaseObject {
  object: string
  kind: string
  namespace?: string
  name: string
  from: 'cases' | 'cluster'
}

const enc = encodeURIComponent

/** The exceptions; stale ones only with stale. */
export const fetchExceptions = (background = false, stale = false) => call<ExceptionsList>('/api/v1/cases/exceptions' + (stale ? '?stale=1' : ''), {}, background)
export const fetchException = (id: string) => call<AcceptedException>('/api/v1/cases/exceptions/' + enc(id))
export const removeException = (id: string) => call<AcceptedException>('/api/v1/cases/exceptions/' + enc(id), { method: 'DELETE' })
/** New exception (build 14): approvers only; refused (409) while an active one covers the same cluster, object and rule. */
export const createException = (input: NewExceptionInput) => call<AcceptedException>('/api/v1/cases/exceptions', post(input))
/** What New exception suggests for the object typed so far (two characters or more), on the cluster: approvers only; only what the person may see. */
export const fetchCaseObjects = (cluster: string, q: string, limit = 20) => call<{ objects: CaseObject[] }>(`/api/v1/cases/objects?cluster=${enc(cluster)}&q=${enc(q)}&limit=${limit}`)
/** Make active again (build 14): refused (409) while an active one covers the same cluster, object and rule. */
export const reactivateException = (id: string) => call<AcceptedException>('/api/v1/cases/exceptions/' + enc(id) + '/reactivate', { method: 'POST' })

/** Where an exception opens: Cases › Mycroft › Exceptions (MY-01). */
export const exceptionHref = (id: string) => `#/cases/mycroft/exceptions/${enc(id)}`
