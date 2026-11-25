import { useCallback, useEffect, useMemo, useState } from 'react'
import { fetchRuns, HEALTH_LABEL, healthTone, mergedNote, runStatus, usd, type RunBrief } from '../../api/jobs'
import { useShowCosts } from '../../lib/costs'
import { fmtTs } from '../../lib/format'
import { navigate, type Route } from '../../lib/router'
import type { ColumnDef, Term } from '../../lib/table'
import { useTable } from '../../lib/useTable'
import { SortTh } from '../automation/SortTh'
import { ColumnsMenu } from '../ColumnsMenu'
import { Drawer } from '../Drawer'
import { ExportMenu } from '../ExportMenu'
import { Select } from '../Select'
import { pathId, runHref, runsHref, scheduleHref } from './jobsRoute'
import { refLink, RunDetail } from './RunDetail'
import { runEffort, runModel, SaidCell, saidText } from './modelEffort'
import { useRunNames, type RunNames } from './runNames'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const STATUSES: [string, string][] = [
  ['', 'Any status'],
  ['ok', 'Done'],
  ['running', 'Running'],
  ['failed', 'Failed'],
  ['budget', 'Budget spent'],
  ['skipped', 'Skipped'],
  ['missed', 'Missed'],
  ['interrupted', 'Interrupted'],
  ['cancelled', 'Cancelled'],
  ['noAccess', 'Cannot read the cluster'],
]

/** A run's status, as a pill: a run done takes the tone of what it found, "Done · 2 critical" (JR-01); "Done, with errors" when a cluster did not answer (W-2). */
export function StatusPill({ run, testId }: { run: Pick<RunBrief, 'status' | 'health' | 'severities' | 'failedClusters'>; testId?: string }) {
  const [label, tone] = runStatus(run)
  return (
    <span className="rk-pill" data-tone={tone} data-testid={testId} data-status={run.status}>
      {label}
    </span>
  )
}

/** A run's health, as a pill: critical red, warning amber, info or nothing found green, failed red; a dash while it runs (JR-01, JR-03). */
export function HealthPill({ run }: { run: Pick<RunBrief, 'health'> }) {
  if (!run.health) return <span style={{ color: 'var(--fg3)' }}>—</span>
  return (
    <span className="rk-pill" data-tone={healthTone(run.health)} data-testid="run-health-cell" data-health={run.health}>
      {HEALTH_LABEL[run.health]}
    </span>
  )
}

/** What a run came to, in a few words: its findings and proposals, or why it did not run. */
export function runSummary(r: RunBrief): string {
  if (r.status === 'running') return 'running'
  if (r.status !== 'ok' && r.note) return mergedNote(r.note)
  const parts: string[] = []
  if (r.findings) parts.push(`${r.findings} finding${r.findings === 1 ? '' : 's'}`)
  if (r.proposals) parts.push(`${r.proposals} proposed change${r.proposals === 1 ? '' : 's'}`)
  // A change it made that was not filed is not in the Inbox: said apart.
  if (r.notFiled) parts.push(`${r.notFiled} proposed change${r.notFiled === 1 ? '' : 's'} not filed`)
  return parts.length ? parts.join(', ') : 'report'
}

/**
 * AIOps › Jobs › Runs (Phase 12, JB-16, FX-13, JR-03): every run of a
 * job, scheduled or started by an event or by hand, newest first, with
 * when it started and ended, its status and health (the worst of what it
 * found), its result and cost; one opens in a drawer at
 * #/aiops/jobs/runs/<id>.
 */
export function RunsTab({ route, onToast }: { route: Route; onToast?: (msg: string) => void }) {
  const open = pathId(route.path, 'runs')
  const [status, setStatus] = useState('')
  const [mine, setMine] = useState(false)
  const [runs, setRuns] = useState<RunBrief[] | null>(null)
  const costs = useShowCosts()
  const names = useRunNames()
  const [total, setTotal] = useState(0)
  const [error, setError] = useState('')
  const [now] = useState(() => Date.now())
  const columns = useMemo(() => runColumns(costs, names), [costs, names])
  const rows = useMemo(() => runs ?? [], [runs])
  const table = useTable<RunBrief>({ id: 'aiops-runs', columns, rows, terms: runTerms, now, defaultSort: { col: 'started', dir: 'desc' } })
  const load = useCallback(
    (background = false) => {
      fetchRuns({ status, mine, limit: 200 }, background).then(
        (r) => {
          setRuns(r.runs ?? [])
          setTotal(r.total)
          setError('')
        },
        (e: unknown) => setError(errorText(e)),
      )
    },
    [status, mine],
  )
  useEffect(() => {
    load()
    const t = setInterval(() => load(true), 15000)
    return () => clearInterval(t)
  }, [load])
  return (
    <section className="rk-card" style={{ padding: '20px 24px 24px', display: 'flex', flexDirection: 'column', gap: 14 }} aria-labelledby="runs-h" data-testid="runs-tab">
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10 }}>
        <h2 id="runs-h" style={{ margin: 0, fontSize: 17, fontWeight: 600, flex: '1 1 auto' }}>
          Runs
        </h2>
        <Select label="Status" value={status} options={STATUSES} onChange={setStatus} testId="runs-status" />
        <label className="rk-tcheck" style={{ color: 'var(--fg)' }}>
          <input type="checkbox" checked={mine} onChange={(e) => setMine(e.target.checked)} data-testid="runs-mine" /> Jobs I set
        </label>
      </div>
      <p style={{ margin: 0, color: 'var(--fg2)', lineHeight: 1.5, fontSize: 13 }}>
        Each run of a job: what started it, the agent's report, what it found and proposed, and what it cost. Runs are read-only; a change one proposes waits in the Inbox. The oldest runs are let go as new ones come.
      </p>
      {error && (
        <p className="rk-t" data-tone="err" style={{ margin: 0 }}>
          {error}
        </p>
      )}
      {runs && runs.length === 0 && !error && (
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="runs-empty">
          No runs yet. Jobs are set in{' '}
          <a className="rk-link" href={scheduleHref}>
            Jobs › Schedule
          </a>
          .
        </p>
      )}
      {runs && runs.length > 0 && (
        <>
          {/* Columns and Export above the table they act on, as every table's (CF-66). */}
          <div className="rk-tools" data-testid="runs-table-tools">
            <ColumnsMenu cols={columns} hidden={table.hidden} defaults={table.defaults} onChange={table.setHidden} compact={false} />
            <ExportMenu onExport={(fmt) => table.exportRows(fmt, undefined, 'runs', onToast ?? (() => undefined))} disabled={!table.shown.length} what="the runs in view" />
          </div>
          <div style={{ overflowX: 'auto' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse', minWidth: 760, fontSize: 13 }} data-testid="runs-table">
              <thead>
                <tr>
                  {table.cols.map((c) => (
                    <SortTh key={c.id} col={c.id} label={c.label} sort={table.sort} onSort={table.onSort} />
                  ))}
                </tr>
              </thead>
              <tbody>
                {table.shown.map((r) => (
                  <tr key={r.id} style={{ background: r.id === open ? 'var(--accent-weak)' : undefined, cursor: 'pointer' }} onClick={() => navigate(runHref(r.id))} data-testid="run-row">
                    {table.cols.map((c) => (
                      <td key={c.id} style={td}>
                        {c.cell(r, now)}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
            {total > runs.length && (
              <p className="rk-fhint">
                The newest {runs.length} of {total} runs.
              </p>
            )}
          </div>
        </>
      )}
      {open && (
        <Drawer label="Run" kicker="Run" title={runs?.find((r) => r.id === open)?.label ?? open} facts={open} onClose={() => navigate(runsHref)} width={860} copy={false} testId="run-drawer">
          <RunDetail id={open} onToast={onToast} />
        </Drawer>
      )}
    </section>
  )
}

const td: React.CSSProperties = { padding: '9px 10px', borderBottom: '1px solid var(--line)', verticalAlign: 'top' }

const time = (t?: string) => (t ? Date.parse(t) : null)
const runTerms = (r: RunBrief): Term[] => [{ kind: 'job', text: r.label }]

/**
 * The Runs table's columns: each sorts, exports and may be hidden in the
 * Columns menu, as every table's (X-13). Model and Effort say what the run
 * ran with (the owner, November 23; AI-48): the model on its provider and
 * what it moved up from, the effort with Auto's reason.
 */
export function runColumns(costs: boolean, names: RunNames): ColumnDef<RunBrief>[] {
  const cols: ColumnDef<RunBrief>[] = [
    {
      id: 'started',
      label: 'Started',
      width: '',
      min: 140,
      defaultDir: 'desc',
      sortValue: (r) => time(r.started),
      cell: (r) => (
        <a className="rk-link" href={runHref(r.id)} onClick={(e) => e.stopPropagation()}>
          {fmtTs(r.started)}
        </a>
      ),
      export: (r) => [{ head: 'Started', cell: r.started }],
    },
    {
      id: 'ended',
      label: 'Ended',
      width: '',
      min: 140,
      defaultDir: 'desc',
      sortValue: (r) => time(r.ended),
      cell: (r) => (
        <span style={{ color: r.ended ? undefined : 'var(--fg3)' }} data-testid="run-ended">
          {r.ended ? fmtTs(r.ended) : r.status === 'running' ? 'running' : '—'}
        </span>
      ),
      export: (r) => [{ head: 'Ended', cell: r.ended ?? '' }],
    },
    {
      id: 'job',
      label: 'Job',
      width: '',
      min: 160,
      sortValue: (r) => r.label,
      cell: (r) => (
        <>
          <strong>{r.label}</strong>
          <br />
          <span style={{ color: 'var(--fg2)', fontSize: 12 }}>
            {/* The agent alone: the Model and Effort columns say what it ran with. */}
            <span data-testid="run-agent-line">{r.agent ? names.agent(r.agent) : 'no agent'}</span>
            {r.clusters?.length ? ` · ${r.clusters.map(names.cluster).join(', ')}` : ''}
          </span>
        </>
      ),
      export: (r) => [
        { head: 'Job', cell: r.label },
        { head: 'Agent', cell: r.agent ? names.agent(r.agent) : '' },
        { head: 'Clusters', cell: (r.clusters ?? []).map(names.cluster).join(', ') },
      ],
    },
    {
      id: 'model',
      label: 'Model',
      hint: 'The model the run ran on, on its provider, and the model it moved up from',
      width: '',
      min: 130,
      sortValue: (r) => runModel(r).text.replace('—', ''),
      cell: (r) => <SaidCell said={runModel(r)} testId="run-model" />,
      export: (r) => [
        { head: 'Model', cell: r.model ?? '' },
        { head: 'Provider', cell: r.provider ?? '' },
        { head: 'Moved up from', cell: r.movedUp?.from ?? '' },
      ],
    },
    {
      id: 'effort',
      label: 'Effort',
      hint: 'The highest effort a step ran at, and why Auto picked it',
      width: '',
      min: 90,
      sortValue: (r) => r.effort ?? '',
      cell: (r) => <SaidCell said={runEffort(r)} testId="run-effort-cell" />,
      export: (r) => [{ head: 'Effort', cell: saidText(runEffort(r)) }],
    },
    {
      id: 'by',
      label: 'Started by',
      width: '',
      min: 160,
      sortValue: (r) => r.trigger.what,
      cell: (r) => {
        const ref = refLink(r.ref)
        return (
          <>
            {r.trigger.what}
            {r.repeats ? <span style={{ color: 'var(--fg3)' }}> · {r.repeats} repeats</span> : null}
            {ref && (
              <>
                {' · '}
                <a className="rk-link" href={ref.href} onClick={(e) => e.stopPropagation()}>
                  {ref.label}
                </a>
              </>
            )}
          </>
        )
      },
      export: (r) => [{ head: 'Started by', cell: r.trigger.what }],
    },
    {
      id: 'status',
      label: 'Status',
      width: '',
      min: 100,
      sortValue: (r) => runStatus(r)[0],
      cell: (r) => <StatusPill run={r} testId="run-status-cell" />,
      export: (r) => [{ head: 'Status', cell: runStatus(r)[0] }],
    },
    {
      id: 'health',
      label: 'Health',
      width: '',
      min: 90,
      sortValue: (r) => (r.health ? HEALTH_LABEL[r.health] : null),
      cell: (r) => <HealthPill run={r} />,
      export: (r) => [{ head: 'Health', cell: r.health ? HEALTH_LABEL[r.health] : '' }],
    },
    {
      id: 'result',
      label: 'Result',
      width: '',
      min: 160,
      sortValue: (r) => runSummary(r),
      cell: (r) => <span style={{ color: r.status === 'ok' ? undefined : 'var(--fg2)' }}>{runSummary(r)}</span>,
      export: (r) => [{ head: 'Result', cell: runSummary(r) }],
    },
  ]
  if (costs)
    cols.push({
      id: 'cost',
      label: 'Cost',
      width: '',
      min: 70,
      right: true,
      defaultDir: 'desc',
      sortValue: (r) => r.costUSD,
      cell: (r) => usd(r.costUSD),
      export: (r) => [{ head: 'Cost (US$)', cell: r.costUSD }],
    })
  return cols
}
