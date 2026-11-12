// AIOps › Mycroft › Exceptions (Phase 12, FX-06, MY-01; wave 6). An
// exception is an agent-found case someone closed as intended or
// acceptable, with why and until when; while it is active the
// agents stop reporting the same check on the same object, it counts the
// findings it left out and when it last did, and an approver may remove
// it. Stale ones (never matched, or not lately) are filtered and sorted
// out to be reviewed. It is the console's record only, never a wiki page
// (MY-09), and links back to its case. Since wave 6e the RCAs are no list
// of their own here: an RCA is part of the case it was fixed with. Since
// wave 6g they are a table that sorts by any column, Updated newest first
// (the stale view by last matched, never first), and the one a link names
// opens in a drawer with everything about it; housekeeping's duplicates
// show what they duplicate. Since build 14: an approver creates one by
// hand (New exception) and makes an inactive one active again; a removed
// exception is shown as Inactive with why (turned off by someone, or a
// duplicate of another); the filter is Active | Stale | Expired |
// Inactive | All; the check is called the validation rule, with what it
// means beside its id; and the cluster shows by its name, its id in the
// hover.
import { useCallback, useEffect, useRef, useState, type JSX } from 'react'
import { CallError } from '../../api/call'
import { exceptionHref, fetchExceptions, reactivateException, removeException, type AcceptedException, type ExceptionCluster, type ExceptionState } from '../../api/lessons'
import { fmtTs } from '../../lib/format'
import { navigate } from '../../lib/router'
import { useSorted, type SortCol } from '../../lib/useSorted'
import { SortTh } from '../automation/SortTh'
import { ConfirmDialog } from '../ConfirmDialog'
import { Drawer } from '../Drawer'
import { caseHref, runHref } from './jobsRoute'
import { NewExceptionDrawer } from './NewException'
import { ObjectLink } from './ObjectLink'
import { EmptyRow, MycroftPage, MycroftTable } from './MycroftPage'

/** What a refusal or a failure says: a role without the records is told so plainly. */
const errorText = (e: unknown) =>
  e instanceof CallError && e.status === 403 ? 'Your role does not include these records: they are for those who approve proposed changes, the console-wide roles and the job editors.' : e instanceof Error ? e.message : String(e)

/** The state as people read it: the server's "removed" is Inactive (build 14). */
const STATE: Record<ExceptionState, [string, string]> = {
  active: ['Active', 'ok'],
  expired: ['Expired', 'muted'],
  removed: ['Inactive', 'muted'],
}

/** Why an exception is inactive: "duplicate of EX-2" or "turned off by sam". */
function inactiveWhy(e: Pick<AcceptedException, 'duplicateOf' | 'removedBy'>): string {
  return e.duplicateOf ? `duplicate of ${e.duplicateOf}` : `turned off by ${e.removedBy || 'someone'}`
}

/** Whether an inactive exception may be made active again: its last day not passed (the server checks again). */
const mayReactivate = (e: AcceptedException, now: number) => e.state === 'removed' && !(e.expires && Date.parse(e.expires) <= now)

/** The cluster by its name, its id in the hover (as cases show it, CF-60). */
function ClusterName({ e }: { e: AcceptedException }) {
  if (!e.cluster) return <span style={{ color: 'var(--fg3)' }}>—</span>
  return (
    <span title={e.cluster} data-testid="exception-cluster-name">
      {e.clusterName || e.cluster}
    </span>
  )
}

/** The validation rule's id, and what it means when the catalogue says. */
function Rule({ e, block }: { e: AcceptedException; block?: boolean }) {
  return (
    <span data-testid="exception-rule">
      <span className="rk-mono">{e.check}</span>
      {e.ruleMeaning && (block ? <span className="tb-sub">{e.ruleMeaning}</span> : <span style={{ color: 'var(--fg2)' }}> · {e.ruleMeaning}</span>)}
    </span>
  )
}

/** Scrolls the item a link opened into view, once. */
function useScrollTo(open: string, ready: boolean) {
  const ref = useRef<HTMLElement | null>(null)
  useEffect(() => {
    if (ready && open) ref.current?.scrollIntoView?.({ block: 'nearest' })
  }, [open, ready])
  return ref
}

function Line({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', gap: 8, fontSize: 12.5 }}>
      <span style={{ color: 'var(--fg3)', minWidth: 90 }}>{label}</span>
      <span style={{ minWidth: 0, overflowWrap: 'anywhere' }}>{children}</span>
    </div>
  )
}

type Show = 'active' | 'stale' | 'expired' | 'inactive' | 'all'

const SHOW: [Show, string, string][] = [
  ['active', 'Active', 'The exceptions the agents follow now'],
  ['stale', 'Stale', 'Never matched, or not lately: review whether they still apply'],
  ['expired', 'Expired', 'Past their last day'],
  ['inactive', 'Inactive', 'Turned off by someone, or by the housekeeping as a duplicate of another'],
  ['all', 'All', 'Expired and inactive ones too'],
]

/** When an exception last matched, for sorting the stale ones: never first. */
const lastOf = (e: AcceptedException) => e.lastMatched ?? ''

/** Which exceptions a view keeps, and in what order: stale ones never matched first, then the longest unmatched (the table sorts them again). */
export function exceptionsShown(list: AcceptedException[], show: Show, open = ''): AcceptedException[] {
  const keeps = (e: AcceptedException) => {
    switch (show) {
      case 'all':
        return true
      case 'expired':
        return e.state === 'expired'
      case 'inactive':
        return e.state === 'removed'
      case 'stale':
        return e.state === 'active' && e.stale
    }
    return e.state === 'active'
  }
  const kept = list.filter((e) => e.id === open || keeps(e))
  return show === 'stale' ? [...kept].sort((a, b) => lastOf(a).localeCompare(lastOf(b))) : kept
}

/** Where a closed drawer goes back to. */
const LIST = '#/aiops/mycroft/exceptions'

const time = (at?: string) => (at ? Date.parse(at) || null : null)
/** When it last changed: older records have no updated, so when it was removed or made. */
const updatedOf = (e: AcceptedException) => e.updated ?? e.removedAt ?? e.at

const EXCEPTION_SORT: SortCol<AcceptedException>[] = [
  { id: 'id', sortValue: (e) => Number(e.id.replace(/\D/g, '')) || 0, defaultDir: 'desc' },
  { id: 'title', sortValue: (e) => e.title },
  { id: 'cluster', sortValue: (e) => e.clusterName || e.cluster || null },
  { id: 'object', sortValue: (e) => e.resource || null },
  { id: 'rule', sortValue: (e) => e.check },
  { id: 'state', sortValue: (e) => e.state },
  { id: 'reason', sortValue: (e) => e.reason || null },
  { id: 'matches', sortValue: (e) => e.matches, defaultDir: 'desc' },
  // Never matched counts as the oldest: first when the stale ones are sorted up.
  { id: 'lastMatched', sortValue: (e) => time(e.lastMatched) ?? 0, defaultDir: 'desc' },
  { id: 'updated', sortValue: (e) => time(updatedOf(e)), defaultDir: 'desc' },
]

/** How often it matched: "3 times", or never. */
const matchesText = (e: AcceptedException) => (e.matches ? `${e.matches} ${e.matches === 1 ? 'time' : 'times'}` : 'never')

/** The accepted exceptions (FX-06), a table sorted by Updated; open is the one a link names, in a drawer. */
export function ExceptionsView({ open, onToast, onCount }: { open: string; onToast?: (msg: string) => void; onCount?: (n: number) => void }): JSX.Element {
  const [list, setList] = useState<AcceptedException[] | null>(null)
  const [may, setMay] = useState(false)
  const [clusters, setClusters] = useState<ExceptionCluster[]>([])
  const [adding, setAdding] = useState(false)
  const [reviving, setReviving] = useState<AcceptedException | null>(null)
  const [error, setError] = useState('')
  const [show, setShow] = useState<Show>('active')
  const [removing, setRemoving] = useState<AcceptedException | null>(null)
  const [busy, setBusy] = useState(false)
  const [removeError, setRemoveError] = useState('')
  const [now] = useState(() => Date.now())
  const load = useCallback(() => {
    fetchExceptions().then(
      (r) => {
        setList(r.exceptions)
        setMay(r.mayChange)
        setClusters(r.clusters ?? [])
        setError('')
        onCount?.(r.exceptions.filter((e) => e.state === 'active').length)
      },
      (e: unknown) => setError(errorText(e)),
    )
  }, [onCount])
  useEffect(() => load(), [load])
  const openOne = list?.find((e) => e.id === open)
  const kept = exceptionsShown(list ?? [], show, open)
  // The stale view keeps its own sort, by last matched, never first.
  const main = useSorted('mycroft-exceptions', EXCEPTION_SORT, kept, { col: 'updated', dir: 'desc' }, now)
  const staleSort = useSorted('mycroft-exceptions-stale', EXCEPTION_SORT, kept, { col: 'lastMatched', dir: 'asc' }, now)
  const { sort, onSort, shown } = show === 'stale' ? staleSort : main
  const th = (col: string, label: string) => <SortTh col={col} label={label} sort={sort} onSort={onSort} />
  const stale = (list ?? []).filter((e) => e.state === 'active' && e.stale).length
  const ref = useScrollTo(open, !!list)
  const remove = async () => {
    if (!removing) return
    setBusy(true)
    setRemoveError('')
    try {
      await removeException(removing.id)
      onToast?.(`${removing.id} removed: the agents report it again.`)
      setRemoving(null)
      load()
    } catch (e) {
      setRemoveError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const reactivate = async () => {
    if (!reviving) return
    setBusy(true)
    setRemoveError('')
    try {
      await reactivateException(reviving.id)
      onToast?.(`${reviving.id} is active again: the agents leave it out again.`)
      setReviving(null)
      load()
    } catch (e) {
      setRemoveError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <MycroftPage
      testId="knowledge-exceptions"
      lead="Issues that have been accepted as-is. While an exception is active, agents stop creating cases for that object. Closing a case as an exception will create a new exception."
      filter={
        <div className="rk-seg" role="group" aria-label="Show" data-testid="exceptions-show">
          {SHOW.map(([v, label, tip]) => (
            <button key={v} aria-pressed={show === v} title={tip} onClick={() => setShow(v)} data-testid={`exceptions-show-${v}`}>
              {label}
              {v === 'stale' && list ? <span style={{ color: 'var(--fg3)', fontWeight: 400 }}> {stale}</span> : null}
            </button>
          ))}
        </div>
      }
      action={
        may && (
          <button className="rk-btn" data-on onClick={() => setAdding(true)} data-testid="exceptions-new">
            New exception
          </button>
        )
      }
    >
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
          {error}
        </p>
      )}
      {list === null && !error && <p className="rk-fhint">Loading…</p>}
      {open && list && !openOne && (
        <p className="rk-fhint" style={{ margin: 0 }}>
          There is no exception {open}.
        </p>
      )}
      {list && (
        <MycroftTable label="Exceptions" testId="exceptions-table">
          <thead>
            <tr>
              {th('id', 'Exception')}
              {th('title', 'Title')}
              {th('cluster', 'Cluster')}
              {th('object', 'Object')}
              {th('rule', 'Validation rule')}
              {th('state', 'State')}
              {th('reason', 'Justification')}
              {th('matches', 'Matches')}
              {th('lastMatched', 'Last matched')}
              {th('updated', 'Updated')}
              {may && <th aria-label="Tools" />}
            </tr>
          </thead>
          <tbody>
            {shown.length === 0 && (
              <EmptyRow cols={may ? 11 : 10} testId="exceptions-empty">
                {!list.length
                  ? 'No exceptions yet. Close an agent-found case as an exception to accept it as it is, or create one with New exception.'
                  : show === 'stale'
                    ? 'No stale exceptions: each matched lately.'
                    : show === 'expired'
                      ? 'No expired exceptions.'
                      : show === 'inactive'
                        ? 'No inactive exceptions.'
                        : 'No active exceptions.'}
              </EmptyRow>
            )}
            {shown.map((e) => {
              const [label, tone] = STATE[e.state] ?? [e.state, 'muted']
              const isOpen = e.id === open
              return (
                <tr
                  key={e.id}
                  ref={isOpen ? (el) => void (ref.current = el) : undefined}
                  className="cs-row"
                  aria-current={isOpen ? 'true' : undefined}
                  aria-selected={isOpen}
                  onClick={() => navigate(exceptionHref(e.id))}
                  data-testid="exception"
                  data-id={e.id}
                >
                  <td>
                    <a className="rk-link rk-mono" href={exceptionHref(e.id)} onClick={(ev) => ev.stopPropagation()}>
                      {e.id}
                    </a>
                  </td>
                  <td>{e.title}</td>
                  <td>
                    <ClusterName e={e} />
                  </td>
                  <td onClick={(ev) => ev.stopPropagation()}>{e.resource ? <ObjectLink cluster={e.cluster ?? ''} resource={e.resource} /> : <span style={{ color: 'var(--fg3)' }}>cluster-wide</span>}</td>
                  <td style={{ maxWidth: 220 }}>
                    <Rule e={e} block />
                  </td>
                  <td>
                    <span style={{ display: 'inline-flex', gap: 6, flexWrap: 'wrap', alignItems: 'center' }}>
                      <span className="rk-pill" data-tone={tone} data-testid="exception-state">
                        {label}
                      </span>
                      {e.stale && e.state === 'active' && (
                        <span className="rk-pill" data-tone="warn" title="Never matched, or not lately (Settings › Cases)" data-testid="exception-stale">
                          Stale
                        </span>
                      )}
                      {e.state === 'removed' && e.duplicateOf && (
                        <span className="tb-sub" data-testid="exception-duplicate-of">
                          duplicate of{' '}
                          <a className="rk-link rk-mono" href={exceptionHref(e.duplicateOf)} onClick={(ev) => ev.stopPropagation()}>
                            {e.duplicateOf}
                          </a>
                        </span>
                      )}
                      {e.state === 'removed' && !e.duplicateOf && (
                        <span className="tb-sub" data-testid="exception-inactive-why">
                          {inactiveWhy(e)}
                        </span>
                      )}
                    </span>
                  </td>
                  <td style={{ maxWidth: 280 }}>{e.reason}</td>
                  <td data-testid="exception-matches">{matchesText(e)}</td>
                  <td>{e.lastMatched ? fmtTs(e.lastMatched) : '—'}</td>
                  <td data-testid="exception-updated">{fmtTs(updatedOf(e))}</td>
                  {may && (
                    <td>
                      {e.state === 'active' && (
                        <button
                          className="rk-btn"
                          data-size="sm"
                          onClick={(ev) => {
                            ev.stopPropagation()
                            setRemoving(e)
                          }}
                          data-testid="exception-remove"
                        >
                          Remove
                        </button>
                      )}
                      {mayReactivate(e, now) && (
                        <button
                          className="rk-btn"
                          data-size="sm"
                          onClick={(ev) => {
                            ev.stopPropagation()
                            setReviving(e)
                          }}
                          data-testid="exception-reactivate"
                        >
                          Make active again
                        </button>
                      )}
                    </td>
                  )}
                </tr>
              )
            })}
          </tbody>
        </MycroftTable>
      )}
      {openOne && <ExceptionDrawer e={openOne} may={may} now={now} onRemove={() => setRemoving(openOne)} onReactivate={() => setReviving(openOne)} onClose={() => navigate(LIST)} />}
      {removing && (
        <ConfirmDialog title={`Remove ${removing.id}?`} confirm="Remove" danger busy={busy} error={removeError} onCancel={() => setRemoving(null)} onConfirm={() => void remove()}>
          <p style={{ margin: 0 }}>The agents report “{removing.title}” again: the next run that sees it opens a case. The exception stays listed as inactive, turned off by you; it can be made active again.</p>
        </ConfirmDialog>
      )}
      {reviving && (
        <ConfirmDialog
          title={`Make ${reviving.id} active again?`}
          confirm="Make active again"
          busy={busy}
          error={removeError}
          onCancel={() => {
            setReviving(null)
            setRemoveError('')
          }}
          onConfirm={() => void reactivate()}
        >
          <p style={{ margin: 0 }}>
            The agents stop reporting “{reviving.title}” again{reviving.resource ? ` on ${reviving.resource}` : ' on the cluster'}. Refused while another active exception covers the same cluster, object and validation rule.
          </p>
        </ConfirmDialog>
      )}
      {adding && (
        <NewExceptionDrawer
          clusters={clusters}
          onClose={() => setAdding(false)}
          onCreated={(e) => {
            setAdding(false)
            onToast?.(`${e.id} is created: the agents stop reporting it.`)
            load()
            navigate(exceptionHref(e.id))
          }}
        />
      )}
    </MycroftPage>
  )
}

/** One exception, whole: its justification, cluster and object, the validation rule and what it means, how often it matched, who created it and why it is inactive, and where it came from. */
function ExceptionDrawer({ e, may, now, onRemove, onReactivate, onClose }: { e: AcceptedException; may: boolean; now: number; onRemove: () => void; onReactivate: () => void; onClose: () => void }) {
  const [label, tone] = STATE[e.state] ?? [e.state, 'muted']
  const footer =
    may && e.state === 'active' ? (
      <button className="rk-btn" data-danger="true" onClick={onRemove} data-testid="exception-drawer-remove">
        Remove
      </button>
    ) : may && mayReactivate(e, now) ? (
      <button className="rk-btn" data-on onClick={onReactivate} data-testid="exception-drawer-reactivate">
        Make active again
      </button>
    ) : undefined
  return (
    <Drawer
      label="Exception"
      kicker={`${e.id} · created ${fmtTs(e.at)}`}
      title={e.title}
      copy={false}
      onClose={onClose}
      width={720}
      testId="exception-drawer"
      footer={footer}
      facts={
        <span className="rk-pill" data-tone={tone} data-testid="exception-drawer-state">
          {label}
        </span>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="exception-details">
        <Line label="Justification">{e.reason}</Line>
        <Line label="Cluster">
          <ClusterName e={e} />
        </Line>
        <Line label="Object">{e.resource ? <ObjectLink cluster={e.cluster ?? ''} resource={e.resource} /> : 'cluster-wide'}</Line>
        <Line label="Validation rule">
          <Rule e={e} />
        </Line>
        <Line label="Matched">{e.matches ? `${matchesText(e)} · last ${fmtTs(e.lastMatched)}` : 'never'}</Line>
        <Line label="Created">
          by {e.by}, {fmtTs(e.at)}
          {e.expires ? ` · last day ${e.expires.slice(0, 10)}` : ' · permanent'}
        </Line>
        {e.state === 'removed' && (
          <Line label="Inactive">
            <span data-testid="exception-drawer-inactive">
              {e.duplicateOf ? (
                <>
                  duplicate of{' '}
                  <a className="rk-link rk-mono" href={exceptionHref(e.duplicateOf)}>
                    {e.duplicateOf}
                  </a>
                </>
              ) : (
                inactiveWhy(e)
              )}
              {e.removedAt ? `, ${fmtTs(e.removedAt)}` : ''}
            </span>
          </Line>
        )}
        <Line label="Updated">{fmtTs(updatedOf(e))}</Line>
        {(e.case || (e.finding && e.run)) && (
          <Line label="From">
            {e.case ? (
              <a className="rk-link" href={caseHref(e.case)} data-testid="exception-case">
                case {e.case}
              </a>
            ) : null}
            {e.run ? (
              <>
                {e.case ? ' · ' : ''}
                <a className="rk-link" href={runHref(e.run)}>
                  run {e.run}
                  {e.job ? ` (${e.job})` : ''}
                </a>
              </>
            ) : null}
          </Line>
        )}
        {e.detail && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>{e.detail}</p>}
      </div>
    </Drawer>
  )
}
