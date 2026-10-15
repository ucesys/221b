// Each person's own preferences, such as the columns each table shows
// (asked at the Phase 4 review). Someone signed in keeps them with their
// account on the server, when the console is set up to (preferences.perUser
// in the chart), so they follow them to any browser; otherwise this browser
// keeps them.

import { useCallback, useSyncExternalStore } from 'react'
import { readJSON, writeJSON } from './storage'

type Where = 'browser' | 'account'

let where: Where = 'browser'
let values: Record<string, unknown> = {}
let version = 0
const browserCache = new Map<string, unknown>()
let timer: ReturnType<typeof setTimeout> | undefined
const listeners = new Set<() => void>()

function changed() {
  version++
  for (const fn of listeners) fn()
}

function subscribe(fn: () => void) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

/** Where preferences are kept now. */
export function prefsWhere(): Where {
  return where
}

/** A preference, or the fallback when none is kept. */
export function getPref<T>(key: string, fallback: T): T {
  if (where === 'account') return key in values ? (values[key] as T) : fallback
  // Read once and kept, so a value is the same object on every render.
  if (!browserCache.has(key)) browserCache.set(key, readJSON<unknown>(key, undefined))
  const v = browserCache.get(key)
  return v === undefined ? fallback : (v as T)
}

/** Keeps a preference: in this browser, or with the account a moment later. */
export function setPref(key: string, value: unknown): void {
  if (where === 'account') {
    values = { ...values, [key]: value }
    clearTimeout(timer)
    pending = true
    timer = setTimeout(() => void save(), 400)
  } else {
    browserCache.set(key, value)
    writeJSON(key, value)
  }
  changed()
}

let pending = false

async function save(): Promise<void> {
  clearTimeout(timer)
  pending = false
  try {
    // keepalive: a save started as the page goes away still arrives.
    await fetch('/api/v1/prefs', { method: 'PUT', keepalive: true, headers: { 'Content-Type': 'application/json', Accept: 'application/json' }, body: JSON.stringify({ prefs: values }) })
  } catch {
    // A preference is a convenience; the next change saves them all again.
  }
}

// A change made just before a reload or a closed tab is saved at once.
if (typeof window !== 'undefined') {
  window.addEventListener('pagehide', () => {
    if (pending) void save()
  })
}

/** The preferences this browser keeps, for the first sign-in with account preferences. */
function browserPrefs(): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  try {
    for (let i = 0; i < window.localStorage.length; i++) {
      const k = window.localStorage.key(i)
      if (k && k.startsWith('221b.') && k.endsWith('.hidden')) out[k] = readJSON<unknown>(k, null)
    }
  } catch {
    // No storage: nothing to bring along.
  }
  return out
}

/**
 * Asks the server whether this person's preferences are kept with their
 * account, and loads them. The first time, the columns this browser had
 * become the account's.
 */
export async function loadPrefs(): Promise<void> {
  let res: Response
  try {
    res = await fetch('/api/v1/prefs', { headers: { Accept: 'application/json' } })
  } catch {
    return
  }
  if (!res.ok) return
  const body = (await res.json().catch(() => null)) as { prefs?: Record<string, unknown>; store?: string } | null
  if (!body || body.store !== 'account') return
  where = 'account'
  values = body.prefs ?? {}
  if (Object.keys(values).length === 0) {
    const local = browserPrefs()
    if (Object.keys(local).length) {
      values = local
      void save()
    }
  }
  changed()
}

/** A preference as React state: [value, set]. */
export function usePref<T>(key: string, fallback: T): [T, (v: T) => void] {
  useSyncExternalStore(subscribe, () => version, () => version)
  const value = getPref<T>(key, fallback)
  const set = useCallback((v: T) => setPref(key, v), [key])
  return [value, set]
}

/** For tests: back to this browser's storage. */
export function resetPrefsForTests(): void {
  where = 'browser'
  values = {}
  browserCache.clear()
  changed()
}
