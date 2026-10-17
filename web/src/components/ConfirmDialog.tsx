import { useEffect, useRef, type ReactNode } from 'react'

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
 * A confirmation for a change to who may do what (RB-43, RB-51): modal, with
 * focus on Cancel so Enter never confirms by accident, and Escape cancelling.
 */
export function ConfirmDialog({ title, children, confirm, danger, busy, error, onConfirm, onCancel }: Props) {
  const cancel = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    cancel.current?.focus()
  }, [])
  return (
    <div style={{ position: 'fixed', inset: 0, zIndex: 60, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16 }}>
      <button aria-label="Cancel" tabIndex={-1} onClick={onCancel} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <div
        role="alertdialog"
        aria-modal="true"
        aria-label={title}
        className="rk-card"
        data-testid="confirm"
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            onCancel()
          }
        }}
        style={{ position: 'relative', width: 'min(460px, 100%)', padding: '18px 20px', display: 'flex', flexDirection: 'column', gap: 12, boxShadow: 'var(--shadow)' }}
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
    </div>
  )
}
