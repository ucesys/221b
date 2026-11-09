// Cases › Jobs (Phase 12, FX-13): two sub-tabs, Schedule (the jobs, with
// what starts each and its budget) and Runs (each run with its report,
// findings and proposed changes). #/cases/jobs is the Schedule,
// #/cases/jobs/runs the Runs; the old #/cases/runs links open the Runs.
import { navigate, type Route } from '../../lib/router'
import { JobsTab } from './JobsTab'
import { jobsSub, runsHref, scheduleHref } from './jobsRoute'
import { RunsTab } from './RunsTab'

const SUBS = [
  { id: 'schedule', label: 'Schedule', href: scheduleHref, tip: 'The jobs: what starts each, on which clusters, and its budget' },
  { id: 'runs', label: 'Runs', href: runsHref, tip: 'Each run: its report, findings and proposed changes, and what it cost' },
] as const

export function JobsArea({ route, onToast }: { route: Route; onToast?: (msg: string) => void }) {
  const sub = jobsSub(route.path)
  return (
    // The column's width, whatever the tab holds: Schedule and Runs are as wide as each other (JR-04),
    // and as wide as the Inbox (CF-66).
    <div className="cs-col cs-col-inbox" style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="jobs-area">
      <div className="rk-seg" role="group" aria-label="Jobs" style={{ alignSelf: 'flex-start' }}>
        {SUBS.map((s) => (
          <button key={s.id} aria-pressed={sub === s.id} title={s.tip} onClick={() => sub !== s.id && navigate(s.href)} data-testid={`jobs-sub-${s.id}`}>
            {s.label}
          </button>
        ))}
      </div>
      {sub === 'runs' ? <RunsTab route={route} onToast={onToast} /> : <JobsTab route={route} onToast={onToast} />}
    </div>
  )
}
