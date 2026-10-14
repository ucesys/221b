import type { ClusterInfo } from '../api/types'
import type { Route } from '../lib/router'

/** What every tab gets from the shell. */
export interface TabProps {
  cluster: ClusterInfo | null
  now: number
  onToast: (msg: string) => void
  route: Route
}
