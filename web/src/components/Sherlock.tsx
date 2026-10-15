import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type PointerEvent, type ReactNode } from 'react'
import { askSherlock, newSherlockChat, stopSherlock, SherlockError, usd, type SherlockMsg, type SherlockRef, type SherlockState } from '../api/sherlock'
import { Markdown } from '../lib/markdown'
import { peekClickHash } from '../lib/peek'
import { canPopOut, parseRef, post, refTarget, type RefTarget } from '../lib/sherlock'
import { CheckIcon, CopyIcon, CrossIcon, DockIcon, MaximizeIcon, MinimizeIcon, PopInIcon, PopOutIcon, RestoreIcon, SendIcon, SherlockIcon } from './Icons'

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

function CodeBlock({ text, lang, open }: { text: string; lang: string; open: boolean }) {
  const [copied, setCopied] = useState(false)
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
  return (
    <div className="rk-cblk" data-wrap={shell ? 'true' : 'false'} data-testid="sherlock-code">
      <pre aria-label={shell ? 'Commands' : 'Code'}>{text}</pre>
      {!open && (
        <div>
          <button className="rk-btn" onClick={() => void copy()} title={shell ? 'Copy the commands. Sherlock never runs them: Watson will, once you approve them.' : 'Copy'}>
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

/** The "Ask next" line the server takes off a finished answer, hidden while it arrives. */
function withoutAskNext(text: string): string {
  return text.replace(/\n[ \t*_]*Ask(?: n(?:e(?:x(?:t(?::[^\n]*)?)?)?)?)?\s*$/, '')
}

function refLabel(r: SherlockRef): string {
  const kind = r.kind === 'HelmRelease' ? 'release' : r.kind === 'PersistentVolumeClaim' ? 'claim' : r.kind.toLowerCase()
  return `Open ${kind} ${r.name}`
}

function Answer({ m, link, admin, onAsk, canAsk }: { m: SherlockMsg; link: (url: string, label: string) => ReactNode | null; admin: boolean; onAsk: (q: string) => void; canAsk: boolean }) {
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
          <Markdown text={m.status === 'done' ? m.text : withoutAskNext(m.text)} link={link} code={(text, lang, open, key) => <CodeBlock key={key} text={text} lang={lang} open={open && m.status !== 'done'} />} />
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
      {m.status === 'done' && (m.follow?.length ?? 0) > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 6, paddingTop: 2 }} data-testid="sherlock-follow">
          <span style={{ fontSize: 12, color: 'var(--fg3)', marginRight: 2 }}>Ask next</span>
          {m.follow!.map((q) => (
            <button key={q} className="rk-fchip" onClick={() => onAsk(q)} disabled={!canAsk} style={{ fontFamily: 'var(--font)', fontSize: 12 }}>
              {q}
            </button>
          ))}
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
  onToast: (msg: string) => void
}

/** Sherlock's pane: the conversation and the question box. */
export function SherlockPane({ cluster, clusterName, state, inWindow, controls, onToast }: PaneProps) {
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
        <span className="rk-avatar" aria-hidden="true">
          <SherlockIcon />
        </span>
        <h2>Sherlock</h2>
        <small title={`Answers questions about ${clusterName}`}>Answers questions about {clusterName}</small>
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
                Ask about the nodes, fleets, pods, releases, access and alerts in {clusterName}. Sherlock reads only what you may read, never sees Secret values, and changes nothing: when something needs a change, it gives you the command.
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
                <Answer key={m.id} m={m} link={link} admin={!!status?.admin} onAsk={(q) => void send(q)} canAsk={canAsk} />
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

// ---------- the dock ----------

export type DockMode = 'open' | 'max' | 'min'

interface DockProps extends Omit<PaneProps, 'controls' | 'inWindow'> {
  mode: DockMode
  onMode: (m: DockMode) => void
  height: number
  onHeight: (h: number) => void
  /** Moves the panel to a window of its own; undefined where the browser cannot. */
  onPopOut?: () => void
}

const MIN_H = 150

/**
 * The panel docked at the bottom of every tab (P5-01): resizable by its
 * edge or grip (the arrow keys too), maximized over the page, or
 * minimized to an icon. It can also move to a window of its own (P5-08).
 */
export function SherlockDock({ mode, onMode, height, onHeight, onPopOut, ...pane }: DockProps) {
  const drag = useRef<{ y0: number; h0: number } | null>(null)
  const section = useRef<HTMLElement>(null)
  const clamp = (h: number) => Math.round(Math.max(MIN_H, Math.min(window.innerHeight - 150, h)))
  const busy = !!pane.state.conv?.busy

  // Escape restores a maximized panel; focus moves into it and back.
  useEffect(() => {
    if (mode === 'max') section.current?.querySelector<HTMLTextAreaElement>('textarea')?.focus()
  }, [mode])

  if (mode === 'min') {
    return (
      <button className="rk-fab" aria-label={busy ? 'Open Sherlock — an answer is arriving' : 'Open Sherlock'} title="Open Sherlock" onClick={() => onMode('open')} data-testid="sherlock-fab">
        <DockIcon />
        {busy && <span className="rk-spin" aria-hidden="true" />}
      </button>
    )
  }
  const down = (e: PointerEvent<HTMLElement>) => {
    if (e.button !== 0) return
    e.currentTarget.setPointerCapture?.(e.pointerId)
    e.preventDefault()
    drag.current = { y0: e.clientY, h0: section.current?.getBoundingClientRect().height ?? height }
  }
  const move = (e: PointerEvent<HTMLElement>) => {
    const d = drag.current
    if (d) onHeight(clamp(d.h0 + d.y0 - e.clientY))
  }
  const up = (e: PointerEvent<HTMLElement>) => {
    drag.current = null
    e.currentTarget.releasePointerCapture?.(e.pointerId)
  }
  const key = (e: KeyboardEvent<HTMLButtonElement>) => {
    const step = { ArrowUp: 1, PageUp: 4, ArrowDown: -1, PageDown: -4 }[e.key]
    if (!step) return
    e.preventDefault()
    onHeight(clamp((section.current?.getBoundingClientRect().height ?? height) + step * 32))
  }
  const controls = (
    <>
      {onPopOut && (
        <button className="rk-icon-btn" aria-label="Open in a window of its own" title="Open in a window of its own — for another monitor" onClick={onPopOut} data-testid="sherlock-popout">
          <PopOutIcon />
        </button>
      )}
      <button className="rk-icon-btn" aria-label="Minimize" title="Minimize to an icon" onClick={() => onMode('min')} data-testid="sherlock-minimize">
        <MinimizeIcon />
      </button>
      <button className="rk-icon-btn" aria-label={mode === 'max' ? 'Restore to the bottom of the page' : 'Maximize'} title={mode === 'max' ? 'Restore to the bottom of the page' : 'Maximize'} onClick={() => onMode(mode === 'max' ? 'open' : 'max')} data-testid="sherlock-maximize">
        {mode === 'max' ? <RestoreIcon /> : <MaximizeIcon />}
      </button>
    </>
  )
  return (
    <>
      {mode === 'max' && <button className="rk-dock-bd" aria-label="Restore the Sherlock panel" tabIndex={-1} onClick={() => onMode('open')} />}
      <section
        ref={section}
        className="rk-dock"
        data-mode={mode}
        data-pane="sherlock"
        role={mode === 'max' ? 'dialog' : 'region'}
        aria-modal={mode === 'max' ? true : undefined}
        aria-label="Sherlock panel"
        style={{ ['--dock-cols' as string]: 'minmax(0, 1fr)' }}
        onKeyDown={(e) => {
          if (e.key === 'Escape' && mode === 'max') {
            e.stopPropagation()
            onMode('open')
          }
        }}
        data-testid="sherlock-dock"
      >
        {mode === 'open' && (
          <>
            <div className="rk-dock-edge" aria-hidden="true" onPointerDown={down} onPointerMove={move} onPointerUp={up} onPointerCancel={up} />
            <button className="rk-grip" aria-label="Resize the Sherlock panel" title="Drag to resize — the arrow keys work too" onPointerDown={down} onPointerMove={move} onPointerUp={up} onPointerCancel={up} onKeyDown={key} data-testid="sherlock-grip">
              <i />
            </button>
          </>
        )}
        <div className="rk-dock-body">
          <SherlockPane {...pane} controls={controls} />
        </div>
      </section>
    </>
  )
}

/** In the main window while Sherlock is in its own: an icon that brings it forward. */
export function PoppedOutFab({ onFocus, onDock }: { onFocus: () => void; onDock: () => void }) {
  return (
    <div className="rk-popfab" data-testid="sherlock-popped">
      <button className="rk-fab" aria-label="Sherlock is in a window of its own — bring it forward" title="Sherlock is in a window of its own — bring it forward" onClick={onFocus}>
        <DockIcon />
      </button>
      <button className="rk-btn rk-popback" onClick={onDock} title="Close Sherlock's window and dock it here again" data-testid="sherlock-dock-here">
        <PopInIcon />
        Dock here
      </button>
    </div>
  )
}

/** What a link from Sherlock's window opens in the main one. */
export function openTarget(t: RefTarget): string | null {
  if ('peek' in t) return peekClickHash(t.peek)
  return t.href
}

export { canPopOut }
