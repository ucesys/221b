import { useEffect, useState } from 'react'
import { budgetText, fetchJobs, type Job } from '../../api/jobs'
import { useShowCosts } from '../../lib/costs'
import { jobHref } from './jobsRoute'

/**
 * An agent's jobs, in its drawer's "On its own" card (Phase 12, JB-15):
 * what starts it with nobody at the screen, on which clusters, within
 * what budget, each a link to AIOps › Jobs. Nothing when it has none.
 */
export function AgentJobs({ agent }: { agent: string }) {
  const [jobs, setJobs] = useState<Job[]>([])
  const costs = useShowCosts()
  useEffect(() => {
    let live = true
    fetchJobs(true).then(
      (v) => live && setJobs((v?.jobs ?? []).filter((j) => j.agent === agent)),
      () => live && setJobs([]),
    )
    return () => {
      live = false
    }
  }, [agent])
  if (!jobs.length) return null
  return (
    <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 4 }} data-testid="agent-jobs">
      {jobs.map((j) => (
        <li key={j.id}>
          <a className="rk-link" href={jobHref(j.id)}>
            {j.label}
          </a>
          : {j.on ? j.whenText : 'paused'}
          {j.clusters?.length ? `, on ${j.clusters.join(', ')}` : ''}
          {costs && (j.budget.runUSD || j.budget.dayUSD) ? `, up to ${budgetText(j.budget)}` : ''}.
        </li>
      ))}
    </ul>
  )
}
