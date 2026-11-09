// A case's fields edited where they are shown (CF-60), and the choice a
// conflict asks for (CF-59, CF-60). A field is edited in place: a click on
// it opens it, Enter (or leaving it) saves, Escape cancels; each save
// sends what the edit started from, and a field someone changed since is
// refused. Then, as for the case's shared draft, the person sees the
// newer value and picks Discard my changes (theirs stays) or Overwrite
// (theirs is saved over, which the case's history records). Since build
// 14 every field edited in place shows a pencil beside its value, even
// an empty one, so it reads as editable; the value opens it too. A choice
// from a list (priority, type, assignee, cluster) is a select, which needs
// no pencil.
import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { ConfirmDialog } from '../ConfirmDialog'
import { PencilIcon } from '../Icons'

/** What a person picks when what they edit changed since they started: theirs, mine, or neither yet. */
export type ConflictChoice = 'discard' | 'overwrite' | 'cancel'

/** A conflict waiting for the person's choice. */
export interface Conflict {
  /** "Pat changed this draft at 14:02". */
  title: string
  /** Their version, as it is now. */
  body: ReactNode
  resolve: (choice: ConflictChoice) => void
}

/** The choice a conflict asks for, in the app's own dialog: Discard my changes, or Overwrite. */
export function ConflictDialog({ conflict, onDone }: { conflict: Conflict; onDone: () => void }) {
  const pick = (choice: ConflictChoice) => {
    onDone()
    conflict.resolve(choice)
  }
  return (
    <ConfirmDialog title={conflict.title} confirm="Overwrite" onConfirm={() => pick('overwrite')} onCancel={() => pick('cancel')} also={{ label: 'Discard my changes', onClick: () => pick('discard'), testId: 'case-conflict-discard' }}>
      <div data-testid="case-conflict">{conflict.body}</div>
      <span className="tb-hint" style={{ fontSize: 12 }}>
        Discard my changes keeps theirs. Overwrite saves yours over it; the case’s history says so.
      </span>
    </ConfirmDialog>
  )
}

/** Opens a conflict and waits for the person's choice. */
export function useConflict(): [Conflict | null, (title: string, body: ReactNode) => Promise<ConflictChoice>, () => void] {
  const [conflict, setConflict] = useState<Conflict | null>(null)
  const ask = (title: string, body: ReactNode) => new Promise<ConflictChoice>((resolve) => setConflict({ title, body, resolve }))
  return [conflict, ask, () => setConflict(null)]
}

interface Props {
  value: string
  /** The field's name, for its input and the edit button. */
  label: string
  testId: string
  multiline?: boolean
  mono?: boolean
  /** Shown when the value is empty. */
  empty?: ReactNode
  /** What is wrong with a value, if anything: it is not saved then. */
  check?: (v: string) => string
  maxLength?: number
  placeholder?: string
  /** Saves the value from the one the edit started with; false keeps it open. */
  onSave: (next: string, base: string) => Promise<boolean>
  /** How the value shows, when it holds a link of its own: only the pencil opens it then. */
  children?: ReactNode
}

/**
 * A field edited in place (CF-60): a pencil beside its value opens it,
 * shown always, also when the field is empty (build 14); the value is a
 * button that opens it too, unless it shows a link. Enter saves
 * (Shift+Enter is a new line in a long field), so does leaving it; Escape
 * cancels and never closes the drawer. A value unchanged is not sent.
 */
export function InPlace({ value, label, testId, multiline, mono, empty, check, maxLength, placeholder, onSave, children }: Props) {
  const [editing, setEditing] = useState<{ text: string; base: string } | null>(null)
  const field = useRef<HTMLInputElement & HTMLTextAreaElement>(null)
  // While a save is out (or its conflict asks), leaving the field saves nothing more.
  const saving = useRef(false)
  const open = !!editing
  useEffect(() => {
    if (open) field.current?.focus()
  }, [open])
  const bad = editing && check ? check(editing.text) : ''
  const cancel = () => {
    saving.current = false
    setEditing(null)
  }
  const commit = async () => {
    if (!editing || saving.current) return
    const next = editing.text.trim()
    if (next === editing.base.trim()) return cancel()
    if (check?.(next)) return
    saving.current = true
    const ok = await onSave(next, editing.base)
    saving.current = false
    if (ok) setEditing(null)
    else field.current?.focus()
  }
  if (editing) {
    const props = {
      ref: field,
      className: `${multiline ? 'cs-ta' : 'cs-in'}${mono ? ' rk-mono' : ''}`,
      'aria-label': label,
      value: editing.text,
      maxLength,
      placeholder,
      'aria-invalid': bad ? true : undefined,
      onChange: (e: { target: { value: string } }) => setEditing({ ...editing, text: e.target.value }),
      onBlur: () => void commit(),
      onKeyDown: (e: KeyboardEvent) => {
        if (e.key === 'Escape') {
          // Escape leaves the field, not the drawer.
          e.preventDefault()
          e.stopPropagation()
          cancel()
        } else if (e.key === 'Enter' && !(multiline && e.shiftKey)) {
          e.preventDefault()
          void commit()
        }
      },
      'data-testid': `${testId}-input`,
    }
    return (
      <span style={{ display: 'flex', flexDirection: 'column', gap: 4, width: '100%' }}>
        {multiline ? <textarea {...props} /> : <input {...props} style={{ width: '100%', font: 'inherit' }} />}
        {bad ? <span className="tb-err">{bad}</span> : <span className="tb-hint" style={{ fontSize: 11.5 }}>Enter saves{multiline ? ', Shift+Enter is a new line' : ''}; Escape cancels.</span>}
      </span>
    )
  }
  const start = () => setEditing({ text: value, base: value })
  const pencil = (
    <button type="button" className="rk-icon-btn" aria-label={`Edit ${label.toLowerCase()}`} title={`Edit ${label.toLowerCase()}`} onClick={start} data-testid={`${testId}-edit`} style={{ flex: 'none' }}>
      <PencilIcon />
    </button>
  )
  if (children) {
    return (
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
        {children}
        {pencil}
      </span>
    )
  }
  return (
    <span style={{ display: 'inline-flex', alignItems: 'flex-start', gap: 6, maxWidth: '100%' }}>
      <button type="button" className={mono ? 'rk-mono' : undefined} title={`Click to edit ${label.toLowerCase()}`} onClick={start} data-testid={`${testId}-value`}
        style={{ background: 'none', border: 0, borderBottom: '1px dashed var(--line)', borderRadius: 0, padding: 0, margin: 0, font: 'inherit', color: 'inherit', textAlign: 'left', cursor: 'text', whiteSpace: multiline ? 'pre-wrap' : undefined, overflowWrap: 'anywhere', minWidth: 0 }}>
        {value || empty || <span className="tb-hint">none</span>}
      </button>
      {pencil}
    </span>
  )
}
