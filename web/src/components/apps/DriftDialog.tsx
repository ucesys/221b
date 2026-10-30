import { useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import type { AppDrift } from '../../api/apps'
import { trapTab } from '../../lib/focus'
import { fmtTs } from '../../lib/format'
import { layer } from '../../lib/layer'
import { CloseIcon } from '../Icons'
import { DiffLines } from './DiffView'

/**
 * What drifted (R2-10): each object of the deployed manifest that the
 * cluster has differently or not at all, with the fields and a diff of
 * the manifest's side against the cluster's.
 */
export function DriftDialog({ name, drift, onClose }: { name: string; drift: AppDrift; onClose: () => void }) {
  const closeRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    closeRef.current?.focus()
  }, [])
  const stop = (e: { stopPropagation: () => void }) => e.stopPropagation()
  const off = drift.objects.filter((o) => o.state !== 'same')
  return createPortal(
    <div style={{ position: 'fixed', inset: 0, zIndex: 60, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16, whiteSpace: 'normal' }} onClick={stop} onMouseDown={stop} onKeyDown={stop}>
      <button aria-label="Close" tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <div
        role="dialog"
        aria-modal="true"
        aria-label={`What drifted in ${name}`}
        className="rk-card"
        data-testid="drift-dialog"
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            onClose()
            return
          }
          trapTab(e)
        }}
        style={{ position: 'relative', width: 'min(1200px, 96vw)', maxHeight: '90vh', padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 12, boxShadow: 'var(--shadow)', minHeight: 0 }}
      >
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10 }}>
          <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>
            What drifted in <span className="rk-mono">{name}</span>
          </h2>
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>
            {drift.changed} changed · {drift.missing} missing{drift.unknown ? ` · ${drift.unknown} unknown` : ''} · {drift.same} as deployed · checked {fmtTs(drift.checked, { seconds: true })}
          </span>
          <span style={{ flex: '1 1 auto' }} />
          <button ref={closeRef} className="rk-icon-btn" aria-label="Close" title="Close (Esc)" onClick={onClose} data-testid="drift-close">
            <CloseIcon />
          </button>
        </div>
        <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>
          Drift is what the cluster has differently from the deployed manifest: a field the manifest sets and someone changed by hand, a scaled Deployment, a deleted object. Fields the cluster adds on its own (defaults, status, Helm’s labels) do not count. An upgrade from the source puts the manifest back; <i>Check again</i> reads the cluster again.
        </p>
        <div style={{ flex: '1 1 auto', minHeight: 0, overflow: 'auto', display: 'flex', flexDirection: 'column', gap: 12 }}>
          {off.length === 0 && (
            <p className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5 }}>
              Every object is as deployed.
            </p>
          )}
          {off.map((o) => (
            <section key={`${o.kind}/${o.namespace ?? ''}/${o.name}`} style={{ display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="drift-object" data-state={o.state}>
              <h3 style={{ margin: 0, fontSize: 13.5, fontWeight: 600, display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
                <span className="rk-pill" data-tone={o.state === 'missing' ? 'err' : o.state === 'changed' ? 'warn' : 'muted'}>
                  <span className="rk-dot" />
                  {o.state}
                </span>
                <span>
                  {o.kind} <span className="rk-mono">{o.name}</span>
                  {o.namespace ? <span style={{ color: 'var(--fg3)', fontWeight: 400 }}> in {o.namespace}</span> : null}
                </span>
              </h3>
              {o.state === 'missing' && (
                <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>
                  The deployed manifest has it; the cluster does not. It was deleted, or it never applied.
                </p>
              )}
              {o.error && (
                <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>
                  {o.error}
                </p>
              )}
              {o.fields && o.fields.length > 0 && (
                <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>
                  Fields: <span className="rk-mono">{o.fields.join(', ')}</span>
                </p>
              )}
              {o.diff && (
                <pre className="rk-code rk-mono" style={{ margin: 0, padding: '10px 12px', fontSize: 12, lineHeight: 1.5, overflow: 'auto', maxHeight: 420, whiteSpace: 'pre', background: 'var(--sunken)', borderRadius: 8, border: '1px solid var(--line)' }}>
                  <DiffLines text={o.diff} />
                </pre>
              )}
            </section>
          ))}
        </div>
      </div>
    </div>,
    layer(),
  )
}
