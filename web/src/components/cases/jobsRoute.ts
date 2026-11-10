// Where AIOps › Jobs reads what is open (FX-13): #/cases/jobs is the
// Schedule and #/cases/jobs/<id> a job there; #/cases/jobs/runs the Runs
// and #/cases/jobs/runs/<run-id> a run; since CF-89 #/cases/jobs/rules
// the Validation rules and #/cases/jobs/rules/<id>[/<section>] a rule's
// drawer. The path is read after the tab's name, or from its start when
// the shell has taken the name off.

/** The path after "jobs". */
function rest(path: string[]): string[] {
  const at = path.indexOf('jobs')
  if (at >= 0) return path.slice(at + 1)
  const first = path[0] ?? ''
  return first === 'cases' || first === 'inbox' ? [] : path
}

export type JobsSub = 'schedule' | 'runs' | 'rules'

/** Which of Jobs' sub-tabs a path opens. */
export function jobsSub(path: string[]): JobsSub {
  const s = rest(path)[0]
  return s === 'runs' || s === 'rules' ? s : 'schedule'
}

/** The id of the job (tab "jobs"), the run (tab "runs") or the rule (tab "rules") a path opens; "" for none. */
export function pathId(path: string[], tab: 'runs' | 'jobs' | 'rules'): string {
  const r = rest(path)
  if (tab === 'runs' || tab === 'rules') return r[0] === tab ? (r[1] ?? '') : ''
  return r[0] && r[0] !== 'runs' && r[0] !== 'rules' ? r[0] : ''
}

/** The list in a rule's drawer a path opens (…/rules/<id>/<section>); "" for none. */
export function ruleSection(path: string[]): string {
  const r = rest(path)
  return r[0] === 'rules' ? (r[2] ?? '') : ''
}

export const scheduleHref = '#/cases/jobs'
export const runsHref = '#/cases/jobs/runs'
/** Jobs › Validation rules (CF-89); a rule's drawer is ruleHref in api/rules. */
export const rulesHref = '#/cases/jobs/rules'
export const runHref = (id: string) => `${runsHref}/${encodeURIComponent(id)}`
export const jobHref = (id: string) => `${scheduleHref}/${encodeURIComponent(id)}`

/** A case's drawer in the Inbox: "C-12" opens #/cases/inbox/case/12 (Phase 12, wave 5). */
export { caseHref } from '../../api/casework'

