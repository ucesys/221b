import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { fetchPilot, type PilotEntry } from '../../api/ai'
import { usd } from '../../api/sherlock'
import { useShowCosts } from '../../lib/costs'
import { trapTab } from '../../lib/focus'
import { fmtTs } from '../../lib/format'
import { layer } from '../../lib/layer'
import { CloseIcon } from '../Icons'

interface Props {
  agentId: string
  agentName: string
  onClose: () => void
  /** Called with the number of answers read, to keep the drawer's count right. */
  onCount?: (n: number) => void
}

/** How many answers show at first, and how many more each "Show more" adds. */
export const PILOT_PAGE = 25

/** The answers newest first, by their number. */
export function newestFirst(entries: PilotEntry[]): PilotEntry[] {
  return [...entries].sort((a, b) => b.n - a.n)
}

/**
 * The answers logged during the agent's pilots, both versions', to compare
 * (AI-27; not rated since AI-29): in a dialog of their own, as the test dialog is, since the
 * history can be long. Newest first, a page at a time, scrolled inside the
 * dialog. The server keeps the latest 200 answers of all agents.
 */
export function PilotHistoryDialog({ agentId, agentName, onClose, onCount }: Props) {
  const costs = useShowCosts()
  const [entries, setEntries] = useState<PilotEntry[] | null>(null)
  const [error, setError] = useState('')
  const [shown, setShown] = useState(PILOT_PAGE)
  const closeRef = useRef<HTMLButtonElement>(null)
  const counted = useRef(onCount)
  useEffect(() => {
    counted.current = onCount
  }, [onCount])
  useEffect(() => {
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : null
    closeRef.current?.focus()
    return () => before?.focus()
  }, [])
  useEffect(() => {
    let live = true
    fetchPilot(agentId).then(
      (r) => {
        if (!live) return
        const list = newestFirst(r.entries ?? [])
        setEntries(list)
        counted.current?.(list.length)
      },
      (e: unknown) => {
        if (live) setError(e instanceof Error ? e.message : String(e))
      },
    )
    return () => {
      live = false
    }
  }, [agentId])
  const list = entries ?? []
  const page = list.slice(0, shown)
  const left = list.length - page.length
  const halt = (e: { stopPropagation: () => void }) => e.stopPropagation()
  return createPortal(
    <div style={{ position: 'fixed', inset: 0, zIndex: 60, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16, whiteSpace: 'normal' }} onClick={halt} onMouseDown={halt} onKeyDown={halt}>
      <button aria-label="Close the pilot history" tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="pilot-history-title"
        className="rk-card"
        data-testid="pilot-history-dialog"
        data-state={entries ? 'ready' : 'loading'}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            onClose()
            return
          }
          trapTab(e)
        }}
        style={{ position: 'relative', width: 'min(1100px, 96vw)', maxHeight: '90vh', padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 10, boxShadow: 'var(--shadow)', minHeight: 0, overflowWrap: 'anywhere' }}
      >
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10 }}>
          <h2 id="pilot-history-title" style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>
            Pilot history of the {agentName}
          </h2>
          {entries && (
            <span className="rk-pill" data-tone="muted" data-testid="pilot-history-count">
              {list.length} answer{list.length === 1 ? '' : 's'}
            </span>
          )}
          <span style={{ flex: '1 1 auto' }} />
          <button ref={closeRef} className="rk-icon-btn" aria-label="Close the pilot history" title="Close (Esc)" onClick={onClose} data-testid="pilot-history-close">
            <CloseIcon />
          </button>
        </div>
        <p className="rk-fhint" style={{ margin: 0, fontSize: 12.5 }}>
          Every answer of the {agentName} while a draft was piloted, the draft’s and the live version’s alike, newest first, to compare the two before the draft goes live.
        </p>
        {error && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
            {error}
          </p>
        )}
        <div style={{ flex: '1 1 auto', minHeight: 0, overflow: 'auto', display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="pilot-history-list">
          {!entries ? (
            !error && (
              <p className="rk-fhint" style={{ margin: 0 }}>
                Reading the pilot history…
              </p>
            )
          ) : !list.length ? (
            <p className="rk-fhint" style={{ margin: 0 }}>
              No answer logged in this pilot yet.
            </p>
          ) : (
            <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="ins-pilot-log">
              {page.map((e) => (
                <li key={e.n} data-testid="pilot-history-entry" style={{ fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 4, border: '1px solid var(--line)', borderRadius: 8, padding: '6px 10px' }}>
                  <span className="rk-fhint">
                    {fmtTs(e.at)} · {e.who} · {e.version.includes('pilot') ? 'the draft' : 'the live version'} · {e.cluster} · {e.model}
                    {costs ? ` · ${usd(e.costUSD)}` : ''}
                  </span>
                  <strong>{e.question}</strong>
                  <span style={{ whiteSpace: 'pre-wrap' }}>{e.answer}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
        {left > 0 && (
          <span style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            <button className="rk-btn" onClick={() => setShown(shown + PILOT_PAGE)} data-testid="pilot-history-more">
              Show {Math.min(left, PILOT_PAGE)} more
            </button>
            <span className="rk-fhint">
              {page.length} of {list.length} shown
            </span>
          </span>
        )}
      </div>
    </div>,
    layer(),
  )
}
