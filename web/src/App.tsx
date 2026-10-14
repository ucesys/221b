import { useCallback, useEffect, useState } from 'react'
import { useMe } from './api/auth'
import { LiveContext, useLiveMeta, useLiveStore } from './api/live'
import { Banners } from './components/Banners'
import { HelmTab } from './components/HelmTab'
import { NamespacesTab } from './components/NamespacesTab'
import { NodesTab } from './components/NodesTab'
import { PodsTab } from './components/PodsTab'
import { ServicesTab } from './components/ServicesTab'
import { SettingsDialog } from './components/SettingsDialog'
import { Toast } from './components/Toast'
import { TopBar, type Theme } from './components/TopBar'
import { TABS, useRoute } from './lib/router'
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
  const store = useLiveStore()
  return (
    <LiveContext.Provider value={store}>
      <Shell />
    </LiveContext.Provider>
  )
}

function Shell() {
  const live = useLiveMeta()
  const route = useRoute()
  const now = useNow()
  const me = useMe()
  const [theme, setTheme] = useState<Theme>(initialTheme)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [toast, setToast] = useState('')
  const clearToast = useCallback(() => setToast(''), [])
  const closeSettings = useCallback(() => setSettingsOpen(false), [])

  const changeTheme = (t: Theme) => {
    setTheme(t)
    writeJSON(THEME_KEY, t)
  }

  // The Helm tab needs Secrets; with them off it is not offered.
  const tab = route.tab === 'helm' && live.cluster?.secrets === 'off' ? 'nodes' : route.tab
  const tabLabel = TABS.find((t) => t.id === tab)?.label ?? 'Nodes'

  useEffect(() => {
    document.title = live.cluster?.name ? `${live.cluster.name} · ${tabLabel} · 221B` : '221B'
    document.documentElement.style.colorScheme = theme
    document.body.style.background = theme === 'dark' ? '#0D1013' : '#F2F3F5'
  }, [live.cluster?.name, tabLabel, theme])

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
      <TopBar cluster={live.cluster} counts={live.counts} tab={tab} stream={live.stream} updatedAt={live.updatedAt} refreshedAt={live.refreshedAt} settings={live.settings} settingsOpen={settingsOpen} onSettings={() => setSettingsOpen(true)} me={me} />
      <Banners cluster={live.cluster} stream={live.stream} />
      <div className="rk-main" style={{ display: 'flex', flexWrap: 'wrap', flex: '1 1 auto', alignItems: 'stretch' }}>
        {tab === 'nodes' && <NodesTab {...common} />}
        {tab === 'pods' && <PodsTab {...common} />}
        {tab === 'namespaces' && <NamespacesTab {...common} />}
        {tab === 'services' && <ServicesTab {...common} />}
        {tab === 'helm' && <HelmTab {...common} />}
      </div>
      <SettingsDialog open={settingsOpen} onClose={closeSettings} theme={theme} onTheme={changeTheme} settings={live.settings} refreshedAt={live.refreshedAt} canChange={me?.admin ?? true} />
      {toast && <Toast message={toast} onDone={clearToast} />}
    </div>
  )
}
