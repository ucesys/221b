import { memo, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { gridTemplate, type ColumnDef, type SortState } from '../lib/table'

interface Props<R> {
  /** Names the table for assistive technology: "Nodes", "Pods". */
  label: string
  rows: R[]
  cols: ColumnDef<R>[]
  rowKey: (r: R) => string
  /** The row's accessible name: what the first cell says, plus its state. */
  rowLabel: (r: R, open: boolean) => string
  sort: SortState
  onSort: (col: string) => void
  selected?: string | null
  onOpen?: (key: string) => void
  compact?: boolean
  now: number
  /** Shown instead of rows when there are none. */
  empty: string
  /** A data attribute on each row, for tests: data-<rowAttr>=key. */
  rowAttr?: string
  /** A fixed row height in px; rows must not wrap. */
  rowHeight?: number
  /** Rendered after the rows, inside the card. */
  footer?: ReactNode
}

/** Rows above and below the visible range that are rendered anyway. */
const OVERSCAN = 12

/** The fewest rows rendered, so a table scrolled to is never blank. */
const MIN_WINDOW = 40

/** Tables up to this many rows are rendered whole; longer ones are windowed. */
const WINDOW_FROM = 100

/**
 * Finds the element that scrolls the table: a pane, or the window. The
 * table's own card scrolls sideways only, so the search starts above it.
 */
function scrollParent(el: HTMLElement | null): HTMLElement | Window {
  let p = el?.closest('.rk-card')?.parentElement ?? el?.parentElement ?? null
  while (p && p !== document.body) {
    const o = getComputedStyle(p).overflowY
    if (o === 'auto' || o === 'scroll') return p
    p = p.parentElement
  }
  return window
}

/**
 * Renders only the rows in view plus a margin, so a table of 15,000 pods
 * costs what a screenful does. Rows have a fixed height, so the position
 * of any row is known without rendering the ones above it. A count of 0
 * turns it off.
 */
function useWindow(count: number, rowHeight: number, container: React.RefObject<HTMLDivElement | null>): [number, number] {
  const [range, setRange] = useState<[number, number]>([0, Math.min(count, MIN_WINDOW)])
  useEffect(() => {
    const el = container.current
    if (!el || count === 0) return
    const sp = scrollParent(el)
    const update = () => {
      const viewTop = sp === window ? 0 : (sp as HTMLElement).getBoundingClientRect().top
      const viewHeight = sp === window ? window.innerHeight : (sp as HTMLElement).clientHeight
      const top = el.getBoundingClientRect().top - viewTop
      const first = Math.max(0, Math.floor(-top / rowHeight) - OVERSCAN)
      const last = Math.min(count, Math.max(first + MIN_WINDOW, Math.ceil((viewHeight - top) / rowHeight) + OVERSCAN))
      setRange((r) => (r[0] === first && r[1] === last ? r : [first, last]))
    }
    // Measured at once, and again after layout settles: a browser that
    // delays animation frames (WebKit, headless) must not show no rows.
    update()
    const frame = requestAnimationFrame(update)
    sp.addEventListener('scroll', update, { passive: true })
    window.addEventListener('resize', update)
    const ro = new ResizeObserver(update)
    ro.observe(el)
    return () => {
      cancelAnimationFrame(frame)
      sp.removeEventListener('scroll', update)
      window.removeEventListener('resize', update)
      ro.disconnect()
    }
  }, [container, count, rowHeight])
  let first = Math.min(range[0], count)
  let last = Math.min(range[1], count)
  if (last <= first && count > 0) {
    // Before the first measurement (a table that just grew past the
    // window threshold started from an empty range): the first rows.
    first = 0
    last = Math.min(count, MIN_WINDOW)
  }
  return [first, last]
}

/** The links and buttons in a row, in order. */
function focusables(row: HTMLElement): HTMLElement[] {
  return Array.from(row.querySelectorAll<HTMLElement>('a[href], button:not(:disabled), input:not(:disabled)'))
}

/** The rendered row next to this one in the given direction, if any. */
function neighbour(row: HTMLElement, dir: 1 | -1): HTMLElement | null {
  const el = dir > 0 ? row.nextElementSibling : row.previousElementSibling
  return el instanceof HTMLElement && el.classList.contains('rk-gr') ? el : null
}

const Row = memo(function Row<R>({
  r,
  cols,
  rowKey,
  index,
  label,
  selected,
  onOpen,
  onJump,
  now,
  rowAttr,
  style,
}: {
  r: R
  cols: ColumnDef<R>[]
  rowKey: string
  index: number
  label: string
  selected: boolean
  onOpen?: (key: string) => void
  onJump: (edge: 'first' | 'last') => void
  now: number
  rowAttr?: string
  style?: React.CSSProperties
}) {
  const attrs: Record<string, string> = rowAttr ? { [`data-${rowAttr}`]: rowKey } : {}
  return (
    <div
      className="rk-gr"
      role="row"
      aria-rowindex={index + 2}
      aria-label={label}
      aria-selected={onOpen ? selected : undefined}
      tabIndex={onOpen ? 0 : -1}
      data-sel={selected}
      data-key={rowKey}
      data-open={onOpen ? 'true' : undefined}
      style={style}
      onClick={(e) => {
        // Links and buttons inside a cell act on their own.
        if ((e.target as HTMLElement).closest('a, button, input')) return
        onOpen?.(rowKey)
      }}
      onKeyDown={(e) => {
        const row = e.currentTarget
        const target = e.target as HTMLElement
        switch (e.key) {
          case 'Enter':
          case ' ':
            if (onOpen && target === row) {
              e.preventDefault()
              onOpen(rowKey)
            }
            break
          case 'ArrowDown':
          case 'ArrowUp': {
            const next = neighbour(row, e.key === 'ArrowDown' ? 1 : -1)
            if (next) {
              e.preventDefault()
              next.focus()
            }
            break
          }
          case 'Home':
          case 'End':
            e.preventDefault()
            onJump(e.key === 'Home' ? 'first' : 'last')
            break
          case 'ArrowRight': {
            // Into the row's links and buttons, one at a time.
            const f = focusables(row)
            const i = target === row ? -1 : f.indexOf(target)
            if (i < f.length - 1 && f[i + 1]) {
              e.preventDefault()
              f[i + 1]!.focus()
            }
            break
          }
          case 'ArrowLeft': {
            const f = focusables(row)
            const i = f.indexOf(target)
            if (i >= 0) {
              e.preventDefault()
              ;(i === 0 ? row : f[i - 1]!).focus()
            }
            break
          }
        }
      }}
      {...attrs}
    >
      {cols.map((c, i) => (
        <span key={c.id} role={i === 0 ? 'rowheader' : 'gridcell'} aria-colindex={i + 1} className={c.right ? 'rk-r rk-cell' : 'rk-cell'}>
          {c.cell(r, now)}
        </span>
      ))}
    </div>
  )
}) as <R>(props: {
  r: R
  cols: ColumnDef<R>[]
  rowKey: string
  index: number
  label: string
  selected: boolean
  onOpen?: (key: string) => void
  onJump: (edge: 'first' | 'last') => void
  now: number
  rowAttr?: string
  style?: React.CSSProperties
}) => ReactNode

/**
 * A sortable grid of rows with a fixed height each. Rows are focusable: Enter
 * opens one, the arrow keys move between rows and into a row's links, and
 * Home and End jump to the ends. Only the rows in view are rendered.
 */
export function DataTable<R>({ label, rows, cols, rowKey, rowLabel, sort, onSort, selected = null, onOpen, compact = false, now, empty, rowAttr, rowHeight = 37, footer }: Props<R>) {
  const container = useRef<HTMLDivElement>(null)
  const { template, minWidth } = useMemo(() => gridTemplate(cols, compact), [cols, compact])
  const windowed = rows.length > WINDOW_FROM
  const [winFirst, winLast] = useWindow(windowed ? rows.length : 0, rowHeight, container)
  const [first, last] = windowed ? [winFirst, winLast] : [0, rows.length]
  const groups = useMemo(() => {
    const out: { label: string; start: number; end: number }[] = []
    cols.forEach((c, i) => {
      if (!c.group) return
      const g = out[out.length - 1]
      if (g && g.label === c.group && g.end === i - 1) g.end = i
      else out.push({ label: c.group, start: i, end: i })
    })
    return out
  }, [cols])
  const shown = rows.slice(first, last)
  const above = first * rowHeight
  const below = Math.max(0, rows.length - last) * rowHeight

  // Scrolls the first or last row into view, then focuses it once it is
  // rendered. The latest rows are read through a ref, so the callback is
  // stable and rows are not re-rendered for it.
  const latest = useRef({ rows, rowKey, rowHeight })
  useEffect(() => {
    latest.current = { rows, rowKey, rowHeight }
  })
  const jump = useCallback((edge: 'first' | 'last') => {
    const { rows, rowKey, rowHeight } = latest.current
    const el = container.current
    const row = rows[edge === 'first' ? 0 : rows.length - 1]
    if (!el || !row) return
    const key = rowKey(row)
    const index = edge === 'first' ? 0 : rows.length - 1
    const sp = scrollParent(el)
    const viewTop = sp === window ? 0 : (sp as HTMLElement).getBoundingClientRect().top
    const viewHeight = sp === window ? window.innerHeight : (sp as HTMLElement).clientHeight
    const rowTop = el.getBoundingClientRect().top + index * rowHeight - viewTop
    const by = rowTop < 0 ? rowTop - 8 : rowTop + rowHeight > viewHeight ? rowTop + rowHeight - viewHeight + 8 : 0
    if (by) sp.scrollBy({ top: by })
    const focus = (tries: number) => {
      const target = el.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`)
      if (target) target.focus()
      else if (tries > 0) requestAnimationFrame(() => focus(tries - 1))
    }
    requestAnimationFrame(() => focus(6))
  }, [])

  return (
    <div className="rk-card" style={{ overflowX: 'auto' }}>
      <div className="rk-grid" style={{ ['--cols' as string]: template, minWidth }} role="grid" aria-label={label} aria-rowcount={rows.length + 1} aria-colcount={cols.length} data-total={rows.length}>
        {groups.length > 0 && (
          <div className="rk-ghg" aria-hidden="true">
            {groups.map((g) => (
              <span key={g.label} className="rk-thl" style={{ gridColumn: `${g.start + 1} / ${g.end + 2}`, fontSize: 11, fontWeight: 600, letterSpacing: '.05em', textTransform: 'uppercase', color: 'var(--fg3)', borderBottom: '1px solid var(--line2)', margin: '6px 10px 0', padding: '0 0 4px' }}>
                {g.label}
              </span>
            ))}
          </div>
        )}
        <div className="rk-gh" role="row" aria-rowindex={1}>
          {cols.map((c, i) => {
            const on = c.id === sort.col
            const name = c.sortName ?? c.label.toLowerCase()
            return (
              <div key={c.id} role="columnheader" aria-colindex={i + 1} aria-sort={on ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'} className="rk-thc">
                <button className={c.right ? 'rk-th rk-r' : 'rk-th'} data-active={on} data-col={c.id} title={(on ? 'Sorted by ' : 'Sort by ') + name + (c.hint ? '\n\n' + c.hint : '')} aria-label={`${c.group ? c.group + ' ' : ''}${c.label}: ${on ? `sorted ${sort.dir === 'asc' ? 'ascending' : 'descending'}` : 'sort'}`} onClick={() => onSort(c.id)}>
                  {c.label}
                  <span className="rk-arrow" aria-hidden="true">
                    {on ? (sort.dir === 'asc' ? '↑' : '↓') : ''}
                  </span>
                </button>
              </div>
            )
          })}
        </div>
        <div ref={container} data-testid="rows" role="rowgroup">
          {above > 0 && <div style={{ height: above }} aria-hidden="true" />}
          {shown.map((r, i) => {
            const k = rowKey(r)
            return <Row<R> key={k} r={r} cols={cols} rowKey={k} index={first + i} label={rowLabel(r, selected === k)} selected={selected === k} onOpen={onOpen} onJump={jump} now={now} rowAttr={rowAttr} style={{ height: rowHeight }} />
          })}
          {below > 0 && <div style={{ height: below }} aria-hidden="true" />}
        </div>
      </div>
      {rows.length === 0 && <p style={{ margin: 0, padding: '28px 16px', textAlign: 'center', fontSize: 12.5, color: 'var(--fg3)' }}>{empty}</p>}
      {footer}
    </div>
  )
}
