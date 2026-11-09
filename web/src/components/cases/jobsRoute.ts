// Where Cases › Jobs reads what is open (FX-13): #/cases/jobs is the
// Schedule and #/cases/jobs/<id> a job there; #/cases/jobs/runs the Runs
// and #/cases/jobs/runs/<run-id> a run. The path is read after the tab's
// name, or from its start when the shell has taken the name off.

/** The path after "jobs". */
function rest(path: string[]): string[] {
  const at = path.indexOf('jobs')
  if (at >= 0) return path.slice(at + 1)
  const first = path[0] ?? ''
  return first === 'cases' || first === 'inbox' ? [] : path
}

/** Which of Jobs' sub-tabs a path opens. */
export function jobsSub(path: string[]): 'schedule' | 'runs' {
  return rest(path)[0] === 'runs' ? 'runs' : 'schedule'
}

/** The id of the job (tab "jobs") or the run (tab "runs") a path opens; "" for none. */
export function pathId(path: string[], tab: 'runs' | 'jobs'): string {
  const r = rest(path)
  if (tab === 'runs') return r[0] === 'runs' ? (r[1] ?? '') : ''
  return r[0] && r[0] !== 'runs' ? r[0] : ''
}

export const scheduleHref = '#/cases/jobs'
export const runsHref = '#/cases/jobs/runs'
export const runHref = (id: string) => `${runsHref}/${encodeURIComponent(id)}`
export const jobHref = (id: string) => `${scheduleHref}/${encodeURIComponent(id)}`

/** A case's drawer in the Inbox: "C-12" opens #/cases/inbox/case/12 (Phase 12, wave 5). */
export { caseHref } from '../../api/casework'

