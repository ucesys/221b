import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { fetchDiff, type AppDiff, type AppRefs, type AppView, type RefKind, type RevisionView } from '../../api/apps'
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

/**
 * A revision's diff (HA-17), large enough to read: what the revision
 * changed against the one before it, or against the deployed revision or
 * the source, as chosen at the top.
 */
export function DiffDialog({ clusterId, namespace, name, revisions, revision, app, refs, onClose }: Props) {
  const latest = revisions[0]?.revision ?? revision
  const before = revisions.find((r) => r.revision < revision)?.revision
  const [from, setFrom] = useState<number>(before ?? revision)
  const [to, setTo] = useState<number | 'source'>(before ? revision : latest === revision && app ? 'source' : revision)
  const [ref, setRef] = useState<{ ref: string; kind: RefKind } | null>(null)
  // The answer for the choice made; a choice without one yet is busy.
  const key = `${from}>${String(to)}@${ref ? ref.kind + ':' + ref.ref : ''}`
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
    fetchDiff(clusterId, namespace, name, { from, to, ref: to === 'source' ? ref?.ref : undefined, refKind: to === 'source' ? ref?.kind : undefined })
      .then((d) => live && setResult({ key, diff: d }))
      .catch((e: unknown) => live && setResult({ key, error: e instanceof Error ? e.message : String(e) }))
    return () => {
      live = false
    }
  }, [clusterId, namespace, name, from, to, ref, key])
  const stop = (e: { stopPropagation: () => void }) => e.stopPropagation()
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
          <span style={{ fontSize: 12.5 }}>from revision</span>
          <label className="rk-field" style={{ width: 90 }}>
            <select value={from} onChange={(e) => setFrom(Number(e.target.value))} aria-label="From revision" data-testid="diff-from">
              {revisions.map((r) => (
                <option key={r.revision} value={r.revision}>
                  {r.revision}
                </option>
              ))}
            </select>
          </label>
          <span style={{ fontSize: 12.5 }}>to</span>
          <label className="rk-field" style={{ width: 180 }}>
            <select value={String(to)} onChange={(e) => setTo(e.target.value === 'source' ? 'source' : Number(e.target.value))} aria-label="To revision" data-testid="diff-to">
              {revisions.map((r) => (
                <option key={r.revision} value={r.revision}>
                  revision {r.revision}
                  {r.revision === latest ? ' (deployed)' : ''}
                </option>
              ))}
              {app && <option value="source">the source (git)</option>}
            </select>
          </label>
          {to === 'source' && app && (
            <label className="rk-field" style={{ minWidth: 200 }}>
              <select value={ref ? `${ref.kind}:${ref.ref}` : ''} onChange={(e) => (e.target.value ? setRef({ kind: e.target.value.split(':')[0] as RefKind, ref: e.target.value.split(':').slice(1).join(':') }) : setRef(null))} aria-label="At branch or tag" data-testid="diff-ref">
                <option value="">at {app.source.ref} (deployed ref)</option>
                {refs?.branches.map((b) => (
                  <option key={'b' + b} value={'branch:' + b}>
                    branch {b}
                  </option>
                ))}
                {refs?.tags.map((t) => (
                  <option key={'t' + t} value={'tag:' + t}>
                    tag {t}
                  </option>
                ))}
              </select>
            </label>
          )}
          {busy && <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>Comparing…</span>}
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
