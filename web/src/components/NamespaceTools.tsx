import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import { layer } from '../lib/layer'
import { WrenchIcon } from './Icons'
import { useWatsonApi } from './Watson'

/** What the namespace's wrench offers (NS-01, NS-02). */
export type NamespaceAction = 'new' | 'delete'

function place(anchor: HTMLElement | null, width = 300): CSSProperties {
  const r = anchor?.getBoundingClientRect()
  if (!r) return { top: 80, left: 80 }
  const left = Math.max(8, Math.min(window.innerWidth - width - 8, r.left - 8))
  const below = window.innerHeight - r.bottom - 8
  return below >= 160 ? { top: r.bottom + 4, left, maxHeight: below } : { bottom: window.innerHeight - r.top + 4, left, maxHeight: r.top - 8 }
}

/** The wrench beside a namespace's name: New namespace…, and Delete namespace… through Watson. */
export function NamespaceTools({ namespace, canCreate, onPick }: { namespace: string; canCreate: boolean; onPick: (a: NamespaceAction) => void }) {
  const watson = useWatsonApi()
  const [anchor, setAnchor] = useState<HTMLElement | null>(null)
  const menu = useRef<HTMLDivElement>(null)
  const [style, setStyle] = useState<CSSProperties>({})
  const open = anchor !== null
  const onClose = () => setAnchor(null)
  useEffect(() => {
    if (!open) return
    const at = anchor?.getBoundingClientRect()
    const away = (e: MouseEvent) => {
      if (menu.current && !menu.current.contains(e.target as Node) && !anchor?.contains(e.target as Node)) onClose()
    }
    const scroll = (e: Event) => {
      if (!menu.current || menu.current.contains(e.target as Node)) return
      const now = anchor?.getBoundingClientRect()
      if (!at || !now || Math.abs(now.top - at.top) > 2 || Math.abs(now.left - at.left) > 2) onClose()
    }
    document.addEventListener('mousedown', away)
    window.addEventListener('scroll', scroll, true)
    window.addEventListener('resize', onClose)
    return () => {
      document.removeEventListener('mousedown', away)
      window.removeEventListener('scroll', scroll, true)
      window.removeEventListener('resize', onClose)
    }
  }, [open, anchor])
  useLayoutEffect(() => {
    if (open) menu.current?.querySelector<HTMLElement>('[role=menuitem]:not([aria-disabled=true])')?.focus()
  }, [open])
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
  const deleteOff = !watson.available ? (watson.readOnly ? 'this cluster is connected read-only' : 'Watson is off here') : undefined
  const items: { key: NamespaceAction; label: string; hint?: string; off?: string; danger?: boolean; sepBefore?: boolean }[] = [
    { key: 'new', label: 'New namespace…', off: canCreate ? undefined : 'for Admins and SuperAdmins' },
    { key: 'delete', label: 'Delete namespace…', hint: 'everything in it goes', danger: true, sepBefore: true, off: deleteOff },
  ]
  return (
    <>
      <button
        className="tb-tools"
        style={{ opacity: 1, marginLeft: 0 }}
        aria-label={`Tools for namespace ${namespace}`}
        title={`Tools for namespace ${namespace}`}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={(e) => {
          e.stopPropagation()
          e.preventDefault()
          const el = e.currentTarget
          setStyle(place(el))
          setAnchor((a) => (a ? null : el))
        }}
        onMouseDown={(e) => e.stopPropagation()}
        data-testid={`tools-namespace-${namespace}`}
      >
        <WrenchIcon />
      </button>
      {open &&
        createPortal(
          <div ref={menu} className="tb-menu" role="menu" aria-label={`Tools for namespace ${namespace}`} style={style} onKeyDown={keys} onClick={(e) => e.stopPropagation()} onMouseDown={(e) => e.stopPropagation()} data-testid="namespace-menu">
            <div className="tb-mhead">
              Tools for namespace <span className="rk-mono">{namespace}</span>
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
                  data-testid={`namespace-tool-${it.key}`}
                >
                  {it.label}
                  <span className="tb-mh">{it.off ? it.off : it.hint}</span>
                </button>
              </div>
            ))}
          </div>,
          layer(),
        )}
    </>
  )
}
