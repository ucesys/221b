import { useEffect, useRef, useState } from 'react'
import { COLUMNS, type ColumnId } from '../lib/nodes'
import { Chevron, ColumnsIcon } from './Icons'

interface Props {
  hidden: ReadonlySet<ColumnId>
  onChange: (hidden: Set<ColumnId>) => void
  /** Columns the details panel hides for space; they show a note. */
  compact: boolean
}

/** The prototype's Columns menu: the first column always shows. */
export function ColumnsMenu({ hidden, onChange, compact }: Props) {
  const [open, setOpen] = useState(false)
  const button = useRef<HTMLButtonElement>(null)
  const menu = useRef<HTMLDivElement>(null)
  const optional = COLUMNS.slice(1)
  const shown = optional.filter((c) => !hidden.has(c.id)).length + 1
  const custom = hidden.size > 0

  useEffect(() => {
    if (open) menu.current?.querySelector('input')?.focus()
  }, [open])

  const close = () => {
    setOpen(false)
    button.current?.focus()
  }

  const toggle = (id: ColumnId) => {
    const next = new Set(hidden)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    onChange(next)
  }

  return (
    <div
      className="rk-dd"
      style={{ position: 'relative' }}
      onKeyDown={(e) => {
        if (e.key === 'Escape' && open) {
          e.stopPropagation()
          close()
        }
      }}
    >
      <button
        ref={button}
        className="rk-btn"
        aria-expanded={open}
        aria-haspopup="true"
        data-on={custom}
        title="Choose which columns to show"
        onClick={() => setOpen(!open)}
        style={{ height: 32 }}
      >
        <ColumnsIcon />
        Columns
        {custom && (
          <span className="rk-cnt">
            {shown}/{COLUMNS.length}
          </span>
        )}
        <Chevron />
      </button>
      {open && (
        <>
          <button className="rk-ddcatch" tabIndex={-1} aria-hidden="true" onClick={() => setOpen(false)} />
          <div ref={menu} className="rk-ddmenu" data-align="right" role="group" aria-label="Columns to show">
            <div className="rk-ddhead">The first column always shows</div>
            {optional.map((c) => (
              <label key={c.id} className="rk-ddopt">
                <input type="checkbox" checked={!hidden.has(c.id)} onChange={() => toggle(c.id)} />
                <span style={{ flex: '1 1 auto' }}>{c.label}</span>
                {compact && c.wideOnly && !hidden.has(c.id) && <span style={{ fontSize: 11, color: 'var(--fg3)' }}>after details close</span>}
              </label>
            ))}
            <div className="rk-ddsep" />
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10, padding: '2px 4px 2px 8px' }}>
              <button className="rk-link" onClick={() => onChange(new Set())} style={{ fontSize: 12 }}>
                Reset to default
              </button>
              <button className="rk-btn" onClick={close} style={{ height: 28 }}>
                Done
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  )
}
