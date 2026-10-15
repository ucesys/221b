import { createContext, useContext, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { askSherlock, newSherlockChat, stopSherlock, SherlockError, usd, type SherlockMsg, type SherlockRef, type SherlockState } from '../api/sherlock'
import { Markdown } from '../lib/markdown'
import { peekClickHash } from '../lib/peek'
import { canPopOut, parseRef, post, refTarget, type RefTarget } from '../lib/sherlock'
import { CheckIcon, CopyIcon, CrossIcon, SendIcon, SherlockIcon, WatsonIcon } from './Icons'
import { isCommands } from '../api/watson'
import { useWatsonApi } from './Watson'

// ---------- asking from anywhere ----------

export interface SherlockApi {
  /** Whether questions can be asked in this cluster now. */
  available: boolean
  /** Asks; over says the asker is over the page (a drawer), so the answer shows maximized, above it. */
  ask: (question: string, over?: boolean) => void
}

export const SherlockContext = createContext<SherlockApi>({ available: false, ask: () => undefined })

export function useSherlockApi(): SherlockApi {
  return useContext(SherlockContext)
}

/**
 * Starts a question to Sherlock from a drawer or a callout (P5-01): the
 * panel opens, or the separate window comes forward, with the answer.
 * Nothing shows while Sherlock is off for the cluster.
 */
export function AskSherlock({ question, label = 'Ask Sherlock', compact, over }: { question: string; label?: string; compact?: boolean; over?: boolean }) {
  const { available, ask } = useSherlockApi()
  if (!available) return null
  return (
    <button className="rk-btn rk-ask" onClick={() => ask(question, over)} title={`Ask Sherlock: “${question}”`} style={{ height: compact ? 26 : 30 }} data-testid="ask-sherlock" data-question={question}>
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

function CodeBlock({ text, lang, open, why }: { text: string; lang: string; open: boolean; why?: string }) {
  const [copied, setCopied] = useState(false)
  const [sent, setSent] = useState(false)
  const watson = useWatsonApi()
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    } catch {
      setCopied(false)
    }
  }
  const shell = /^(sh|bash|shell|console|zsh)?$/.test(lang) && /^\s*(kubectl|helm)\s/m.test(text)
  const commands = shell && isCommands(text)
  return (
    <div className="rk-cblk" data-wrap={shell ? 'true' : 'false'} data-testid="sherlock-code">
      <pre aria-label={shell ? 'Commands' : 'Code'}>{text}</pre>
      {!open && (
        <div>
          {commands && watson.available && (
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

function seconds(from?: string, to?: string): string {
  if (!from || !to) return ''
  const s = (Date.parse(to) - Date.parse(from)) / 1000
  return s < 10 ? `${s.toFixed(1)} s` : `${Math.round(s)} s`
}

function refLabel(r: SherlockRef): string {
  const kind = r.kind === 'HelmRelease' ? 'release' : r.kind === 'PersistentVolumeClaim' ? 'claim' : r.kind.toLowerCase()
  return `Open ${kind} ${r.name}`
}

function Answer({ m, link, admin, question }: { m: SherlockMsg; link: (url: string, label: string) => ReactNode | null; admin: boolean; question?: string }) {
  const thinking = (m.status === 'thinking' || m.status === 'answering') && !m.text
  const meta = [m.model, m.doneAt ? seconds(m.at, m.doneAt) : '', admin && m.costUSD ? usd(m.costUSD) : ''].filter(Boolean).join(' · ')
  return (
    <div className="rk-ans" data-testid="sherlock-answer" data-status={m.status}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5, fontWeight: 600, color: 'var(--fg2)' }}>
        <span className="rk-avatar" aria-hidden="true">
          <SherlockIcon />
        </span>
        Sherlock
      </div>
      <Steps m={m} />
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
      {m.text && (
        <div className="rk-ans-body" data-testid="sherlock-text">
          <Markdown text={m.text} link={link} code={(text, lang, open, key) => <CodeBlock key={key} text={text} lang={lang} open={open && m.status !== 'done'} why={question ? `Sherlock proposed this answering “${question}”.` : undefined} />} />
        </div>
      )}
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
            return el ? <span key={`${r.kind}/${r.namespace}/${r.name}`} className="rk-refbtn">{el}</span> : null
          })}
        </div>
      )}
      {m.status === 'done' && meta && (
        <span style={{ fontSize: 11, color: 'var(--fg3)' }} data-testid="sherlock-meta">
          {meta}
        </span>
      )}
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
}

/** The question an answer answers. */
function questionBefore(msgs: SherlockMsg[], id: number): string | undefined {
  const i = msgs.findIndex((m) => m.id === id)
  for (let j = i - 1; j >= 0; j--) if (msgs[j]!.role === 'user') return msgs[j]!.text
  return undefined
}

/** Sherlock's pane: the conversation and the question box. */
export function SherlockPane({ cluster, clusterName, state, inWindow, controls, switcher, onToast }: PaneProps) {
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')
  const scroller = useRef<HTMLDivElement>(null)
  const pinned = useRef(true)
  const status = state.status
  const conv = state.conv
  const msgs = conv?.messages ?? []
  const busy = !!conv?.busy
  const canAsk = !!status?.available && !busy && !sending

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
      await askSherlock(cluster, q)
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
            post({ type: 'open', cluster, target: t })
          }}
        >
          {label}
        </a>
      )
    }
    if ('peek' in t) {
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
      await stopSherlock(cluster)
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

  const unavailable = status && !status.available ? status.reason || status.capped || '' : ''
  return (
    <section className="rk-dpane rk-sher" aria-label="Sherlock" data-testid="sherlock">
      <div className="rk-dph">
        <span className={'rk-avatar' + (switcher ? ' rk-wide' : '')} aria-hidden="true">
          <SherlockIcon />
        </span>
        <h2 className={switcher ? 'rk-wide' : undefined}>Sherlock</h2>
        <small className={switcher ? 'rk-wide' : undefined} title={`Answers questions about ${clusterName}`}>
          Answers questions about {clusterName}
        </small>
        {switcher}
        <span style={{ flex: '1 1 auto' }} />
        {busy && (
          <button className="rk-btn" onClick={() => void stop()} style={{ height: 28 }} data-testid="sherlock-stop">
            Stop
          </button>
        )}
        {msgs.length > 0 && !busy && (
          <button className="rk-btn" onClick={() => void fresh()} style={{ height: 28 }} data-testid="sherlock-new">
            New chat
          </button>
        )}
        {controls}
      </div>
      <div
        className="rk-chat"
        ref={scroller}
        onScroll={(e) => {
          const el = e.currentTarget
          pinned.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 48
        }}
      >
        <div style={{ maxWidth: 900, margin: '0 auto', display: 'flex', flexDirection: 'column', gap: 16 }}>
          {msgs.length === 0 && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }} data-testid="sherlock-empty">
              <p style={{ margin: 0, maxWidth: '82ch', fontSize: 12.5, color: 'var(--fg2)' }}>
                Ask about the nodes, fleets, pods, releases, access and alerts in {clusterName}. Sherlock reads only what you may read, never sees Secret values, and changes nothing: when something needs a change, it hands the command to Watson, where nothing runs until someone approves it.
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
                  {m.text}
                </div>
              ) : (
                <Answer key={m.id} m={m} link={link} admin={!!status?.admin} question={questionBefore(msgs, m.id)} />
              ),
            )}
          </div>
        </div>
      </div>
      {unavailable ? (
        <div className="rk-cmp" data-testid="sherlock-unavailable">
          <p className="rk-t" data-tone={status?.capped ? 'warn' : 'muted'} style={{ margin: 0, fontSize: 12.5, padding: '6px 4px' }} role="status">
            {unavailable}
          </p>
        </div>
      ) : (
        <div className="rk-cmp">
          <label className="rk-field rk-ta" style={{ flex: '1 1 auto' }}>
            <span className="rk-sr">Ask Sherlock about this cluster</span>
            <textarea
              rows={2}
              placeholder="Ask Sherlock — Enter sends, Shift+Enter starts a new line"
              value={draft}
              maxLength={4000}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                  e.preventDefault()
                  void send(draft)
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
