// Alerts links to reports on the same service (Phase 12, CS-20): an alert
// on something a service runs on (its cluster, namespace, workload or
// host, as the issue service's services say) shows how many open reports
// that service has, and opens them in the Inbox. Only operators see what
// services run on, so only they get the link. Since wave 5 (CU-02, CU-05)
// an alert also offers New Case to those who work the cases: a critical
// alert opens a P0 case, a warning a P2 (wave 6). Since CF-77 an alert an
// open case already covers shows that case in place of New Case, and since
// CF-79 a job's acknowledgement links its run and the case. Since CF-82 a
// job acknowledges an alert its run concluded on without a case too: covered
// by an exception, or diagnosed as expected, with why; and since CF-84 New
// Case names the alert by its reason and keeps its key.
import { useState } from 'react'
import type { AlertRow } from '../../api/types'
import { caseHref, coversOf, createCase, fetchAlertCovers, type AlertCovers, type CasesView } from '../../api/casework'
import { fetchServices, type IssueService, type OpReport } from '../../api/issues'
import { exceptionHref } from '../../api/lessons'
import { alertReason } from '../../lib/alerts'
import { useIssueMe, useShared } from '../../lib/cases'
import { fmtTs } from '../../lib/format'
import { casesHref, navigate } from '../../lib/router'
import { useOpenReports } from './count'
import { loadWaiting, WAITING_CASES } from './InboxCases'
import { runHref } from './jobsRoute'
import { errText, SERVICES } from './parts'

/** What a case opened from an alert is about: its object, as the cases name objects. */
export function alertObject(r: Pick<AlertRow, 'kind' | 'name' | 'namespace'>): string {
  const kind = r.kind === 'pod' ? 'Pod' : r.kind === 'node' ? 'Node' : 'PersistentVolumeClaim'
  return r.namespace && r.kind !== 'node' ? `${r.namespace}/${kind}/${r.name}` : `${kind}/${r.name}`
}

/** An alert's severity as a case's priority reads it: failed and error are critical. */
export const alertSeverity = (s: AlertRow['severity']) => (s === 'failed' || s === 'error' ? 'critical' : 'warning')

/** The shared list of the cases that cover a cluster's alerts, read every 30 seconds. */
export const alertCoversKey = (cluster: string) => `cases.alert-cases.${cluster}`

/**
 * New Case from an alert, for those who work the cases; or, when an open case
 * already covers the alert (CF-77), that case: the newest, with how many more.
 */
export function AlertCase({ row, cluster }: { row: AlertRow; cluster: string }) {
  const cases = useShared<CasesView>(WAITING_CASES, loadWaiting)
  const may = !!cases.value?.mayChange
  const covers = useShared<AlertCovers>(may && cluster ? alertCoversKey(cluster) : null, (background) => fetchAlertCovers(cluster, background))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  if (!may) return null
  const covering = coversOf(covers.value, row.key, alertObject(row))
  const c = covering[0]
  if (c) {
    const more = covering.length - 1
    return (
      <a
        className="rk-btn"
        href={caseHref(c.number)}
        title={`${c.id}, P${c.priority}: ${c.title}. An open case about this alert${more ? `; ${more} more open ${more === 1 ? 'case covers' : 'cases cover'} it too` : ''}.`}
        data-testid="alert-case-link"
        data-case={c.id}
      >
        {c.id}
        {more > 0 && <span className="rk-cnt">+{more}</span>}
      </a>
    )
  }
  const open = async () => {
    setBusy(true)
    setErr('')
    try {
      const c = await createCase({ fromAlert: { cluster, key: row.key, name: alertReason(row), severity: alertSeverity(row.severity), object: alertObject(row), summary: row.why ?? row.lastMessage ?? row.waitingMessage } })
      navigate(caseHref(c.number))
    } catch (e) {
      setErr(errText(e))
      setBusy(false)
    }
  }
  return (
    <>
      <button className="rk-btn" disabled={busy} onClick={() => void open()} title={`Opens a case about it: ${alertSeverity(row.severity) === 'critical' ? 'P0, a critical alert' : 'P2, a warning'}`} data-testid="alert-new-case">
        New Case
      </button>
      {err && <span className="rk-t" data-tone="err">{err}</span>}
    </>
  )
}

/** The most of a job's reason the tag shows; the whole is in its hover. */
const WHY_SHOWN = 100

/**
 * An alert's acknowledgement: who and when; a job's links its run and the case
 * its findings went to (CF-79), or, since CF-82, says it diagnosed the alert
 * as expected, with the agent's reason, or that an exception covers what it
 * found, with the exception.
 */
export function AlertAckTag({ ack }: { ack: NonNullable<AlertRow['ack']> }) {
  const job = !!ack.run
  const at = fmtTs(ack.at, { seconds: true })
  const stays = 'It stays off the banners until it changes or clears; the tab still counts it.'
  const variant = !job ? 'person' : ack.case ? 'case' : ack.exception ? 'exception' : ack.expected ? 'expected' : 'case'
  const why = ack.why ?? ''
  const tip = {
    person: `Acknowledged by ${ack.by} at ${at}. ${stays}`,
    case: `Acknowledged by the job ${ack.by} at ${at}: its run ${ack.run} sent what it found to ${ack.case ?? 'a case'}. ${stays}`,
    exception: `Acknowledged by the job ${ack.by} at ${at}: its run ${ack.run} found what the exception ${ack.exception} covers, so no case. ${stays}`,
    expected: `Acknowledged by the job ${ack.by} at ${at}: its run ${ack.run} diagnosed the alert as expected, ${why ? `the system working as intended: ${why}` : 'what it found below the job’s lowest priority'}. ${stays}`,
  }[variant]
  const run = ack.run && (
    <a className="rk-link" href={runHref(ack.run)} data-testid="alert-ack-run">
      {ack.run}
    </a>
  )
  return (
    <span className="rk-tag" data-tone="warn" style={{ fontSize: 11, padding: '2px 8px' }} title={tip} data-testid="alert-acked" data-job={job ? 'true' : undefined} data-variant={variant}>
      {variant === 'expected' ? (
        <>
          Acknowledged by {ack.by}: expected
          {why ? ` — ${why.length > WHY_SHOWN ? why.slice(0, WHY_SHOWN - 1) + '…' : why}` : ''}, {run}
        </>
      ) : variant === 'exception' ? (
        <>
          Acknowledged by {ack.by}: covered by{' '}
          <a className="rk-link" href={exceptionHref(ack.exception!)} data-testid="alert-ack-exception">
            {ack.exception}
          </a>
          , {run}
        </>
      ) : (
        <>
          Acknowledged by {ack.by} at {fmtTs(ack.at)}
          {run && (
            <>
              {' · '}
              {run}
            </>
          )}
          {ack.case && (
            <>
              {' · '}
              <a className="rk-link" href={caseHref(ack.case)} data-testid="alert-ack-case">
                {ack.case}
              </a>
            </>
          )}
        </>
      )}
    </span>
  )
}

/** The services an alert's object is part of: clusters are the connection's id and name. */
export function servicesForAlert(r: Pick<AlertRow, 'kind' | 'name' | 'namespace'>, clusters: string[], services: IssueService[]): IssueService[] {
  const inCluster = (c?: string) => !c || clusters.includes(c)
  return services.filter((s) =>
    (s.runsOn ?? []).some((o) => {
      switch (o.kind) {
        case 'cluster':
          return clusters.includes(o.value)
        case 'namespace':
          return r.kind !== 'node' && r.namespace === o.value && inCluster(o.cluster)
        case 'workload': {
          if (r.kind !== 'pod' || !inCluster(o.cluster)) return false
          const [ns, name] = o.value.includes('/') ? (o.value.split('/', 2) as [string, string]) : ['', o.value]
          return (!ns || ns === r.namespace) && (r.name === name || r.name.startsWith(name + '-'))
        }
        case 'host':
          return r.kind === 'node' && r.name === o.value && inCluster(o.cluster)
        default:
          return false
      }
    }),
  )
}

/** The open reports per service. */
export function openBy(reports: OpReport[]): Map<string, number> {
  const out = new Map<string, number>()
  for (const r of reports) if (r.service && r.status !== 'closed') out.set(r.service, (out.get(r.service) ?? 0) + 1)
  return out
}

export function AlertReports({ row, clusters }: { row: AlertRow; clusters: string[] }) {
  return (
    <>
      <AlertCase row={row} cluster={clusters[0] ?? ''} />
      <ServiceReports row={row} clusters={clusters} />
    </>
  )
}

function ServiceReports({ row, clusters }: { row: AlertRow; clusters: string[] }) {
  const me = useIssueMe()
  const operator = !!me?.operator
  const services = useShared<IssueService[]>(operator ? SERVICES : null, fetchServices)
  const reports = useOpenReports(operator)
  if (!operator || !services.value || !reports.value) return null
  const open = openBy(reports.value)
  const hit = servicesForAlert(row, clusters, services.value).filter((s) => (open.get(s.id) ?? 0) > 0)
  if (hit.length === 0) return null
  return (
    <>
      {hit.map((s) => (
        <a key={s.id} className="rk-btn" href={casesHref('inbox', [], { q: s.name })} title={`Open cases reported on ${s.name}, which runs here`} data-testid="alert-reports">
          {s.name}: {open.get(s.id)} {open.get(s.id) === 1 ? 'report' : 'reports'}
        </a>
      ))}
    </>
  )
}
