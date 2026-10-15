import { envOf } from '../api/clusters'
import { useHome, type HomeCluster } from '../api/home'
import type { Me } from '../api/types'
import { fmtAge } from '../lib/format'
import { activeAlerts, alertTone, healthOf, podLine, resourceLine, type UseLine } from '../lib/home'
import { clusterHref, clusterPageHref } from '../lib/router'

interface Props {
  now: number
  me: Me | null
  /** Opens the connection editor for a new cluster. */
  onAdd: () => void
}

/** How to read the bars, on hover (asked at the Phase 6 review, in place of a note under them). */
const BAR_LEGEND = 'Light bar: requested. Solid bar: in use. The percentage is what is requested of what is allocatable.'

function Bar({ line, testid, note }: { line: UseLine; testid: string; note?: string }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '64px minmax(0, 1fr)', alignItems: 'center', gap: '4px 10px' }} data-testid={testid} title={[line.tip, line.reqPct > 0 ? BAR_LEGEND : '', note ?? ''].filter(Boolean).join('\n\n')}>
      <span style={{ fontSize: 12, color: 'var(--fg2)' }}>{line.label}</span>
      <span className="rk-bar" style={{ height: 8, borderRadius: 4, width: '100%' }} role="img" aria-label={`${line.label}: ${line.text}`}>
        {line.reqPct > 0 && <span className="rk-req" style={{ width: `${Math.min(100, line.reqPct).toFixed(1)}%` }} />}
        {line.usePct !== null && <span className="rk-use" data-tone={line.useTone} style={{ width: `${Math.min(100, line.usePct).toFixed(1)}%` }} />}
      </span>
      <span />
      <span className="rk-num" style={{ fontSize: 11.5, color: 'var(--fg3)' }} data-testid={`${testid}-text`}>
        {line.text}
      </span>
    </div>
  )
}

function Stat({ label, value, sub, tone, href, testid }: { label: string; value: string | number; sub?: string; tone?: string; href: string; testid: string }) {
  return (
    <a className="rk-card rk-tile rk-homestat" data-tone={tone} href={href} style={{ padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 2, textDecoration: 'none', color: 'inherit' }} data-testid={testid}>
      <span className="rk-num" style={{ fontSize: 20, fontWeight: 600, lineHeight: 1.1 }}>
        {value}
      </span>
      <span style={{ fontSize: 12, color: 'var(--fg2)' }}>{label}</span>
      {sub && <span style={{ fontSize: 11, color: 'var(--fg3)' }}>{sub}</span>}
    </a>
  )
}

function ClusterCard({ c, now }: { c: HomeCluster; now: number }) {
  const env = envOf(c.env)
  const health = healthOf(c)
  const up = c.state === 'connected' && c.synced
  const metrics = c.metrics === 'available'
  const active = activeAlerts(c)
  const at = (tab: Parameters<typeof clusterHref>[1], params: Record<string, string> = {}) => clusterHref(c.id, tab, [], params)
  const nodeTone = c.nodes.total - c.nodes.ready > 0 || c.nodes.errors > 0 ? 'err' : c.nodes.warnings > 0 || c.nodes.cordoned > 0 ? 'warn' : undefined
  const podTone = c.pods.failing > 0 ? 'err' : c.pods.notReady > 0 ? 'warn' : undefined
  const noUsage = metrics ? undefined : `No usage figures: metrics-server ${c.metrics === 'unavailable' ? 'does not answer' : 'has not answered yet'}, so the bars show only what is requested.`
  return (
    <section className="rk-card" style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 14, minWidth: 0 }} aria-labelledby={`home-${c.id}`} data-testid="home-card" data-cluster={c.id} data-health={health.tone}>
      <header style={{ display: 'flex', alignItems: 'flex-start', gap: 10, minWidth: 0 }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0, flex: '1 1 auto' }}>
          <h2 id={`home-${c.id}`} style={{ margin: 0, fontSize: 15, fontWeight: 600, display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <a href={at('alerts')} style={{ color: 'inherit', textDecoration: 'none' }} className="rk-homename" data-testid="home-open">
              {c.name}
            </a>
            <span className="rk-tag" data-tone={env.tone}>
              {env.label}
            </span>
          </h2>
          <span style={{ fontSize: 12, color: 'var(--fg3)' }}>
            {[c.version, c.local ? 'the console’s own cluster' : '', c.refreshedAt ? `refreshed ${fmtAge(c.refreshedAt, now)} ago` : ''].filter(Boolean).join(' · ')}
          </span>
        </div>
        <span className="rk-pill" data-tone={health.tone} title={health.reasons.join('\n') || undefined} data-testid="home-health">
          {health.text}
        </span>
      </header>
      {health.reasons.length > 0 && (
        <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5, color: 'var(--fg2)', display: 'flex', flexDirection: 'column', gap: 2 }} data-testid="home-reasons">
          {health.reasons.slice(0, 4).map((r) => (
            <li key={r}>{r}</li>
          ))}
        </ul>
      )}
      {up ? (
        <>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(96px, 100%), 1fr))', gap: 8 }}>
            <Stat label="Nodes ready" value={`${c.nodes.ready}/${c.nodes.total}`} sub={[c.nodes.controlPlane ? `${c.nodes.controlPlane} control plane` : '', c.nodes.cordoned ? `${c.nodes.cordoned} cordoned` : ''].filter(Boolean).join(' · ') || undefined} tone={nodeTone} href={at('nodes')} testid="home-nodes" />
            <Stat label="Pods" value={c.pods.total} sub={[`${c.pods.running} running`, c.pods.failing ? `${c.pods.failing} failing` : '', c.pods.notReady ? `${c.pods.notReady} not ready` : ''].filter(Boolean).join(' · ')} tone={podTone} href={at('pods')} testid="home-pods" />
            <Stat label={active === 1 ? 'Alert' : 'Alerts'} value={active} sub={c.alerts.acknowledged ? `${c.alerts.acknowledged} acknowledged` : c.alerts.recovered ? `${c.alerts.recovered} recovered` : undefined} tone={alertTone(c)} href={at('alerts')} testid="home-alerts" />
            <Stat label="Namespaces" value={c.namespaces} sub={`${c.services} Service${c.services === 1 ? '' : 's'}`} href={at('namespaces')} testid="home-namespaces" />
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }} aria-label="Resources">
            <Bar line={resourceLine('CPU', c.cpu, metrics)} testid="home-cpu" note={noUsage} />
            <Bar line={resourceLine('Memory', c.memory, metrics)} testid="home-memory" note={noUsage} />
            <Bar line={podLine(c)} testid="home-podroom" />
          </div>
        </>
      ) : (
        <a className="rk-btn" href={clusterPageHref(c.id)} style={{ alignSelf: 'flex-start', height: 30 }}>
          See its connection
        </a>
      )}
    </section>
  )
}

/**
 * The home page and landing page (asked at the Phase 4 review): every
 * cluster the console is set up for, with its health and what its nodes'
 * CPU, memory and pod room hold, as the person may see them.
 */
export function HomePage({ now, me, onAdd }: Props) {
  const { clusters, loaded, error } = useHome()
  const up = clusters.filter((c) => c.state === 'connected')
  const sum = (f: (c: HomeCluster) => number) => clusters.reduce((n, c) => n + f(c), 0)
  const bad = clusters.filter((c) => healthOf(c).tone === 'err').length
  const warn = clusters.filter((c) => healthOf(c).tone === 'warn').length
  return (
    <main className="rk-main" style={{ flex: '1 1 auto', display: 'flex', flexDirection: 'column', gap: 16, padding: '20px 24px 40px', background: 'var(--bg)', minWidth: 0 }} data-testid="home">
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, flexWrap: 'wrap' }}>
        <h1 style={{ margin: 0, fontSize: 18, fontWeight: 600 }}>Clusters</h1>
        {loaded && (
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }} data-testid="home-summary">
            {[`${clusters.length} cluster${clusters.length === 1 ? '' : 's'}`, `${up.length} connected`, bad ? `${bad} need${bad === 1 ? 's' : ''} attention` : '', warn ? `${warn} with warnings` : '', `${sum((c) => c.nodes.total)} nodes`, `${sum((c) => c.pods.total)} pods`].filter(Boolean).join(' · ')}
          </span>
        )}
        <span style={{ flex: '1 1 auto' }} />
        {me?.admin && (
          <button className="rk-btn" onClick={onAdd} style={{ height: 30 }} data-testid="home-add">
            Add a cluster
          </button>
        )}
      </div>
      {error && (
        <div className="rk-card" data-tone="err" style={{ padding: '10px 14px', fontSize: 13, color: 'var(--err)' }} role="alert">
          {error}
        </div>
      )}
      {!loaded ? (
        <p style={{ margin: 0, color: 'var(--fg3)' }}>Loading…</p>
      ) : (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(min(460px, 100%), 1fr))', gap: 14, alignItems: 'start' }} data-testid="home-cards">
          {clusters.map((c) => (
            <ClusterCard key={c.id} c={c} now={now} />
          ))}
        </div>
      )}
    </main>
  )
}
