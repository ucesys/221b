import { useUnsaved } from '../lib/unsaved'

/**
 * The foot of every Settings section (6.3 review): changes wait for Save,
 * the same way on every page, and Undo puts back what is saved. Changes
 * not saved yet keep Sherlock from moving the view (SR-14).
 */
export function SaveBar({
  changed,
  busy,
  onSave,
  onUndo,
  error,
  saved,
  testId,
  what = 'a Settings section',
}: {
  changed: boolean
  busy: boolean
  onSave: () => void
  onUndo: () => void
  error: string
  saved: string
  testId: string
  /** What holds the changes, as Sherlock is told: "Settings › SLA". */
  what?: string
}) {
  useUnsaved(changed, what)
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', position: 'sticky', bottom: 0, padding: '10px 0', background: 'var(--surface)', borderTop: '1px solid var(--line)' }} data-testid="save-bar">
      <button className="rk-btn" data-primary="true" disabled={!changed || busy} onClick={onSave} data-testid={testId}>
        Save changes
      </button>
      {changed && (
        <button className="rk-link" onClick={onUndo} disabled={busy} style={{ fontSize: 12.5 }}>
          Undo changes
        </button>
      )}
      {error ? (
        <span className="rk-t" data-tone="err" role="alert" style={{ fontSize: 12.5 }} data-testid="save-error">
          {error}
        </span>
      ) : saved && !changed ? (
        <span className="rk-t" data-tone="ok" role="status" style={{ fontSize: 12.5 }}>
          {saved}
        </span>
      ) : (
        <span className="rk-fhint" style={{ fontSize: 12.5 }}>
          {changed ? 'Not saved yet.' : 'No changes.'}
        </span>
      )}
    </div>
  )
}
