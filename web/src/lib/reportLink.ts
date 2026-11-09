// The ways into the report page from 221B (Phase 12, CS-15 to CS-19,
// ADR-89): New report and On behalf of in Cases, Report a problem and My
// reports in the account menu, Sherlock's Report this and the error
// screens' Report this problem. Each opens the page in a new tab, at the
// address the issue service's settings give.
//
// A pre-filled report is handed over in this browser, never in the
// address's query, which servers and proxies log: the text goes into
// localStorage under a key made for this one opening, and the page's
// fragment names the key. The report page reads it once, removes it, and
// puts it in its box; nothing is sent until the person presses Send. A
// report page at another origin cannot read this browser's storage there,
// so the text rides in the fragment itself, which the browser never sends.
// Handovers not taken within two minutes are thrown away.

const PREFIX = '221b.report.handoff.'

/** How long a handover waits for the page to open. */
export const HANDOFF_TTL = 120_000

/** The most text handed over: the report page takes 4,000 characters. */
export const HANDOFF_MAX = 4000

interface Stored {
  text: string
  at: number
}

/** Where the report page is: the settings' address, "/report" by default. */
export function reportBase(reportURL: string | undefined): string {
  const u = (reportURL ?? '').trim()
  return u === '' ? '/report' : u.replace(/\/+$/, '')
}

/** Whether an address is on this page's origin. */
function sameOrigin(url: string): boolean {
  try {
    return new URL(url, window.location.href).origin === window.location.origin
  } catch {
    return false
  }
}

/** A key for one handover. */
function newKey(): string {
  const c = globalThis.crypto
  if (c && 'randomUUID' in c) return c.randomUUID()
  return Math.random().toString(36).slice(2) + Date.now().toString(36)
}

/** Throws away handovers nobody took. */
function sweep(now: number) {
  try {
    const ls = window.localStorage
    for (let i = ls.length - 1; i >= 0; i--) {
      const key = ls.key(i)
      if (!key?.startsWith(PREFIX)) continue
      const v = JSON.parse(ls.getItem(key) ?? 'null') as Stored | null
      if (!v || typeof v.at !== 'number' || now - v.at > HANDOFF_TTL) ls.removeItem(key)
    }
  } catch {
    // Without storage there is nothing to sweep.
  }
}

export interface ReportLink {
  /** The text to put in the box, for the person to edit and send. */
  text?: string
  /** Files for someone else: an operator's On behalf of (IS-03, IS-28). */
  forUser?: string
  /** Opens the person's own reports alone. */
  mine?: boolean
}

/**
 * The report page's address for a link, handing its text over: in this
 * browser's storage when the page is on this origin, in the fragment when
 * it is not, and never in the query.
 */
export function reportHref(reportURL: string | undefined, link: ReportLink = {}, now = Date.now()): string {
  const base = reportBase(reportURL)
  let url = link.mine ? base + '/mine' : base
  if (link.forUser?.trim()) url += '?for=' + encodeURIComponent(link.forUser.trim())
  const text = (link.text ?? '').trim().slice(0, HANDOFF_MAX)
  if (!text) return url
  if (!sameOrigin(url)) return url + '#text=' + encodeURIComponent(text)
  sweep(now)
  const key = newKey()
  try {
    window.localStorage.setItem(PREFIX + key, JSON.stringify({ text, at: now } satisfies Stored))
    return url + '#handoff=' + encodeURIComponent(key)
  } catch {
    // Storage refused: the fragment still keeps it off the server.
    return url + '#text=' + encodeURIComponent(text)
  }
}

let taken: { hash: string; text: string } | null = null

/**
 * What the report page was handed (its fragment's #handoff=<key> or
 * #text=), read once: the stored text is removed as it is read, and the
 * fragment is taken off the address. Reading the same fragment again in
 * the same page (React's strict mode renders twice) gives the same text.
 */
export function takeHandoff(hash: string = window.location.hash, now = Date.now()): string {
  if (taken && taken.hash === hash) return taken.text
  const params = new URLSearchParams(hash.replace(/^#/, ''))
  let text = ''
  const key = params.get('handoff')
  if (key) {
    try {
      const raw = window.localStorage.getItem(PREFIX + key)
      window.localStorage.removeItem(PREFIX + key)
      const v = raw ? (JSON.parse(raw) as Stored) : null
      if (v && typeof v.text === 'string' && typeof v.at === 'number' && now - v.at <= HANDOFF_TTL) text = v.text
    } catch {
      text = ''
    }
  } else if (params.has('text')) {
    text = params.get('text') ?? ''
  }
  text = text.slice(0, HANDOFF_MAX)
  taken = { hash, text }
  if ((key || params.has('text')) && typeof window !== 'undefined' && window.location.hash === hash) {
    window.history.replaceState(null, '', window.location.pathname + window.location.search)
  }
  return text
}

/** For tests: forgets what was taken. */
export function forgetHandoffForTests(): void {
  taken = null
}

// ---------- what is handed over ----------

/** Fenced code, which may hold commands and output, is left out of a summary. */
function prose(text: string): string {
  return text
    .replace(/```[\s\S]*?(```|$)/g, ' ')
    .replace(/`([^`]*)`/g, '$1')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/[ \t]+/g, ' ')
    .replace(/\n{3,}/g, '\n\n')
    .trim()
}

const cut = (s: string, n: number) => (s.length > n ? s.slice(0, n - 1).trimEnd() + '…' : s)

/**
 * The summary Sherlock's Report this hands over (CS-18): the last question
 * and the start of the answer to it, in words, with no code blocks, for
 * the person to edit. Sherlock never sees a Secret's value (X-01), and the
 * report page scrubs what is sent.
 */
export function sherlockSummary(clusterName: string, question: string, answer: string): string {
  const q = cut(prose(question), 400)
  const a = cut(prose(answer), 1200)
  const lines = [`I asked Sherlock about ${clusterName || 'a cluster'}: ${q}`]
  if (a) lines.push('', 'Sherlock said:', a)
  lines.push('', 'It is not fixed yet. What I see:')
  return lines.join('\n')
}

/**
 * The text an error screen's Report this problem hands over (CS-19): the
 * screen, the error, the page's address and the time, for the person to
 * add what they were doing.
 */
export function errorSummary(screen: string, error: string, where: string = window.location.hash || '/', at: Date = new Date()): string {
  return [`221B showed an error on ${screen}.`, `Error: ${cut(error.trim(), 600)}`, `Page: ${where}`, `When: ${at.toISOString()}`, '', 'What I was doing:'].join('\n')
}
