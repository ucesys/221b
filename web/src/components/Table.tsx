import { Children, cloneElement, isValidElement, useEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react'

/**
 * Scrolls sideways when its content is wider than the space, as a table
 * is on a phone. Only then is it a tab stop and a named region, so the
 * keyboard can reach what scrolls (WCAG 2.1.1) without a stop on every
 * table that fits.
 */
export function ScrollBox({ label, children, className, style }: { label: string; children: ReactNode; className?: string; style?: CSSProperties }) {
  const ref = useRef<HTMLDivElement>(null)
  const [scrolls, setScrolls] = useState(false)
  useEffect(() => {
    const el = ref.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(() => setScrolls(el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return (
    <div ref={ref} className={className} style={{ overflowX: 'auto', ...style }} tabIndex={scrolls ? 0 : undefined} role={scrolls ? 'region' : undefined} aria-label={scrolls ? label : undefined}>
      {children}
    </div>
  )
}

/**
 * A small static table for a details panel: a header row and plain rows,
 * laid out as a grid with the same look as the main tables. For sortable,
 * windowed tables see DataTable.
 */
export function Table({ label, cols, template, minWidth, children }: { label: string; cols: { label: string; right?: boolean }[]; template: string; minWidth?: number; children: ReactNode }) {
  return (
    <div className="rk-grid" style={{ ['--cols' as string]: template, minWidth }} role="table" aria-label={label}>
      <div className="rk-gh" role="row">
        {cols.map((c) => (
          <span key={c.label} className={c.right ? 'rk-thl rk-r' : 'rk-thl'} role="columnheader">
            {c.label}
          </span>
        ))}
      </div>
      {children}
    </div>
  )
}

/** A row of a Table; each child is one cell. */
export function TR({ children, top = false, style, ...rest }: { children: ReactNode; top?: boolean; style?: CSSProperties; [attr: `data-${string}`]: string | undefined }) {
  return (
    <div className={top ? 'rk-gr rk-top' : 'rk-gr'} role="row" style={style} {...rest}>
      {Children.map(children, (c) => (isValidElement<{ role?: string }>(c) ? cloneElement(c, { role: 'cell' }) : c))}
    </div>
  )
}
