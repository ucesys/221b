// Sherlock on a case (Phase 12, wave 6f, CF-47): one conversation per case,
// kept with the case on the server and shared by the people who work it,
// each question naming who asked. It is streamed as a person's own
// conversation is, on a stream of its own for the case.

import { useEffect, useState } from 'react'
import { checkSession } from './auth'
import { runSharedUI, sherlockCall, type AskContext, type Conversation, type SherlockMsg, type SherlockState, type SherlockStatus } from './sherlock'

const base = (id: string) => `/api/v1/aiops/cases/${encodeURIComponent(id.replace(/^C-/i, ''))}/sherlock`

/** A case's conversation's address, which also names it to the view actions (SR-19). */
export const caseSherlockBase = base

/** Asks on a case, with the asker's window and view: Sherlock's view changes go to that window only (SR-19). */
export const askCaseSherlock = (id: string, question: string, ctx?: AskContext) => sherlockCall<Conversation>('POST', `${base(id)}/ask`, { question, ...(ctx ? { window: ctx.window, view: ctx.view } : {}) })

/** Sends the asker's window's result of a view action back to a shared conversation's answer (SR-19). */
export async function postSharedUIResult(url: string, id: string, result: unknown): Promise<void> {
  try {
    await fetch(`${url}/ui/${encodeURIComponent(id)}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(result) })
  } catch {
    // The answer gives up after 5 seconds and says so.
  }
}
export const stopCaseSherlock = (id: string) => sherlockCall<{ stopped: boolean }>('POST', `${base(id)}/stop`, {})

/** Merges an answer that changed into the conversation. */
export function mergeAnswer(conv: Conversation | null, rev: number, message: SherlockMsg): Conversation | null {
  if (!conv) return conv
  const msgs = conv.messages.slice()
  const i = msgs.findIndex((m) => m.id === message.id)
  if (i >= 0) msgs[i] = message
  else msgs.push(message)
  return { ...conv, messages: msgs, rev }
}

/**
 * A case's conversation as it changes: off while on is false (the case
 * closed, or the Troubleshoot side not shown). The stream opens again after
 * an HTTP error, waiting longer each time, as the assistants' stream does.
 */
export function useCaseSherlock(id: string, on = true): SherlockState {
  return useSharedSherlock(base(id), on)
}

/**
 * A shared conversation as it changes, at its base URL: a case's, or a
 * proposed change's (CF-61). Off while on is false.
 */
export function useSharedSherlock(url: string, on = true): SherlockState {
  const id = url
  const [state, setState] = useState<SherlockState & { id: string }>({ id, status: null, conv: null, live: false })
  useEffect(() => {
    if (!on || typeof EventSource === 'undefined') return
    let es: EventSource | null = null
    let retry: ReturnType<typeof setTimeout> | undefined
    let failures = 0
    let gone = false
    const mine = (s: SherlockState & { id: string }) => (s.id === id ? s : { id, status: null, conv: null, live: false })
    const connect = () => {
      if (gone) return
      const src = new EventSource(`${url}/events`)
      es = src
      src.addEventListener('open', () => {
        failures = 0
        setState((s) => ({ ...mine(s), live: true }))
      })
      src.addEventListener('error', () => {
        if (es !== src) return
        setState((s) => ({ ...mine(s), live: false }))
        if (src.readyState === EventSource.CLOSED) {
          failures++
          checkSession()
          retry = setTimeout(connect, Math.min(30_000, 1000 * 2 ** Math.min(failures, 5)))
        }
      })
      src.addEventListener('status', (e) => {
        const status = JSON.parse((e as MessageEvent<string>).data) as SherlockStatus
        setState((s) => ({ ...mine(s), status }))
      })
      src.addEventListener('conversation', (e) => {
        const conv = JSON.parse((e as MessageEvent<string>).data) as Conversation
        setState((s) => ({ ...mine(s), conv, live: true }))
      })
      // Sherlock's view actions for the asker's window: the server sends
      // them on the asker's stream only, and the main window runs them (SR-19).
      src.addEventListener('ui', (e) => {
        runSharedUI(JSON.parse((e as MessageEvent<string>).data), url, (rid, res) => postSharedUIResult(url, rid, res))
      })
      src.addEventListener('answer', (e) => {
        const { rev, message } = JSON.parse((e as MessageEvent<string>).data) as { rev: number; message: SherlockMsg }
        setState((s) => {
          const cur = mine(s)
          return { ...cur, conv: mergeAnswer(cur.conv, rev, message) }
        })
      })
    }
    connect()
    return () => {
      gone = true
      clearTimeout(retry)
      es?.close()
    }
  }, [id, url, on])
  return state.id === id ? state : { status: null, conv: null, live: false }
}
