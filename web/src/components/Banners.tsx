import { credText, type ClusterSummary } from '../api/clusters'
import type { AlertBanner, BannerItem } from '../lib/alerts'
import { peekClickHash } from '../lib/peek'
import type { ClusterInfo } from '../api/types'
import type { StreamState } from '../api/live'
import { fmtTs } from '../lib/format'
import { AlertIcon, ClockIcon } from './Icons'

interface Props {
  cluster: ClusterInfo | null
  stream: StreamState
  now: number
  /** The open cluster's connection, for its credentials' expiry (P3-07). */
  summary?: ClusterSummary | null
  /** Where "Fix the connection" leads: the cluster page, when not on it. */
  pageHref?: string
  /** Why nothing is shown to this person (P3-02); "" otherwise. */
  accessError?: string
  /** What the Alerts tab has (P4-07), for every other tab; empty on it. */
  alerts?: AlertBanner[]
  /** Where "View alerts →" leads. */
  alertsHref?: string
}

interface Banner {
  tone: 'err' | 'warn' | 'info'
  title: string
  detail: string
  link?: { href: string; label: string; testid?: string }
  /** Monospace detail, for lists of objects. */
  mono?: boolean
  /** The detail as items, some of which open a drawer (ST-21). */
  items?: BannerItem[]
  testid?: string
}

/** Banners under the top bar for anything that makes the view stale or partial. */
export function banners(cluster: ClusterInfo | null, stream: StreamState, summary?: ClusterSummary | null, pageHref?: string, now = Date.now(), accessError = ''): Banner[] {
  const out: Banner[] = []
  if (stream === 'reconnecting') {
    out.push({ tone: 'warn', title: 'Lost contact with the 221B server', detail: 'Reconnecting. The table shows the last state it received.' })
  }
  // While the API server is away, that is the reason nothing is shown.
  if (accessError && cluster?.state !== 'disconnected') {
    out.push({ tone: 'err', title: 'Nothing can be shown to you yet', detail: accessError })
  }
  // A cluster that cannot be reached gets one banner: expired credentials
  // are the likely cause when they have, and the page says the rest.
  if (cluster?.state === 'disconnected') {
    const expired = summary?.expired ? `${credText(summary, now)}. ` : ''
    out.push({
      tone: 'err',
      title: expired ? 'The console’s credentials for this cluster have expired' : 'Cannot reach the Kubernetes API server',
      detail: `${expired}${cluster.message ?? ''} Showing the last known state, since ${fmtTs(cluster.since)}.`.trim(),
      link: pageHref ? { href: pageHref, label: expired ? 'Fix the connection' : 'About the connection' } : undefined,
    })
  } else if (summary?.expired) {
    out.push({ tone: 'err', title: 'The console’s credentials for this cluster have expired', detail: credText(summary, now) + '.', link: pageHref ? { href: pageHref, label: 'Fix the connection' } : undefined })
  } else if (summary?.expiring) {
    out.push({ tone: 'warn', title: 'The console’s credentials for this cluster expire soon', detail: credText(summary, now) + '.', link: pageHref ? { href: pageHref, label: 'Fix the connection' } : undefined })
  }
  // Each kind that cannot be watched is a warning; while the API server is
  // away or refuses the console, they all say the same thing as the
  // banner above, so they wait for it to come back.
  if (cluster?.state === 'connected') {
    for (const w of cluster.warnings ?? []) out.push({ tone: 'warn', title: 'Partial data', detail: w })
  }
  if (cluster && cluster.state === 'connected' && !cluster.synced) {
    out.push({ tone: 'info', title: 'Listing nodes and pods', detail: 'The first list of a large cluster can take a few seconds.' })
  }
  return out
}

export function Banners({ cluster, stream, now, summary, pageHref, accessError, alerts = [], alertsHref }: Props) {
  const list = banners(cluster, stream, summary, pageHref, now, accessError)
  // What is failing in the cluster comes after what is wrong with the view
  // of it, and only while the cluster is connected.
  if (cluster?.state === 'connected') {
    for (const a of alerts) list.push({ tone: a.tone, title: a.title, detail: a.detail, items: a.items, mono: true, testid: 'alert-banner', link: alertsHref ? { href: alertsHref, label: 'View alerts →', testid: 'alert-banner-link' } : undefined })
  }
  if (!list.length) return null
  return (
    <div role="status" aria-live="polite" style={{ flex: 'none', display: 'flex', flexDirection: 'column' }}>
      {list.map((b) => (
        <div key={b.title + b.detail} className="rk-banner" data-tone={b.tone} data-testid={b.testid}>
          {b.tone === 'err' ? <AlertIcon /> : <ClockIcon />}
          <strong className="rk-banner-title">{b.title}</strong>
          <span className={'rk-banner-detail' + (b.mono ? ' rk-mono' : '')} title={b.detail}>
            {b.items
              ? b.items.map((it, i) => (
                  <span key={it.text}>
                    {i > 0 && '  ·  '}
                    {it.peek ? (
                      <a href={peekClickHash(it.peek)} data-peek={it.peek} style={{ color: 'inherit' }} data-testid="alert-banner-volume">
                        {it.text}
                      </a>
                    ) : (
                      it.text
                    )}
                  </span>
                ))
              : b.detail}
          </span>
          {b.link && (
            <a href={b.link.href} data-testid={b.link.testid ?? 'banner-link'}>
              {b.link.label}
            </a>
          )}
        </div>
      ))}
    </div>
  )
}
