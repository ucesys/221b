import { createContext, useContext } from 'react'

/** What a page or a drawer can ask of Sherlock. */
export interface SherlockApi {
  /** Whether questions can be asked in this cluster now. */
  available: boolean
  /** Asks; over says the asker is over the page (a drawer), so the answer shows maximized, above it. */
  ask: (question: string, over?: boolean) => void
}

export const SherlockContext = createContext<SherlockApi>({ available: false, ask: () => undefined })

export function useSherlockApi(): SherlockApi {
  return useContext(SherlockContext)
}
