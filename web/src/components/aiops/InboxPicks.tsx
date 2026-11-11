// Picked cases in the Inbox (Phase 12, wave 6c, CF-38): a box on each
// case row and one in the first column's heading that picks every case in
// view, for the bar's one action on many (BulkBar.tsx). Only those who
// work the cases get the boxes; the page gives the picks through context,
// as the Nodes table does (NodePicks.tsx).
import { createContext, useContext, useEffect, useRef } from 'react'

export interface InboxPicking {
  /** The cases picked, by id: "C-12". */
  picked: Set<string>
  toggle: (id: string) => void
  /** The cases in view, by id, in the table's order. */
  shown: string[]
  setMany: (ids: string[], on: boolean) => void
}

export const InboxPicks = createContext<InboxPicking | null>(null)

/** A case row's box, in a 24px target; a click on it is not a click on the row. */
export function RowPickBox({ id }: { id: string }) {
  const ctx = useContext(InboxPicks)
  if (!ctx) return null
  const on = ctx.picked.has(id)
  return (
    <label className="rk-pickbox" onClick={(e) => e.stopPropagation()}>
      <input type="checkbox" checked={on} onChange={() => ctx.toggle(id)} aria-label={`Pick ${id}`} title={on ? `Unpick ${id}` : `Pick ${id}, to do one thing to many cases`} data-testid="inbox-pick" data-id={id} />
    </label>
  )
}

/** The heading's box: picks every case in view, or, when all are, unpicks them; half ticked when some are. */
export function HeadPickBox() {
  const ctx = useContext(InboxPicks)
  const box = useRef<HTMLInputElement>(null)
  const n = ctx ? ctx.shown.filter((id) => ctx.picked.has(id)).length : 0
  const all = !!ctx && ctx.shown.length > 0 && n === ctx.shown.length
  const some = n > 0 && !all
  useEffect(() => {
    if (box.current) box.current.indeterminate = some
  }, [some])
  if (!ctx) return null
  const what = all ? `Unpick the ${ctx.shown.length} cases in view` : `Pick the ${ctx.shown.length} cases in view`
  return (
    <label className="rk-pickbox" style={{ alignSelf: 'center', margin: '0 0 0 4px' }} onClick={(e) => e.stopPropagation()}>
      <input ref={box} type="checkbox" checked={all} disabled={!ctx.shown.length} onChange={() => ctx.setMany(ctx.shown, !all)} aria-label={what} title={what} data-testid="inbox-pick-all" />
    </label>
  )
}

/** Where a row that cannot be picked (not a case) keeps the box's room, so the priorities line up. */
export function RowPickSpace() {
  const ctx = useContext(InboxPicks)
  if (!ctx) return null
  return <span style={{ width: 18, flex: 'none' }} aria-hidden="true" />
}
