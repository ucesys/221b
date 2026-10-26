import { useEffect, useRef, useState } from 'react'
import { DEFAULT_GRACE, GRACE_CHOICES, REFRESH_CHOICES, refreshLabel, saveSettings } from '../api/settings'
import type { Settings } from '../api/types'
import { DEFAULT_TIME_FORMAT, fmtClock, fmtTs, setTimeFormat, type TimeFormat } from '../lib/format'
import { adminHref, navigate, type Route } from '../lib/router'
import { SaveBar } from './SaveBar'
import { SherlockSettings } from './SherlockSettings'
import { WatsonSettings } from './WatsonSettings'

type Section = 'general' | 'sherlock' | 'watson'

const SECTIONS: { id: Section; label: string; hint: string }[] = [
  { id: 'general', label: 'General', hint: 'Refresh rate, times, alerts' },
  { id: 'sherlock', label: 'Sherlock', hint: 'On per cluster, API key, model, caps' },
  { id: 'watson', label: 'Watson', hint: 'Where it runs, approvals' },
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
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState('')
  const [draft, setDraft] = useState<number | undefined>(undefined)
  // Another browser's change arrives through the stream; the page follows
  // it and drops a stale error.
  const [seen, setSeen] = useState(settings?.refreshSeconds)
  if (settings?.refreshSeconds !== seen) {
    setSeen(settings?.refreshSeconds)
    setError('')
  }
  const picked = draft ?? settings?.refreshSeconds
  const [formatDraft, setFormatDraft] = useState<TimeFormat | undefined>(undefined)
  const currentFormat = settings?.timeFormat ?? DEFAULT_TIME_FORMAT
  const pickedFormat = formatDraft ?? currentFormat
  const [graceDraft, setGraceDraft] = useState<number | undefined>(undefined)
  const currentGrace = settings?.pendingGraceSeconds ?? DEFAULT_GRACE
  const pickedGrace = graceDraft ?? currentGrace
  const [bannersDraft, setBannersDraft] = useState<boolean | undefined>(undefined)
  const currentBanners = settings?.alertBanners ?? true
  const pickedBanners = bannersDraft ?? currentBanners
  const changed = (draft !== undefined && draft !== settings?.refreshSeconds) || formatDraft !== undefined || graceDraft !== undefined || bannersDraft !== undefined

  const save = async () => {
    if (picked === undefined) return
    setBusy(true)
    setError('')
    setSaved('')
    try {
      await saveSettings({ refreshSeconds: picked, timeFormat: pickedFormat, pendingGraceSeconds: pickedGrace, alertBanners: pickedBanners })
      setDraft(undefined)
      setBannersDraft(undefined)
      setFormatDraft(undefined)
      setGraceDraft(undefined)
      setSaved('Saved.')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
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
            <button key={s} aria-pressed={picked === s} disabled={!settings || busy} onClick={() => setDraft(s === current ? undefined : s)}>
              {refreshLabel(s)}
            </button>
          ))}
        </div>
        <p className="rk-fhint" style={{ margin: 0 }}>
          For everyone: the server reads what it does not watch, rebuilds the rows that changed and updates every open console at the same moment. Nodes and pods are watched, so the rate adds no requests to the Kubernetes API for them.
        </p>
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="settings-status">
          {settings ? (
            <>
              Now every {refreshLabel(settings.refreshSeconds)}
              {refreshedAt ? ` · last refresh ${fmtClock(refreshedAt, { seconds: true })}` : ''}. {kept}
            </>
          ) : (
            'Loading the settings from the server…'
          )}
        </p>
      </section>
      <TimeFormatSection picked={pickedFormat} current={currentFormat} disabled={!settings || busy} onPick={(f) => setFormatDraft(f.date === currentFormat.date && f.clock === currentFormat.clock ? undefined : f)} />
      <GraceSection picked={pickedGrace} current={currentGrace} disabled={!settings || busy} onPick={(g) => setGraceDraft(g === currentGrace ? undefined : g)} />
      <BannersSection picked={pickedBanners} disabled={!settings || busy} onPick={(b) => setBannersDraft(b === currentBanners ? undefined : b)} />
      <SaveBar changed={changed} busy={busy} onSave={() => void save()} onUndo={() => { setDraft(undefined); setFormatDraft(undefined); setGraceDraft(undefined); setBannersDraft(undefined) }} error={error} saved={saved} testId="settings-save" />
    </>
  )
}

/** Whether the alerts show as banners at the top of every tab but Alerts. */
function BannersSection({ picked, disabled, onPick }: { picked: boolean; disabled: boolean; onPick: (b: boolean) => void }) {
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Alert banners">
      <h3 className="rk-h">Alert banners</h3>
      <div className="rk-seg" role="group" aria-label="Alert banners at the top of every tab" style={{ alignSelf: 'flex-start' }} data-testid="settings-banners">
        <button aria-pressed={picked} disabled={disabled} onClick={() => onPick(true)}>
          Show
        </button>
        <button aria-pressed={!picked} disabled={disabled} onClick={() => onPick(false)}>
          Hide
        </button>
      </div>
      <p className="rk-fhint" style={{ margin: 0 }}>
        For everyone: the banners at the top of every tab that list what the Alerts tab has. Hidden, the Alerts tab’s name still turns red or amber. Banners about the connection itself always show.
      </p>
    </section>
  )
}

/** How long a pod may be pending, or running but not ready, before it is an alert. */
function GraceSection({ picked, current, disabled, onPick }: { picked: number; current: number; disabled: boolean; onPick: (g: number) => void }) {
  const choices: number[] = [...GRACE_CHOICES]
  if (!choices.includes(current)) choices.push(current)
  choices.sort((a, b) => a - b)
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Pending pods">
      <h3 className="rk-h">Pending pods</h3>
      <div className="rk-seg" role="group" aria-label="Alert on a pending pod after" style={{ alignSelf: 'flex-start', flexWrap: 'wrap' }}>
        {choices.map((g) => (
          <button key={g} aria-pressed={picked === g} disabled={disabled} onClick={() => onPick(g)}>
            {g === 0 ? 'At once' : refreshLabel(g)}
          </button>
        ))}
      </div>
      <p className="rk-fhint" style={{ margin: 0 }}>
        For everyone: a pod that is pending, or running but not ready, is an alert only after this long, so pods that are starting do not raise one.
      </p>
    </section>
  )
}

const DATE_CHOICES: { id: TimeFormat['date']; label: string }[] = [
  { id: 'iso', label: '2026-05-03' },
  { id: 'us', label: '05/03/2026' },
  { id: 'eu', label: '03/05/2026' },
  { id: 'long', label: 'May 3, 2026' },
]
const CLOCK_CHOICES: { id: TimeFormat['clock']; label: string }[] = [
  { id: '12h', label: '1:34pm' },
  { id: '24h', label: '13:34' },
]

/** How every console shows dates and times, each in its browser's time zone. */
function TimeFormatSection({ picked, current, disabled, onPick }: { picked: TimeFormat; current: TimeFormat; disabled: boolean; onPick: (f: TimeFormat) => void }) {
  // The example shows the picked format; the console keeps the saved one
  // until it is saved.
  setTimeFormat(picked)
  const example = fmtTs(new Date())
  setTimeFormat(current)
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Date and time">
      <h3 className="rk-h">Date and time</h3>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}>
        <div className="rk-seg" role="group" aria-label="Date" style={{ flexWrap: 'wrap' }}>
          {DATE_CHOICES.map((c) => (
            <button key={c.id} aria-pressed={picked.date === c.id} disabled={disabled} onClick={() => onPick({ ...picked, date: c.id })}>
              {c.label}
            </button>
          ))}
        </div>
        <div className="rk-seg" role="group" aria-label="Clock">
          {CLOCK_CHOICES.map((c) => (
            <button key={c.id} aria-pressed={picked.clock === c.id} disabled={disabled} onClick={() => onPick({ ...picked, clock: c.id })}>
              {c.label}
            </button>
          ))}
        </div>
      </div>
      <p className="rk-fhint" style={{ margin: 0 }} data-testid="time-format-example">
        Now: <span className="rk-mono">{example}</span>
      </p>
      <p className="rk-fhint" style={{ margin: 0 }}>For everyone: every date and time in the console reads this way, each in the time zone of the browser that shows it.</p>
    </section>
  )
}
