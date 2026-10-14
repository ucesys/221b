import { credText, type ClusterSummary } from '../api/clusters'
import type { ClusterInfo } from '../api/types'
import type { StreamState } from '../api/live'
import { fmtTs } from '../lib/format'
import { AlertIcon, ClockIcon } from './Icons'

interface Props {
  cluster: ClusterInfo | null
  stream: StreamState
  /** The open cluster's connection, for its credentials' expiry (P3-07). */
  summary?: ClusterSummary | null
  /** Where "Fix the connection" leads: the cluster page, when not on it. */
  pageHref?: string
}

interface Banner {
  tone: 'err' | 'warn' | 'info'
  title: string
  detail: string
  link?: { href: string; label: string }
}

/** Banners under the top bar for anything that makes the view stale or partial. */
export function banners(cluster: ClusterInfo | null, stream: StreamState, summary?: ClusterSummary | null, pageHref?: string, now = Date.now()): Banner[] {
  const out: Banner[] = []
  if (stream === 'reconnecting') {
    out.push({ tone: 'warn', title: 'Lost contact with the 221B server', detail: 'Reconnecting. The table shows the last state it received.' })
  }
  if (cluster?.state === 'disconnected') {
    out.push({ tone: 'err', title: 'Cannot reach the Kubernetes API server', detail: `${cluster.message ?? ''} Showing the last known state, since ${fmtTs(cluster.since)}.`.trim(), link: pageHref ? { href: pageHref, label: 'About the connection' } : undefined })
  }
  if (summary?.expired) {
    out.push({ tone: 'err', title: 'The console’s credentials for this cluster have expired', detail: credText(summary, now) + '.', link: pageHref ? { href: pageHref, label: 'Fix the connection' } : undefined })
  } else if (summary?.expiring) {
    out.push({ tone: 'warn', title: 'The console’s credentials for this cluster expire soon', detail: credText(summary, now) + '.', link: pageHref ? { href: pageHref, label: 'Fix the connection' } : undefined })
  }
  for (const w of cluster?.warnings ?? []) out.push({ tone: 'warn', title: 'Partial data', detail: w })
  if (cluster && cluster.state === 'connected' && !cluster.synced) {
    out.push({ tone: 'info', title: 'Listing nodes and pods', detail: 'The first list of a large cluster can take a few seconds.' })
  }
  return out
}

export function Banners({ cluster, stream, summary, pageHref }: Props) {
  const list = banners(cluster, stream, summary, pageHref)
  if (!list.length) return null
  return (
    <div role="status" aria-live="polite" style={{ flex: 'none', display: 'flex', flexDirection: 'column' }}>
      {list.map((b) => (
        <div key={b.title + b.detail} className="rk-banner" data-tone={b.tone}>
          {b.tone === 'err' ? <AlertIcon /> : <ClockIcon />}
          <strong style={{ fontWeight: 600, whiteSpace: 'nowrap' }}>{b.title}</strong>
          <span className="rk-banner-detail" title={b.detail}>
            {b.detail}
          </span>
          {b.link && (
            <a href={b.link.href} data-testid="banner-link">
              {b.link.label}
            </a>
          )}
        </div>
      ))}
    </div>
  )
}
