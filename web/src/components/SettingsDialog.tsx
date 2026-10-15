import { useEffect, useRef, useState } from 'react'
import { REFRESH_CHOICES, refreshLabel, saveSettings } from '../api/settings'
import type { Settings } from '../api/types'
import { fmtClock } from '../lib/format'
import { CloseIcon } from './Icons'
import { SherlockSettings } from './SherlockSettings'
import type { Theme } from './TopBar'

interface Props {
  open: boolean
  onClose: () => void
  theme: Theme
  onTheme: (t: Theme) => void
  settings: Settings | null
  refreshedAt: Date | null
  /** Whether this person may change the shared settings: administrators do (P3-03). */
  canChange?: boolean
}

/**
 * The Settings dialog: the theme, kept in this browser, and the refresh
 * rate, kept on the server for everyone. Both apply as soon as they are
 * picked.
 */
export function SettingsDialog({ open, onClose, theme, onTheme, settings, refreshedAt, canChange = true }: Props) {
  const ref = useRef<HTMLDialogElement>(null)
  const [saving, setSaving] = useState<number | null>(null)
  const [error, setError] = useState('')
  // Another browser's change arrives through the stream; the dialog follows
  // it and drops a stale error.
  const [seen, setSeen] = useState(settings?.refreshSeconds)
  if (settings?.refreshSeconds !== seen) {
    setSeen(settings?.refreshSeconds)
    setError('')
  }

  useEffect(() => {
    const d = ref.current
    if (!d) return
    if (open && !d.open) d.showModal()
    else if (!open && d.open) d.close()
  }, [open])

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
    <dialog
      ref={ref}
      className="rk-dialog"
      aria-label="Settings"
      onClose={onClose}
      onClick={(e) => {
        // A click on the backdrop, outside the box, closes it.
        if (e.target === ref.current) onClose()
      }}
    >
      <div className="rk-dialog-box">
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '14px 14px 12px 20px', borderBottom: '1px solid var(--line)' }}>
          <h2 style={{ margin: 0, flex: '1 1 auto', fontSize: 15, fontWeight: 600 }}>Settings</h2>
          <button className="rk-icon-btn" aria-label="Close settings" title="Close (Esc)" onClick={onClose}>
            <CloseIcon />
          </button>
        </div>
        <div style={{ padding: '16px 20px 20px', display: 'flex', flexDirection: 'column', gap: 20 }}>
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
            <div className="rk-seg" role="group" aria-label="Refresh rate" style={{ alignSelf: 'flex-start' }}>
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
          {canChange && <SherlockSettings open={open} />}
        </div>
      </div>
    </dialog>
  )
}
