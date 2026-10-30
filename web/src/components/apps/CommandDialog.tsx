import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { deleteCommand, runCommand, saveCommand, sourceText, type AppView } from '../../api/apps'
import type { WatsonItem } from '../../api/watson'
import type { AppRow } from '../../columns/apps'
import { trapTab } from '../../lib/focus'
import { layer } from '../../lib/layer'

interface Props {
  cluster: string
  row: AppRow
  /** The command to run, edit or delete; none makes a new one. */
  command?: string
  onClose: () => void
  /** A saved or deleted command: the record, and what was done. */
  onSaved: (r: AppView, doing: string) => void
  /** A run: the Watson card, and what is being done. */
  onRun: (it: WatsonItem, doing: string) => void
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }}>
      <span style={{ fontSize: 12, fontWeight: 600, color: 'var(--fg2)' }}>{label}</span>
      {children}
      {hint && <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{hint}</span>}
    </div>
  )
}

/**
 * An application's own command (asked October 30, 2026): a redeploy from
 * the source with its own parameters over the saved values and
 * parameters. New: name, note and parameters, then Save puts it in the
 * Tools menu. Picked from the menu: what it does, with Run, Edit and
 * Delete.
 */
export function CommandDialog({ cluster, row, command, onClose, onSaved, onRun }: Props) {
  const existing = command ? row.app?.commands?.find((c) => c.name === command) : undefined
  const [mode, setMode] = useState<'run' | 'edit'>(existing ? 'run' : 'edit')
  const [name, setName] = useState(existing?.name ?? '')
  const [note, setNote] = useState(existing?.note ?? '')
  const [params, setParams] = useState((existing?.params ?? []).join('\n'))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [sure, setSure] = useState(false)
  const first = useRef<HTMLInputElement>(null)
  const primary = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (mode === 'edit') first.current?.focus()
    else primary.current?.focus()
  }, [mode])
  const paramList = () => params.split('\n').map((s) => s.trim()).filter(Boolean)
  const call = async (what: () => Promise<void>) => {
    setBusy(true)
    setError('')
    try {
      await what()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const save = () =>
    call(async () => {
      const r = await saveCommand(cluster, row.namespace, row.name, existing?.name ?? name.trim(), { name: name.trim(), note: note.trim(), params: paramList() })
      onSaved(r, existing ? `Saved command ${name.trim()}` : `Added ${name.trim()} to the Tools menu of ${row.name}`)
    })
  const run = () =>
    call(async () => {
      const it = await runCommand(cluster, row.namespace, row.name, existing!.name, row.orphan)
      onRun(it, `Redeploying ${row.name} with ${existing!.name}`)
    })
  const del = () =>
    call(async () => {
      const r = await deleteCommand(cluster, row.namespace, row.name, existing!.name)
      onSaved(r, `Deleted command ${existing!.name}`)
    })
  const canSave = !busy && name.trim() !== '' && paramList().length > 0
  const title = !existing ? 'New command' : mode === 'run' ? `Run ${existing.name}` : `Edit command ${existing.name}`
  const stop = (e: { stopPropagation: () => void }) => e.stopPropagation()
  return createPortal(
    <div style={{ position: 'fixed', inset: 0, zIndex: 60, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16, whiteSpace: 'normal' }} onClick={stop} onMouseDown={stop} onKeyDown={stop}>
      <button aria-label="Cancel" tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="rk-card"
        data-testid="command-dialog"
        data-mode={existing ? mode : 'new'}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            onClose()
            return
          }
          trapTab(e)
        }}
        style={{ position: 'relative', width: 'min(640px, 96vw)', padding: '18px 22px', display: 'flex', flexDirection: 'column', gap: 14, boxShadow: 'var(--shadow)' }}
      >
        <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>{title}</h2>
        <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>
          Redeploys <span className="rk-mono">{row.name}</span> in <span className="rk-mono">{row.namespace}</span>
          {row.app ? ` from ${sourceText(row.app.source)}` : ''} with the parameters below over its saved values and parameters, as you, after your approval in Watson. The command is kept with the application and offered in its Tools menu.
        </p>
        {mode === 'run' && existing ? (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10, fontSize: 12.5 }}>
            {existing.note && <p style={{ margin: 0 }}>{existing.note}</p>}
            <Field label="Parameters">
              <pre className="rk-mono" style={{ margin: 0, padding: '8px 10px', border: '1px solid var(--line)', borderRadius: 7, background: 'var(--surface)', fontSize: 12.5, whiteSpace: 'pre-wrap' }} data-testid="command-shown-params">
                {existing.params.join('\n')}
              </pre>
            </Field>
            {existing.updatedBy && (
              <span style={{ color: 'var(--fg3)', fontSize: 11.5 }}>
                Last changed by {existing.updatedBy}
                {existing.createdBy && existing.createdBy !== existing.updatedBy ? `; made by ${existing.createdBy}` : ''}.
              </span>
            )}
          </div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <Field label="Name" hint="Letters, digits, spaces, dots, dashes and underscores; unique within the application">
              <span className="rk-field">
                <input ref={first} value={name} onChange={(e) => setName(e.target.value)} maxLength={40} placeholder="Scale up" aria-label="Command name" data-testid="command-name" />
              </span>
            </Field>
            <Field label="Note" hint="What it is for, shown in the menu (optional)">
              <span className="rk-field">
                <input value={note} onChange={(e) => setNote(e.target.value)} maxLength={200} placeholder="Twice the pods, for the sale" aria-label="Note" data-testid="command-note" />
              </span>
            </Field>
            <Field label="Parameters" hint="One key=value per line, as helm --set takes them; applied over the saved values and parameters">
              <textarea className="rk-area rk-mono" value={params} onChange={(e) => setParams(e.target.value)} spellCheck={false} style={{ minHeight: 96, fontSize: 12.5, lineHeight: 1.5, resize: 'vertical' }} placeholder={'replicaCount=4\nimage.tag=v1.2.0'} aria-label="Parameters" data-testid="command-params" />
            </Field>
          </div>
        )}
        {error && (
          <p className="rk-callout" data-tone="warn" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="command-error">
            {error}
          </p>
        )}
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          {mode === 'run' && existing && (
            <button className="rk-btn" data-danger="true" onClick={() => (sure ? void del() : setSure(true))} disabled={busy} data-testid="command-delete">
              {sure ? 'Delete, really' : 'Delete…'}
            </button>
          )}
          <span style={{ flex: '1 1 auto' }} />
          <button className="rk-btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          {mode === 'run' && existing ? (
            <>
              <button className="rk-btn" onClick={() => setMode('edit')} disabled={busy} data-testid="command-edit">
                Edit
              </button>
              <button ref={primary} className="rk-btn" data-on onClick={() => void run()} disabled={busy} data-testid="command-run">
                {busy ? 'Submitting…' : 'Run'}
              </button>
            </>
          ) : (
            <>
              {existing && (
                <button className="rk-btn" onClick={() => setMode('run')} disabled={busy}>
                  Back
                </button>
              )}
              <button className="rk-btn" data-on onClick={() => void save()} disabled={!canSave} data-testid="command-save">
                {busy ? 'Saving…' : existing ? 'Save' : 'Add to the Tools menu'}
              </button>
            </>
          )}
        </div>
      </div>
    </div>,
    layer(),
  )
}
