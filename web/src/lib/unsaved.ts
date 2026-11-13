// Unsaved changes in this console window (SR-14): a form, drawer or
// section that holds edits not yet saved says so here while it does, and
// Sherlock leaves the view as it is instead of moving the person away
// from them. Nothing here blocks the person's own navigation.

import { useEffect } from 'react'

const holders = new Map<symbol, string>()
const listeners = new Set<() => void>()

function changed() {
  for (const fn of listeners) fn()
}

/** Says, while changed is true, that what is open holds unsaved edits; what names it, for the notice. */
export function useUnsaved(changed: boolean, what = 'a form'): void {
  useEffect(() => {
    if (!changed) return
    return holdUnsaved(what)
  }, [changed, what])
}

/** The same as a component, for a form that computes its changes after an early return. */
export function Unsaved({ when, what }: { when: boolean; what?: string }): null {
  useUnsaved(when, what)
  return null
}

/** Marks unsaved edits until the returned function is called: for code outside React and tests. */
export function holdUnsaved(what = 'a form'): () => void {
  const k = Symbol(what)
  holders.set(k, what)
  changed()
  return () => {
    if (holders.delete(k)) changed()
  }
}

/** Whether anything open in this window holds unsaved edits. */
export function hasUnsaved(): boolean {
  return holders.size > 0
}

/** What holds them, for the result Sherlock reads: "the agent drawer". */
export function unsavedWhat(): string[] {
  return [...new Set(holders.values())]
}

export function onUnsaved(fn: () => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}
