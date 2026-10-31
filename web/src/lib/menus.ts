import { useEffect, useRef } from 'react'

// One pop-up menu at a time (asked October 30, 2026): a Tools button
// stops the mousedown that would close another menu, so two could stack.
// A menu announces itself as it opens, and every other open menu closes.
const EVENT = 'b221-menu-open'

export function useSoleMenu(open: boolean, onClose: () => void): void {
  const close = useRef(onClose)
  useEffect(() => {
    close.current = onClose
  }, [onClose])
  useEffect(() => {
    if (!open) return
    const token = Symbol('menu')
    window.dispatchEvent(new CustomEvent(EVENT, { detail: token }))
    const other = (e: Event) => {
      if ((e as CustomEvent).detail !== token) close.current()
    }
    window.addEventListener(EVENT, other)
    return () => window.removeEventListener(EVENT, other)
  }, [open])
}
