// The Save of the Settings page open, for the prompt on leaving it with
// changes not saved (CU-16): the page offers it while it has changes, and
// the prompt saves through it before leaving.

import { createContext, useContext, useEffect } from 'react'

/** The page's Save while it has changes: true when all of it was saved. */
export type LeaveSave = { current: (() => Promise<boolean>) | null }

export const LeaveSaveContext = createContext<LeaveSave | null>(null)

/** Offers the page's Save to the prompt on leaving it; null while it has no changes. */
export function useLeaveSave(save: (() => Promise<boolean>) | null): void {
  const ref = useContext(LeaveSaveContext)
  useEffect(() => {
    if (!ref) return
    ref.current = save
    return () => {
      if (ref.current === save) ref.current = null
    }
  })
}
