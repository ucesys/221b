import { useEffect, useRef, useState } from 'react'
import { REFRESH_CHOICES, refreshLabel, saveSettings } from '../api/settings'
import type { Settings } from '../api/types'
import { fmtClock } from '../lib/format'
import { adminHref, navigate, type Route } from '../lib/router'
import { SherlockSettings } from './SherlockSettings'
import { WatsonSettings } from './WatsonSettings'

type Section = 'general' | 'sherlock' | 'watson'

const SECTIONS: { id: Section; label: string; hint: string }[] = [
  { id: 'general', label: 'General', hint: 'Refresh rate' },
  { id: 'sherlock', label: 'Sherlock', hint: 'AI provider, model, clusters, caps' },
  { id: 'watson', label: 'Watson', hint: 'Commands and approvals' },
]

const settingsHref = (section: Section) => adminHref('settings', section === 'general' ? [] : [section])

interface Props {
  route: Route
  settings: Settings | null
  refreshedAt: Date | null
}

/**
 * Settings, the last page of the Administration area (RB-47), for
 * SuperAdmins: General (the refresh rate, kept on the server), Sherlock and
 * Watson. The theme is personal and lives in the account menu. The page and
 * its frame, with Back and Escape, are the Administration area's.
 */
export function SettingsPanel({ route, settings, refreshedAt }: Props) {
  const asked = route.path[1] as Section | undefined
  const section: Section = SECTIONS.some((s) => s.id === asked) ? asked! : 'general'
  const heading = useRef<HTMLHeadingElement>(null)

  useEffect(() => {
    heading.current?.focus({ preventScroll: true })
  }, [section])

  const label = SECTIONS.find((s) => s.id === section)!.label
  return (
    <div style={{ display: 'flex', gap: 20, flexWrap: 'wrap', alignItems: 'flex-start' }} data-testid="settings-page">
      <nav aria-label="Settings sections" style={{ flex: '0 0 200px', display: 'flex', flexDirection: 'column', gap: 2, position: 'sticky', top: 16 }} className="rk-setnav">
        {SECTIONS.map((s) => (
          <a
            key={s.id}
            href={settingsHref(s.id)}
            aria-current={s.id === section ? 'page' : undefined}
            onClick={(e) => {
              e.preventDefault()
              navigate(settingsHref(s.id), true)
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
      <section className="rk-card" style={{ flex: '1 1 480px', minWidth: 0, padding: '20px 24px 24px', display: 'flex', flexDirection: 'column', gap: 20 }} aria-labelledby="settings-heading">
        <h2 id="settings-heading" ref={heading} tabIndex={-1} style={{ margin: 0, fontSize: 16, fontWeight: 600, outline: 'none' }}>
          {label}
        </h2>
        {section === 'general' && <General settings={settings} refreshedAt={refreshedAt} />}
        {section === 'sherlock' && <SherlockSettings open />}
        {section === 'watson' && <WatsonSettings open />}
      </section>
    </div>
  )
}

function General({ settings, refreshedAt }: Omit<Props, 'route'>) {
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
      <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Refresh rate">
        <h3 className="rk-h">Refresh rate</h3>
        <div className="rk-seg" role="group" aria-label="Refresh rate" style={{ alignSelf: 'flex-start', flexWrap: 'wrap' }}>
          {choices.map((s) => (
            <button key={s} aria-pressed={current === s} aria-busy={saving === s} disabled={!settings || saving !== null} onClick={() => void pick(s)}>
              {refreshLabel(s)}
            </button>
          ))}
        </div>
        <p className="rk-fhint" style={{ margin: 0 }}>
          For everyone: the server reads what it does not watch, rebuilds the rows that changed and updates every open console at the same moment. Nodes and pods are watched, so the rate adds no requests to the Kubernetes API for them.
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
