// Many cases at once (Phase 12, wave 6c, CF-38): with cases picked in
// the Inbox, a bar says how many and offers one action for all of them:
// Assign, Priority, Type, Incident number, Analyze, and Close with one
// outcome (Fix with one text and RCA; No action needed with one
// explanation, since CF-90 for every case; Known issue, linked or made once; Duplicate of one
// case; Exception with one reason and last day). The server checks each
// case by the rules of its single action and answers for each; the
// results show here, done or refused with why. Since wave 6d (CF-42)
// Fixed always shows the RCA's fields, and the server keeps one RCA for
// every case fixed at once. Since wave 6e the RCA's fix is what was done,
// and the one RCA is kept on each case closed, no record of its own.
import { useEffect, useRef, useState } from 'react'
import {
  BULK_MAX,
  bulkCases,
  CASE_TYPE_IDS,
  CASE_TYPES,
  caseNumber,
  closeKnown,
  fetchCase,
  fetchOpenKnown,
  incidentError,
  INCIDENT_MAX,
  OUTCOMES,
  type BulkAction,
  type BulkFields,
  type BulkResult,
  type CaseBrief,
  type CasePriority,
  type CaseType,
  type Completion,
  type OpenKnown,
  type Outcome,
} from '../../api/casework'
import { nWord } from '../../lib/format'
import { ConfirmDialog } from '../ConfirmDialog'
import { Select } from '../Select'
import { CLOSE_HINT } from './CaseDrawer'
import { errText, PRIORITIES, PRIORITY } from './parts'

/** The dialogs of the bar's actions. */
export type BulkDialog = 'assign' | 'priority' | 'type' | 'incident' | 'analyze' | 'close'

/** The outcomes Close offers for many cases, in its order. */
const CLOSE_OUTCOMES: Outcome[] = ['fix', 'noAction', 'knownIssue', 'duplicate', 'exception']

const OUTCOME_HINT: Record<Outcome, string> = {
  fix: 'Fixed: one text says what was done for all of them, and one RCA covers them; at P0 and P1 it is required.',
  noAction: CLOSE_HINT.noAction,
  knownIssue: 'A real problem with a workaround and a fix to come: link one known issue, or make one, for all of them.',
  duplicate: 'The same problem as one other open case: their reporters and history go to it.',
  exception: 'Intended or acceptable here: the agents stop reporting it. Agent-found cases only.',
}

/** What the server answered for each case, with the cases' ids in the order they were picked. */
export interface BulkOutcome {
  action: string
  results: BulkResult[]
}

/** The words of a bulk action, as the results say what was done: "Assigned", "Closed as fixed". */
export function doneText(action: BulkAction, fields: BulkFields): string {
  switch (action) {
    case 'assign':
      return fields.assignee?.id ? 'Assigned' : 'Unassigned'
    case 'priority':
      return `Set to P${fields.priority}`
    case 'type':
      return `Set to ${CASE_TYPES[fields.type as CaseType] ?? fields.type}`
    case 'incident':
      return fields.incident ? `Incident number ${fields.incident}` : 'Incident number cleared'
    case 'analyze':
      return 'Analysis started'
    case 'fix':
      return 'Closed as fixed'
    case 'known':
      return `Closed as known issue${fields.id ? ` ${fields.id}` : ''}`
    case 'duplicate':
      return `Closed as a duplicate of ${fields.original ?? 'the original'}`
    case 'exception':
      return 'Closed as exception'
    case 'close':
      return `Closed as ${OUTCOMES[fields.outcome ?? 'noAction'].toLowerCase()}`
  }
}

/**
 * Sends one action for the cases, at most 100 to a request (the bar
 * takes no more), and returns each case's answer; a request the server
 * refuses whole (400) refuses each of its cases with the same words.
 */
export async function runBulk(ids: string[], action: BulkAction, fields: BulkFields): Promise<BulkResult[]> {
  const out: BulkResult[] = []
  for (let i = 0; i < ids.length; i += BULK_MAX) {
    const part = ids.slice(i, i + BULK_MAX)
    try {
      const r = await bulkCases(part, action, fields)
      const by = new Map(r.results.map((x) => [x.id.toUpperCase(), x]))
      for (const id of part) out.push(by.get(id.toUpperCase()) ?? { id, ok: false, error: 'The server did not answer for this case.' })
    } catch (e) {
      for (const id of part) out.push({ id, ok: false, error: errText(e) })
    }
  }
  return out
}

interface Props {
  /** The picked cases, as the Inbox lists them. */
  cases: CaseBrief[]
  onClear: () => void
  /** After an action: the results, for the page to show (BulkResults), the list to read again and the picks to keep the refused. */
  onDone: (o: BulkOutcome) => void
}

/** The bar over the picked cases, at the foot of the Inbox. */
export function BulkBar({ cases, onClear, onDone }: Props) {
  const [dialog, setDialog] = useState<BulkDialog | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const ids = cases.map((c) => c.id)
  const n = ids.length
  const close = () => {
    setDialog(null)
    setErr('')
  }
  /** Runs the action; the dialog gives way to the results. */
  const run = async (action: BulkAction, fields: BulkFields, first?: () => Promise<{ fields: BulkFields; results: BulkResult[]; rest: string[] }>) => {
    setBusy(true)
    setErr('')
    try {
      let f = fields
      let done: BulkResult[] = []
      let rest = ids
      // Known issue, made once: the first case makes it, the others link it.
      if (first) {
        const r = await first()
        f = r.fields
        done = r.results
        rest = r.rest
      }
      const all = rest.length ? [...done, ...(await runBulk(rest, action, f))] : done
      const o = { action: doneText(action, f), results: all }
      setDialog(null)
      onDone(o)
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <div className="rk-card rk-pickbar" role="group" aria-label="Picked cases" data-testid="inbox-bulkbar">
        <strong style={{ fontSize: 12.5, fontWeight: 600 }} data-testid="inbox-bulk-count">
          {n} selected
        </strong>
        <span style={{ fontSize: 12, color: 'var(--fg2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '36ch' }} title={ids.join(', ')}>
          {n > 4 ? `${ids.slice(0, 3).join(', ')} and ${n - 3} more` : ids.join(', ')}
        </span>
        <span style={{ flex: '1 1 auto' }} />
        <button className="rk-btn" onClick={() => setDialog('assign')} data-testid="inbox-bulk-assign" title="Give them all to one person, or to nobody">
          Assign…
        </button>
        <button className="rk-btn" onClick={() => setDialog('priority')} data-testid="inbox-bulk-priority">
          Priority…
        </button>
        <button className="rk-btn" onClick={() => setDialog('type')} data-testid="inbox-bulk-type">
          Type…
        </button>
        <button className="rk-btn" onClick={() => setDialog('incident')} data-testid="inbox-bulk-incident">
          Incident number…
        </button>
        <button className="rk-btn" onClick={() => setDialog('analyze')} data-testid="inbox-bulk-analyze" title="The Troubleshooter analyzes each now">
          Analyze
        </button>
        <button className="rk-btn" onClick={() => setDialog('close')} data-testid="inbox-bulk-close" title="Close them all with one outcome">
          Close…
        </button>
        <button className="rk-btn" onClick={onClear} title="Unpick every case" data-testid="inbox-bulk-clear">
          Clear
        </button>
      </div>
      {dialog === 'assign' && <AssignDialog n={n} busy={busy} err={err} onCancel={close} onRun={(f) => void run('assign', f)} />}
      {dialog === 'priority' && <PriorityDialog n={n} busy={busy} err={err} onCancel={close} onRun={(f) => void run('priority', f)} />}
      {dialog === 'type' && <TypeDialog n={n} busy={busy} err={err} onCancel={close} onRun={(f) => void run('type', f)} />}
      {dialog === 'incident' && <IncidentDialog n={n} busy={busy} err={err} onCancel={close} onRun={(f) => void run('incident', f)} />}
      {dialog === 'analyze' && (
        <ConfirmDialog title={`Analyze ${nWord(n, 'case', 'cases')}`} confirm="Analyze now" busy={busy} error={err} onCancel={close} onConfirm={() => void run('analyze', {})}>
          <span style={{ color: 'var(--fg2)' }}>The Troubleshooter analyzes each now, within its agent’s budget. A case that names no cluster, or is closed, is refused.</span>
        </ConfirmDialog>
      )}
      {dialog === 'close' && <CloseDialog cases={cases} busy={busy} err={err} onCancel={close} onRun={(a, f, first) => void run(a, f, first)} />}
    </>
  )
}

interface DialogProps {
  n: number
  busy: boolean
  err: string
  onCancel: () => void
  onRun: (f: BulkFields) => void
}

function AssignDialog({ n, busy, err, onCancel, onRun }: DialogProps) {
  const [to, setTo] = useState<'me' | 'nobody' | 'other'>('me')
  const [other, setOther] = useState('')
  const id = to === 'me' ? 'me' : to === 'nobody' ? '' : other.trim()
  return (
    <ConfirmDialog title={`Assign ${nWord(n, 'case', 'cases')}`} confirm={to === 'nobody' ? 'Unassign' : 'Assign'} busy={busy} disabled={to === 'other' && !id} error={err} onCancel={onCancel} onConfirm={() => onRun({ assignee: { id } })}>
      <fieldset style={{ border: 0, padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="inbox-bulk-assign-to">
        <legend className="rk-sr">Assign to</legend>
        {(
          [
            ['me', 'Me'],
            ['nobody', 'Nobody'],
            ['other', 'Someone else'],
          ] as const
        ).map(([v, l]) => (
          <label key={v} className="cs-inline" style={{ color: 'var(--fg)' }}>
            <input type="radio" name="bulk-assign" checked={to === v} onChange={() => setTo(v)} data-testid={`inbox-bulk-assign-${v}`} /> {l}
          </label>
        ))}
      </fieldset>
      {to === 'other' && <input className="cs-in" aria-label="Their user name" placeholder="user name" value={other} onChange={(e) => setOther(e.target.value)} data-testid="inbox-bulk-assign-other" />}
    </ConfirmDialog>
  )
}

function PriorityDialog({ n, busy, err, onCancel, onRun }: DialogProps) {
  const [p, setP] = useState<CasePriority>(2)
  return (
    <ConfirmDialog title={`Priority of ${nWord(n, 'case', 'cases')}`} confirm={`Set P${p}`} busy={busy} error={err} onCancel={onCancel} onConfirm={() => onRun({ priority: p })}>
      <Select label="Priority" value={String(p)} options={PRIORITIES.map((x): [string, string] => [String(x), PRIORITY[x]!])} onChange={(v) => setP(Number(v) as CasePriority)} testId="inbox-bulk-priority-pick" />
      <span className="tb-hint" style={{ fontSize: 12 }}>
        An agent-found case raised to P0 or P1 starts its SLA clock from now; lowered, its clock stops.
      </span>
    </ConfirmDialog>
  )
}

function TypeDialog({ n, busy, err, onCancel, onRun }: DialogProps) {
  const [t, setT] = useState<CaseType>('other')
  return (
    <ConfirmDialog title={`Type of ${nWord(n, 'case', 'cases')}`} confirm={`Set ${CASE_TYPES[t]}`} busy={busy} error={err} onCancel={onCancel} onConfirm={() => onRun({ type: t })}>
      <Select label="Type" value={t} options={CASE_TYPE_IDS.map((x): [string, string] => [x, CASE_TYPES[x]])} onChange={(v) => setT(v as CaseType)} testId="inbox-bulk-type-pick" />
    </ConfirmDialog>
  )
}

function IncidentDialog({ n, busy, err, onCancel, onRun }: DialogProps) {
  const [v, setV] = useState('')
  const bad = incidentError(v)
  return (
    <ConfirmDialog title={`Incident number of ${nWord(n, 'case', 'cases')}`} confirm={v.trim() ? 'Set' : 'Clear it'} busy={busy} disabled={!!bad} error={err} onCancel={onCancel} onConfirm={() => onRun({ incident: v.trim() })}>
      <label className="cs-lbl">
        Incident number (empty clears it)
        <input className="cs-in rk-mono" maxLength={INCIDENT_MAX} placeholder="INC0012345" value={v} onChange={(e) => setV(e.target.value)} data-testid="inbox-bulk-incident-text" />
      </label>
      {bad && <span className="tb-err">{bad}</span>}
    </ConfirmDialog>
  )
}

type First = () => Promise<{ fields: BulkFields; results: BulkResult[]; rest: string[] }>

/** Close with one outcome, and what that outcome needs, once for all. */
function CloseDialog({ cases, busy, err, onCancel, onRun }: { cases: CaseBrief[]; busy: boolean; err: string; onCancel: () => void; onRun: (a: BulkAction, f: BulkFields, first?: First) => void }) {
  const n = cases.length
  const urgent = cases.some((c) => c.priority <= 1)
  const [outcome, setOutcome] = useState<Outcome>('fix')
  const [text, setText] = useState('')
  const [rca, setRCA] = useState<Completion>({ cause: '', prevention: '', text: '' })
  const [saveFix, setSaveFix] = useState(true)
  const [original, setOriginal] = useState('')
  const [reason, setReason] = useState('')
  const [expires, setExpires] = useState('')
  const [known, setKnown] = useState<OpenKnown[] | null>(null)
  const [knownErr, setKnownErr] = useState('')
  const [knownHow, setKnownHow] = useState<'link' | 'create'>('link')
  const [pick, setPick] = useState('')
  const [kiTitle, setKiTitle] = useState(cases[0]?.title ?? '')
  const [workaround, setWorkaround] = useState('')
  // Fixed starts What was done from the picked cases' changes that finished
  // done (CF-49), named by case when there are several, while nothing is
  // written and Fixed is still the choice.
  const [prefilled, setPrefilled] = useState(false)
  const typed = useRef(false)
  const choice = useRef(outcome)
  useEffect(() => {
    choice.current = outcome
  }, [outcome])
  useEffect(() => {
    let gone = false
    const lines = (id: string) =>
      Promise.resolve()
        .then(() => fetchCase(id, true))
        .then((v) => ({ id, lines: v?.fixCommands ?? [] }))
        .catch(() => ({ id, lines: [] as string[] }))
    void Promise.all(cases.map((c) => lines(c.id))).then((got) => {
      const found = got.filter((g) => g.lines.length > 0)
      if (gone || typed.current || choice.current !== 'fix' || found.length === 0) return
      const text = cases.length === 1 ? found[0]!.lines.join('\n') : found.flatMap((g) => g.lines.map((l) => `${g.id}: ${l}`)).join('\n')
      setText((t) => (t.trim() ? t : text))
      setPrefilled(true)
    })
    return () => {
      gone = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  useEffect(() => {
    if (outcome !== 'knownIssue' || known !== null) return
    let gone = false
    fetchOpenKnown().then(
      (k) => !gone && setKnown(k),
      (e: unknown) => !gone && setKnownErr(errText(e)),
    )
    return () => {
      gone = true
    }
  }, [outcome, known])
  const orig = caseNumber(original)
  const picked = new Set(cases.map((c) => c.number))
  const origErr = outcome === 'duplicate' && original.trim() && !orig ? 'Write the case as C-12.' : outcome === 'duplicate' && orig && picked.has(orig) ? 'The original cannot be one of the picked cases.' : ''
  let ready = false
  switch (outcome) {
    case 'fix':
      // Without a cause no RCA goes: the P0 and P1 cases are refused, the others close.
      ready = !!text.trim()
      break
    case 'noAction':
      ready = !!text.trim()
      break
    case 'knownIssue':
      ready = knownHow === 'link' ? !!pick : !!kiTitle.trim()
      break
    case 'duplicate':
      ready = !!orig && !origErr && !!text.trim()
      break
    case 'exception':
      ready = !!reason.trim()
      break
  }
  const confirm = () => {
    switch (outcome) {
      case 'fix':
        return onRun('fix', rca.cause.trim() ? { text: text.trim(), rca: { cause: rca.cause.trim(), prevention: rca.prevention.trim(), ...(rca.text?.trim() ? { text: rca.text.trim() } : {}) }, saveFix } : { text: text.trim(), saveFix })
      case 'noAction':
        return onRun('close', { outcome, explanation: text.trim() })
      case 'duplicate':
        return onRun('duplicate', { outcome: 'duplicate', explanation: text.trim(), original: `C-${orig}` })
      case 'exception':
        return onRun('exception', expires ? { reason: reason.trim(), expires } : { reason: reason.trim() })
      case 'knownIssue':
        if (knownHow === 'link') return onRun('known', { id: pick })
        // Made once, from the first case; the others link the one made.
        return onRun('known', {}, async () => {
          const [head, ...rest] = cases
          try {
            const v = await closeKnown(head!.id, { create: { title: kiTitle.trim(), symptoms: [kiTitle.trim()], workaround: workaround.trim() } })
            const id = v.knownIssue ?? ''
            if (!id) return { fields: {}, results: [{ id: head!.id, ok: false, error: 'The known issue was made, but the case does not name it.' }], rest: [] }
            return { fields: { id }, results: [{ id: head!.id, ok: true }], rest: rest.map((c) => c.id) }
          } catch (e) {
            // Nothing was made: none of the others can link it.
            return { fields: {}, results: cases.map((c) => ({ id: c.id, ok: false, error: c === head ? errText(e) : `The known issue could not be made from ${head!.id}.` })), rest: [] }
          }
        })
    }
  }
  const label = outcome === 'knownIssue' ? (knownHow === 'link' ? `Close as ${pick || 'this known issue'}` : 'Make it and close') : `Close as ${OUTCOMES[outcome].toLowerCase()}`
  return (
    <ConfirmDialog title={`Close ${nWord(n, 'case', 'cases')}`} confirm={label} busy={busy} disabled={!ready} error={err} onCancel={onCancel} onConfirm={confirm}>
      <fieldset style={{ border: 0, padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="inbox-bulk-outcomes">
        <legend className="rk-sr">How they close</legend>
        {CLOSE_OUTCOMES.map((o) => (
          <label key={o} className="cs-inline" style={{ alignItems: 'flex-start', color: 'var(--fg)' }}>
            <input type="radio" name="bulk-close" checked={outcome === o} onChange={() => setOutcome(o)} data-testid={`inbox-bulk-close-${o}`} />
            <span>
              <strong>{OUTCOMES[o]}</strong> <span className="tb-hint">{OUTCOME_HINT[o]}</span>
            </span>
          </label>
        ))}
      </fieldset>
      {outcome === 'fix' && (
        <>
          <label className="cs-lbl">
            What was done (required)
            <textarea
              className="cs-ta"
              value={text}
              maxLength={4000}
              onChange={(e) => {
                typed.current = true
                setText(e.target.value)
              }}
              data-testid="inbox-bulk-text"
            />
          </label>
          {prefilled && (
            <span className="tb-hint" style={{ fontSize: 12 }} data-testid="inbox-bulk-prefilled">
              Filled in from the changes run from the picked cases that finished done: edit it to say what was done.
            </span>
          )}
          <span className="rk-h" style={{ fontSize: 12 }}>
            Root-cause analysis
          </span>
          <span className="tb-hint" data-testid="inbox-bulk-rca-hint">
            {urgent ? 'Required for the P0 and P1 cases.' : 'Optional.'} One RCA covers every case closed here, with what was done as its fix; it is kept on each case closed.
          </span>
          <label className="cs-lbl">
            Cause
            <textarea className="cs-ta" value={rca.cause} maxLength={4000} onChange={(e) => setRCA({ ...rca, cause: e.target.value })} data-testid="inbox-bulk-rca-cause" />
          </label>
          <label className="cs-lbl">
            Prevention
            <textarea className="cs-ta" style={{ minHeight: 56 }} value={rca.prevention} maxLength={4000} onChange={(e) => setRCA({ ...rca, prevention: e.target.value })} data-testid="inbox-bulk-rca-prevention" />
          </label>
          <label className="cs-lbl">
            Notes (optional)
            <textarea className="cs-ta" style={{ minHeight: 56 }} value={rca.text ?? ''} maxLength={4000} onChange={(e) => setRCA({ ...rca, text: e.target.value })} data-testid="inbox-bulk-rca-text" />
          </label>
          {urgent && !rca.cause.trim() && (
            <span className="tb-hint" data-testid="inbox-bulk-rca-refused">
              Without a cause no RCA goes: the P0 and P1 cases are refused; the others close.
            </span>
          )}
          <label className="cs-inline" style={{ alignItems: 'flex-start', color: 'var(--fg)' }}>
            <input type="checkbox" checked={saveFix} onChange={(e) => setSaveFix(e.target.checked)} data-testid="inbox-bulk-save-fix" />
            <span>
              Save fix <span className="tb-hint">One fix for all of them in Mycroft › Fixes, given to the agents for the cases like these.</span>
            </span>
          </label>
        </>
      )}
      {(outcome === 'noAction' || outcome === 'duplicate') && (
        <>
          {outcome === 'duplicate' && (
            <label className="cs-lbl">
              The original case
              <input className="cs-in rk-mono" style={{ width: 140 }} placeholder="C-12" value={original} onChange={(e) => setOriginal(e.target.value)} data-testid="inbox-bulk-original" />
            </label>
          )}
          {origErr && <span className="tb-err">{origErr}</span>}
          <label className="cs-lbl">
            Explanation (required): reporters read it
            <textarea className="cs-ta" style={{ minHeight: 64 }} value={text} maxLength={1000} onChange={(e) => setText(e.target.value)} data-testid="inbox-bulk-text" />
          </label>
        </>
      )}
      {outcome === 'knownIssue' && (
        <>
          <span style={{ display: 'flex', gap: 12 }}>
            <label className="cs-inline" style={{ color: 'var(--fg)' }}>
              <input type="radio" name="bulk-known" checked={knownHow === 'link'} onChange={() => setKnownHow('link')} data-testid="inbox-bulk-known-link" /> Link one
            </label>
            <label className="cs-inline" style={{ color: 'var(--fg)' }}>
              <input type="radio" name="bulk-known" checked={knownHow === 'create'} onChange={() => setKnownHow('create')} data-testid="inbox-bulk-known-create" /> Make one
            </label>
          </span>
          {knownHow === 'link' ? (
            knownErr ? (
              <span className="tb-err">{knownErr}</span>
            ) : known === null ? (
              <span className="tb-hint">Loading…</span>
            ) : known.length === 0 ? (
              <span className="tb-hint">No known issue is open: make one.</span>
            ) : (
              <Select label="Known issue" value={pick} options={[['', 'Choose…'], ...known.map((k): [string, string] => [k.id, `${k.id}: ${k.title}`])]} onChange={setPick} testId="inbox-bulk-known-pick" width="100%" />
            )
          ) : (
            <>
              <label className="cs-lbl">
                Title
                <input className="cs-in" value={kiTitle} maxLength={300} onChange={(e) => setKiTitle(e.target.value)} data-testid="inbox-bulk-known-title" />
              </label>
              <label className="cs-lbl">
                Workaround (optional)
                <input className="cs-in" value={workaround} maxLength={300} onChange={(e) => setWorkaround(e.target.value)} data-testid="inbox-bulk-known-workaround" />
              </label>
              <span className="tb-hint">It is made from {cases[0]?.id}; the others link it.</span>
            </>
          )}
        </>
      )}
      {outcome === 'exception' && (
        <>
          <label className="cs-lbl">
            Justification (required)
            <textarea className="cs-ta" style={{ minHeight: 64 }} value={reason} maxLength={1000} onChange={(e) => setReason(e.target.value)} data-testid="inbox-bulk-reason" />
          </label>
          <label className="cs-lbl">
            Last day (optional)
            <input className="cs-in" type="date" style={{ width: 180 }} value={expires} onChange={(e) => setExpires(e.target.value)} data-testid="inbox-bulk-expires" />
          </label>
        </>
      )}
      <span className="tb-hint" style={{ fontSize: 12 }}>
        Each case is checked by the rules of its own Close: a case this outcome does not fit is refused, with why, and stays as it is.
      </span>
    </ConfirmDialog>
  )
}

/** What each case did with the action: done, or refused with why. */
export function BulkResults({ outcome, onClose }: { outcome: BulkOutcome; onClose: () => void }) {
  const ok = outcome.results.filter((r) => r.ok)
  const refused = outcome.results.filter((r) => !r.ok)
  return (
    <ConfirmDialog title={`${outcome.action}: ${ok.length} of ${outcome.results.length} done`} confirm="Close" alone onCancel={onClose} onConfirm={onClose}>
      <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 4, maxHeight: 320, overflowY: 'auto' }} data-testid="inbox-bulk-results">
        {outcome.results.map((r) => (
          <li key={r.id} style={{ display: 'flex', gap: 8, alignItems: 'baseline' }} data-testid="inbox-bulk-result" data-id={r.id} data-ok={r.ok ? 'true' : 'false'}>
            <span className="rk-tag" data-tone={r.ok ? 'ok' : 'err'}>
              {r.ok ? 'done' : 'refused'}
            </span>
            <span className="rk-mono">{r.id}</span>
            {!r.ok && (
              <span style={{ color: 'var(--fg2)' }}>
                {r.error || 'Refused.'}
                {r.offers?.length ? ' Open the case to pick what fits instead.' : ''}
              </span>
            )}
          </li>
        ))}
      </ul>
      {refused.length > 0 && <span className="tb-hint">The refused cases stay picked.</span>}
    </ConfirmDialog>
  )
}
