import { useCallback, useEffect, useMemo, useState } from 'react'
import { addJob, budgetText, describeJobSchedule, fetchJobRuns, fetchJobs, rebuildJobNow, removeJob, runJobNow, updateJob, usd, type Job, type JobChange, type JobEvent, type JobsView, type JobWhen, type RunBrief } from '../../api/jobs'
import { hideAmounts, useShowCosts } from '../../lib/costs'
import { fmtTs } from '../../lib/format'
import { ConfirmDialog } from '../ConfirmDialog'
import { adminHref, navigate, type Route } from '../../lib/router'
import { Drawer } from '../Drawer'
import { SchedulePicker, type Schedule } from '../SchedulePicker'
import { Select } from '../Select'
import { jobHref, pathId, runHref, scheduleHref } from './jobsRoute'
import { PRIORITIES, PRIORITY } from './parts'
import { RunDetail } from './RunDetail'
import { runSummary, StatusPill } from './RunsTab'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const EVENT_LABEL: Record<JobEvent, string> = {
  alert: 'When an alert fires',
  report: 'When a report arrives',
  source: 'When its source changes',
  case: 'When a case is opened',
}

/** A source's Wiki sync job (KN-28): it reads its source, on no cluster. */
const isWikiSync = (j: Pick<Job, 'kind'>) => j.kind.startsWith('wiki-sync-')

/** The clusters a job covers, in words. */
function clustersText(j: Job, names: Map<string, string>): string {
  if (isWikiSync(j)) return 'Reads its knowledge source'
  if (j.kind === 'report-triage') return 'Reports from every service'
  if (!j.clusters?.length) return 'All clusters'
  return 'On ' + j.clusters.map((c) => names.get(c) ?? c).join(', ')
}

/** A job's state, as a pill. */
function StatePill({ j }: { j: Job }) {
  if (!j.on)
    return (
      <span className="rk-pill" data-testid="job-state">
        Paused
      </span>
    )
  if (j.stoppedToday)
    return (
      <span className="rk-pill" data-tone="warn" title={j.stoppedToday} data-testid="job-state">
        Stopped for the day
      </span>
    )
  if (j.unusable)
    return (
      <span className="rk-pill" data-tone="warn" title={j.unusable} data-testid="job-state">
        On, cannot run
      </span>
    )
  return (
    <span className="rk-pill" data-tone="ok" data-testid="job-state">
      {j.running ? 'On · running' : 'On'}
    </span>
  )
}

/** When a job runs, as the list shows it: the schedule, its zone under it. */
function WhenCell({ j }: { j: Job }) {
  const text = j.whenText
  const cut = j.when.schedule && !j.when.event ? text.lastIndexOf(', ') : -1
  if (cut < 0) return <>{text}</>
  return (
    <>
      {text.slice(0, cut)}
      <br />
      <span style={{ color: 'var(--fg2)', fontSize: 12 }}>{text.slice(cut + 2)}</span>
    </>
  )
}

function LastCell({ j }: { j: Job }) {
  if (j.external) {
    if (!j.external.lastAt) return <span style={{ color: 'var(--fg3)' }}>Never</span>
    return (
      <>
        {fmtTs(j.external.lastAt)} · <span style={{ color: j.external.lastOk ? undefined : 'var(--err)' }}>{j.external.last}</span>
      </>
    )
  }
  if (!j.last) return <span style={{ color: 'var(--fg3)' }}>Never</span>
  return (
    <>
      {fmtTs(j.last.started)} ·{' '}
      <a className="rk-link" href={runHref(j.last.id)} onClick={(e) => e.stopPropagation()}>
        {runSummary(j.last)}
      </a>
    </>
  )
}

/**
 * AIOps › Jobs › Schedule (Phase 12, JB-15, FX-13): every job that starts an agent with
 * nobody at the screen, and the scheduled backup. Each shows its agent,
 * when it runs in plain words, its budget, its last and next run and its
 * state; one opens in a drawer (#/aiops/jobs/<id>) with Run now, Pause,
 * its settings and its runs.
 */
export function JobsTab({ route, onToast }: { route: Route; onToast?: (msg: string) => void }) {
  const open = pathId(route.path, 'jobs')
  const costs = useShowCosts()
  const [view, setView] = useState<JobsView | null>(null)
  const saved = (j: Job) => setView((v) => (v ? { ...v, jobs: v.jobs.map((x) => (x.id === j.id ? j : x)) } : v))
  const [error, setError] = useState('')
  const load = useCallback((background = false) => {
    fetchJobs(background).then(
      (v) => {
        setView(v)
        setError('')
      },
      (e: unknown) => setError(errorText(e)),
    )
  }, [])
  useEffect(() => {
    load()
    const t = setInterval(() => load(true), 15000)
    return () => clearInterval(t)
  }, [load])
  const names = useMemo(() => new Map((view?.clusters ?? []).map((c) => [c.id, c.name])), [view])
  const job = view?.jobs.find((j) => j.id === open)
  return (
    <section className="rk-card" style={{ padding: '20px 24px 24px', display: 'flex', flexDirection: 'column', gap: 16 }} aria-labelledby="jobs-h" data-testid="jobs-tab">
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10 }}>
        <h2 id="jobs-h" style={{ margin: 0, fontSize: 17, fontWeight: 600, flex: '1 1 auto' }}>
          Jobs
        </h2>
      </div>
      <p style={{ margin: 0, color: 'var(--fg2)', lineHeight: 1.5, fontSize: 13 }}>
        Jobs that start an agent with nobody at the screen. Open one to see exactly what it does: its agent, the instructions it sends, what it reads and what it may make, all of which may be changed. Their output is read-only: reports,
        notes, wiki pages. A change they want waits in the Inbox. Who may edit jobs is set in{' '}
        <a className="rk-link" href={adminHref('settings', ['jobs'])}>
          Administration › Settings
        </a>
        .
      </p>
      {error && (
        <p className="rk-t" data-tone="err" style={{ margin: 0 }} data-testid="jobs-error">
          {error}
        </p>
      )}
      {!view && !error && <p className="rk-fhint">Loading…</p>}
      {view && (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', minWidth: 760, fontSize: 13 }} data-testid="jobs-table">
            <thead>
              <tr>
                {['Job', 'When', ...(costs ? ['Budget'] : []), 'Last run', 'Next run', 'State'].map((h) => (
                  <th key={h} style={th}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {view.jobs.map((j) => (
                <tr key={j.id} style={{ background: j.id === open ? 'var(--accent-weak)' : undefined, cursor: 'pointer' }} onClick={() => navigate(jobHref(j.id))} data-testid="job-row" data-job={j.id}>
                  <td style={td}>
                    <a className="rk-link" href={jobHref(j.id)} onClick={(e) => e.stopPropagation()} style={{ fontWeight: 600, color: 'var(--fg)' }}>
                      {j.label}
                    </a>
                    {/* The agent beside the name (I-5): several jobs share one agent, and its settings. */}
                    {j.agentName && (
                      <span style={{ color: 'var(--fg2)' }} title={`Run by the ${j.agentName}: its model, tools and caps in Settings › Agents apply to every job it runs`} data-testid="job-agent">
                        {' · '}
                        {j.agentName}
                      </span>
                    )}
                    <br />
                    <span style={{ color: 'var(--fg2)', fontSize: 12 }}>
                      {j.external ? `Set in ${j.where ?? 'Administration'}${j.external.detail ? ` · ${j.external.detail}` : ''}` : `${j.agentName ? '' : 'No agent · '}${clustersText(j, names)}`}
                    </span>
                  </td>
                  <td style={td}>
                    <WhenCell j={j} />
                  </td>
                  {costs && <td style={{ ...td, color: j.external ? 'var(--fg3)' : undefined }}>{j.external ? 'None' : budgetText(j.budget)}</td>}
                  <td style={td}>
                    <LastCell j={j} />
                  </td>
                  <td style={{ ...td, color: j.next ? undefined : 'var(--fg3)' }}>{j.next ? fmtTs(j.next) : j.nextText}</td>
                  <td style={td}>
                    <StatePill j={j} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {open && view && (
        <Drawer
          label="Job"
          kicker={job ? `${job.agentName ?? 'No agent'} · job` : 'Job'}
          title={job?.label ?? open}
          facts={job ? <span data-testid="job-drawer-state">{stateLine(job)}</span> : undefined}
          footer={job && view.mayEdit ? <JobActions key={job.id} job={job} onSaved={saved} onToast={onToast} /> : undefined}
          onClose={() => navigate(scheduleHref)}
          width={820}
          copy={false}
          testId="job-drawer"
        >
          {job ? (
            <JobPanel key={job.id + (job.setAt ?? '')} job={job} view={view} onSaved={saved} onRemoved={() => load()} onAdded={(j) => (load(), navigate(jobHref(j.id)))} onToast={onToast} />
          ) : (
            <p className="rk-fhint">There is no job “{open}”.</p>
          )}
        </Drawer>
      )}
    </section>
  )
}

const th: React.CSSProperties = { fontSize: 12, color: 'var(--fg2)', fontWeight: 500, textAlign: 'left', padding: '8px 10px', borderBottom: '1px solid var(--line)' }
const td: React.CSSProperties = { padding: '9px 10px', borderBottom: '1px solid var(--line)', verticalAlign: 'top' }

/** A job's settings as the form edits them. */
interface Draft {
  name: string
  /** The instructions as edited: the job's own, or the built-in ones (JR-06). */
  task: string
  scheduled: boolean
  schedule: Schedule
  events: JobEvent[]
  severity: 'warning' | 'error'
  allClusters: boolean
  clusters: string[]
  runUSD: string
  dayUSD: string
  /** The lowest priority a finding opens a case at, for a job that has findings; null for one without (CF-33). */
  minPriority: number | null
  skipOverlap: boolean
  catchUp: boolean
}

function draftOf(j: Job, zone: string): Draft {
  return {
    name: j.name ?? '',
    task: j.task ?? j.builtinTask ?? '',
    scheduled: !!j.when.schedule,
    schedule: j.when.schedule ?? { kind: 'daily', at: '02:00', timeZone: zone },
    events: j.when.event ? [j.when.event] : [],
    severity: j.when.severity ?? 'warning',
    allClusters: !j.clusters?.length,
    clusters: j.clusters ?? [],
    runUSD: j.budget.runUSD ? String(j.budget.runUSD) : '',
    dayUSD: j.budget.dayUSD ? String(j.budget.dayUSD) : '',
    minPriority: typeof j.minPriority === 'number' ? j.minPriority : null,
    skipOverlap: j.skipOverlap,
    catchUp: j.catchUp,
  }
}

function changeOf(d: Draft, j: Job): JobChange {
  const when: JobWhen = {}
  if (d.scheduled && j.schedulable) when.schedule = d.schedule
  if (d.events.length) {
    when.event = d.events[0]
    if (when.event === 'alert') when.severity = d.severity
  }
  const c: JobChange = {
    when,
    clusters: d.allClusters ? [] : d.clusters,
    budget: { runUSD: Number(d.runUSD) || 0, dayUSD: Number(d.dayUSD) || 0 },
    ...(d.minPriority !== null ? { minPriority: d.minPriority } : {}),
    skipOverlap: d.skipOverlap,
    catchUp: d.catchUp,
  }
  // Any job takes a name and instructions of its own; the built-in text is the built-in (JR-06).
  c.name = d.name.trim()
  if (j.builtinTask !== undefined) c.task = d.task.trim() === j.builtinTask.trim() ? '' : d.task
  return c
}

/** The header's line: whether the job runs, and when. */
function stateLine(j: Job): string {
  const state = j.running ? 'Running' : j.on ? 'On' : 'Off'
  return j.whenText ? `${state} · ${j.whenText}` : state
}

/**
 * The drawer's footer: turn the job on or pause it on the left; the runs a
 * person starts by hand on the right, Run now and, for a Wiki sync,
 * Rebuild now… (KN-29). Always in view while the settings or runs scroll.
 */
function JobActions({ job, onSaved, onToast }: { job: Job; onSaved: (j: Job) => void; onToast?: (msg: string) => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [rebuilding, setRebuilding] = useState(false)
  const act = async (what: string, f: () => Promise<unknown>) => {
    setBusy(true)
    setError('')
    try {
      await f()
      onToast?.(what)
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const setOn = (on: boolean) =>
    act(on ? `${job.label} resumed.` : `${job.label} paused.`, async () => {
      onSaved(await updateJob(job.id, { on }))
    })
  const runNow = () =>
    act(`${job.label} started.`, async () => {
      const r = await runJobNow(job.id)
      if (r.id) onSaved({ ...job, running: true, last: r })
    })
  const rebuildNow = () =>
    act(`${job.label}: rebuilding.`, async () => {
      const r = await rebuildJobNow(job.id)
      setRebuilding(false)
      if (r.id) onSaved({ ...job, running: true, last: r })
    })
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, width: '100%' }} data-testid="job-actions">
      <button className="rk-btn" disabled={busy || (!!job.external && !job.external.ready)} onClick={() => void setOn(!job.on)} data-testid="job-toggle">
        {job.on ? 'Pause' : job.external ? 'Resume' : 'Turn on'}
      </button>
      {error && (
        <span className="rk-t" data-tone="err" style={{ fontSize: 12.5 }} data-testid="job-action-error">
          {error}
        </span>
      )}
      <span style={{ flex: '1 1 auto' }} />
      {job.manual && (
        <button className="rk-btn" disabled={busy || job.running} onClick={() => void runNow()} data-testid="job-run-now">
          Run now
        </button>
      )}
      {job.manual && job.rebuild && (
        <button className="rk-btn" disabled={busy || job.running} onClick={() => setRebuilding(true)} title="Gives Mycroft every item again, unchanged ones too, and redrafts the pages from all of them" data-testid="job-rebuild-now">
          Rebuild now…
        </button>
      )}
      {rebuilding && (
        <ConfirmDialog title={`Rebuild ${job.label}?`} confirm="Rebuild now" busy={busy} onCancel={() => setRebuilding(false)} onConfirm={() => void rebuildNow()}>
          <p style={{ margin: 0 }} data-testid="job-rebuild-text">
            Mycroft gets every item of this source again, unchanged ones too, and redrafts the pages from them. Nothing is deleted; the drafts wait for review in AIOps › Inbox. It costs as much as reading the source the first time.
          </p>
        </ConfirmDialog>
      )}
    </div>
  )
}

/** The open job: its actions, its settings, its runs. */
function JobPanel({ job, view, onSaved, onRemoved, onAdded, onToast }: { job: Job; view: JobsView; onSaved: (j: Job) => void; onRemoved: () => void; onAdded: (j: Job) => void; onToast?: (msg: string) => void }) {
  const [tab, setTab] = useState<'settings' | 'history'>('settings')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const zone = view.timeZone || 'UTC'
  const [draft, setDraft] = useState<Draft>(() => draftOf(job, zone))
  const costs = useShowCosts()
  const may = view.mayEdit
  const changed = JSON.stringify(draft) !== JSON.stringify(draftOf(job, zone))
  const act = async (what: string, f: () => Promise<unknown>) => {
    setBusy(true)
    setError('')
    try {
      await f()
      onToast?.(what)
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const save = () =>
    act('Saved.', async () => {
      const j = await updateJob(job.id, changeOf(draft, job))
      onSaved(j)
    })
  const kind = view.kinds.find((k) => k.id === job.kind)
  const addAnother = () =>
    act(`Another ${kind?.label ?? job.label} job added, paused.`, async () => {
      onAdded(await addJob(job.kind))
    })
  const remove = () =>
    act(`${job.label} removed.`, async () => {
      await removeJob(job.id)
      onRemoved()
      navigate(scheduleHref)
    })
  const set = (p: Partial<Draft>) => setDraft((d) => ({ ...d, ...p }))
  const toggleEvent = (e: JobEvent, on: boolean) => set({ events: on ? [e] : draft.events.filter((x) => x !== e) })
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }} data-testid="job-panel">
      {job.unusable && (
        <p className="rk-callout" style={{ margin: 0, fontSize: 12.5 }} data-testid="job-unusable">
          {job.unusable}
        </p>
      )}
      {job.stoppedToday && (
        <p className="rk-callout" style={{ margin: 0, fontSize: 12.5 }} data-testid="job-stopped">
          {hideAmounts(job.stoppedToday, costs)}. It starts again tomorrow (UTC), or when its budget is raised.
        </p>
      )}
      {error && (
        <p className="rk-t" data-tone="err" style={{ margin: 0 }} data-testid="job-error">
          {error}
        </p>
      )}
      {!job.external && (
        <div className="rk-seg" role="tablist" aria-label="Job" style={{ alignSelf: 'flex-start' }}>
          {(['settings', 'history'] as const).map((t) => (
            <button key={t} type="button" role="tab" aria-selected={tab === t} aria-pressed={tab === t} onClick={() => setTab(t)} data-testid={`job-tab-${t}`}>
              {t === 'settings' ? 'Settings' : 'Runs'}
            </button>
          ))}
        </div>
      )}
      {(job.external || tab === 'settings') && (
        <span className="rk-fhint" data-testid="job-set-by">
          {job.setBy ? `Set by ${job.setBy}${job.setAt ? `, ${fmtTs(job.setAt)}` : ''}` : 'Not changed since it was made.'}
        </span>
      )}
      {job.external ? (
        <ExternalPanel job={job} />
      ) : tab === 'history' ? (
        <JobHistory job={job} onToast={onToast} />
      ) : (
        <fieldset disabled={!may || busy} style={{ border: 0, padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: 14 }} data-testid="job-form">
          <Definition job={job} draft={draft} set={set} may={may} />
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)' }}>
            Name
            <span className="rk-field">
              <input value={draft.name} onChange={(e) => set({ name: e.target.value })} maxLength={80} placeholder={kind?.label ?? job.label} data-testid="job-name" />
            </span>
          </label>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            <span style={label}>Starts</span>
            {job.schedulable && (
              <>
                <label className="rk-tcheck" style={{ color: 'var(--fg)' }}>
                  <input type="checkbox" checked={draft.scheduled} onChange={(e) => set({ scheduled: e.target.checked })} data-testid="job-scheduled" /> On a schedule
                </label>
                {draft.scheduled && <SchedulePicker value={draft.schedule} onChange={(s) => set({ schedule: s })} describe={describeJobSchedule} defaultZone={zone} disabled={!may} testId="job-sched" />}
              </>
            )}
            {(job.events ?? []).map((e) => (
              <div key={e} style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
                <label className="rk-tcheck" style={{ color: 'var(--fg)' }}>
                  <input type="checkbox" checked={draft.events.includes(e)} onChange={(ev) => toggleEvent(e, ev.target.checked)} data-testid={`job-event-${e}`} /> {EVENT_LABEL[e]}
                </label>
                {e === 'alert' && draft.events.includes('alert') && (
                  <Select
                    label="Severity"
                    value={draft.severity}
                    options={[
                      ['warning', 'warning and above'],
                      ['error', 'errors only'],
                    ]}
                    onChange={(v) => set({ severity: v as Draft['severity'] })}
                    disabled={!may}
                    testId="job-severity"
                  />
                )}
              </div>
            ))}
          </div>
          {job.kind !== 'report-triage' && !isWikiSync(job) && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              <span style={label}>Clusters</span>
              <label className="rk-tcheck" style={{ color: 'var(--fg)' }}>
                <input type="radio" name="job-clusters" checked={draft.allClusters} onChange={() => set({ allClusters: true })} data-testid="job-all-clusters" /> All clusters
              </label>
              <label className="rk-tcheck" style={{ color: 'var(--fg)' }}>
                <input type="radio" name="job-clusters" checked={!draft.allClusters} onChange={() => set({ allClusters: false })} data-testid="job-some-clusters" /> These clusters:
              </label>
              {!draft.allClusters && (
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12, paddingLeft: 22, fontSize: 12.5 }}>
                  {view.clusters.map((c) => (
                    <label key={c.id} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                      <input
                        type="checkbox"
                        checked={draft.clusters.includes(c.id)}
                        onChange={(e) => set({ clusters: e.target.checked ? [...draft.clusters, c.id] : draft.clusters.filter((x) => x !== c.id) })}
                        data-testid={`job-cluster-${c.id}`}
                      />
                      {c.name}
                    </label>
                  ))}
                </div>
              )}
            </div>
          )}
          <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            {(
              [
                ['runUSD', 'Budget per run (US$)'],
                ['dayUSD', 'Budget per day (US$)'],
              ] as const
            ).map(([k, l]) => (
              <label key={k} style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)' }}>
                {l}
                <span className="rk-field" style={{ width: 150 }}>
                  <input type="number" min={0} step={0.5} placeholder="None" value={draft[k]} onChange={(e) => set({ [k]: e.target.value } as Partial<Draft>)} data-testid={`job-${k}`} />
                </span>
              </label>
            ))}
          </div>
          <span className="rk-fhint">A job stops when either is spent, and says so.{costs ? ` Today it has spent ${usd(job.spentToday)}.` : ''} The agent's own caps and the overall limits in Settings › Agents hold too.</span>
          {draft.minPriority !== null && (
            // --- A job's lowest priority (Phase 12, wave 6c, CF-33) ---
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)' }} data-testid="job-min-priority-field">
              Lowest priority
              <Select
                label="Lowest priority"
                value={String(draft.minPriority)}
                options={PRIORITIES.map((p): [string, string] => [String(p), PRIORITY[p]!])}
                disabled={!may}
                onChange={(v) => set({ minPriority: Number(v) })}
                testId="job-min-priority"
                width={260}
              />
              <span className="rk-fhint">Findings below it open no case: the run keeps them, marked ignored.</span>
            </div>
          )}
          <label className="rk-tcheck" style={{ color: 'var(--fg)' }}>
            <input type="checkbox" checked={draft.skipOverlap} onChange={(e) => set({ skipOverlap: e.target.checked })} data-testid="job-skip" /> Skip a run while the last one is still going
          </label>
          {job.schedulable && (
            <label className="rk-tcheck" style={{ color: 'var(--fg)' }}>
              <input type="checkbox" checked={draft.catchUp} onChange={(e) => set({ catchUp: e.target.checked })} data-testid="job-catch-up" /> Run once after a restart if missed within the last hour
            </label>
          )}
          {may && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <button className="rk-btn" data-primary="true" disabled={!changed || busy} onClick={() => void save()} data-testid="job-save">
                Save changes
              </button>
              <span style={{ color: 'var(--fg2)', fontSize: 12.5 }}>{changed ? 'Not saved yet.' : 'No changes.'}</span>
              <span style={{ flex: '1 1 auto' }} />
              {job.added && (
                <button className="rk-btn" data-danger="true" disabled={busy} onClick={() => void remove()} data-testid="job-remove">
                  Remove job
                </button>
              )}
            </div>
          )}
          {!may && <span className="rk-fhint">Only a SuperAdmin, or a role Settings › Jobs names, changes jobs.</span>}
        </fieldset>
      )}
      {may && kind?.addMore && (
        <div className="rk-callout" style={{ margin: 0, display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10, fontSize: 12.5 }} data-testid="job-add-another-box">
          <span style={{ flex: '1 1 320px' }}>
            Want it on another schedule, on other clusters, or with other instructions as well? Add another {kind.label} job: it starts paused, as a copy of the built-in one, and this one stays as it is.
          </span>
          <button className="rk-btn" disabled={busy} onClick={() => void addAnother()} data-testid="job-add-another">
            Add another {kind.label} job
          </button>
        </div>
      )}
    </div>
  )
}

const label: React.CSSProperties = { fontSize: 11, letterSpacing: '.06em', textTransform: 'uppercase', color: 'var(--fg2)', fontWeight: 600 }

/**
 * A job's definition, plainly (JR-05, JR-06): what it does, which agent,
 * the instructions it sends (the kind's built-in ones pre-filled, editable,
 * with Reset), what it reads and what it may make. The run puts what it is
 * about around the instructions (the cluster, the alert, the report, the
 * accepted exceptions) and asks for the answer's format after them.
 */
function Definition({ job, draft, set, may }: { job: Job; draft: Draft; set: (p: Partial<Draft>) => void; may: boolean }) {
  const builtin = job.builtinTask
  const own = builtin !== undefined && draft.task.trim() !== builtin.trim()
  return (
    <section aria-label="What it does" style={{ display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="job-definition">
      <span style={label}>What it does</span>
      <dl className="cs-kv" style={{ margin: 0, fontSize: 12.5 }}>
        <dt>Makes</dt>
        <dd data-testid="job-def-does">{job.does ?? '—'}</dd>
        <dt>Agent</dt>
        <dd>{job.agentName ?? 'No agent'}</dd>
        {job.reads && (
          <>
            <dt>Reads</dt>
            <dd data-testid="job-def-reads">{job.reads}</dd>
          </>
        )}
        {job.proposes && (
          <>
            <dt>May make</dt>
            <dd data-testid="job-def-proposes">{job.proposes}</dd>
          </>
        )}
      </dl>
      {builtin === undefined ? (
        <span className="rk-fhint">It sends no instructions of its own to change.</span>
      ) : (
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)' }}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            Instructions it sends its agent
            <span className="rk-pill" data-tone={own ? 'info' : undefined} data-testid="job-task-state">
              {own ? 'Its own' : 'Built-in'}
            </span>
            {own && may && (
              <button type="button" className="rk-btn" data-size="sm" onClick={() => set({ task: builtin })} data-testid="job-task-reset">
                Reset to built-in
              </button>
            )}
          </span>
          <textarea
            className="rk-area rk-mono"
            rows={7}
            maxLength={8000}
            value={draft.task}
            onChange={(e) => set({ task: e.target.value })}
            spellCheck={false}
            style={{ fontSize: 12, lineHeight: 1.5, resize: 'vertical' }}
            data-testid="job-task"
          />
          <span className="rk-fhint">
            Each run puts what it is about around them (the cluster, the alert or report, the accepted exceptions) and asks for a report with findings (each becomes a case, or joins the open case of the same problem) and proposed changes
            after them; nothing it proposes runs without a person.
          </span>
        </label>
      )}
    </section>
  )
}

/** The backups to S3: shown here, set in Administration › Backup. */
function ExternalPanel({ job }: { job: Job }) {
  const x = job.external!
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, fontSize: 13 }} data-testid="job-external">
      <span>
        {x.when ?? job.whenText}
        {x.next ? ` · next ${fmtTs(x.next)}` : ''}
      </span>
      {x.detail && (
        <span className="rk-mono" style={{ fontSize: 12 }}>
          {x.detail}
        </span>
      )}
      <span style={{ color: x.lastAt && !x.lastOk ? 'var(--err)' : 'var(--fg2)' }}>{x.lastAt ? `Last: ${fmtTs(x.lastAt)} · ${x.last ?? ''}` : 'Not run yet.'}</span>
      <span>
        Its bucket, schedule, retention and encryption are set in{' '}
        <a className="rk-link" href={job.link ?? '#/admin/backup'}>
          {job.where ?? 'Administration › Backup'}
        </a>
        ; here it runs now or pauses.
      </span>
    </div>
  )
}

/** A job's runs, newest first, each opening to its output and cost. */
function JobHistory({ job, onToast }: { job: Job; onToast?: (msg: string) => void }) {
  const costs = useShowCosts()
  const [runs, setRuns] = useState<RunBrief[] | null>(null)
  const [error, setError] = useState('')
  const [open, setOpen] = useState('')
  useEffect(() => {
    fetchJobRuns(job.id, 30).then(
      (r) => setRuns(r.runs ?? []),
      (e: unknown) => setError(errorText(e)),
    )
  }, [job.id, job.last?.id])
  if (error)
    return (
      <p className="rk-t" data-tone="err">
        {error}
      </p>
    )
  if (!runs) return <p className="rk-fhint">Loading…</p>
  if (!runs.length)
    return (
      <p className="rk-fhint" data-testid="job-history-empty">
        No runs yet.
      </p>
    )
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="job-history">
      {runs.map((r) => (
        <div key={r.id} style={{ borderBottom: '1px solid var(--line)', paddingBottom: 6 }}>
          <button
            className="rk-btn"
            style={{ width: '100%', justifyContent: 'flex-start', gap: 10, border: 0, background: 'transparent' }}
            aria-expanded={open === r.id}
            onClick={() => setOpen(open === r.id ? '' : r.id)}
            data-testid="job-history-row"
          >
            <span style={{ minWidth: 140, textAlign: 'left' }}>{fmtTs(r.started)}</span>
            <StatusPill run={r} />
            <span style={{ flex: '1 1 auto', textAlign: 'left', overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {r.trigger.what} · {runSummary(r)}
            </span>
            {costs && <span>{usd(r.costUSD)}</span>}
          </button>
          {open === r.id && (
            <div style={{ padding: '8px 4px' }}>
              <RunDetail id={r.id} onToast={onToast} />
              <a className="rk-link" href={runHref(r.id)} style={{ fontSize: 12.5 }}>
                Open in Runs
              </a>
            </div>
          )}
        </div>
      ))}
    </div>
  )
}
