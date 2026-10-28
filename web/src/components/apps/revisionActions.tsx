import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import { layer } from '../../lib/layer'
import { DiffIcon, RollbackIcon, WrenchIcon } from '../Icons'

/**
 * What a revision row can ask for (Phase 10 review, Phase 10.1): the diff
 * of that revision, and a rollback to it; rollbackOff says why there is
 * no rollback here, current which revision runs now.
 */
export const RevisionActions = createContext<{ diff: (revision: number) => void; rollback?: (revision: number) => void; rollbackOff?: string; current?: number } | null>(null)

function place(anchor: HTMLElement | null, width = 320): CSSProperties {
  const r = anchor?.getBoundingClientRect()
  if (!r) return { top: 80, left: 80 }
  const left = Math.max(8, Math.min(window.innerWidth - width - 8, r.left - 8))
  const below = window.innerHeight - r.bottom - 8
  return below >= 140 ? { top: r.bottom + 4, left, maxHeight: below } : { bottom: window.innerHeight - r.top + 4, left, maxHeight: r.top - 8 }
}

/** The wrench in a revision row: a menu with Diff… and Roll back to this revision… (asked at the Phase 10.1 review). */
export function RevisionTools({ revision }: { revision: number }) {
  const ctx = useContext(RevisionActions)
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
  if (!ctx) return null
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
  const current = revision === ctx.current
  const items = [
    { key: 'diff', icon: <DiffIcon />, label: 'Diff…', hint: revision > 1 ? `against revision ${revision - 1}` : 'the first revision', off: undefined as string | undefined, run: () => ctx.diff(revision) },
    {
      key: 'rollback',
      icon: <RollbackIcon />,
      label: 'Roll back to this revision…',
      hint: 'helm rollback, a Watson card to approve',
      off: current ? 'this revision runs now' : ctx.rollback ? undefined : (ctx.rollbackOff ?? 'not here'),
      run: () => ctx.rollback?.(revision),
    },
  ]
  return (
    <>
      <button
        className="tb-tools"
        style={{ opacity: 1, marginLeft: 0 }}
        aria-label={`Tools for revision ${revision}`}
        title={`Tools for revision ${revision}: diff, roll back`}
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
        data-testid={`revision-tools-${revision}`}
      >
        <WrenchIcon />
      </button>
      {open &&
        createPortal(
          <div ref={menu} className="tb-menu" role="menu" aria-label={`Tools for revision ${revision}`} style={style} onKeyDown={keys} onClick={(e) => e.stopPropagation()} onMouseDown={(e) => e.stopPropagation()} data-testid="revision-menu">
            <div className="tb-mhead">
              Tools for revision <span className="rk-mono">{revision}</span>
              {current ? ' · runs now' : ''}
            </div>
            {items.map((it) => (
              <button
                key={it.key}
                role="menuitem"
                className="tb-mi"
                aria-disabled={it.off ? true : undefined}
                title={it.off ? `${it.label.replace('…', '')}: ${it.off}` : undefined}
                onClick={() => {
                  if (it.off) return
                  onClose()
                  it.run()
                }}
                data-testid={`revision-tool-${it.key}`}
              >
                {it.icon}
                {it.label}
                <span className="tb-mh">{it.off ? it.off : it.hint}</span>
              </button>
            ))}
          </div>,
          layer(),
        )}
    </>
  )
}
