// Which console window Sherlock's UI actions change (P6U-08), and the
// glue between the window, the conversation's stream and lib/uiControl.
// A main window (one with tabs) runs the actions sent to it. Sherlock's
// own window runs none: it names the main window that opened it, which it
// follows, and asks that window for its view.

import { useEffect } from 'react'
import { postUIResult, setUIHandler, type AskContext, type Conversation } from '../api/sherlock'
import { onPop, post } from './sherlock'
import { canUndo, endAnswering, markUndone, runRequest, setAnswering, showChanges, undo, viewState, WINDOW_ID, type ShowChange, type UIRequest } from './uiControl'

declare global {
  interface Window {
    /** The id of a main console window, read by the Sherlock window it opened. */
    __221bWindow?: string
  }
}

let isMain = false
/** The cluster this main window shows, for a Sherlock window whose opener is gone. */
let mainCluster = ''

/** The main window Sherlock's own window follows: the one that opened it, while it is open. */
export function followedWindow(): string {
  try {
    const o = window.opener as Window | null
    return o && !o.closed && typeof o.__221bWindow === 'string' ? o.__221bWindow : ''
  } catch {
    return ''
  }
}

/**
 * Asks the main window this one drives for its id and view: the one that
 * opened it, or, with that one gone, the first main window on the cluster
 * that answers, as a link from here opens in one (P6U-08, SR-19).
 */
function mainView(cluster: string): Promise<{ window: string; view: string }> {
  const win = followedWindow()
  if (!win && !cluster) return Promise.resolve({ window: '', view: '' })
  const req = Math.random().toString(36).slice(2, 10)
  return new Promise((resolve) => {
    const t = setTimeout(() => {
      off()
      resolve({ window: win, view: '' })
    }, 600)
    const off = onPop((m) => {
      if (m.type === 'view' && m.req === req) {
        clearTimeout(t)
        off()
        resolve({ window: m.window, view: m.view })
      }
    })
    post(win ? { type: 'view?', req, window: win } : { type: 'view?', req, window: '', cluster })
  })
}

/** The window and view a question goes with (P6U-03, P6U-08); cluster finds a main window when the one that opened this is gone. */
export async function askContext(cluster = ''): Promise<AskContext> {
  if (isMain) return { window: WINDOW_ID, view: JSON.stringify(viewState()) }
  return mainView(cluster)
}

/** Asks this window, or the one holding the record, to undo an answer's view changes. */
export async function askUndo(cluster: string, answer: number): Promise<boolean> {
  if (canUndo(cluster, answer)) return undo(cluster, answer)
  return new Promise<boolean>((resolve) => {
    const t = setTimeout(() => {
      off()
      resolve(false)
    }, 1000)
    const off = onPop((m) => {
      if (m.type === 'undone' && m.cluster === cluster && m.answer === answer) {
        clearTimeout(t)
        off()
        resolve(true)
      }
    })
    post({ type: 'undo', cluster, answer })
  })
}

/**
 * Show in 221B (SR-15): applies the changes an answer left undone, here in
 * a main window, or in the main window Sherlock's own window follows.
 * Resolves with what could not be applied; null when no window answered.
 */
export async function askShow(changes: ShowChange[], force = false, cluster = ''): Promise<{ applied: number; failed: string[]; unsaved?: string[] } | null> {
  if (isMain) {
    const r = await showChanges(changes, force)
    return { applied: r.applied.length, failed: r.failed, ...(r.unsaved ? { unsaved: r.unsaved } : {}) }
  }
  const win = followedWindow() || (await mainView(cluster)).window
  if (!win) return null
  const req = Math.random().toString(36).slice(2, 10)
  return new Promise((resolve) => {
    const t = setTimeout(() => {
      off()
      resolve(null)
    }, 8000)
    const off = onPop((m) => {
      if (m.type === 'shown' && m.req === req) {
        clearTimeout(t)
        off()
        resolve({ applied: m.applied, failed: m.failed, ...(m.unsaved ? { unsaved: m.unsaved } : {}) })
      }
    })
    post({ type: 'show', req, window: win, changes, force })
  })
}

/** In a main window: runs the UI actions sent to it, and follows the answer in progress. */
export function useUIControl(cluster: string, conv: Conversation | null): void {
  useEffect(() => {
    isMain = true
    window.__221bWindow = WINDOW_ID
    setUIHandler((raw, from, reply) => {
      const req = raw as UIRequest
      if (!req || req.window !== WINDOW_ID) return
      void runRequest(req, from).then((res) => (reply ? reply(req.id, res) : postUIResult(from, req.id, res)))
    })
    const off = onPop((m) => {
      // Asked by name, or, by a Sherlock window whose opener is gone, as a main window on its cluster.
      if (m.type === 'view?' && (m.window === WINDOW_ID || (!m.window && !!m.cluster && m.cluster === mainCluster))) post({ type: 'view', req: m.req, window: WINDOW_ID, view: JSON.stringify(viewState()) })
      if (m.type === 'undo' && canUndo(m.cluster, m.answer))
        void undo(m.cluster, m.answer).then((ok) => {
          if (ok) post({ type: 'undone', cluster: m.cluster, answer: m.answer })
        })
      if (m.type === 'undone') markUndone(m.cluster, m.answer)
      if (m.type === 'show' && m.window === WINDOW_ID)
        void showChanges(m.changes, m.force).then((r) => {
          post({ type: 'shown', req: m.req, applied: r.applied.length, failed: r.failed, ...(r.unsaved ? { unsaved: r.unsaved } : {}) })
          if (r.applied.length) window.focus()
        })
    })
    return () => {
      isMain = false
      setUIHandler(null)
      off()
    }
  }, [])

  useEffect(() => {
    mainCluster = cluster
  }, [cluster])

  // Sherlock changes the view only while it answers (P6U-09): the answer
  // in progress is the last one while the conversation is busy.
  const last = conv?.messages[conv.messages.length - 1]
  const answering = conv?.busy && last?.role === 'sherlock' ? last.id : null
  useEffect(() => {
    setAnswering(cluster, answering)
  }, [answering, cluster])
}

/**
 * On a shared conversation in a main window (a case's or a proposed
 * change's, SR-19): while the answer to a question this person asked here
 * is given, it is the answer whose view changes this window takes, and its
 * end ends only that. scope names the conversation, as its ui events do.
 */
export function useSharedAnswering(scope: string, conv: Conversation | null, mine: ReadonlySet<number>): void {
  const last = conv?.messages[conv.messages.length - 1]
  const id = conv?.busy && last?.role === 'sherlock' && mine.has(last.id) ? last.id : null
  useEffect(() => {
    if (id === null || !isMain) return
    setAnswering(scope, id)
    return () => endAnswering(scope, id)
  }, [scope, id])
}
