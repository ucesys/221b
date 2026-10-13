import { useMemo, useRef, useState } from 'react'
import type { NodeRow } from '../api/types'
import { effectTone, facetMatch, facetsActive, nodeFacets, type FacetSelection } from '../lib/nodes'
import { Chevron, CloseIcon, FilterIcon } from './Icons'

interface Props {
  nodes: NodeRow[]
  value: FacetSelection
  onChange: (f: FacetSelection) => void
  /** Fills the width of its parent, as in the Pods tab's filter panel. */
  block?: boolean
}

function toggle(list: string[], id: string): string[] {
  return list.includes(id) ? list.filter((x) => x !== id) : [...list, id]
}

/**
 * The "Select nodes by label and taint" picker the Nodes and Pods tabs
 * share (P2-09). Values of one label match any of them; different labels
 * and taints must all match. The selection shows as chips beside the
 * button, each removable.
 */
export function FacetPicker({ nodes, value, onChange, block = false }: Props) {
  const [open, setOpen] = useState(false)
  const button = useRef<HTMLButtonElement>(null)
  const facets = useMemo(() => nodeFacets(nodes), [nodes])
  const active = facetsActive(value)
  const matching = useMemo(() => (active ? nodes.filter((n) => facetMatch(n, value)) : nodes), [nodes, value, active])
  const chips = [...value.labels.map((l) => ({ id: l, text: l, remove: () => onChange({ ...value, labels: value.labels.filter((x) => x !== l) }) })), ...value.taints.map((t) => ({ id: t, text: t, remove: () => onChange({ ...value, taints: value.taints.filter((x) => x !== t) }) }))]
  const count = value.labels.length + value.taints.length
  const close = () => {
    setOpen(false)
    button.current?.focus()
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, minWidth: 0, width: block ? '100%' : undefined }}>
      <div
        className="rk-dd"
        style={{ position: 'relative', display: 'flex' }}
        onKeyDown={(e) => {
          if (e.key === 'Escape' && open) {
            e.stopPropagation()
            close()
          }
        }}
      >
        <button ref={button} className="rk-btn" aria-expanded={open} aria-haspopup="true" data-on={active} onClick={() => setOpen(!open)} style={{ height: 32, justifyContent: 'flex-start', width: block ? '100%' : undefined }} title="Narrow the nodes by their labels and taints">
          <FilterIcon />
          <span style={{ flex: block ? '1 1 auto' : undefined, textAlign: 'left' }}>Select nodes by label and taint</span>
          {count > 0 && <span className="rk-cnt">{count}</span>}
          <Chevron />
        </button>
        {open && (
          <>
            <button className="rk-ddcatch" tabIndex={-1} aria-hidden="true" onClick={() => setOpen(false)} />
            <div className="rk-ddmenu" role="group" aria-label="Select nodes by label and taint" style={{ minWidth: 'min(640px, calc(100vw - 32px))', maxWidth: 'min(760px, calc(100vw - 32px))', padding: 12, gap: 10 }}>
              <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', gap: '4px 10px' }}>
                <h3 className="rk-h">Labels</h3>
                <span style={{ flex: '1 1 auto' }} />
                {facets.labels.length === 0 && <span style={{ fontSize: 12, color: 'var(--fg3)' }}>No labels besides the kubernetes.io ones</span>}
              </div>
              <div className="rk-fgroups">
                {facets.labels.map((g) => (
                  <div key={g.key} style={{ minWidth: 0 }}>
                    <span className="rk-fkey" title={g.key}>
                      {g.key}
                    </span>
                    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 5 }}>
                      {g.values.map((v) => (
                        <button key={v.text} className="rk-fchip" aria-pressed={value.labels.includes(v.text)} title={`${v.text} — ${v.count} ${v.count === 1 ? 'node' : 'nodes'}`} data-label={v.text} onClick={() => onChange({ ...value, labels: toggle(value.labels, v.text) })}>
                          {v.value || 'set'}
                          <span className="rk-cnt">{v.count}</span>
                        </button>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
              <h3 className="rk-h">Taints</h3>
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                {facets.taints.length === 0 && <span style={{ fontSize: 12, color: 'var(--fg3)' }}>No node has a taint</span>}
                {facets.taints.map((t) => (
                  <button key={t.id} className="rk-fchip" aria-pressed={value.taints.includes(t.id)} title={`Taint ${t.id} — ${t.count} ${t.count === 1 ? 'node' : 'nodes'}`} data-taint={t.id} onClick={() => onChange({ ...value, taints: toggle(value.taints, t.id) })}>
                    {t.key}
                    {t.value ? '=' + t.value : ''}
                    <span className="rk-t" data-tone={effectTone(t.effect)} style={{ fontSize: 10.5 }}>
                      {t.effect}
                    </span>
                    <span className="rk-cnt">{t.count}</span>
                  </button>
                ))}
              </div>
              <p style={{ margin: 0, fontSize: 11.5, color: 'var(--fg3)' }}>Values of one label match any of them; different labels and taints must all match.</p>
              <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 12px', paddingTop: 10, borderTop: '1px solid var(--line)' }}>
                <span style={{ fontSize: 12, color: 'var(--fg3)', marginRight: 'auto' }} data-testid="facet-match">
                  {!active ? 'Pick one or more values to narrow the nodes.' : matching.length ? `${matching.length} of ${nodes.length} nodes match` : 'No node has all of these.'}
                </span>
                {active && (
                  <button className="rk-link" onClick={() => onChange({ labels: [], taints: [] })} style={{ fontSize: 12 }}>
                    Clear
                  </button>
                )}
                <button className="rk-btn" onClick={close} style={{ height: 28 }}>
                  Done
                </button>
              </div>
            </div>
          </>
        )}
      </div>
      {active && (
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 6 }} data-testid="facet-chips">
          <span style={{ fontSize: 12, color: 'var(--fg3)' }}>
            {matching.length} of {nodes.length} nodes match
          </span>
          {chips.map((c) => (
            <button key={c.id} className="rk-fchip" data-on="true" onClick={c.remove} aria-label={`Remove ${c.text}`} title={`Remove ${c.text}`}>
              {c.text}
              <CloseIcon />
            </button>
          ))}
          <button className="rk-link" onClick={() => onChange({ labels: [], taints: [] })} style={{ fontSize: 12, marginLeft: 2 }}>
            Clear
          </button>
        </div>
      )}
    </div>
  )
}
