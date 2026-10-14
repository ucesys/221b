import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { ClusterInfo, Counts, Settings, Tone } from '../api/types'
import type { StreamState } from '../api/live'
import { refreshLabel } from '../api/settings'
import { fmtClock, fmtTs } from '../lib/format'
import { href, TABS, type Tab } from '../lib/router'
import { Chevron, GearIcon, Logo } from './Icons'

export type Theme = 'light' | 'dark'

interface Props {
  cluster: ClusterInfo | null
  counts: Counts
  tab: Tab
  stream: StreamState
  updatedAt: Date | null
  refreshedAt: Date | null
  settings: Settings | null
  settingsOpen: boolean
  onSettings: () => void
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

interface Measure {
  avail: number
  gap: number
  w: Record<string, number>
}

/**
 * Which tabs fit in the bar: every tab, from the left, while there is room
 * for all of them; otherwise those that fit beside a More button, which
 * takes the rest. The open tab, when it is not among them, rides on the More
 * button, which then reads as that tab (P2-01).
 */
export function navPlan(all: Tab[], active: Tab, m: Measure | null): { vis: Tab[]; moreAs: Tab | '' } {
  if (!m) return { vis: all, moreAs: '' }
  const need = (t: Tab) => (m.w[t] ?? 0) + m.gap
  if (all.reduce((n, t) => n + need(t), 0) <= m.avail) return { vis: all, moreAs: '' }
  const room = m.avail - (m.w.more ?? 0)
  let used = 0
  const vis: Tab[] = []
  for (const t of all) {
    if (used + need(t) > room) break
    vis.push(t)
    used += need(t)
  }
  if (!all.includes(active) || vis.includes(active)) return { vis, moreAs: '' }
  const wide = m.w['more:' + active] ?? m.w.more ?? 0
  while (vis.length && used + wide > m.avail) {
    used -= need(vis.pop()!)
  }
  return { vis, moreAs: active }
}

/** Seconds until the next refresh, from the last one and the refresh rate. */
export function secondsToRefresh(refreshedAt: Date | null, every: number, now: number): number | null {
  if (!refreshedAt || every <= 0) return null
  const elapsed = (now - refreshedAt.getTime()) / 1000
  return Math.max(0, Math.ceil(every - elapsed))
}

/** A small ring that drains as the next refresh comes nearer, next to "Live". */
function RefreshRing({ left, every }: { left: number; every: number }) {
  const r = 6
  const c = 2 * Math.PI * r
  const frac = every > 0 ? Math.min(1, left / every) : 0
  return (
    <svg className="rk-ring" width={16} height={16} viewBox="0 0 16 16" aria-hidden="true" data-testid="refresh-ring" data-left={left}>
      <circle cx={8} cy={8} r={r} fill="none" stroke="var(--line)" strokeWidth={2} />
      <circle cx={8} cy={8} r={r} fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeDasharray={`${(c * frac).toFixed(2)} ${c.toFixed(2)}`} transform="rotate(-90 8 8)" />
    </svg>
  )
}

export function TopBar({ cluster, counts, tab, stream, updatedAt, refreshedAt, settings, settingsOpen, onSettings }: Props) {
  const conn = connection(cluster, stream)
  const name = cluster?.name ?? '—'
  const version = cluster?.version ?? ''
  const live = stream === 'open' && cluster?.state === 'connected'
  // A clock for the countdown to the next refresh, once a second.
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!live) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [live])
  const every = settings?.refreshSeconds ?? 0
  const left = live ? secondsToRefresh(refreshedAt, every, now) : null
  const liveTip = [
    settings ? `Refreshes every ${refreshLabel(settings.refreshSeconds)}, for every open console at once.` : '',
    left !== null ? `Next refresh in ${left} s.` : '',
    refreshedAt ? `Last refresh ${refreshedAt.toLocaleString()}.` : '',
    updatedAt ? `Last change ${updatedAt.toLocaleString()}.` : '',
  ]
    .filter(Boolean)
    .join(' ')

  // The Helm tab needs Secrets; without them there is nothing to show.
  const tabs = TABS.filter((t) => t.id !== 'helm' || cluster?.secrets !== 'off')
  const ids = tabs.map((t) => t.id)
  const nav = useRef<HTMLElement>(null)
  const measureEl = useRef<HTMLDivElement>(null)
  const [measure, setMeasure] = useState<Measure | null>(null)
  const [moreOpen, setMoreOpen] = useState(false)
  const moreButton = useRef<HTMLButtonElement>(null)
  const countOf = (id: Tab) => counts[id]

  useLayoutEffect(() => {
    const el = nav.current
    const ms = measureEl.current
    if (!el || !ms) return
    const update = () => {
      const w: Record<string, number> = {}
      const n = ids.length
      const kids = ms.children
      for (let i = 0; i < n; i++) {
        w[ids[i]!] = Math.ceil(kids[i]!.getBoundingClientRect().width)
        w['more:' + ids[i]!] = Math.ceil(kids[n + 1 + i]!.getBoundingClientRect().width)
      }
      w.more = Math.ceil(kids[n]!.getBoundingClientRect().width)
      const gap = parseFloat(getComputedStyle(el).columnGap) || 22
      setMeasure({ avail: el.clientWidth, gap, w })
    }
    update()
    const ro = new ResizeObserver(update)
    ro.observe(el)
    return () => ro.disconnect()
    // Counts change the widths, so measure again when they do.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ids.join(','), JSON.stringify(counts)])

  // Opening another tab closes the menu.
  const [lastTab, setLastTab] = useState(tab)
  if (tab !== lastTab) {
    setLastTab(tab)
    setMoreOpen(false)
  }

  const plan = navPlan(ids, tab, measure)
  const inMenu = tabs.filter((t) => !plan.vis.includes(t.id))
  const moreTab = plan.moreAs ? tabs.find((t) => t.id === plan.moreAs) : undefined
  const hasMore = inMenu.length > 0

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
      <nav ref={nav} className="rk-views" aria-label="Views" style={{ flex: '1 1 0', minWidth: 0, display: 'flex', alignItems: 'stretch' }}>
        <div className="rk-tabs">
          {tabs
            .filter((t) => plan.vis.includes(t.id))
            .map((t) => (
              <a key={t.id} className="rk-tab" href={href(t.id)} aria-current={tab === t.id ? 'page' : undefined} title={t.tip} data-tab={t.id}>
                {t.label}
                {countOf(t.id) !== undefined && (
                  <span className="rk-count" data-tone="muted">
                    {countOf(t.id)}
                  </span>
                )}
              </a>
            ))}
        </div>
        {hasMore && (
          <div
            className="rk-dd"
            style={{ display: 'flex', alignItems: 'stretch' }}
            onKeyDown={(e) => {
              if (e.key === 'Escape' && moreOpen) {
                e.stopPropagation()
                setMoreOpen(false)
                moreButton.current?.focus()
              }
            }}
          >
            <button
              ref={moreButton}
              className="rk-tab"
              aria-haspopup="menu"
              aria-expanded={moreOpen}
              aria-current={moreTab ? 'page' : undefined}
              title={moreTab ? `${moreTab.label}: open. More views…` : 'More views'}
              onClick={() => setMoreOpen(!moreOpen)}
              data-testid="more"
            >
              {moreTab ? moreTab.label : 'More'}
              {moreTab && countOf(moreTab.id) !== undefined && (
                <span className="rk-count" data-tone="muted">
                  {countOf(moreTab.id)}
                </span>
              )}
              <Chevron />
            </button>
            {moreOpen && (
              <>
                <button className="rk-ddcatch" tabIndex={-1} aria-hidden="true" onClick={() => setMoreOpen(false)} />
                <div className="rk-ddmenu" role="menu" aria-label="More views">
                  {inMenu.map((t) => (
                    <a key={t.id} className="rk-ddopt" role="menuitem" href={href(t.id)} aria-current={tab === t.id ? 'page' : undefined} title={t.tip} onClick={() => setMoreOpen(false)} data-tab={t.id}>
                      <span style={{ flex: '1 1 auto' }}>{t.label}</span>
                      {countOf(t.id) !== undefined && <span className="rk-cnt">{countOf(t.id)}</span>}
                    </a>
                  ))}
                </div>
              </>
            )}
          </div>
        )}
      </nav>
      <div className="rk-meta" style={{ flex: 'none', display: 'flex', alignItems: 'center', gap: 10, minHeight: 52 }}>
        <span style={{ fontSize: 12, color: 'var(--fg3)', whiteSpace: 'nowrap', display: 'inline-flex', alignItems: 'center', gap: 6 }} title={liveTip || undefined} data-testid="live">
          {left !== null && <RefreshRing left={left} every={every} />}
          {live ? 'Live' : conn.label}
          <span className="rk-asof">
            {' '}
            · refreshed <span className="rk-mono rk-num">{fmtClock(refreshedAt ?? updatedAt)}</span>
          </span>
        </span>
        <button className="rk-icon-btn" aria-label="Settings" aria-haspopup="dialog" aria-expanded={settingsOpen} title="Settings: theme and refresh rate" onClick={onSettings}>
          <GearIcon />
        </button>
      </div>
      <div ref={measureEl} className="rk-tabmeasure" aria-hidden="true">
        {tabs.map((t) => (
          <span key={t.id} className="rk-tab">
            {t.label}
            {countOf(t.id) !== undefined && <span className="rk-count">{countOf(t.id)}</span>}
          </span>
        ))}
        <span className="rk-tab">
          More
          <Chevron />
        </span>
        {tabs.map((t) => (
          <span key={'more:' + t.id} className="rk-tab">
            {t.label}
            {countOf(t.id) !== undefined && <span className="rk-count">{countOf(t.id)}</span>}
            <Chevron />
          </span>
        ))}
      </div>
    </header>
  )
}
