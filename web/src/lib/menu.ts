import type { CSSProperties } from 'react'

/**
 * Where a toolbar menu goes: fixed to the window, so a short table's card
 * does not clip it, right-aligned with its button, below it or above it
 * when there is more room there.
 */
export function menuPlace(button: HTMLElement | null): CSSProperties {
  const r = button?.getBoundingClientRect()
  if (!r) return {}
  const below = window.innerHeight - r.bottom - 12
  const above = r.top - 12
  const right = Math.max(8, window.innerWidth - r.right)
  return below >= 280 || below >= above
    ? { position: 'fixed', top: r.bottom + 4, right, left: 'auto', bottom: 'auto', maxHeight: below, overflowY: 'auto' }
    : { position: 'fixed', bottom: window.innerHeight - r.top + 4, right, left: 'auto', top: 'auto', maxHeight: above, overflowY: 'auto' }
}
