import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { fetchHistory, fetchHistoryItem, type HistoryEntry, type HistoryText, type TestBrief } from '../../api/ai'
import { trapTab } from '../../lib/focus'
import { fmtTs } from '../../lib/format'
import { layer } from '../../lib/layer'
import { DiffView } from '../apps/DiffView'
import { CloseIcon } from '../Icons'
import { instructionsDiff } from './TextDiff'

interface Props {
  agentId: string
  agentName: string
  clusterName?: (id: string) => string
  /** A version whose Diff… was clicked: shown against the version before it. Without one, the version in force against the draft now. */
  version?: number
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

/** An entry as its select lists it: "Version 7 · live · in force · Nov 17 18:44 · ann", "Draft 9 · the draft now · tested 12/12 passed · …", "Draft 8 · discarded · …". */
export function entryOption(e: HistoryEntry): string {
  const t = e.test
  const parts = [
    entryLabel(e),
    e.kind === 'live' ? 'live' : e.kind === 'discarded' ? 'discarded' : '',
    e.inForce ? 'in force' : '',
    e.current ? 'the draft now' : '',
    t ? `tested ${t.passed}/${t.cases} passed${t.state === 'stopped' ? ' (stopped)' : t.state === 'failed' ? ' (failed)' : ''}` : '',
    e.at && !e.at.startsWith('0001') ? fmtTs(e.at) : '',
    e.by ?? '',
  ]
  return parts.filter(Boolean).join(' · ')
}

/** What to compare at first: a version against the version before it; else the draft now against the version in force, else the newest two. */
export function defaultPair(entries: HistoryEntry[], version?: number): [string, string] | null {
  if (!entries.length) return null
  if (version !== undefined) {
    const at = entries.findIndex((e) => e.kind === 'live' && (e.n ?? 0) === version)
    if (at >= 0) {
      const before = entries.slice(at + 1).find((e) => e.kind === 'live')
      return [entryKey(before ?? entries[at]!), entryKey(entries[at]!)]
    }
  }
  const current = entries.find((e) => e.current)
  const inForce = entries.find((e) => e.inForce)
  if (current && inForce) return [entryKey(inForce), entryKey(current)]
  if (entries.length === 1) return [entryKey(entries[0]!), entryKey(entries[0]!)]
  return [entryKey(entries[1]!), entryKey(entries[0]!)]
}

/**
 * The agent's history (AI-32, AI-41): any two of its versions and drafts
 * compared as Helm › History compares a release's revisions (DiffDialog):
 * a select for each side, and one unified diff of the fields that changed.
 * Restore lives in the drawer's Versions list.
 */
export function HistoryDialog({ agentId, agentName, clusterName, version, onClose }: Props) {
  const [entries, setEntries] = useState<HistoryEntry[] | null>(null)
  const [error, setError] = useState('')
  const [pair, setPair] = useState<[string, string] | null>(null)
  const [texts, setTexts] = useState<Record<string, HistoryText>>({})
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
        setPair(defaultPair(list, version))
      },
      (e: unknown) => live && setError(e instanceof Error ? e.message : String(e)),
    )
    return () => {
      live = false
    }
  }, [agentId, version])
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
  const name = (k: string) => {
    const e = list.find((x) => entryKey(x) === k)
    return e ? entryLabel(e) : k
  }
  const from = pair ? texts[pair[0]] : undefined
  const to = pair ? texts[pair[1]] : undefined
  const busy = !!pair && (!from || !to) && !error
  const halt = (e: { stopPropagation: () => void }) => e.stopPropagation()
  const side = (i: 0 | 1, label: string, testId: string) => (
    <label className="rk-field" style={{ minWidth: 200, maxWidth: 'min(520px, 100%)' }}>
      <select
        value={pair?.[i] ?? ''}
        disabled={!pair}
        onChange={(e) => {
          const k = e.target.value
          setPair((p) => (p ? (i === 0 ? [k, p[1]] : [p[0], k]) : [k, k]))
        }}
        aria-label={label}
        data-testid={testId}
      >
        {list.map((e) => (
          <option key={entryKey(e)} value={entryKey(e)}>
            {entryOption(e)}
          </option>
        ))}
      </select>
    </label>
  )
  return createPortal(
    <div style={{ position: 'fixed', inset: 0, zIndex: 60, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16, whiteSpace: 'normal' }} onClick={halt} onMouseDown={halt} onKeyDown={halt}>
      <button aria-label="Close the diff" tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="history-dialog-title"
        className="rk-card"
        data-testid="history-dialog"
        data-state={entries && !busy ? 'ready' : 'loading'}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            onClose()
            return
          }
          trapTab(e)
        }}
        style={{ position: 'relative', width: 'min(1400px, 96vw)', height: '90vh', padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 12, boxShadow: 'var(--shadow)', minHeight: 0 }}
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10 }}>
            <h2 id="history-dialog-title" style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>
              Diff of {agentName} instructions
            </h2>
            <span style={{ fontSize: 12.5 }}>from</span>
            {side(0, 'From', 'history-from')}
            <span style={{ fontSize: 12.5 }}>to</span>
            {side(1, 'To', 'history-to')}
            {(!entries || busy) && !error && <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>{entries ? 'Comparing…' : 'Reading the history…'}</span>}
            <span style={{ flex: '1 1 auto' }} />
            <button ref={closeRef} className="rk-icon-btn" aria-label="Close the diff" title="Close (Esc)" onClick={onClose} data-testid="history-close">
              <CloseIcon />
            </button>
          </div>
          <p className="rk-fhint" style={{ margin: 0, fontSize: 12.5 }} data-testid="history-hint">
            Every version that went live and every draft saved since, newest first.
          </p>
        </div>
        <div style={{ flex: '1 1 auto', minHeight: 0, overflow: 'auto', display: 'flex', flexDirection: 'column' }} data-testid="history-diff">
          {error && (
            <p className="rk-callout" data-tone="warn" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
              {error}
            </p>
          )}
          {pair && from && to && <DiffView diff={instructionsDiff(from, to, name(pair[0]), name(pair[1]), clusterName)} same="are the same" fill />}
        </div>
      </div>
    </div>,
    layer(),
  )
}
