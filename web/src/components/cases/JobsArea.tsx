// AIOps › Jobs (Phase 12, FX-13): Schedule (the jobs, with what starts
// each and its budget) and Runs (each run with its report, findings and
// proposed changes); since CF-89 a third, Validation rules (the check ids
// the agents name their findings by), moved here from Mycroft.
// #/cases/jobs is the Schedule, #/cases/jobs/runs the Runs,
// #/cases/jobs/rules[/<id>[/<section>]] the rules; the old #/cases/runs
// and #/cases/mycroft/rules links open them here (lib/router.ts).
import { navigate, type Route } from '../../lib/router'
import { JobsTab } from './JobsTab'
import { jobsSub, pathId, ruleSection, rulesHref, runsHref, scheduleHref } from './jobsRoute'
import { RulesView } from './RulesView'
import { RunsTab } from './RunsTab'

const SUBS = [
  { id: 'schedule', label: 'Schedule', href: scheduleHref, tip: 'The jobs: what starts each, on which clusters, and its budget' },
  { id: 'runs', label: 'Runs', href: runsHref, tip: 'Each run: its report, findings and proposed changes, and what it cost' },
  { id: 'rules', label: 'Validation rules', href: rulesHref, tip: 'The rules the agents name their findings by: edit, retire, merge, and promote the ids they made up' },
] as const

export function JobsArea({ route, onToast }: { route: Route; onToast?: (msg: string) => void }) {
  const sub = jobsSub(route.path)
  const ruleOpen = sub === 'rules' && pathId(route.path, 'rules') !== ''
  return (
    // The column's width, whatever the tab holds: Schedule, Runs and Validation rules are as wide as
    // each other (JR-04), and as wide as the Inbox (CF-66).
    <div className="cs-col cs-col-inbox" style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="jobs-area">
      <div className="rk-seg" role="group" aria-label="Jobs" style={{ alignSelf: 'flex-start' }}>
        {SUBS.map((s) => (
          <button key={s.id} aria-pressed={sub === s.id} title={s.tip} onClick={() => (sub !== s.id || ruleOpen) && navigate(s.href)} data-testid={`jobs-sub-${s.id}`}>
            {s.label}
          </button>
        ))}
      </div>
      {sub === 'runs' ? (
        <RunsTab route={route} onToast={onToast} />
      ) : sub === 'rules' ? (
        <RulesView open={pathId(route.path, 'rules')} section={ruleSection(route.path)} onToast={onToast} />
      ) : (
        <JobsTab route={route} onToast={onToast} />
      )}
    </div>
  )
}
