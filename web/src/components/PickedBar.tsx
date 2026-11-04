import { useState, type ReactNode } from 'react'
import { saveFleets } from '../api/fleets'
import type { Fleet, FleetRow, NodeRow } from '../api/types'
import { has, listText } from '../lib/fleets'
import { nWord } from '../lib/format'
import { FleetNodes, type FleetNodesMode } from './FleetNodes'

interface Props {
  cluster: string
  /** The picked nodes. */
  nodes: NodeRow[]
  all: NodeRow[]
  fleets: Fleet[]
  rows: FleetRow[]
  /** The fleet the page shows: the first choice to add to or remove from. */
  current?: Fleet
  /** Stays in the page's flow rather than at the bottom edge of the window. */
  inFlow?: boolean
  onCancel: () => void
  /** A new fleet is saved; the page shows it. */
  onCreated: (f: Fleet) => void
  onToast: (msg: string) => void
  /** Beside the names: what else the page can pick. */
  note?: ReactNode
  /** What else the page can do with the picked nodes. */
  children?: ReactNode
}

/**
 * The bar over picked nodes, on the Fleets tab and the Nodes page, and the
 * dialog behind its fleet buttons: a new fleet, into one, or out of one.
 */
export function PickedBar({ cluster, nodes, all, fleets, rows, current, inFlow, onCancel, onCreated, onToast, note, children }: Props) {
  const [dialog, setDialog] = useState<FleetNodesMode | null>(null)
  const names = nodes.map((n) => n.name)
  const inOne = fleets.some((f) => nodes.some((n) => has(f, n)))
  return (
    <>
      <div className="rk-card rk-pickbar" role="group" aria-label="Picked nodes" data-testid="fleet-nodebar" style={inFlow ? { position: 'static' } : undefined}>
        <strong style={{ fontSize: 12.5, fontWeight: 600 }}>Picked {nWord(names.length, 'node', 'nodes')}</strong>
        <span style={{ fontSize: 12, color: 'var(--fg2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '40ch' }} title={names.join(', ')}>
          {listText(names.length > 4 ? [...names.slice(0, 3), `${names.length - 3} more`] : names)}
        </span>
        {note}
        <span style={{ flex: '1 1 auto' }} />
        <button className="rk-btn" onClick={() => setDialog('new')} title="A new fleet of these nodes, by a label they share or a new one" data-testid="fleet-nodebar-new">
          New fleet…
        </button>
        <button className="rk-btn" onClick={() => setDialog('add')} disabled={!fleets.some((f) => nodes.some((n) => !has(f, n)))} title={fleets.length ? 'Put these nodes in a fleet you choose' : 'No fleets yet: make one first'} data-testid="fleet-nodebar-add">
          Add to a fleet…
        </button>
        <button className="rk-btn" onClick={() => setDialog('remove')} disabled={!inOne} title={inOne ? 'Take these nodes out of a fleet you choose' : 'None of these nodes is in a fleet'} data-testid="fleet-nodebar-remove">
          Remove from a fleet…
        </button>
        {children}
        <button className="rk-btn" onClick={onCancel} title="Unpick every node" data-testid="fleet-nodebar-cancel">
          Cancel
        </button>
      </div>
      {dialog && (
        <FleetNodes
          mode={dialog}
          nodes={nodes}
          all={all}
          fleets={fleets}
          rows={rows}
          current={current}
          onClose={() => setDialog(null)}
          onCreate={async (f) => {
            await saveFleets(cluster, [...fleets, f])
            onCreated(f)
          }}
          onSave={async (next) => {
            await saveFleets(cluster, next)
          }}
          onDone={(m) => {
            setDialog(null)
            onCancel()
            onToast(m)
          }}
        />
      )}
    </>
  )
}
