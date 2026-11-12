// Small parts the Cases tabs share: how a priority and people read, and
// the known issues' statuses (IS-05, IS-08).
import type { ReactNode } from 'react'
import type { KnownStatus, Person } from '../../api/issues'
import { priorityText } from '../../report/api'

export const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

// --- One flow for findings and cases (Phase 12, wave 6): P0 to P4.
/** The priorities in words: "P0: Outage" to "P4: Question". */
export const PRIORITY: Record<number, string> = Object.fromEntries([0, 1, 2, 3, 4].map((p) => [p, priorityText(p)]))

/** The priorities a picker offers, P0 first. */
export const PRIORITIES = [0, 1, 2, 3, 4] as const

/** Whether a value is a priority: 0 is P0, so never test one for truth. */
export const isPriority = (p: unknown): p is number => typeof p === 'number' && p >= 0 && p <= 4

export const priorityTone = (p: number) => (p === 0 ? 'err' : p === 1 ? 'warn' : p === 2 ? 'info' : 'muted')

export const KNOWN_STATUS: Record<KnownStatus, string> = {
  investigating: 'Investigating',
  identified: 'Identified',
  fixInProgress: 'Fix in progress',
  resolved: 'Resolved',
}

export const knownTone = (s: KnownStatus) => (s === 'resolved' ? 'ok' : s === 'fixInProgress' ? 'warn' : s === 'identified' ? 'info' : 'err')

export const who = (p?: Person | null) => (p ? p.name || p.id : '')

/** The first line of a text, cut to fit a row. */
export function firstLine(text: string | undefined, max = 90): string {
  const line = (text ?? '').split('\n')[0]?.trim() ?? ''
  return line.length > max ? line.slice(0, max - 1).trimEnd() + '…' : line
}

export function Tag({ tone, children, testId }: { tone: string; children: ReactNode; testId?: string }) {
  return (
    <span className="rk-tag" data-tone={tone} data-testid={testId}>
      {children}
    </span>
  )
}

/** The issue service's services, shared by the pages that read them. */
export const SERVICES = 'cases.services'
