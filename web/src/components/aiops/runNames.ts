import { useEffect, useMemo, useState } from 'react'
import { fetchJobs } from '../../api/jobs'

/** Names for the ids a run carries. */
export interface RunNames {
  /** An agent by its name ("Auditor"), or its id when the name is not known. */
  agent: (id?: string) => string
  /** A cluster by its name, or its id when the console does not list it. */
  cluster: (id: string) => string
}

/**
 * A run names its agent and its clusters by id. The jobs' own list has the
 * names of both, so the Runs table and a run's drawer read them from there
 * and show names, as Schedule and the Inbox do (U-11, U-12). Until the
 * list comes, and for whoever may not read it, the ids stand.
 */
export function useRunNames(): RunNames {
  const [maps, setMaps] = useState<{ agents: Record<string, string>; clusters: Record<string, string> }>({ agents: {}, clusters: {} })
  useEffect(() => {
    let live = true
    fetchJobs(true).then(
      (v) => {
        if (!live) return
        const agents: Record<string, string> = {}
        for (const k of [...(v.kinds ?? []), ...(v.jobs ?? [])]) if (k.agent && k.agentName) agents[k.agent] = k.agentName
        const clusters: Record<string, string> = {}
        for (const c of v.clusters ?? []) if (c.name) clusters[c.id] = c.name
        setMaps({ agents, clusters })
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
    }),
    [maps],
  )
}
