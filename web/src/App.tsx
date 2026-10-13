import { useCallback, useEffect, useState } from 'react'
import { useLive } from './api/live'
import { Banners } from './components/Banners'
import { NodesTab } from './components/NodesTab'
import { Toast } from './components/Toast'
import { TopBar, type Theme } from './components/TopBar'
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
  const live = useLive()
  const now = useNow()
  const [theme, setTheme] = useState<Theme>(initialTheme)
  const [toast, setToast] = useState('')
  const clearToast = useCallback(() => setToast(''), [])

  const changeTheme = (t: Theme) => {
    setTheme(t)
    writeJSON(THEME_KEY, t)
  }

  useEffect(() => {
    document.title = live.cluster?.name ? `${live.cluster.name} · Nodes · 221B` : '221B'
    document.documentElement.style.colorScheme = theme
    document.body.style.background = theme === 'dark' ? '#0D1013' : '#F2F3F5'
  }, [live.cluster?.name, theme])

  return (
    <div
      className="rk"
      data-theme={theme}
      data-accent="teal"
      data-density="comfortable"
      data-dock="off"
      style={{ minHeight: '100vh', display: 'flex', flexDirection: 'column', background: 'var(--bg)', color: 'var(--fg)', fontFamily: 'var(--font)', fontSize: 13, lineHeight: 1.45, WebkitFontSmoothing: 'antialiased' }}
    >
      <TopBar cluster={live.cluster} rows={live.rows} stream={live.stream} updatedAt={live.updatedAt} theme={theme} onTheme={changeTheme} />
      <Banners cluster={live.cluster} stream={live.stream} />
      <div className="rk-main" style={{ display: 'flex', flexWrap: 'wrap', flex: '1 1 auto', alignItems: 'stretch' }}>
        <main className="rk-pane" style={{ flex: '999 1 560px', minWidth: 0, display: 'flex', flexDirection: 'column' }}>
          <NodesTab rows={live.rows} cluster={live.cluster} unscheduled={live.unscheduled} now={now} onToast={setToast} />
        </main>
      </div>
      {toast && <Toast message={toast} onDone={clearToast} />}
    </div>
  )
}
