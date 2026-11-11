import { useEffect, useState } from 'react'
import { fetchProposals, type ProposalQuery, type Proposals } from '../../api/cards'

/**
 * Polls what waits, every so often and when the person comes back to the
 * tab. A person who may not see proposals gets nothing, quietly.
 */
export function useProposals(q: ProposalQuery, every = 15_000): { data: Proposals | null; error: string; reload: () => void } {
  const [data, setData] = useState<Proposals | null>(null)
  const [error, setError] = useState('')
  const [tick, setTick] = useState(0)
  const key = JSON.stringify(q)
  useEffect(() => {
    let gone = false
    const query = JSON.parse(key) as ProposalQuery
    const look = (background: boolean) =>
      fetchProposals(query, background).then(
        (r) => {
          if (gone) return
          setData(r)
          setError('')
        },
        (e: unknown) => !gone && setError(e instanceof Error ? e.message : String(e)),
      )
    void look(false)
    const t = setInterval(() => void look(true), every)
    const back = () => {
      if (document.visibilityState === 'visible') void look(true)
    }
    window.addEventListener('focus', back)
    document.addEventListener('visibilitychange', back)
    return () => {
      gone = true
      clearInterval(t)
      window.removeEventListener('focus', back)
      document.removeEventListener('visibilitychange', back)
    }
  }, [key, every, tick])
  return { data, error, reload: () => setTick((t) => t + 1) }
}
