import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { CloseIcon } from './Icons'

export interface DrawerTab {
  id: string
  label: string
  count?: number | string
}

interface Props {
  /** The aside's accessible name: "Pod details". */
  label: string
  /** The small line above the title: "Pod in ‹namespace›". */
  kicker: ReactNode
  title: string
  /** The facts line under the title. */
  facts?: ReactNode
  tabs?: DrawerTab[]
  tab?: string
  onTab?: (id: string) => void
  onClose: () => void
  /** Shown above the body: a notice such as "this pod was deleted". */
  notice?: ReactNode
  children: ReactNode
  width?: number
  testId?: string
}

/**
 * The prototype's details drawer: over the page from the right, with a
 * backdrop that closes it, a header with tabs, and a scrolling body.
 */
export function Drawer({ label, kicker, title, facts, tabs, tab, onTab, onClose, notice, children, width = 1000, testId }: Props) {
  const closeRef = useRef<HTMLButtonElement>(null)
  const body = useRef<HTMLDivElement>(null)
  useEffect(() => {
    closeRef.current?.focus()
  }, [title])
  useEffect(() => {
    body.current?.scrollTo(0, 0)
  }, [tab])
  // While the body scrolls it is a tab stop and a named region, so the
  // keyboard can reach what scrolls (WCAG 2.1.1), as ScrollBox does. The
  // content changes with every tab and fetch, so it is measured after each
  // render and when the drawer is resized.
  const [scrolls, setScrolls] = useState(false)
  const observer = useRef<ResizeObserver | null>(null)
  useEffect(() => {
    const el = body.current
    if (!el || typeof ResizeObserver === 'undefined') return
    observer.current = new ResizeObserver(() => setScrolls(el.scrollHeight > el.clientHeight + 1))
    return () => observer.current?.disconnect()
  }, [])
  useLayoutEffect(() => {
    const el = body.current
    if (!el) return
    setScrolls(el.scrollHeight > el.clientHeight + 1)
    // The sections grow as their own fetches land, so each is watched too.
    const ro = observer.current
    if (ro) {
      ro.disconnect()
      ro.observe(el)
      for (const child of el.children) ro.observe(child)
    }
  }, [tab, children])
  return (
    <div style={{ position: 'fixed', top: 0, right: 0, bottom: 0, left: 0, zIndex: 40, display: 'flex', justifyContent: 'flex-end' }}>
      <button aria-label={`Close ${label.toLowerCase()}`} tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <aside
        className="rk-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={label}
        data-testid={testId}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation()
            onClose()
          }
        }}
        style={{ position: 'relative', width: `min(${width}px, 100%)`, height: '100%', display: 'flex', flexDirection: 'column', background: 'var(--surface)', borderLeft: '1px solid var(--line)', boxShadow: 'var(--shadow)' }}
      >
        <div style={{ flex: 'none', padding: '16px 20px 0', display: 'flex', flexDirection: 'column', gap: 10, borderBottom: '1px solid var(--line)' }}>
          <div style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
            <div style={{ flex: '1 1 auto', minWidth: 0, display: 'flex', flexDirection: 'column', gap: 4 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 11.5, color: 'var(--fg3)' }}>{kicker}</div>
              <h2 className="rk-mono" style={{ margin: 0, fontSize: 17, fontWeight: 600, lineHeight: 1.3, overflowWrap: 'anywhere' }}>
                {title}
              </h2>
            </div>
            <button ref={closeRef} className="rk-icon-btn" aria-label={`Close ${label.toLowerCase()}`} title="Close (Esc)" onClick={onClose}>
              <CloseIcon />
            </button>
          </div>
          {facts && <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 16px', fontSize: 12, color: 'var(--fg3)' }}>{facts}</div>}
          {tabs && tabs.length > 0 && (
            <nav aria-label={`${label} sections`} style={{ display: 'flex', flexWrap: 'wrap', gap: '0 20px' }}>
              {tabs.map((t) => (
                <button key={t.id} className="rk-tab" aria-current={tab === t.id ? 'page' : undefined} onClick={() => onTab?.(t.id)} style={{ height: 38 }} data-dtab={t.id}>
                  {t.label}
                  {t.count !== undefined && t.count !== 0 && <span className="rk-count">{t.count}</span>}
                </button>
              ))}
            </nav>
          )}
          {!tabs?.length && <div style={{ height: 6 }} />}
        </div>
        {notice}
        <div ref={body} className="rk-drawer-body" tabIndex={scrolls ? 0 : undefined} role={scrolls ? 'region' : undefined} aria-label={scrolls ? `${label} content` : undefined} style={{ flex: '1 1 auto', minHeight: 0, overflow: 'auto', padding: '18px 20px 32px', display: 'flex', flexDirection: 'column', gap: 16, background: 'var(--bg)' }}>
          {children}
        </div>
      </aside>
    </div>
  )
}

/** A card with a small heading, as the drawers lay their sections out. */
export function Card({ title, aside, children, pad = true, label }: { title?: string; aside?: ReactNode; children: ReactNode; pad?: boolean; label?: string }) {
  return (
    <section className="rk-card" aria-label={label ?? title} style={{ padding: pad ? '14px 16px' : 0, display: 'flex', flexDirection: 'column', gap: 12, minWidth: 0, overflow: pad ? undefined : 'hidden' }}>
      {title && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: pad ? 0 : '12px 16px 0' }}>
          <h3 className="rk-h">{title}</h3>
          {aside && <span style={{ flex: '1 1 auto' }} />}
          {aside}
        </div>
      )}
      {children}
    </section>
  )
}

/** Key–value facts, as the prototype's dl.rk-kv. */
export function Facts({ items }: { items: [string, ReactNode, string?][] }) {
  return (
    <dl className="rk-kv">
      {items.map(([k, v, tip]) => (
        <div key={k} style={{ display: 'contents' }}>
          <dt>{k}</dt>
          <dd className="rk-mono" title={tip} style={{ whiteSpace: 'pre-line' }}>
            {v}
          </dd>
        </div>
      ))}
    </dl>
  )
}

/** Labels or annotations as chips. */
export function Chips({ map, empty = 'None' }: { map: Record<string, string>; empty?: string }) {
  const keys = Object.keys(map).sort()
  if (!keys.length) return <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>{empty}</p>
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
      {keys.map((k) => (
        <span key={k} className="rk-chip">
          {k}
          <em>{map[k] ? '=' + map[k] : ''}</em>
        </span>
      ))}
    </div>
  )
}
