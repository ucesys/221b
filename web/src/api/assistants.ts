import { apiPath } from '../lib/router'
import { unauthorized } from './auth'
import { whenLoaded } from '../lib/loaded'

// Sherlock and Watson share one event stream per cluster (assistants/
// events): a browser opens at most six connections to a host over
// HTTP/1.1, for all its tabs, and each stream holds one. Their events
// arrive named sherlock-…, watson-… and sessions-….

export type Assistant = 'sherlock' | 'watson' | 'sessions'

export interface AssistantHandlers {
  open: () => void
  error: () => void
  /** An event of this assistant, by its name without the prefix. */
  event: (name: string, data: string) => void
}

const EVENTS: Record<Assistant, string[]> = {
  sherlock: ['status', 'conversation', 'answer', 'ui'],
  watson: ['status', 'items', 'item', 'gone'],
  // The Toolbox's sessions (Phase 9), for the dock's tabs.
  sessions: ['all', 'session', 'gone'],
}

interface Sub {
  who: Assistant
  h: AssistantHandlers
}

interface Shared {
  subs: Set<Sub>
  es: EventSource | null
  open: boolean
  /** Connections in a row that ended in an HTTP error. */
  failures: number
  /** Cancels a start still waiting for the page to load. */
  cancel: () => void
}

const streams = new Map<string, Shared>()

function connect(cluster: string, s: Shared) {
  s.cancel()
  s.es?.close()
  s.es = null
  s.open = false
  // After the page has loaded (Firefox holds its load event while a stream
  // opened during loading stays open), and after every panel rendered with
  // this one has subscribed, so they share the first connection.
  let timer: ReturnType<typeof setTimeout> | undefined
  let retry: ReturnType<typeof setTimeout> | undefined
  const cancelLoad = whenLoaded(() => {
    timer = setTimeout(() => {
      const es = new EventSource(apiPath('assistants/events', cluster))
      s.es = es
      es.addEventListener('open', () => {
        s.open = true
        s.failures = 0
        for (const sub of s.subs) sub.h.open()
      })
      es.addEventListener('error', () => {
        if (s.es !== es) return
        s.open = false
        for (const sub of s.subs) sub.h.error()
        // EventSource retries a dropped connection by itself, but gives up
        // for good on an HTTP error, such as a 502 from a proxy while the
        // server restarts: open it again, waiting longer after each
        // failure, up to 30 seconds. When the session has ended, the server
        // answers 401 to anything, and sign-in is the way back.
        if (es.readyState === EventSource.CLOSED) {
          s.failures++
          void fetch('/api/v1/me', { headers: { Accept: 'application/json' } }).then((r) => unauthorized(r), () => false)
          const wait = Math.min(30_000, 1000 * 2 ** Math.min(s.failures, 5)) * (0.75 + Math.random() / 2)
          retry = setTimeout(() => connect(cluster, s), wait)
        }
      })
      for (const who of ['sherlock', 'watson', 'sessions'] as const) {
        for (const name of EVENTS[who]) {
          es.addEventListener(`${who}-${name}`, (e) => {
            const data = (e as MessageEvent<string>).data
            for (const sub of s.subs) if (sub.who === who) sub.h.event(name, data)
          })
        }
      }
    }, 0)
  })
  s.cancel = () => {
    cancelLoad()
    clearTimeout(timer)
    clearTimeout(retry)
  }
}

/**
 * Listens to one assistant's events on the cluster's shared stream; the
 * returned function stops. The stream opens with the first listener and
 * closes with the last. One that joins an open stream connects it again,
 * so every listener starts from the status and the full state.
 */
export function subscribeAssistant(cluster: string, who: Assistant, h: AssistantHandlers): () => void {
  let s = streams.get(cluster)
  const sub: Sub = { who, h }
  if (!s) {
    s = { subs: new Set(), es: null, open: false, failures: 0, cancel: () => undefined }
    streams.set(cluster, s)
    s.subs.add(sub)
    connect(cluster, s)
  } else {
    s.subs.add(sub)
    if (s.es) connect(cluster, s)
  }
  const shared = s
  return () => {
    shared.subs.delete(sub)
    if (shared.subs.size === 0) {
      shared.cancel()
      shared.es?.close()
      streams.delete(cluster)
    }
  }
}
