import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { createNamespace } from '../api/secrets'
import { trapTab } from '../lib/focus'
import { layer } from '../lib/layer'

/** Makes a namespace (Phase 10, NS-01): a name and optional labels, written as the person. */
export function NamespaceDialog({ onClose, onDone }: { onClose: () => void; onDone: (name: string) => void }) {
  const [name, setName] = useState('')
  const [labels, setLabels] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const first = useRef<HTMLInputElement>(null)
  useEffect(() => {
    first.current?.focus()
  }, [])
  const submit = () => {
    const map: Record<string, string> = {}
    for (const line of labels.split('\n')) {
      const l = line.trim()
      if (!l) continue
      const [k, ...rest] = l.split('=')
      if (!k?.trim() || rest.length === 0) {
        setError(`"${l}" is not key=value`)
        return
      }
      map[k.trim()] = rest.join('=').trim()
    }
    setBusy(true)
    setError('')
    createNamespace(name.trim(), map)
      .then(() => {
        setBusy(false)
        onDone(name.trim())
      })
      .catch((e: unknown) => {
        setBusy(false)
        setError(e instanceof Error ? e.message : String(e))
      })
  }
  const stop = (e: { stopPropagation: () => void }) => e.stopPropagation()
  return createPortal(
    <div style={{ position: 'fixed', inset: 0, zIndex: 60, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16, whiteSpace: 'normal' }} onClick={stop} onMouseDown={stop} onKeyDown={stop}>
      <button aria-label="Cancel" tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <div
        role="dialog"
        aria-modal="true"
        aria-label="New namespace"
        className="rk-card"
        data-testid="namespace-dialog"
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            onClose()
            return
          }
          if (e.key === 'Enter' && (e.target as HTMLElement).tagName === 'INPUT' && name.trim() && !busy) submit()
          trapTab(e)
        }}
        style={{ position: 'relative', width: 'min(520px, 100%)', padding: '18px 20px', display: 'flex', flexDirection: 'column', gap: 14, boxShadow: 'var(--shadow)' }}
      >
        <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>New namespace</h2>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, fontWeight: 600, color: 'var(--fg2)' }}>
          Name
          <span className="rk-field">
            <input ref={first} className="rk-mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="my-namespace" data-testid="namespace-new-name" />
          </span>
          <span className="rk-fhint">Lower-case letters, digits and dashes, up to 63 characters.</span>
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, fontWeight: 600, color: 'var(--fg2)' }}>
          Labels
          <textarea className="rk-area rk-mono" value={labels} onChange={(e) => setLabels(e.target.value)} spellCheck={false} rows={3} style={{ minHeight: 60, fontSize: 12.5, resize: 'vertical' }} placeholder={'team=platform\npod-security.kubernetes.io/enforce=restricted'} aria-label="Labels" data-testid="namespace-new-labels" />
          <span className="rk-fhint">Optional, one key=value per line.</span>
        </label>
        {error && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
            {error}
          </p>
        )}
        <p className="rk-hint" style={{ margin: 0 }}>
          Written as you: Kubernetes decides whether you may create namespaces.
        </p>
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <button className="rk-btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="rk-btn" data-on="true" onClick={submit} disabled={busy || !name.trim()} aria-busy={busy} data-testid="namespace-create">
            {busy ? 'Creating…' : 'Create'}
          </button>
        </div>
      </div>
    </div>,
    layer(),
  )
}
