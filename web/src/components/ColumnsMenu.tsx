import { useEffect, useRef, useState } from 'react'
import type { ColumnDef } from '../lib/table'
import { Chevron, ColumnsIcon } from './Icons'

interface Props<R> {
  cols: ColumnDef<R>[]
  hidden: ReadonlySet<string>
  defaults: ReadonlySet<string>
  onChange: (hidden: Set<string>) => void
  /** Columns a details panel hides for space; they show a note. */
  compact: boolean
}

function sameSet(a: ReadonlySet<string>, b: ReadonlySet<string>): boolean {
  if (a.size !== b.size) return false
  for (const x of a) if (!b.has(x)) return false
  return true
}

/** The Columns menu every table has: the first column always shows. */
export function ColumnsMenu<R>({ cols, hidden, defaults, onChange, compact }: Props<R>) {
  const [open, setOpen] = useState(false)
  // Where the menu goes: fixed to the window, so a short table's card does
  // not clip it, below the button or above it when there is more room there.
  const [place, setPlace] = useState<React.CSSProperties>({})
  const button = useRef<HTMLButtonElement>(null)
  const menu = useRef<HTMLDivElement>(null)
  const optional = cols.slice(1)
  const shown = optional.filter((c) => !hidden.has(c.id)).length + 1
  const custom = !sameSet(hidden, defaults)

  useEffect(() => {
    if (open) menu.current?.querySelector('input')?.focus()
  }, [open])
  useEffect(() => {
    if (!open) return
    const shut = () => setOpen(false)
    window.addEventListener('resize', shut)
    return () => window.removeEventListener('resize', shut)
  }, [open])

  const openMenu = () => {
    const r = button.current?.getBoundingClientRect()
    if (r) {
      const below = window.innerHeight - r.bottom - 12
      const above = r.top - 12
      const right = Math.max(8, window.innerWidth - r.right)
      setPlace(
        below >= 280 || below >= above
          ? { position: 'fixed', top: r.bottom + 4, right, left: 'auto', bottom: 'auto', maxHeight: below, overflowY: 'auto' }
          : { position: 'fixed', bottom: window.innerHeight - r.top + 4, right, left: 'auto', top: 'auto', maxHeight: above, overflowY: 'auto' },
      )
    }
    setOpen(true)
  }

  const close = () => {
    setOpen(false)
    button.current?.focus()
  }

  const toggle = (id: string) => {
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
      <button ref={button} className="rk-btn" aria-expanded={open} aria-haspopup="true" data-on={custom} title="Choose which columns to show" onClick={() => (open ? setOpen(false) : openMenu())} style={{ height: 32 }}>
        <ColumnsIcon />
        Columns
        {custom && (
          <span className="rk-cnt">
            {shown}/{cols.length}
          </span>
        )}
        <Chevron />
      </button>
      {open && (
        <>
          <button className="rk-ddcatch" tabIndex={-1} aria-hidden="true" onClick={() => setOpen(false)} />
          <div ref={menu} className="rk-ddmenu" data-align="right" role="group" aria-label="Columns to show" style={place}>
            <div className="rk-ddhead" style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <span style={{ flex: '1 1 auto' }}>The first column always shows</span>
              <button className="rk-link" onClick={() => onChange(new Set())} style={{ fontSize: 12 }} data-testid="cols-all">
                Select all
              </button>
              <button className="rk-link" onClick={() => onChange(new Set(optional.map((c) => c.id)))} style={{ fontSize: 12 }} data-testid="cols-none">
                Clear all
              </button>
            </div>
            {optional.map((c) => (
              <label key={c.id} className="rk-ddopt">
                <input type="checkbox" checked={!hidden.has(c.id)} onChange={() => toggle(c.id)} />
                <span style={{ flex: '1 1 auto' }}>{c.group ? `${c.group}: ${c.label}` : c.label}</span>
                {compact && c.wideOnly && !hidden.has(c.id) && <span style={{ fontSize: 11, color: 'var(--fg3)' }}>after details close</span>}
              </label>
            ))}
            <div className="rk-ddsep" />
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10, padding: '2px 4px 2px 8px' }}>
              <button className="rk-link" onClick={() => onChange(new Set(defaults))} style={{ fontSize: 12 }}>
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
