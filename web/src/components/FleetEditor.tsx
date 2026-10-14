import { useEffect, useMemo, useRef, useState } from 'react'
import type { Fleet, NodeRow } from '../api/types'
import { COLOR_NAMES, fixCommand, has, missing, newId, nextColor, parseReq, reqsOf, withLabel } from '../lib/fleets'
import { effectTone, labelText, taintId } from '../lib/nodes'
import { CloseIcon, CopyIcon, SearchIcon } from './Icons'

/** What the editor opens with: a saved fleet, or a new one, perhaps prefilled. */
export interface EditorSeed {
  fleet: Fleet | null
  name?: string
  labels?: string[]
  taints?: string[]
}

interface Props {
  seed: EditorSeed
  fleets: Fleet[]
  nodes: NodeRow[]
  now: number
  onClose: () => void
  onSave: (f: Fleet, isNew: boolean) => Promise<void>
  onDelete: (f: Fleet) => Promise<void>
}

function XIcon() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" aria-hidden="true">
      <path d="M7 7l10 10M17 7 7 17" />
    </svg>
  )
}

/** The fleet editor (P4-01): name, description and colour, the labels and taints, and a live preview of the nodes it takes. */
export function FleetEditor({ seed, fleets, nodes, onClose, onSave, onDelete }: Props) {
  const orig = seed.fleet
  const [name, setName] = useState(orig?.name ?? seed.name ?? '')
  const [desc, setDesc] = useState(orig?.desc ?? '')
  const [color, setColor] = useState(orig?.color ?? nextColor(fleets))
  const [labels, setLabels] = useState<string[]>(orig?.labels ?? seed.labels ?? [])
  const [taints, setTaints] = useState<string[]>(orig?.taints ?? seed.taints ?? [])
  const [q, setQ] = useState('')
  const [text, setText] = useState('')
  const [textErr, setTextErr] = useState('')
  const [saveErr, setSaveErr] = useState('')
  const [confirmDel, setConfirmDel] = useState(false)
  const [busy, setBusy] = useState(false)
  const [copied, setCopied] = useState('')
  const nameRef = useRef<HTMLInputElement>(null)
  const closeRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (orig) closeRef.current?.focus()
    else nameRef.current?.focus()
  }, [orig])

  const title = orig ? orig.name : name.trim() || 'New fleet'
  const draft = { labels, taints }
  const reqCount = labels.length + taints.length
  const members = useMemo(() => (reqCount ? nodes.filter((n) => has(draft, n)) : []), [nodes, labels, taints]) // eslint-disable-line react-hooks/exhaustive-deps
  const near = useMemo(
    () =>
      reqCount >= 2
        ? nodes
            .filter((n) => !has(draft, n))
            .map((n) => ({ n, miss: missing(draft, n) }))
            .filter((x) => x.miss.length === 1)
        : [],
    [nodes, labels, taints], // eslint-disable-line react-hooks/exhaustive-deps
  )

  // Every label value and taint in the cluster, with the nodes that carry it.
  const facets = useMemo(() => {
    const labelMap = new Map<string, Map<string, string[]>>()
    const taintMap = new Map<string, { id: string; key: string; effect: string; nodes: string[] }>()
    for (const n of nodes) {
      for (const [k, v] of Object.entries(n.labels)) {
        const m = labelMap.get(k) ?? new Map<string, string[]>()
        m.set(v, [...(m.get(v) ?? []), n.name])
        labelMap.set(k, m)
      }
      for (const t of n.taints) {
        const id = taintId(t)
        const e = taintMap.get(id) ?? { id, key: t.key + (t.value ? '=' + t.value : ''), effect: t.effect, nodes: [] }
        e.nodes.push(n.name)
        taintMap.set(id, e)
      }
    }
    return { labels: labelMap, taints: [...taintMap.values()].sort((a, b) => a.id.localeCompare(b.id)) }
  }, [nodes])
  const lq = q.trim().toLowerCase()
  const labelGroups = [...facets.labels.entries()]
    .filter(([k]) => k !== 'kubernetes.io/hostname' || lq)
    .map(([k, m]) => ({ key: k, values: [...m.entries()].filter(([v]) => !lq || `${k}=${v}`.toLowerCase().includes(lq)).sort((a, b) => a[0].localeCompare(b[0], undefined, { numeric: true })) }))
    .filter((g) => g.values.length > 0)
    .sort((a, b) => a.key.localeCompare(b.key))
  const taintChips = facets.taints.filter((t) => !lq || t.id.toLowerCase().includes(lq))
  const nodeList = (ns: string[]) => (ns.length > 6 ? ns.slice(0, 6).join(', ') + ` and ${ns.length - 6} more` : ns.join(', '))

  const toggleLabel = (k: string, v: string) => {
    const l = k + '=' + v
    setLabels(labels.includes(l) ? labels.filter((x) => x !== l) : withLabel(labels, l))
    setSaveErr('')
  }
  const toggleTaint = (id: string) => {
    setTaints(taints.includes(id) ? taints.filter((x) => x !== id) : [...taints, id])
    setSaveErr('')
  }
  const addText = () => {
    const r = parseReq(text)
    if (r.error) {
      setTextErr(r.error)
      return
    }
    if (r.label) setLabels(withLabel(labels, r.label))
    if (r.taint && !taints.includes(r.taint)) setTaints([...taints, r.taint])
    setText('')
    setTextErr('')
    setSaveErr('')
  }

  const save = async () => {
    const n = name.trim()
    if (!n) return setSaveErr('Give the fleet a name.')
    if (fleets.some((f) => f.id !== orig?.id && f.name.trim().toLowerCase() === n.toLowerCase())) return setSaveErr(`Another fleet is already called “${n}”.`)
    if (!reqCount) return setSaveErr('Pick at least one label or taint.')
    const f: Fleet = { id: orig?.id ?? newId(n, fleets), name: n, color, desc: desc.trim(), labels, taints }
    setBusy(true)
    try {
      await onSave(f, !orig)
    } catch (e) {
      setSaveErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const del = async () => {
    if (!orig) return
    if (!confirmDel) {
      setConfirmDel(true)
      return
    }
    setBusy(true)
    try {
      await onDelete(orig)
    } catch (e) {
      setSaveErr((e as Error).message)
      setBusy(false)
    }
  }
  const copy = (cmd: string) => {
    void navigator.clipboard?.writeText(cmd).then(
      () => setCopied(cmd),
      () => undefined,
    )
  }

  const matchText = !reqCount ? 'Pick a label or taint to see which nodes match.' : members.length ? `${members.length} ${members.length === 1 ? 'node matches' : 'nodes match'} today` : 'No node has all of these yet'

  return (
    <div style={{ position: 'fixed', top: 0, right: 0, bottom: 0, left: 0, zIndex: 40, display: 'flex', justifyContent: 'flex-end' }}>
      <button aria-label="Close the fleet editor" tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <aside
        className="rk-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        data-testid="fleet-editor"
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            onClose()
          }
        }}
        style={{ position: 'relative', width: 'min(700px, 100%)', height: '100%', display: 'flex', flexDirection: 'column', background: 'var(--surface)', borderLeft: '1px solid var(--line)', boxShadow: 'var(--shadow)' }}
      >
        <div style={{ flex: 'none', padding: '16px 20px 14px', display: 'flex', alignItems: 'flex-start', gap: 10, borderBottom: '1px solid var(--line)' }}>
          <div style={{ flex: '1 1 auto', minWidth: 0, display: 'flex', flexDirection: 'column', gap: 4 }}>
            <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{orig ? 'Edit fleet' : 'New fleet'}</span>
            <h2 style={{ margin: 0, fontSize: 17, fontWeight: 600, lineHeight: 1.3, overflowWrap: 'anywhere' }}>{title}</h2>
          </div>
          <button ref={closeRef} className="rk-icon-btn" aria-label="Close the fleet editor" title="Close (Esc)" onClick={onClose}>
            <CloseIcon />
          </button>
        </div>
        <div style={{ flex: '1 1 auto', minHeight: 0, overflow: 'auto', padding: '18px 20px 28px', display: 'flex', flexDirection: 'column', gap: 20, background: 'var(--bg)' }}>
          <section className="rk-card" style={{ padding: '14px 16px', display: 'flex', flexDirection: 'column', gap: 12 }}>
            <label style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 12, color: 'var(--fg3)' }}>
              Name
              <span className="rk-field">
                <input
                  ref={nameRef}
                  type="text"
                  value={name}
                  onChange={(e) => {
                    setName(e.target.value)
                    setSaveErr('')
                  }}
                  placeholder="For example: GPU workers"
                  data-testid="fleet-name"
                />
              </span>
            </label>
            <label style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 12, color: 'var(--fg3)' }}>
              Description
              <span className="rk-field">
                <input type="text" value={desc} onChange={(e) => setDesc(e.target.value)} placeholder="What runs on these nodes (optional)" data-testid="fleet-desc" />
              </span>
            </label>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
              <span style={{ fontSize: 12, color: 'var(--fg3)', marginRight: 4 }}>Color</span>
              <div role="radiogroup" aria-label="Color" style={{ display: 'flex', gap: 8 }}>
                {COLOR_NAMES.map((c, i) => (
                  <button key={c} className="rk-swatch" role="radio" aria-checked={color === i} aria-label={c} data-c={i} onClick={() => setColor(i)} />
                ))}
              </div>
            </div>
          </section>

          <section style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', gap: '4px 10px' }}>
              <h3 className="rk-h">Nodes in this fleet have all of these</h3>
              {reqCount > 0 && (
                <span style={{ fontSize: 12, color: 'var(--fg3)' }}>
                  {reqCount} {reqCount === 1 ? 'requirement' : 'requirements'}
                </span>
              )}
            </div>
            {reqCount > 0 ? (
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }} data-testid="fleet-reqs">
                {reqsOf(draft).map((r) => {
                  const aria = r.kind === 'l' ? `Stop requiring ${r.text}` : `Stop requiring the taint ${r.id}`
                  return (
                    <button key={r.kind + r.id} className="rk-fchip" data-on="true" aria-label={aria} title={aria} onClick={() => (r.kind === 'l' ? setLabels(labels.filter((x) => x !== r.id)) : setTaints(taints.filter((x) => x !== r.id)))}>
                      {r.text}
                      {r.effect && (
                        <span className="rk-tag" data-tone={r.tone}>
                          {r.effect}
                        </span>
                      )}
                      <XIcon />
                    </button>
                  )
                })}
              </div>
            ) : (
              <p style={{ margin: 0, padding: '12px 14px', fontSize: 12.5, color: 'var(--fg3)', border: '1px dashed var(--line2)', borderRadius: 8 }}>Pick at least one label or taint below.</p>
            )}
          </section>

          <section className="rk-card" style={{ padding: '14px 16px', display: 'flex', flexDirection: 'column', gap: 12 }}>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 12px' }}>
              <h3 className="rk-h" style={{ marginRight: 'auto' }}>
                Labels and taints in the cluster
              </h3>
              <label className="rk-field" style={{ width: 230, maxWidth: '100%' }}>
                <SearchIcon />
                <span className="rk-sr">Find a label or taint</span>
                <input type="search" placeholder="Find a label or taint" value={q} onChange={(e) => setQ(e.target.value)} />
              </label>
            </div>
            <div className="rk-fgroups">
              {labelGroups.map((g) => (
                <div key={g.key} style={{ minWidth: 0 }}>
                  <span className="rk-fkey" title={g.key}>
                    {g.key}
                  </span>
                  <div style={{ display: 'flex', flexWrap: 'wrap', gap: 5 }}>
                    {g.values.map(([v, ns]) => {
                      const on = labels.includes(g.key + '=' + v)
                      return (
                        <button key={v} className="rk-fchip" aria-pressed={on} title={`${labelText(g.key, v)} — on ${nodeList(ns)}\n${on ? 'Click to remove it' : 'Click to require it'}`} onClick={() => toggleLabel(g.key, v)} data-label={g.key + '=' + v}>
                          {v || '(set)'}
                          <span className="rk-cnt">{ns.length}</span>
                        </button>
                      )
                    })}
                  </div>
                </div>
              ))}
            </div>
            {taintChips.length > 0 && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                <span className="rk-fkey">taints</span>
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                  {taintChips.map((t) => {
                    const on = taints.includes(t.id)
                    return (
                      <button key={t.id} className="rk-fchip" aria-pressed={on} title={`Taint ${t.id} — on ${nodeList(t.nodes)}\n${on ? 'Click to remove it' : 'Click to require it'}`} onClick={() => toggleTaint(t.id)} data-taint={t.id}>
                        {t.key}
                        <span className="rk-tag" data-tone={effectTone(t.effect)}>
                          {t.effect}
                        </span>
                        <span className="rk-cnt">{t.nodes.length}</span>
                      </button>
                    )
                  })}
                </div>
              </div>
            )}
            {lq && labelGroups.length === 0 && taintChips.length === 0 && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>Nothing in the cluster matches “{q.trim()}”. Type it below to require it anyway.</p>}
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6, paddingTop: 10, borderTop: '1px solid var(--line)' }}>
              <span style={{ fontSize: 12, color: 'var(--fg3)' }}>
                Or type one that no node has yet: <span className="rk-mono">key=value</span> for a label, <span className="rk-mono">key=value:NoSchedule</span> for a taint
              </span>
              <div style={{ display: 'flex', gap: 8 }}>
                <label className="rk-field" style={{ flex: '1 1 auto' }}>
                  <span className="rk-sr">Label or taint to require</span>
                  <input
                    type="text"
                    value={text}
                    onChange={(e) => {
                      setText(e.target.value)
                      setTextErr('')
                    }}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault()
                        addText()
                      }
                    }}
                    placeholder="gpu=a100 or nvidia.com/gpu=true:NoSchedule"
                    spellCheck={false}
                    style={{ fontFamily: 'var(--mono)' }}
                    data-testid="fleet-text"
                  />
                </label>
                <button className="rk-btn" onClick={addText} disabled={!text.trim()} data-testid="fleet-text-add">
                  Add
                </button>
              </div>
              {textErr && (
                <span className="rk-t" data-tone="err" style={{ fontSize: 12 }} data-testid="fleet-text-error">
                  {textErr}
                </span>
              )}
            </div>
          </section>

          <section style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', gap: '4px 10px' }}>
              <h3 className="rk-h">Preview</h3>
              <span style={{ fontSize: 12, color: 'var(--fg3)' }} data-testid="fleet-preview">
                {matchText}
              </span>
            </div>
            {members.length > 0 && (
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                {members.map((m) => (
                  <span key={m.name} className="rk-chip" data-member={m.name}>
                    {m.name}
                    {m.cordoned && <em>cordoned</em>}
                  </span>
                ))}
              </div>
            )}
            {near.length > 0 && (
              <div className="rk-card" style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="fleet-near">
                <strong style={{ fontSize: 12.5, fontWeight: 600 }}>Almost in: nodes that miss one requirement</strong>
                {near.map(({ n, miss }) => {
                  const m = miss[0]!
                  const cmd = fixCommand(n.name, m)
                  return (
                    <div key={n.name} style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 12px', paddingTop: 8, borderTop: '1px solid var(--line)' }}>
                      <span className="rk-mono" style={{ fontWeight: 600 }}>
                        {n.name}
                      </span>
                      <span style={{ flex: '1 1 260px', fontSize: 12, color: 'var(--fg2)' }}>
                        Missing {m.text}
                        {m.kind === 'l' && m.conflict !== null ? ` (its ${m.key} is ${m.conflict || '""'})` : ''}
                      </span>
                      <button className="rk-btn" onClick={() => copy(cmd)} title={`${cmd}\nCopies the command; the console does not change the cluster.`}>
                        <CopyIcon />
                        {copied === cmd ? 'Copied' : 'Copy the command'}
                      </button>
                    </div>
                  )
                })}
              </div>
            )}
          </section>
          <p style={{ margin: 0, fontSize: 11.5, color: 'var(--fg3)' }}>Fleets are saved in this console, not in the cluster. A node joins or leaves a fleet only through its own labels and taints.</p>
        </div>
        <div style={{ flex: 'none', display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, padding: '12px 20px', borderTop: '1px solid var(--line)', background: 'var(--surface)' }}>
          <button className="rk-btn" data-on="true" onClick={() => void save()} disabled={busy} style={{ height: 34, padding: '0 16px' }} data-testid="fleet-save">
            {orig ? 'Save changes' : 'Create fleet'}
          </button>
          <button className="rk-btn" onClick={onClose} style={{ height: 34 }}>
            Cancel
          </button>
          {saveErr && (
            <span className="rk-t" data-tone="err" style={{ fontSize: 12 }} data-testid="fleet-error">
              {saveErr}
            </span>
          )}
          <span style={{ flex: '1 1 auto' }} />
          {orig && (
            <button className="rk-btn" onClick={() => void del()} disabled={busy} title="Removes the fleet from this console; nodes keep their labels and taints" style={{ height: 34, color: 'var(--err)' }} data-testid="fleet-delete">
              {confirmDel ? 'Click again to delete' : 'Delete fleet'}
            </button>
          )}
        </div>
      </aside>
    </div>
  )
}

