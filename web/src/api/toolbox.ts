// The Toolbox (Phase 9): what a person may use where, the cards that ask
// for a session, and the sessions themselves, streamed beside Sherlock's
// and Watson's events and attached over a WebSocket.

import { useEffect, useRef, useState } from 'react'
import { unauthorized } from './auth'
import { subscribeAssistant } from './assistants'
import type { WatsonItem } from './watson'
import { apiPath } from '../lib/router'

export type ToolName = 'shell' | 'command' | 'forward' | 'debug-container' | 'debug-pod' | 'node-shell' | 'node-command'

export const POD_TOOLS: ToolName[] = ['shell', 'command', 'forward', 'debug-container']
export const NODE_TOOLS: ToolName[] = ['node-shell', 'node-command', 'debug-pod']

export interface ToolboxImage {
  name: string
  image: string
  caps?: string[]
  tools?: string
}

export interface NamespaceTools {
  /** "" for a tool the person may use there, else why not. */
  tools: Partial<Record<ToolName, string>>
  /** The namespace's Pod Security level: privileged, baseline or restricted. */
  level: string
}

export interface ToolboxInfo {
  enabled: boolean
  readOnly: boolean
  production: boolean
  twoPerson: boolean
  signedIn: boolean
  admin: boolean
  me: string
  /** Why a tool is off everywhere on this cluster, by tool. */
  off: Partial<Record<ToolName, string>>
  idleSeconds: number
  maxMinutes: number
  transcripts: string
  nodeApproval: string
  nodeNamespace: string
  mountVolumes: boolean
  fileTransfer: boolean
  cliMinVersion?: string
  images: ToolboxImage[]
  debugContainerImages: string[]
  debugPodProfiles: string[]
  nodeProfiles: string[]
  podCommands?: string[]
  nodeCommands?: string[]
  namespaces: Record<string, NamespaceTools>
  nodes: Partial<Record<ToolName, string>>
  values: unknown
}

/** What a dialog sends; the server checks it all again. */
export interface ToolRequest {
  tool: ToolName
  namespace?: string
  pod?: string
  container?: string
  node?: string
  shell?: string
  command?: string
  port?: number
  localPort?: number
  image?: string
  target?: string
  mountVolumes?: boolean
  readOnlyVolumes?: boolean
  profile?: string
  serviceAccount?: string
  labelsFrom?: string
  labels?: Record<string, string>
  minutes?: number
  reason?: string
}

export interface ForwardConn {
  id: number
  from?: string
  opened: string
  closed?: string
  bytes: number
}

export interface ForwardStats {
  cli?: string
  listen?: string
  open: number
  total: number
  bytesIn: number
  bytesOut: number
  last?: string
  conns?: ForwardConn[]
}

export type SessionState = 'starting' | 'live' | 'ended'

export interface SessionView {
  id: string
  cluster: string
  tool: ToolName
  summary: string
  request: ToolRequest
  owner: string
  mine: boolean
  approver?: string
  card?: string
  state: SessionState
  created: string
  started?: string
  lastActive: string
  ends?: string
  ended?: string
  endedBy?: string
  endNote?: string
  exit?: number
  namespace?: string
  pod?: string
  container?: string
  notes?: string[]
  objects?: { kind: string; namespace: string; name: string }[]
  forward?: ForwardStats
  /** The command its owner runs for a port forward. */
  cli?: string
  watchers?: string[]
  recorded: boolean
  interactive: boolean
  idleSeconds: number
  rev: number
}

export interface SessionRecord {
  id: string
  cluster: string
  tool: ToolName
  summary: string
  namespace?: string
  pod?: string
  container?: string
  node?: string
  owner: string
  ownerUser?: string
  approver?: string
  reason?: string
  created: string
  started?: string
  ended: string
  endedBy: string
  endNote?: string
  exit?: number
  recorded?: boolean
  bytes?: number
  connections?: number
  watchers?: string[]
}

export class ToolboxError extends Error {
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
    throw new ToolboxError('The console did not answer. Check the connection and try again.', 0)
  }
  if (unauthorized(res)) throw new ToolboxError('Your session has ended; sign in again.', 401)
  if (res.status === 204) return undefined as T
  const data = (await res.json().catch(() => null)) as (T & { error?: string }) | null
  if (!res.ok) throw new ToolboxError(data?.error || `The console answered ${res.status}.`, res.status)
  return data as T
}

const cache = new Map<string, { at: number; info: Promise<ToolboxInfo> }>()

/**
 * What the person may use on a cluster, for some namespaces and the nodes;
 * kept a minute, as the server keeps its access answers.
 */
export function fetchToolbox(cluster: string, namespaces: string[] = [], nodes = false): Promise<ToolboxInfo> {
  const ns = [...new Set(namespaces)].sort().join(',')
  const key = `${cluster}\x00${ns}\x00${nodes}`
  const hit = cache.get(key)
  if (hit && Date.now() - hit.at < 60_000) return hit.info
  const q = new URLSearchParams()
  if (ns) q.set('namespaces', ns)
  if (nodes) q.set('nodes', '1')
  const info = call<ToolboxInfo>('GET', apiPath('toolbox', cluster) + (q.size ? '?' + q.toString() : ''))
  info.catch(() => cache.delete(key))
  cache.set(key, { at: Date.now(), info })
  return info
}

/**
 * Sends a dialog's request: the session opens at once (opened), or where a
 * second person must approve it, it becomes a Watson card (TB-36).
 */
export const requestSession = (cluster: string, req: ToolRequest, from = '') => call<WatsonItem & { opened?: SessionView }>('POST', apiPath('watson/sessions', cluster), { ...req, from })

/** A Tools menu's fixed change: the server writes the command from it. */
export type ToolAction = { action: 'cordon' | 'uncordon' | 'drain'; node: string } | { action: 'delete-pod'; namespace: string; pod: string }

/** Runs a fixed change as the person, at once unless a second person must approve; Watson shows it. */
export const runToolAction = (cluster: string, a: ToolAction) => call<WatsonItem>('POST', apiPath('watson/actions', cluster), a)
export const endSession = (cluster: string, id: string, reason = '') => call<SessionView>('POST', apiPath(`sessions/${encodeURIComponent(id)}/end`, cluster), { reason })
export const keepSession = (cluster: string, id: string) => call<SessionView>('POST', apiPath(`sessions/${encodeURIComponent(id)}/keep`, cluster), {})
export const extendSession = (cluster: string, id: string) => call<SessionView>('POST', apiPath(`sessions/${encodeURIComponent(id)}/extend`, cluster), {})
export const fetchSessions = (cluster: string) => call<SessionView[]>('GET', apiPath('sessions', cluster))
export const fetchSessionHistory = (cluster: string) => call<SessionRecord[]>('GET', apiPath('sessions', cluster) + '?history=1')
export const sessionOutputHref = (cluster: string, id: string) => apiPath(`sessions/${encodeURIComponent(id)}/output`, cluster)
export const transcriptHref = (cluster: string, id: string) => apiPath(`sessions/${encodeURIComponent(id)}/transcript`, cluster)

/** The WebSocket of a terminal: its owner's, or an administrator's read-only view (TB-55). */
export function attachURL(cluster: string, id: string, watch = false): string {
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${scheme}//${location.host}${apiPath(`sessions/${encodeURIComponent(id)}/${watch ? 'watch' : 'attach'}`, cluster)}`
}

export interface SessionsState {
  sessions: SessionView[]
  live: boolean
}

/** Sorts sessions: live first, then newest. */
export function sortSessions(list: SessionView[]): SessionView[] {
  const rank = (s: SessionView) => (s.state === 'ended' ? 1 : 0)
  return list.slice().sort((a, b) => rank(a) - rank(b) || b.created.localeCompare(a.created))
}

/**
 * The sessions a person sees on a cluster, as they change; onNew hears of
 * each that appears after the first list, such as one an approval opened.
 */
export function useSessions(cluster: string, on = true, onNew?: (s: SessionView) => void): SessionsState {
  const [state, setState] = useState<SessionsState & { cluster: string }>({ cluster, sessions: [], live: false })
  const heard = useRef(onNew)
  useEffect(() => {
    heard.current = onNew
  }, [onNew])
  useEffect(() => {
    if (!on || typeof EventSource === 'undefined') return
    let known: Set<string> | null = null
    const mine = (s: SessionsState & { cluster: string }) => (s.cluster === cluster ? s : { cluster, sessions: [], live: false })
    return subscribeAssistant(cluster, 'sessions', {
      open: () => setState((s) => ({ ...mine(s), live: true })),
      error: () => setState((s) => ({ ...mine(s), live: false })),
      event: (name, data) => {
        if (name === 'all') {
          const sessions = sortSessions(JSON.parse(data) as SessionView[])
          if (known) for (const x of sessions) if (!known.has(x.id)) heard.current?.(x)
          known = new Set(sessions.map((x) => x.id))
          setState((s) => ({ ...mine(s), sessions, live: true }))
        } else if (name === 'session') {
          const next = JSON.parse(data) as SessionView
          if (known && !known.has(next.id)) {
            known.add(next.id)
            heard.current?.(next)
          }
          setState((s) => {
            const cur = mine(s)
            const others = cur.sessions.filter((x) => x.id !== next.id)
            return { ...cur, sessions: sortSessions([...others, next]) }
          })
        } else if (name === 'gone') {
          const { id } = JSON.parse(data) as { id: string }
          setState((s) => {
            const cur = mine(s)
            return { ...cur, sessions: cur.sessions.filter((x) => x.id !== id) }
          })
        }
      },
    })
  }, [cluster, on])
  return state.cluster === cluster ? state : { sessions: [], live: false }
}

/** A tool's name as the menus and dialogs show it. */
export function toolLabel(t: ToolName): string {
  return {
    shell: 'Shell',
    command: 'Run a command',
    forward: 'Port forward',
    'debug-container': 'Debug container',
    'debug-pod': 'Debug pod',
    'node-shell': 'Node shell',
    'node-command': 'Run a command on the node',
  }[t]
}

/** A session's short tab label: "sh api-1 · main", "pf 5432 → postgres-0", "node worker-3". */
export function tabLabel(s: SessionView): string {
  const r = s.request
  const pod = s.pod || r.pod || ''
  switch (s.tool) {
    case 'forward':
      return `pf ${r.localPort || r.port} → ${pod}`
    case 'node-shell':
    case 'node-command':
      return `node ${r.node ?? ''}`
    case 'debug-container':
      return `dbg ${pod}`
    case 'debug-pod':
      return `dbg ${s.pod || r.namespace || ''}`
    case 'command':
      return `run ${pod}`
    default:
      return `sh ${pod}${s.container ? ' · ' + s.container : ''}`
  }
}

/** How a session ended, in words. */
export function endedWords(s: { endedBy?: string; endNote?: string }): string {
  if (s.endNote) return s.endNote
  return (
    {
      idle: 'Ended after no activity.',
      limit: 'Ended at its time limit.',
      owner: 'Ended by its owner.',
      admin: 'Ended by an administrator.',
      finished: 'Finished.',
      gone: 'The pod went away.',
      restart: 'The console restarted.',
      failed: 'It could not open.',
      signout: 'Its owner signed out.',
      'no-cli': 'No CLI connected in time.',
    }[s.endedBy ?? ''] ?? 'Ended.'
  )
}

/** Bytes, short: 1.2 MB. */
export function fBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const u = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${u[i]}`
}

/** mm:ss, or h:mm:ss. */
export function clock(seconds: number): string {
  const s = Math.max(0, Math.round(seconds))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const ss = String(s % 60).padStart(2, '0')
  return h ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`
}
