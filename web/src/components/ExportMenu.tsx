import { useEffect, useRef, useState } from 'react'
import { EXPORT_FORMATS, type ExportFormat } from '../lib/table'
import { Chevron, CsvIcon, ExcelIcon, ExportIcon, JsonIcon } from './Icons'

export type { ExportFormat }

interface Props {
  onExport: (format: ExportFormat) => void
  disabled: boolean
  /** What is exported, for the tooltip: "the nodes in view". */
  what?: string
}

/** The Export menu every table has: the rows and columns on screen, as Excel, CSV or JSON. */
export function ExportMenu({ onExport, disabled, what = 'the rows in view' }: Props) {
  const [open, setOpen] = useState(false)
  const button = useRef<HTMLButtonElement>(null)
  const menu = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (open) menu.current?.querySelector('button')?.focus()
  }, [open])

  const close = () => {
    setOpen(false)
    button.current?.focus()
  }

  const pick = (f: ExportFormat) => {
    close()
    onExport(f)
  }

  const icons = { xlsx: <ExcelIcon />, csv: <CsvIcon />, json: <JsonIcon /> }
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
      <button ref={button} className="rk-btn" aria-expanded={open} aria-haspopup="true" title={`Download ${what}`} disabled={disabled} onClick={() => setOpen(!open)} style={{ height: 32 }}>
        <ExportIcon />
        Export
        <Chevron />
      </button>
      {open && (
        <>
          <button className="rk-ddcatch" tabIndex={-1} aria-hidden="true" onClick={() => setOpen(false)} />
          <div ref={menu} className="rk-ddmenu" data-align="right" role="group" aria-label="Export format" style={{ minWidth: 200 }}>
            <div className="rk-ddhead">The rows and columns on screen, in this order</div>
            {EXPORT_FORMATS.map((f) => (
              <button key={f.id} className="rk-ddopt" onClick={() => pick(f.id)}>
                {icons[f.id]}
                <span style={{ flex: '1 1 auto' }}>{f.label}</span>
                <span className="rk-mono" style={{ fontSize: 11, color: 'var(--fg3)' }}>
                  {f.ext}
                </span>
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  )
}
