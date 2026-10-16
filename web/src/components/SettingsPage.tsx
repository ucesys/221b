import { useEffect, useRef, useState } from 'react'
import { REFRESH_CHOICES, refreshLabel, saveSettings } from '../api/settings'
import type { Settings } from '../api/types'
import { fmtClock } from '../lib/format'
import { HOME, currentRoute, navigate, type Route } from '../lib/router'
import { SherlockSettings } from './SherlockSettings'
import { WatsonSettings } from './WatsonSettings'
import type { Theme } from './TopBar'

export const SETTINGS = '#/settings'

type Section = 'general' | 'sherlock' | 'watson'

const SECTIONS: { id: Section; label: string; hint: string; admin: boolean }[] = [
  { id: 'general', label: 'General', hint: 'Theme and refresh rate', admin: false },
  { id: 'sherlock', label: 'Sherlock', hint: 'AI provider, model, clusters, caps', admin: true },
  { id: 'watson', label: 'Watson', hint: 'Commands and approvals', admin: true },
]

// Where Settings was opened from, to go back to.
let back = HOME

/** Opens the settings page, remembering the page to return to. */
export function openSettings(): void {
  if (currentRoute().view !== 'settings') back = location.hash || HOME
  navigate(SETTINGS)
}

/** Returns to the page Settings was opened from, with focus on the gear. */
export function closeSettings(): void {
  navigate(back)
  back = HOME
  requestAnimationFrame(() => document.querySelector<HTMLElement>('[data-testid="settings-btn"]')?.focus())
}

interface Props {
  route: Route
  theme: Theme
  onTheme: (t: Theme) => void
  settings: Settings | null
  refreshedAt: Date | null
  /** Whether this person may change the shared settings: administrators do (P3-03). */
  canChange: boolean
}

/**
 * Settings, a page of their own (asked at the Phase 6 review): General for
 * everyone (the theme, kept in this browser, and the refresh rate, kept on
 * the server), and Sherlock and Watson for administrators. Back or Escape
 * returns to where it was opened from.
 */
export function SettingsPage({ route, theme, onTheme, settings, refreshedAt, canChange }: Props) {
  const shown = SECTIONS.filter((s) => canChange || !s.admin)
  const asked = route.path[0] as Section | undefined
  const section: Section = shown.some((s) => s.id === asked) ? asked! : 'general'
  const heading = useRef<HTMLHeadingElement>(null)

  useEffect(() => {
    heading.current?.focus()
  }, [section])
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.defaultPrevented) return
      const t = e.target as HTMLElement | null
      // Escape in a field is the field's; elsewhere it leaves the page.
      if (t && (t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || (t.tagName === 'INPUT' && (t as HTMLInputElement).value !== ''))) return
      if (document.querySelector('dialog[open]')) return
      e.preventDefault()
      closeSettings()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const label = SECTIONS.find((s) => s.id === section)!.label
  return (
    <main className="rk-main" style={{ flex: '1 1 auto', display: 'flex', justifyContent: 'center', alignItems: 'flex-start', padding: '24px 20px 48px', background: 'var(--bg)' }} aria-label="Settings" data-testid="settings-page">
      <div style={{ width: 'min(1040px, 100%)', display: 'flex', flexDirection: 'column', gap: 16, minWidth: 0 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <button className="rk-btn" onClick={closeSettings} title="Back (Esc)" data-testid="settings-back">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="m15 18-6-6 6-6" />
            </svg>
            Back
          </button>
          <h1 style={{ margin: 0, fontSize: 20, fontWeight: 600 }}>Settings</h1>
        </div>
        <div style={{ display: 'flex', gap: 20, flexWrap: 'wrap', alignItems: 'flex-start' }}>
          {shown.length > 1 && (
            <nav aria-label="Settings sections" style={{ flex: '0 0 200px', display: 'flex', flexDirection: 'column', gap: 2, position: 'sticky', top: 16 }} className="rk-setnav">
              {shown.map((s) => (
                <a
                  key={s.id}
                  href={`${SETTINGS}/${s.id}`}
                  aria-current={s.id === section ? 'page' : undefined}
                  onClick={(e) => {
                    e.preventDefault()
                    navigate(`${SETTINGS}/${s.id}`, true)
                  }}
                  data-testid="settings-section"
                  data-section={s.id}
                  style={{ display: 'flex', flexDirection: 'column', padding: '8px 12px', borderRadius: 8, textDecoration: 'none', color: 'var(--fg)', background: s.id === section ? 'var(--surface)' : 'transparent', boxShadow: s.id === section ? 'inset 3px 0 0 var(--accent)' : undefined }}
                >
                  <span style={{ fontWeight: 600, fontSize: 13 }}>{s.label}</span>
                  <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{s.hint}</span>
                </a>
              ))}
            </nav>
          )}
          <section className="rk-card" style={{ flex: '1 1 480px', minWidth: 0, padding: '20px 24px 24px', display: 'flex', flexDirection: 'column', gap: 20 }} aria-labelledby="settings-heading">
            <h2 id="settings-heading" ref={heading} tabIndex={-1} style={{ margin: 0, fontSize: 16, fontWeight: 600, outline: 'none' }}>
              {label}
            </h2>
            {section === 'general' && <General theme={theme} onTheme={onTheme} settings={settings} refreshedAt={refreshedAt} canChange={canChange} />}
            {section === 'sherlock' && <SherlockSettings open />}
            {section === 'watson' && <WatsonSettings open />}
          </section>
        </div>
      </div>
    </main>
  )
}

function General({ theme, onTheme, settings, refreshedAt, canChange }: Omit<Props, 'route'>) {
  const [saving, setSaving] = useState<number | null>(null)
  const [error, setError] = useState('')
  // Another browser's change arrives through the stream; the page follows
  // it and drops a stale error.
  const [seen, setSeen] = useState(settings?.refreshSeconds)
  if (settings?.refreshSeconds !== seen) {
    setSeen(settings?.refreshSeconds)
    setError('')
  }

  const pick = async (seconds: number) => {
    if (saving !== null || seconds === settings?.refreshSeconds) return
    setSaving(seconds)
    setError('')
    try {
      await saveSettings({ refreshSeconds: seconds })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(null)
    }
  }

  const current = settings?.refreshSeconds
  const choices: number[] = [...REFRESH_CHOICES]
  if (current && !choices.includes(current)) choices.push(current)
  choices.sort((a, b) => a - b)
  const kept =
    settings?.storage === 'configmap'
      ? 'Kept on the server in its ConfigMap, so it survives restarts.'
      : settings
        ? 'Kept on the server until it restarts; it was started without a ConfigMap to keep settings in.'
        : ''
  return (
    <>
      <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Theme">
        <h3 className="rk-h">Theme</h3>
        <div className="rk-seg" role="group" aria-label="Theme" style={{ alignSelf: 'flex-start' }}>
          {(['light', 'dark'] as const).map((t) => (
            <button key={t} aria-pressed={theme === t} onClick={() => onTheme(t)}>
              {t === 'light' ? 'Light' : 'Dark'}
            </button>
          ))}
        </div>
        <p className="rk-fhint" style={{ margin: 0 }}>
          For this browser.
        </p>
      </section>
      <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Refresh rate">
        <h3 className="rk-h">Refresh rate</h3>
        <div className="rk-seg" role="group" aria-label="Refresh rate" style={{ alignSelf: 'flex-start', flexWrap: 'wrap' }}>
          {choices.map((s) => (
            <button key={s} aria-pressed={current === s} aria-busy={saving === s} disabled={!settings || saving !== null || !canChange} onClick={() => void pick(s)}>
              {refreshLabel(s)}
            </button>
          ))}
        </div>
        <p className="rk-fhint" style={{ margin: 0 }}>
          {canChange
            ? 'For everyone: the server reads what it does not watch, rebuilds the rows that changed and updates every open console at the same moment. Nodes and pods are watched, so the rate adds no requests to the Kubernetes API for them.'
            : 'For everyone; an administrator sets it.'}
        </p>
        {error ? (
          <p className="rk-t" data-tone="err" style={{ margin: 0, fontSize: 12 }} role="alert">
            {error}
          </p>
        ) : (
          <p className="rk-fhint" style={{ margin: 0 }} data-testid="settings-status">
            {settings ? (
              <>
                Every {refreshLabel(settings.refreshSeconds)}
                {refreshedAt ? ` · last refresh ${fmtClock(refreshedAt)}` : ''}. {kept}
              </>
            ) : (
              'Loading the settings from the server…'
            )}
          </p>
        )}
      </section>
    </>
  )
}
