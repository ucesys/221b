import { useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'
import { askSherlock, newSherlockChat, stopSherlock, SherlockError, usd, type AskContext, type SherlockHand, type SherlockMsg, type SherlockRef, type SherlockState } from '../api/sherlock'
import { useShowCosts } from '../lib/costs'
import { saveTestCase } from '../api/ai'
import { Markdown } from '../lib/markdown'
import { peekClickHash } from '../lib/peek'
import { clusterHref, currentRoute, FIRST_TAB } from '../lib/router'
import { canPopOut, parseRef, post, refTarget, type RefTarget } from '../lib/sherlock'
import { CheckIcon, CopyIcon, CrossIcon, SendIcon, SherlockIcon, WatsonIcon } from './Icons'
import { isCommands } from '../api/watson'
import { autoRunReads, useWatsonApi } from './Watson'
import { isUndone, onUndone } from '../lib/uiControl'
import { askContext, askShow, askUndo, followedWindow, useSharedAnswering } from '../lib/uiWindow'
import { copyText } from '../lib/clipboard'
import { sherlockSummary } from '../lib/reportLink'
import { ReportPageLink } from './aiops/ReportPageLink'
import { mayReport, useIssueMe } from '../lib/cases'

// ---------- asking from anywhere ----------

// The context lives in its own small module so a wrench menu (and the
// browser tests that import the table columns) need not load this pane.
import { SherlockContext, useSherlockApi, type SherlockApi } from './sherlockContext'
export { SherlockContext, useSherlockApi, type SherlockApi }

/**
 * Starts a question to Sherlock from a drawer or a callout (P5-01): the
 * panel opens, or the separate window comes forward, with the answer.
 * Nothing shows while Sherlock is off for the cluster.
 */
export function AskSherlock({ question, label = 'Ask Sherlock', compact, over, testId = 'ask-sherlock' }: { question: string; label?: string; compact?: boolean; over?: boolean; testId?: string }) {
  const { available, ask } = useSherlockApi()
  if (!available) return null
  return (
    <button className="rk-btn rk-ask" onClick={() => ask(question, over)} title={`Ask Sherlock: “${question}”`} style={{ height: compact ? 26 : 30 }} data-testid={testId} data-question={question}>
      <span className="rk-avatar" style={{ width: 18, height: 18 }} aria-hidden="true">
        <SherlockIcon size={10} />
      </span>
      {label}
    </button>
  )
}

// ---------- the conversation ----------

const SUGGESTIONS: [string, string][] = [
  ['What needs attention right now?', 'Ranks the alerts by impact'],
  ['Which pods use the most memory?', 'Uses live usage from metrics-server'],
  ['Which nodes cannot be drained now, and why?', 'Checks disruption budgets and local storage'],
  ['Summarize the fleets', 'Groups nodes by their labels and taints'],
  ['Who can read Secrets?', 'Traces every binding that allows it'],
  ['Which Helm releases failed, and why?', 'Reads each release’s history'],
]

function CodeBlock({ text, lang, open, why, draft }: { text: string; lang: string; open: boolean; why?: string; draft?: boolean }) {
  const [copied, setCopied] = useState(false)
  const [sent, setSent] = useState(false)
  const watson = useWatsonApi()
  const copy = async () => {
    if (!(await copyText(text))) {
      setCopied(false)
      return
    }
    setCopied(true)
    setTimeout(() => setCopied(false), 1600)
  }
  const shell = /^(sh|bash|shell|console|zsh)?$/.test(lang) && /^\s*(kubectl|helm)\s/m.test(text)
  const commands = shell && isCommands(text)
  return (
    <div className="rk-cblk" data-wrap={shell ? 'true' : 'false'} data-testid="sherlock-code">
      <pre aria-label={shell ? 'Commands' : 'Code'}>{text}</pre>
      {!open && (
        <div>
          {/* On a case (CF-48), the commands go to the case's Watson: a change waits on its card for an approval, a read runs as Auto-run reads says. */}
          {commands && watson.available && !draft && watson.onCase && <RunInWatson onClick={() => watson.send(text, { from: 'Sherlock', why })} />}
          {commands && watson.available && !draft && !watson.onCase && (
            <button
              className="rk-btn"
              data-on={sent ? 'false' : 'true'}
              onClick={() => {
                watson.send(text, { from: 'Sherlock', why })
                setSent(true)
              }}
              disabled={sent}
              title="Hands these commands to Watson. Nothing changes until someone approves them there."
              data-testid="sherlock-send-watson"
            >
              <WatsonIcon />
              {sent ? 'Sent to Watson' : 'Send to Watson'}
            </button>
          )}
          <button className="rk-btn" onClick={() => void copy()} title={shell ? 'Copy the commands. Sherlock never runs them: Watson does, once they are approved.' : 'Copy'}>
            <CopyIcon />
            {copied ? 'Copied' : 'Copy'}
          </button>
        </div>
      )}
    </div>
  )
}

/**
 * Run in Watson on a case (CF-48): the same button on Sherlock's commands
 * and on the case's suggested fix. It hands the command to the drawer's
 * Watson as the dock's Send to Watson does: a change always waits on its
 * card for an approval; a read runs at once while Auto-run reads is on,
 * and otherwise waits for Run. What it says follows that setting, read
 * again as the pointer or the focus reaches it.
 */
export function RunInWatson({ onClick, disabled, testId = 'sherlock-run-watson', title }: { onClick: () => void; disabled?: boolean; testId?: string; title?: string }) {
  const { onCase } = useWatsonApi()
  const [auto, setAuto] = useState(autoRunReads)
  const where = onCase === 'proposal' ? 'the proposed change' : 'the case'
  const tip = title ?? runInWatsonTip(where, auto)
  const look = () => setAuto(autoRunReads())
  return (
    <button className="rk-btn" data-on="true" onClick={onClick} onMouseEnter={look} onFocus={look} disabled={disabled} title={tip} data-testid={testId}>
      <WatsonIcon />
      Run in Watson
    </button>
  )
}

/** What Run in Watson does, as Watson's Auto-run reads has it (CF-48). */
export function runInWatsonTip(where: string, autoReads: boolean): string {
  return autoReads
    ? `Hands the command to Watson beside ${where}. A change waits there on its card until you approve it, and runs as you; a read runs at once, as Auto-run reads is on in Watson.`
    : `Hands the command to Watson beside ${where}. It waits there on its card: a change until you approve it, a read until you press Run; it runs as you.`
}

function Steps({ m }: { m: SherlockMsg }) {
  if (!m.steps?.length) return null
  return (
    <div className="rk-steps" data-testid="sherlock-steps">
      {m.steps.map((s, i) => (
        <span key={i} data-status={s.status}>
          {s.status === 'done' ? <CheckIcon /> : s.status === 'failed' ? <CrossIcon /> : <span className="rk-spin rk-spin-ui" aria-hidden="true" />}
          {s.label}
          {s.status === 'failed' && <span className="rk-sr"> (did not succeed)</span>}
        </span>
      ))}
    </div>
  )
}

/** An agent named in a sentence: "the Troubleshooter", but "Mycroft". */
export function agentRef(name: string): string {
  return name === 'Sherlock' || name === 'Mycroft' ? name : `the ${name}`
}

const capital = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

// --- PM-U1: Sherlock is still running, said above the question box ---

/** The running hand-over deepest in an answer: who is working now. */
function runningHand(hands?: SherlockHand[]): SherlockHand | undefined {
  for (const h of hands ?? []) {
    if (h.status !== 'running') continue
    return runningHand(h.hands) ?? h
  }
  return undefined
}

/** What the banner says Sherlock is doing, from its latest answer; first match wins. */
export function workingLabel(m: SherlockMsg | undefined, sending: boolean): string {
  const h = runningHand(m?.hands)
  if (h) return `${capital(agentRef(h.toName))} is working`
  const step = [...(m?.steps ?? [])].reverse().find((s) => s.status === 'running')
  if (step) return step.label
  if (m?.status === 'answering' && m.text) return 'Writing the answer'
  return sending ? 'Sending' : 'Thinking'
}

/** m:ss since a time. */
export function elapsed(from: string | undefined, now: number): string {
  const t = from ? Date.parse(from) : NaN
  const s = Number.isNaN(t) ? 0 : Math.max(0, Math.floor((now - t) / 1000))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

const TITLE_MARK = '● '

/**
 * The line above the question box while Sherlock works (PM-U1): a spinner,
 * what it is doing, how many reads, how long, and Stop. While it shows, the
 * page's title starts with a dot, for a tab in the background.
 */
export function WorkingBanner({ m, sending, onStop }: { m: SherlockMsg | undefined; sending: boolean; onStop?: () => void }) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const mark = () => {
      if (!document.title.startsWith(TITLE_MARK)) document.title = TITLE_MARK + document.title
    }
    mark()
    const t = window.setInterval(() => {
      setNow(Date.now())
      mark()
    }, 1000)
    return () => {
      window.clearInterval(t)
      if (document.title.startsWith(TITLE_MARK)) document.title = document.title.slice(TITLE_MARK.length)
    }
  }, [])
  const reads = m?.steps?.length ?? 0
  const label = workingLabel(m, sending)
  return (
    <div className="rk-banner rk-working" data-tone="info" role="status" aria-live="polite" data-testid="sherlock-working">
      <span className="rk-spin rk-spin-ui" aria-hidden="true" />
      <span className="rk-banner-title rk-working-label" title={label} data-testid="sherlock-working-label">
        {label}
      </span>
      {reads > 0 && (
        <span data-testid="sherlock-working-reads">
          {'· '}
          {reads === 1 ? '1 read' : `${reads} reads`}
        </span>
      )}
      {m?.at && !sending && (
        <span className="rk-mono" aria-hidden="true" data-testid="sherlock-working-time">
          {'· '}
          {elapsed(m.at, now)}
        </span>
      )}
      <span style={{ flex: '1 1 auto' }} />
      {onStop && (
        <button className="rk-btn" onClick={onStop} style={{ height: 26 }} data-testid="sherlock-stop">
          Stop
        </button>
      )}
    </div>
  )
}

/**
 * The parts of an answer handed to other agents (Phase 12): "Sherlock asked
 * the Troubleshooter: …", opening to what it read and what came back, with
 * its own hand-overs inside, its model and, for an administrator, its cost.
 */
export function Hands({ hands, link, admin }: { hands?: SherlockHand[]; link: (url: string, label: string) => ReactNode | null; admin: boolean }) {
  // Show costs (CF-40): off, no cost for anyone.
  const costs = useShowCosts()
  if (!hands?.length) return null
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="sherlock-hands">
      {hands.map((h) => (
        <details key={h.id} open={h.status === 'running'} data-testid="sherlock-hand" data-status={h.status} data-agent={h.to} style={{ border: '1px solid var(--line)', borderRadius: 8, background: 'var(--sunken)' }}>
          <summary style={{ cursor: 'pointer', padding: '7px 10px', fontSize: 12.5, color: 'var(--fg2)' }}>
            {capital(agentRef(h.fromName))} asked <strong>{agentRef(h.toName)}</strong>
            {h.task ? `: ${h.task.length > 160 ? h.task.slice(0, 160) + '…' : h.task}` : ''}
            {h.model && (
              <>
                {' · '}
                <span className="rk-mono">{h.model}</span>
              </>
            )}
            {admin && costs && h.costUSD ? ` · ${usd(h.costUSD)}` : ''}
            {h.status === 'running' && <span className="rk-spin rk-spin-ui" aria-label="working" style={{ marginLeft: 6 }} />}
          </summary>
          <div style={{ padding: '0 12px 10px', display: 'flex', flexDirection: 'column', gap: 6, fontSize: 12.5 }}>
            {!!h.steps?.length && (
              <div className="rk-steps">
                {h.steps.map((s, i) => (
                  <span key={i} data-status={s.status}>
                    {s.status === 'done' ? <CheckIcon /> : s.status === 'failed' ? <CrossIcon /> : <span className="rk-spin rk-spin-ui" aria-hidden="true" />}
                    {s.label}
                  </span>
                ))}
              </div>
            )}
            <Hands hands={h.hands} link={link} admin={admin} />
            {h.text && (
              <div className="rk-ans-body" data-testid="sherlock-hand-text">
                <Markdown text={h.text} link={link} code={(text, lang, _open, key) => <CodeBlock key={key} text={text} lang={lang} open={false} draft />} />
              </div>
            )}
            {(h.status === 'failed' || h.status === 'refused') && (
              <p className="rk-t" data-tone="warn" style={{ margin: 0 }}>
                {h.error || 'It could not finish.'}
              </p>
            )}
          </div>
        </details>
      ))}
    </div>
  )
}

/** Saves an answer as a test case of the agent that gave it (Phase 12): for a SuperAdmin. */
function SaveCase({ m, cluster, onToast }: { m: SherlockMsg; cluster: string; onToast: (msg: string) => void }) {
  const [busy, setBusy] = useState(false)
  return (
    <button
      className="rk-link"
      style={{ fontSize: 11, alignSelf: 'flex-start' }}
      disabled={busy}
      data-testid="sherlock-save-case"
      onClick={async () => {
        setBusy(true)
        try {
          const c = await saveTestCase(cluster, m.id, '', [])
          onToast(`Saved as test case ${c.id}, with its tool results recorded: it runs with the agent’s tests in Settings › Agents.`)
        } catch (e) {
          onToast(e instanceof Error ? e.message : String(e))
        } finally {
          setBusy(false)
        }
      }}
    >
      Save as test case
    </button>
  )
}

/**
 * Report this (Phase 12, CS-18): once a conversation has ended, an offer to
 * report the problem if it is not fixed, which opens the report page with
 * the last question and answer filled in, for the person to edit first.
 */
function ReportThis({ msgs, clusterName }: { msgs: SherlockMsg[]; clusterName: string }) {
  const im = useIssueMe()
  const last = msgs[msgs.length - 1]
  if (!mayReport(im) || !last || last.role !== 'sherlock' || !(last.status === 'done' || last.status === 'failed' || last.status === 'stopped')) return null
  const answer = last.text || last.error || ''
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, padding: '8px 10px', border: '1px dashed var(--line2)', borderRadius: 8, fontSize: 12.5, color: 'var(--fg2)' }} data-testid="sherlock-report-box">
      Still broken, or someone else should look?
      <ReportPageLink text={() => sherlockSummary(clusterName, questionBefore(msgs, last.id) ?? '', answer)} testId="sherlock-report">
        Report this ↗
      </ReportPageLink>
      <span>Opens the report page with this summary filled in, for you to edit before sending.</span>
    </div>
  )
}

function seconds(from?: string, to?: string): string {
  if (!from || !to) return ''
  const s = (Date.parse(to) - Date.parse(from)) / 1000
  return s < 10 ? `${s.toFixed(1)} s` : `${Math.round(s)} s`
}

/** A link inside this console: a tab of this cluster or another, with plain parameters. */
const CONSOLE_HASH = /^#\/(?:c\/[a-z0-9][a-z0-9-]*\/)?[a-z]+(?:\/[A-Za-z0-9._~%-]+)*(?:\?[A-Za-z0-9._~%=&,:+-]*)?$/

function refLabel(r: SherlockRef): string {
  const kind = r.kind === 'HelmRelease' ? 'release' : r.kind === 'PersistentVolumeClaim' ? 'claim' : r.kind.toLowerCase()
  return `Open ${kind} ${r.name}`
}

/**
 * Under an answer that changed the view (P6U-10): what it changed, read
 * out as each change lands, and Undo, which puts the view back as it was
 * before the answer's first change. Neither takes focus.
 */
function ViewNotice({ m, cluster, onToast }: { m: SherlockMsg; cluster: string; onToast: (msg: string) => void }) {
  const done = useSyncExternalStore(onUndone, () => isUndone(cluster, m.id))
  const [busy, setBusy] = useState(false)
  const changes = m.view ?? []
  return (
    <div className="rk-viewnote" data-testid="sherlock-view-changed">
      <div role="status" aria-live="polite" style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
        <strong style={{ fontSize: 12, fontWeight: 600 }}>{done ? 'Sherlock’s changes to this view were undone' : 'Sherlock changed this view'}</strong>
        <ul style={{ margin: 0, paddingLeft: 16, fontSize: 12, color: 'var(--fg2)' }} data-testid="sherlock-view-changes">
          {changes.map((c, i) => (
            <li key={i}>{c.label}</li>
          ))}
        </ul>
      </div>
      {!done && (
        <button
          className="rk-btn"
          data-testid="sherlock-view-undo"
          disabled={busy}
          onClick={async () => {
            setBusy(true)
            const ok = await askUndo(cluster, m.id)
            setBusy(false)
            if (!ok) onToast('That view is not open in any window now, so there is nothing to undo.')
          }}
        >
          Undo
        </button>
      )}
    </div>
  )
}

/** Why an answer's view changes were left, for the line above Show in 221B (SR-15). */
function leftWhy(show: NonNullable<SherlockMsg['show']>): string {
  if (show.some((s) => s.why === 'unsaved')) return 'Sherlock left your view as it was: you have unsaved changes open.'
  if (show.some((s) => s.why === 'moved')) return 'Sherlock left your view as you set it during the answer.'
  if (show.some((s) => s.why === 'stopped')) return 'The answer ended before it was done, so Sherlock left your view as it was.'
  return 'The console window did not answer in time, so the view was left as it was.'
}

/**
 * Under an answer whose view changes were left undone (SR-15): why, and
 * Show in 221B, which applies them when the person presses it. With unsaved
 * changes still open it asks first, and leaves them unless told to.
 */
function ShowNotice({ m, cluster }: { m: SherlockMsg; cluster: string }) {
  const [busy, setBusy] = useState(false)
  const [said, setSaid] = useState<{ text: string; tone?: string } | null>(null)
  const [ask, setAsk] = useState<string[] | null>(null)
  const show = m.show ?? []
  const go = async (force: boolean) => {
    setBusy(true)
    setAsk(null)
    const r = await askShow(
      show.map((s) => ({ action: s.action, input: s.input })),
      force,
      cluster,
    )
    setBusy(false)
    if (!r) setSaid({ text: 'No console window is open to show it in.', tone: 'err' })
    else if (r.unsaved) setAsk(r.unsaved)
    else if (r.failed.length) setSaid({ text: `${r.applied ? 'Shown in part: ' : 'Not shown: '}${r.failed.join(' ')}`, tone: 'err' })
    else setSaid({ text: 'Shown.' })
  }
  return (
    <div className="rk-viewnote" data-testid="sherlock-view-left">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }}>
        <strong style={{ fontSize: 12, fontWeight: 600 }}>{leftWhy(show)}</strong>
        {ask && (
          <span style={{ fontSize: 12, color: 'var(--fg2)' }} data-testid="sherlock-show-unsaved">
            {ask.join(', ')} still {ask.length === 1 ? 'has' : 'have'} unsaved changes, which showing this leaves behind.
          </span>
        )}
        <span role="status" aria-live="polite" className="rk-t" data-tone={said?.tone} style={{ fontSize: 12 }} data-testid="sherlock-show-said">
          {said?.text ?? ''}
        </span>
      </div>
      {ask ? (
        <span
          style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}
          // Escape here is Stay, not closing the panel.
          onKeyDown={(e) => {
            if (e.key !== 'Escape') return
            e.preventDefault()
            e.stopPropagation()
            setAsk(null)
          }}
        >
          <button className="rk-btn" disabled={busy} onClick={() => void go(true)} data-testid="sherlock-show-anyway">
            Leave them and show
          </button>
          <button className="rk-btn" disabled={busy} onClick={() => setAsk(null)} data-testid="sherlock-show-stay">
            Stay
          </button>
        </span>
      ) : (
        <button className="rk-btn" disabled={busy} onClick={() => void go(false)} data-testid="sherlock-show">
          Show in 221B
        </button>
      )}
    </div>
  )
}

function Answer({
  m,
  cluster,
  link,
  admin,
  question,
  onToast,
  testCase = true,
  scope,
  views = true,
}: {
  m: SherlockMsg
  cluster: string
  link: (url: string, label: string) => ReactNode | null
  admin: boolean
  question?: string
  onToast: (msg: string) => void
  testCase?: boolean
  /** What names the conversation to the view actions, when not the cluster: a shared one's (SR-19). */
  scope?: string
  /** Whether its view changes are this person's, to undo or show: on a shared conversation, only the asker's (SR-19). */
  views?: boolean
}) {
  const thinking = (m.status === 'thinking' || m.status === 'answering') && !m.text
  const costs = useShowCosts()
  const meta = [m.model, m.doneAt ? seconds(m.at, m.doneAt) : '', admin && costs && m.costUSD ? usd(m.costUSD) : ''].filter(Boolean).join(' · ')
  return (
    <div className="rk-ans" data-testid="sherlock-answer" data-status={m.status}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5, fontWeight: 600, color: 'var(--fg2)' }}>
        <span className="rk-avatar" aria-hidden="true">
          <SherlockIcon />
        </span>
        Sherlock
      </div>
      <Steps m={m} />
      <Hands hands={m.hands} link={link} admin={admin} />
      {thinking && (
        <div className="rk-steps">
          <span className="rk-dots">
            {m.steps?.length ? 'Reading the cluster' : 'Thinking'}
            <i>.</i>
            <i>.</i>
            <i>.</i>
          </span>
        </div>
      )}
      {!m.text && m.note && (
        <div className="rk-ans-body" style={{ color: 'var(--fg3)' }} aria-label="What Sherlock said before reading more" data-testid="sherlock-note">
          <Markdown text={m.note} link={link} code={(text, lang, _open, key) => <CodeBlock key={key} text={text} lang={lang} open={false} draft />} />
        </div>
      )}
      {m.text && (
        <div className="rk-ans-body" data-testid="sherlock-text">
          <Markdown
            text={m.text}
            link={link}
            code={(text, lang, open, key) => <CodeBlock key={key} text={text} lang={lang} open={open && m.status !== 'done'} why={question ? `Sherlock proposed this answering “${question}”.` : undefined} />}
          />
        </div>
      )}
      {views && (m.view?.length ?? 0) > 0 && <ViewNotice m={m} cluster={scope ?? cluster} onToast={onToast} />}
      {views && (m.show?.length ?? 0) > 0 && m.status !== 'thinking' && m.status !== 'answering' && <ShowNotice m={m} cluster={cluster} />}
      {m.status === 'stopped' && (
        <p className="rk-t" data-tone="muted" style={{ fontSize: 12.5 }} data-testid="sherlock-stopped">
          {m.error || 'Stopped.'}
        </p>
      )}
      {m.status === 'failed' && (
        <p className="rk-t" data-tone="err" role="alert" style={{ fontSize: 12.5 }} data-testid="sherlock-error">
          {m.error || 'Sherlock could not answer.'}
        </p>
      )}
      {m.status === 'done' && (m.refs?.length ?? 0) > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }} data-testid="sherlock-refs">
          {m.refs!.slice(0, 3).map((r) => {
            const el = link(`221b://${r.kind}/${r.namespace ? r.namespace + '/' : ''}${r.name}`, refLabel(r) + ' →')
            return el ? (
              <span key={`${r.kind}/${r.namespace}/${r.name}`} className="rk-refbtn">
                {el}
              </span>
            ) : null
          })}
        </div>
      )}
      {m.status === 'done' && meta && (
        <span style={{ fontSize: 11, color: 'var(--fg3)' }} data-testid="sherlock-meta">
          {meta}
        </span>
      )}
      {m.status === 'done' && admin && testCase && <SaveCase m={m} cluster={cluster} onToast={onToast} />}
    </div>
  )
}

interface PaneProps {
  cluster: string
  clusterName: string
  state: SherlockState
  /** In Sherlock's own window: links open in the main window. */
  inWindow?: boolean
  /** The panel's controls, when docked. */
  controls?: ReactNode
  /** The Sherlock and Watson switch on narrow screens. */
  switcher?: ReactNode
  onToast: (msg: string) => void
  /** A question to put in the box. It waits there for the person to send it. */
  fill?: string
  onFilled?: () => void
  /** A conversation that is not the person's own: a case's (CF-47). */
  shared?: SharedChat
}

/**
 * A conversation the people who work a case share (Phase 12, wave 6f,
 * CF-47): asked and stopped on the case, each question naming its asker;
 * no New chat, no Report this, no test cases.
 */
export interface SharedChat {
  /** Asks, with the asker's window and view: Sherlock's view changes go to that window only (SR-19). */
  ask: (question: string, ctx?: AskContext) => Promise<unknown>
  /** What names the conversation to the view actions: its address (SR-19). */
  scope: string
  stop: () => Promise<unknown>
  /** The line beside the title. */
  sub: string
  /** What the conversation says while it is empty. */
  empty: string
  /** Why this person may only read it, when so. */
  readOnly?: string
}

/** The question an answer answers. */
function questionBefore(msgs: SherlockMsg[], id: number): string | undefined {
  const i = msgs.findIndex((m) => m.id === id)
  for (let j = i - 1; j >= 0; j--) if (msgs[j]!.role === 'user') return msgs[j]!.text
  return undefined
}

// The questions asked from this browser, newest last, for the arrow keys
// to recall (asked October 30, 2026): kept in the browser, at most fifty.
const HISTORY_KEY = 'sherlock.history'
const HISTORY_MAX = 50

export function loadHistory(): string[] {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(HISTORY_KEY) ?? '[]')
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string').slice(-HISTORY_MAX) : []
  } catch {
    return []
  }
}

/** Adds a question to the history; the same question again moves to the end. */
export function remember(list: string[], q: string): string[] {
  const next = [...list.filter((x) => x !== q), q].slice(-HISTORY_MAX)
  try {
    localStorage.setItem(HISTORY_KEY, JSON.stringify(next))
  } catch {
    // A browser without storage recalls nothing after a reload.
  }
  return next
}

/**
 * What the arrow keys do in the question box: ArrowUp on the first line
 * recalls the question before the one shown (the draft is kept to come
 * back to), ArrowDown on the last line the one after, and past the newest
 * the draft again; elsewhere in a multi-line draft they move the caret.
 * Returns the text to show, or null to let the key be.
 */
export function recall(key: 'ArrowUp' | 'ArrowDown', value: string, caret: number, history: string[], at: { index: number; draft: string }): string | null {
  if (history.length === 0) return null
  if (key === 'ArrowUp') {
    if (value.lastIndexOf('\n', caret - 1) !== -1) return null
    if (at.index === -1) at.draft = value
    if (at.index + 1 >= history.length) return history[0]!
    at.index += 1
    return history[history.length - 1 - at.index]!
  }
  if (value.indexOf('\n', caret) !== -1 || at.index === -1) return null
  at.index -= 1
  return at.index === -1 ? at.draft : history[history.length - 1 - at.index]!
}

// A question being typed outlives the pane: closed to its icon (Escape,
// SR-17) and opened again, the box has it back. Per cluster, in memory only.
const keptDrafts = new Map<string, string>()

/** Sherlock's pane: the conversation and the question box. */
export function SherlockPane({ cluster, clusterName, state, inWindow, controls, switcher, onToast, fill, onFilled, shared }: PaneProps) {
  const [draft, setDraft0] = useState(fill || (shared ? '' : (keptDrafts.get(cluster) ?? '')))
  const setDraft = (text: string) => {
    setDraft0(text)
    if (!shared) keptDrafts.set(cluster, text)
  }
  const history = useRef<string[]>(loadHistory())
  const at = useRef({ index: -1, draft: '' })
  const box = useRef<HTMLTextAreaElement>(null)
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')
  const scroller = useRef<HTMLDivElement>(null)
  const pinned = useRef(true)
  const status = state.status
  const conv = state.conv
  const msgs = conv?.messages ?? []
  const busy = !!conv?.busy
  const canAsk = !!status?.available && !busy && !sending
  // On a shared conversation, the answers to the questions asked here: their
  // view changes are this window's, as in the person's own (SR-19).
  const [mine, setMine] = useState<ReadonlySet<number>>(() => new Set())
  useSharedAnswering(shared?.scope ?? '', shared ? conv : null, mine)

  // A question handed over waits in the box; it is the person who asks it.
  const [filled, setFilled] = useState(fill)
  if (fill !== filled) {
    setFilled(fill)
    if (fill) setDraft(fill)
  }
  useEffect(() => {
    if (!fill) return
    at.current.index = -1
    box.current?.focus()
    onFilled?.()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fill])

  // Keep the newest part in view while an answer arrives, unless the
  // reader has scrolled up.
  useLayoutEffect(() => {
    const el = scroller.current
    if (el && pinned.current) el.scrollTop = el.scrollHeight
  })

  const send = async (q: string) => {
    q = q.trim()
    if (!q || !canAsk) return
    setSending(true)
    setError('')
    pinned.current = true
    try {
      // Every Sherlock drives the main window: the dock, its own window, a
      // case's and a proposal's (SR-19).
      const ctx = await askContext(cluster)
      if (shared) {
        const asked = (await shared.ask(q, ctx)) as { messages?: SherlockMsg[] } | undefined
        const answer = asked?.messages?.[asked.messages.length - 1]
        if (answer?.role === 'sherlock') setMine((s) => new Set(s).add(answer.id))
      } else await askSherlock(cluster, q, ctx)
      history.current = remember(history.current, q)
      at.current = { index: -1, draft: '' }
      setDraft('')
    } catch (e) {
      setError(e instanceof SherlockError ? e.message : String(e))
    } finally {
      setSending(false)
    }
  }

  const link = (url: string, label: string): ReactNode | null => {
    if (/^https?:\/\//.test(url)) {
      return (
        <a href={url} target="_blank" rel="noreferrer noopener" className="rk-link">
          {label}
        </a>
      )
    }
    // A link to another tab or cluster of this console, which Sherlock
    // offers rather than switching clusters (P6U-06).
    if (CONSOLE_HASH.test(url)) {
      const t: RefTarget = { href: url }
      return (
        <a
          href={url}
          className="rk-link"
          data-testid="sherlock-console-link"
          onClick={(e) => {
            if (!inWindow) return
            e.preventDefault()
            post({ type: 'open', cluster, target: t, window: followedWindow() })
          }}
        >
          {label}
        </a>
      )
    }
    const r = parseRef(url)
    const t = r ? refTarget(r, cluster) : null
    if (!t) return null
    const title = r ? `${r.kind} ${r.namespace ? r.namespace + '/' : ''}${r.name}` : undefined
    if (inWindow) {
      return (
        <a
          href={'peek' in t ? '#' : t.href}
          className="rk-link rk-cite"
          title={`${title} — opens in the main window`}
          data-cite={url}
          onClick={(e) => {
            e.preventDefault()
            post({ type: 'open', cluster, target: t, window: followedWindow() })
          }}
        >
          {label}
        </a>
      )
    }
    if ('peek' in t) {
      // The drawer opens over a cluster's tabs; from the home page or a
      // connection's page, the link goes to the first tab with it open.
      if (currentRoute().view !== 'tabs') {
        return (
          <a href={clusterHref(cluster, FIRST_TAB, [], { peek: t.peek })} className="rk-link rk-cite" title={title} data-cite={url}>
            {label}
          </a>
        )
      }
      return (
        <a href={peekClickHash(t.peek)} data-peek={t.peek} className="rk-link rk-cite" title={title} data-cite={url}>
          {label}
        </a>
      )
    }
    return (
      <a href={t.href} className="rk-link rk-cite" title={title} data-cite={url}>
        {label}
      </a>
    )
  }

  const stop = async () => {
    try {
      await (shared ? shared.stop() : stopSherlock(cluster))
    } catch (e) {
      onToast(e instanceof Error ? e.message : String(e))
    }
  }
  const fresh = async () => {
    try {
      await newSherlockChat(cluster)
      setError('')
    } catch (e) {
      onToast(e instanceof Error ? e.message : String(e))
    }
  }

  const lastAnswer = [...msgs].reverse().find((m) => m.role === 'sherlock')
  const unavailable = status && !status.available ? status.reason || status.capped || '' : ''
  return (
    <section className="rk-dpane rk-sher" aria-label="Sherlock" data-testid="sherlock" data-shared={shared ? 'true' : undefined}>
      <div className="rk-dph">
        <span className={'rk-avatar' + (switcher ? ' rk-wide' : '')} aria-hidden="true">
          <SherlockIcon />
        </span>
        <h2 className={switcher ? 'rk-wide' : undefined}>Sherlock</h2>
        <small className={switcher && !shared ? 'rk-wide' : undefined} title={shared?.sub ?? `Answers questions about ${clusterName}`}>
          {shared?.sub ?? `Answers questions about ${clusterName}`}
        </small>
        {status?.setup && (
          <span className="rk-pill" data-tone="info" title="Getting started has steps open: ask “What’s next?”" data-testid="sherlock-setup">
            setup
          </span>
        )}
        {switcher}
        <span style={{ flex: '1 1 auto' }} />
        {msgs.length > 0 && !busy && !shared && (
          <button className="rk-btn" onClick={() => void fresh()} style={{ height: 28 }} data-testid="sherlock-new">
            New chat
          </button>
        )}
        {controls}
      </div>
      <div
        className="rk-chat"
        ref={scroller}
        // Scrollable with the keyboard, as Watson's log is (WCAG 2.1.1).
        tabIndex={0}
        role="region"
        aria-label="Conversation with Sherlock"
        onScroll={(e) => {
          const el = e.currentTarget
          pinned.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 48
        }}
      >
        <div style={{ maxWidth: 900, margin: '0 auto', display: 'flex', flexDirection: 'column', gap: 16 }}>
          {msgs.length === 0 && shared && (
            <p style={{ margin: 0, maxWidth: '82ch', fontSize: 12.5, color: 'var(--fg2)' }} data-testid="sherlock-empty">
              {shared.empty}
            </p>
          )}
          {msgs.length === 0 && !shared && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }} data-testid="sherlock-empty">
              <p style={{ margin: 0, maxWidth: '82ch', fontSize: 12.5, color: 'var(--fg2)' }}>
                Ask about the nodes, fleets, pods, releases, access and alerts in {clusterName}. Sherlock reads only what you may read, never sees Secret values, and changes nothing: when something needs a change, it hands the command to
                Watson, where nothing runs until someone approves it.
              </p>
              {status?.available && (
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                  {SUGGESTIONS.map(([q, sub]) => (
                    <button key={q} className="rk-fchip" onClick={() => void send(q)} title={sub} style={{ fontFamily: 'var(--font)', fontSize: 12 }} disabled={!canAsk}>
                      {q}
                    </button>
                  ))}
                </div>
              )}
            </div>
          )}
          <div aria-live="polite" style={{ display: 'flex', flexDirection: 'column', gap: 20 }} data-testid="sherlock-messages">
            {msgs.map((m) =>
              m.role === 'user' ? (
                <div key={m.id} className="rk-chat-user" data-testid="sherlock-question">
                  {m.asker && (
                    <span style={{ display: 'block', fontSize: 11, fontWeight: 600, opacity: 0.8 }} data-testid="sherlock-asker">
                      {m.asker} asked
                    </span>
                  )}
                  {m.text}
                </div>
              ) : (
                <Answer key={m.id} m={m} cluster={cluster} link={link} admin={!!status?.admin} question={questionBefore(msgs, m.id)} onToast={onToast} testCase={!shared} scope={shared?.scope} views={!shared || mine.has(m.id)} />
              ),
            )}
          </div>
          {!busy && msgs.length > 0 && !shared && <ReportThis msgs={msgs} clusterName={clusterName} />}
        </div>
      </div>
      {(busy || sending) && <WorkingBanner m={busy ? lastAnswer : undefined} sending={sending && !busy} onStop={busy && !shared?.readOnly ? () => void stop() : undefined} />}
      {shared?.readOnly ? (
        <div className="rk-cmp" data-testid="sherlock-read-only">
          <p className="rk-t" data-tone="muted" style={{ margin: 0, fontSize: 12.5, padding: '6px 4px' }} role="status">
            {shared.readOnly}
          </p>
        </div>
      ) : unavailable ? (
        <div className="rk-cmp" data-testid="sherlock-unavailable">
          <p className="rk-t" data-tone={status?.capped ? 'warn' : 'muted'} style={{ margin: 0, fontSize: 12.5, padding: '6px 4px' }} role="status">
            {unavailable}
          </p>
        </div>
      ) : (
        <div className="rk-cmp">
          <label className="rk-field rk-ta" style={{ flex: '1 1 auto' }}>
            <span className="rk-sr">{shared ? 'Ask Sherlock about this case' : 'Ask Sherlock about this cluster'}</span>
            <textarea
              ref={box}
              rows={2}
              placeholder={
                busy
                  ? 'Sherlock is working. Stop to ask something else.'
                  : shared
                    ? 'Ask Sherlock about this case — everyone who works it sees the conversation; Enter sends'
                    : 'Ask Sherlock — Enter sends, Shift+Enter starts a new line, ↑ recalls an earlier question'
              }
              value={draft}
              maxLength={4000}
              onChange={(e) => {
                at.current.index = -1
                setDraft(e.target.value)
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                  e.preventDefault()
                  void send(draft)
                  return
                }
                if ((e.key === 'ArrowUp' || e.key === 'ArrowDown') && !e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey) {
                  const el = e.currentTarget
                  const text = recall(e.key, el.value, e.key === 'ArrowUp' ? el.selectionStart : el.selectionEnd, history.current, at.current)
                  if (text === null) return
                  e.preventDefault()
                  setDraft(text)
                  requestAnimationFrame(() => box.current?.setSelectionRange(text.length, text.length))
                }
              }}
              data-testid="sherlock-input"
            />
          </label>
          <button className="rk-send" onClick={() => void send(draft)} disabled={!canAsk || !draft.trim()} data-testid="sherlock-send">
            <SendIcon />
            Ask
          </button>
        </div>
      )}
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, padding: '0 14px 10px', fontSize: 12.5 }} data-testid="sherlock-ask-error">
          {error}
        </p>
      )}
      {inWindow && <span hidden data-testid="sherlock-window" />}
    </section>
  )
}

/** What a link from Sherlock's window opens in the main one. */
export function openTarget(t: RefTarget): string | null {
  if ('peek' in t) return peekClickHash(t.peek)
  return t.href
}

export { canPopOut }
