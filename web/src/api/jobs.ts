// The jobs that start an agent on their own, their runs and findings
// (Phase 12, JB-15 to JB-18, FX-01 to FX-12): AIOps › Jobs › Schedule and
// Runs. Since wave 6 a finding is not decided on its own: the console
// routes each finding of a run to a case (a new one, or the open case of
// the same problem), a known issue or an exception, and the run says
// what each did.
import { call, post } from './call'
import { hideAmounts } from '../lib/costs'

/** A schedule as the console keeps it (package schedule's Spec). */
export interface ScheduleSpec {
  kind: 'daily' | 'weekdays' | 'weekly' | 'hours' | 'cron'
  /** HH:MM: when a daily, weekday or weekly schedule runs, and the first run each day of an every-N-hours one. */
  at?: string
  /** A weekly schedule's days: mon to sun. */
  days?: string[]
  /** Hours between runs, 1 to 23. */
  every?: number
  cron?: string
  /** An IANA name; empty is the console's. */
  timeZone?: string
}

export type JobEvent = 'alert' | 'report' | 'source' | 'case'

export interface JobWhen {
  schedule?: ScheduleSpec
  event?: JobEvent | ''
  /** An alert job's least severity. */
  severity?: 'warning' | 'error'
}

export interface Budget {
  runUSD?: number
  dayUSD?: number
}

export interface Trigger {
  kind: 'schedule' | 'event' | 'manual'
  what: string
  setBy?: string
  by?: string
  slot?: string
  event?: string
  key?: string
}

export type RunStatus = 'running' | 'ok' | 'failed' | 'skipped' | 'missed' | 'budget' | 'interrupted' | 'cancelled'

// --- Jobs and findings, round 2 (Phase 12, wave 5, JR-01 to JR-11) ---

/** A run's health (JR-01): the worst of what it found, or that it failed; missing while it runs and for a time let go. */
export type RunHealth = 'critical' | 'warning' | 'info' | 'none' | 'failed'

/** A run's findings by severity. */
export interface Severities {
  critical?: number
  warning?: number
  info?: number
}

export interface RunBrief {
  id: string
  job: string
  label: string
  agent?: string
  trigger: Trigger
  subject?: string
  repeats?: number
  ref?: string
  clusters?: string[]
  started: string
  ended?: string
  status: RunStatus
  note?: string
  findings: number
  /** Why its findings were not routed into cases (W-8). */
  notRouted?: string
  proposals: number
  costUSD: number
  model?: string
  health?: RunHealth
  severities?: Severities
  /** What the run could not be used for (I-3): see RunLimits. */
  failedClusters?: string[]
  leftOut?: LeftOut
  notRechecked?: number
}

/**
 * What a run could not be used for (phase 12 review, I-3): the clusters
 * that did not answer, so none of their fixes was verified (W-2); the
 * findings left out past the run's cap, the least severe first (W-3); and
 * how many fixes in its scope the agent did not re-check (W-1).
 */
export interface RunLimits {
  failedClusters?: string[]
  leftOut?: LeftOut
  notRechecked?: number
}

/** The findings a run left out past its cap, by severity (W-3). */
export interface LeftOut {
  count: number
  severities: Severities
  cap: number
}

// --- One flow for findings and cases (Phase 12, wave 6) ---

/** What the agents' repeats of a problem are matched by: the cluster, the object ("" cluster-wide) and the check. */
export interface FindingKey {
  cluster: string
  object: string
  check: string
}

/**
 * What a finding did (wave 6): opened a case, joined the open case of the
 * same problem (seen), was recorded on a known issue (known), was left
 * out by an exception (excepted), reopened a case whose fix did not hold,
 * opened a case marked a regression of one fixed before, was over the
 * run's cap of new cases (capped), or was not valid (its check or object);
 * since wave 6c, ignored: below the job's lowest priority (CF-33); since
 * CF-82, expected: the agent found the system working as intended, and
 * said why, so it opened no case whatever its severity.
 */
export type FindingRouting = 'opened' | 'seen' | 'known' | 'excepted' | 'reopened' | 'regression' | 'capped' | 'invalid' | 'ignored' | 'expected'

export interface Finding {
  id: string
  severity: 'info' | 'warning' | 'critical'
  /** The check, from the job kind's list or other.<slug>. */
  check?: string
  title: string
  detail?: string
  evidence?: string[]
  cluster?: string
  resource?: string
  key?: FindingKey
  routing: FindingRouting
  /** The case it opened, joined or reopened: "C-12". */
  case?: string
  /** The known issue it was recorded on: "KI-4". */
  knownIssue?: string
  /** The exception that left it out: "EX-3". */
  exception?: string
  /** Why it was not valid. */
  invalidReason?: string
  /** Its check was never seen on the cluster before this run. */
  newCheck?: boolean
  /** The priority its severity gives it, 0 to 4, when the server says (CF-33). */
  priority?: number
  /** The agent found the system working as intended, and why, with its evidence (CF-82). */
  expected?: boolean
  expectedWhy?: string
  /** How sure the agent is of the cause: confirmed when it read the evidence, likely when it could not confirm it (PM-J1d). */
  confidence?: 'confirmed' | 'likely'
}

/** How many findings of a run did each thing, and the fixes it verified. */
export interface Routed {
  opened: number
  seen: number
  known: number
  excepted: number
  reopened: number
  regression: number
  verified: number
  capped: number
  invalid: number
  /** Below the job's lowest priority: kept on the run, no case (CF-33). */
  ignored?: number
  /** The system working as intended, the agent said: kept on the run, no case (CF-82). */
  expected?: number
}

export interface Run {
  id: string
  job: string
  kind: string
  label: string
  agent?: string
  trigger: Trigger
  subject?: string
  repeats?: number
  lastSeen?: string
  ref?: string
  clusters?: string[]
  model?: string
  started: string
  ended?: string
  status: RunStatus
  note?: string
  report?: string
  findings?: Finding[]
  /** What its findings did; missing on a run under way. */
  routed?: Routed
  /** The fixes this run verified: it re-checked each one and found the problem gone (W-1). */
  verifiedCases?: string[]
  /** Validation rules (check ids) never seen on the cluster before this run. */
  newChecks?: string[]
  /** Why its findings were not routed into cases (W-8, I-3); reroute: the console tries again on its next look. */
  notRouted?: string
  reroute?: boolean
  /** What the validation rules of its findings and the new ones mean, by id, when the catalogue says (build 14). */
  ruleMeanings?: Record<string, string>
  proposals?: ProposalRef[]
  tokens: { in: number; out: number }
  costUSD: number
  /** Whether the person may cancel it while it runs (job editors). */
  mayCancel?: boolean
  health?: RunHealth
  severities?: Severities
  /** What the run did, as its kind says it: a Wiki sync's source, items read, drafts (KN-28). */
  facts?: RunFact[]
  /** Its kind makes no findings: it is not an audit (a Wiki sync, the drift check). */
  noFindings?: boolean
  /** What the run could not be used for (I-3): see RunLimits. */
  failedClusters?: string[]
  leftOut?: LeftOut
  notRechecked?: number
}

/** One thing a run did, for its drawer (KN-28): a label, the value in words, where it leads. */
export interface RunFact {
  label: string
  text: string
  href?: string
  tone?: 'err' | 'warn'
}

/** A change a run proposed: filed as a card (its id), or not, with why; and what it resolves (CF-25 to CF-27). */
export interface ProposalRef {
  id: string
  title: string
  /** Why it was not filed: "names no case it resolves" for an agent's change that names none. */
  error?: string
  /** The cases ("C-12") and known issues ("KI-3") it resolves. */
  resolves?: string[]
  /** Their titles, by id, when the server says (CF-39). */
  resolvesTitles?: Record<string, string>
}

export interface ExternalState {
  ready: boolean
  detail?: string
  on: boolean
  when?: string
  next?: string
  lastAt?: string
  last?: string
  lastOk?: boolean
  running?: boolean
}

export interface Job {
  id: string
  kind: string
  name?: string
  /** The job's own instructions to its agent; missing is its kind's built-in ones (JR-06). */
  task?: string
  when: JobWhen
  clusters?: string[]
  budget: Budget
  /** The lowest priority a finding may open a case at, 0 to 4; below it a finding is ignored (CF-33). Missing for a kind without findings. */
  minPriority?: number
  on: boolean
  skipOverlap: boolean
  catchUp: boolean
  setBy?: string
  setAt?: string
  since?: string
  added?: boolean
  label: string
  agent?: string
  agentName?: string
  does?: string
  events?: JobEvent[]
  /** The kind's built-in instructions; missing for a kind whose instructions cannot be changed (JR-05). */
  builtinTask?: string
  /** What its agent reads, and what a run may make (JR-05). */
  reads?: string
  proposes?: string
  schedulable: boolean
  manual: boolean
  /** Rebuild now is offered beside Run now: a Wiki sync gives Mycroft every item again (KN-29). */
  rebuild?: boolean
  whenText: string
  next?: string
  nextText?: string
  last?: RunBrief
  running: boolean
  spentToday: number
  stoppedToday?: string
  unusable?: string
  external?: ExternalState
  where?: string
  link?: string
}

export interface JobKind {
  id: string
  label: string
  agent?: string
  agentName?: string
  does?: string
  events?: JobEvent[]
  schedulable: boolean
  external?: boolean
  /** The kind sends instructions a job may change. */
  task?: boolean
  /** Another job of the kind may be added: one a report or a case starts runs its first job only (JR-07). */
  addMore?: boolean
}

export interface JobsSettings {
  /** Roles besides SuperAdmin that may edit jobs. */
  editors?: string[]
  readerRole?: 'viewer' | 'superViewer'
  readerGroup?: string
  setBy?: string
  setAt?: string
}

export interface JobsView {
  jobs: Job[]
  kinds: JobKind[]
  clusters: { id: string; name: string }[]
  settings: JobsSettings
  mayEdit: boolean
  admin: boolean
  timeZone: string
}

/** A change to a job: each field given is set. */
export interface JobChange {
  name?: string
  /** "" goes back to the kind's built-in instructions. */
  task?: string
  when?: JobWhen
  clusters?: string[]
  budget?: Budget
  /** 0 to 4. */
  minPriority?: number
  on?: boolean
  skipOverlap?: boolean
  catchUp?: boolean
}

const put = (body: unknown): RequestInit => ({ method: 'PUT', body: JSON.stringify(body) })
const enc = encodeURIComponent

export const fetchJobs = (background = false) => call<JobsView>('/api/v1/jobs', {}, background)
export const fetchJob = (id: string) => call<Job>('/api/v1/jobs/' + enc(id))
export const updateJob = (id: string, change: JobChange) => call<Job>('/api/v1/jobs/' + enc(id), put(change))
export const addJob = (kind: string) => call<Job>('/api/v1/jobs', post({ kind }))
export async function removeJob(id: string): Promise<void> {
  const res = await fetch('/api/v1/jobs/' + enc(id), { method: 'DELETE', headers: { Accept: 'application/json' } })
  if (!res.ok) {
    let msg = `The server answered ${res.status}.`
    try {
      const b = (await res.json()) as { error?: string }
      if (b.error) msg = b.error
    } catch {
      // Not JSON.
    }
    throw new Error(msg)
  }
}
export const runJobNow = (id: string) => call<RunBrief>('/api/v1/jobs/' + enc(id) + '/run', post({}))
export const rebuildJobNow = (id: string) => call<RunBrief>('/api/v1/jobs/' + enc(id) + '/rebuild', post({}))
export const fetchJobRuns = (id: string, limit = 20) => call<{ runs: RunBrief[]; total: number }>(`/api/v1/jobs/${enc(id)}/runs?limit=${limit}`)
export const describeJobSchedule = (s: ScheduleSpec) => call<{ description?: string; next?: string[]; error?: string }>('/api/v1/jobs/schedule', post(s))
export const fetchJobsSettings = () => call<JobsSettings>('/api/v1/jobs/settings')
export const saveJobsSettings = (s: JobsSettings) => call<JobsSettings>('/api/v1/jobs/settings', put(s))

export interface RunQuery {
  job?: string
  status?: string
  ref?: string
  mine?: boolean
  limit?: number
}
export function fetchRuns(q: RunQuery = {}, background = false) {
  const p = new URLSearchParams()
  if (q.job) p.set('job', q.job)
  if (q.status) p.set('status', q.status)
  if (q.ref) p.set('ref', q.ref)
  if (q.mine) p.set('mine', '1')
  if (q.limit) p.set('limit', String(q.limit))
  const qs = p.toString()
  return call<{ runs: RunBrief[]; total: number }>('/api/v1/runs' + (qs ? '?' + qs : ''), {}, background)
}
export const fetchRun = (id: string) => call<Run>('/api/v1/runs/' + enc(id))
export const cancelRun = (id: string) => call<RunBrief>(`/api/v1/runs/${enc(id)}/cancel`, post({}))

/** What a run's status reads as, and its tone. */
export const STATUS: Record<RunStatus, [string, 'ok' | 'warn' | 'err' | undefined]> = {
  running: ['Running', undefined],
  ok: ['Done', 'ok'],
  failed: ['Failed', 'err'],
  skipped: ['Skipped', 'warn'],
  missed: ['Missed', 'warn'],
  budget: ['Budget spent', 'warn'],
  interrupted: ['Interrupted', 'err'],
  cancelled: ['Cancelled', 'warn'],
}

const HEALTH_TONE: Record<RunHealth, 'ok' | 'warn' | 'err'> = { critical: 'err', warning: 'warn', info: 'ok', none: 'ok', failed: 'err' }

/** A run's health in words, for the Health column (JR-01). */
export const HEALTH_LABEL: Record<RunHealth, string> = { critical: 'Critical', warning: 'Warning', info: 'Info', none: 'None found', failed: 'Failed' }

/** The tone a run's health takes. */
export const healthTone = (h: RunHealth | undefined) => (h ? HEALTH_TONE[h] : undefined)

/**
 * A run's status as it reads (JR-01): a run done takes the tone of what it
 * found, and says the worst of it: "Done · 2 critical", amber "Done · 1
 * warning", green "Done" for info or nothing.
 */
export function runStatus(r: Pick<RunBrief, 'status' | 'health' | 'severities' | 'failedClusters'>): [string, 'ok' | 'warn' | 'err' | undefined] {
  const [status, tone] = STATUS[r.status] ?? [r.status, undefined]
  if (r.status === 'running' || r.status === 'skipped' || r.status === 'missed') return [status, tone]
  // A run done with a cluster that did not answer is done with errors (W-2).
  const errors = r.status === 'ok' && (r.failedClusters ?? []).length > 0
  const label = errors ? 'Done, with errors' : status
  const s = r.severities ?? {}
  const worst = s.critical ? `${s.critical} critical` : s.warning ? `${s.warning} warning${s.warning === 1 ? '' : 's'}` : ''
  const words = worst ? `${label} · ${worst}` : label
  if (r.status !== 'ok') return [words, tone]
  const found = healthTone(r.health) ?? tone
  return [words, errors && found !== 'err' ? 'warn' : found]
}

/** A run's limits in words, one line each (I-3): what failed, what was left out, what was not re-checked. */
export function runLimits(r: RunLimits): { key: 'failed' | 'leftOut' | 'notRechecked'; text: string }[] {
  const out: { key: 'failed' | 'leftOut' | 'notRechecked'; text: string }[] = []
  const failed = r.failedClusters ?? []
  if (failed.length) out.push({ key: 'failed', text: `${failed.length === 1 ? 'A cluster' : 'Clusters'} that did not answer: ${failed.join(', ')}. No fix there was verified.` })
  const l = r.leftOut
  if (l && l.count > 0) {
    const s = l.severities ?? {}
    const by = [s.critical ? `${s.critical} critical` : '', s.warning ? `${s.warning} warning${s.warning === 1 ? '' : 's'}` : '', s.info ? `${s.info} info` : ''].filter(Boolean).join(', ')
    out.push({ key: 'leftOut', text: `${l.count} ${l.count === 1 ? 'finding' : 'findings'} left out past the cap of ${l.cap}${by ? ` (${by})` : ''}: the least severe go first. Their problems count as neither seen nor gone.` })
  }
  const n = r.notRechecked ?? 0
  if (n > 0) out.push({ key: 'notRechecked', text: `${n} ${n === 1 ? 'fix' : 'fixes'} in its scope ${n === 1 ? 'was' : 'were'} not re-checked: ${n === 1 ? 'it keeps' : 'they keep'} waiting for a run that does.` })
  return out
}

/** A run's note with each line said once, with how many, for runs kept before notes were merged (JR-02); without amounts while costs are hidden (CF-40). */
export function mergedNote(note: string | undefined): string {
  if (!note) return ''
  const parts = hideAmounts(note).split('; ')
  const count = new Map<string, number>()
  for (const p of parts) count.set(p, (count.get(p) ?? 0) + 1)
  return [...count.entries()]
    .map(([p, n]) => {
      if (n < 2) return p
      const refuse = 'a proposed command Watson would refuse was left out'
      if (p.startsWith(refuse)) return `${n} proposed commands Watson would refuse were left out${p.slice(refuse.length)}`
      const other = 'a proposed command that is not kubectl or helm was left out'
      if (p.startsWith(other)) return `${n} proposed commands that are not kubectl or helm were left out${p.slice(other.length)}`
      return `${p} (${n} times)`
    })
    .join('; ')
}

/** A dollar amount: "$4", "$0.42". */
export function usd(v: number | undefined): string {
  if (!v) return '$0'
  if (v >= 10 || Number.isInteger(v)) return '$' + (Math.round(v * 100) / 100).toString()
  return '$' + v.toFixed(v < 0.01 ? 4 : 2)
}

/** A job's budget in words, as the list shows it. */
export function budgetText(b: Budget | undefined): string {
  const parts: string[] = []
  if (b?.runUSD) parts.push(`${usd(b.runUSD)} a run`)
  if (b?.dayUSD) parts.push(`${usd(b.dayUSD)} a day`)
  return parts.length ? parts.join(', ') : 'None'
}
