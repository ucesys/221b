import { useCallback, useEffect, useState } from 'react'
import { useMe } from './api/auth'
import { useClusters, type ClusterSummary } from './api/clusters'
import { LiveContext, useLiveMeta, useLiveStore, useView } from './api/live'
import type { AccessRow, AlertRow, PolicyRow } from './api/types'
import { AccessTab } from './components/AccessTab'
import { NetworksTab } from './components/NetworksTab'
import { PeekPane } from './components/PeekPane'
import { PoliciesTab } from './components/PoliciesTab'
import { StorageTab } from './components/StorageTab'
import { AlertsTab } from './components/AlertsTab'
import { Banners } from './components/Banners'
import { ClusterEditor } from './components/ClusterEditor'
import { ClusterPage } from './components/ClusterPage'
import { FleetsTab } from './components/FleetsTab'
import { HelmTab } from './components/HelmTab'
import { HomePage } from './components/HomePage'
import { NamespacesTab } from './components/NamespacesTab'
import { NodesTab } from './components/NodesTab'
import { PodsTab } from './components/PodsTab'
import { ServicesTab } from './components/ServicesTab'
import { AdminPageView } from './components/AdminPage'
import { TipLayer } from './components/TipLayer'
import { Toast } from './components/Toast'
import { TopBar, type Theme } from './components/TopBar'
import { badgeOf, bannersOf, tabTone } from './lib/alerts'
import { accessBadge } from './lib/rbac'
import { findings as policyFindings, flagOf as policyFlag, modelOf as policyModel } from './lib/policies'
import { adminHref, apiPath, clusterHref, clusterPageHref, currentRoute, FIRST_TAB, HOME, href, LOCAL, navigate, TABS, useRoute, withParams } from './lib/router'
import { askSherlock, useSherlock } from './api/sherlock'
import { sendToWatson, useWatson, WatsonError, type Send } from './api/watson'
import { canPopOut, clusterTroubleshootQuestion, focusPopup, onPop, popOut, post, reviewQuestion, usePopped, WINDOW_VIEWS, type Pane } from './lib/sherlock'
import { openTarget, SherlockContext, SherlockPane } from './components/Sherlock'
import { Dock, PoppedOutFab, type DockMode } from './components/Dock'
import { autoRunReads, WatsonContext, watsonOff, WatsonPane } from './components/Watson'
import { askContext, useUIControl } from './lib/uiWindow'
import { ViewAnnouncer } from './components/ViewAnnouncer'
import { AssistantWindow } from './components/AssistantWindow'
import { readJSON, writeJSON } from './lib/storage'
import { setTimeFormat } from './lib/format'

const THEME_KEY = '221b.theme'

function initialTheme(): Theme {
  const saved = readJSON<Theme | null>(THEME_KEY, null)
  if (saved === 'light' || saved === 'dark') return saved
  try {
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  } catch {
    return 'light'
  }
}

/** Ticks once a minute so ages stay current without a change from the server. */
function useNow(every = 60_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), every)
    return () => clearInterval(t)
  }, [every])
  return now
}

export function App() {
  const route = useRoute()
  // Sherlock and Watson in their window have no tabs (P5-08, P6-09).
  if ((WINDOW_VIEWS as readonly string[]).includes(route.view)) return <AssistantShell />
  return <Console />
}

function Console() {
  const route = useRoute()
  // One stream per cluster: opening another cluster opens its own (P3-09).
  const store = useLiveStore(apiPath('events', route.cluster))
  return (
    <LiveContext.Provider value={store}>
      <Shell />
    </LiveContext.Provider>
  )
}

/** Sherlock's and Watson's own window: the panels alone, in the console's theme. */
function AssistantShell() {
  const route = useRoute()
  const { clusters } = useClusters()
  const [theme] = useState<Theme>(initialTheme)
  const [toast, setToast] = useState('')
  const clearToast = useCallback(() => setToast(''), [])
  useEffect(() => {
    document.documentElement.style.colorScheme = theme
    document.body.style.background = theme === 'dark' ? '#0D1013' : '#F2F3F5'
  }, [theme])
  const name = clusters.find((c) => c.id === route.cluster)?.name ?? (route.cluster === LOCAL ? '' : route.cluster)
  return (
    <div className="rk" data-theme={theme} data-accent="teal" data-density="comfortable" data-dock="off" style={{ minHeight: '100vh', background: 'var(--bg)', color: 'var(--fg)', fontFamily: 'var(--font)', fontSize: 13, lineHeight: 1.45, WebkitFontSmoothing: 'antialiased' }}>
      <AssistantWindow cluster={route.cluster} clusterName={name} onToast={setToast} />
      {toast && <Toast message={toast} onDone={clearToast} />}
    </div>
  )
}

const DOCK_KEY = '221b.dock'

interface DockState {
  mode: DockMode
  h: number
  /** Sherlock's share of the width, and the panel a narrow screen shows. */
  split?: number
  pane?: Pane
}

/** The connection editor's subject: a cluster's id, null for a new one, undefined when closed. */
type Editing = string | null | undefined

function Shell() {
  const live = useLiveMeta()
  // Every date and time follows the format the settings carry.
  setTimeFormat(live.settings?.timeFormat)
  const route = useRoute()
  const now = useNow()
  const me = useMe()
  const { clusters, loaded: clustersLoaded } = useClusters()
  const [theme, setTheme] = useState<Theme>(initialTheme)
  const [editing, setEditing] = useState<Editing>(undefined)
  const [toast, setToast] = useState('')
  const clearToast = useCallback(() => setToast(''), [])
  const closeEditor = useCallback(() => setEditing(undefined), [])

  const changeTheme = (t: Theme) => {
    setTheme(t)
    writeJSON(THEME_KEY, t)
  }

  const summary = clusters.find((c) => c.id === route.cluster)
  const editOrig = editing ? clusters.find((c) => c.id === editing) : undefined
  // A cluster whose connection could not even start has no data: its page
  // says why. One that is not there at all gets the same page's notice.
  const view = route.view === 'home' ? 'home' : route.view === 'admin' ? 'admin' : route.view === 'cluster' || (clustersLoaded && (!summary || summary.state === 'failed')) ? 'cluster' : 'tabs'
  // The Helm tab needs Secrets; with them off it is not offered.
  const tab = route.tab === 'helm' && live.cluster?.secrets === 'off' ? FIRST_TAB : route.tab
  const tabLabel = TABS.find((t) => t.id === tab)?.label ?? 'Alerts'
  const clusterName = summary?.name ?? live.cluster?.name ?? ''

  useEffect(() => {
    document.title = view === 'home' ? 'Clusters · 221B' : view === 'admin' ? 'Administration · 221B' : clusterName ? `${clusterName} · ${view === 'cluster' ? 'Connection' : tabLabel} · 221B` : '221B'
    document.documentElement.style.colorScheme = theme
    document.body.style.background = theme === 'dark' ? '#0D1013' : '#F2F3F5'
  }, [clusterName, tabLabel, theme, view])

  const onSaved = (c: ClusterSummary, added: boolean) => {
    setEditing(undefined)
    if (added) {
      setToast(`Added ${c.name}.`)
      navigate(clusterHref(c.id, FIRST_TAB))
    } else {
      setToast(`Saved ${c.name}.${c.state === 'failed' ? ' It cannot start: ' + (c.message ?? '') : ''}`)
    }
  }
  const onRemoved = (c: ClusterSummary) => {
    setEditing(undefined)
    setToast(`Removed ${c.name} and its credentials.`)
    if (route.cluster === c.id) navigate(HOME)
  }

  // Every tab shows what the Alerts tab has (P4-07), and the bar counts it.
  const alerts = useView<AlertRow>('alerts')
  const alertBadge = badgeOf(alerts.rows)
  const alertTab = alerts.loaded ? tabTone(alerts.rows) : undefined
  const fleetDefs = live.settings?.fleets?.[route.cluster] ?? []
  // The Access tab counts its findings while it is open (P4-03).
  const access = useView<AccessRow>(tab === 'access' && view === 'tabs' ? 'access' : null)
  const accessCount = accessBadge(access.rows.flatMap((r) => ('findings' in r ? r.findings : [])))
  const policies = useView<PolicyRow>(tab === 'policies' && view === 'tabs' ? 'policies' : null)
  const policyCount = policyFlag(policies.loaded ? policyFindings(policyModel(policies.rows)) : [])
  const counts = { ...live.counts, fleets: fleetDefs.length, ...(alerts.loaded ? { alerts: alertBadge.count } : {}), ...(access.loaded ? { access: accessCount.count } : {}), ...(policies.loaded ? { policies: policyCount.count } : {}) }
  const common = { cluster: live.cluster, now, onToast: setToast, route }

  // Sherlock (Phase 5) and Watson (Phase 6): docked at the bottom of every
  // tab, or in windows of their own (P5-08, P6-09).
  const sherlock = useSherlock(route.cluster)
  // Kept on the settings page too, so going there and back does not
  // reconnect the shared stream (ADR-56).
  const watson = useWatson(route.cluster, view !== 'cluster' || !!summary)
  const popped = usePopped()
  // Sherlock's UI actions run in this window (P6U-08).
  useUIControl(route.cluster, sherlock.conv)
  const [dock, setDockState] = useState<DockState>(() => {
    const d = readJSON<DockState | null>(DOCK_KEY, null)
    return d && (d.mode === 'open' || d.mode === 'min' || d.mode === 'max') && typeof d.h === 'number' ? d : { mode: 'open', h: 300 }
  })
  const setDock = useCallback((d: DockState) => {
    setDockState(d)
    writeJSON(DOCK_KEY, { ...d, mode: d.mode === 'max' ? 'open' : d.mode })
  }, [])
  const sherlockOn = !!sherlock.status?.enabled
  // Watson shows only beside Sherlock: where Sherlock is off, so is Watson.
  const watsonOn = sherlockOn && !!watson.status?.available
  // Both leave the main window together, for one window of their own.
  const sherlockDocked = sherlockOn && !popped
  const watsonDocked = watsonOn && !popped
  // The same on the home page and a cluster's page as on the tabs; the
  // Administration area has its own Back and Escape, so it leaves them.
  const dockShown = view !== 'admin' && (sherlockDocked || watsonDocked)
  useEffect(() => {
    if (popped) post({ type: 'cluster', cluster: route.cluster })
  }, [popped, route.cluster])
  useEffect(
    () =>
      onPop((m) => {
        if (m.type !== 'open') return
        const here = currentRoute()
        const hash = m.cluster === here.cluster && here.view === 'tabs' ? openTarget(m.target) : 'peek' in m.target ? clusterHref(m.cluster, FIRST_TAB, [], { peek: m.target.peek }) : m.target.href
        if (hash) navigate(hash)
        window.focus()
      }),
    [],
  )
  const sherlockApi = {
    available: !!sherlock.status?.available && view === 'tabs',
    ask: (q: string, over?: boolean) => {
      const away = popped
      if (!away && (over || dock.mode === 'min')) setDock({ ...dock, mode: over ? 'max' : 'open', pane: 'sherlock' })
      else if (!away) setDock({ ...dock, pane: 'sherlock' })
      askContext()
        .then((ctx) => askSherlock(route.cluster, q, ctx))
        .then(() => {
          if (away) focusPopup()
        })
        .catch((e: unknown) => setToast(e instanceof Error ? e.message : String(e)))
    },
  }
  // Buttons and Sherlock hand commands to Watson (P6-06): the panel opens,
  // or Watson's window comes forward, with the card to approve. Wherever
  // the dock is, so an answer read on the home page can be sent too.
  const watsonApi = {
    available: watsonOn && dockShown,
    readOnly: !!watson.status?.readOnly,
    how: watson.status?.how,
    off: watsonOff({ installed: !!watson.status?.available, sherlockOn, admin: !!sherlock.status?.admin, settingsHref: adminHref('settings', ['sherlock']) }),
    send: (command: string, o: Send = {}) => {
      const away = popped
      if (!away) setDock({ ...dock, mode: o.over ? 'max' : dock.mode === 'min' ? 'open' : dock.mode, pane: 'watson' })
      sendToWatson(route.cluster, command, { run: autoRunReads(), ...o })
        .then(() => {
          if (away) focusPopup()
        })
        .catch((e: unknown) => setToast(e instanceof WatsonError ? e.message : String(e)))
    },
  }
  // A home card's Audit button (Phase 6 review) opens the cluster with
  // ?ask=review, its Troubleshoot button with ?ask=troubleshoot: Sherlock
  // asks for the review or the troubleshooting once it can.
  const askFor = view === 'tabs' ? route.params.get('ask') : null
  const reviewAsked = askFor === 'review' || askFor === 'troubleshoot'
  const reviewReady = reviewAsked && sherlockApi.available && clusterName !== ''
  const reviewRefused = reviewAsked && !!sherlock.status && !sherlock.status.available
  useEffect(() => {
    if (!reviewReady && !reviewRefused) return
    navigate(withParams(currentRoute(), { ask: undefined }), true)
    // Refused (Sherlock went off since the home page showed the button), the
    // dock says why.
    if (reviewReady) sherlockApi.ask(askFor === 'troubleshoot' ? clusterTroubleshootQuestion(clusterName) : reviewQuestion(clusterName), true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reviewReady, reviewRefused])
  const popOutNow = () => {
    if (popOut(route.cluster)) setDock({ ...dock, mode: dock.mode === 'max' ? 'open' : dock.mode })
    else setToast('The browser did not open a new window. Allow pop-ups for the console to move Sherlock and Watson to a window of their own.')
  }
  const waiting = watson.items.filter((x) => x.status === 'pending' && x.kind === 'change' && x.canApprove).length
  const busy = !!sherlock.conv?.busy || watson.items.some((x) => x.status === 'running' || x.status === 'queued')
  return (
    <SherlockContext.Provider value={sherlockApi}>
    <WatsonContext.Provider value={watsonApi}>
    <div
      className="rk"
      data-theme={theme}
      data-accent="teal"
      data-density="comfortable"
      data-dock={dockShown ? dock.mode : 'off'}
      style={{ ['--dock-h' as string]: `${dock.h}px`, minHeight: '100vh', display: 'flex', flexDirection: 'column', background: 'var(--bg)', color: 'var(--fg)', fontFamily: 'var(--font)', fontSize: 13, lineHeight: 1.45, WebkitFontSmoothing: 'antialiased' }}
    >
      <TopBar cluster={live.cluster} counts={counts} labelTones={{ alerts: alertTab }} tones={{ alerts: alertBadge.tone, access: accessCount.tone, policies: policyCount.tone === 'info' ? 'muted' : policyCount.tone }} tab={view === 'tabs' ? tab : null} stream={live.stream} updatedAt={live.updatedAt} refreshedAt={live.refreshedAt} settings={live.settings} theme={theme} onTheme={changeTheme} me={me} route={route} onEditCluster={(id) => setEditing(id)} />
      {view !== 'home' && view !== 'admin' && <Banners cluster={live.cluster} stream={live.stream} now={now} summary={summary ?? null} pageHref={view === 'tabs' ? clusterPageHref(route.cluster) : undefined} accessError={live.accessError} alerts={view === 'tabs' && tab !== 'alerts' ? bannersOf(alerts.rows, now) : []} alertsHref={href('alerts')} />}
      {view === 'home' ? (
        <HomePage me={me} onAdd={() => setEditing(null)} onEdit={(id) => setEditing(id)} />
      ) : view === 'admin' ? (
        <AdminPageView route={route} me={me} now={now} cluster={live.cluster} onToast={setToast} settings={live.settings} refreshedAt={live.refreshedAt} />
      ) : view === 'cluster' ? (
        <ClusterPage route={route} me={me} now={now} onEdit={(id) => setEditing(id)} />
      ) : (
        <div className="rk-main" style={{ display: 'flex', flexWrap: 'wrap', flex: '1 1 auto', alignItems: 'stretch' }}>
          {tab === 'nodes' && <NodesTab {...common} />}
          {tab === 'fleets' && <FleetsTab {...common} me={me} />}
          {tab === 'alerts' && <AlertsTab {...common} />}
          {tab === 'pods' && <PodsTab {...common} />}
          {tab === 'namespaces' && <NamespacesTab {...common} />}
          {tab === 'services' && <ServicesTab {...common} />}
          {tab === 'helm' && <HelmTab {...common} />}
          {tab === 'access' && <AccessTab {...common} />}
          {tab === 'networks' && <NetworksTab {...common} />}
          {tab === 'storage' && <StorageTab {...common} />}
          {tab === 'policies' && <PoliciesTab {...common} />}
        </div>
      )}
      {dockShown && (
        <Dock
          mode={dock.mode}
          onMode={(mode) => setDock({ ...dock, mode })}
          height={dock.h}
          onHeight={(h) => setDock({ ...dock, h })}
          split={dock.split ?? 0.5}
          onSplit={(split) => setDock({ ...dock, split })}
          pane={dock.pane ?? 'sherlock'}
          onPane={(pane) => setDock({ ...dock, pane })}
          sherlock={sherlockDocked ? (controls, switcher) => <SherlockPane cluster={route.cluster} clusterName={clusterName} state={sherlock} onToast={setToast} controls={controls} switcher={switcher} /> : null}
          watson={watsonDocked ? (controls, switcher) => <WatsonPane cluster={route.cluster} clusterName={clusterName} state={watson} onToast={setToast} controls={controls} switcher={switcher} /> : null}
          onPopOut={canPopOut() ? popOutNow : undefined}
          waiting={waiting}
          busy={busy}
        />
      )}
      {view !== 'admin' && popped && <PoppedOutFab onFocus={() => focusPopup()} onDock={() => post({ type: 'close' })} />}
      {view === 'tabs' && <PeekPane route={route} cluster={live.cluster} now={now} onToast={setToast} />}
      {popped && <ViewAnnouncer />}
      {editing === null && <ClusterEditor key="+" orig={null} now={now} onClose={closeEditor} onSaved={onSaved} onRemoved={onRemoved} />}
      {editing && editOrig && <ClusterEditor key={editing} orig={editOrig} now={now} onClose={closeEditor} onSaved={onSaved} onRemoved={onRemoved} />}
      {toast && <Toast message={toast} onDone={clearToast} />}
      <TipLayer />
    </div>
    </WatsonContext.Provider>
    </SherlockContext.Provider>
  )
}
