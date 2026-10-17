import { useEffect, useState } from 'react'

/**
 * One tooltip for the whole page: any element with data-tip shows it at
 * once on hover or keyboard focus, above the element (below near the top
 * edge). It is fixed to the window, so a table's scrolling or clipping
 * does not cut it off. Native titles are slow and easy to miss on bars.
 */
export function TipLayer() {
  const [tip, setTip] = useState<{ text: string; x: number; y: number; below: boolean } | null>(null)
  useEffect(() => {
    const show = (e: Event) => {
      const el = (e.target as Element | null)?.closest?.('[data-tip]') as HTMLElement | null
      if (!el) return
      const text = el.dataset.tip
      if (!text) return
      const r = el.getBoundingClientRect()
      const below = r.top < 90
      setTip({ text, x: Math.min(Math.max(r.left + r.width / 2, 160), window.innerWidth - 160), y: below ? r.bottom + 8 : r.top - 8, below })
    }
    const hide = (e: Event) => {
      const el = (e.target as Element | null)?.closest?.('[data-tip]')
      const to = (e as MouseEvent).relatedTarget as Element | null
      if (el && to && el.contains(to)) return
      setTip(null)
    }
    document.addEventListener('mouseover', show)
    document.addEventListener('mouseout', hide)
    document.addEventListener('focusin', show)
    document.addEventListener('focusout', hide)
    const gone = () => setTip(null)
    window.addEventListener('scroll', gone, true)
    return () => {
      window.removeEventListener('scroll', gone, true)
      document.removeEventListener('mouseover', show)
      document.removeEventListener('mouseout', hide)
      document.removeEventListener('focusin', show)
      document.removeEventListener('focusout', hide)
    }
  }, [])
  if (!tip) return null
  return (
    <div role="tooltip" className="rk-tiplayer" data-testid="tip" style={{ left: tip.x, top: tip.y, transform: tip.below ? 'translateX(-50%)' : 'translate(-50%, -100%)' }}>
      {tip.text}
    </div>
  )
}
