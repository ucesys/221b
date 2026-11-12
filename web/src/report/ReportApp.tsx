// The report page (Phase 12, IS-27, IS-28, IS-30): one box to say what is
// wrong, the reply to it, the person's reports and the known problems.
// ?text= fills the box and never sends it; ?for= files for someone else,
// for an operator. /report/mine shows the reports alone. A link from the
// console hands its text over in the fragment (CS-15, ADR-89): read once,
// it fills the box, and nothing is sent until the person presses Send. The page asks for
// what changed every half minute and says it at the top. The reporter may
// say how bad it is (P1 to P4) and which cluster it is on; left alone, the
// server's defaults apply (CF-64).
import { useCallback, useEffect, useRef, useState } from 'react'
import { signOut, useActivity } from '../api/auth'
import { readJSON } from '../lib/storage'
import { takeHandoff } from '../lib/reportLink'
import {
  addMessage,
  fetchIssueMe,
  fetchKnown,
  fetchMine,
  fetchUpdates,
  followKnown,
  knownPill,
  matchKnown,
  priorityText,
  reopenReport,
  REPORT_PRIORITIES,
  sendReport,
  unlinkReport,
  type IssueMe,
  type Known,
  type Notice,
  type Receipt,
  type Report,
} from './api'
import { Pill } from './Pill'
import { KnownPanel } from './KnownPanel'
import { MyReports } from './MyReports'
import { ReplyCard } from './ReplyCard'

type Theme = 'light' | 'dark'

/** The console's theme, as the person last chose it there. */
function initialTheme(): Theme {
  const saved = readJSON<Theme | null>('221b.theme', null)
  if (saved === 'light' || saved === 'dark') return saved
  try {
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  } catch {
    return 'light'
  }
}

/** Initials for the avatar: "Ada Byron" is AB. */
export function initials(name: string): string {
  const parts = name.split(/[\s._@-]+/).filter(Boolean)
  return ((parts[0]?.[0] ?? '') + (parts.length > 1 ? (parts[parts.length - 1]?.[0] ?? '') : '')).toUpperCase() || '?'
}

/** How often the page asks what changed. */
export const POLL_EVERY = 30_000

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

export function ReportApp() {
  useActivity()
  const [theme] = useState<Theme>(initialTheme)
  const params = new URLSearchParams(window.location.search)
  const mineOnly = /^\/report\/mine\/?$/.test(window.location.pathname)
  const [me, setMe] = useState<IssueMe | null>(null)
  const [meError, setMeError] = useState('')
  const [handed] = useState(() => takeHandoff())
  const [text, setText] = useState(() => handed || (params.get('text') ?? ''))
  const forUser = (params.get('for') ?? '').trim()
  // The priority and cluster chosen; '' is "Not sure" (CF-64).
  const [priority, setPriority] = useState('')
  const [cluster, setCluster] = useState('')
  const [sending, setSending] = useState(false)
  const [sendError, setSendError] = useState('')
  const [receipt, setReceipt] = useState<Receipt | null>(null)
  const [reports, setReports] = useState<Report[] | null>(null)
  const [reportsError, setReportsError] = useState('')
  const [known, setKnown] = useState<Known[] | null>(null)
  const [notices, setNotices] = useState<Notice[]>([])
  const since = useRef(new Date().toISOString())

  useEffect(() => {
    document.documentElement.style.colorScheme = theme
    document.body.style.background = theme === 'dark' ? '#0D1013' : '#F2F3F5'
  }, [theme])

  const reload = useCallback(
    (background = false) =>
      Promise.all([
        fetchMine(background).then(
          (r) => {
            setReports(r)
            setReportsError('')
          },
          (e) => setReportsError(errorText(e)),
        ),
        fetchKnown(background).then(setKnown, () => setKnown((k) => k ?? [])),
      ]),
    [],
  )

  useEffect(() => {
    void fetchIssueMe().then(setMe, (e) => setMeError(errorText(e)))
    void reload()
  }, [reload])

  // What changed, every half minute: said at the top, and the lists
  // read again.
  useEffect(() => {
    const t = setInterval(() => {
      void fetchUpdates(since.current).then(
        (u) => {
          since.current = u.now
          if (u.updates.length > 0) {
            setNotices((n) => [...u.updates, ...n].slice(0, 5))
            void reload(true)
          }
        },
        () => undefined,
      )
    }, POLL_EVERY)
    return () => clearInterval(t)
  }, [reload])

  const operatorFor = forUser !== '' && !!me?.operator
  const send = async () => {
    setSending(true)
    setSendError('')
    try {
      const r = await sendReport(text.trim(), { for: operatorFor ? forUser : undefined, priority: priority ? Number(priority) : undefined, cluster: cluster || undefined })
      setReceipt(r)
      setText('')
      setPriority('')
      setCluster('')
      void reload()
    } catch (e) {
      setSendError(errorText(e))
    } finally {
      setSending(false)
    }
  }
  // The known problems what is typed may be, as it is typed (wave 6).
  const [typed, setTyped] = useState(text)
  useEffect(() => {
    const t = setTimeout(() => setTyped(text), 400)
    return () => clearTimeout(t)
  }, [text])
  const maybe = matchKnown(typed, known ?? [])
  const replace = (r: Report) => setReports((list) => (list ?? []).map((x) => (x.number === r.number ? r : x)))
  const name = me?.person.name || me?.person.id || ''
  const mayReport = !me || me.report || me.operator
  const clusters = me?.clusters ?? []

  return (
    <div className="rk rp" data-theme={theme} data-accent="teal" data-density="comfortable">
      <header className="rp-top">
        <span className="rp-badge" aria-hidden="true">
          !
        </span>
        <strong className="rp-title">Report a problem</strong>
        {me?.company && <span className="rk-dim">{me.company}</span>}
        <span className="rp-grow" />
        {name && (
          <>
            <span className="rk-dim rp-who">Signed in as {name}</span>
            <span className="rp-av" aria-hidden="true">
              {initials(name)}
            </span>
            <button type="button" className="rk-link rp-small" onClick={() => void signOut()}>
              Sign out
            </button>
          </>
        )}
      </header>
      <main className="rp-main">
        <div className="rp-col">
          {meError && (
            <div className="rk-banner" data-tone="err" role="alert">
              {meError}
            </div>
          )}
          {notices.length > 0 && (
            <section className="rk-card rp-card rp-notices" aria-live="polite" aria-label="What changed">
              <ul>
                {notices.map((n, i) => (
                  <li key={`${n.at}-${i}`}>{n.text}</li>
                ))}
              </ul>
              <button type="button" className="rk-link rp-small" onClick={() => setNotices([])}>
                Dismiss
              </button>
            </section>
          )}
          {mineOnly ? (
            <a className="rp-small" href="/report">
              Report a new problem
            </a>
          ) : (
            <section className="rk-card rp-card" aria-labelledby="rp-say">
              <h1 id="rp-say" className="rp-h1">
                What's wrong?
              </h1>
              <span className="rk-dim">Say it in your own words. A page address, a server name or the error text helps us find it faster.</span>
              {handed && text === handed && (
                <span className="rk-t" data-tone="info" data-testid="handoff-note">
                  Filled in from 221B. Change it as you like: nothing is sent until you press Send.
                </span>
              )}
              {operatorFor && (
                <span className="rk-t" data-tone="info" data-testid="for-note">
                  Reporting for {forUser}: they will see it as theirs, and you as who filed it.
                </span>
              )}
              <form
                className="rp-form"
                onSubmit={(e) => {
                  e.preventDefault()
                  if (text.trim() && !sending) void send()
                }}
              >
                <textarea className="rp-say" aria-labelledby="rp-say" value={text} disabled={!mayReport} onChange={(e) => setText(e.target.value)} data-testid="report-text" />
                <div className="rp-picks">
                  <div className="rp-pick">
                    <label htmlFor="rp-priority">How bad is it?</label>
                    <span className="rk-field rp-field">
                      <select id="rp-priority" value={priority} disabled={!mayReport} onChange={(e) => setPriority(e.target.value)} data-testid="report-priority">
                        <option value="">Not sure</option>
                        {REPORT_PRIORITIES.map(([p, means]) => (
                          <option key={p} value={String(p)}>
                            {`${priorityText(p)} (${means})`}
                          </option>
                        ))}
                      </select>
                    </span>
                  </div>
                  {clusters.length > 0 && (
                    <div className="rp-pick">
                      <label htmlFor="rp-cluster">Which cluster?</label>
                      <span className="rk-field rp-field">
                        <select id="rp-cluster" value={cluster} disabled={!mayReport} onChange={(e) => setCluster(e.target.value)} data-testid="report-cluster">
                          <option value="">Not sure</option>
                          {clusters.map((c) => (
                            <option key={c.id} value={c.id}>
                              {c.name || c.id}
                            </option>
                          ))}
                        </select>
                      </span>
                    </div>
                  )}
                </div>
                <span className="rp-note">Not sure? Leave it: we set it when we look, and may change what you chose.</span>
                <div className="rp-line">
                  <span className="rp-note rp-grow">Don't paste passwords or tokens. If you do, we remove them before anyone sees the report.</span>
                  <button type="submit" className="rk-btn rp-send" data-primary="true" disabled={sending || !text.trim() || !mayReport}>
                    {sending ? 'Sending…' : 'Send'}
                  </button>
                </div>
              </form>
              {maybe.length > 0 && (
                <div className="rp-known" data-testid="report-maybe-known">
                  <strong>This may be a problem we already know about</strong>
                  {maybe.map((k) => (
                    <div key={k.id} className="rp-line" data-testid="report-maybe-known-item">
                      <span>{k.title}</span>
                      <Pill {...knownPill(k)} />
                      {k.workaround && <span className="rk-dim">What you can do: {k.workaround}</span>}
                    </div>
                  ))}
                  <span className="rp-note">Still yours to send: if it is the same, we link your case to it and keep you posted.</span>
                </div>
              )}
              {!mayReport && (
                <span className="rk-t" data-tone="warn">
                  Your role may not report problems here; ask an administrator.
                </span>
              )}
              {sendError && (
                <span className="rk-t" data-tone="err" role="alert">
                  {sendError}
                </span>
              )}
            </section>
          )}
          {receipt && (
            <ReplyCard
              receipt={receipt}
              onUnlink={async (n) => {
                replace(await unlinkReport(n))
              }}
            />
          )}
          <MyReports reports={reports} error={reportsError} onReopen={async (n, reason) => replace(await reopenReport(n, reason))} onMessage={async (n, t) => replace(await addMessage(n, t))} />
        </div>
        <KnownPanel
          known={known}
          statusPage={me?.statusPage ?? ''}
          onFollow={async (id, on) => {
            const k = await followKnown(id, on)
            setKnown((list) => (list ?? []).map((x) => (x.id === k.id ? k : x)))
          }}
        />
      </main>
    </div>
  )
}
