import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { fetchHistory, fetchHistoryItem, type HistoryEntry, type HistoryText, type TestBrief } from '../../api/ai'
import { trapTab } from '../../lib/focus'
import { fmtTs } from '../../lib/format'
import { layer } from '../../lib/layer'
import { VersionDiff } from './TextDiff'

interface Props {
  agentId: string
  agentName: string
  clusterName?: (id: string) => string
  /** Why Restore cannot be used now (unsaved edits, a change under way); "" when it can. */
  restoreBlocked?: string
  /** Restores a version once live; the dialog reads the history again after. */
  onRestore?: (n: number) => Promise<void>
  onClose: () => void
}

/** An entry's key: live:N for a version (0 the chart's baseline), draft:R for a draft. */
export function entryKey(e: HistoryEntry): string {
  return e.kind === 'live' ? `live:${e.n ?? 0}` : `draft:${e.rev ?? 0}`
}

/** An entry in words: "Version 3", "Draft 7", "The chart’s baseline". */
export function entryLabel(e: HistoryEntry): string {
  if (e.kind === 'live') return e.n ? `Version ${e.n}` : 'The chart’s baseline'
  return `Draft ${e.rev}`
}

/** A test's result in words: "5 of 6 passed, Nov 17 10:00". */
export function testText(t: TestBrief): string {
  const how = t.state === 'stopped' ? 'stopped; ' : t.state === 'failed' ? 'failed; ' : ''
  return `tested: ${how}${t.passed} of ${t.cases} passed, ${fmtTs(t.at)}`
}

/** What to compare at first: the draft now against the version in force, else the newest two. */
export function defaultPair(entries: HistoryEntry[]): [string, string] | null {
  if (!entries.length) return null
  const current = entries.find((e) => e.current)
  const inForce = entries.find((e) => e.inForce)
  if (current && inForce) return [entryKey(inForce), entryKey(current)]
  if (entries.length === 1) return [entryKey(entries[0]!), entryKey(entries[0]!)]
  return [entryKey(entries[1]!), entryKey(entries[0]!)]
}

const KIND: Record<HistoryEntry['kind'], { text: string; tone: string }> = {
  live: { text: 'Live', tone: 'ok' },
  draft: { text: 'Draft', tone: 'info' },
  discarded: { text: 'Discarded', tone: 'muted' },
}

/**
 * The agent's history (AI-32): every version once live and every draft
 * saved since the last went live, newest first, any two compared field by
 * field as Helm › History compares a release's revisions. A version once
 * live, not in force, can be restored from here as from the drawer.
 */
export function HistoryDialog({ agentId, agentName, clusterName, restoreBlocked = '', onRestore, onClose }: Props) {
  const [entries, setEntries] = useState<HistoryEntry[] | null>(null)
  const [error, setError] = useState('')
  const [pair, setPair] = useState<[string, string] | null>(null)
  const [texts, setTexts] = useState<Record<string, HistoryText>>({})
  const [busy, setBusy] = useState(false)
  const [reads, setReads] = useState(0)
  const closeRef = useRef<HTMLButtonElement>(null)
  // The sides read or being read, each once.
  const asked = useRef(new Set<string>())
  useEffect(() => {
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : null
    closeRef.current?.focus()
    return () => before?.focus()
  }, [])
  useEffect(() => {
    let live = true
    fetchHistory(agentId).then(
      (r) => {
        if (!live) return
        const list = r.entries ?? []
        setEntries(list)
        asked.current.clear()
        setTexts({})
        setPair((p) => (p && list.some((e) => entryKey(e) === p[0]) && list.some((e) => entryKey(e) === p[1]) ? p : defaultPair(list)))
      },
      (e: unknown) => live && setError(e instanceof Error ? e.message : String(e)),
    )
    return () => {
      live = false
    }
  }, [agentId, reads])
  // Each side's whole text, read once.
  useEffect(() => {
    if (!pair || !entries) return
    for (const key of new Set(pair)) {
      if (asked.current.has(key)) continue
      asked.current.add(key)
      const [kind, n] = key.split(':') as ['live' | 'draft', string]
      fetchHistoryItem(agentId, kind, Number(n)).then(
        (t) => setTexts((m) => ({ ...m, [key]: t })),
        (e: unknown) => {
          asked.current.delete(key)
          setError(e instanceof Error ? e.message : String(e))
        },
      )
    }
  }, [agentId, pair, entries])
  const list = entries ?? []
  const byKey = (k: string) => list.find((e) => entryKey(e) === k)
  const from = pair ? texts[pair[0]] : undefined
  const to = pair ? texts[pair[1]] : undefined
  const pick = (side: 0 | 1, key: string) => setPair((p) => (side === 0 ? [key, p?.[1] ?? key] : [p?.[0] ?? key, key]))
  const restore = async (n: number) => {
    if (!onRestore) return
    setBusy(true)
    setError('')
    try {
      await onRestore(n)
      setReads((x) => x + 1)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const halt = (e: { stopPropagation: () => void }) => e.stopPropagation()
  const sideName = (k?: string) => {
    const e = k ? byKey(k) : undefined
    return e ? entryLabel(e) : '—'
  }
  return createPortal(
    <div style={{ position: 'fixed', inset: 0, zIndex: 60, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16, whiteSpace: 'normal' }} onClick={halt} onMouseDown={halt} onKeyDown={halt}>
      <button aria-label="Close the history" tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="history-dialog-title"
        className="rk-card"
        data-testid="history-dialog"
        data-state={entries ? 'ready' : 'loading'}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            onClose()
            return
          }
          trapTab(e)
        }}
        style={{ position: 'relative', width: 'min(1400px, 96vw)', height: '90vh', padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 10, boxShadow: 'var(--shadow)', minHeight: 0, overflowWrap: 'anywhere' }}
      >
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10 }}>
          <h2 id="history-dialog-title" style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>
            History of the {agentName}
          </h2>
          {pair && (
            <span style={{ fontSize: 12.5 }} data-testid="history-pair">
              <b style={{ fontWeight: 600 }}>{sideName(pair[0])}</b> → <b style={{ fontWeight: 600 }}>{sideName(pair[1])}</b>
            </span>
          )}
          <span style={{ flex: '1 1 auto' }} />
          <button ref={closeRef} className="rk-btn" title="Close (Esc)" onClick={onClose} data-testid="history-close">
            Close
          </button>
        </div>
        <p className="rk-fhint" style={{ margin: 0, fontSize: 12.5 }}>
          Every version that went live, and every draft saved since the last one did, newest first. Pick any two to compare them: From is the older side, To the newer. The drafts are kept until a draft goes live; the one that does is kept
          as its version.
        </p>
        {error && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
            {error}
          </p>
        )}
        <div style={{ flex: '1 1 auto', minHeight: 0, display: 'flex', flexWrap: 'wrap', gap: 12 }}>
          <ul
            style={{ flex: '1 1 300px', maxWidth: 420, minHeight: 0, maxHeight: '100%', overflow: 'auto', margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 6 }}
            aria-label="Versions and drafts"
            data-testid="history-list"
          >
            {!entries && !error && <li className="rk-fhint">Reading the history…</li>}
            {list.map((e) => {
              const key = entryKey(e)
              const kind = KIND[e.kind]
              return (
                <li key={key} data-testid="history-entry" data-key={key} style={{ border: '1px solid var(--line)', borderRadius: 8, padding: '6px 10px', fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 4 }}>
                  <span style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
                    <strong>{entryLabel(e)}</strong>
                    <span className="rk-pill" data-tone={kind.tone}>
                      {kind.text}
                    </span>
                    {e.inForce && (
                      <span className="rk-pill" data-tone="ok" data-testid="history-in-force">
                        In force
                      </span>
                    )}
                    {e.current && (
                      <span className="rk-pill" data-tone="info" data-testid="history-current">
                        The draft now
                      </span>
                    )}
                    {e.kind === 'live' && e.rev ? <span className="rk-fhint">was draft {e.rev}</span> : null}
                  </span>
                  {(e.by || (e.at && !e.at.startsWith('0001'))) && (
                    <span className="rk-fhint">
                      {e.by ?? ''}
                      {e.at && !e.at.startsWith('0001') ? `${e.by ? ', ' : ''}${fmtTs(e.at)}` : ''}
                    </span>
                  )}
                  {e.note && <span data-testid="history-note">{e.note}</span>}
                  {e.test && (
                    <span className="rk-fhint" data-tone={e.test.passed < e.test.cases ? 'err' : undefined}>
                      {testText(e.test)}
                    </span>
                  )}
                  <span style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                    <button className="rk-btn" aria-pressed={pair?.[0] === key} onClick={() => pick(0, key)} data-testid="history-from">
                      From
                    </button>
                    <button className="rk-btn" aria-pressed={pair?.[1] === key} onClick={() => pick(1, key)} data-testid="history-to">
                      To
                    </button>
                    {e.kind === 'live' && !!e.n && !e.inForce && onRestore && (
                      <button className="rk-btn" disabled={busy || !!restoreBlocked} title={restoreBlocked || undefined} onClick={() => void restore(e.n!)} data-testid="history-restore">
                        Restore
                      </button>
                    )}
                  </span>
                </li>
              )
            })}
          </ul>
          <div style={{ flex: '3 1 420px', minWidth: 0, minHeight: 0, maxHeight: '100%', overflow: 'auto' }} data-testid="history-diff">
            {pair && (!from || !to) && !error && <p className="rk-fhint">Comparing…</p>}
            {from && to && <VersionDiff from={from} to={to} clusterName={clusterName} />}
          </div>
        </div>
      </div>
    </div>,
    layer(),
  )
}
