// Watson (Phase 6): the commands of a cluster as one person sees them,
// kept on the server and streamed to every window of the session (P6-09).

import { useEffect, useState } from 'react'
import { unauthorized } from './auth'
import { subscribeAssistant } from './assistants'
import { apiPath } from '../lib/router'

export interface WatsonLine {
  /** out, err, or info (Watson's own notes). */
  k: 'out' | 'err' | 'info'
  t: string
}

export interface WatsonPlan {
  ready: boolean
  targets?: string[]
  impact?: string[]
  dryRun?: WatsonLine[]
  dryRunExit: number
  dryRunNote?: string
  dryRunning?: boolean
}

export type WatsonStatusName = 'pending' | 'queued' | 'running' | 'done' | 'failed' | 'stopped' | 'lapsed' | 'discarded' | 'refused'

export interface WatsonItem {
  id: string
  command: string
  kind: 'read' | 'change'
  from: string
  why?: string
  status: WatsonStatusName
  by: string
  mine: boolean
  created: string
  expires?: string
  plan?: WatsonPlan
  approver?: string
  started?: string
  finished?: string
  exit: number
  reason?: string
  rule?: string
  canApprove: boolean
  waiting?: string
  lines?: WatsonLine[]
  lineStart: number
  rev: number
}

export interface WatsonStatus {
  available: boolean
  readOnly: boolean
  how?: string
  twoPerson: boolean
  env: string
  signedIn: boolean
  me: string
  prompt: string
  lapseSeconds: number
  reads: string[]
  changes: string[]
  admin: boolean
}

export interface WatsonSettings {
  twoPerson: boolean
}

export class WatsonError extends Error {
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
    throw new WatsonError('The console did not answer. Check the connection and try again.', 0)
  }
  if (unauthorized(res)) throw new WatsonError('Your session has ended; sign in again.', 401)
  if (res.status === 204) return undefined as T
  const data = (await res.json().catch(() => null)) as (T & { error?: string }) | null
  if (!res.ok) throw new WatsonError(data?.error || `The console answered ${res.status}.`, res.status)
  return data as T
}

export interface Send {
  /** Where it came from: a button, Sherlock; "Typed" when empty. */
  from?: string
  /** A note for the card, such as Sherlock's reason. */
  why?: string
  /** Runs reads at once; changes always wait for approval. */
  run?: boolean
  /** The sender is over the page (a drawer): the panel shows maximized, above it. */
  over?: boolean
}

export const sendToWatson = (cluster: string, command: string, o: Send = {}) => call<WatsonItem[]>('POST', apiPath('watson/commands', cluster), { command, from: o.from ?? '', why: o.why ?? '', run: !!o.run })
export const approveWatson = (cluster: string, id: string) => call<WatsonItem>('POST', apiPath(`watson/commands/${encodeURIComponent(id)}/approve`, cluster), {})
export const discardWatson = (cluster: string, id: string) => call<WatsonItem>('POST', apiPath(`watson/commands/${encodeURIComponent(id)}/discard`, cluster), {})
export const stopWatson = (cluster: string, id: string) => call<WatsonItem>('POST', apiPath(`watson/commands/${encodeURIComponent(id)}/stop`, cluster), {})
export const editWatson = (cluster: string, id: string, command: string) => call<WatsonItem[]>('PUT', apiPath(`watson/commands/${encodeURIComponent(id)}`, cluster), { command })
export const clearWatson = (cluster: string) => call<void>('POST', apiPath('watson/clear', cluster), {})
export const fetchWatsonConfig = () => call<WatsonSettings>('GET', '/api/v1/watson/config')
export const saveWatsonConfig = (s: WatsonSettings) => call<WatsonSettings>('PUT', '/api/v1/watson/config', s)

export interface WatsonState {
  status: WatsonStatus | null
  items: WatsonItem[]
  live: boolean
}

/** Merges an item's update: its new lines follow the ones already here. */
export function mergeItem(items: WatsonItem[], next: WatsonItem): WatsonItem[] {
  const i = items.findIndex((x) => x.id === next.id)
  if (i < 0) return [...items, next].sort((a, b) => a.created.localeCompare(b.created))
  const prev = items[i]!
  const lines = (prev.lines ?? []).slice(0, next.lineStart).concat(next.lines ?? [])
  const out = items.slice()
  out[i] = { ...next, lines }
  return out
}

/** Watson in a cluster: its status and commands, as they change. */
export function useWatson(cluster: string, on = true): WatsonState {
  const [state, setState] = useState<WatsonState & { cluster: string }>({ cluster, status: null, items: [], live: false })
  useEffect(() => {
    if (!on || typeof EventSource === 'undefined') return
    const mine = (s: WatsonState & { cluster: string }) => (s.cluster === cluster ? s : { cluster, status: null, items: [], live: false })
    return subscribeAssistant(cluster, 'watson', {
      open: () => setState((s) => ({ ...mine(s), live: true })),
      error: () => setState((s) => ({ ...mine(s), live: false })),
      event: (name, data) => {
        if (name === 'status') {
          const status = JSON.parse(data) as WatsonStatus
          setState((s) => ({ ...mine(s), status }))
        } else if (name === 'items') {
          const items = JSON.parse(data) as WatsonItem[]
          setState((s) => ({ ...mine(s), items, live: true }))
        } else if (name === 'item') {
          const item = JSON.parse(data) as WatsonItem
          setState((s) => {
            const cur = mine(s)
            return { ...cur, items: mergeItem(cur.items, item) }
          })
        } else if (name === 'gone') {
          const { id } = JSON.parse(data) as { id: string }
          setState((s) => {
            const cur = mine(s)
            return { ...cur, items: cur.items.filter((x) => x.id !== id) }
          })
        }
      },
    })
  }, [cluster, on])
  return state.cluster === cluster ? state : { status: null, items: [], live: false }
}

/** Splits a block of commands, as Sherlock writes them, into one per line. */
export function splitCommands(text: string): string[] {
  const out: string[] = []
  let cur = ''
  for (const raw of text.split('\n')) {
    const line = raw.replace(/\s+$/, '')
    if (!cur && (/^\s*#/.test(line) || !line.trim())) continue
    const part = cur ? line.trimStart() : line
    if (part.endsWith('\\')) {
      cur += part.slice(0, -1).trimEnd() + ' '
      continue
    }
    cur += part
    const c = stripComment(cur).trim()
    if (c) out.push(c)
    cur = ''
  }
  const c = stripComment(cur).trim()
  if (c) out.push(c)
  return out
}

function stripComment(s: string): string {
  let q = ''
  for (let i = 0; i < s.length; i++) {
    const ch = s[i]!
    if (q) {
      if (ch === q) q = ''
    } else if (ch === "'" || ch === '"') q = ch
    else if (ch === '#' && (i === 0 || /\s/.test(s[i - 1]!))) return s.slice(0, i)
  }
  return s
}

/** Whether a block of code is commands Watson could run. */
export function isCommands(text: string): boolean {
  const cmds = splitCommands(text)
  return cmds.length > 0 && cmds.every((c) => /^(kubectl|helm)\s/.test(c))
}

/** Whether Watson runs a command (P6-02): the rest it refuses. */
export function watsonSupports(command: string): boolean {
  // Flags may come before the verb (kubectl -n web get pods).
  const words = command.trim().split(/\s+/)
  const out = words.slice(0, 1)
  let i = 1
  while (i < words.length && words[i]!.startsWith('-')) {
    const w = words[i++]!
    if (!w.includes('=') && /^(-n|--namespace|--kube-context|--context)$/.test(w)) i++
  }
  const c = out.concat(words.slice(i)).join(' ')
  return (
    /^kubectl\s+(get|describe|logs|top|events|cordon|uncordon|drain|scale|label|annotate|taint)\b/.test(c) ||
    /^kubectl\s+auth\s+can-i\b/.test(c) ||
    /^kubectl\s+rollout\s+(restart|undo)\b/.test(c) ||
    /^kubectl\s+delete\s+(pods?|po|pvc|persistentvolumeclaims?)([\s/]|$)/.test(c) ||
    /^kubectl\s+patch\s+(pvc|persistentvolumeclaims?)[\s/]/.test(c) ||
    /^helm\s+(list|ls|status|history|hist|rollback)\b/.test(c) ||
    /^helm\s+get\s+values\b/.test(c)
  )
}
