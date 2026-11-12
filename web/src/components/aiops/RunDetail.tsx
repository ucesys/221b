import { useCallback, useEffect, useRef, useState } from 'react'
import { caseHref } from '../../api/casework'
import { cancelRun, fetchRun, HEALTH_LABEL, healthTone, mergedNote, runLimits, runStatus, usd, type Finding, type FindingRouting, type Routed, type Run, type RunFact } from '../../api/jobs'
import { useAIOpsItem } from '../../lib/aiopsItem'
import { useShowCosts } from '../../lib/costs'
import { fmtAge, fmtTs, nWord } from '../../lib/format'
import { Markdown } from '../../lib/markdown'
import { clusterHref } from '../../lib/router'
import { ConfirmDialog } from '../ConfirmDialog'
import { FindingObject, RoutingChip, SeverityPill } from './FindingParts'
import { proposalHref, Resolves } from './ProposalLinks'
import { useRunNames } from './runNames'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** Where a run's reference leads: a report, or an alert's cluster. */
export function refLink(ref: string | undefined): { href: string; label: string } | null {
  if (!ref) return null
  const [kind, rest] = [ref.slice(0, ref.indexOf(':')), ref.slice(ref.indexOf(':') + 1)]
  // --- Cases replace reports (Phase 12, wave 5): a report is case C-<n>, and a case's analysis links to the case.
  if (kind === 'report') return { href: caseHref(rest), label: `C-${rest}` }
  if (kind === 'case') return { href: caseHref(rest), label: rest }
  if (kind === 'alert') {
    const cluster = rest.slice(0, rest.indexOf('/'))
    return { href: clusterHref(cluster || 'local', 'alerts'), label: 'the alert' }
  }
  return null
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
      <span style={{ fontSize: 11, letterSpacing: '.06em', textTransform: 'uppercase', color: 'var(--fg3)', fontWeight: 600 }}>{label}</span>
      <span style={{ fontSize: 12.5, overflowWrap: 'anywhere' }}>{children}</span>
    </div>
  )
}

/** What a run's findings did, in the order the summary says it, with the words for one and for many. */
const ROUTED: [keyof Routed, string, string][] = [
  ['opened', 'new case', 'new cases'],
  ['reopened', 'case reopened: the fix did not hold', 'cases reopened: the fix did not hold'],
  ['regression', 'regression', 'regressions'],
  ['seen', 'seen again on an open case', 'seen again on open cases'],
  ['known', 'still present on a known issue', 'still present on known issues'],
  ['excepted', 'left out by an exception', 'left out by exceptions'],
  ['verified', 'fix verified', 'fixes verified'],
  ['capped', 'over the cap of new cases', 'over the cap of new cases'],
  ['invalid', 'not valid', 'not valid'],
  ['ignored', 'below the job’s lowest priority', 'below the job’s lowest priority'],
  ['expected', 'expected: no problem', 'expected: no problem'],
]

// Findings that need a person first: what opened or reopened a case.
const ORDER: Record<FindingRouting, number> = { reopened: 0, regression: 1, opened: 2, seen: 3, capped: 4, known: 5, excepted: 6, invalid: 7, ignored: 8, expected: 9 }
const SEV: Record<Finding['severity'], number> = { critical: 0, warning: 1, info: 2 }

/** What a run's findings did, as a line of counts; nothing for a run without findings. */
export function routedParts(r: Routed | undefined): { key: keyof Routed; text: string }[] {
  if (!r) return []
  return ROUTED.filter(([k]) => (r[k] ?? 0) > 0).map(([k, one, many]) => ({ key: k, text: `${r[k]} ${r[k] === 1 ? one : many}` }))
}

/** The titles a run already knows of what its changes resolve: the cases and known issues its findings went to (CF-39). */
export function findingTitles(run: Pick<Run, 'findings'>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const f of run.findings ?? []) {
    const id = f.case || f.knownIssue
    if (id && !out[id]) out[id] = f.title
  }
  return out
}

/** A finding's validation rule (its check): its key's, or as the agent named it. */
const ruleOf = (f: { key?: { check: string }; check?: string }) => f.key?.check || f.check || ''

/** What the run could not be used for (I-3): clusters that did not answer, findings left out past the cap, fixes not re-checked. */
export function RunLimitsNote({ run }: { run: Run }) {
  const lines = runLimits(run)
  if (!lines.length) return null
  return (
    <div className="rk-callout" data-tone="warn" style={{ flexDirection: 'column', gap: 4, fontSize: 12.5 }} data-testid="run-limits">
      <strong>What this run could not be used for</strong>
      {lines.map((l) => (
        <span key={l.key} data-testid={`run-limit-${l.key}`}>
          {l.text}
        </span>
      ))}
    </div>
  )
}

/**
 * One run, whole (JB-16; wave 6): what started it and who set the trigger,
 * the agent and model, its report, what its findings did (new cases,
 * seen again, known, excepted, reopened, regressions, verified fixes,
 * capped, not valid) with each finding linked to its case, known issue or
 * exception, the validation rules new on the cluster (each finding's with
 * what it means), the proposed changes it filed,
 * and its tokens and cost; a run under way may be cancelled (FX-12).
 * A run of a kind that makes no findings (a Wiki sync, KN-28) says what
 * it did instead: its facts (the source, items read, unchanged, gone and
 * skipped, the drafts with a link to AIOps › Inbox), its report, and no
 * findings or proposed changes sections.
 */
export function RunDetail({ id, onToast }: { id: string; onToast?: (msg: string) => void }) {
  const names = useRunNames()
  const [run, setRun] = useState<Run | null>(null)
  const [error, setError] = useState('')
  const status = useRef('')
  // How long a run under way has run counts from the last read.
  const [now, setNow] = useState(() => Date.now())
  const costs = useShowCosts()
  // Sherlock is told which run "this" is; one on a single cluster moves the dock there (IB-11).
  useAIOpsItem(run ? { kind: 'run', id: run.id, cluster: run.clusters?.length === 1 ? run.clusters[0] : undefined, summary: `${run.label} · ${run.status}` } : { kind: 'run', id })
  const load = useCallback(() => {
    fetchRun(id).then(
      (r) => {
        status.current = r.status
        setRun(r)
        setNow(Date.now())
        setError('')
      },
      (e: unknown) => setError(errorText(e)),
    )
  }, [id])
  useEffect(() => {
    load()
    // A run under way is read again until it ends.
    const t = setInterval(() => {
      if (status.current === 'running') load()
    }, 5000)
    return () => clearInterval(t)
  }, [load])
  if (error && !run) {
    return (
      <p className="rk-t" data-tone="err" style={{ margin: 0 }} data-testid="run-error">
        {error}
      </p>
    )
  }
  if (!run) return <p className="rk-fhint">Loading…</p>
  // The status takes the tone of what the run found (JR-01).
  const [label, tone] = runStatus(run)
  const ref = refLink(run.ref)
  // What needs a person first, then the worst.
  const findings = [...(run.findings ?? [])].sort((a, b) => (ORDER[a.routing] ?? 9) - (ORDER[b.routing] ?? 9) || (SEV[a.severity] ?? 3) - (SEV[b.severity] ?? 3))
  const routed = routedParts(run.routed)
  const known = findingTitles(run)
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }} data-testid="run-detail">
      {run.status === 'running' && run.mayCancel && <CancelRun id={run.id} label={run.label} onDone={load} onToast={onToast} />}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))', gap: 12 }}>
        <Fact label="Status">
          <span className="rk-pill" data-tone={tone} data-testid="run-status">
            {label}
          </span>
          {run.repeats ? <span style={{ color: 'var(--fg3)' }}> · {nWord(run.repeats, 'repeat', 'repeats')} since</span> : null}
        </Fact>
        {run.health && (
          <Fact label="Health">
            <span className="rk-pill" data-tone={healthTone(run.health)} data-testid="run-health">
              {HEALTH_LABEL[run.health]}
            </span>
          </Fact>
        )}
        <Fact label="Started by">
          <span data-testid="run-trigger">{run.trigger.what}</span>
          {run.trigger.setBy ? <span style={{ color: 'var(--fg3)' }}> · trigger set by {run.trigger.setBy}</span> : null}
        </Fact>
        <Fact label="Agent">
          {run.agent ? names.agent(run.agent) : '—'}
          {run.model ? <span style={{ color: 'var(--fg3)' }}> · {run.model}</span> : null}
        </Fact>
        <Fact label="When">
          {fmtTs(run.started)}
          {run.ended ? ` to ${fmtTs(run.ended)}` : <span style={{ color: 'var(--fg3)' }}> · running {fmtAge(run.started, now)}</span>}
        </Fact>
        <Fact label="Clusters">{run.clusters?.length ? run.clusters.map(names.cluster).join(', ') : '—'}</Fact>
        {costs ? (
          <Fact label="Cost">
            <span data-testid="run-cost">{usd(run.costUSD)}</span>
            <span style={{ color: 'var(--fg3)' }}>
              {' '}
              · {run.tokens.in.toLocaleString()} tokens in, {run.tokens.out.toLocaleString()} out
            </span>
          </Fact>
        ) : (
          // Show costs off (CF-40): the tokens, without dollars.
          <Fact label="Tokens">
            <span data-testid="run-tokens">
              {run.tokens.in.toLocaleString()} in, {run.tokens.out.toLocaleString()} out
            </span>
          </Fact>
        )}
        {ref && (
          <Fact label="About">
            <a className="rk-link" href={ref.href} data-testid="run-ref">
              {ref.label}
            </a>
          </Fact>
        )}
      </div>
      {run.note && (
        <p className="rk-callout" style={{ margin: 0, fontSize: 12.5 }} data-testid="run-note">
          {mergedNote(run.note)}
        </p>
      )}
      <RunLimitsNote run={run} />
      {(run.facts ?? []).length > 0 && <RunFacts facts={run.facts!} />}
      <section aria-label="Report" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <h3 style={{ margin: 0, fontSize: 13.5, fontWeight: 600 }}>Report</h3>
        {run.report ? (
          <div className="rk-ans-body" style={{ fontSize: 13, lineHeight: 1.5 }} data-testid="run-report">
            <Markdown
              text={run.report}
              link={(url, text) =>
                url.startsWith('#') || url.startsWith('https://') ? (
                  <a className="rk-link" href={url}>
                    {text}
                  </a>
                ) : null
              }
              code={(text, _lang, _open, key) => (
                <pre key={key} className="rk-mono rk-code" style={{ whiteSpace: 'pre-wrap', fontSize: 12 }}>
                  {text}
                </pre>
              )}
            />
          </div>
        ) : (
          <p className="rk-fhint" style={{ margin: 0 }}>
            {run.status === 'running' ? 'The run is under way.' : 'No report.'}
          </p>
        )}
      </section>
      {!run.noFindings && (
        <section aria-label="What its findings did" style={{ display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="run-routed">
          <h3 style={{ margin: 0, fontSize: 13.5, fontWeight: 600 }}>What its findings did</h3>
          {run.notRouted && (
            <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="run-not-routed">
              Not routed into cases: {run.notRouted.replace(/\.$/, '')}. {run.reroute ? 'The console tries again on its next look.' : 'The console no longer tries: see the note.'}
            </p>
          )}
          {routed.length === 0 && run.notRouted ? null : routed.length === 0 ? (
            <p className="rk-fhint" style={{ margin: 0 }}>
              {run.status === 'running' ? 'The run is under way.' : findings.length ? 'Not routed.' : 'No findings.'}
            </p>
          ) : (
            <span style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
              {routed.map((p) => (
                <span key={p.key} className="rk-tag" data-tone={p.key === 'reopened' || p.key === 'regression' ? 'err' : p.key === 'opened' ? 'warn' : p.key === 'verified' ? 'ok' : 'muted'} data-testid={`run-routed-${p.key}`}>
                  {p.text}
                </span>
              ))}
            </span>
          )}
          {(run.verifiedCases ?? []).length > 0 && (
            <span style={{ fontSize: 12.5 }} data-testid="run-verified">
              Fixes verified (re-checked, the problem gone):{' '}
              {run.verifiedCases!.map((id, i) => (
                <span key={id}>
                  {i > 0 ? ', ' : ''}
                  <a className="rk-link rk-mono" href={caseHref(id)}>
                    {id}
                  </a>
                </span>
              ))}
            </span>
          )}
          {(run.newChecks ?? []).length > 0 && (
            <span style={{ fontSize: 12.5 }} data-testid="run-new-checks">
              Validation rules new on this cluster:{' '}
              {run.newChecks!.map((c, i) => (
                <span key={c}>
                  {i > 0 ? ', ' : ''}
                  <span className="rk-mono" title={run.ruleMeanings?.[c]}>
                    {c}
                  </span>
                </span>
              ))}
            </span>
          )}
        </section>
      )}
      {!run.noFindings && (
        <section aria-label="Findings" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <h3 style={{ margin: 0, fontSize: 13.5, fontWeight: 600 }}>
            Findings <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{findings.length || 'none'}</span>
          </h3>
          {findings.map((f) => (
            <div key={f.id} className="rk-card" style={{ padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="run-finding" data-routing={f.routing}>
              <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                <SeverityPill severity={f.severity} />
                <strong style={{ fontSize: 13, flex: '1 1 auto' }}>{f.title}</strong>
                <RoutingChip f={f} testId={`finding-${f.id}-routing`} />
              </div>
              <span style={{ display: 'inline-flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                <FindingObject f={f} />
                {ruleOf(f) && (
                  <span style={{ fontSize: 12, color: 'var(--fg3)' }} title="Validation rule" data-testid={`finding-${f.id}-rule`}>
                    <span className="rk-mono">{ruleOf(f)}</span>
                    {run.ruleMeanings?.[ruleOf(f)] ? ` · ${run.ruleMeanings[ruleOf(f)]}` : ''}
                  </span>
                )}
                {f.newCheck && (
                  <span className="rk-tag" data-tone="info" title="Never seen on this cluster before this run" data-testid={`finding-${f.id}-new-check`}>
                    new validation rule
                  </span>
                )}
                {f.confidence === 'likely' && (
                  <span className="rk-tag" data-tone="muted" title="The cause fits the evidence, and the agent could not confirm it" data-testid={`finding-${f.id}-likely`}>
                    likely
                  </span>
                )}
              </span>
              {f.routing === 'invalid' && f.invalidReason && (
                <span className="rk-t" data-tone="warn" style={{ fontSize: 12.5 }} data-testid={`finding-${f.id}-invalid`}>
                  Not routed: {f.invalidReason}
                </span>
              )}
              {f.routing === 'expected' && (
                <span className="rk-fhint" style={{ fontSize: 12.5 }} data-testid={`finding-${f.id}-expected`}>
                  Opened no case: expected{f.expectedWhy ? ` — ${f.expectedWhy.replace(/\.$/, '')}` : ''}.
                </span>
              )}
              {f.routing === 'ignored' && (
                <span className="rk-fhint" style={{ fontSize: 12.5 }} data-testid={`finding-${f.id}-ignored`}>
                  Opened no case: below the job’s lowest priority{typeof f.priority === 'number' ? ` (it is P${f.priority})` : ''}.
                </span>
              )}
              {f.detail && <span style={{ fontSize: 12.5 }}>{f.detail}</span>}
              {f.evidence?.length ? (
                <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12, color: 'var(--fg2)' }}>
                  {f.evidence.map((e, i) => (
                    <li key={i} className="rk-mono">
                      {e}
                    </li>
                  ))}
                </ul>
              ) : null}
            </div>
          ))}
        </section>
      )}
      {!(run.noFindings && !run.proposals?.length) && (
        <section aria-label="Proposed changes" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <h3 style={{ margin: 0, fontSize: 13.5, fontWeight: 600 }}>Proposed changes</h3>
          {run.proposals?.length ? (
            <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 4 }} data-testid="run-proposals">
              {run.proposals.map((p, i) => (
                // What each change resolves (CF-25); one that named nothing was not filed (CF-27).
                <li key={p.id || `${i}-${p.title}`} data-testid="run-proposal" data-filed={p.error ? 'false' : 'true'}>
                  {p.title}
                  {p.error ? (
                    <span className="rk-t" data-tone="err" data-testid="run-proposal-error">
                      {' '}
                      — not filed: {p.error}
                    </span>
                  ) : (
                    <>
                      {' '}
                      —{' '}
                      <a className="rk-link" href={p.id ? proposalHref(p.id) : '#/aiops/inbox'}>
                        {p.id ? `${p.id} in the Inbox` : 'in the Inbox'}
                      </a>
                    </>
                  )}
                  {p.resolves?.length ? (
                    <span style={{ display: 'block', color: 'var(--fg2)' }}>
                      <Resolves ids={p.resolves} titles={{ ...known, ...p.resolvesTitles }} testId="run-proposal-resolves" />
                    </span>
                  ) : null}
                </li>
              ))}
            </ul>
          ) : (
            <p className="rk-fhint" style={{ margin: 0 }}>
              None. A change an agent wants waits for a person in AIOps › Inbox; nothing a job does changes anything.
            </p>
          )}
        </section>
      )}
    </div>
  )
}

/** What a run did, as its kind says it (KN-28): each fact with its value, linked where it leads, toned when it went wrong. */
function RunFacts({ facts }: { facts: RunFact[] }) {
  return (
    <section aria-label="What it did" style={{ display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="run-facts">
      <h3 style={{ margin: 0, fontSize: 13.5, fontWeight: 600 }}>What it did</h3>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))', gap: 12 }}>
        {facts.map((f) => (
          <Fact key={f.label} label={f.label}>
            <span className={f.tone ? 'rk-t' : undefined} data-tone={f.tone} data-testid="run-fact" data-label={f.label}>
              {f.href && f.href.startsWith('#') ? (
                <a className="rk-link" href={f.href}>
                  {f.text}
                </a>
              ) : (
                f.text
              )}
            </span>
          </Fact>
        ))}
      </div>
    </section>
  )
}

/** Cancel, for a run under way (FX-12): asks first, then stops the agent; what it spent so far is counted. */
function CancelRun({ id, label, onDone, onToast }: { id: string; label: string; onDone: () => void; onToast?: (msg: string) => void }) {
  const [asking, setAsking] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const cancel = async () => {
    setBusy(true)
    setError('')
    try {
      const r = await cancelRun(id)
      setAsking(false)
      onToast?.(r.status === 'cancelled' ? 'Cancelled.' : 'Cancelling…')
      onDone()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="rk-callout" style={{ display: 'flex', alignItems: 'center', gap: 10, margin: 0, fontSize: 12.5 }}>
      <span style={{ flex: '1 1 auto' }}>The run is under way.</span>
      <button className="rk-btn" data-size="sm" onClick={() => setAsking(true)} data-testid="run-cancel">
        Cancel run
      </button>
      {asking && (
        <ConfirmDialog title={`Cancel ${id}?`} confirm="Yes, cancel it" danger busy={busy} error={error} onCancel={() => setAsking(false)} onConfirm={() => void cancel()}>
          <p style={{ margin: 0 }}>{label} stops where it is: its agent’s call is cut short, what it found so far is kept, and what it spent so far counts against its budget.</p>
        </ConfirmDialog>
      )}
    </div>
  )
}
