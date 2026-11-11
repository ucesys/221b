import { useEffect, useMemo, useState } from 'react'
import * as api from '../../api/jobs'
import { fetchJobs } from '../../api/jobs'

/** Names for the ids a run carries. */
export interface RunNames {
  /** An agent by its name ("Auditor"), or its id when the name is not known. */
  agent: (id?: string) => string
  /** A cluster by its name, or its id when the console does not list it. */
  cluster: (id: string) => string
  /** A job by its label ("Nightly audit"), or its id when the label is not known (R2-15). */
  job: (id?: string) => string
}

/**
 * A run names its agent and its clusters by id, and a case its job. The
 * jobs' own list has the names of all three, so the Runs table, a run's
 * drawer and a case's drawer read them from there and show names, as
 * Schedule and the Inbox do (U-11, U-12, R2-15). Until the list comes, and
 * for whoever may not read it, the ids stand.
 */
export function useRunNames(): RunNames {
  const [maps, setMaps] = useState<{ agents: Record<string, string>; clusters: Record<string, string>; jobs: Record<string, string> }>({ agents: {}, clusters: {}, jobs: {} })
  useEffect(() => {
    let live = true
    fetchJobs(true).then(
      (v) => {
        if (!live) return
        const list = (x: unknown) => (Array.isArray(x) ? x : [])
        const agents: Record<string, string> = {}
        const jobs: Record<string, string> = {}
        // A job's own label after its kind's: a job may be named apart from its kind.
        for (const k of [...list(v.kinds), ...list(v.jobs)] as (api.JobKind | api.Job)[]) {
          if (k.agent && k.agentName) agents[k.agent] = k.agentName
          if (k.id && k.label) jobs[k.id] = k.label
        }
        const clusters: Record<string, string> = {}
        for (const c of list(v.clusters) as { id: string; name?: string }[]) if (c.name) clusters[c.id] = c.name
        setMaps({ agents, clusters, jobs })
      },
      () => undefined,
    )
    return () => {
      live = false
    }
  }, [])
  return useMemo(
    () => ({
      agent: (id) => (id ? (maps.agents[id] ?? id) : ''),
      cluster: (id) => maps.clusters[id] ?? id,
      job: (id) => (id ? (maps.jobs[id] ?? id) : ''),
    }),
    [maps],
  )
}
