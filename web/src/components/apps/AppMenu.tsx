import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { layer } from '../../lib/layer'
import { WrenchIcon } from '../Icons'
import type { AppRow } from '../../columns/apps'

/** What the table's rows need to offer their tools: whether changes run here, and where a pick goes. */
export const AppsActions = createContext<{ canChange: boolean; act: (a: AppAction, row: AppRow) => void } | null>(null)

/** A release's name with its Tools button, in the table (HA-10). */
export function NameWithAppTools({ row, children }: { row: AppRow; children: ReactNode }) {
  const ctx = useContext(AppsActions)
  return (
    <span className="tb-namecell">
      {children}
      {ctx && ctx.canChange && <AppToolsButton row={row} canChange={ctx.canChange} onPick={(a) => ctx.act(a, row)} />}
    </span>
  )
}

/** What the menu offers on a release (HA-10): each opens a dialog, or asks. */
export type AppAction = 'install' | 'deploy' | 'upgrade' | 'switch' | 'values' | 'source' | 'forget' | 'uninstall'

interface Item {
  key: AppAction
  label: string
  hint?: string
  danger?: boolean
  sepBefore?: boolean
  off?: string
}

/** The items for a release: without a source only Set source and Uninstall do anything. */
export function appItems(r: AppRow, canChange: boolean): Item[] {
  const off = canChange ? undefined : 'Watson is off here'
  if (r.orphan) {
    return [
      { key: 'upgrade', label: 'Install…', hint: 'from its source, with its values', off },
      { key: 'forget', label: 'Forget', hint: 'drops the record', sepBefore: true, danger: true, off },
    ]
  }
  return [
    { key: 'upgrade', label: 'Upgrade…', hint: r.app ? `from ${r.app.source.ref}` : undefined, off: off ?? (r.app ? undefined : 'set the source first') },
    { key: 'uninstall', label: 'Uninstall…', danger: true, sepBefore: true, off },
  ]
}

function place(anchor: HTMLElement | null, width = 300): CSSProperties {
  const r = anchor?.getBoundingClientRect()
  if (!r) return { top: 80, left: 80 }
  const left = Math.max(8, Math.min(window.innerWidth - width - 8, r.left - 8))
  const below = window.innerHeight - r.bottom - 8
  return below >= 240 ? { top: r.bottom + 4, left, maxHeight: below } : { bottom: window.innerHeight - r.top + 4, left, maxHeight: r.top - 8 }
}

/** The Tools button of a release on the Helm1 page, with its menu. */
export function AppToolsButton({ row, canChange, onPick, label = 'Tools' }: { row: AppRow; canChange: boolean; onPick: (a: AppAction) => void; label?: ReactNode }) {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null)
  const open = anchor !== null
  const what = `release ${row.name}`
  return (
    <>
      <button
        className={typeof label === 'string' && label !== 'Tools' ? 'rk-btn' : 'tb-tools'}
        aria-label={`Tools for ${what}`}
        title={`Tools for ${what}`}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={(e) => {
          e.stopPropagation()
          e.preventDefault()
          const el = e.currentTarget
          setAnchor((a) => (a ? null : el))
        }}
        onMouseDown={(e) => e.stopPropagation()}
        data-testid={`app-tools-${row.namespace}-${row.name}`}
      >
        {label === 'Tools' ? <WrenchIcon /> : label}
      </button>
      {open && <AppMenu row={row} canChange={canChange} anchor={anchor} onClose={() => setAnchor(null)} onPick={onPick} />}
    </>
  )
}

function AppMenu({ row, canChange, anchor, onClose, onPick }: { row: AppRow; canChange: boolean; anchor: HTMLElement | null; onClose: () => void; onPick: (a: AppAction) => void }) {
  const menu = useRef<HTMLDivElement>(null)
  const [style] = useState(() => place(anchor))
  const items = appItems(row, canChange)
  useEffect(() => {
    const away = (e: MouseEvent) => {
      if (menu.current && !menu.current.contains(e.target as Node) && !anchor?.contains(e.target as Node)) onClose()
    }
    const scroll = (e: Event) => {
      if (menu.current && !menu.current.contains(e.target as Node)) onClose()
    }
    document.addEventListener('mousedown', away)
    window.addEventListener('scroll', scroll, true)
    window.addEventListener('resize', onClose)
    return () => {
      document.removeEventListener('mousedown', away)
      window.removeEventListener('scroll', scroll, true)
      window.removeEventListener('resize', onClose)
    }
  }, [anchor, onClose])
  useLayoutEffect(() => {
    menu.current?.querySelector<HTMLElement>('[role=menuitem]:not([aria-disabled=true])')?.focus()
  }, [])
  const keys = (e: KeyboardEvent<HTMLDivElement>) => {
    e.stopPropagation()
    if (e.key === 'Escape') {
      onClose()
      anchor?.focus()
      return
    }
    if (e.key === 'Tab') {
      onClose()
      return
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    e.preventDefault()
    const all = [...(menu.current?.querySelectorAll<HTMLElement>('[role=menuitem]:not([aria-disabled=true])') ?? [])]
    const i = all.indexOf(document.activeElement as HTMLElement)
    all[(i + (e.key === 'ArrowDown' ? 1 : all.length - 1)) % all.length]?.focus()
  }
  return createPortal(
    <div ref={menu} className="tb-menu" role="menu" aria-label={`Tools for release ${row.name}`} style={style} onKeyDown={keys} onClick={(e) => e.stopPropagation()} onMouseDown={(e) => e.stopPropagation()} data-testid="app-menu">
      <div className="tb-mhead">
        Tools for release <span className="rk-mono">{row.name}</span> in <span className="rk-mono">{row.namespace}</span>
      </div>
      {items.map((it) => (
        <div key={it.key} style={{ display: 'contents' }}>
          {it.sepBefore && <div className="tb-msep" role="separator" />}
          <button
            role="menuitem"
            className="tb-mi"
            aria-disabled={it.off ? true : undefined}
            data-danger={it.danger || undefined}
            title={it.off ? `${it.label.replace('…', '')}: ${it.off}` : undefined}
            onClick={() => {
              if (it.off) return
              onClose()
              onPick(it.key)
            }}
            data-testid={`app-tool-${it.key}`}
          >
            {it.label}
            <span className="tb-mh">{it.off ? it.off : it.hint}</span>
          </button>
        </div>
      ))}
    </div>,
    layer(),
  )
}
