import type { ClusterInfo, Tone } from '../api/types'
import type { StreamState } from '../api/live'
import { fmtTs } from './format'

/** The connection to the cluster as a tone and a sentence. */
export function connection(cluster: ClusterInfo | null, stream: StreamState): { tone: Tone; label: string; tip: string } {
  if (stream === 'reconnecting') return { tone: 'warn', label: 'Reconnecting', tip: 'Lost contact with the 221B server; reconnecting.' }
  if (!cluster || stream === 'connecting') return { tone: 'muted', label: 'Connecting', tip: 'Connecting to the 221B server.' }
  if (cluster.state === 'disconnected') {
    return { tone: 'err', label: 'Disconnected', tip: `Cannot reach the Kubernetes API server. ${cluster.message ? cluster.message + ' ' : ''}Since ${fmtTs(cluster.since)}.` }
  }
  if (cluster.state === 'connecting') return { tone: 'muted', label: 'Connecting', tip: 'Connecting to the Kubernetes API server.' }
  if (!cluster.synced) return { tone: 'info', label: 'Listing', tip: 'Listing nodes and pods for the first time.' }
  return { tone: 'ok', label: 'Connected', tip: `Connected to the Kubernetes API server since ${fmtTs(cluster.since)}.` }
}
