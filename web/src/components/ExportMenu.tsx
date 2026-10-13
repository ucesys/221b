import { useEffect, useRef, useState } from 'react'
import { Chevron, ExcelIcon, ExportIcon, JsonIcon } from './Icons'

export type ExportFormat = 'xlsx' | 'json'

interface Props {
  onExport: (format: ExportFormat) => void
  disabled: boolean
}

/** The Export menu: the rows and columns on screen, as Excel or JSON. */
export function ExportMenu({ onExport, disabled }: Props) {
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
      <button ref={button} className="rk-btn" aria-expanded={open} aria-haspopup="true" title="Download the nodes in view" disabled={disabled} onClick={() => setOpen(!open)} style={{ height: 32 }}>
        <ExportIcon />
        Export
        <Chevron />
      </button>
      {open && (
        <>
          <button className="rk-ddcatch" tabIndex={-1} aria-hidden="true" onClick={() => setOpen(false)} />
          <div ref={menu} className="rk-ddmenu" data-align="right" role="group" aria-label="Export format" style={{ minWidth: 200 }}>
            <div className="rk-ddhead">The rows and columns on screen, in this order</div>
            <button className="rk-ddopt" onClick={() => pick('xlsx')}>
              <ExcelIcon />
              <span style={{ flex: '1 1 auto' }}>Excel</span>
              <span className="rk-mono" style={{ fontSize: 11, color: 'var(--fg3)' }}>
                .xlsx
              </span>
            </button>
            <button className="rk-ddopt" onClick={() => pick('json')}>
              <JsonIcon />
              <span style={{ flex: '1 1 auto' }}>JSON</span>
              <span className="rk-mono" style={{ fontSize: 11, color: 'var(--fg3)' }}>
                .json
              </span>
            </button>
          </div>
        </>
      )}
    </div>
  )
}
