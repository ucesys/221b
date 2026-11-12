// Getting started (Phase 12, GS-01 to GS-06): the checklist of setting the
// console up, worked out by the server from the console as it is, for
// SuperAdmins. Only a dismissal is kept; nothing is ticked by hand.
import { useCallback, useEffect, useState } from 'react'
import { call, post } from './call'

export type StepState = 'done' | 'open' | 'dismissed'

export interface SetupStep {
  id: string
  n: number
  title: string
  state: StepState
  /** What the console has now, and what is missing. */
  detail: string
  /** Why the step matters and comes where it does. */
  why: string
  /** The screen where it is done, as a console link. */
  link: string
  where: string
  /** The field or button to use there. */
  action: string
  docs: string
  dismissedBy?: string
  dismissedAt?: string
}

export interface SetupView {
  steps: SetupStep[]
  done: number
  dismissed: number
  open: number
  next?: string
  complete: boolean
  kept: boolean
}

export const fetchSetup = (background = false) => call<SetupView>('/api/v1/setup', {}, background)

/** Dismisses steps, or brings them back; the answer is the checklist as it is now. */
export const dismissSteps = (steps: string[], dismiss: boolean) => call<SetupView>('/api/v1/setup/dismiss', post({ steps, dismiss }))

// Every open checklist follows a change made in another (the home page's
// card and Settings).
const listeners = new Set<(v: SetupView) => void>()

export function announceSetup(v: SetupView): void {
  for (const fn of listeners) fn(v)
}

/** "4 of 9 done", or "Done" when every step is done or dismissed. */
export function progressText(v: SetupView): string {
  return `${v.done} of ${v.steps.length} done`
}

/** The hint under Settings' section name. */
export function leftText(v: SetupView | null): string {
  if (!v) return 'What is left to set up'
  if (v.complete) return 'Every step done or dismissed'
  return `${v.open} step${v.open === 1 ? '' : 's'} left`
}

/** The last part of where a step is done: "Administration › Settings › Knowledge" → "Knowledge". */
export function shortWhere(where: string): string {
  const first = where.split(',')[0] ?? where
  const parts = first.split('›')
  return (parts[parts.length - 1] ?? first).trim()
}

/** The step to do next, if any. */
export function nextStep(v: SetupView): SetupStep | undefined {
  return v.steps.find((s) => s.id === v.next)
}

/**
 * The checklist for a SuperAdmin (enabled), read again every minute and
 * whenever another view changes it.
 */
export function useSetup(enabled: boolean): { view: SetupView | null; error: string; reload: () => void; set: (v: SetupView) => void } {
  const [view, setView] = useState<SetupView | null>(null)
  const [error, setError] = useState('')
  const reload = useCallback(() => {
    if (!enabled) return
    fetchSetup(true).then(
      (v) => {
        setView(v)
        setError('')
      },
      (e: unknown) => setError(e instanceof Error ? e.message : String(e)),
    )
  }, [enabled])
  useEffect(() => {
    if (!enabled) return
    reload()
    const t = window.setInterval(reload, 60_000)
    listeners.add(setView)
    return () => {
      window.clearInterval(t)
      listeners.delete(setView)
    }
  }, [enabled, reload])
  const set = useCallback((v: SetupView) => announceSetup(v), [])
  return { view, error, reload, set }
}
