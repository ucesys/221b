import type { ClusterInfo, NodeRow, Tone } from '../api/types'
import type { StreamState } from '../api/live'
import { fmtClock, fmtTs } from '../lib/format'
import { Logo, MoonIcon, SunIcon } from './Icons'

export type Theme = 'light' | 'dark'

interface Props {
  cluster: ClusterInfo | null
  rows: NodeRow[]
  stream: StreamState
  updatedAt: Date | null
  theme: Theme
  onTheme: (t: Theme) => void
}

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

export function TopBar({ cluster, rows, stream, updatedAt, theme, onTheme }: Props) {
  const conn = connection(cluster, stream)
  const errors = rows.filter((r) => r.errors > 0).length
  const warnings = rows.filter((r) => !r.errors && r.warnings > 0).length
  // As in the prototype, the Nodes count stays neutral; problems show in the
  // table, and in the Alerts tab once it exists.
  const countTone: Tone = 'muted'
  const countTip = errors || warnings ? `${errors ? `${errors} with errors` : ''}${errors && warnings ? ', ' : ''}${warnings ? `${warnings} with warnings` : ''}` : 'No problems'
  const name = cluster?.name ?? '—'
  const version = cluster?.version ?? ''
  const live = stream === 'open' && cluster?.state === 'connected'
  return (
    <header
      className="rk-hdr"
      style={{ flex: 'none', position: 'relative', display: 'flex', flexWrap: 'wrap', alignItems: 'stretch', columnGap: 24, padding: '0 20px', background: 'var(--surface)', borderBottom: '1px solid var(--line)' }}
    >
      <div className="rk-brand" style={{ flex: 'none', display: 'flex', alignItems: 'center', gap: 10, minWidth: 0, minHeight: 52 }}>
        <Logo />
        <div className="rk-clbtn rk-clstatic" title={conn.tip} aria-label={`Cluster ${name}${version ? ', Kubernetes ' + version : ''}: ${conn.label}`} role="status" data-testid="cluster">
          <span className="rk-t" data-tone={conn.tone} style={{ display: 'inline-flex' }}>
            <span className="rk-dot" />
          </span>
          <span className="rk-mono rk-clname">{name}</span>
          {version && (
            <span className="rk-ver rk-mono" style={{ color: 'var(--fg3)' }} data-testid="cluster-version">
              {version}
            </span>
          )}
          <span className="rk-sr" data-testid="cluster-state">
            {conn.label}
          </span>
        </div>
      </div>
      <nav className="rk-views" aria-label="Views" style={{ flex: '1 1 0', minWidth: 0, display: 'flex', alignItems: 'stretch' }}>
        <div className="rk-tabs">
          <button className="rk-tab" aria-current="page" title={`Nodes · ${countTip}`}>
            Nodes{' '}
            <span className="rk-count" data-tone={countTone}>
              {rows.length}
            </span>
          </button>
        </div>
      </nav>
      <div className="rk-meta" style={{ flex: 'none', display: 'flex', alignItems: 'center', gap: 10, minHeight: 52 }}>
        <span style={{ fontSize: 12, color: 'var(--fg3)', whiteSpace: 'nowrap' }} title={updatedAt ? `Last change ${updatedAt.toLocaleString()}` : undefined} data-testid="live">
          {live ? 'Live' : conn.label}
          <span className="rk-asof">
            {' '}
            · updated <span className="rk-mono rk-num">{fmtClock(updatedAt)}</span>
          </span>
        </span>
        <button
          className="rk-icon-btn"
          aria-label={theme === 'dark' ? 'Switch to the light theme' : 'Switch to the dark theme'}
          title={theme === 'dark' ? 'Light theme' : 'Dark theme'}
          onClick={() => onTheme(theme === 'dark' ? 'light' : 'dark')}
        >
          {theme === 'dark' ? <SunIcon /> : <MoonIcon />}
        </button>
      </div>
    </header>
  )
}
