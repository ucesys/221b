import { useState } from 'react'
import { dismissSteps, leftText, nextStep, progressText, shortWhere, useSetup, type SetupStep, type SetupView } from '../api/setup'
import { adminHref } from '../lib/router'

// Getting started (Phase 12, GS-01 to GS-06): the checklist of setting the
// console up, as the mockup Start.dc.html draws it. The server checks each
// step against the console; this page only shows them, links each to its
// screen, and dismisses or brings back.

/** The tone of a step's number: done, next, or the others. */
function numStyle(st: SetupStep, next: boolean) {
  if (st.state === 'done') return { background: 'var(--ok-weak)', color: 'var(--ok)' }
  if (next) return { background: 'var(--accent)', color: 'var(--accent-fg)' }
  return { background: 'var(--sunken)', color: 'var(--fg2)' }
}

function Step({ st, next, last, busy, onDismiss }: { st: SetupStep; next: boolean; last: boolean; busy: boolean; onDismiss: (id: string, dismiss: boolean) => void }) {
  return (
    <li
      data-testid="setup-step"
      data-step={st.id}
      data-state={st.state}
      aria-current={next ? 'step' : undefined}
      style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 12px', padding: '9px 12px', borderBottom: last ? 0 : '1px solid var(--line)', background: next ? 'var(--accent-weak)' : undefined }}
    >
      <span aria-hidden="true" style={{ display: 'inline-flex', alignItems: 'center', justifyContent: 'center', width: 22, height: 22, borderRadius: 11, fontSize: 11.5, fontWeight: 600, flex: 'none', ...numStyle(st, next) }}>
        {st.n}
      </span>
      <span style={{ flex: '1 1 240px', minWidth: 0, display: 'flex', flexDirection: 'column', gap: 2 }}>
        <span style={{ fontWeight: next ? 600 : 500 }}>{st.title}</span>
        <span className="rk-fhint" style={{ fontSize: 12 }} data-testid="setup-detail">
          {st.detail}
        </span>
        {st.state === 'dismissed' && st.dismissedBy && (
          <span className="rk-fhint" style={{ fontSize: 11.5 }}>
            Dismissed by {st.dismissedBy}
          </span>
        )}
      </span>
      {st.state === 'done' && (
        <span className="rk-pill" data-tone="ok">
          Done
        </span>
      )}
      {st.state === 'dismissed' && (
        <>
          <span className="rk-pill">Dismissed</span>
          <button className="rk-btn" data-size="sm" disabled={busy} onClick={() => onDismiss(st.id, false)} data-testid="setup-restore">
            Bring back
          </button>
        </>
      )}
      {st.state === 'open' && (
        <>
          <a className="rk-btn" data-primary={next ? 'true' : undefined} href={st.link} title={`${st.where}: ${st.action}`} data-testid="setup-open">
            {next ? `Open ${shortWhere(st.where)}` : 'Open'}
          </a>
          <button className="rk-btn" data-size="sm" disabled={busy} onClick={() => onDismiss(st.id, true)} title="Hide this step: it does not apply here. It can be brought back." data-testid="setup-dismiss">
            Dismiss
          </button>
        </>
      )}
    </li>
  )
}

/** Settings › Getting started. */
export function GettingStartedSection({ onToast }: { onToast?: (msg: string) => void }) {
  const { view, error, set } = useSetup(true)
  const [busy, setBusy] = useState(false)
  const change = async (steps: string[], dismiss: boolean) => {
    setBusy(true)
    try {
      set(await dismissSteps(steps, dismiss))
    } catch (e) {
      onToast?.(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  if (!view) {
    return (
      <p className="rk-fhint" style={{ margin: 0 }} role={error ? 'alert' : undefined}>
        {error || 'Checking the console…'}
      </p>
    )
  }
  const open = view.steps.filter((s) => s.state === 'open').map((s) => s.id)
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="setup-section">
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10 }}>
        <span className="rk-pill" data-tone={view.complete ? 'ok' : 'info'} data-testid="setup-progress">
          {progressText(view)}
        </span>
        {view.dismissed > 0 && <span className="rk-fhint">{view.dismissed} dismissed</span>}
        <span style={{ flex: '1 1 auto' }} />
        {open.length > 0 && (
          <button className="rk-btn" disabled={busy} onClick={() => void change(open, true)} data-testid="setup-dismiss-rest">
            Dismiss the rest
          </button>
        )}
      </div>
      <p style={{ margin: 0, color: 'var(--fg2)' }}>
        Each step is checked against the console as it is, and ticks itself when done. Passwords, keys and tokens are typed on each step’s own screen, never here or in a chat.
        {view.complete ? ' Every step is done or dismissed, so the list has left the home page and Sherlock’s setup skill has ended.' : ' Sherlock helps with the rest: ask it “What’s next?”.'}
      </p>
      <ol aria-label="Steps" style={{ listStyle: 'none', margin: 0, padding: 0, border: '1px solid var(--line)', borderRadius: 8, overflow: 'hidden' }}>
        {view.steps.map((st, i) => (
          <Step key={st.id} st={st} next={st.id === view.next} last={i === view.steps.length - 1} busy={busy} onDismiss={(id, d) => void change([id], d)} />
        ))}
      </ol>
      {!view.kept && (
        <p className="rk-fhint" style={{ margin: 0 }}>
          Dismissed steps are kept in memory until the console restarts: it was started without a ConfigMap for its settings.
        </p>
      )}
    </div>
  )
}

/** The home page's card for a SuperAdmin, until every step is done or dismissed. */
export function GettingStartedCard() {
  const { view } = useSetup(true)
  if (!view || view.complete) return null
  return <SetupCard view={view} />
}

function SetupCard({ view }: { view: SetupView }) {
  const next = nextStep(view)
  return (
    <section className="rk-card" style={{ padding: '12px 16px', display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 14px' }} aria-labelledby="home-setup" data-testid="home-setup">
      <h2 id="home-setup" style={{ margin: 0, fontSize: 14, fontWeight: 600 }}>
        Getting started
      </h2>
      <span className="rk-pill" data-tone="info">
        {progressText(view)}
      </span>
      {next && (
        <span style={{ flex: '1 1 260px', minWidth: 0, fontSize: 13 }} data-testid="home-setup-next">
          Next: <strong>{next.title}</strong>
        </span>
      )}
      {next && (
        <a className="rk-btn" data-primary="true" href={next.link} data-testid="home-setup-open">
          Open {shortWhere(next.where)}
        </a>
      )}
      <a className="rk-btn" href={adminHref('settings', ['start'])} data-testid="home-setup-all">
        {leftText(view)}
      </a>
    </section>
  )
}
