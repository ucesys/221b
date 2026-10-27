import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { createSecret } from '../api/secrets'
import { trapTab } from '../lib/focus'
import { layer } from '../lib/layer'

/** Makes a Secret in a namespace (SE-05): a name, a type and its keys, written as the person. */
export function SecretDialog({ namespace, reveal, onClose, onDone }: { namespace: string; reveal: boolean; onClose: () => void; onDone: (name: string) => void }) {
  const [name, setName] = useState('')
  const [type, setType] = useState('Opaque')
  const [rows, setRows] = useState<{ key: string; value: string }[]>([{ key: '', value: '' }])
  const [showTyping, setShowTyping] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const first = useRef<HTMLInputElement>(null)
  useEffect(() => {
    first.current?.focus()
  }, [])
  const submit = () => {
    const set: Record<string, string> = {}
    for (const r of rows) {
      const k = r.key.trim()
      if (!k) continue
      if (!/^[A-Za-z0-9._-]+$/.test(k)) {
        setError(`"${k}" is not a key: letters, digits, dots, dashes and underscores`)
        return
      }
      set[k] = r.value
    }
    setBusy(true)
    setError('')
    createSecret(namespace, { name: name.trim(), type: type.trim() || 'Opaque', set })
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
        aria-label={`New Secret in ${namespace}`}
        className="rk-card"
        data-testid="secret-dialog"
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            onClose()
            return
          }
          trapTab(e)
        }}
        style={{ position: 'relative', width: 'min(640px, 100%)', maxHeight: 'calc(100vh - 32px)', overflow: 'auto', padding: '18px 20px', display: 'flex', flexDirection: 'column', gap: 14, boxShadow: 'var(--shadow)' }}
      >
        <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>New Secret in {namespace}</h2>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: '12px 16px' }}>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, fontWeight: 600, color: 'var(--fg2)' }}>
            Name
            <span className="rk-field">
              <input ref={first} className="rk-mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="my-secret" data-testid="secret-new-name" />
            </span>
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, fontWeight: 600, color: 'var(--fg2)' }}>
            Type
            <span className="rk-field">
              <input className="rk-mono" list="secret-types" value={type} onChange={(e) => setType(e.target.value)} data-testid="secret-new-type" />
              <datalist id="secret-types">
                {['Opaque', 'kubernetes.io/tls', 'kubernetes.io/basic-auth', 'kubernetes.io/ssh-auth', 'kubernetes.io/dockerconfigjson'].map((t) => (
                  <option key={t} value={t} />
                ))}
              </datalist>
            </span>
          </label>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <span style={{ fontSize: 12, fontWeight: 600, color: 'var(--fg2)' }}>Keys</span>
          {rows.map((r, i) => (
            <div key={i} style={{ display: 'grid', gridTemplateColumns: 'minmax(140px, 1fr) minmax(200px, 2fr) auto', gap: 8, alignItems: 'start' }}>
              <span className="rk-field">
                <input className="rk-mono" value={r.key} onChange={(e) => setRows((l) => l.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))} placeholder="key" aria-label="Key" data-testid={`secret-new-key-${i}`} />
              </span>
              <textarea className={`rk-area rk-mono${showTyping || reveal ? '' : ' rk-secret'}`} value={r.value} onChange={(e) => setRows((l) => l.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))} placeholder="value" aria-label="Value" spellCheck={false} autoComplete="off" rows={2} style={{ minHeight: 44, fontSize: 12.5, resize: 'vertical' }} data-testid={`secret-new-value-${i}`} />
              <button className="rk-link" onClick={() => setRows((l) => (l.length > 1 ? l.filter((_, j) => j !== i) : l))} style={{ marginTop: 8 }} aria-label="Drop this key">
                Drop
              </button>
            </div>
          ))}
          <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
            <button className="rk-btn" onClick={() => setRows((l) => [...l, { key: '', value: '' }])}>
              Add key
            </button>
            {!reveal && (
              <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
                <input type="checkbox" checked={showTyping} onChange={(e) => setShowTyping(e.target.checked)} /> Show what I type
              </label>
            )}
          </div>
        </div>
        {error && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
            {error}
          </p>
        )}
        <p className="rk-hint" style={{ margin: 0 }}>
          Written as you: Kubernetes decides whether you may create a Secret in {namespace}. The console keeps no copy of the values.
        </p>
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <button className="rk-btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="rk-btn" data-on="true" onClick={submit} disabled={busy || !name.trim()} aria-busy={busy} data-testid="secret-create">
            {busy ? 'Creating…' : 'Create'}
          </button>
        </div>
      </div>
    </div>,
    layer(),
  )
}
