// Show costs (Phase 12, wave 6c, CF-40): a SuperAdmin's switch in
// Settings › AI. Off, no dollar amount shows anywhere for anyone (runs,
// jobs, spend, Sherlock's usage, agents, tests), except the budget fields
// where budgets are set; the budgets are still enforced. The console-wide
// settings the shell loads carry it (showCosts); this keeps the last value
// read, for every part of the page.
import { useSyncExternalStore } from 'react'

let shown = true
const listeners = new Set<() => void>()

/** Takes the value the settings carry; anything but false shows costs. */
export function setShowCosts(v: boolean | undefined | null): void {
  const next = v !== false
  if (next === shown) return
  shown = next
  for (const l of listeners) l()
}

/** Whether costs show now, outside React. */
export const showCosts = () => shown

function subscribe(l: () => void) {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

/** Whether costs show, read again when the setting changes. */
export function useShowCosts(): boolean {
  return useSyncExternalStore(subscribe, showCosts, showCosts)
}

/**
 * A text the server wrote, without its dollar amounts while costs are
 * hidden: "the day’s budget of $10.00 is spent" reads "the day’s budget is
 * spent". As it is when costs show.
 */
export function hideAmounts(text: string, shownNow = shown): string {
  if (shownNow) return text
  return text.replace(/(\s+(of|at|up to|over|about))?\s*(US)?\$\s?\d[\d,]*(\.\d+)?/g, '')
}
