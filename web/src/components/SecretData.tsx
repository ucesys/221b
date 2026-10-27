import { useEffect, useState } from 'react'
import { fetchSecretsConfig, revealSecretValue, updateSecret, type SecretsConfig, type SecretValue } from '../api/secrets'
import type { KeySize } from '../api/types'
import { fMem, nWord } from '../lib/format'
import { CopyName } from './CopyName'
import { ScrollBox, Table, TR } from './Table'

function fBytes(n: number): string {
  return n < 1024 ? `${n} B` : fMem(n)
}

/**
 * A Secret's keys and sizes (P2-08), and for an Admin or SuperAdmin the
 * edits (SE-02 to SE-04): replace a value, add a key, remove one, each
 * written as them. A value shows only where the chart turned
 * secrets.reveal on, one key at a time, read live and audited (SE-03);
 * otherwise the new values are typed blind.
 */
export function SecretData({ namespace: ns, name, keys, onToast }: { namespace: string; name: string; keys: KeySize[]; onToast: (msg: string) => void }) {
  const [cfg, setCfg] = useState<SecretsConfig | null>(null)
  const [shown, setShown] = useState<Record<string, SecretValue>>({})
  const [busyKey, setBusyKey] = useState('')
  const [editing, setEditing] = useState(false)
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const [removed, setRemoved] = useState<Set<string>>(new Set())
  const [added, setAdded] = useState<{ key: string; value: string }[]>([])
  const [showTyping, setShowTyping] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let live = true
    fetchSecretsConfig().then(
      (c) => live && setCfg(c),
      () => live && setCfg({ read: true, reveal: false, edit: false }),
    )
    return () => {
      live = false
    }
  }, [])

  const reveal = (key: string) => {
    if (shown[key]) {
      setShown((s) => {
        const next = { ...s }
        delete next[key]
        return next
      })
      return
    }
    setBusyKey(key)
    revealSecretValue(ns, name, key)
      .then((v) => setShown((s) => ({ ...s, [key]: v })))
      .catch((e: unknown) => onToast(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusyKey(''))
  }

  const cancel = () => {
    setEditing(false)
    setDrafts({})
    setRemoved(new Set())
    setAdded([])
    setError('')
  }

  const save = () => {
    const set: Record<string, string> = {}
    for (const [k, v] of Object.entries(drafts)) if (!removed.has(k)) set[k] = v
    for (const a of added) {
      const k = a.key.trim()
      if (!k) continue
      if (!/^[A-Za-z0-9._-]+$/.test(k)) {
        setError(`"${k}" is not a key: letters, digits, dots, dashes and underscores`)
        return
      }
      set[k] = a.value
    }
    const remove = [...removed]
    if (Object.keys(set).length + remove.length === 0) {
      cancel()
      return
    }
    setSaving(true)
    setError('')
    updateSecret(ns, name, { set, remove })
      .then(() => {
        onToast(`Secret ${name} changed: ${nWord(Object.keys(set).length + remove.length, 'key', 'keys')}.`)
        setShown({})
        cancel()
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setSaving(false))
  }

  const canEdit = !!cfg?.edit
  const canReveal = !!cfg?.reveal && canEdit
  const valueBox = (value: string, onChange: (v: string) => void, label: string, testId: string) => (
    <textarea className={`rk-area rk-mono${showTyping || canReveal ? '' : ' rk-secret'}`} value={value} onChange={(e) => onChange(e.target.value)} aria-label={label} spellCheck={false} autoComplete="off" rows={2} style={{ minHeight: 44, fontSize: 12.5, resize: 'vertical' }} data-testid={testId} />
  )

  return (
    <>
      {keys.length === 0 && !editing ? (
        <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 12.5, color: 'var(--fg3)' }}>No data.</p>
      ) : (
        <ScrollBox label="Keys">
          <Table label="Keys" template={canEdit ? 'minmax(160px, 1fr) 90px minmax(220px, 2fr)' : 'minmax(200px, 1fr) 110px'} minWidth={canEdit ? 480 : 310} cols={canEdit ? [{ label: 'Key' }, { label: 'Size', right: true }, { label: 'Value' }] : [{ label: 'Key' }, { label: 'Size', right: true }]}>
            {keys.map((k) => {
              const v = shown[k.key]
              const gone = removed.has(k.key)
              const draft = drafts[k.key]
              return (
                <TR key={k.key}>
                  <span className="rk-mono" style={{ fontSize: 12, textDecoration: gone ? 'line-through' : undefined, color: gone ? 'var(--fg3)' : undefined }}>
                    {k.key}
                  </span>
                  <span className="rk-r rk-num">{fBytes(k.bytes)}</span>
                  {canEdit && (
                    <span style={{ display: 'flex', flexDirection: 'column', gap: 6, minWidth: 0, fontSize: 12 }} data-testid={`secret-key-${k.key}`}>
                      {editing && draft !== undefined && !gone ? (
                        valueBox(draft, (val) => setDrafts((d) => ({ ...d, [k.key]: val })), `New value of ${k.key}`, `secret-draft-${k.key}`)
                      ) : v ? (
                        <span style={{ display: 'flex', alignItems: 'flex-start', gap: 6, minWidth: 0 }}>
                          <code className="rk-mono" style={{ fontSize: 12, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', flex: '1 1 auto', minWidth: 0, maxHeight: 160, overflow: 'auto', background: 'var(--sunken)', padding: '4px 6px', borderRadius: 6 }} data-testid={`secret-value-${k.key}`}>
                            {v.value}
                          </code>
                          <CopyName text={v.value} what={`value of ${k.key}`} />
                        </span>
                      ) : (
                        <span style={{ color: 'var(--fg3)' }}>{gone ? 'removed on save' : '••••••••'}</span>
                      )}
                      <span style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                        {canReveal && !gone && (
                          <button className="rk-link" onClick={() => reveal(k.key)} disabled={busyKey === k.key} data-testid={`secret-reveal-${k.key}`}>
                            {busyKey === k.key ? 'Reading…' : v ? 'Hide' : 'Reveal'}
                          </button>
                        )}
                        {v?.base64 && <span style={{ color: 'var(--fg3)' }}>binary, shown as base64</span>}
                        {editing && !gone && draft === undefined && (
                          <button className="rk-link" onClick={() => setDrafts((d) => ({ ...d, [k.key]: '' }))} data-testid={`secret-replace-${k.key}`}>
                            Replace
                          </button>
                        )}
                        {editing && !gone && draft !== undefined && (
                          <button
                            className="rk-link"
                            onClick={() =>
                              setDrafts((d) => {
                                const next = { ...d }
                                delete next[k.key]
                                return next
                              })
                            }
                          >
                            Keep as is
                          </button>
                        )}
                        {editing && (
                          <button
                            className="rk-link"
                            onClick={() =>
                              setRemoved((r) => {
                                const next = new Set(r)
                                if (next.has(k.key)) next.delete(k.key)
                                else next.add(k.key)
                                return next
                              })
                            }
                            data-testid={`secret-remove-${k.key}`}
                          >
                            {gone ? 'Keep' : 'Remove'}
                          </button>
                        )}
                      </span>
                    </span>
                  )}
                </TR>
              )
            })}
            {editing &&
              added.map((a, i) => (
                <TR key={'new' + i}>
                  <span>
                    <input className="rk-mono" value={a.key} onChange={(e) => setAdded((l) => l.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))} placeholder="new-key" aria-label="New key" style={{ width: '100%', boxSizing: 'border-box', fontSize: 12, padding: '4px 6px', border: '1px solid var(--line)', borderRadius: 6, background: 'var(--surface)', color: 'var(--fg)' }} data-testid={`secret-new-key-${i}`} />
                  </span>
                  <span className="rk-r rk-num" style={{ color: 'var(--fg3)' }}>
                    new
                  </span>
                  <span style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                    {valueBox(a.value, (val) => setAdded((l) => l.map((x, j) => (j === i ? { ...x, value: val } : x))), 'New value', `secret-new-value-${i}`)}
                    <button className="rk-link" onClick={() => setAdded((l) => l.filter((_, j) => j !== i))} style={{ alignSelf: 'flex-start' }}>
                      Drop
                    </button>
                  </span>
                </TR>
              ))}
          </Table>
        </ScrollBox>
      )}
      {canEdit && (
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, padding: '0 16px 10px' }} data-testid="secret-edit-bar">
          {!editing ? (
            <button className="rk-btn" onClick={() => setEditing(true)} data-testid="secret-edit">
              Edit
            </button>
          ) : (
            <>
              <button className="rk-btn" onClick={() => setAdded((l) => [...l, { key: '', value: '' }])} data-testid="secret-add-key">
                Add key
              </button>
              {!canReveal && (
                <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
                  <input type="checkbox" checked={showTyping} onChange={(e) => setShowTyping(e.target.checked)} /> Show what I type
                </label>
              )}
              <span style={{ flex: '1 1 auto' }} />
              <button className="rk-btn" onClick={cancel} disabled={saving}>
                Cancel
              </button>
              <button className="rk-btn" data-on="true" onClick={save} disabled={saving} aria-busy={saving} data-testid="secret-save">
                {saving ? 'Saving…' : 'Save'}
              </button>
            </>
          )}
          {error && (
            <span className="rk-t" data-tone="err" role="alert" style={{ fontSize: 12.5, flexBasis: '100%' }}>
              {error}
            </span>
          )}
        </div>
      )}
      <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 11.5, color: 'var(--fg3)' }} data-testid="keys-note">
        221B shows a Secret’s keys and their sizes, never their values (X-01); the YAML tab hides them too.
        {cfg?.edit ? (cfg.reveal ? ' Reveal reads one value live, as you, and the audit log records it. Edits are written as you; Kubernetes decides.' : ' Edits are written as you, without showing the old values; Kubernetes decides. Revealing is off here (secrets.reveal in the chart).') : ''}
      </p>
    </>
  )
}
