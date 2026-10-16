// Sherlock (Phase 5): a conversation per person and cluster, kept on the
// server, streamed to every window of the session (P5-08).

import { useEffect, useState } from 'react'
import { unauthorized } from './auth'
import { subscribeAssistant } from './assistants'
import { apiPath } from '../lib/router'

export interface SherlockRef {
  kind: string
  namespace?: string
  name: string
}

export interface SherlockStep {
  label: string
  tool: string
  status: 'running' | 'done' | 'failed'
}

export interface SherlockUsage {
  input_tokens: number
  output_tokens: number
  cache_creation_input_tokens: number
  cache_read_input_tokens: number
}

export interface SherlockMsg {
  id: number
  role: 'user' | 'sherlock'
  text: string
  steps?: SherlockStep[]
  refs?: SherlockRef[]
  status?: 'thinking' | 'answering' | 'done' | 'stopped' | 'failed'
  error?: string
  at: string
  firstAt?: string
  doneAt?: string
  model?: string
  usage?: SherlockUsage
  costUSD?: number
  /** What the answer changed in the asking window's view (P6U-10). */
  view?: { label: string }[]
}

export interface Conversation {
  id: string
  messages: SherlockMsg[]
  busy: boolean
  rev: number
}

export interface SherlockStatus {
  available: boolean
  enabled: boolean
  key: boolean
  reason?: string
  capped?: string
  model: string
  admin: boolean
}

export interface SherlockModel {
  id: string
  label: string
  in: number
  out: number
  longIn?: number
  longOut?: number
  longAbove?: number
  note?: string
}

export interface SherlockSettings {
  clusters?: Record<string, boolean>
  model: string
  dailyUSD: number
  monthlyUSD: number
  /** Whether Sherlock may change the asking person's view (P6U-14); absent is on. */
  viewControl?: boolean
}

export interface SherlockConfig {
  settings: SherlockSettings
  key: { set: boolean; source?: 'console' | 'chart'; setAt?: string; setBy?: string; store: 'secret' | 'memory' }
  spend: { day: string; dayUSD: number; dayAsked: number; month: string; monthUSD: number; monthAsked: number }
  models: SherlockModel[]
  clusters: { id: string; name: string; env: string }[]
  running: number
}

export class SherlockError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message)
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers: body === undefined ? { Accept: 'application/json' } : { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
    throw new SherlockError('The console did not answer. Check the connection and try again.', 0)
  }
  if (unauthorized(res)) throw new SherlockError('Your session has ended; sign in again.', 401)
  const data = (await res.json().catch(() => null)) as (T & { error?: string }) | null
  if (!res.ok) throw new SherlockError(data?.error || `The console answered ${res.status}.`, res.status)
  return data as T
}

/** The console window whose view Sherlock may change, and that view (P6U-03, P6U-08). */
export interface AskContext {
  window: string
  view: string
}

export const askSherlock = (cluster: string, question: string, ctx?: AskContext) =>
  call<Conversation>('POST', apiPath('sherlock/ask', cluster), { question, ...(ctx ? { window: ctx.window, view: ctx.view } : {}) })

/** Sends a window's result of a UI action back to the answer waiting for it (P6U-17). */
export async function postUIResult(cluster: string, id: string, result: unknown): Promise<void> {
  try {
    await fetch(apiPath('sherlock/ui/' + encodeURIComponent(id), cluster), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(result) })
  } catch {
    // The answer gives up after 5 seconds and says so.
  }
}

/** Where a main console window runs the UI actions sent to it. */
let uiHandler: ((req: unknown, cluster: string) => void) | null = null

export function setUIHandler(fn: ((req: unknown, cluster: string) => void) | null): void {
  uiHandler = fn
}
export const stopSherlock = (cluster: string) => call<{ stopped: boolean }>('POST', apiPath('sherlock/stop', cluster), {})
export const newSherlockChat = (cluster: string) => call<Conversation>('POST', apiPath('sherlock/new', cluster), {})
export const fetchSherlockConfig = () => call<SherlockConfig>('GET', '/api/v1/sherlock/config')
export const saveSherlockConfig = (s: SherlockSettings) => call<SherlockConfig>('PUT', '/api/v1/sherlock/config', s)
export const saveSherlockKey = (key: string) => call<SherlockConfig>('PUT', '/api/v1/sherlock/key', { key })
export const deleteSherlockKey = () => call<SherlockConfig>('DELETE', '/api/v1/sherlock/key')

export interface SherlockState {
  status: SherlockStatus | null
  conv: Conversation | null
  /** Whether the stream is open. */
  live: boolean
}

/** Sherlock in a cluster: its status and the conversation, as they change. */
export function useSherlock(cluster: string, on = true): SherlockState {
  // State is kept with the cluster it is for, so another cluster starts empty.
  const [state, setState] = useState<SherlockState & { cluster: string }>({ cluster, status: null, conv: null, live: false })
  useEffect(() => {
    if (!on || typeof EventSource === 'undefined') return
    const mine = (s: SherlockState & { cluster: string }) => (s.cluster === cluster ? s : { cluster, status: null, conv: null, live: false })
    return subscribeAssistant(cluster, 'sherlock', {
      open: () => setState((s) => ({ ...mine(s), live: true })),
      error: () => setState((s) => ({ ...mine(s), live: false })),
      event: (name, data) => {
        if (name === 'status') {
          const status = JSON.parse(data) as SherlockStatus
          setState((s) => ({ ...mine(s), status }))
        } else if (name === 'conversation') {
          const conv = JSON.parse(data) as Conversation
          setState((s) => ({ ...mine(s), conv, live: true }))
        } else if (name === 'ui') {
          uiHandler?.(JSON.parse(data), cluster)
        } else if (name === 'answer') {
          const { rev, message } = JSON.parse(data) as { rev: number; message: SherlockMsg }
          setState((s) => {
            const cur = mine(s)
            if (!cur.conv) return cur
            const msgs = cur.conv.messages.slice()
            const i = msgs.findIndex((m) => m.id === message.id)
            if (i >= 0) msgs[i] = message
            else msgs.push(message)
            return { ...cur, conv: { ...cur.conv, messages: msgs, rev } }
          })
        }
      },
    })
  }, [cluster, on])
  return state.cluster === cluster ? state : { status: null, conv: null, live: false }
}

/** The price of a model, for the settings: "$4 in · $20 out per million tokens". */
export function priceText(m: SherlockModel): string {
  const f = (n: number) => (n >= 1 ? `$${n}` : `$${n.toFixed(2)}`)
  return `${f(m.in)} in · ${f(m.out)} out per million tokens`
}

/** Dollars as the settings show them. */
export function usd(n: number): string {
  return n < 0.01 && n > 0 ? '<$0.01' : `$${n.toFixed(2)}`
}
