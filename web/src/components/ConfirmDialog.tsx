import { useEffect, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { trapTab } from '../lib/focus'
import { layer } from '../lib/layer'

interface Props {
  title: string
  children: ReactNode
  confirm: string
  /** The confirming button is styled as a warning for a change that ends access. */
  danger?: boolean
  busy?: boolean
  error?: string
  onConfirm: () => void
  onCancel: () => void
}

/**
 * A confirmation for a change to who may do what (RB-43, RB-51) or for a
 * Tools menu's change (TB-02, TB-03): modal, with focus on Cancel so Enter
 * never confirms by accident, Tab kept inside, and Escape cancelling. It
 * renders at the end of the document, so that opening it from a table cell
 * or a menu gives it neither their styles (a cell's text does not wrap) nor
 * their keyboard handling; its own events stop at it. The end of the app's
 * root, to keep the theme's colours.
 */
export function ConfirmDialog({ title, children, confirm, danger, busy, error, onConfirm, onCancel }: Props) {
  const cancel = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    cancel.current?.focus()
  }, [])
  const stop = (e: { stopPropagation: () => void }) => e.stopPropagation()
  return createPortal(
    <div style={{ position: 'fixed', inset: 0, zIndex: 60, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16, whiteSpace: 'normal' }} onClick={stop} onMouseDown={stop} onKeyDown={stop}>
      <button aria-label="Cancel" tabIndex={-1} onClick={onCancel} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <div
        role="alertdialog"
        aria-modal="true"
        aria-label={title}
        className="rk-card"
        data-testid="confirm"
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            onCancel()
            return
          }
          trapTab(e)
        }}
        style={{ position: 'relative', width: 'min(460px, 100%)', padding: '18px 20px', display: 'flex', flexDirection: 'column', gap: 12, boxShadow: 'var(--shadow)', overflowWrap: 'anywhere' }}
      >
        <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>{title}</h2>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8, fontSize: 13 }}>{children}</div>
        {error && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
            {error}
          </p>
        )}
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <button ref={cancel} className="rk-btn" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button className="rk-btn" data-on={!danger} data-danger={danger ? 'true' : undefined} aria-busy={busy} disabled={busy} onClick={onConfirm} data-testid="confirm-ok" style={danger ? { borderColor: 'var(--err)', color: 'var(--err)' } : undefined}>
            {confirm}
          </button>
        </div>
      </div>
    </div>,
    layer(),
  )
}
