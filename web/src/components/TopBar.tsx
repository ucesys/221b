import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { signOut } from '../api/auth'
import type { ClusterInfo, Counts, Me, Settings } from '../api/types'
import type { StreamState } from '../api/live'
import { refreshLabel } from '../api/settings'
import { connection } from '../lib/connection'
import { fmtTs } from '../lib/format'
import { ROLE_NAME, topRole } from '../lib/admin'
import { adminHref, HOME, tabHref, TABS, type Route, type Tab } from '../lib/router'
import { openAdmin, pagesFor } from './AdminPage'
import { ClusterMenu } from './ClusterMenu'
import { Chevron, GearIcon, Logo, MoonIcon, SunIcon } from './Icons'

export type Theme = 'light' | 'dark'

interface Props {
  cluster: ClusterInfo | null
  counts: Counts
  /** The tone of a tab's count, when it is not muted: the Alerts tab's. */
  tones?: Record<string, string>
  /** The tone of a tab's name: the Alerts tab's while it has alerts, acknowledged or not. */
  labelTones?: Record<string, string | undefined>
  /** The open tab; null on a page that is not a tab, such as a cluster's. */
  tab: Tab | null
  stream: StreamState
  updatedAt: Date | null
  refreshedAt: Date | null
  settings: Settings | null
  theme: Theme
  onTheme: (t: Theme) => void
  /** Who is signed in; null while unknown. */
  me: Me | null
  route: Route
  /** Opens the connection editor for a cluster, or for a new one (null). */
  onEditCluster: (id: string | null) => void
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

/**
 * A small ring that drains as the next refresh comes nearer, next to "Live":
 * full at a refresh, empty when the next is due, one smooth sweep between
 * them. Each refresh starts a new sweep; the first starts where the
 * countdown already is.
 */
function RefreshRing({ left, every, refreshedAt, now }: { left: number; every: number; refreshedAt: Date; now: number }) {
  const at = refreshedAt.getTime()
  const [first] = useState(at)
  const elapsed = at === first ? Math.min(every, Math.max(0, (now - at) / 1000)) : 0
  return (
    <svg className="rk-ring" width={18} height={18} viewBox="0 0 16 16" aria-hidden="true" data-testid="refresh-ring" data-left={left}>
      <circle cx={8} cy={8} r={6} fill="none" stroke="var(--line2)" strokeWidth={2.2} />
      <RingArc key={at} every={every} elapsed={elapsed} />
    </svg>
  )
}

/** One sweep of the ring, from the refresh it was drawn for. */
function RingArc({ every, elapsed }: { every: number; elapsed: number }) {
  // Where the sweep started is fixed when it is drawn; the clock's ticks must not move it.
  const [start] = useState(elapsed)
  return (
    <circle
      className="rk-ring-arc"
      cx={8}
      cy={8}
      r={6}
      fill="none"
      stroke="var(--accent)"
      strokeWidth={2.2}
      strokeLinecap="round"
      pathLength={100}
      strokeDasharray="100 100"
      transform="rotate(-90 8 8)"
      style={{ animationDuration: `${every}s`, animationDelay: `${-start}s`, ['--steps' as string]: Math.max(1, Math.round(every)) }}
    />
  )
}

/** The first letters of a person's name, for the account button. */
export function initials(me: Me): string {
  const name = (me.user.name || me.user.username || '?').trim()
  const parts = name.split(/[\s._@-]+/).filter(Boolean)
  const first = parts[0]?.[0] ?? '?'
  const second = parts.length > 1 ? (parts[1]?.[0] ?? '') : ''
  return (first + second).toUpperCase()
}

/** The theme choice, personal and kept in this browser (RB-47). */
function ThemeChoice({ theme, onTheme }: { theme: Theme; onTheme: (t: Theme) => void }) {
  return (
    <div style={{ padding: '6px 8px 8px', display: 'flex', flexDirection: 'column', gap: 6 }}>
      <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>Theme</span>
      <div className="rk-seg" role="group" aria-label="Theme" style={{ alignSelf: 'flex-start' }}>
        {(['light', 'dark'] as const).map((t) => (
          <button key={t} aria-pressed={theme === t} onClick={() => onTheme(t)} data-testid={`theme-${t}`}>
            {t === 'light' ? <SunIcon /> : <MoonIcon />}
            {t === 'light' ? 'Light' : 'Dark'}
          </button>
        ))}
      </div>
      <span className="rk-fhint">For this browser.</span>
    </div>
  )
}

/** The name of the highest role a person holds, for the account menu. */
export function roleLabel(me: Me): string {
  const top = topRole((me.roles ?? []).map((role) => ({ role })))
  if (top) return ROLE_NAME[top]
  return me.admin ? ROLE_NAME.superAdmin : 'No role'
}

/** The account button and its menu: who is signed in, their role, the theme, and sign-out. */
function AccountMenu({ me, theme, onTheme }: { me: Me; theme: Theme; onTheme: (t: Theme) => void }) {
  const [open, setOpen] = useState(false)
  const button = useRef<HTMLButtonElement>(null)
  if (me.mode === 'none') {
    return (
      <>
        <span className="rk-tag" data-tone="warn" title="Sign-in is off on this console: everyone who reaches it is an administrator. Turn it on in the chart (auth.mode) before sharing it." data-testid="signin-off">
          Sign-in off
        </span>
        <button className="rk-icon-btn" aria-label={theme === 'light' ? 'Switch to the dark theme' : 'Switch to the light theme'} title="Theme, for this browser" onClick={() => onTheme(theme === 'light' ? 'dark' : 'light')} data-testid="theme-toggle">
          {theme === 'light' ? <MoonIcon /> : <SunIcon />}
        </button>
      </>
    )
  }
  const role = roleLabel(me)
  const label = me.user.name || me.user.username
  return (
    <div
      className="rk-dd"
      style={{ position: 'relative', display: 'flex', alignItems: 'center' }}
      onKeyDown={(e) => {
        if (e.key === 'Escape' && open) {
          e.stopPropagation()
          setOpen(false)
          button.current?.focus()
        }
      }}
    >
      <button ref={button} className="rk-avatar-btn" aria-haspopup="menu" aria-expanded={open} aria-label={`Account: ${label}, ${role}`} title={`${label} · ${role}`} onClick={() => setOpen(!open)} data-testid="account">
        {initials(me)}
      </button>
      {open && (
        <>
          <button className="rk-ddcatch" tabIndex={-1} aria-hidden="true" onClick={() => setOpen(false)} />
          <div className="rk-ddmenu" role="menu" aria-label="Account" style={{ left: 'auto', right: 0, minWidth: 240 }}>
            <div className="rk-ddhead" style={{ flexDirection: 'column', alignItems: 'flex-start', gap: 2 }}>
              <span style={{ fontSize: 13, fontWeight: 600, color: 'var(--fg)' }} data-testid="account-name">
                {label}
              </span>
              {me.user.email && me.user.email !== label && (
                <span className="rk-mono" style={{ fontSize: 11.5, color: 'var(--fg3)' }}>
                  {me.user.email}
                </span>
              )}
              <span style={{ display: 'flex', gap: 6, alignItems: 'center', marginTop: 4 }}>
                <span className="rk-tag" data-tone={me.admin ? 'info' : 'muted'} data-testid="account-role">
                  {role}
                </span>
                <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{me.method === 'oidc' ? `signed in with ${me.provider ?? 'OIDC'}` : me.method === 'ldap' ? 'signed in with LDAP' : 'local account'}</span>
              </span>
            </div>
            {me.user.groups && me.user.groups.length > 0 && (
              <div style={{ padding: '4px 8px 6px', fontSize: 11.5, color: 'var(--fg3)' }} title={me.user.groups.join('\n')}>
                {me.user.groups.length === 1 ? 'Group ' : 'Groups '}
                <span className="rk-mono">{me.user.groups.slice(0, 3).join(', ')}</span>
                {me.user.groups.length > 3 ? ` and ${me.user.groups.length - 3} more` : ''}
              </div>
            )}
            <div className="rk-ddsep" />
            <ThemeChoice theme={theme} onTheme={onTheme} />
            <div className="rk-ddsep" />
            <button className="rk-ddopt" role="menuitem" onClick={() => void signOut()} data-testid="signout">
              Sign out
            </button>
          </div>
        </>
      )}
    </div>
  )
}

export function TopBar({ cluster, counts, tones, labelTones, tab, stream, updatedAt, refreshedAt, settings, theme, onTheme, me, route, onEditCluster }: Props) {
  const conn = connection(cluster, stream)
  const live = stream === 'open' && cluster?.state === 'connected'
  // A clock for the countdown to the next refresh, once a second.
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!live) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [live])
  const every = settings?.refreshSeconds ?? 0
  // A new rate starts a new interval on the server from the moment it is
  // set (live.Store.SetSettings), and the browser hears of it at that
  // moment: the countdown and the ring start again then, at the new speed,
  // rather than finishing the old interval first.
  const [rate, setRate] = useState({ every, at: 0 })
  if (rate.every !== every) setRate({ every, at: rate.every ? now : 0 })
  const since = refreshedAt && rate.at > refreshedAt.getTime() ? new Date(rate.at) : refreshedAt
  const left = live ? secondsToRefresh(since, every, now) : null
  const liveTip = [
    settings ? `Refreshes every ${refreshLabel(settings.refreshSeconds)}, for every open console at once.` : '',
    left !== null ? `Next refresh in ${left} s.` : '',
    refreshedAt ? `Last refresh ${fmtTs(refreshedAt, { seconds: true })}.` : '',
    updatedAt ? `Last change ${fmtTs(updatedAt, { seconds: true })}.` : '',
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
  // Only Alerts carries a count in the bar; the other tabs say theirs on the page.
  const countOf = (id: Tab) => (id === 'alerts' ? counts[id] : undefined)

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

  const plan = navPlan(ids, tab ?? ids[0]!, measure)
  const inMenu = tabs.filter((t) => !plan.vis.includes(t.id))
  const moreTab = plan.moreAs ? tabs.find((t) => t.id === plan.moreAs) : undefined
  const hasMore = inMenu.length > 0

  return (
    <header
      className="rk-hdr"
      style={{ flex: 'none', position: 'relative', display: 'flex', flexWrap: 'wrap', alignItems: 'stretch', columnGap: 24, padding: '0 20px', background: 'var(--surface)', borderBottom: '1px solid var(--line)' }}
    >
      <div className="rk-brand" style={{ flex: 'none', display: 'flex', alignItems: 'center', gap: 10, minWidth: 0, minHeight: 52 }}>
        <a className="rk-home" href={HOME} aria-label="Home: every cluster" title="Home: every cluster at a glance" aria-current={route.view === 'home' ? 'page' : undefined} data-testid="home-link">
          <Logo />
        </a>
        <ClusterMenu cluster={cluster} stream={stream} route={route} me={me} onEdit={onEditCluster} />
      </div>
      <nav ref={nav} className="rk-views" aria-label="Views" style={{ flex: '1 1 0', minWidth: 0, display: 'flex', alignItems: 'stretch' }}>
        <div className="rk-tabs">
          {tabs
            .filter((t) => plan.vis.includes(t.id))
            .map((t) => (
              <a key={t.id} className="rk-tab" href={tabHref(t.id)} aria-current={tab === t.id ? 'page' : undefined} title={t.tip} data-tab={t.id} data-tone={labelTones?.[t.id]}>
                {t.label}
                {countOf(t.id) !== undefined && (
                  <span className="rk-count" data-tone={tones?.[t.id] ?? 'muted'}>
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
                <span className="rk-count" data-tone={tones?.[moreTab.id] ?? 'muted'}>
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
                    <a key={t.id} className="rk-ddopt" role="menuitem" href={tabHref(t.id)} aria-current={tab === t.id ? 'page' : undefined} title={t.tip} onClick={() => setMoreOpen(false)} data-tab={t.id} data-tone={labelTones?.[t.id]}>
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
        <span style={{ fontSize: 12, color: 'var(--fg3)', whiteSpace: 'nowrap', display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'default', userSelect: 'none' }} title={liveTip || undefined} data-testid="live">
          {left !== null && since && <RefreshRing left={left} every={every} refreshedAt={since} now={now} />}
          {live ? 'Live' : conn.label}
        </span>
        {me && pagesFor(me).length > 0 && (
          // Administration: roles, permissions, users and settings, behind
          // the gear (the Settings page is one of its pages).
          <a
            className="rk-icon-btn"
            href={adminHref(pagesFor(me)[0]!)}
            aria-current={route.view === 'admin' ? 'page' : undefined}
            aria-label="Administration"
            title="Administration: roles, permissions, users and settings"
            onClick={(e) => {
              e.preventDefault()
              openAdmin(pagesFor(me)[0]!)
            }}
            data-testid="admin-link"
          >
            <GearIcon />
          </a>
        )}
        {me && <AccountMenu me={me} theme={theme} onTheme={onTheme} />}
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
