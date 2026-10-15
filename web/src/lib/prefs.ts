// Each person's own preferences, such as the columns each table shows,
// kept in this browser (localStorage), whether or not they are signed in
// (decided at the Phase 6 review). Every table and picker that uses one
// re-renders when it changes, here or in another tab of this browser.

import { useCallback, useSyncExternalStore } from 'react'
import { readJSON, writeJSON } from './storage'

let version = 0
const cache = new Map<string, unknown>()
const listeners = new Set<() => void>()

function changed() {
  version++
  for (const fn of listeners) fn()
}

function subscribe(fn: () => void) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

// Another tab of this browser changed one.
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (e) => {
    if (e.key && cache.has(e.key)) {
      cache.delete(e.key)
      changed()
    }
  })
}

/** A preference, or the fallback when none is kept. */
export function getPref<T>(key: string, fallback: T): T {
  // Read once and kept, so a value is the same object on every render.
  if (!cache.has(key)) cache.set(key, readJSON<unknown>(key, undefined))
  const v = cache.get(key)
  return v === undefined ? fallback : (v as T)
}

/** Keeps a preference in this browser. */
export function setPref(key: string, value: unknown): void {
  cache.set(key, value)
  writeJSON(key, value)
  changed()
}

/** A preference as React state: [value, set]. */
export function usePref<T>(key: string, fallback: T): [T, (v: T) => void] {
  useSyncExternalStore(subscribe, () => version, () => version)
  const value = getPref<T>(key, fallback)
  const set = useCallback((v: T) => setPref(key, v), [key])
  return [value, set]
}
