import { useEffect, useRef, useState } from 'react'
import { DEFAULT_GRACE, GRACE_CHOICES, REFRESH_CHOICES, refreshLabel, saveSettings } from '../api/settings'
import type { Settings } from '../api/types'
import { DEFAULT_TIME_FORMAT, fmtClock, fmtTs, setTimeFormat, type TimeFormat } from '../lib/format'
import { adminHref, navigate, type Route } from '../lib/router'
import { fetchIssueSettings, saveIssueSettings } from '../api/issues'
import { IssueSettings } from './IssueSettings'
import { SLASettings } from './SLASettings'
import { CasesSettings } from './CasesSettings' // One flow for findings and cases (Phase 12, wave 6)
import { EmailSettings, useMailForm } from './EmailSettings' // Email about cases (EM-01)
import { SaveBar } from './SaveBar'
import { WatsonSettings } from './WatsonSettings'
import { AgentsSection } from './ai/AgentsSection'
import { KnowledgeSection } from './ai/KnowledgeSection'
import { LimitsSection } from './ai/LimitsSection' // Limits (Phase 12.1, AI-37)
import { ProvidersSection } from './ai/ProvidersSection'
import { JobsSettings } from './aiops/JobsSettings'
import { GettingStartedSection } from './GettingStarted'
import { HelmReposSettings } from './apps/HelmReposSettings' // Helm repositories (Phase 12, wave 5)
import { CredentialsSettings } from './CredentialsSettings' // Settings › Credentials (0.12.1)
import { expiringSoon, fetchCredentials } from '../api/credentials'
import { leftText, useSetup } from '../api/setup'
import { LeaveSaveContext } from '../lib/leaveSave'
import { hasUnsaved, unsavedWhat } from '../lib/unsaved'
import { ConfirmDialog } from './ConfirmDialog'

type Section = 'start' | 'general' | 'credentials' | 'providers' | 'agents' | 'limits' | 'knowledge' | 'watson' | 'apps' | 'issues' | 'jobs' | 'sla' | 'cases'

interface SectionInfo {
  id: Section
  label: string
  hint: string
}

// The sections, in groups under a heading (Phase 12): the AI agents'
// replace Sherlock's of before, whose address leads to Agents.
const GROUPS: { label?: string; sections: SectionInfo[] }[] = [
  {
    sections: [
      // --- Getting started (Phase 12, GS-05): first, with what is left.
      { id: 'start', label: 'Getting started', hint: 'What is left to set up' },
      // Email is a section of General since 0.12.1 (its old address leads there).
      { id: 'general', label: 'General', hint: 'Refresh rate, times, alerts, email' },
      // --- Settings › Credentials (0.12.1, CR-01): after General, as
      // applications, repositories and Mycroft's sources all use them.
      { id: 'credentials', label: 'Credentials', hint: 'Tokens and keys the console signs in with' },
    ],
  },
  {
    label: 'AI',
    sections: [
      { id: 'providers', label: 'Providers', hint: 'Where models come from' },
      { id: 'agents', label: 'Agents', hint: 'Sherlock and the others: models, tools, budgets' },
      // --- Limits (Phase 12.1, AI-37): the total, each provider's, each agent's.
      { id: 'limits', label: 'Limits', hint: 'What the agents may spend: total, providers, agents' },
      { id: 'knowledge', label: 'Knowledge', hint: 'Mycroft’s sources and storage' },
    ],
  },
  {
    label: 'Changes',
    sections: [
      { id: 'watson', label: 'Watson', hint: 'Where it runs, approvals' },
      // --- Helm repositories (Phase 12, wave 5) ---
      { id: 'apps', label: 'Applications', hint: 'Helm repositories and OCI registries' },
    ],
  },
  {
    label: 'AIOps',
    sections: [
      // --- Cases replace reports (Phase 12, wave 5): the SLA, for every
      // case; first, since every case has one.
      { id: 'sla', label: 'SLA', hint: 'Response and resolution targets, P0 to P4' },
      { id: 'issues', label: 'Issues', hint: 'Matching, timers, webhook' },
      // --- One flow for findings and cases (Phase 12, wave 6) ---
      { id: 'cases', label: 'Cases', hint: 'Keeping agent-found cases, exceptions, caps' },
      // --- Jobs (Phase 12) ---
      { id: 'jobs', label: 'Jobs', hint: 'Who may edit jobs, what agents read as' },
    ],
  },
]

const SECTIONS: SectionInfo[] = GROUPS.flatMap((g) => g.sections)

const settingsHref = (section: Section) => adminHref('settings', section === 'general' ? [] : [section])

/** The section an address asks for: Sherlock's of before is Agents now, and Email is General's. */
export function sectionOf(asked: string | undefined): Section {
  if (asked === 'sherlock') return 'agents'
  if (asked === 'email') return 'general'
  return SECTIONS.find((s) => s.id === asked)?.id ?? 'general'
}

interface Props {
  route: Route
  settings: Settings | null
  refreshedAt: Date | null
  onToast?: (msg: string) => void
}

/**
 * Settings, the last page of the Administration area (RB-47), for
 * SuperAdmins: General (the refresh rate, kept on the server), the AI
 * agents' Providers, Agents and Knowledge (Phase 12), Watson, and the issue
 * service's (Phase 12). The theme is personal and lives in the account
 * menu. The page and its frame, with Back and Escape, are the
 * Administration area's.
 */
export function SettingsPanel({ route, settings, refreshedAt, onToast }: Props) {
  const section = sectionOf(route.path[1])
  const heading = useRef<HTMLHeadingElement>(null)

  useEffect(() => {
    heading.current?.focus({ preventScroll: true })
  }, [section])
  // Settings › Email of before (EM-01): General's Email section, at its
  // own address no more.
  const askedEmail = route.path[1] === 'email'
  useEffect(() => {
    if (!askedEmail) return
    document.getElementById('settings-email')?.scrollIntoView?.({ block: 'start' })
    navigate(settingsHref('general'), true)
  }, [askedEmail])

  // Leaving a page with changes not saved asks first (CU-16): Save (where
  // the page's Save can say it all went), Discard, or stay. Closing or
  // reloading the browser's tab asks the browser's own way.
  const leaveSave = useRef<(() => Promise<boolean>) | null>(null)
  const [leaving, setLeaving] = useState<{ to: string; canSave: boolean } | null>(null)
  const [leaveBusy, setLeaveBusy] = useState(false)
  useEffect(() => {
    const ask = (e: BeforeUnloadEvent) => {
      if (!hasUnsaved()) return
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', ask)
    return () => window.removeEventListener('beforeunload', ask)
  }, [])
  const go = (to: string) => {
    if (to !== settingsHref(section) && hasUnsaved()) setLeaving({ to, canSave: !!leaveSave.current })
    else navigate(to, true)
  }

  const label = SECTIONS.find((s) => s.id === section)!.label
  // Getting started's hint says how many steps are left (Phase 12).
  const { view: setup } = useSetup(true)
  // Credentials' hint says how many expire within 14 days (CR-08).
  const [credsSoon, setCredsSoon] = useState(0)
  useEffect(() => {
    let stop = false
    fetchCredentials().then(
      (v) => !stop && setCredsSoon(expiringSoon(v.credentials ?? []).length),
      () => undefined,
    )
    return () => {
      stop = true
    }
  }, [section])
  return (
    <LeaveSaveContext.Provider value={leaveSave}>
      <div style={{ display: 'flex', gap: 20, flexWrap: 'wrap', alignItems: 'flex-start' }} data-testid="settings-page">
        <nav aria-label="Settings sections" style={{ flex: '0 0 200px', display: 'flex', flexDirection: 'column', gap: 2, position: 'sticky', top: 16 }} className="rk-setnav">
          {GROUPS.map((g) => [
            g.label && (
              <span key={g.label} className="rk-fhint" style={{ padding: '10px 12px 2px', fontSize: 11, fontWeight: 600, textTransform: 'uppercase', letterSpacing: 0.4 }} data-testid="settings-group">
                {g.label}
              </span>
            ),
            ...g.sections.map((s) => (
              <a
                key={s.id}
                href={settingsHref(s.id)}
                aria-current={s.id === section ? 'page' : undefined}
                onClick={(e) => {
                  e.preventDefault()
                  go(settingsHref(s.id))
                }}
                data-testid="settings-section"
                data-section={s.id}
                style={{
                  display: 'flex',
                  flexDirection: 'column',
                  padding: '8px 12px',
                  borderRadius: 8,
                  textDecoration: 'none',
                  color: 'var(--fg)',
                  background: s.id === section ? 'var(--surface)' : 'transparent',
                  boxShadow: s.id === section ? 'inset 3px 0 0 var(--accent)' : undefined,
                }}
              >
                <span style={{ fontWeight: 600, fontSize: 13 }}>{s.label}</span>
                <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{s.id === 'start' ? leftText(setup) : s.id === 'credentials' && credsSoon > 0 ? `${credsSoon} ${credsSoon === 1 ? 'expires' : 'expire'} within 14 days` : s.hint}</span>
              </a>
            )),
          ])}
        </nav>
        <section className="rk-card" style={{ flex: '1 1 480px', minWidth: 0, padding: '20px 24px 24px', display: 'flex', flexDirection: 'column', gap: 20 }} aria-labelledby="settings-heading">
          <h2 id="settings-heading" ref={heading} tabIndex={-1} style={{ margin: 0, fontSize: 16, fontWeight: 600, outline: 'none' }}>
            {label}
          </h2>
          {section === 'start' && <GettingStartedSection onToast={onToast} />}
          {section === 'general' && <General settings={settings} refreshedAt={refreshedAt} />}
          {section === 'credentials' && <CredentialsSettings onToast={onToast} />}
          {section === 'providers' && <ProvidersSection onToast={onToast} initial={route.path[2]} />}
          {section === 'agents' && <AgentsSection onToast={onToast} initial={route.path[2]} />}
          {section === 'limits' && <LimitsSection onToast={onToast} />}
          {section === 'knowledge' && <KnowledgeSection onToast={onToast} focus={route.path[2]} />}
          {section === 'watson' && <WatsonSettings open />}
          {section === 'apps' && <HelmReposSettings onToast={onToast} />}
          {section === 'issues' && <IssueSettings open />}
          {section === 'jobs' && <JobsSettings />}
          {section === 'sla' && <SLASettings />}
          {section === 'cases' && <CasesSettings />}
        </section>
        {leaving && (
          <ConfirmDialog
            title={leaving.canSave ? 'Save or discard?' : 'Discard the changes?'}
            confirm="Discard"
            danger
            busy={leaveBusy}
            also={
              leaving.canSave
                ? {
                    label: 'Save',
                    testId: 'leave-save',
                    onClick: () => {
                      const save = leaveSave.current
                      if (!save) return
                      setLeaveBusy(true)
                      void save().then((ok) => {
                        setLeaveBusy(false)
                        setLeaving(null)
                        // What failed is said on the page's Save bar, where it stays.
                        if (ok) navigate(leaving.to, true)
                      })
                    },
                  }
                : undefined
            }
            onCancel={() => setLeaving(null)}
            onConfirm={() => {
              setLeaving(null)
              navigate(leaving.to, true)
            }}
          >
            <p style={{ margin: 0 }} data-testid="leave-unsaved">
              {unsavedWhat().join(', ') || 'This page'} has changes not saved. {leaving.canSave ? 'Save them, or discard them and leave.' : 'Discard them and leave, or cancel to stay.'}
            </p>
          </ConfirmDialog>
        )}
      </div>
    </LeaveSaveContext.Provider>
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
  const [fleetsDraft, setFleetsDraft] = useState<boolean | undefined>(undefined)
  const currentFleets = settings?.fleetsPage ?? true
  const pickedFleets = fleetsDraft ?? currentFleets
  const [imagesDraft, setImagesDraft] = useState<boolean | undefined>(undefined)
  const currentImages = settings?.imageChecks ?? true
  const pickedImages = imagesDraft ?? currentImages
  // The company or IT name on the report page (IS-29): the issue service's
  // setting, kept where it was, set here since 0.12.1. Without the issue
  // service the field is not shown.
  const [company, setCompany] = useState<string | null>(null)
  const [companyDraft, setCompanyDraft] = useState<string | undefined>(undefined)
  useEffect(() => {
    let gone = false
    fetchIssueSettings().then(
      (s) => !gone && setCompany(s.company),
      () => !gone && setCompany(null),
    )
    return () => {
      gone = true
    }
  }, [])
  const companyChanged = company !== null && companyDraft !== undefined && companyDraft.trim() !== company
  const settingsChanged = (draft !== undefined && draft !== settings?.refreshSeconds) || formatDraft !== undefined || graceDraft !== undefined || bannersDraft !== undefined || fleetsDraft !== undefined || imagesDraft !== undefined
  // Email (EM-11) is saved by the same bar (CU-16).
  const mail = useMailForm()
  const changed = settingsChanged || companyChanged || mail.changed

  // One Save for the page (CU-16): only the parts changed, in turn; a part
  // that fails keeps what was typed and is named, the others stay saved,
  // and Save again tries only what is left.
  const save = async (): Promise<boolean> => {
    setBusy(true)
    setError('')
    setSaved('')
    const failed: string[] = []
    const notes: string[] = []
    const why = (err: unknown) => (err instanceof Error ? err.message : String(err))
    if (settingsChanged && picked !== undefined) {
      try {
        await saveSettings({ refreshSeconds: picked, timeFormat: pickedFormat, pendingGraceSeconds: pickedGrace, alertBanners: pickedBanners, fleetsPage: pickedFleets, imageChecks: pickedImages })
        setDraft(undefined)
        setBannersDraft(undefined)
        setFleetsDraft(undefined)
        setImagesDraft(undefined)
        setFormatDraft(undefined)
        setGraceDraft(undefined)
      } catch (err) {
        failed.push(`Console settings not saved: ${why(err)}`)
      }
    }
    if (companyChanged) {
      try {
        // Read again just before, so Settings › Issues' other values stay as they are now.
        const current = await fetchIssueSettings()
        const s = await saveIssueSettings({ ...current, company: companyDraft.trim() })
        setCompany(s.company)
        setCompanyDraft(undefined)
      } catch (err) {
        failed.push(`Company or IT name not saved: ${why(err)}`)
      }
    }
    if (mail.changed) {
      try {
        notes.push(await mail.save())
      } catch (err) {
        failed.push(`Email not saved: ${why(err)}`)
      }
    }
    setError(failed.join(' '))
    if (!failed.length) setSaved(['Saved.', ...notes].join(' '))
    setBusy(false)
    return !failed.length
  }

  const current = settings?.refreshSeconds
  const choices: number[] = [...REFRESH_CHOICES]
  if (current && !choices.includes(current)) choices.push(current)
  choices.sort((a, b) => a - b)
  const kept = settings?.storage === 'configmap' ? 'Kept on the server in its ConfigMap, so it survives restarts.' : settings ? 'Kept on the server until it restarts; it was started without a ConfigMap to keep settings in.' : ''
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
      <FleetsPageSection picked={pickedFleets} disabled={!settings || busy} onPick={(b) => setFleetsDraft(b === currentFleets ? undefined : b)} />
      <ImageChecksSection picked={pickedImages} disabled={!settings || busy} onPick={(b) => setImagesDraft(b === currentImages ? undefined : b)} />
      {company !== null && (
        <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Company or IT name">
          <h3 className="rk-h">Company or IT name</h3>
          <span className="rk-field" style={{ maxWidth: 360 }}>
            <input value={companyDraft ?? company} maxLength={120} disabled={busy} onChange={(e) => setCompanyDraft(e.target.value)} aria-label="Company or IT name" data-testid="settings-company" />
          </span>
          <p className="rk-fhint" style={{ margin: 0 }}>
            In the report page’s header, such as “Example Corp IT”.
          </p>
        </section>
      )}
      {/* --- Email about cases (EM-01), in General since 0.12.1, saved by its bar (CU-16) --- */}
      <section id="settings-email" style={{ display: 'flex', flexDirection: 'column', gap: 8, borderTop: '1px solid var(--line)', paddingTop: 16 }} aria-label="Email" data-testid="settings-email-section">
        <h3 className="rk-h">Email</h3>
        <EmailSettings mail={mail} busy={busy} />
      </section>
      <SaveBar
        what="Settings › General"
        changed={changed}
        busy={busy}
        onSave={() => void save()}
        save={save}
        onUndo={() => {
          setDraft(undefined)
          setFormatDraft(undefined)
          setGraceDraft(undefined)
          setBannersDraft(undefined)
          setFleetsDraft(undefined)
          setImagesDraft(undefined)
          setCompanyDraft(undefined)
          mail.undo()
          setError('')
        }}
        error={error}
        saved={saved}
        testId="settings-save"
      />
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

/** Whether the Fleets tab shows; fleets are made and kept from the Nodes tab either way (R2-03). */
function FleetsPageSection({ picked, disabled, onPick }: { picked: boolean; disabled: boolean; onPick: (b: boolean) => void }) {
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Fleets page">
      <h3 className="rk-h">Fleets page</h3>
      <div className="rk-seg" role="group" aria-label="The Fleets tab" style={{ alignSelf: 'flex-start' }} data-testid="settings-fleets-page">
        <button aria-pressed={picked} disabled={disabled} onClick={() => onPick(true)}>
          Show
        </button>
        <button aria-pressed={!picked} disabled={disabled} onClick={() => onPick(false)}>
          Hide
        </button>
      </div>
      <p className="rk-fhint" style={{ margin: 0 }}>
        For everyone: the Fleets tab, with every node against every label and taint. Fleets are made, changed and deleted from the Nodes tab’s fleet list either way, and filter the nodes there.
      </p>
    </section>
  )
}

/** Whether the leader looks for newer images than the Helm releases run (HA-23). */
function ImageChecksSection({ picked, disabled, onPick }: { picked: boolean; disabled: boolean; onPick: (b: boolean) => void }) {
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Newer images">
      <h3 className="rk-h">Newer images</h3>
      <div className="rk-seg" role="group" aria-label="Look for newer images" style={{ alignSelf: 'flex-start' }} data-testid="settings-image-checks">
        <button aria-pressed={picked} disabled={disabled} onClick={() => onPick(true)}>
          Check
        </button>
        <button aria-pressed={!picked} disabled={disabled} onClick={() => onPick(false)}>
          Off
        </button>
      </div>
      <p className="rk-fhint" style={{ margin: 0 }}>
        For everyone: the console asks the registries of the images the Helm releases run, anonymously and at most every six hours each, for a newer tag of the same form, and the Helm tab says when there is one. Private repositories and
        registries it cannot reach are not checked. Off, no registry is asked.
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
      <p className="rk-fhint" style={{ margin: 0 }}>
        For everyone: every date and time in the console reads this way, each in the time zone of the browser that shows it.
      </p>
    </section>
  )
}
