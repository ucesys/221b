import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { fetchTestRun, stopTest, type CaseExpect, type CaseResult, type CaseState, type RunState, type TestRun } from '../../api/ai'
import { usd } from '../../api/sherlock'
import { useShowCosts } from '../../lib/costs'
import { trapTab } from '../../lib/focus'
import { fmtTs } from '../../lib/format'
import { layer } from '../../lib/layer'
import { CloseIcon } from '../Icons'

interface Props {
  agentId: string
  agentName: string
  /** The draft's revision now, to say when the run is of an earlier one. */
  draftRev?: number
  onClose: () => void
  /** Called when a run seen in flight ends. */
  onEnded?: (run: TestRun) => void
}

/** How often a run in flight is read again. */
export const POLL_MS = 2000

const RUN_TEXT: Record<RunState, string> = { running: 'Running', done: 'Done', stopped: 'Stopped', failed: 'Failed' }
const RUN_TONE: Record<RunState, string> = { running: 'info', done: 'ok', stopped: 'warn', failed: 'err' }
// The state in words and a mark, never by colour alone.
const CASE_TEXT: Record<CaseState, string> = { waiting: 'Waiting', running: 'Running…', passed: '✓ Passed', failed: '✗ Failed' }
const CASE_TONE: Record<CaseState, string> = { waiting: 'muted', running: 'info', passed: 'ok', failed: 'err' }

/** A case of the run, as the dialog lists it. */
export interface CaseRow {
  key: string
  name: string
  base?: boolean
  state: CaseState
  result?: CaseResult
}

/**
 * The run's cases, the failed first, the rest in the order they run. Cases
 * run several at once (AI-25), so the results so far are those finished, in
 * the order of the cases: each finds its case by its id. A run kept before
 * Phase 12.1 lists only its results.
 */
export function caseRows(run: TestRun): CaseRow[] {
  const results = run.results ?? []
  const used = new Set<number>()
  const resultOf = (id: string) => {
    const i = results.findIndex((r, j) => !used.has(j) && r.id === id)
    if (i < 0) return undefined
    used.add(i)
    return results[i]
  }
  const rows: CaseRow[] = run.planned?.length
    ? run.planned.map((p, i) => ({ key: `${i}-${p.id}`, name: p.name, base: p.base, state: p.state, result: p.state === 'passed' || p.state === 'failed' ? resultOf(p.id) : undefined }))
    : results.map((r, i) => ({ key: `${i}-${r.id}`, name: r.name, base: r.base, state: r.passed ? 'passed' : 'failed', result: r }))
  return [...rows.filter((r) => r.state === 'failed'), ...rows.filter((r) => r.state !== 'failed')]
}

/** Where the run is, in words: "3 of 12 cases · 4 running · 2 of 10 real questions". Several cases run at once (AI-25). */
export function progressText(run: TestRun): string {
  const total = run.planned?.length ?? run.cases
  const parts = [`${run.cases} of ${total} case${total === 1 ? '' : 's'}`]
  const inFlight = run.state === 'running' ? (run.planned ?? []).filter((p) => p.state === 'running').length : 0
  if (inFlight) parts.push(`${inFlight} running`)
  const replays = run.replays?.length ?? 0
  if (run.replaysPlanned) parts.push(`${replays} of ${run.replaysPlanned} real question${run.replaysPlanned === 1 ? '' : 's'}`)
  else if (replays) parts.push(`${replays} real question${replays === 1 ? '' : 's'}`)
  return parts.join(' · ')
}

/**
 * A test run, large enough to read (Phase 12.1): while it runs, each
 * case's state as it changes, read again every two seconds, with a Stop;
 * after, the same read-only. Each case shows its question, what it
 * expected line by line, met or not, and what the agent did; the real
 * questions replayed show the earlier answer beside the draft's. Closing
 * it does not stop the run.
 */
export function TestDialog({ agentId, agentName, draftRev, onClose, onEnded }: Props) {
  const costs = useShowCosts()
  const [run, setRun] = useState<TestRun | null>(null)
  const [error, setError] = useState('')
  const [tab, setTab] = useState<'cases' | 'replays'>('cases')
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const [stopping, setStopping] = useState(false)
  const closeRef = useRef<HTMLButtonElement>(null)
  const ended = useRef(onEnded)
  useEffect(() => {
    ended.current = onEnded
  }, [onEnded])
  useEffect(() => {
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : null
    closeRef.current?.focus()
    return () => before?.focus()
  }, [])
  useEffect(() => {
    let live = true
    let timer = 0
    let seen = false
    const tick = () => {
      fetchTestRun(agentId).then(
        (r) => {
          if (!live) return
          setRun(r)
          setError('')
          if (r.state === 'running') {
            seen = true
            timer = window.setTimeout(tick, POLL_MS)
          } else if (seen) {
            seen = false
            ended.current?.(r)
          }
        },
        (e: unknown) => {
          if (!live) return
          setError(e instanceof Error ? e.message : String(e))
          if (seen) timer = window.setTimeout(tick, POLL_MS)
        },
      )
    }
    tick()
    return () => {
      live = false
      window.clearTimeout(timer)
    }
  }, [agentId])
  const state: RunState = run?.state ?? 'done'
  const running = state === 'running'
  const stop = () => {
    setStopping(true)
    stopTest(agentId).catch((e: unknown) => {
      setStopping(false)
      setError(e instanceof Error ? e.message : String(e))
    })
  }
  const rows = run ? caseRows(run) : []
  const total = run?.planned?.length ?? run?.cases ?? 0
  const replays = run?.replays ?? []
  const halt = (e: { stopPropagation: () => void }) => e.stopPropagation()
  return createPortal(
    <div style={{ position: 'fixed', inset: 0, zIndex: 60, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16, whiteSpace: 'normal' }} onClick={halt} onMouseDown={halt} onKeyDown={halt}>
      <button aria-label="Close the tests" tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="test-dialog-title"
        className="rk-card"
        data-testid="test-dialog"
        data-state={run ? state : 'loading'}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            onClose()
            return
          }
          trapTab(e)
        }}
        style={{ position: 'relative', width: 'min(1100px, 96vw)', maxHeight: '90vh', padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 10, boxShadow: 'var(--shadow)', minHeight: 0, overflowWrap: 'anywhere' }}
      >
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10 }}>
          <h2 id="test-dialog-title" style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>
            Tests of the {agentName}
          </h2>
          {run && (
            <span className="rk-pill" data-tone="muted">
              {run.rev ? `Draft ${run.rev}` : 'Instructions in force'}
            </span>
          )}
          {run && (
            <span className="rk-pill" data-tone={RUN_TONE[state]} data-testid="test-dialog-state" data-state={state}>
              {stopping && running ? 'Stopping…' : RUN_TEXT[state]}
            </span>
          )}
          <span style={{ flex: '1 1 auto' }} />
          {running && (
            <button className="rk-btn" onClick={stop} disabled={stopping} data-testid="test-dialog-stop">
              Stop
            </button>
          )}
          <button ref={closeRef} className="rk-icon-btn" aria-label="Close the tests" title={running ? 'Close (Esc); the tests go on' : 'Close (Esc)'} onClick={onClose} data-testid="test-dialog-close">
            <CloseIcon />
          </button>
        </div>
        <p className="rk-fhint" style={{ margin: 0, fontSize: 12.5 }} aria-live="polite" data-testid="test-dialog-progress">
          {run ? `${progressText(run)}${costs ? ` · ${usd(run.costUSD)} of about ${usd(run.estimate)}` : ''} · started ${fmtTs(run.at)}${run.by ? ` by ${run.by}` : ''}` : error ? '' : 'Reading the test run…'}
        </p>
        {run && running && <span className="rk-fhint">Closing this does not stop the tests; the drawer shows their progress.</span>}
        {run && run.rev > 0 && draftRev !== undefined && run.rev !== draftRev && (
          <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="test-dialog-earlier">
            This run tested draft {run.rev}, an earlier draft than the one saved now (draft {draftRev}). Run the tests again to test it.
          </p>
        )}
        {run && run.rev === 0 && (
          <p className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5 }} data-testid="test-dialog-earlier">
            This run tested the instructions in force, not a draft.
          </p>
        )}
        {run && state === 'stopped' && (
          <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="test-dialog-stopped">
            {run.stoppedAt ? `Stopped at case ${run.stoppedAt} of ${total}: the cases running then were cut short, and those still waiting did not run.` : 'Stopped while the real questions were replayed.'}
          </p>
        )}
        {run && state === 'failed' && (
          <p className="rk-callout" data-tone="err" style={{ margin: 0, fontSize: 12.5 }}>
            The run could not go on{run.stoppedAt ? ` at case ${run.stoppedAt}` : ''}: {run.error}
          </p>
        )}
        {error && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
            {error}
          </p>
        )}
        {run && (
          <>
            <div className="rk-seg" role="tablist" aria-label="What the run tested" style={{ alignSelf: 'flex-start' }}>
              <button type="button" role="tab" id="test-tab-cases" aria-controls="test-panel" aria-selected={tab === 'cases'} aria-pressed={tab === 'cases'} onClick={() => setTab('cases')} data-testid="test-dialog-tab-cases">
                Test cases ({total})
              </button>
              <button type="button" role="tab" id="test-tab-replays" aria-controls="test-panel" aria-selected={tab === 'replays'} aria-pressed={tab === 'replays'} onClick={() => setTab('replays')} data-testid="test-dialog-tab-replays">
                Real questions ({run.replaysPlanned || replays.length})
              </button>
            </div>
            <div id="test-panel" role="tabpanel" aria-labelledby={tab === 'cases' ? 'test-tab-cases' : 'test-tab-replays'} style={{ flex: '1 1 auto', minHeight: 0, overflow: 'auto', display: 'flex', flexDirection: 'column', gap: 6 }}>
              {tab === 'cases' ? (
                rows.length ? (
                  <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 6 }}>
                    {rows.map((row) => {
                      const expanded = open[row.key] ?? row.state === 'failed'
                      const id = `test-case-${row.key}`
                      return (
                        <li key={row.key} data-testid="test-case-row" data-state={row.state} style={{ border: '1px solid var(--line)', borderRadius: 8, padding: '6px 10px', fontSize: 12.5 }}>
                          <button
                            type="button"
                            className="rk-link"
                            aria-expanded={expanded}
                            aria-controls={id}
                            onClick={() => setOpen({ ...open, [row.key]: !expanded })}
                            style={{ display: 'flex', gap: 8, alignItems: 'center', width: '100%', textAlign: 'left', fontSize: 12.5, color: 'var(--fg)' }}
                            data-testid="test-case-toggle"
                          >
                            <span aria-hidden="true">{expanded ? '▾' : '▸'}</span>
                            <strong style={{ flex: '1 1 auto' }}>{row.name}</strong>
                            {row.base && <span className="rk-fhint">ships with the console</span>}
                            <span className="rk-pill" data-tone={CASE_TONE[row.state]} data-testid="test-case-state">
                              {CASE_TEXT[row.state]}
                            </span>
                          </button>
                          {expanded && (
                            <div id={id} style={{ paddingTop: 6 }}>
                              <CaseDetail result={row.result} />
                            </div>
                          )}
                        </li>
                      )
                    })}
                  </ul>
                ) : (
                  <p className="rk-fhint" style={{ margin: 0 }}>
                    This agent has no test cases.
                  </p>
                )
              ) : (
                <Replays run={run} />
              )}
            </div>
          </>
        )}
      </div>
    </div>,
    layer(),
  )
}

function Part({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <span style={{ fontWeight: 600 }}>{title}</span>
      {children}
    </div>
  )
}

const pre = { margin: 0, padding: '6px 8px', fontSize: 12, whiteSpace: 'pre-wrap', background: 'var(--sunken)', borderRadius: 8, border: '1px solid var(--line)' } as const

/** What a case expected, for a run kept before its checks were. */
function expectLines(e?: CaseExpect): string[] {
  if (!e) return []
  return [...(e.tools ?? []).map((t) => `calls ${t}`), ...(e.handOvers ?? []).map((h) => `hands over to ${h}`), ...(e.refuse ? ['refuses'] : []), ...(e.points ?? []).map((p) => `makes the point: ${p}`)]
}

/** One case: its question, what it expected line by line, what the agent did, and why it failed. */
function CaseDetail({ result }: { result?: CaseResult }) {
  if (!result)
    return (
      <p className="rk-fhint" style={{ margin: 0 }}>
        Not answered yet.
      </p>
    )
  const secrets = result.expect?.secrets ?? 0
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(300px, 100%), 1fr))', gap: 10 }} data-testid="test-case-detail">
      <Part title="Question">
        <pre className="rk-mono" style={pre}>
          {result.question || '(not kept with this run)'}
        </pre>
      </Part>
      <Part title="Expected">
        {result.checks?.length ? (
          <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 2 }} data-testid="test-case-checks">
            {result.checks.map((c, i) => (
              <li key={i} data-ok={c.ok ? 'true' : 'false'} style={{ display: 'flex', gap: 6 }}>
                <span aria-hidden="true" style={{ color: c.ok ? 'var(--ok)' : 'var(--err)', fontWeight: 700 }}>
                  {c.ok ? '✓' : '✗'}
                </span>
                <span className="rk-sr">{c.ok ? 'Met: ' : 'Not met: '}</span>
                <span>{c.what}</span>
              </li>
            ))}
          </ul>
        ) : (
          <ul style={{ margin: 0, paddingLeft: 18 }}>
            {expectLines(result.expect).map((l) => (
              <li key={l}>{l}</li>
            ))}
            <li>no credential-looking text</li>
          </ul>
        )}
        {secrets > 0 && (
          <span className="rk-fhint">
            {secrets} planted secret{secrets === 1 ? '' : 's'} in the recorded results, never shown.
          </span>
        )}
      </Part>
      <Part title="Actual">
        <pre style={pre} data-testid="test-case-answer">
          {result.answer || '(no answer)'}
        </pre>
        {result.answer.endsWith('…') && <span className="rk-fhint">Shortened.</span>}
        <span>Tools called: {result.tools?.length ? result.tools.join(', ') : 'none'}</span>
        <span>Hand-overs: {result.handOvers?.length ? result.handOvers.join(', ') : 'none'}</span>
      </Part>
      {!!result.why?.length && (
        <Part title="Why it failed">
          <ul style={{ margin: 0, paddingLeft: 18 }} data-testid="test-case-why">
            {result.why.map((w, i) => (
              <li key={i}>{w}</li>
            ))}
          </ul>
        </Part>
      )}
    </div>
  )
}

/** The real questions replayed: the earlier answer beside the draft's. */
function Replays({ run }: { run: TestRun }) {
  const list = run.replays ?? []
  const left = (run.replaysPlanned ?? 0) - list.length
  return (
    <>
      <span className="rk-fhint">The latest real questions, answered again with the tool results recorded then. The same means both answers cite the same objects.</span>
      {!list.length && !left && (
        <p className="rk-fhint" style={{ margin: 0 }}>
          No real question was replayed in this run.
        </p>
      )}
      <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 8 }}>
        {list.map((r, i) => (
          <li key={i} data-testid="test-replay" data-same={r.same ? 'true' : 'false'} style={{ border: '1px solid var(--line)', borderRadius: 8, padding: '6px 10px', fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
              <strong style={{ flex: '1 1 auto' }}>{r.question}</strong>
              <span className="rk-fhint">{r.cluster}</span>
              <span className="rk-pill" data-tone={r.error ? 'err' : r.same ? 'ok' : 'warn'} data-testid="test-replay-same">
                {r.error ? '✗ Error' : r.same ? '= Same' : '≠ Different'}
              </span>
            </span>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(300px, 100%), 1fr))', gap: 8 }}>
              <Part title="Earlier answer">
                <pre style={pre}>{r.before || '(none)'}</pre>
              </Part>
              <Part title="The draft’s answer">
                <pre style={pre}>{r.error ? `It did not finish: ${r.error}` : r.after || '(none)'}</pre>
              </Part>
            </div>
          </li>
        ))}
      </ul>
      {left > 0 && run.state === 'running' && <span className="rk-fhint">{left} more to replay after the cases.</span>}
    </>
  )
}
