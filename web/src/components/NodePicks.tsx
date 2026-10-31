import { createContext, useContext } from 'react'
import type { Fleet } from '../api/types'

/**
 * Picking nodes on the Nodes page (asked October 30, 2026): a box beside
 * each name, and a bar above the table with what the picked nodes can
 * become: a fleet of names, members of one, paired for SSH.
 */
export const NodePicking = createContext<{ picked: Set<string>; toggle: (name: string) => void } | null>(null)

/** The box beside a node's name; nothing where nodes are not picked. */
export function PickBox({ name }: { name: string }) {
  const ctx = useContext(NodePicking)
  if (!ctx) return null
  return <input type="checkbox" className="rk-pick" checked={ctx.picked.has(name)} onChange={() => ctx.toggle(name)} onClick={(e) => e.stopPropagation()} aria-label={`Pick node ${name}`} data-testid={`node-pick-${name}`} />
}

interface BarProps {
  picked: string[]
  /** The nodes in view, for Pick all. */
  shown: string[]
  onPickAll: () => void
  onClear: () => void
  /** The fleets of names the picked nodes may join; an administrator only. */
  fleets: Fleet[] | null
  onMakeFleet: () => void
  onAddToFleet: (id: string) => void
  /** Why SSH pairing is not offered, or nothing when it is. */
  sshOff?: string
  onPairSSH: () => void
}

export function PickedBar({ picked, shown, onPickAll, onClear, fleets, onMakeFleet, onAddToFleet, sshOff, onPairSSH }: BarProps) {
  const n = picked.length
  const lists = (fleets ?? []).filter((f) => f.kind === 'nodes')
  return (
    <div className="rk-callout" data-tone="info" role="region" aria-label="Picked nodes" style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 10px', fontSize: 12.5 }} data-testid="node-picked-bar">
      <strong data-testid="node-picked-count">
        {n} {n === 1 ? 'node' : 'nodes'} picked
      </strong>
      {shown.some((s) => !picked.includes(s)) && (
        <button className="rk-link" onClick={onPickAll} data-testid="node-pick-all">
          pick all {shown.length} in view
        </button>
      )}
      <span style={{ flex: '1 1 auto' }} />
      {fleets && (
        <>
          <button className="rk-btn" onClick={onMakeFleet} title="A fleet of these nodes, by name" data-testid="node-picked-fleet">
            Make a fleet…
          </button>
          <label className="rk-field" style={{ width: 190 }} title={lists.length ? 'Add the picked nodes to a fleet of names' : 'No fleet of names yet; make one'}>
            <select value="" onChange={(e) => e.target.value && onAddToFleet(e.target.value)} disabled={lists.length === 0} aria-label="Add to a fleet" data-testid="node-picked-add">
              <option value="">{lists.length ? 'Add to fleet…' : 'No fleet of names yet'}</option>
              {lists.map((f) => (
                <option key={f.id} value={f.id}>
                  {f.name}
                </option>
              ))}
            </select>
          </label>
        </>
      )}
      <button className="rk-btn" onClick={onPairSSH} disabled={!!sshOff} title={sshOff ?? 'Pair every picked node for SSH: one session each'} data-testid="node-picked-ssh">
        Pair for SSH…
      </button>
      <button className="rk-btn" onClick={onClear} data-testid="node-picked-clear">
        Clear
      </button>
    </div>
  )
}
