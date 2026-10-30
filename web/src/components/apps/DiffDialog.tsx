import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { fetchAppRefs, fetchDiff, parseSide, sideValue, type AppDiff, type AppRefs, type AppView, type DiffSide, type RevisionView } from '../../api/apps'
import { trapTab } from '../../lib/focus'
import { layer } from '../../lib/layer'
import { CloseIcon } from '../Icons'
import { DiffView } from './DiffView'

interface Props {
  clusterId: string
  namespace: string
  name: string
  revisions: RevisionView[]
  /** The revision whose icon was clicked: shown against the one before it. */
  revision: number
  app?: AppView
  refs: AppRefs | null
  onClose: () => void
}

/** One side's choices: the revisions, then the source at its saved ref and at each branch and tag (asked October 30, 2026). */
function SideSelect({ label, value, onChange, revisions, latest, app, refs, testId }: { label: string; value: DiffSide; onChange: (s: DiffSide) => void; revisions: RevisionView[]; latest: number; app?: AppView; refs: AppRefs | null; testId: string }) {
  return (
    <label className="rk-field" style={{ minWidth: 200 }}>
      <select value={sideValue(value)} onChange={(e) => onChange(parseSide(e.target.value))} aria-label={label} data-testid={testId}>
        {revisions.map((r) => (
          <option key={r.revision} value={r.revision}>
            revision {r.revision}
            {r.revision === latest ? ' (deployed)' : ''}
          </option>
        ))}
        {app && (
          <optgroup label="The source (git)">
            <option value="source">
              source at {app.source.ref} ({app.source.refKind}, deployed ref)
            </option>
            {refs?.branches
              .filter((b) => app.source.refKind !== 'branch' || b !== app.source.ref)
              .map((b) => (
                <option key={'b' + b} value={'source:branch:' + b}>
                  source at branch {b}
                </option>
              ))}
            {refs?.tags
              .filter((t) => app.source.refKind !== 'tag' || t !== app.source.ref)
              .map((t) => (
                <option key={'t' + t} value={'source:tag:' + t}>
                  source at tag {t}
                </option>
              ))}
          </optgroup>
        )}
      </select>
    </label>
  )
}

/**
 * A revision's diff (HA-17), large enough to read: what the revision
 * changed against the one before it, or against any other revision, or
 * against the source at its deployed branch or tag, or at any branch or
 * tag of the repository; either side may be any of these, so two refs of
 * the source compare too (asked October 30, 2026).
 */
export function DiffDialog({ clusterId, namespace, name, revisions, revision, app, refs: given, onClose }: Props) {
  const latest = revisions[0]?.revision ?? revision
  const before = revisions.find((r) => r.revision < revision)?.revision
  const [from, setFrom] = useState<DiffSide>({ kind: 'revision', revision: before ?? revision })
  const [to, setTo] = useState<DiffSide>(before ? { kind: 'revision', revision } : latest === revision && app ? { kind: 'source' } : { kind: 'revision', revision })
  // The branches and tags: as the panel has them, else read here.
  const [refs, setRefs] = useState<AppRefs | null>(given)
  useEffect(() => {
    if (refs || !app) return
    let live = true
    fetchAppRefs(clusterId, namespace, name)
      .then((r) => live && setRefs(r))
      .catch(() => undefined)
    return () => {
      live = false
    }
  }, [refs, app, clusterId, namespace, name])
  // The answer for the choice made; a choice without one yet is busy.
  const key = `${sideValue(from)}>${sideValue(to)}`
  const [result, setResult] = useState<{ key: string; diff?: AppDiff; error?: string } | null>(null)
  const diff = result?.key === key ? result.diff : undefined
  const error = result?.key === key ? result.error : undefined
  const busy = result?.key !== key
  const closeRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    closeRef.current?.focus()
  }, [])
  useEffect(() => {
    let live = true
    fetchDiff(clusterId, namespace, name, { from, to })
      .then((d) => live && setResult({ key, diff: d }))
      .catch((e: unknown) => live && setResult({ key, error: e instanceof Error ? e.message : String(e) }))
    return () => {
      live = false
    }
  }, [clusterId, namespace, name, from, to, key])
  const stop = (e: { stopPropagation: () => void }) => e.stopPropagation()
  const pick = { revisions, latest, app, refs }
  return createPortal(
    <div style={{ position: 'fixed', inset: 0, zIndex: 60, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16, whiteSpace: 'normal' }} onClick={stop} onMouseDown={stop} onKeyDown={stop}>
      <button aria-label="Close" tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <div
        role="dialog"
        aria-modal="true"
        aria-label={`Diff of ${name}`}
        className="rk-card"
        data-testid="diff-dialog"
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            onClose()
            return
          }
          trapTab(e)
        }}
        style={{ position: 'relative', width: 'min(1400px, 96vw)', height: '90vh', padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 12, boxShadow: 'var(--shadow)', minHeight: 0 }}
      >
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10 }}>
          <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>
            Diff of <span className="rk-mono">{name}</span>
          </h2>
          <span style={{ fontSize: 12.5 }}>from</span>
          <SideSelect label="From" value={from} onChange={setFrom} testId="diff-from" {...pick} />
          <span style={{ fontSize: 12.5 }}>to</span>
          <SideSelect label="To" value={to} onChange={setTo} testId="diff-to" {...pick} />
          {busy && <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>Comparing…</span>}
          {!busy && app && !refs && <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>Reading the repository's branches and tags…</span>}
          <span style={{ flex: '1 1 auto' }} />
          <button ref={closeRef} className="rk-icon-btn" aria-label="Close the diff" title="Close (Esc)" onClick={onClose} data-testid="diff-close">
            <CloseIcon />
          </button>
        </div>
        <div style={{ flex: '1 1 auto', minHeight: 0, overflow: 'auto', display: 'flex', flexDirection: 'column' }}>
          {error && (
            <p className="rk-callout" data-tone="warn" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
              {error}
            </p>
          )}
          {diff && <DiffView diff={diff} fill />}
        </div>
      </div>
    </div>,
    layer(),
  )
}
