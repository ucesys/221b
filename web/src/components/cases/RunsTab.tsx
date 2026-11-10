import { useCallback, useEffect, useState } from 'react'
import { fetchRuns, HEALTH_LABEL, healthTone, mergedNote, runStatus, usd, type RunBrief } from '../../api/jobs'
import { useShowCosts } from '../../lib/costs'
import { fmtTs } from '../../lib/format'
import { navigate, type Route } from '../../lib/router'
import { Drawer } from '../Drawer'
import { Select } from '../Select'
import { pathId, runHref, runsHref, scheduleHref } from './jobsRoute'
import { refLink, RunDetail } from './RunDetail'

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
]

/** A run's status, as a pill: a run done takes the tone of what it found, "Done · 2 critical" (JR-01). */
export function StatusPill({ run, testId }: { run: Pick<RunBrief, 'status' | 'health' | 'severities'>; testId?: string }) {
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
  if (r.proposals) parts.push(`${r.proposals} proposal${r.proposals === 1 ? '' : 's'}`)
  return parts.length ? parts.join(', ') : 'report'
}

/**
 * AIOps › Jobs › Runs (Phase 12, JB-16, FX-13, JR-03): every run of a
 * job, scheduled or started by an event or by hand, newest first, with
 * when it started and ended, its status and health (the worst of what it
 * found), its result and cost; one opens in a drawer at
 * #/cases/jobs/runs/<id>.
 */
export function RunsTab({ route, onToast }: { route: Route; onToast?: (msg: string) => void }) {
  const open = pathId(route.path, 'runs')
  const [status, setStatus] = useState('')
  const [mine, setMine] = useState(false)
  const [runs, setRuns] = useState<RunBrief[] | null>(null)
  const costs = useShowCosts()
  const [total, setTotal] = useState(0)
  const [error, setError] = useState('')
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
        <div style={{ overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', minWidth: 760, fontSize: 13 }} data-testid="runs-table">
            <thead>
              <tr>
                {['Started', 'Ended', 'Job', 'Started by', 'Status', 'Health', 'Result', ...(costs ? ['Cost'] : [])].map((h) => (
                  <th key={h} style={{ fontSize: 12, color: 'var(--fg2)', fontWeight: 500, textAlign: 'left', padding: '8px 10px', borderBottom: '1px solid var(--line)' }}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {runs.map((r) => {
                const ref = refLink(r.ref)
                return (
                  <tr key={r.id} style={{ background: r.id === open ? 'var(--accent-weak)' : undefined, cursor: 'pointer' }} onClick={() => navigate(runHref(r.id))} data-testid="run-row">
                    <td style={td}>
                      <a className="rk-link" href={runHref(r.id)} onClick={(e) => e.stopPropagation()}>
                        {fmtTs(r.started)}
                      </a>
                    </td>
                    <td style={{ ...td, color: r.ended ? undefined : 'var(--fg3)' }} data-testid="run-ended">
                      {r.ended ? fmtTs(r.ended) : r.status === 'running' ? 'running' : '—'}
                    </td>
                    <td style={td}>
                      <strong>{r.label}</strong>
                      <br />
                      <span style={{ color: 'var(--fg2)', fontSize: 12 }}>
                        {r.agent ?? 'no agent'}
                        {r.clusters?.length ? ` · ${r.clusters.join(', ')}` : ''}
                      </span>
                    </td>
                    <td style={td}>
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
                    </td>
                    <td style={td}>
                      <StatusPill run={r} testId="run-status-cell" />
                    </td>
                    <td style={td}>
                      <HealthPill run={r} />
                    </td>
                    <td style={{ ...td, color: r.status === 'ok' ? undefined : 'var(--fg2)' }}>{runSummary(r)}</td>
                    {costs && <td style={td}>{usd(r.costUSD)}</td>}
                  </tr>
                )
              })}
            </tbody>
          </table>
          {total > runs.length && <p className="rk-fhint">The newest {runs.length} of {total} runs.</p>}
        </div>
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
