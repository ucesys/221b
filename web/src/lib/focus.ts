// Keeping the focus inside a modal: a dialog that covers the page must not
// let Tab wander into what is behind it, where Escape would no longer close
// it and a screen reader would read the wrong thing.

import type { KeyboardEvent } from 'react'

/** What Tab stops on, in document order. */
export const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

/**
 * The focusable elements inside root, in document order, leaving out the
 * ones not shown. A browser says what is shown by whether an element has a
 * box; a test's document lays nothing out, so there every element counts.
 */
export function focusables(root: HTMLElement): HTMLElement[] {
  const all = [...root.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((el) => !el.closest('[hidden], [aria-hidden="true"]'))
  const shown = all.filter((el) => el.getClientRects().length > 0)
  return shown.length > 0 ? shown : all
}

/**
 * Keeps Tab inside root: from the last element it goes to the first, and
 * Shift-Tab from the first to the last. Call it from the root's keydown
 * handler; it does nothing for other keys.
 */
export function trapTab(e: KeyboardEvent<HTMLElement>, root: HTMLElement = e.currentTarget): void {
  if (e.key !== 'Tab') return
  const all = focusables(root)
  const first = all[0]
  const last = all[all.length - 1]
  if (!first || !last) return
  const active = document.activeElement
  if (e.shiftKey && (active === first || !root.contains(active))) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && (active === last || !root.contains(active))) {
    e.preventDefault()
    first.focus()
  }
}
