// Run History and a run's drawer: its nodes, their output and what is left to do.
import { useEffect, useRef, useState } from 'react'
import { boundTo, cancelRun, countsText, fetchRun, isWaiting, profileName, NODE_LABEL, nodeTone, requestTone, runTone, retryRun, type HistoryEntry, type NodeState, type Run, type RunDetail, type WaitingRun } from '../../api/automation'
import { fmtTs } from '../../lib/format'
import { useSorted, type SortCol } from '../../lib/useSorted'
import { SortTh } from './SortTh'
import { ConfirmDialog } from '../ConfirmDialog'
import { Drawer } from '../Drawer'
import { SearchIcon } from '../Icons'
import { Select } from '../Select'
import { runToolAction } from '../../api/toolbox'
import { errText, Pill } from './parts'
import { ConstantBadge, EmptyChip, VariableChip } from './ValueTag'

/** A run's nodes counted by state. */
export function tally(r: Run): Partial<Record<NodeState, number>> {
  const m: Partial<Record<NodeState, number>> = {}
  for (const n of r.nodes) m[n.state] = (m[n.state] ?? 0) + 1
  return m
}

export function duration(from: string, to: string | undefined, now: number): string {
  const s = Math.max(0, Math.round(((to ? new Date(to).getTime() : now) - new Date(from).getTime()) / 1000))
  return s < 60 ? `${s} s` : s < 3600 ? `${Math.floor(s / 60)} min ${s % 60} s` : `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`
}

export function RunDrawer({ id, now, onRetried, onClose, onToast, onEnded }: { id: number; now: number; onRetried: (r: Run) => void; onClose: () => void; onToast: (msg: string) => void; onEnded: () => void }) {
  const [d, setD] = useState<RunDetail | null>(null)
  const [err, setErr] = useState('')
  const [node, setNode] = useState<string | null>(null)
  const [confirm, setConfirm] = useState(false)
  const [busy, setBusy] = useState(false)
  const wasLive = useRef(false)
  const ended = useRef(onEnded)
  useEffect(() => {
    ended.current = onEnded
  })
  const live = d?.live ?? true

  useEffect(() => {
    let gone = false
    const get = (background: boolean) =>
      fetchRun(id, background)
        .then((x) => {
          if (gone) return
          setD(x)
          setErr('')
          if (wasLive.current && !x.live) ended.current()
          wasLive.current = x.live
        })
        .catch((e: unknown) => !gone && setErr(errText(e)))
    void get(false)
    const t = live ? setInterval(() => void get(true), 2000) : undefined
    return () => {
      gone = true
      clearInterval(t)
    }
  }, [id, live])

  const run = d?.run
  const sel = node ?? run?.nodes.find((n) => n.state === 'failed' || n.state === 'running')?.node ?? run?.nodes[0]?.node
  const out = sel ? (d?.output?.[sel] ?? run?.nodes.find((n) => n.node === sel)?.tail ?? '') : ''
  const params = Object.entries(run?.nodes.find((n) => n.node === sel)?.values ?? {})
  const failedNodes = (run?.nodes ?? []).filter((n) => n.state === 'failed' || n.state === 'unreachable').length
  return (
    <>
      <Drawer
        label="Run"
        kicker={run ? `${run.dryRun ? 'Dry run' : 'Run'} on ${run.clusterName || run.cluster} › ${run.fleetName || run.fleet}` : 'Run'}
        title={run ? `#${run.id} ${run.script} v${run.version}` : `#${id}`}
        copy={false}
        width={1400}
        onClose={onClose}
        testId="run-drawer"
        tools={
          d?.live ? (
            <button className="rk-btn" data-danger="true" onClick={() => setConfirm(true)} data-testid="run-cancel">
              Cancel run
            </button>
          ) : run && failedNodes > 0 ? (
            <button
              className="rk-btn"
              onClick={() =>
                void retryRun(run.cluster, run.id).then(
                  (r) => (isWaiting(r) ? onToast(`Run request ${r.waiting.id} waits for another admin’s approval of ${r.waiting.changed.join(', ')}: it is in AIOps › Inbox.`) : onRetried(r)),
                  (e: unknown) => setErr(errText(e)),
                )
              }
              data-testid="run-retry"
            >
              Retry {failedNodes} failed
            </button>
          ) : undefined
        }
        facts={
          run ? (
            <span
              style={{
                display: 'flex',
                gap: 8,
                flexWrap: 'wrap',
                alignItems: 'center',
                fontSize: 12.5,
              }}
            >
              <Pill tone={runTone(run.state)} testId="run-state">
                {run.state}
              </Pill>
              <span>{countsText(tally(run))}</span>
              <span style={{ color: 'var(--fg3)' }}>
                by {run.by.name} · {fmtTs(run.started)} · {duration(run.started, run.ended, now)} · privilege {run.privilege} · rollout {run.rollout.mode}
                {run.rollout.size ? ` ${run.rollout.size}` : ''}
              </span>
            </span>
          ) : undefined
        }
        notice={
          run?.note ? (
            <p className="rk-callout" data-tone={run.state === 'succeeded' ? 'info' : 'warn'} style={{ margin: 0, fontSize: 12.5 }}>
              {run.note}
            </p>
          ) : undefined
        }
      >
        {err && <p className="tb-err">{err}</p>}
        {run && params.length > 0 && (
          <section
            style={{
              padding: '12px 16px',
              borderRadius: 8,
              border: '1px solid var(--line)',
              borderLeft: '3px solid var(--accent)',
              background: 'var(--surface)',
            }}
            data-testid="run-parameters"
          >
            <h3 className="rk-h" style={{ margin: '0 0 2px' }}>
              Parameters
            </h3>
            <p className="tb-hint" style={{ margin: '0 0 8px' }}>
              What this run gave the script{sel ? ` on ${sel}` : ''}.
            </p>
            <dl className="rk-kv">
              {params.map(([k, v]) => {
                // FU-04, FU-06: a value bound to a variable shows its chip; one given for the run its badge.
                const own = run.overrides?.[k]
                const bound = boundTo(own)
                return (
                  <div key={k} style={{ display: 'contents' }}>
                    <dt className="rk-mono">{k}</dt>
                    <dd className="rk-mono">
                      {v}
                      {own !== undefined && (
                        <span style={{ marginLeft: 8, display: 'inline-flex', verticalAlign: 'middle' }} data-testid="run-param-origin" data-bound={bound}>
                          {bound ? <VariableChip variable={bound} title={`The run gave each node its own value of ${bound}.`} /> : own === '' ? <EmptyChip kind="empty" name={k} /> : <ConstantBadge />}
                        </span>
                      )}
                    </dd>
                  </div>
                )
              })}
            </dl>
            {run.valuesApproved && (
              <p className="tb-hint" style={{ margin: '8px 0 0' }} data-testid="run-values-approved">
                The run’s own values were approved by {run.valuesApproved.by.name}, {fmtTs(run.valuesApproved.at)} (run request {run.valuesApproved.request}).
              </p>
            )}
          </section>
        )}
        {run && (
          <p className="tb-hint" style={{ margin: 0 }} data-testid="run-watson">
            Watson via {run.script} v{run.version}, run #{run.id}
            {run.basis ? `: ${run.basis}` : ''}
            {run.config ? ` · run as ${profileName(run.config)}` : ''}
            {run.identity ? ` · as ${run.identity}` : ''}
            {run.hooks?.length ? ` · ${run.hooks.join(', ')}` : ''}
            {run.retryOf ? ` · retries run ${run.retryOf}` : ''}
            {run.approvers?.length ? ` · approved by ${run.approvers.map((a) => a.name).join(', ')}` : ''}
          </p>
        )}
        {run && (
          <h3
            className="rk-h"
            style={{
              margin: 0,
              paddingTop: params.length > 0 ? 12 : 0,
              borderTop: params.length > 0 ? '1px solid var(--line)' : undefined,
            }}
            data-testid="run-results"
          >
            Results
          </h3>
        )}
        {run && (
          <div
            style={{
              display: 'flex',
              gap: 16,
              flexWrap: 'wrap',
              alignItems: 'flex-start',
            }}
          >
            <table className="tb-table" style={{ flex: '1 1 380px', width: 'auto' }} data-testid="run-nodes-table">
              <thead>
                <tr>
                  <th>Node</th>
                  <th>Batch</th>
                  <th>Result</th>
                  <th>Exit</th>
                </tr>
              </thead>
              <tbody>
                {run.nodes.map((n) => (
                  <tr
                    key={n.node}
                    data-sel={sel === n.node}
                    onClick={() => setNode(n.node)}
                    style={{
                      cursor: 'pointer',
                      background: sel === n.node ? 'var(--accent-weak)' : undefined,
                    }}
                    data-testid="run-node"
                  >
                    <td className="rk-mono">{n.node}</td>
                    <td>{n.batch}</td>
                    <td>
                      <Pill tone={nodeTone(n.state)}>{NODE_LABEL[n.state]}</Pill>
                      {(n.msg || n.error) && (
                        <div
                          style={{
                            fontSize: 12,
                            color: n.error ? 'var(--err)' : 'var(--fg2)',
                          }}
                        >
                          {n.error || n.msg}
                        </div>
                      )}
                      {n.cordoned && !live && (
                        <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, marginTop: 2 }} data-testid="run-node-cordoned">
                          <span style={{ color: 'var(--warn)' }}>left cordoned</span>
                          <button
                            className="rk-btn"
                            data-size="sm"
                            onClick={(e) => {
                              e.stopPropagation()
                              runToolAction(run.cluster, { action: 'uncordon', node: n.node }).then(
                                (it) =>
                                  onToast(
                                    it.status === 'refused'
                                      ? it.reason || 'Watson refused it.'
                                      : it.status === 'pending'
                                        ? `${it.command}: the card is in Watson. Read it and approve it there.`
                                        : `Uncordoning ${n.node}. Watson shows how it goes.`,
                                  ),
                                (err: unknown) => onToast(err instanceof Error ? err.message : String(err)),
                              )
                            }}
                            data-testid="run-node-uncordon"
                          >
                            Uncordon
                          </button>
                        </div>
                      )}
                    </td>
                    <td className="rk-mono">{n.exit ?? ''}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <div style={{ flex: '2 1 420px', minWidth: 0 }}>
              <h3 className="rk-h" style={{ margin: '0 0 6px' }}>
                Output{sel ? ` of ${sel}` : ''}
              </h3>
              <pre
                className="rk-mono"
                style={{
                  margin: 0,
                  padding: 12,
                  background: 'var(--sunken)',
                  borderRadius: 8,
                  fontSize: 12,
                  overflow: 'auto',
                  maxHeight: 480,
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-word',
                }}
                data-testid="run-output"
              >
                {out || 'No output.'}
              </pre>
            </div>
          </div>
        )}
      </Drawer>
      {confirm && (
        <ConfirmDialog
          title={`Cancel run ${id}?`}
          confirm="Cancel run"
          danger
          busy={busy}
          onCancel={() => setConfirm(false)}
          onConfirm={() => {
            setBusy(true)
            cancelRun(id)
              .then(() => {
                onToast(`Run ${id} is cancelled: nodes running stop, waiting nodes do not start.`)
                setConfirm(false)
              })
              .catch((e: unknown) => setErr(errText(e)))
              .finally(() => setBusy(false))
          }}
        >
          <p style={{ margin: 0 }}>Scripts running now get SIGTERM; no node that has not started will start. What a script already changed stays changed.</p>
        </ConfirmDialog>
      )}
    </>
  )
}

/** What an entry of Run History sorts and searches by: a run's own fields, or a request's. */
const entryScript = (e: HistoryEntry) => (e.kind === 'run' ? e.run.script : e.request.script)
const entryWhere = (e: HistoryEntry) => (e.kind === 'run' ? `${e.run.clusterName || e.run.cluster} › ${e.run.fleetName || e.run.fleet}` : `${e.request.clusterName || e.request.cluster} › ${e.request.fleetName || e.request.fleet}`)
const entryBy = (e: HistoryEntry) => (e.kind === 'run' ? e.run.by.name : e.request.by.name)
const entryNodes = (e: HistoryEntry) => (e.kind === 'run' ? e.run.nodes.length : e.request.targets.length)
const entryID = (e: HistoryEntry) => (e.kind === 'run' ? e.run.id : e.request.id)

export const RUN_SORT: SortCol<HistoryEntry>[] = [
  { id: 'id', sortValue: (e) => e.at, defaultDir: 'desc' },
  { id: 'script', sortValue: entryScript },
  { id: 'where', sortValue: entryWhere },
  { id: 'by', sortValue: entryBy },
  { id: 'state', sortValue: (e) => e.state },
  { id: 'nodes', sortValue: entryNodes, defaultDir: 'desc' },
  { id: 'started', sortValue: (e) => e.at, defaultDir: 'desc' },
  {
    id: 'took',
    sortValue: (e, now) => (e.kind === 'run' ? (e.run.ended ? Date.parse(e.run.ended) : now) - Date.parse(e.run.started) : -1),
    defaultDir: 'desc',
  },
]

/** The states Run History filters by (AU-24): the runs', then the requests'. */
const STATES: [string, string][] = [
  ['', 'Any state'],
  ['running', 'Running'],
  ['succeeded', 'Succeeded'],
  ['failed', 'Failed'],
  ['stopped', 'Stopped'],
  ['cancelled', 'Cancelled'],
  ['interrupted', 'Interrupted'],
  ['waiting', 'Waiting for approval'],
  ['rejected', 'Rejected'],
  ['withdrawn', 'Withdrawn'],
  ['expired', 'Expired'],
]

/** Where a waiting request is approved: its row in AIOps › Inbox. */
export const requestHref = (w: WaitingRun) => `#/aiops/inbox/approval/${encodeURIComponent(`run-${w.cluster}-${w.id}`)}`

/** What became of a request, in words: who rejected or withdrew it and why, or that it expired. */
export function requestText(e: Extract<HistoryEntry, { kind: 'request' }>): string {
  const w = e.request
  const why = w.comment ? `: ${w.comment}` : ''
  switch (e.state) {
    case 'waiting':
      return `waits for approval of ${w.changed.join(', ')}`
    case 'rejected':
      return `rejected by ${w.decidedBy?.name ?? 'someone'}${why}`
    case 'withdrawn':
      return `withdrawn by ${w.decidedBy?.name ?? w.by.name}${why}`
    case 'expired':
      return `expired${why}`
    case 'approved':
      return `approved by ${w.decidedBy?.name ?? 'someone'}${w.run ? `: run ${w.run}` : ''}`
  }
}

/**
 * Automation › Run History (AU-24, DB-37): every run and every request
 * for a run whose own values wait for approval, newest first, with a
 * filter by state; a rejected request says who rejected it and why, a
 * waiting one links to its approval.
 */
export function RunsSection({ entries, now, onOpen }: { entries: HistoryEntry[]; now: number; onOpen: (id: number) => void }) {
  const [q, setQ] = useState('')
  const [state, setState] = useState('')
  const lq = q.toLowerCase()
  const picked = entries.filter(
    (e) =>
      (!state || e.state === state) &&
      (!q || entryScript(e).includes(lq) || (e.kind === 'run' ? (e.run.fleetName ?? e.run.fleet) : (e.request.fleetName ?? e.request.fleet)).toLowerCase().includes(lq) || entryBy(e).toLowerCase().includes(lq) || String(entryID(e)) === q),
  )
  const { sort, onSort, shown } = useSorted('runs', RUN_SORT, picked, { col: 'id', dir: 'desc' }, now)
  const th = (col: string, label: string) => <SortTh col={col} label={label} sort={sort} onSort={onSort} />
  return (
    <>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 12,
          flexWrap: 'wrap',
        }}
      >
        <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>Run history</h2>
        <label className="rk-field" style={{ width: 260 }}>
          <SearchIcon />
          <span className="rk-sr">Find a run</span>
          <input type="search" placeholder="Find a script, fleet, person or #" value={q} onChange={(e) => setQ(e.target.value)} data-testid="runs-search" />
        </label>
        <Select label="State" value={state} options={STATES} onChange={setState} testId="runs-state" />
      </div>
      {entries.length === 0 ? (
        <p className="tb-hint" data-testid="runs-empty">
          Nothing has run yet. Dry run a script from Scripts to see what it would change.
        </p>
      ) : (
        <div className="rk-card" style={{ padding: 0, overflow: 'auto' }}>
          <table className="tb-table" data-testid="runs-table">
            <thead>
              <tr>
                {th('id', '#')}
                {th('script', 'Script')}
                {th('where', 'Where')}
                {th('by', 'By')}
                {th('state', 'State')}
                {th('nodes', 'Nodes')}
                {th('started', 'Started')}
                {th('took', 'Took')}
              </tr>
            </thead>
            <tbody>
              {shown.map((e) =>
                e.kind === 'run' ? (
                  <tr key={`run-${e.run.id}`} onClick={() => onOpen(e.run.id)} style={{ cursor: 'pointer' }} data-testid="run-row">
                    <td>
                      <button className="rk-link" onClick={() => onOpen(e.run.id)}>
                        {e.run.id}
                      </button>
                    </td>
                    <td>
                      <span className="rk-mono">{e.run.script}</span> v{e.run.version}
                      {e.run.dryRun && (
                        <>
                          {' '}
                          <Pill>dry run</Pill>
                        </>
                      )}
                    </td>
                    <td>{entryWhere(e)}</td>
                    <td>{e.run.by.name}</td>
                    <td>
                      <Pill tone={runTone(e.run.state)}>{e.run.state}</Pill>
                      {e.run.valuesApproved && (
                        <span className="tb-hint" style={{ display: 'block', fontSize: 12 }} data-testid="run-request-of">
                          request {e.run.valuesApproved.request}, approved by {e.run.valuesApproved.by.name}
                        </span>
                      )}
                    </td>
                    <td>{countsText(tally(e.run))}</td>
                    <td style={{ whiteSpace: 'nowrap' }}>{fmtTs(e.run.started)}</td>
                    <td style={{ whiteSpace: 'nowrap' }}>{duration(e.run.started, e.run.ended, now)}</td>
                  </tr>
                ) : (
                  <tr key={`request-${e.request.cluster}-${e.request.id}`} data-testid="run-request-row" data-state={e.state}>
                    <td style={{ whiteSpace: 'nowrap' }}>request {e.request.id}</td>
                    <td>
                      <span className="rk-mono">{e.request.script}</span> v{e.request.version}
                    </td>
                    <td>{entryWhere(e)}</td>
                    <td>{e.request.by.name}</td>
                    <td>
                      <Pill tone={requestTone(e.state)}>{e.state === 'waiting' ? 'waiting for approval' : e.state}</Pill>
                      <span className="tb-hint" style={{ display: 'block', fontSize: 12 }} data-testid="run-request-what">
                        {requestText(e)}
                        {e.state === 'waiting' && (
                          <>
                            {' · '}
                            <a className="rk-link" href={requestHref(e.request)} data-testid="run-request-approval">
                              its approval
                            </a>
                          </>
                        )}
                      </span>
                    </td>
                    <td>
                      {e.request.targets.length} {e.request.targets.length === 1 ? 'node' : 'nodes'}
                    </td>
                    <td style={{ whiteSpace: 'nowrap' }}>{fmtTs(e.request.at)}</td>
                    <td>—</td>
                  </tr>
                ),
              )}
            </tbody>
          </table>
          {shown.length === 0 && (
            <p className="tb-hint" style={{ padding: '10px 14px', margin: 0 }} data-testid="runs-none">
              Nothing in this state.
            </p>
          )}
        </div>
      )}
    </>
  )
}
