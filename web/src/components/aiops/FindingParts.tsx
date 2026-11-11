// A finding's parts, as a run shows them (Phase 12, FX-01; wave 6): its
// severity, the object it is about, and what it did, linked to where it
// went: the case it opened, joined (seen) or reopened, the known issue it
// was recorded on, the exception that left it out, or why it was not
// valid. A finding is not decided on its own any more: the case is.
import type { ReactNode } from 'react'
import { caseHref } from '../../api/casework'
import { exceptionHref } from '../../api/lessons'
import type { Finding, FindingRouting } from '../../api/jobs'
import { aiopsHref } from '../../lib/router'
import { ObjectLink } from './ObjectLink'

const SEVERITY: Record<Finding['severity'], [string, 'ok' | 'warn' | 'err' | undefined]> = {
  critical: ['Critical', 'err'],
  warning: ['Warning', 'warn'],
  info: ['Info', undefined],
}

/** A finding's severity, as a pill. */
export function SeverityPill({ severity }: { severity: Finding['severity'] }) {
  const [label, tone] = SEVERITY[severity] ?? SEVERITY.warning
  return (
    <span className="rk-pill" data-tone={tone}>
      {label}
    </span>
  )
}

/** A known issue's page in Mycroft › Known issues. */
export const knownHref = (id: string) => aiopsHref('mycroft', ['known', id])

/** The object a finding is about, linked, with its cluster. */
export function FindingObject({ f }: { f: Finding }) {
  const resource = f.key?.object || f.resource
  const cluster = f.key?.cluster || f.cluster
  if (!cluster && !resource) return null
  return (
    <span className="rk-mono" style={{ fontSize: 12, color: 'var(--fg2)', display: 'inline-flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }} data-testid="finding-object">
      {cluster && <span>{cluster}</span>}
      {cluster && resource && <span aria-hidden="true">·</span>}
      {resource ? <ObjectLink cluster={cluster ?? ''} resource={resource} /> : <span>cluster-wide</span>}
    </span>
  )
}

/** What each routing reads as, with its tone. */
export const ROUTING: Record<FindingRouting, { label: string; tone: string; hint: string }> = {
  opened: { label: 'New case', tone: 'warn', hint: 'Opened a new case' },
  seen: { label: 'Seen again', tone: 'info', hint: 'Joined the open case of the same problem' },
  known: { label: 'Known issue', tone: 'muted', hint: 'Recorded on the known issue as still present' },
  excepted: { label: 'Excepted', tone: 'muted', hint: 'Left out by an accepted exception' },
  reopened: { label: 'Reopened', tone: 'err', hint: 'The fix did not hold: the case is open again' },
  regression: { label: 'Regression', tone: 'err', hint: 'Back after a fix: a new case marked a regression' },
  capped: { label: 'Capped', tone: 'muted', hint: 'Over the run’s limit of new cases (Settings › Cases)' },
  invalid: { label: 'Not valid', tone: 'muted', hint: 'Its validation rule or object was not valid: not routed' },
  // --- A job's lowest priority (Phase 12, wave 6c, CF-33) ---
  ignored: { label: 'Ignored', tone: 'muted', hint: 'Below the job’s lowest priority: it opened no case' },
  // --- Alerts that are not a problem (Phase 12, wave 6, CF-82) ---
  expected: { label: 'Expected', tone: 'muted', hint: 'The system working as intended, the agent said why: it opened no case' },
}

/** What a finding did, as a chip, linked to its case, known issue or exception. */
export function RoutingChip({ f, testId }: { f: Finding; testId: string }) {
  const r = ROUTING[f.routing] ?? { label: f.routing, tone: 'muted', hint: '' }
  let link: ReactNode = null
  if (f.case)
    link = (
      <a className="rk-link rk-mono" href={caseHref(f.case)} data-testid={`${testId}-case`}>
        {f.case}
      </a>
    )
  else if (f.knownIssue)
    link = (
      <a className="rk-link rk-mono" href={knownHref(f.knownIssue)} data-testid={`${testId}-known`}>
        {f.knownIssue}
      </a>
    )
  else if (f.exception)
    link = (
      <a className="rk-link rk-mono" href={exceptionHref(f.exception)} data-testid={`${testId}-exception`}>
        {f.exception}
      </a>
    )
  return (
    <span style={{ display: 'inline-flex', gap: 6, alignItems: 'center', fontSize: 12 }} data-testid={testId} data-routing={f.routing}>
      <span className="rk-tag" data-tone={r.tone} title={r.hint}>
        {r.label}
      </span>
      {link}
    </span>
  )
}
