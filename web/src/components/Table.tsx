import { Children, cloneElement, isValidElement, type CSSProperties, type ReactNode } from 'react'

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
