// Which console window Sherlock's UI actions change (P6U-08), and the
// glue between the window, the conversation's stream and lib/uiControl.
// A main window (one with tabs) runs the actions sent to it. Sherlock's
// own window runs none: it names the main window that opened it, which it
// follows, and asks that window for its view.

import { useEffect } from 'react'
import { postUIResult, setUIHandler, type AskContext, type Conversation } from '../api/sherlock'
import { onPop, post } from './sherlock'
import { canUndo, markUndone, runRequest, setAnswering, undo, viewState, WINDOW_ID, type UIRequest } from './uiControl'

declare global {
  interface Window {
    /** The id of a main console window, read by the Sherlock window it opened. */
    __221bWindow?: string
  }
}

let isMain = false

/** The main window Sherlock's own window follows: the one that opened it, while it is open. */
function followedWindow(): string {
  try {
    const o = window.opener as Window | null
    return o && !o.closed && typeof o.__221bWindow === 'string' ? o.__221bWindow : ''
  } catch {
    return ''
  }
}

/** The window and view a question goes with (P6U-03, P6U-08). */
export async function askContext(): Promise<AskContext> {
  if (isMain) return { window: WINDOW_ID, view: JSON.stringify(viewState()) }
  const win = followedWindow()
  if (!win) return { window: '', view: '' }
  const req = Math.random().toString(36).slice(2, 10)
  const view = await new Promise<string>((resolve) => {
    const t = setTimeout(() => {
      off()
      resolve('')
    }, 600)
    const off = onPop((m) => {
      if (m.type === 'view' && m.req === req) {
        clearTimeout(t)
        off()
        resolve(m.view)
      }
    })
    post({ type: 'view?', req, window: win })
  })
  return { window: win, view }
}

/** Asks this window, or the one holding the record, to undo an answer's view changes. */
export async function askUndo(answer: number): Promise<boolean> {
  if (canUndo(answer)) return undo(answer)
  return new Promise<boolean>((resolve) => {
    const t = setTimeout(() => {
      off()
      resolve(false)
    }, 1000)
    const off = onPop((m) => {
      if (m.type === 'undone' && m.answer === answer) {
        clearTimeout(t)
        off()
        resolve(true)
      }
    })
    post({ type: 'undo', answer })
  })
}

/** In a main window: runs the UI actions sent to it, and follows the answer in progress. */
export function useUIControl(cluster: string, conv: Conversation | null): void {
  useEffect(() => {
    isMain = true
    window.__221bWindow = WINDOW_ID
    setUIHandler((raw, from) => {
      const req = raw as UIRequest
      if (!req || req.window !== WINDOW_ID) return
      void runRequest(req).then((res) => postUIResult(from, req.id, res))
    })
    const off = onPop((m) => {
      if (m.type === 'view?' && m.window === WINDOW_ID) post({ type: 'view', req: m.req, window: WINDOW_ID, view: JSON.stringify(viewState()) })
      if (m.type === 'undo' && canUndo(m.answer))
        void undo(m.answer).then((ok) => {
          if (ok) post({ type: 'undone', answer: m.answer })
        })
      if (m.type === 'undone') markUndone(m.answer)
    })
    return () => {
      isMain = false
      setUIHandler(null)
      off()
    }
  }, [])

  // Sherlock changes the view only while it answers (P6U-09): the answer
  // in progress is the last one while the conversation is busy.
  const last = conv?.messages[conv.messages.length - 1]
  const answering = conv?.busy && last?.role === 'sherlock' ? last.id : null
  useEffect(() => {
    setAnswering(answering)
  }, [answering, cluster])
}
