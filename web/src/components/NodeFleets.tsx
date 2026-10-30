import { useState } from 'react'
import { saveFleets } from '../api/fleets'
import type { Fleet, FleetRow, Me, NodeRow } from '../api/types'
import { fleetOf, reqText } from '../lib/fleets'
import { href } from '../lib/router'
import { FleetEditor, type EditorSeed } from './FleetEditor'
import { PlusIcon } from './Icons'

function Pencil() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 20h9" />
      <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
    </svg>
  )
}

interface Props {
  cluster: string
  fleets: FleetRow[]
  defs: Fleet[]
  nodes: NodeRow[]
  /** The fleets picked, by id, or "none" for the nodes in no fleet. */
  sel: string[]
  onSel: (ids: string[] | undefined) => void
  loaded: boolean
  me: Me | null
  now: number
  fleetsPage: boolean
  onToast: (msg: string) => void
}

/**
 * The fleets beside the Nodes table (asked at the Phase 10.1 review,
 * R2-02): each one filters the table, and an administrator makes,
 * changes and deletes them here with the same editor as the Fleets tab.
 */
export function NodeFleets({ cluster, fleets, defs, nodes, sel, onSel, loaded, me, now, fleetsPage, onToast }: Props) {
  const admin = me?.admin ?? true
  const [editor, setEditor] = useState<EditorSeed | null>(null)
  const none = fleets.find((r) => r.kind === 'none')
  const list = fleets.filter((r) => r.kind === 'fleet').sort((a, b) => a.order - b.order)
  const pick = (f: FleetRow, checked: boolean) => onSel(checked ? sel.filter((s) => s !== f.id) : [...sel.filter((s) => s !== 'none'), f.id!])
  return (
    <aside className="rk-pane rk-nodefleets" aria-label="Fleets" data-testid="node-fleets" style={{ flex: '0 0 250px', display: 'flex', flexDirection: 'column', background: 'var(--surface)', borderRight: '1px solid var(--line)', overflow: 'auto' }}>
      <div style={{ padding: '14px 14px 10px', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, borderBottom: '1px solid var(--line)' }}>
        <h2 className="rk-h">
          Fleets <span style={{ fontWeight: 500, letterSpacing: 0, textTransform: 'none' }}>{list.length}</span>
        </h2>
        {admin && (
          <button className="rk-btn" onClick={() => setEditor({ fleet: null })} style={{ height: 26 }} data-testid="node-fleet-new">
            <PlusIcon />
            New
          </button>
        )}
      </div>
      <div style={{ padding: '0 0 6px' }}>
        <button className="rk-fl rk-fl-main" data-sel={sel.length === 0} aria-pressed={sel.length === 0} onClick={() => onSel(undefined)} style={{ borderTop: 0, padding: '9px 14px' }} data-testid="node-fleet-all">
          <span className="rk-fl-top">
            <span className="rk-fl-name">All nodes</span>
            <span className="rk-cnt">{nodes.length}</span>
          </span>
        </button>
        {list.map((f) => {
          const checked = sel.includes(f.id!)
          return (
            <div key={f.key} className="rk-fl" data-sel={checked} data-fleet={f.id} style={{ paddingRight: 6 }}>
              <label className="rk-fl-check" title={checked ? `Show every node again` : `Show only the nodes of ${f.name}`}>
                <input type="checkbox" checked={checked} aria-label={checked ? `Stop filtering by ${f.name}` : `Filter the nodes by ${f.name}`} onChange={() => pick(f, checked)} data-testid="node-fleet-check" />
              </label>
              <button className="rk-fl-main" aria-pressed={checked} onClick={() => onSel(checked && sel.length === 1 ? undefined : [f.id!])} title={checked && sel.length === 1 ? 'Show all nodes again' : `Show only ${f.name}`} data-testid="node-fleet-pick" style={{ padding: '8px 6px 8px 0' }}>
                <span className="rk-fl-top">
                  <span className="rk-fdot" data-c={f.color} />
                  <span className="rk-fl-name">{f.name}</span>
                  <span className="rk-cnt" data-testid="node-fleet-count">
                    {f.members.length}
                  </span>
                </span>
                <span className="rk-fl-def" style={{ fontSize: 11 }}>
                  {reqText(fleetOf(f))}
                  {f.deploy ? ` · deploys to ${f.deploy}` : ''}
                </span>
              </button>
              {admin && (
                <button className="rk-icon-btn" aria-label={`Edit fleet ${f.name}`} title="Edit this fleet" onClick={() => setEditor({ fleet: fleetOf(f) })} data-testid="node-fleet-edit">
                  <Pencil />
                </button>
              )}
            </div>
          )
        })}
        {loaded && list.length === 0 && (
          <p style={{ margin: 0, padding: '12px 14px', fontSize: 12.5, color: 'var(--fg3)' }}>
            No fleets yet. {admin ? 'Create one: nodes join it by their labels and taints, or by name.' : 'An administrator can create them.'}
          </p>
        )}
        {none && list.length > 0 && (
          <button className="rk-fl rk-fl-main" data-sel={sel[0] === 'none'} aria-pressed={sel[0] === 'none'} onClick={() => onSel(sel[0] === 'none' ? undefined : ['none'])} style={{ padding: '9px 14px' }} data-testid="node-fleet-none">
            <span className="rk-fl-top">
              <span className="rk-fl-name" style={{ fontWeight: 500 }}>
                Not in any fleet
              </span>
              <span className="rk-cnt">{none.members.length}</span>
            </span>
          </button>
        )}
        {fleetsPage && (
          <p style={{ margin: 0, padding: '10px 14px', fontSize: 12, color: 'var(--fg3)' }}>
            Every node against every label and taint:{' '}
            <a href={href('fleets')} style={{ color: 'var(--accent)' }}>
              Fleets
            </a>
            .
          </p>
        )}
      </div>
      {editor && admin && (
        <FleetEditor
          seed={editor}
          fleets={defs}
          nodes={nodes}
          now={now}
          onClose={() => setEditor(null)}
          onSave={async (f, isNew) => {
            await saveFleets(cluster, isNew ? [...defs, f] : defs.map((x) => (x.id === f.id ? f : x)))
            onToast(isNew ? `Created the fleet “${f.name}”.` : `Saved the fleet “${f.name}”.`)
            setEditor(null)
            onSel([f.id])
          }}
          onDelete={async (f) => {
            await saveFleets(cluster, defs.filter((x) => x.id !== f.id))
            onToast(`Deleted the fleet “${f.name}”. Its nodes keep their labels and taints.`)
            setEditor(null)
            onSel(undefined)
          }}
        />
      )}
    </aside>
  )
}
