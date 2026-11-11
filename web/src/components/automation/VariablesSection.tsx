import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { ALL_NODES, fetchFleets, saveFleetVariables, type AutomationFleets, type VariableChange, type Privilege, type ScriptSummary } from '../../api/automation'
import { useView } from '../../api/live'
import type { FleetRow } from '../../api/types'
import type { EditorSeed } from '../FleetEditor'
import { fleetOf, reqText } from '../../lib/fleets'
import { ownValues, variableSaves, scopeRows, type Override, type VariableEdit, type VariableRow } from '../../lib/variables'
import { Chevron, PencilIcon, PlusIcon } from '../Icons'
import { VariableChangesPanel, ImpactList } from './VariableChanges'
import { FleetEdit } from './FleetEdit'
import { VariableDrawer } from './VariableDrawer'
import { Seg } from './Seg'
import { RebootTimeout } from './RebootTimeout'

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** The Overwritten column: what the value overwrites, and which nodes differ. */
function Flags({ row }: { row: VariableRow }) {
  const clashing = row.over.filter((o) => o.conflict).length
  return (
    <>
      {row.conflict && <div>Conflict: {row.conflict.join(' and ')}</div>}
      {row.overwrites && <div>Overwrites {row.overwrites.from}</div>}
      {row.over.length > 0 && (
        <div
          title={row.over
            .slice(0, 12)
            .map((o) => o.node)
            .join(', ')}
        >
          On {row.over.length} {row.over.length === 1 ? 'node' : 'nodes'}
          {clashing > 0 ? `, ${clashing} in conflict` : ''}
        </div>
      )}
    </>
  )
}

/**
 * The Variables page: All nodes, each node under it and the fleets on the
 * left; on the right a table of what the one picked gets, and a drawer to
 * change a variable.
 */
export function VariablesSection({
  cluster,
  scripts,
  linkedFleet,
  now,
  frame,
  onToast,
}: {
  cluster: string
  scripts: ScriptSummary[]
  /** The fleet a link opens on, as Pending approvals sends a reviewer here. */
  linkedFleet: string | null
  now: number
  /** The page beside the pane. */
  frame: (body: ReactNode) => ReactNode
  onToast: (msg: string) => void
}) {
  const [fleets, setFleets] = useState<AutomationFleets | null>(null)
  const [err, setErr] = useState('')
  const [fleetId, setFleetId] = useState('')
  const [node, setNode] = useState<string | null>(null)
  const [nodesOpen, setNodesOpen] = useState(false)
  // A row being edited, or null for a new parameter.
  const [editing, setEditing] = useState<VariableRow | null | undefined>(undefined)
  const [ceiling, setCeiling] = useState<Privilege>('none')
  const [ceilingWhy, setCeilingWhy] = useState('')
  const [ceilingErr, setCeilingErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [lastChange, setLastChange] = useState<VariableChange | null>(null)
  const [changesAt, setChangesAt] = useState(0)
  const [fleetEdit, setFleetEdit] = useState<EditorSeed | null>(null)
  const { rows: fleetRows } = useView<FleetRow>('fleets')

  const showCeiling = useCallback((f: AutomationFleets, id: string) => {
    const p = f.fleets.find((x) => x.id === id)?.params
    setCeiling(p?.ceiling === 'full' ? 'full' : 'none')
    setCeilingWhy('')
    setCeilingErr('')
  }, [])

  useEffect(() => {
    let gone = false
    fetchFleets(cluster)
      .then((f) => {
        if (gone) return
        setFleets(f)
        const id = f.fleets.find((x) => x.id === linkedFleet)?.id ?? f.fleets.find((x) => x.id === ALL_NODES)?.id ?? f.fleets[0]?.id ?? ''
        setFleetId(id)
        setNode(null)
        showCeiling(f, id)
      })
      .catch((e: unknown) => !gone && setErr(errText(e)))
    return () => {
      gone = true
    }
  }, [cluster, linkedFleet, showCeiling])

  const pick = (id: string, n: string | null) => {
    if (!fleets) return
    setFleetId(id)
    setNode(n)
    setLastChange(null)
    showCeiling(fleets, id)
  }
  const reload = useCallback(async () => {
    const fresh = await fetchFleets(cluster)
    setFleets(fresh)
    return fresh
  }, [cluster])

  const fleet = fleets?.fleets.find((f) => f.id === fleetId)
  const everyNode = fleets?.fleets.find((f) => f.id === ALL_NODES)
  const others = (fleets?.fleets ?? []).filter((f) => f.id !== ALL_NODES)
  const scope = useMemo(() => ({ fleet: fleetId, node }), [fleetId, node])
  const rows = useMemo(() => (fleets && fleet ? scopeRows(fleets.fleets, scope) : []), [fleets, fleet, scope])
  const place = node ?? fleet?.name ?? ''
  const clashes = (fleets?.conflicts ?? []).filter((c) => (node ? c.node === node : c.fleets.includes(fleet?.name ?? '')))
  const fullScripts = scripts.filter((s) => s.privilege === 'full').map((s) => s.name)
  const ceilingChanged = !!fleet && ceiling !== (fleet.params?.ceiling === 'full' ? 'full' : 'none')

  // Read just before saving: what is sent replaces all of a fleet's values.
  const apply = async (edit: VariableEdit, at: typeof scope, reason: string) => {
    const now = await fetchFleets(cluster)
    let change: VariableChange | null = null
    for (const s of variableSaves(now.fleets, at, edit)) {
      const r = await saveFleetVariables(cluster, s.fleet, {
        ...s.params,
        reason: reason || undefined,
      })
      change = r.change ?? change
    }
    setLastChange(change)
    setChangesAt((n) => n + 1)
    await reload()
    return change
  }
  const saveParam = async (edit: VariableEdit, reason: string) => {
    const change = await apply(edit, scope, reason)
    const what = edit.name ?? edit.old ?? ''
    setEditing(undefined)
    onToast(change?.state === 'waiting' ? `The change to ${what} on ${place} waits for approval; the current value stays.` : edit.name ? `${what} is saved on ${place}.` : `${what} is deleted from ${place}.`)
  }
  const dropOverride = async (name: string, o: Override) => {
    const change = await apply({ old: name }, { fleet: ALL_NODES, node: o.node }, '')
    setEditing(undefined)
    onToast(change?.state === 'waiting' ? `Removing the value of ${o.node} waits for approval.` : `${o.node} inherits ${name} again.`)
  }
  const saveCeiling = async () => {
    if (!fleet) return
    setBusy(true)
    setCeilingErr('')
    try {
      const now = await fetchFleets(cluster)
      const p = now.fleets.find((f) => f.id === fleet.id)?.params ?? {}
      const { change } = await saveFleetVariables(cluster, fleet.id, {
        ...p,
        ceiling,
        reason: ceilingWhy || undefined,
      })
      setLastChange(change ?? null)
      setChangesAt((n) => n + 1)
      showCeiling(await reload(), fleet.id)
      onToast(change?.state === 'waiting' ? `The ceiling of ${fleet.name} waits for approval.` : `The ceiling of ${fleet.name} is saved.`)
    } catch (e) {
      setCeilingErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  const defined = new Map(fleetRows.filter((r) => r.kind === 'fleet').map((r) => [r.id, r]))
  const valuesLine = (n: number) => (n === 0 ? 'No variables' : n === 1 ? '1 variable' : `${n} variables`)
  const conflicted = (name: string) => (fleets?.conflicts ?? []).some((c) => c.fleets.includes(name))
  return (
    <>
      <aside
        className="rk-pane"
        aria-label="Fleets"
        style={{
          flex: '0 0 250px',
          display: 'flex',
          flexDirection: 'column',
          background: 'var(--surface)',
          borderRight: '1px solid var(--line)',
          overflow: 'auto',
        }}
        data-testid="params-scopes"
      >
        <div
          style={{
            padding: '14px 14px 10px',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            gap: 8,
            borderBottom: '1px solid var(--line)',
          }}
        >
          <h2 className="rk-h">
            Fleets{' '}
            <span
              style={{
                fontWeight: 500,
                letterSpacing: 0,
                textTransform: 'none',
              }}
            >
              {others.length}
            </span>
          </h2>
          <button className="rk-btn" onClick={() => setFleetEdit({ fleet: null })} style={{ height: 26 }} data-testid="params-fleet-new">
            <PlusIcon />
            New
          </button>
        </div>
        <div style={{ padding: '0 0 6px' }}>
          {everyNode && (
            <div className="rk-fl" data-sel={fleetId === ALL_NODES && node === null} style={{ borderTop: 0, padding: 0 }}>
              <button className="rk-fl-main" aria-pressed={fleetId === ALL_NODES && node === null} onClick={() => pick(ALL_NODES, null)} style={{ padding: '9px 4px 9px 14px' }} data-testid="params-scope" data-id={ALL_NODES}>
                <span className="rk-fl-top">
                  <span className="rk-fl-name">{everyNode.name}</span>
                  <span className="rk-cnt">{everyNode.nodes.length}</span>
                </span>
                <span className="rk-fl-def" style={{ fontSize: 11 }}>
                  {valuesLine(Object.keys(everyNode.params?.values ?? {}).length)}
                </span>
              </button>
              <button
                className="rk-icon-btn"
                aria-expanded={nodesOpen}
                aria-label={nodesOpen ? 'Hide the nodes' : 'Show the nodes'}
                title={nodesOpen ? 'Hide the nodes' : 'Show the nodes'}
                onClick={() => setNodesOpen(!nodesOpen)}
                style={{ flex: 'none', marginRight: 6 }}
                data-testid="params-nodes-toggle"
              >
                <span
                  style={{
                    display: 'inline-flex',
                    transform: nodesOpen ? 'rotate(180deg)' : undefined,
                  }}
                >
                  <Chevron />
                </span>
              </button>
            </div>
          )}
          {nodesOpen && (
            <div style={{ maxHeight: 320, overflow: 'auto' }} data-testid="params-nodes">
              {(everyNode?.nodes ?? []).map((n) => {
                const own = fleets ? ownValues(fleets.fleets, n.name) : 0
                return (
                  <button
                    key={n.name}
                    className="rk-fl rk-fl-main"
                    data-sel={node === n.name}
                    aria-pressed={node === n.name}
                    onClick={() => pick(ALL_NODES, n.name)}
                    style={{ padding: '6px 14px 6px 28px' }}
                    data-testid="params-node"
                    data-node={n.name}
                  >
                    <span className="rk-fl-top">
                      <span className="rk-fl-name rk-mono">{n.name}</span>
                      {own > 0 && (
                        <span className="rk-cnt" title="Values of its own">
                          {own}
                        </span>
                      )}
                    </span>
                  </button>
                )
              })}
            </div>
          )}
          {others.map((f) => {
            const def = defined.get(f.id)
            const sel = fleetId === f.id && node === null
            return (
              <div key={f.id} className="rk-fl" data-sel={sel} data-fleet={f.id} style={{ paddingRight: 6 }}>
                <button className="rk-fl-main" aria-pressed={sel} onClick={() => pick(f.id, null)} style={{ padding: '8px 6px 8px 4px' }} data-testid="params-scope" data-id={f.id}>
                  <span className="rk-fl-top">
                    {def && <span className="rk-fdot" data-c={def.color} />}
                    <span className="rk-fl-name">{f.name}</span>
                    <span className="rk-cnt">{f.nodes.length}</span>
                  </span>
                  {def && (
                    <span className="rk-fl-def" style={{ fontSize: 11 }}>
                      {reqText(fleetOf(def))}
                      {def.deploy ? ` · deploys to ${def.deploy}` : ''}
                    </span>
                  )}
                  <span className="rk-fl-def" style={{ fontSize: 11 }}>
                    {valuesLine(Object.keys(f.params?.values ?? {}).length)}
                  </span>
                  {conflicted(f.name) && (
                    <span className="rk-t" data-tone="err" style={{ fontSize: 11.5, fontWeight: 500 }}>
                      Variable conflict
                    </span>
                  )}
                </button>
                {def && (
                  <button className="rk-icon-btn" aria-label={`Edit fleet ${f.name}`} title="Edit this fleet" onClick={() => setFleetEdit({ fleet: fleetOf(def) })} data-testid="params-fleet-edit">
                    <PencilIcon />
                  </button>
                )}
              </div>
            )
          })}
          {fleets && others.length === 0 && (
            <p
              style={{
                margin: 0,
                padding: '12px 14px',
                fontSize: 12.5,
                color: 'var(--fg3)',
              }}
            >
              No fleets yet. Create one: nodes join it by their labels and taints, or by name.
            </p>
          )}
        </div>
      </aside>
      {frame(
        <div
          style={{
            flex: '1 1 560px',
            minWidth: 0,
            display: 'flex',
            flexDirection: 'column',
            gap: 14,
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <h2 style={{ flex: '1 1 auto', margin: 0, fontSize: 15, fontWeight: 600 }}>{place || 'Variables'}</h2>
            {fleet && (
              <button className="rk-btn" data-on onClick={() => setEditing(null)} data-testid="params-new">
                <PlusIcon />
                New variable
              </button>
            )}
          </div>
          <p className="tb-hint" style={{ margin: 0 }}>
            {node
              ? 'What this node gets. Its own value wins over everything it inherits, except a value given to a run.'
              : fleet?.id === ALL_NODES
                ? 'What every node gets. A run’s own value wins, then the node’s, then its fleets’, then All nodes’.'
                : 'What this fleet’s nodes get. Red: some of them get something else.'}
          </p>
          {err && <p className="tb-err">{err}</p>}
          {fleet && !node && (
            <section
              className="rk-card"
              style={{
                padding: '14px 16px',
                display: 'flex',
                flexDirection: 'column',
                gap: 10,
              }}
              aria-label="Privilege ceiling"
            >
              <h3 className="rk-h" style={{ margin: 0 }}>
                Privilege ceiling
              </h3>
              <Seg
                label="Privilege ceiling"
                value={ceiling}
                onChange={setCeiling}
                options={[
                  { id: 'none', label: 'nobody: read-only' },
                  {
                    id: 'full',
                    label: 'elevated: scripts run as the ssh-paired account',
                  },
                ]}
              />
              <p className="tb-hint" style={{ margin: 0 }}>
                {ceiling === 'none'
                  ? fullScripts.length
                    ? `These scripts declare privilege elevated and will not run here: ${fullScripts.join(', ')}. Their dry runs still do.`
                    : 'No script declares privilege elevated yet.'
                  : 'Scripts that declare privilege elevated may run with all the rights of the ssh-paired account, root or sudo.'}
              </p>
              {ceilingChanged && (
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
                  <span className="rk-field" style={{ width: 300, maxWidth: '100%' }}>
                    <input value={ceilingWhy} placeholder="Reason for this change (optional)" aria-label="Reason for this change" onChange={(e) => setCeilingWhy(e.target.value)} />
                  </span>
                  <button className="rk-btn" data-on disabled={busy} onClick={() => void saveCeiling()} data-testid="params-ceiling-save">
                    {busy ? 'Saving…' : 'Save ceiling'}
                  </button>
                </div>
              )}
              {ceilingErr && <p className="tb-err">{ceilingErr}</p>}
            </section>
          )}
          {fleets && fleet && (
            <RebootTimeout
              cluster={cluster}
              fleets={fleets}
              fleetId={fleetId}
              node={node}
              onSaved={async (change, msg) => {
                setLastChange(change)
                setChangesAt((n) => n + 1)
                await reload()
                onToast(msg)
              }}
            />
          )}
          {clashes.length > 0 && (
            <div className="rk-callout" data-tone="err" style={{ margin: 0, fontSize: 12.5 }} data-testid="params-conflicts">
              Defined on more than one fleet, so a run on these nodes is refused until it stays on one, or the node or the run sets it:
              <ul style={{ margin: '4px 0 0', paddingLeft: 18 }}>
                {clashes.map((c) => (
                  <li key={`${c.node}/${c.param}`}>
                    <span className="rk-mono">{c.param}</span> on <span className="rk-mono">{c.node}</span>: {c.fleets.join(' and ')}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {fleet && (
            <section className="rk-card" style={{ padding: '4px 16px 8px' }} aria-label="Variables">
              {rows.length === 0 ? (
                <p className="tb-hint" style={{ margin: '10px 0' }} data-testid="params-empty">
                  No variables here yet.
                </p>
              ) : (
                <table className="tb-table" data-testid="params-table">
                  <thead>
                    <tr>
                      <th>Variable</th>
                      <th>Value</th>
                      <th>From</th>
                      <th>Overwritten</th>
                      <th aria-label="Actions" />
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((r) => {
                      const red = r.over.length > 0 || !!r.conflict
                      return (
                        <tr key={r.name} data-testid="param-row" data-name={r.name} data-red={red} style={{ color: red ? 'var(--err)' : undefined }}>
                          <td className="rk-mono">
                            {r.name}
                            {r.sensitive && (
                              <span className="rk-pill" data-tone="warn" style={{ marginLeft: 6 }}>
                                Sensitive
                              </span>
                            )}
                          </td>
                          <td className="rk-mono" style={{ overflowWrap: 'anywhere' }}>
                            {r.conflict ? 'conflict' : r.value}
                          </td>
                          <td>{r.from}</td>
                          <td data-testid="param-flags">
                            <Flags row={r} />
                          </td>
                          <td style={{ textAlign: 'right' }}>
                            <button className="rk-icon-btn" style={{ margin: '-6px 0' }} aria-label={`Edit ${r.name}`} title={`Edit ${r.name}`} onClick={() => setEditing(r)} data-testid="param-edit">
                              <PencilIcon />
                            </button>
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              )}
            </section>
          )}
          {lastChange && lastChange.state !== 'waiting' && lastChange.impact.length > 0 && (
            <section data-testid="params-impact">
              <h3 className="rk-h" style={{ margin: '0 0 6px' }}>
                What this change touched
              </h3>
              {lastChange.note && (
                <p className="tb-hint" style={{ margin: '0 0 6px' }}>
                  {lastChange.note}
                </p>
              )}
              <ImpactList items={lastChange.impact} />
            </section>
          )}
          {fleet && (
            <VariableChangesPanel
              cluster={cluster}
              fleet={fleet.id}
              version={changesAt}
              onDecided={() => {
                setChangesAt((n) => n + 1)
                reload()
                  .then((f) => showCeiling(f, fleetId))
                  .catch((e: unknown) => setErr(errText(e)))
              }}
            />
          )}
        </div>,
      )}
      {fleetEdit && (
        <FleetEdit
          cluster={cluster}
          seed={fleetEdit}
          now={now}
          onToast={onToast}
          onClose={() => setFleetEdit(null)}
          onSaved={async (f, isNew) => {
            const fresh = await reload()
            setFleetEdit(null)
            setFleetId(f.id)
            setNode(null)
            setLastChange(null)
            showCeiling(fresh, f.id)
            onToast(isNew ? `Created the fleet “${f.name}”.` : `Saved the fleet “${f.name}”.`)
          }}
          onDeleted={async (f) => {
            const fresh = await reload()
            setFleetEdit(null)
            if (fleetId === f.id) {
              setFleetId(ALL_NODES)
              setNode(null)
              showCeiling(fresh, ALL_NODES)
            }
            onToast(`Deleted the fleet “${f.name}”. Its nodes keep their labels and taints.`)
          }}
        />
      )}
      {editing !== undefined && (
        <VariableDrawer
          key={editing?.name ?? 'new'}
          row={editing}
          rows={rows}
          place={place}
          onNode={node !== null}
          scripts={scripts}
          onSave={saveParam}
          onDropOverride={(o) => dropOverride(editing?.name ?? '', o)}
          onShowNode={(n) => {
            setEditing(undefined)
            setNodesOpen(true)
            pick(ALL_NODES, n)
          }}
          onClose={() => setEditing(undefined)}
        />
      )}
    </>
  )
}
