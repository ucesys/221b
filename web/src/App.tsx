import { useCallback, useEffect, useState } from 'react'
import { useMe } from './api/auth'
import { useClusters, type ClusterSummary } from './api/clusters'
import { LiveContext, useLiveMeta, useLiveStore, useView } from './api/live'
import type { AccessRow, AlertRow, PolicyRow } from './api/types'
import { AccessTab } from './components/AccessTab'
import { NetworksTab } from './components/NetworksTab'
import { PeekPane } from './components/PeekPane'
import { PoliciesTab } from './components/PoliciesTab'
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
import { SettingsDialog } from './components/SettingsDialog'
import { Toast } from './components/Toast'
import { TopBar, type Theme } from './components/TopBar'
import { badgeOf, bannersOf, tabTone } from './lib/alerts'
import { accessBadge } from './lib/rbac'
import { findings as policyFindings, flagOf as policyFlag, modelOf as policyModel } from './lib/policies'
import { apiPath, clusterHref, clusterPageHref, FIRST_TAB, HOME, href, navigate, TABS, useRoute } from './lib/router'
import { loadPrefs } from './lib/prefs'
import { readJSON, writeJSON } from './lib/storage'

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
  // One stream per cluster: opening another cluster opens its own (P3-09).
  const store = useLiveStore(apiPath('events', route.cluster))
  return (
    <LiveContext.Provider value={store}>
      <Shell />
    </LiveContext.Provider>
  )
}

/** The connection editor's subject: a cluster's id, null for a new one, undefined when closed. */
type Editing = string | null | undefined

function Shell() {
  const live = useLiveMeta()
  const route = useRoute()
  const now = useNow()
  const me = useMe()
  // A signed-in person's own preferences, when the console keeps them with their account.
  const signedIn = !!me && me.mode !== 'none'
  useEffect(() => {
    if (signedIn) void loadPrefs()
  }, [signedIn])
  const { clusters, loaded: clustersLoaded } = useClusters()
  const [theme, setTheme] = useState<Theme>(initialTheme)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [editing, setEditing] = useState<Editing>(undefined)
  const [toast, setToast] = useState('')
  const clearToast = useCallback(() => setToast(''), [])
  const closeSettings = useCallback(() => setSettingsOpen(false), [])
  const closeEditor = useCallback(() => setEditing(undefined), [])

  const changeTheme = (t: Theme) => {
    setTheme(t)
    writeJSON(THEME_KEY, t)
  }

  const summary = clusters.find((c) => c.id === route.cluster)
  const editOrig = editing ? clusters.find((c) => c.id === editing) : undefined
  // A cluster whose connection could not even start has no data: its page
  // says why. One that is not there at all gets the same page's notice.
  const view = route.view === 'home' ? 'home' : route.view === 'cluster' || (clustersLoaded && (!summary || summary.state === 'failed')) ? 'cluster' : 'tabs'
  // The Helm tab needs Secrets; with them off it is not offered.
  const tab = route.tab === 'helm' && live.cluster?.secrets === 'off' ? FIRST_TAB : route.tab
  const tabLabel = TABS.find((t) => t.id === tab)?.label ?? 'Alerts'
  const clusterName = summary?.name ?? live.cluster?.name ?? ''

  useEffect(() => {
    document.title = view === 'home' ? 'Clusters · 221B' : clusterName ? `${clusterName} · ${view === 'cluster' ? 'Connection' : tabLabel} · 221B` : '221B'
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
  return (
    <div
      className="rk"
      data-theme={theme}
      data-accent="teal"
      data-density="comfortable"
      data-dock="off"
      style={{ minHeight: '100vh', display: 'flex', flexDirection: 'column', background: 'var(--bg)', color: 'var(--fg)', fontFamily: 'var(--font)', fontSize: 13, lineHeight: 1.45, WebkitFontSmoothing: 'antialiased' }}
    >
      <TopBar cluster={live.cluster} counts={counts} labelTones={{ alerts: alertTab }} tones={{ alerts: alertBadge.tone, access: accessCount.tone, policies: policyCount.tone === 'info' ? 'muted' : policyCount.tone }} tab={view === 'tabs' ? tab : null} stream={live.stream} updatedAt={live.updatedAt} refreshedAt={live.refreshedAt} settings={live.settings} settingsOpen={settingsOpen} onSettings={() => setSettingsOpen(true)} me={me} route={route} onEditCluster={(id) => setEditing(id)} />
      {view !== 'home' && <Banners cluster={live.cluster} stream={live.stream} now={now} summary={summary ?? null} pageHref={view === 'tabs' ? clusterPageHref(route.cluster) : undefined} accessError={live.accessError} alerts={view === 'tabs' && tab !== 'alerts' ? bannersOf(alerts.rows, now) : []} alertsHref={href('alerts')} />}
      {view === 'home' ? (
        <HomePage now={now} me={me} onAdd={() => setEditing(null)} />
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
          {tab === 'policies' && <PoliciesTab {...common} />}
        </div>
      )}
      {view === 'tabs' && <PeekPane route={route} cluster={live.cluster} now={now} onToast={setToast} />}
      <SettingsDialog open={settingsOpen} onClose={closeSettings} theme={theme} onTheme={changeTheme} settings={live.settings} refreshedAt={live.refreshedAt} canChange={me?.admin ?? true} />
      {editing === null && <ClusterEditor key="+" orig={null} now={now} onClose={closeEditor} onSaved={onSaved} onRemoved={onRemoved} />}
      {editing && editOrig && <ClusterEditor key={editing} orig={editOrig} now={now} onClose={closeEditor} onSaved={onSaved} onRemoved={onRemoved} />}
      {toast && <Toast message={toast} onDone={clearToast} />}
    </div>
  )
}
