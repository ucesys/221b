import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import type { NodeRow } from '../api/types'
import { pickedNodes, setPickedNodes } from '../lib/fleets'

/**
 * Nodes picked on the Fleets tab (asked after 0.7.1) and the Nodes page
 * (asked October 30, 2026): one list per cluster, kept across tab switches.
 * Gone nodes drop out of it.
 */
export function usePickedNodes(cluster: string, nodes: NodeRow[]) {
  const [state, setState] = useState(() => ({ cluster, names: pickedNodes(cluster) }))
  const names = state.cluster === cluster ? state.names : pickedNodes(cluster)
  const pickedRows = useMemo(() => {
    const byName = new Map(nodes.map((n) => [n.name, n]))
    return names.flatMap((n) => byName.get(n) ?? [])
  }, [names, nodes])
  const picks = useMemo(() => pickedRows.map((n) => n.name), [pickedRows])
  const pickSet = useMemo(() => new Set(picks), [picks])
  const setPicks = useCallback(
    (next: string[]) => {
      setPickedNodes(cluster, next)
      setState({ cluster, names: next })
    },
    [cluster],
  )
  const togglePick = useCallback((name: string) => setPicks(pickSet.has(name) ? picks.filter((n) => n !== name) : [...picks, name]), [picks, pickSet, setPicks])
  return { picks, pickedRows, pickSet, setPicks, togglePick }
}

/** The box beside a node's name, in a 24px target; a click on it is not a click on the row. */
export function PickBox({ name, picked, onToggle }: { name: string; picked: boolean; onToggle: (name: string) => void }) {
  return (
    <label className="rk-pickbox" onClick={(e) => e.stopPropagation()}>
      <input type="checkbox" checked={picked} onChange={() => onToggle(name)} aria-label={`Pick ${name}`} title={picked ? `Unpick ${name}` : `Pick ${name}, to put it in a fleet or take it out`} data-testid="fleet-pick-node" />
    </label>
  )
}

/** The Nodes table's picks, for its name column: nothing where this person has nothing to do with them. */
export const NodePicking = createContext<{ picked: Set<string>; toggle: (name: string) => void; shown: string[]; setMany: (names: string[], on: boolean) => void } | null>(null)

/** The box in a table row, from the page's picks. */
export function RowPickBox({ name }: { name: string }) {
  const ctx = useContext(NodePicking)
  if (!ctx) return null
  return <PickBox name={name} picked={ctx.picked.has(name)} onToggle={ctx.toggle} />
}

/** The box in the name column's heading: picks every node in view, or, when all are, unpicks them; half ticked when some are. */
export function HeadPickBox() {
  const ctx = useContext(NodePicking)
  const box = useRef<HTMLInputElement>(null)
  const n = ctx ? ctx.shown.filter((name) => ctx.picked.has(name)).length : 0
  const all = !!ctx && ctx.shown.length > 0 && n === ctx.shown.length
  const some = n > 0 && !all
  useEffect(() => {
    if (box.current) box.current.indeterminate = some
  }, [some])
  if (!ctx) return null
  const what = all ? `Unpick the ${ctx.shown.length} nodes in view` : `Pick the ${ctx.shown.length} nodes in view`
  return (
    <label className="rk-pickbox" style={{ alignSelf: 'center', margin: '0 0 0 4px' }}>
      <input ref={box} type="checkbox" checked={all} disabled={!ctx.shown.length} onChange={() => ctx.setMany(ctx.shown, !all)} aria-label={what} title={what} data-testid="fleet-pick-all" />
    </label>
  )
}
