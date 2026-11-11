// A case (Phase 12, CW-07 to CW-09; since wave 5, CU-09 to CU-11; since
// wave 6, one flow for findings and cases): one drawer for every case,
// whatever opened it (a person's report, an agent's finding, an alert, by
// hand), at #/aiops/inbox/case/12. It shows what the case is about (the
// cluster, the object, what opened it, the agents' key and how often they
// saw it), the links to other cases (regression of, possible duplicate of,
// same as, related), how a case like it was closed before, the reporter's
// words and the conversation, the analysis and the fix it suggests with
// Run in Watson, the changes that resolve it and, once one applied the
// fix, where its check stands (wave 6b, CaseChanges.tsx), the RCA and the
// history. For those who work the cases: the priority and assignee, a
// reply or note, Link a change, and one Close… (since wave 6d, CF-42)
// that lists the outcomes the server allows this case (allowedOutcomes),
// each with what it needs under it: Fixed (what was done, and an RCA,
// required at P0 and P1, whose fix is what was done; since wave 6e the RCA
// stays on the case, no record of its own), Duplicate (the original is checked first, and
// what fits instead is offered when it is closed; agent-found cases too),
// No action needed (with an explanation; since CF-90 it replaces Not a
// problem and No fix needed, for every case), Known issue (link one or
// make one) and Exception; and Reopen with a reason. Since
// wave 6c: the case's type (CF-37) and incident number (CF-36) in its
// header, set by those who work the cases; who opened it and who did each
// thing in its history, person, agent or system (CF-35); and "Analysis
// queued" while the case analysis job has it in its queue (CF-32). Since
// wave 6f (CF-46 to CF-49) the drawer opens near full screen, the case on
// the left and, on the right, Troubleshoot: Sherlock and Watson on the
// case (CaseTroubleshoot.tsx), tabs Case | Troubleshoot on a narrow
// screen; the case lists the commands run from it, and Fixed starts What
// was done from the changes among them that finished done. Since CF-58
// to CF-60: the priority, type and assignee are set in the header, at the
// right of its facts line, and there is no Ask Sherlock (Sherlock is on
// the right already); the case has one draft, shared by everyone who
// works it, kept by Save, refused when someone changed it since (Discard
// my changes or Overwrite); and its title, object, incident number,
// detail and cluster (by name, from a list) are edited in place, under
// the same rule.
import { useCallback, useContext, useEffect, useRef, useState, type Dispatch, type ReactNode, type SetStateAction } from 'react'
import { useAlerts } from '../../api/acks'
import { actorFull, actorKind, actorName } from '../../api/actor'
import { CallError } from '../../api/call'
import {
  analyzeCase,
  CASE_TYPE_IDS,
  CASE_TYPES,
  caseTypeText,
  incidentError,
  INCIDENT_MAX,
  applyOffer,
  caseHref,
  caseNumber,
  caseShownStatus,
  caseStage,
  caseStatusText,
  caseStatusTone,
  checkDuplicate,
  closeCase,
  closeException,
  closeKnown,
  discardCaseDraft,
  draftRCA,
  fetchCase,
  fetchOpenKnown,
  fixCase,
  fixPrefill,
  keepDraft,
  OUTCOMES,
  reopenCase,
  sayOnCase,
  sendCaseDraft,
  SOURCE,
  unlinkChange,
  updateCase,
  verifiedText,
  waitText,
  type Case,
  type CaseChange,
  type CaseDraft,
  type CaseField,
  type CaseMessage,
  type CasePriority,
  type CaseType,
  type CloseOutcome,
  type Completion,
  type DraftConflict,
  type DuplicateCheck,
  type FieldConflict,
  type Offer,
  type OpenKnown,
  type Outcome,
  type RCADraft,
} from '../../api/casework'
import { fixHref } from '../../api/fixes'
import { exceptionHref } from '../../api/lessons'
import { LiveContext } from '../../api/live'
import { alertKeyOf, isActive } from '../../lib/alerts'
import { useAIOpsItem } from '../../lib/aiopsItem'
import { fmtAge, fmtClock, fmtTs } from '../../lib/format'
import { adminHref, clusterHref, useRoute } from '../../lib/router'
import { openWatson } from '../../lib/watsonFill'
import { Drawer } from '../Drawer'
import { Select } from '../Select'
import { knownHref } from './FindingParts'
import { runHref } from './jobsRoute'
import { CaseChanges, Verification } from './CaseChanges'
import { CaseCommands, CaseTroubleshoot, type CaseRun } from './CaseTroubleshoot'
import { RunInWatson } from '../Sherlock'
import { ConflictDialog, InPlace, useConflict, type ConflictChoice } from './InPlace'
import { NewCaseDrawer, prefillOf } from './NewCase'
import { ObjectLink } from './ObjectLink'
import { errText, KNOWN_STATUS, knownTone, PRIORITIES, PRIORITY, priorityTone, Tag, who } from './parts'

/** How often a case being analysed is read again. */
const EVERY = 5000

/** The time now, read again every minute: for how long cases have been open. */
export function useNow(every = 60_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), every)
    return () => clearInterval(t)
  }, [every])
  return now
}

/** Where the SLA's targets are set (CU-06). */
export const slaHref = () => adminHref('settings', ['sla'])

/** What each way of closing means, in a line, as Close offers them. */
export const CLOSE_HINT: Record<CloseOutcome, string> = {
  duplicate: 'The same problem as another open case: its reporter and history go to that case.',
  noAction: 'Not a problem, or it went away: say why.',
}

/** The outcomes one Close… lists, in its order, as far as the case allows them (CF-42). */
export const CLOSE_ORDER: Outcome[] = ['fix', 'duplicate', 'noAction', 'knownIssue', 'exception']

/** An outcome's line beside its choice in Close…. */
function outcomeHint(o: Outcome, c: Case): string {
  switch (o) {
    case 'fix':
      return c.rcaRequired ? `Say what was done, with a root-cause analysis (required at P${c.priority}).` : 'Say what was done; a root-cause analysis is optional.'
    case 'knownIssue':
      return 'Link a known issue, or make one: the case closes as that known issue.'
    case 'exception':
      return 'Intended or acceptable here: the agents stop reporting it.'
    default:
      return CLOSE_HINT[o]
  }
}

const isOutcome = (v: string | undefined): v is Outcome => !!v && (CLOSE_ORDER as string[]).includes(v)

const minutes = (m: number) => (m <= 0 ? 'none' : m % 1440 === 0 ? `${m / 1440} d` : m % 60 === 0 ? `${m / 60} h` : `${m} min`)

/** Today in the browser, as a date input reads it. */
function today(): string {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

/** A case named "C-12", linked to its drawer. */
export function CaseLink({ id, testId }: { id: string; testId?: string }) {
  return (
    <a className="rk-link rk-mono" href={caseHref(id)} data-testid={testId}>
      {/^c-/i.test(id) ? id.toUpperCase() : `C-${id}`}
    </a>
  )
}

/** What Close… is being written with: the outcome picked, a close's explanation and a duplicate's original. */
interface CloseWriting {
  outcome: Outcome | ''
  explanation: string
  original: string
}

/** What is being written on the case: its shared draft, kept by Save (CF-59). */
interface Writing {
  reply: string
  fix: string
  close: CloseWriting
  rca: Completion
}

const fromDraft = (d?: CaseDraft): Writing => ({
  reply: d?.reply ?? '',
  fix: d?.fix ?? '',
  close: { outcome: isOutcome(d?.close?.outcome) ? d.close.outcome : '', explanation: d?.close?.explanation ?? '', original: d?.close?.original ?? '' },
  rca: { cause: d?.rca?.cause ?? '', prevention: d?.rca?.prevention ?? '', text: d?.rca?.text ?? '' },
})

const blank = (c?: Case): Writing => fromDraft(c?.draft)

/** Whether a draft holds anything. */
const drafted = (d?: CaseDraft) => !!d && !!(d.reply || d.fix || d.close?.explanation || d.close?.outcome || d.rca?.cause || d.rca?.prevention || d.rca?.text)

/** A draft's parts as they read, for a conflict and for those who only read the case. */
function DraftParts({ d, testId }: { d: CaseDraft; testId?: string }) {
  const parts: [string, string | undefined][] = [
    ['Reply or note', d.reply],
    ['What was done', d.fix],
    [d.close?.outcome && isOutcome(d.close.outcome) ? `Close as ${OUTCOMES[d.close.outcome].toLowerCase()}` : 'Close', d.close?.explanation],
    ['Cause', d.rca?.cause],
    ['Prevention', d.rca?.prevention],
    ['Notes', d.rca?.text],
  ]
  const shown = parts.filter(([, v]) => v?.trim())
  if (shown.length === 0) return <span className="tb-hint">Empty: nothing is written in it.</span>
  return (
    <dl className="cs-kv" data-testid={testId}>
      {shown.map(([k, v]) => (
        <span key={k} style={{ display: 'contents' }}>
          <dt>{k}</dt>
          <dd style={{ whiteSpace: 'pre-wrap', maxHeight: 160, overflow: 'auto' }}>{v}</dd>
        </span>
      ))}
    </dl>
  )
}

/** "Pat changed this draft at 14:02": a conflict's title. */
const changedBy = (by: string | undefined, what: string, at: string | undefined) => `${by || 'Someone'} changed ${what}${at ? ` at ${fmtClock(at)}` : ''}`

/** How a field edited in place is named in a conflict. */
const FIELD_NAME: Record<CaseField, string> = { title: 'the title', detail: 'the detail', cluster: 'the cluster', resource: 'what it is about', incident: 'the incident number' }

const hasRCA = (r: Completion) => !!(r.cause.trim() || r.prevention.trim() || r.text?.trim())

const asDraft = (w: Writing): CaseDraft => ({
  reply: w.reply.trim() || undefined,
  fix: w.fix.trim() || undefined,
  close: w.close.outcome || w.close.explanation.trim() ? { outcome: w.close.outcome, explanation: w.close.explanation.trim(), original: caseNumber(w.close.original) ? `C-${caseNumber(w.close.original)}` : undefined } : undefined,
  rca: hasRCA(w.rca) ? { cause: w.rca.cause, prevention: w.rca.prevention, text: w.rca.text } : undefined,
})

type Panel = '' | 'close' | 'reopen'

interface Props {
  /** The case: "C-12", "12", or "new" for New Case. */
  id: string
  onClose: () => void
  /** After a change: the list reads again. */
  onChanged?: () => void
  onToast?: (msg: string) => void
}

export function CaseDrawer(props: Props) {
  // #/aiops/inbox/case/new is New Case, with what its address prefills.
  if (props.id === 'new') {
    const q = typeof window === 'undefined' ? '' : (window.location.hash.split('?')[1] ?? '')
    return <NewCaseDrawer prefill={prefillOf(new URLSearchParams(q))} onClose={props.onClose} onCreated={props.onChanged} onToast={props.onToast} />
  }
  return <OneCase {...props} />
}

function OneCase({ id, onClose, onChanged, onToast }: Props) {
  const [c, setC] = useState<Case | null>(null)
  const [err, setErr] = useState('')
  const [panel, setPanel] = useState<Panel>('')
  const [writing, setWriting] = useState<Writing>(blank())
  // The stamp of the shared draft this editor started from (CF-59): every Save sends it.
  const [base, setBase] = useState('')
  const [busy, setBusy] = useState(false)
  const [actErr, setActErr] = useState('')
  const [conflict, askConflict, endConflict] = useConflict()
  // Run in Watson on the suggested fix: the command goes to the drawer's Watson (CF-48).
  const [run, setRun] = useState<CaseRun | null>(null)
  const [watsonHere, setWatsonHere] = useState(false)
  const toWatson = (command: string) => setRun((r) => ({ command, n: (r?.n ?? 0) + 1 }))
  const seeded = useRef('')
  const now = useNow()
  // Sherlock is told which case "this" is, and the dock follows its cluster (IB-11).
  useAIOpsItem(c ? { kind: 'case', id: c.id, cluster: c.cluster || undefined, summary: `${c.title} · P${c.priority} · ${caseStatusText(c)}${caseStage(c) ? ` (${caseStage(c)!.tag.toLowerCase()})` : ''}` } : { kind: 'case', id })
  const load = useCallback(
    (background = false) =>
      fetchCase(id, background).then(
        (v) => {
          setC(v)
          setErr('')
          // What is being written comes back from the case's draft once.
          if (seeded.current !== v.id) {
            seeded.current = v.id
            setWriting(blank(v))
            setBase(v.draft?.updated ?? '')
          }
        },
        (e: unknown) => setErr(errText(e)),
      ),
    [id],
  )
  useEffect(() => {
    void load()
  }, [load])
  // While the Troubleshooter works on it, read it again until it is done.
  const analyzing = !!c?.analyzing
  useEffect(() => {
    if (!analyzing) return
    const t = setInterval(() => void load(true), EVERY)
    return () => clearInterval(t)
  }, [analyzing, load])
  // sent: the action sent a part of the draft, which the server took out of it for everyone and stamped anew (CF-59).
  const act = async (f: () => Promise<Case>, done: (v: Case) => string, after?: () => void, sent?: boolean) => {
    setBusy(true)
    setActErr('')
    try {
      const v = await f()
      setC(v)
      if (sent) setBase(v.draft?.updated ?? '')
      onChanged?.()
      onToast?.(done(v))
      after?.()
      return true
    } catch (e) {
      setActErr(errText(e))
      return false
    } finally {
      setBusy(false)
    }
  }
  const saved = (v: Case, msg: string) => {
    setC(v)
    onChanged?.()
    onToast?.(msg)
  }
  // Save: the case's one draft, for everyone (CF-59). A draft someone changed since this editor started from it asks: theirs, or mine over it.
  const keep = async () => {
    if (!c) return
    const mine = asDraft(writing)
    const done = (v: Case) => {
      setBase(v.draft?.updated ?? '')
      saved(v, drafted(v.draft) ? `The draft on ${v.id} is saved for everyone who works it.` : `The draft on ${v.id} is empty.`)
    }
    setBusy(true)
    setActErr('')
    try {
      done(await keepDraft(c.id, mine, base))
    } catch (e) {
      const body = e instanceof CallError && e.status === 409 ? (e.body as Partial<DraftConflict> | undefined) : undefined
      if (!body?.draft) {
        setActErr(errText(e))
        return
      }
      const theirs = body.draft
      const choice: ConflictChoice = await askConflict(changedBy(theirs.by, 'this draft', theirs.updated), <DraftParts d={theirs} testId="case-conflict-draft" />)
      if (choice === 'discard') {
        setWriting(fromDraft(theirs))
        setBase(theirs.updated ?? '')
        void load(true)
      } else if (choice === 'overwrite') {
        try {
          done(await keepDraft(c.id, mine, theirs.updated ?? '', true))
        } catch (e2) {
          setActErr(errText(e2))
        }
      }
    } finally {
      setBusy(false)
    }
  }
  // A field edited in place (CF-60): sent with the value it started from; one changed since asks the same choice.
  const saveField = async (field: CaseField, next: string, from: string): Promise<boolean> => {
    if (!c) return false
    const change = { [field]: next } as CaseChange
    setBusy(true)
    setActErr('')
    try {
      saved(await updateCase(c.id, { ...change, base: { [field]: from } }), `${c.id} is saved.`)
      return true
    } catch (e) {
      const body = e instanceof CallError && e.status === 409 ? (e.body as Partial<FieldConflict> | undefined) : undefined
      if (typeof body?.current !== 'string') {
        setActErr(errText(e))
        return false
      }
      const name = FIELD_NAME[body.field ?? field] ?? 'it'
      const choice = await askConflict(
        changedBy(body.by, name, body.at),
        <span>
          It is now:{' '}
          <strong style={{ whiteSpace: 'pre-wrap' }} data-testid="case-conflict-current">
            {body.current || '(empty)'}
          </strong>
        </span>,
      )
      if (choice === 'discard') {
        await load(true)
        return true
      }
      if (choice === 'overwrite') {
        try {
          saved(await updateCase(c.id, { ...change, force: true }), `${c.id} is saved.`)
          return true
        } catch (e2) {
          setActErr(errText(e2))
        }
      }
      return false
    } finally {
      setBusy(false)
    }
  }
  const title = c ? c.title : `Case ${id}`
  const kicker = c ? (
    <span data-testid="case-kicker">
      Case {c.id} ·{' '}
      {c.source.kind === 'report'
        ? `reported by ${who(c.reporter) || actorName(c.createdBy)}${c.filedBy ? `, filed by ${who(c.filedBy)}` : ''}`
        : c.agentFound
          ? actorKind(c.createdBy) === 'agent'
            ? `found by ${actorName(c.createdBy)}${c.source.job ? ` (${c.source.job})` : ''}`
            : `found by ${c.source.job || 'an agent'}`
          : `opened by ${actorName(c.createdBy) || 'someone'}`}{' '}
      · {fmtTs(c.created)}
    </span>
  ) : (
    `Case ${id}`
  )
  const open = !!c && c.status !== 'closed'
  const stage = c && caseStage(c)
  // Those who work the case set the priority and type at the right of this line (CF-58); the others read them as tags.
  const facts = c && (
    <>
      <span style={{ display: 'flex', gap: 6, flexWrap: 'wrap', flex: '1 1 320px', minWidth: 0 }}>
        {!c.mayChange && (
          <Tag tone={priorityTone(c.priority)} testId="case-priority">
            {PRIORITY[c.priority] ?? `P${c.priority}`}
          </Tag>
        )}
        <Tag tone={caseStatusTone(caseShownStatus(c), c.outcome)} testId="case-status">
          {caseStatusText(c)}
        </Tag>
        {stage && (
          // What it waits for now, beside its status (CF-107).
          <span className="rk-tag" data-tone={stage.tone} title={stage.hint} data-testid="case-stage" data-stage={stage.id}>
            {stage.tag}
          </span>
        )}
        <Tag tone="muted" testId="case-source-kind">
          {SOURCE[c.source.kind] ?? c.source.kind}
        </Tag>
        {c.type && !c.mayChange && (
          <span className="rk-tag" data-tone="muted" title="What kind of problem it is" data-testid="case-type" data-type={c.type}>
            {caseTypeText(c.type)}
          </span>
        )}
        {c.incident && (
          <span className="rk-tag rk-mono" data-tone="info" title="The incident number elsewhere" data-testid="case-incident">
            {c.incident}
          </span>
        )}
        {c.seen && c.seen.count > 1 && (
          <Tag tone="info" testId="case-seen-tag">
            seen ×{c.seen.count}
          </Tag>
        )}
        {open && (
          <Tag tone="muted" testId="case-age">
            age: {fmtAge(c.created, now)}
          </Tag>
        )}
        {open && !c.assignee && <Tag tone="warn">unassigned</Tag>}
        {open && !c.slaApplies && (
          <span className="rk-tag" data-tone="muted" data-testid="case-no-sla" title="Agent-found cases have SLA targets only at P0 and P1">
            no SLA
          </span>
        )}
        {c.slaApplies && c.late?.response && <LateTag what="First response" minutes={c.sla?.target.responseMinutes} />}
        {c.slaApplies && c.late?.resolve && <LateTag what="Resolution" minutes={c.sla?.target.resolveMinutes} />}
      </span>
      {c.mayChange && <CaseControls c={c} busy={busy} act={act} />}
    </>
  )
  const back = () => {
    setPanel('')
    setActErr('')
  }
  // Close… opened on one outcome: Fix from where a fix is asked for, Exception from its suggestion.
  const closeAs = (o: Outcome) => {
    setWriting((w) => ({ ...w, close: { ...w.close, outcome: o } }))
    setPanel('close')
  }
  return (
    <Drawer
      label={`Case ${c?.id ?? id}`}
      kicker={kicker}
      title={title}
      heading={c?.mayChange ? <InPlace value={c.title} label="Title" testId="case-title" maxLength={200} check={(v) => (v.trim() ? '' : 'Give the case a title.')} onSave={(next, from) => saveField('title', next, from)} /> : undefined}
      facts={facts}
      onClose={onClose}
      width={800}
      size="full"
      split={c ? { label: 'Troubleshoot', main: 'Case', content: <CaseTroubleshoot c={c} onToast={onToast} onCommands={() => void load(true)} run={run} onRan={() => setRun(null)} onWatson={setWatsonHere} /> } : undefined}
      sideOn={run?.n}
      copy={false}
      testId="case-drawer"
      prose
      footer={c && c.mayChange ? <CaseFooter c={c} busy={busy} panel={panel} onPanel={setPanel} onKeep={() => void keep()} err={panel === '' ? actErr : ''} /> : undefined}
    >
      {err && <p className="tb-err">{err}</p>}
      {!c && !err && <p className="tb-hint">Loading…</p>}
      {c && (
        <>
          {panel === 'close' && (
            <CloseForm
              c={c}
              busy={busy}
              writing={writing}
              onWriting={setWriting}
              onCancel={back}
              act={act}
              err={actErr}
              onDone={() => {
                setPanel('')
                setWriting((w) => ({ ...w, fix: '', rca: blank().rca, close: blank().close }))
              }}
            />
          )}
          {panel === 'reopen' && <ReopenForm c={c} busy={busy} onCancel={back} act={act} err={actErr} onDone={() => setPanel('')} />}
          {panel === '' && (
            <CaseBody
              c={c}
              onRun={watsonHere ? toWatson : undefined}
              busy={busy}
              writing={writing}
              onWriting={setWriting}
              act={act}
              onField={saveField}
              onCloseAs={closeAs}
              onAnalyze={() =>
                void act(
                  () => analyzeCase(c.id),
                  (x) => `The Troubleshooter analyzes ${x.id} again.`,
                )
              }
              onLinked={(v) => {
                setC(v)
                onChanged?.()
                onToast?.(`Linked: the change resolves ${v.id}.`)
              }}
            />
          )}
        </>
      )}
      {conflict && <ConflictDialog conflict={conflict} onDone={endConflict} />}
    </Drawer>
  )
}

/** A target of the SLA passed, linking to where it is set. */
function LateTag({ what, minutes: m }: { what: string; minutes?: number }) {
  return (
    <a href={slaHref()} className="rk-tag" data-tone="err" data-testid="case-late" title={`${what} target${m ? ` (${minutes(m)})` : ''} passed: see Settings › SLA`} style={{ textDecoration: 'none' }}>
      {what} late{m ? ` · target ${minutes(m)}` : ''}
    </a>
  )
}

type Act = (f: () => Promise<Case>, done: (v: Case) => string, after?: () => void, sent?: boolean) => Promise<boolean>

function Sec({ title, children, testId }: { title: string; children: ReactNode; testId?: string }) {
  return (
    <div className="cs-sec" data-testid={testId}>
      <span className="rk-h">{title}</span>
      {children}
    </div>
  )
}

function SourceText({ c }: { c: Case }) {
  switch (c.source.kind) {
    case 'finding':
      return (
        <>
          an agent's finding
          {c.source.run ? (
            <>
              {' in '}
              <a className="rk-link" href={runHref(c.source.run)}>
                {c.source.run}
              </a>
            </>
          ) : null}
          {c.source.job ? <span className="tb-sub">{c.source.job}</span> : null}
        </>
      )
    case 'alert':
      return <AlertSource c={c} />
    case 'report':
      return (
        <>
          a report by {who(c.reporter) || actorName(c.createdBy)}
          {c.filedBy ? `, filed by ${who(c.filedBy)}` : ''}
        </>
      )
    default:
      return <>by hand, by {actorName(c.createdBy) || 'someone'}</>
  }
}

/** The key of the alert a case was opened from: kept since CF-84, from its object before. */
function sourceKey(c: Case): string {
  return c.source.alertKey || alertKeyOf(c.object)
}

/**
 * The alert a case was opened from (CF-84): by its reason, linked to its
 * cluster's Alerts tab filtered to its object (the case's cluster, whichever
 * is open), and whether it is still going or has cleared, when the console
 * has that cluster's alerts (the cluster open). A case opened before keeps
 * the name it was given.
 */
function AlertSource({ c }: { c: Case }) {
  const live = useContext(LiveContext)
  const route = useRoute()
  const key = sourceKey(c)
  const name = c.object?.name || key.slice(Math.max(key.lastIndexOf('/'), key.indexOf(':')) + 1)
  const here = !!live && !!c.cluster && c.cluster === route.cluster && !!key
  return (
    <>
      the alert <span data-testid="case-alert-name">{c.source.alert}</span>
      {c.source.severity ? ` (${c.source.severity})` : ''}
      {c.cluster && name && (
        <>
          {' · '}
          <a className="rk-link" href={clusterHref(c.cluster, 'alerts', [], { q: name })} title={`Open the Alerts tab of ${c.clusterName || c.cluster}, filtered to ${name}`} data-testid="case-alert-link">
            on the Alerts tab
          </a>
        </>
      )}
      {here && <AlertState alertKey={key} />}
    </>
  )
}

/** Whether a case's alert is still going or has cleared, from the live alerts of the cluster open (CF-84). */
function AlertState({ alertKey }: { alertKey: string }) {
  const { rows, loaded } = useAlerts()
  if (!loaded) return null
  const row = rows.find((r) => r.key === alertKey)
  const going = !!row && isActive(row)
  return (
    <>
      {' · '}
      <span
        className="rk-tag"
        data-tone={going ? 'err' : 'ok'}
        title={going ? 'The alert is still on the Alerts tab' : 'The alert is no longer on the Alerts tab, or has recovered'}
        data-testid="case-alert-state"
        data-going={going ? 'true' : 'false'}
      >
        {going ? 'still going' : 'cleared'}
      </span>
    </>
  )
}

/** The links to other cases: regression of, possible duplicate of, same as, duplicate of, related. */
function Links({ c }: { c: Case }) {
  const related = c.related ?? []
  if (!c.regressionOf && !c.possibleDuplicateOf && !c.sameAs && !c.duplicateOf && related.length === 0) return null
  return (
    <Sec title="Other cases" testId="case-links">
      {c.regressionOf && (
        <span data-testid="case-regression">
          A regression of <CaseLink id={c.regressionOf} />: that case was closed as fixed, and the problem is back.
        </span>
      )}
      {c.possibleDuplicateOf && c.status !== 'closed' && (
        <span data-testid="case-possible-duplicate">
          Possibly the same problem as <CaseLink id={c.possibleDuplicateOf} /> (same object, like title). If it is, close this one as its duplicate.
        </span>
      )}
      {c.sameAs && (
        <span data-testid="case-same-as">
          The same problem as <CaseLink id={c.sameAs} />, kept apart.
        </span>
      )}
      {c.duplicateOf && (
        <span data-testid="case-duplicate-of">
          A duplicate of <CaseLink id={c.duplicateOf} />: follow that case.
        </span>
      )}
      {related.length > 0 && (
        <span data-testid="case-related">
          Related:{' '}
          {related.map((r, i) => (
            <span key={r.id}>
              {i > 0 ? ', ' : ''}
              <CaseLink id={r.id} /> {r.title}
            </span>
          ))}
        </span>
      )}
    </Sec>
  )
}

/** How the case was closed: its outcome, the explanation, and what it points to. */
function Closed({ c }: { c: Case }) {
  if (c.status !== 'closed') return null
  return (
    <Sec title="How it was closed" testId="case-closed">
      <span>
        <strong data-testid="case-outcome">{c.outcome ? OUTCOMES[c.outcome] : 'Closed'}</strong>
        {c.closedAt ? <span className="tb-sub">{fmtTs(c.closedAt)}</span> : null}
      </span>
      {c.explanation && (
        <span style={{ whiteSpace: 'pre-wrap' }} data-testid="case-reason">
          {c.explanation}
        </span>
      )}
      {c.outcome === 'knownIssue' && c.knownIssue && (
        <span>
          Known issue{' '}
          <a className="rk-link" href={knownHref(c.knownIssue)} data-testid="case-closed-known">
            {c.knownIssue}
          </a>
          {c.agentFound ? ': the agents keep checking it quietly.' : ''}
        </span>
      )}
      {c.outcome === 'exception' && c.exception && (
        <span>
          Exception{' '}
          <a className="rk-link" href={exceptionHref(c.exception)} data-testid="case-closed-exception">
            {c.exception}
          </a>
          : the agents no longer report it.
        </span>
      )}
      {c.outcome === 'fix' && c.fix && (
        <span>
          Saved as{' '}
          <a className="rk-link rk-mono" href={fixHref(c.fix)} data-testid="case-saved-fix">
            {c.fix}
          </a>{' '}
          in Mycroft › Fixes.
        </span>
      )}
      {c.outcome === 'fix' && (
        <span className="tb-hint" data-testid="case-verified">
          {fixCheckText(c)}
        </span>
      )}
    </Sec>
  )
}

/**
 * Where the check of a case closed as fixed stands (I-1): what the run
 * that verified it re-checked; how long it has waited for a run that
 * re-checks it; or, for a case a person opened, nothing.
 */
function fixCheckText(c: Case): string {
  if (c.verifiedBy) return verifiedText(c.verifiedBy, fmtTs)
  if (c.verified) return `Verified ${fmtTs(c.verified)}: a later run no longer saw it.`
  if (!c.agentFound) return ''
  const next = 'The next run of the job re-checks that the fix holds.'
  return c.verifyWait ? `${next} ${waitText(c.verifyWait, fmtTs)}` : next
}

/** Saves a field edited in place from the value it started with (CF-60); false keeps it open. */
type OnField = (field: CaseField, next: string, from: string) => Promise<boolean>

function CaseBody({
  c,
  onRun,
  busy,
  writing,
  onWriting,
  act,
  onField,
  onCloseAs,
  onAnalyze,
  onLinked,
}: {
  c: Case
  onRun?: (command: string) => void
  busy: boolean
  writing: Writing
  onWriting: (w: Writing) => void
  act: Act
  onField: OnField
  onCloseAs: (o: Outcome) => void
  onAnalyze: () => void
  onLinked: (v: Case) => void
}) {
  const a = c.analysis
  const commands = a?.fix.commands ?? []
  const inv = c.investigation
  const open = c.status !== 'closed'
  const allowed = new Set(c.allowedOutcomes ?? [])
  const edit = c.mayChange
  const report = c.source.kind === 'report'
  const objectShown = c.resource ? c.object && c.cluster ? <ObjectLink cluster={c.cluster} resource={c.resource} /> : <span>{c.resource}</span> : null
  return (
    <>
      {c.previous && (
        <div className="rk-callout" data-testid="case-previous" style={{ flexDirection: 'column', gap: 4 }}>
          <span>
            <strong>Seen before.</strong> <CaseLink id={c.previous.case} /> was closed as {OUTCOMES[c.previous.outcome]?.toLowerCase() ?? c.previous.outcome} by {c.previous.by}, {fmtTs(c.previous.at)}:
          </span>
          <span style={{ whiteSpace: 'pre-wrap' }}>{c.previous.explanation}</span>
        </div>
      )}
      {c.suggestException && open && (
        <div className="rk-callout" data-tone="warn" data-testid="case-suggest-exception" style={{ alignItems: 'center', flexWrap: 'wrap' }}>
          <span style={{ flex: '1 1 300px' }}>This validation rule was closed as no action needed three times. If it is acceptable here, an exception stops the agents reporting it.</span>
          {c.mayChange && allowed.has('exception') && (
            <button className="rk-btn" data-size="sm" disabled={busy} onClick={() => onCloseAs('exception')} data-testid="case-suggest-exception-open">
              Accept as exception…
            </button>
          )}
        </div>
      )}

      <Verification c={c} busy={busy} onFix={() => onCloseAs('fix')} />

      <Closed c={c} />

      <Sec title="What it is about">
        <dl className="cs-kv">
          <dt>Cluster</dt>
          <dd data-testid="case-cluster">{edit ? <ClusterPick c={c} busy={busy} onField={onField} /> : <ClusterName c={c} />}</dd>
          <dt>About</dt>
          <dd data-testid="case-object">
            {edit ? (
              <InPlace
                value={c.resource ?? ''}
                label="What it is about"
                testId="case-resource"
                maxLength={300}
                placeholder="shop/Deployment/web, Node/worker-3, the disk of db-1 at 90%…"
                empty={<span className="tb-hint">nothing named</span>}
                onSave={(next, from) => onField('resource', next, from)}
              >
                {c.object && c.cluster && c.resource ? objectShown : undefined}
              </InPlace>
            ) : (
              (objectShown ?? <span className="tb-hint">nothing named</span>)
            )}
          </dd>
          <dt>Opened from</dt>
          <dd data-testid="case-source">
            <SourceText c={c} />
          </dd>
          {c.createdBy && (
            <>
              <dt>Created by</dt>
              <dd data-testid="case-created-by">{actorFull(c.createdBy)}</dd>
            </>
          )}
          {(c.incident || edit) && (
            <>
              <dt>Incident</dt>
              <dd className="rk-mono" data-testid="case-incident-number">
                {edit ? (
                  <InPlace
                    value={c.incident ?? ''}
                    label="Incident number"
                    testId="case-incident"
                    mono
                    maxLength={INCIDENT_MAX}
                    placeholder="INC0012345"
                    check={incidentError}
                    empty={<span className="tb-hint">none</span>}
                    onSave={(next, from) => onField('incident', next, from)}
                  />
                ) : (
                  c.incident
                )}
              </dd>
            </>
          )}
          {c.key && (
            <>
              <dt>Validation rule</dt>
              <dd data-testid="case-key">
                <span className="rk-mono">{c.key.check}</span>
                {c.ruleMeaning && (
                  <span className="tb-sub" data-testid="case-rule-meaning">
                    {c.ruleMeaning}
                  </span>
                )}
                <span className="tb-sub">
                  {c.key.object ? `on ${c.key.object}` : 'cluster-wide'} · <span title={c.key.cluster}>{keyClusterName(c)}</span>
                </span>
              </dd>
            </>
          )}
          {c.seen && (
            <>
              <dt>Seen</dt>
              <dd data-testid="case-seen">
                ×{c.seen.count} · last {fmtTs(c.seen.last)}
                <span className="tb-sub">first {fmtTs(c.seen.first)}; later runs that see it again add to this case</span>
              </dd>
            </>
          )}
          <dt>Assigned to</dt>
          <dd data-testid="case-assignee">{c.assignee ? who(c.assignee) : <span className="tb-hint">nobody</span>}</dd>
          {c.serviceName && (
            <>
              <dt>Service</dt>
              <dd>{c.serviceName}</dd>
            </>
          )}
          {c.known && (
            <>
              <dt>Known issue</dt>
              <dd data-testid="case-known">
                <span style={{ display: 'inline-flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
                  <a className="rk-link" href={knownHref(c.known.id)}>
                    {c.known.id}: {c.known.title}
                  </a>
                  <Tag tone={knownTone(c.known.status)}>{KNOWN_STATUS[c.known.status]}</Tag>
                  {c.matchConfidence ? <span className="tb-hint">{c.matchConfidence}% match</span> : null}
                </span>
              </dd>
            </>
          )}
          {open && (
            <>
              <dt>SLA</dt>
              <dd data-testid="case-sla">
                {c.slaApplies && c.sla ? (
                  <>
                    P{c.priority}: first response {minutes(c.sla.target.responseMinutes)}, resolved {minutes(c.sla.target.resolveMinutes)}
                    {c.sla.resolveDue && !c.resolved ? <span className="tb-sub">resolution due {fmtTs(c.sla.resolveDue)}</span> : null}{' '}
                  </>
                ) : c.slaApplies ? (
                  'Targets of its priority '
                ) : (
                  <span className="tb-hint">No SLA: an agent-found case has targets only at P0 and P1. </span>
                )}
                <a className="rk-link" href={slaHref()} style={{ fontSize: 12 }}>
                  Settings › SLA
                </a>
              </dd>
            </>
          )}
        </dl>
      </Sec>

      <Links c={c} />

      {(c.detail || c.pruned || (edit && !report)) && (
        <Sec title={report ? 'In their words' : 'Detail'}>
          {/* A report's words stay its reporter's; the detail of a case a person opened is edited in place (CF-60). */}
          {edit && !report && !c.pruned ? (
            <div className="cs-quote" data-testid="case-detail">
              <InPlace value={c.detail ?? ''} label="Detail" testId="case-detail" multiline maxLength={4000} empty={<span className="tb-hint">No detail: click to add some.</span>} onSave={(next, from) => onField('detail', next, from)} />
            </div>
          ) : (
            <p className="cs-quote" data-testid="case-detail">
              {c.pruned ? 'Kept without its text after the retention.' : c.detail}
            </p>
          )}
          {c.scrubbed && <span className="tb-hint">Something that looked like a password or a token was removed before it was kept.</span>}
        </Sec>
      )}

      {inv && (
        <Sec title="The investigation found" testId="case-investigation">
          {inv.technical && <span style={{ whiteSpace: 'pre-wrap' }}>{inv.technical}</span>}
          {inv.cause && (
            <span>
              <strong>Cause:</strong> {inv.cause}
            </span>
          )}
          {(inv.evidence ?? []).length > 0 && (
            <ul style={{ margin: 0, paddingLeft: 18 }}>
              {inv.evidence!.map((e, i) => (
                <li key={i} className="rk-mono" style={{ fontSize: 12 }}>
                  {e}
                </li>
              ))}
            </ul>
          )}
          {inv.plain && (
            <span>
              <strong>For the reporter:</strong> {inv.plain}
            </span>
          )}
        </Sec>
      )}

      <Sec title="The Troubleshooter's analysis" testId="case-analysis">
        {c.analyzing && (
          <span className="tb-hint" data-testid="case-analyzing">
            The Troubleshooter is analyzing it now
            {c.runs?.[0] ? (
              <>
                {' ('}
                <a className="rk-link" href={runHref(c.runs[0])}>
                  {c.runs[0]}
                </a>
                )…
              </>
            ) : (
              '…'
            )}
          </span>
        )}
        {open && c.analysisQueued && !c.analyzing && (
          <span className="tb-hint" data-testid="case-analysis-queued-hint">
            Waiting for analysis: the case analysis job analyzes it when its turn comes, the highest priority first, within its budgets. Analyze runs it now.
          </span>
        )}
        {!a && !c.analyzing && !c.analysisQueued && (
          <span className="tb-hint">{c.cluster ? 'Not analyzed. The case analysis job analyzes a case when it is on (AIOps › Jobs).' : 'Not analyzed: the case names no cluster for the agent to look at.'}</span>
        )}
        {a && (
          <>
            {a.error && (
              <span className="tb-err" data-testid="case-analysis-error">
                {a.error}
              </span>
            )}
            {a.expected && (
              <span data-testid="case-analysis-expected" title="The analysis found the system working as intended (CF-82)">
                <strong>No problem:</strong> {a.expectedWhy}
              </span>
            )}
            {a.cause && (
              <span>
                <strong>Cause:</strong> {a.cause}
              </span>
            )}
            {a.summary && <span style={{ whiteSpace: 'pre-wrap' }}>{a.summary}</span>}
            {(a.evidence ?? []).length > 0 && (
              <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5 }}>
                {a.evidence!.map((e, i) => (
                  <li key={i} className="rk-mono">
                    {e}
                  </li>
                ))}
              </ul>
            )}
            {a.run && (
              <span className="tb-hint" style={{ fontSize: 12 }}>
                From{' '}
                <a className="rk-link" href={runHref(a.run)}>
                  {a.run}
                </a>{' '}
                · {fmtTs(a.at)}
              </span>
            )}
          </>
        )}
        {c.mayChange && open && c.cluster && !c.analyzing && (
          <span>
            <button className="rk-btn" data-size="sm" disabled={busy} onClick={onAnalyze} data-testid="case-analyze">
              {a ? 'Analyze again' : 'Analyze'}
            </button>
          </span>
        )}
      </Sec>

      <Sec title="Fix" testId="case-fix">
        {a && (a.fix.summary || commands.length > 0) ? (
          <>
            <span>
              <strong>Suggested:</strong> {a.fix.summary || 'run in Watson'}
            </span>
            {commands.map((cmd, i) => (
              <div key={i} style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>
                <pre className="rk-code" style={{ margin: 0, padding: '8px 12px', whiteSpace: 'pre-wrap', flex: '1 1 auto' }} data-testid="case-fix-command">
                  $ {cmd}
                </pre>
                {/* Beside the case, its Watson asks before it runs; without it, the dock's Watson has the command in its box. */}
                <RunInWatson onClick={() => (onRun ? onRun(cmd) : openWatson(c.cluster ?? '', cmd))} disabled={!c.cluster} testId="case-open-watson" />
              </div>
            ))}
            {commands.length > 0 && (
              <span className="tb-hint" style={{ fontSize: 12 }}>
                Run in Watson hands it to Watson beside the case, which asks before it runs it, as you, with your own rights. Then close it as Fixed and say what was done.
              </span>
            )}
          </>
        ) : (
          <span className="tb-hint">No fix suggested{a ? '' : ' yet'}.</span>
        )}
        {/* What the analysis suggested and Watson would refuse is left out whole, and says so (W-6). */}
        {(a?.leftOut ?? []).map((l, i) => (
          <span key={i} className="tb-hint" style={{ fontSize: 12 }} data-testid="case-fix-left-out">
            Left out: {l}
          </span>
        ))}
        <CaseChanges
          c={c}
          busy={busy}
          onLinked={onLinked}
          onUnlink={(card) =>
            void act(
              () => unlinkChange(c.id, card),
              (v) => `${card} no longer resolves ${v.id}.`,
            )
          }
        />
        <CaseCommands c={c} />
        {c.fixApplied && (
          <span data-testid="case-fix-applied">
            <strong>Fix applied:</strong> <span style={{ whiteSpace: 'pre-wrap' }}>{c.fixApplied.text}</span>
            <span className="tb-sub">
              {c.fixApplied.by} · {fmtTs(c.fixApplied.at)}
              {c.fixApplied.change && !c.verification && c.status !== 'closed' ? ' · close the case once it is fixed' : ''}
            </span>
          </span>
        )}
      </Sec>

      {c.rca && (
        <Sec title="Root-cause analysis" testId="case-rca">
          <dl className="cs-kv">
            {/* The RCA's fix is what was done: shown once, here only when Fix applied above does not say it already. */}
            {c.rca.fix && c.rca.fix !== c.fixApplied?.text && (
              <>
                <dt>Fix applied</dt>
                <dd style={{ whiteSpace: 'pre-wrap' }} data-testid="case-rca-fix-applied">
                  {c.rca.fix}
                </dd>
              </>
            )}
            <dt>Cause</dt>
            <dd style={{ whiteSpace: 'pre-wrap' }}>{c.rca.cause}</dd>
            {c.rca.prevention && (
              <>
                <dt>Prevention</dt>
                <dd style={{ whiteSpace: 'pre-wrap' }}>{c.rca.prevention}</dd>
              </>
            )}
            {c.rca.text && (
              <>
                <dt>Notes</dt>
                <dd style={{ whiteSpace: 'pre-wrap' }}>{c.rca.text}</dd>
              </>
            )}
            <dt>Given by</dt>
            <dd>
              {c.rca.by} · {fmtTs(c.rca.at)}
            </dd>
          </dl>
          {(c.rca.cases ?? []).length > 0 && (
            <span className="tb-sub" data-testid="case-rca-together">
              Closed together with{' '}
              {c.rca.cases!.map((id, i) => (
                <span key={id}>
                  {i > 0 && ', '}
                  <CaseLink id={id} testId="case-rca-together-link" />
                </span>
              ))}{' '}
              (one RCA)
            </span>
          )}
        </Sec>
      )}

      <Conversation c={c} busy={busy} act={act} />
      {c.mayChange && <Reply c={c} busy={busy} text={writing.reply} onText={(reply) => onWriting({ ...writing, reply })} act={act} />}
      {!c.mayChange && drafted(c.draft) && (
        // Those who only read the cases read the draft too, never change it (CF-59).
        <Sec title="Draft" testId="case-draft-read">
          <DraftParts d={c.draft!} />
          {c.draft?.by && <DraftStamp d={c.draft} />}
        </Sec>
      )}

      <Sec title="History">
        <ol className="cs-msgs" data-testid="case-history">
          {c.history
            .slice()
            .reverse()
            .map((e, i) => (
              <li key={i} className="cs-msg" data-kind="system" data-testid="case-history-entry">
                <span className="cs-meta">
                  {e.by ? (
                    // Who did it, with what they are (CF-35).
                    <span data-testid="case-history-by">{`${actorFull(e.by)} · `}</span>
                  ) : (
                    ''
                  )}
                  {fmtTs(e.at)}
                </span>
                <span className="cs-text">{e.what}</span>
              </li>
            ))}
        </ol>
      </Sec>
    </>
  )
}

/** The conversation, with the replies the investigator drafted: Send, Edit, Discard. */
function Conversation({ c, busy, act }: { c: Case; busy: boolean; act: Act }) {
  const [editing, setEditing] = useState<{ id: number; text: string } | null>(null)
  const list = c.messages
  if (list.length === 0) return null
  const send = (m: CaseMessage, text?: string) =>
    void act(
      () => sendCaseDraft(c.id, m.id, text),
      () => 'Reply sent.',
      () => setEditing(null),
    )
  return (
    <Sec title="Conversation">
      <ol className="cs-msgs" data-testid="case-messages">
        {list.map((m) => (
          <li
            key={m.id}
            className="cs-msg"
            data-kind={m.kind}
            data-internal={m.internal ? 'true' : undefined}
            data-draft={m.draft ? 'true' : undefined}
            data-testid={m.draft ? 'case-message-draft' : m.internal ? 'case-note' : 'case-message'}
          >
            <span className="cs-meta">
              {m.draft ? `Draft by ${m.draftBy === 'agent' ? 'the investigator' : m.draftBy || who(m.from)} · sent only when you press Send` : m.internal ? `Internal note by ${who(m.from)}` : m.kind === 'system' ? 'Update' : who(m.from)}
              {m.sentBy ? ` · sent by ${who(m.sentBy)}` : ''} · {fmtTs(m.at)}
            </span>
            {editing?.id === m.id ? (
              <textarea className="cs-ta" aria-label="Edit the draft" value={editing.text} onChange={(e) => setEditing({ id: m.id, text: e.target.value })} data-testid="case-message-draft-text" />
            ) : (
              <span className="cs-text">{m.text}</span>
            )}
            {c.mayChange && m.draft && (
              <span style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                {editing?.id === m.id ? (
                  <>
                    <button className="rk-btn" data-on disabled={busy || !editing.text.trim()} onClick={() => send(m, editing.text)} data-testid="case-message-send-edited">
                      Send as edited
                    </button>
                    <button className="rk-btn" onClick={() => setEditing(null)}>
                      Cancel
                    </button>
                  </>
                ) : (
                  <>
                    <button className="rk-btn" data-on disabled={busy} onClick={() => send(m)} data-testid="case-message-send">
                      Send
                    </button>
                    <button className="rk-btn" disabled={busy} onClick={() => setEditing({ id: m.id, text: m.text })} data-testid="case-message-edit">
                      Edit
                    </button>
                    <button
                      className="rk-btn"
                      disabled={busy}
                      onClick={() =>
                        void act(
                          () => discardCaseDraft(c.id, m.id),
                          () => 'Draft discarded.',
                        )
                      }
                      data-testid="case-message-discard"
                    >
                      Discard
                    </button>
                  </>
                )}
              </span>
            )}
          </li>
        ))}
      </ol>
    </Sec>
  )
}

/** A reply to the reporter, a question, or an internal note; kept in the case's shared draft by Save (CF-59). */
function Reply({ c, busy, text, onText, act }: { c: Case; busy: boolean; text: string; onText: (t: string) => void; act: Act }) {
  const report = c.source.kind === 'report'
  const say = (how: { internal?: boolean; ask?: boolean }, done: string) =>
    void act(
      () => sayOnCase(c.id, text.trim(), how),
      () => done,
      () => onText(''),
      true,
    )
  return (
    <Sec title={report ? 'Reply to the reporter, or a note' : 'Note'}>
      <textarea
        className="cs-ta"
        aria-label={report ? 'Reply to the reporter, or an internal note' : 'An internal note'}
        value={text}
        onChange={(e) => onText(e.target.value)}
        placeholder={report ? 'Written in words the reporter reads; they see it on the report page. An internal note only the people who work the cases see.' : 'Only the people who work the cases see it.'}
        data-testid="case-reply"
      />
      <span style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
        {report && (
          <>
            <button className="rk-btn" data-on disabled={busy || !text.trim()} onClick={() => say({}, 'Reply sent.')} data-testid="case-send">
              Send reply
            </button>
            <button
              className="rk-btn"
              disabled={busy || !text.trim()}
              title="Sends the question; the case shows it waits for the reporter’s answer"
              onClick={() => say({ ask: true }, 'Question sent; the case waits for the answer.')}
              data-testid="case-ask"
            >
              Ask for info
            </button>
          </>
        )}
        <button className="rk-btn" disabled={busy || !text.trim()} title="Only the people who work the cases see it" onClick={() => say({ internal: true }, 'Note added.')} data-testid="case-note-add">
          Add internal note
        </button>
      </span>
    </Sec>
  )
}

/**
 * The header's controls of those who work the cases (CF-58): priority, type
 * and assignee, at the right end of the facts line, apart from the title;
 * they wrap under the facts on a narrow screen.
 */
function CaseControls({ c, busy, act }: { c: Case; busy: boolean; act: Act }) {
  const [other, setOther] = useState<string | null>(null)
  return (
    <span style={{ marginLeft: 'auto', display: 'flex', flexWrap: 'wrap', gap: '6px 12px', alignItems: 'center', justifyContent: 'flex-end', color: 'var(--fg2)' }} data-testid="case-controls">
      <span className="cs-inline">
        Priority
        <Select
          label="Priority"
          value={String(c.priority)}
          options={PRIORITIES.map((p): [string, string] => [String(p), PRIORITY[p]!])}
          disabled={busy}
          testId="case-priority-set"
          onChange={(v) =>
            void act(
              () => updateCase(c.id, { priority: Number(v) as CasePriority }),
              (x) => `${x.id} is P${x.priority}.`,
            )
          }
        />
      </span>
      <span className="cs-inline">
        Type
        <Select
          label="Type"
          value={c.type && c.type in CASE_TYPES ? c.type : ''}
          options={[...(c.type && c.type in CASE_TYPES ? [] : [['', c.type ? caseTypeText(c.type) : 'Not set'] as [string, string]]), ...CASE_TYPE_IDS.map((t): [string, string] => [t, CASE_TYPES[t]])]}
          disabled={busy}
          testId="case-type-set"
          onChange={(v) => {
            if (v)
              void act(
                () => updateCase(c.id, { type: v as CaseType }),
                (x) => `${x.id} is ${caseTypeText(x.type).toLowerCase() || 'set'}.`,
              )
          }}
        />
      </span>
      <span className="cs-inline">
        Assignee
        <Select
          label="Assignee"
          value={other !== null ? 'other' : c.assignee ? 'current' : ''}
          options={[['', 'Nobody'], ['me', 'Me'], ...(c.assignee ? [['current', who(c.assignee)] as [string, string]] : []), ['other', 'Someone else…']]}
          disabled={busy}
          testId="case-assignee-set"
          onChange={(v) => {
            setOther(v === 'other' ? '' : null)
            if (v === 'me')
              void act(
                () => updateCase(c.id, { assignee: { id: 'me' } }),
                (x) => `${x.id} is yours.`,
              )
            else if (v === '')
              void act(
                () => updateCase(c.id, { assignee: { id: '' } }),
                (x) => `${x.id} is nobody's.`,
              )
          }}
        />
      </span>
      {other !== null && (
        <span className="cs-inline">
          <input className="cs-in" aria-label="Their user name" placeholder="user name" value={other} onChange={(e) => setOther(e.target.value)} data-testid="case-assignee-other" />
          <button
            className="rk-btn"
            data-size="sm"
            disabled={busy || !other.trim()}
            onClick={() =>
              void act(
                () => updateCase(c.id, { assignee: { id: other.trim() } }),
                (x) => `${x.id} is ${who(x.assignee)}'s.`,
                () => setOther(null),
              )
            }
          >
            Set
          </button>
        </span>
      )}
    </span>
  )
}

/** Who saved the shared draft last, and when (CF-59). */
function DraftStamp({ d }: { d: CaseDraft }) {
  return (
    <span className="tb-hint" style={{ fontSize: 12 }} data-testid="case-draft-at">
      Draft last edited by {d.by || 'someone'}
      {d.updated ? `, ${fmtTs(d.updated)}` : ''}
    </span>
  )
}

/** The name of the cluster a case's key is on: the case's own cluster by its name, another by its id (U-11). */
export function keyClusterName(c: Pick<Case, 'key' | 'cluster' | 'clusterName'>): string {
  const id = c.key?.cluster ?? ''
  return id && id === c.cluster ? c.clusterName || id : id
}

/** The cluster by its name, its id in the hover; one the console no longer has says so (CF-60). */
function ClusterName({ c }: { c: Case }) {
  if (!c.cluster) return <span className="tb-hint">none named</span>
  const gone = c.clusterConnected === false
  return (
    <span title={c.cluster} data-testid="case-cluster-name">
      {gone ? `${c.cluster} (not connected)` : c.clusterName || c.cluster}
    </span>
  )
}

/**
 * The cluster, picked from the clusters a case may be on, as New Case lists
 * them, or none (CF-60): saved on change, from the cluster it started from.
 * A cluster the console no longer has stays until someone picks another.
 */
function ClusterPick({ c, busy, onField }: { c: Case; busy: boolean; onField: OnField }) {
  const list = c.clusters ?? []
  const current = c.cluster ?? ''
  const options: [string, string][] = [['', 'No cluster'], ...list.map((x): [string, string] => [x.id, x.name || x.id])]
  if (current && !list.some((x) => x.id === current)) options.push([current, c.clusterConnected === false ? `${current} (not connected)` : c.clusterName || current])
  return (
    <span title={current || undefined} style={{ display: 'inline-flex' }}>
      <Select label="Cluster" value={current} options={options} disabled={busy} testId="case-cluster-set" onChange={(v) => void onField('cluster', v, current)} />
    </span>
  )
}

/** The footer of those who work the cases: actions only, Close… or Reopen, and Save, the case's shared draft (CF-58, CF-59). */
function CaseFooter({ c, busy, panel, onPanel, onKeep, err }: { c: Case; busy: boolean; panel: Panel; onPanel: (p: Panel) => void; onKeep: () => void; err: string }) {
  const open = c.status !== 'closed'
  const allowed = new Set<Outcome>(c.allowedOutcomes ?? [])
  const outcomes = CLOSE_ORDER.filter((o) => allowed.has(o))
  const off = busy || panel !== ''
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, width: '100%' }} data-testid="case-actions">
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
        {open ? (
          <>
            {outcomes.length > 0 && (
              <button className="rk-btn" data-on disabled={off} onClick={() => onPanel('close')} title={`Close it: ${outcomes.map((o) => OUTCOMES[o].toLowerCase()).join(', ')}`} data-testid="case-close-open">
                Close…
              </button>
            )}
            {outcomes.length === 0 && (
              <span className="tb-hint" style={{ fontSize: 12 }}>
                Your role may not close this case.
              </span>
            )}
          </>
        ) : (
          <>
            <span className="tb-hint" style={{ fontSize: 12 }}>
              Closed. {c.mayReopen ? 'Reopen it if the problem is back; its history stays.' : 'Its history stays.'}
            </span>
            {c.mayReopen && (
              <button className="rk-btn" disabled={off} onClick={() => onPanel('reopen')} data-testid="case-reopen">
                Reopen…
              </button>
            )}
          </>
        )}
        <span className="cs-grow" />
        {c.draft?.updated && drafted(c.draft) && <DraftStamp d={c.draft} />}
        <button
          className="rk-btn"
          disabled={busy}
          onClick={onKeep}
          title="Saves what is written here (a reply or note, a fix, a close explanation, an RCA) as the case’s draft, for everyone who works it; the reporter never sees it"
          data-testid="case-keep"
        >
          Save
        </button>
      </div>
      {err && (
        <p className="tb-err" role="alert" style={{ margin: 0 }} data-testid="case-error">
          {err}
        </p>
      )}
    </div>
  )
}

function FormButtons({ busy, ready, label, onSave, onCancel, testId, err }: { busy: boolean; ready: boolean; label: string; onSave: () => void; onCancel: () => void; testId: string; err?: string }) {
  return (
    <>
      {err && (
        <p className="tb-err" role="alert" style={{ margin: 0 }} data-testid="case-error">
          {err}
        </p>
      )}
      <span style={{ display: 'flex', gap: 8 }}>
        <button className="rk-btn" data-on disabled={busy || !ready} onClick={onSave} data-testid={testId}>
          {label}
        </button>
        <button className="rk-btn" disabled={busy} onClick={onCancel}>
          Back
        </button>
      </span>
    </>
  )
}

interface FormProps {
  c: Case
  busy: boolean
  onCancel: () => void
  act: Act
  err: string
  onDone: () => void
}

/**
 * Close…: one form for every outcome the case allows (CF-42), as choices
 * in one order (Fixed, Duplicate, No action needed, Known issue,
 * Exception); what the chosen one needs shows under it, with its
 * own button, which calls that outcome's own route.
 */
function CloseForm({ c, busy, writing, onWriting, onCancel, act, err, onDone }: FormProps & { writing: Writing; onWriting: Dispatch<SetStateAction<Writing>> }) {
  const allowed = CLOSE_ORDER.filter((o) => (c.allowedOutcomes ?? []).includes(o))
  const close = writing.close
  const outcome = close.outcome && allowed.includes(close.outcome) ? close.outcome : allowed.length === 1 ? allowed[0]! : ''
  const setClose = (x: CloseWriting) => onWriting({ ...writing, close: x })
  const form = { c, busy, onCancel, act, err, onDone }
  const fields = (o: Outcome) => {
    switch (o) {
      case 'fix':
        return (
          <FixFields
            {...form}
            text={writing.fix}
            onText={(fix) => onWriting({ ...writing, fix })}
            rca={writing.rca}
            onRCA={(rca) => onWriting({ ...writing, rca })}
            // The agent's draft, as it is when it comes: its fix fills What was done only when nothing is written there.
            onDrafted={(d) => onWriting((w) => ({ ...w, fix: w.fix.trim() ? w.fix : d.fix || w.fix, rca: { ...w.rca, cause: d.cause, prevention: d.prevention } }))}
          />
        )
      case 'knownIssue':
        return <KnownFields {...form} />
      case 'exception':
        return <ExceptionFields {...form} />
      default:
        return <ExplainFields {...form} outcome={o} close={close} onClose={setClose} />
    }
  }
  return (
    <Sec title={`Close ${c.id}`} testId="case-close-form">
      <fieldset style={{ border: 0, padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: 8 }}>
        <legend className="rk-sr">How it closes</legend>
        {allowed.map((o) => (
          <div key={o} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            <label className="cs-inline" style={{ alignItems: 'flex-start', color: 'var(--fg)' }}>
              <input type="radio" name="case-outcome" checked={outcome === o} onChange={() => setClose({ ...close, outcome: o })} data-testid={`case-outcome-${o}`} />
              <span>
                <strong>{OUTCOMES[o]}</strong> <span className="tb-hint">{outcomeHint(o, c)}</span>
              </span>
            </label>
            {outcome === o && <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginLeft: 22 }}>{fields(o)}</div>}
          </div>
        ))}
      </fieldset>
      {!outcome && (
        <>
          {err && (
            <p className="tb-err" role="alert" style={{ margin: 0 }} data-testid="case-error">
              {err}
            </p>
          )}
          <span>
            <button className="rk-btn" disabled={busy} onClick={onCancel}>
              Back
            </button>
          </span>
        </>
      )}
    </Sec>
  )
}

/**
 * Fixed: what was done, and a root-cause analysis (required at P0 and P1,
 * optional otherwise, drafted by the agent on request) of cause, prevention
 * and notes, whose fix is what was done; the case closes as fixed, the RCA
 * stays on it for the agents to read when they analyse a like case, and for
 * an agent-found case the next run of its job checks that the fix holds.
 */
function FixFields({ c, busy, text, onText, rca, onRCA, onDrafted, onCancel, act, err, onDone }: FormProps & { text: string; onText: (t: string) => void; rca: Completion; onRCA: (r: Completion) => void; onDrafted: (d: RCADraft) => void }) {
  const [drafting, setDrafting] = useState(false)
  const [draftErr, setDraftErr] = useState('')
  const [drafted, setDrafted] = useState('')
  // Save fix: on unless the person turns it off; the server saves one fix from what was done and the RCA (wave 6g).
  const [keep, setKeep] = useState(true)
  const commands = c.analysis?.fix.commands ?? []
  // What was done starts from the changes run from the case that finished
  // done (CF-49), only while nothing is written there; the person edits it.
  const [prefilled] = useState(() => !text.trim() && !!fixPrefill(c))
  useEffect(() => {
    if (prefilled) onText(fixPrefill(c))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  // An RCA goes with the fix when it is required or anything of it is written; then it needs its cause.
  const withRCA = c.rcaRequired || hasRCA(rca)
  const draft = async () => {
    setDrafting(true)
    setDraftErr('')
    try {
      const d = await draftRCA(c.id)
      onDrafted(d)
      setDrafted(d.run ?? 'the agent')
    } catch (e) {
      setDraftErr(errText(e))
    } finally {
      setDrafting(false)
    }
  }
  const set = (k: keyof Completion, v: string) => onRCA({ ...rca, [k]: v })
  const ready = !!text.trim() && (!withRCA || !!rca.cause.trim())
  const save = () =>
    void act(
      () => fixCase(c.id, text.trim(), withRCA ? { cause: rca.cause.trim(), prevention: rca.prevention.trim(), text: (rca.text ?? '').trim() } : undefined, keep),
      (v) => `${v.id} is fixed.`,
      onDone,
      true,
    )
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="case-fix-form">
      {commands.length > 0 && (
        <span className="tb-hint" style={{ fontSize: 12.5 }}>
          The suggested fix: {commands.join('; ')}. Run it in Watson as yourself (Run in Watson, under Fix), or approve the proposed change, then say what was done.
        </span>
      )}
      <label className="cs-lbl">
        What was done (required)
        <textarea className="cs-ta" value={text} maxLength={4000} onChange={(e) => onText(e.target.value)} placeholder="The change you made, and where" data-testid="case-fix-text" />
      </label>
      {prefilled && (
        <span className="tb-hint" style={{ fontSize: 12 }} data-testid="case-fix-prefilled">
          Filled in from the changes run from this case that finished done: edit it to say what was done.
        </span>
      )}
      {c.rcaRequired ? (
        <span className="rk-h" style={{ fontSize: 12 }} data-testid="case-rca-required">
          Root-cause analysis (required at P{c.priority})
        </span>
      ) : (
        <span className="rk-h" style={{ fontSize: 12 }} data-testid="case-rca-optional">
          Root-cause analysis (optional)
        </span>
      )}
      <span>
        <button
          className="rk-btn"
          data-size="sm"
          disabled={busy || drafting || !c.cluster}
          onClick={() => void draft()}
          data-testid="case-rca-draft"
          title={c.cluster ? 'The Troubleshooter drafts it from the case’s record; you edit it' : 'The case names no cluster for the agent to look at'}
        >
          {drafting ? 'Drafting…' : 'Draft with the agent'}
        </button>
        {drafted && <span className="tb-sub">Drafted by the Troubleshooter ({drafted}); read and edit it before you save.</span>}
      </span>
      {draftErr && (
        <p className="tb-err" role="alert" style={{ margin: 0 }} data-testid="case-rca-error">
          {draftErr}
        </p>
      )}
      <label className="cs-lbl">
        {withRCA ? 'Cause (required)' : 'Cause'}
        <textarea className="cs-ta" value={rca.cause} maxLength={4000} onChange={(e) => set('cause', e.target.value)} data-testid="case-rca-cause" />
      </label>
      <label className="cs-lbl">
        Prevention
        <textarea className="cs-ta" style={{ minHeight: 56 }} value={rca.prevention} maxLength={4000} onChange={(e) => set('prevention', e.target.value)} data-testid="case-rca-prevention" />
      </label>
      <label className="cs-lbl">
        Notes (optional)
        <textarea className="cs-ta" style={{ minHeight: 56 }} value={rca.text ?? ''} maxLength={4000} onChange={(e) => set('text', e.target.value)} data-testid="case-rca-text" />
      </label>
      <span className="tb-hint" style={{ fontSize: 12 }}>
        The case closes as fixed{withRCA ? '; what was done is the RCA’s fix, and the RCA stays on the case, where the agents read it when they analyze a like case later' : ''}.
        {c.agentFound ? ' The next run of its job checks that the fix holds, and reopens the case if not.' : c.source.kind === 'report' ? ' The reporter is told, and may say it is still broken.' : ''}
      </span>
      <label className="cs-inline" style={{ alignItems: 'flex-start', color: 'var(--fg)' }}>
        <input type="checkbox" checked={keep} onChange={(e) => setKeep(e.target.checked)} data-testid="case-save-fix" />
        <span>
          Save fix <span className="tb-hint">It becomes a fix in Mycroft › Fixes, given to the agents for the cases like it.</span>
        </span>
      </label>
      <FormButtons busy={busy || drafting} ready={ready} label="Close as fixed" testId="case-fix-save" onCancel={onCancel} onSave={save} err={err} />
    </div>
  )
}

/** An offer's words: what pressing it does. */
export function offerText(o: Offer, check: Pick<DuplicateCheck, 'outcome'>): string {
  switch (o.kind) {
    case 'reopenOriginal':
      return check.outcome === 'fix' ? `Reopen ${o.original}: the fix did not hold` : `Reopen ${o.original}`
    case 'regression':
      return `Keep this case open as a regression of ${o.original}`
    case 'sameKnownIssue':
      return `Close as known issue ${o.knownIssue ?? ''}`.trim()
    case 'noAction':
      return `Close as no action needed, as ${o.original} was`
    case 'removeException':
      return o.exception ? `Remove exception ${o.exception} and keep this case open` : 'Remove the exception and keep this case open'
    case 'sameAs':
      return `Keep this case open, marked the same as ${o.original}`
  }
}

/** What happened once an offer is done, for the toast. */
function offerDone(o: Offer, v: Case): string {
  switch (o.kind) {
    case 'reopenOriginal':
      return `${o.original} is open again; ${v.id} is closed as its duplicate.`
    case 'regression':
      return `${v.id} stays open as a regression of ${o.original}.`
    case 'sameKnownIssue':
      return `${v.id} is closed as known issue ${o.knownIssue ?? ''}.`
    case 'noAction':
      return `${v.id} is closed as no action needed.`
    case 'removeException':
      return `Exception ${o.exception ?? ''} is removed; ${v.id} stays open.`
    case 'sameAs':
      return `${v.id} stays open, marked the same as ${o.original}.`
  }
}

/** A duplicate check's answer: allowed, or why not with what fits instead, as buttons. */
function DuplicateAnswer({ c, check, busy, act, onDone }: { c: Case; check: DuplicateCheck; busy: boolean; act: Act; onDone: () => void }) {
  const state = check.status === 'closed' && check.outcome ? `closed as ${OUTCOMES[check.outcome].toLowerCase()}` : check.status === 'new' ? 'new' : 'in progress'
  if (check.allowed) {
    return (
      <span className="tb-hint" data-testid="case-duplicate-ok">
        {check.original} is {state}: this case's reporter and history go to it.
      </span>
    )
  }
  return (
    <div className="rk-callout" data-tone="warn" style={{ flexDirection: 'column', gap: 6 }} data-testid="case-duplicate-refused">
      <span>{check.reason || `${check.original} is ${state}, so this case cannot close as its duplicate.`}</span>
      {check.offers.length > 0 && <span style={{ fontSize: 12 }}>Instead:</span>}
      {check.offers.map((o) => (
        <span key={o.kind} style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <button
            className="rk-btn"
            data-size="sm"
            style={{ alignSelf: 'flex-start' }}
            disabled={busy}
            onClick={() =>
              void act(
                () => applyOffer(c.id, o),
                (v) => offerDone(o, v),
                onDone,
              )
            }
            data-testid={`case-offer-${o.kind}`}
          >
            {offerText(o, check)}
          </button>
          {o.kind === 'noAction' && o.explanation && <span className="tb-sub">“{o.explanation}”</span>}
        </span>
      ))}
    </div>
  )
}

/**
 * Duplicate or No action needed: each with an explanation. A
 * duplicate names its original, which is checked as it is typed (the end
 * of its chain, and whether it is open); a closed original offers what
 * fits instead, and the server checks again when the case is closed.
 */
function ExplainFields({ c, busy, outcome, close, onClose, onCancel, act, err, onDone }: FormProps & { outcome: CloseOutcome; close: CloseWriting; onClose: (c: CloseWriting) => void }) {
  const dup = outcome === 'duplicate'
  const n = caseNumber(close.original)
  const self = n === c.number
  // The answer for the original typed, kept with what it answers.
  const key = dup && n && !self ? `C-${n}` : ''
  const [answer, setAnswer] = useState<{ for: string; check?: DuplicateCheck; err?: string } | null>(null)
  const check = answer && answer.for === key ? (answer.check ?? null) : null
  const checkErr = answer && answer.for === key ? (answer.err ?? '') : ''
  // The original is checked as it is typed.
  useEffect(() => {
    if (!key) return
    let gone = false
    const t = setTimeout(() => {
      checkDuplicate(c.id, key).then(
        (v) => !gone && setAnswer({ for: key, check: v }),
        (e: unknown) => !gone && setAnswer({ for: key, err: errText(e) }),
      )
    }, 400)
    return () => {
      gone = true
      clearTimeout(t)
    }
  }, [key, c.id])
  const ready = !!close.explanation.trim() && (!dup || (!!n && !self && !!check?.allowed))
  const save = () =>
    void act(
      async () => {
        try {
          return await closeCase(c.id, { outcome, explanation: close.explanation.trim(), ...(dup ? { original: check?.original ?? key } : {}) })
        } catch (e) {
          // The original changed since it was checked: what fits instead.
          if (e instanceof CallError && e.status === 409 && e.body?.check) setAnswer({ for: key, check: e.body.check as DuplicateCheck })
          throw e
        }
      },
      (v) => `${v.id} is closed as ${OUTCOMES[outcome].toLowerCase()}.`,
      onDone,
      true,
    )
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="case-explain-form">
      {dup && (
        <>
          <label className="cs-lbl">
            The original case
            <input className="cs-in rk-mono" style={{ width: 140 }} placeholder="C-12" value={close.original} onChange={(e) => onClose({ ...close, original: e.target.value })} data-testid="case-close-original" />
          </label>
          {self && <span className="tb-err">A case cannot be its own original.</span>}
          {checkErr && <span className="tb-err">{checkErr}</span>}
          {check && <DuplicateAnswer c={c} check={check} busy={busy} act={act} onDone={onDone} />}
        </>
      )}
      <label className="cs-lbl">
        Explanation (required){c.source.kind === 'report' ? ': the reporter reads it' : ''}
        <textarea
          className="cs-ta"
          style={{ minHeight: 64 }}
          value={close.explanation}
          maxLength={1000}
          onChange={(e) => onClose({ ...close, explanation: e.target.value })}
          placeholder={outcome === 'noAction' ? 'Not a problem, or it went away: say why' : 'Why it is the same problem'}
          data-testid="case-close-reason"
        />
      </label>
      <FormButtons busy={busy} ready={ready} label={`Close as ${OUTCOMES[outcome].toLowerCase()}`} testId="case-close-save" onCancel={onCancel} onSave={save} err={err} />
    </div>
  )
}

/** Known issue: link one that is open, or make one from the case; the case closes as that known issue. */
function KnownFields({ c, busy, onCancel, act, err, onDone }: FormProps) {
  const [list, setList] = useState<OpenKnown[] | null>(null)
  const [pick, setPick] = useState('')
  const [title, setTitle] = useState(c.title)
  const [symptom, setSymptom] = useState(c.title)
  const [workaround, setWorkaround] = useState('')
  const [loadErr, setLoadErr] = useState('')
  useEffect(() => {
    let gone = false
    fetchOpenKnown().then(
      (k) => !gone && setList(k),
      (e: unknown) => !gone && setLoadErr(errText(e)),
    )
    return () => {
      gone = true
    }
  }, [])
  const others = list ?? []
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="case-known-form">
      <span className="tb-hint" style={{ fontSize: 12.5 }}>
        A real problem with symptoms, a workaround and a fix to come. Many cases can link one known issue{c.agentFound ? '; the agents keep checking it quietly and count when it is still there' : ''}.
      </span>
      <span className="rk-h" style={{ fontSize: 12 }}>
        Link an existing one
      </span>
      {list === null && !loadErr && <span className="tb-hint">Loading…</span>}
      {loadErr && <span className="tb-err">{loadErr}</span>}
      {list !== null && others.length === 0 && <span className="tb-hint">No known issue is open.</span>}
      {others.length > 0 && (
        <span style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
          <Select label="Known issue" value={pick} options={[['', 'Choose…'], ...others.map((k): [string, string] => [k.id, `${k.id}: ${k.title}`])]} onChange={setPick} testId="case-known-pick" width={320} />
          <button
            className="rk-btn"
            data-on
            disabled={busy || !pick}
            onClick={() =>
              void act(
                () => closeKnown(c.id, { id: pick }),
                (v) => `${v.id} is closed as known issue ${pick}.`,
                onDone,
                true,
              )
            }
            data-testid="case-known-link"
          >
            Close as {pick || 'this known issue'}
          </button>
        </span>
      )}
      <span className="rk-h" style={{ fontSize: 12, marginTop: 6 }}>
        Or make one from this case
      </span>
      <label className="cs-lbl">
        Title
        <input className="cs-in" value={title} maxLength={300} onChange={(e) => setTitle(e.target.value)} data-testid="case-known-title" />
      </label>
      <label className="cs-lbl">
        What people see, in their words
        <input className="cs-in" value={symptom} maxLength={300} onChange={(e) => setSymptom(e.target.value)} data-testid="case-known-symptom" />
      </label>
      <label className="cs-lbl">
        Workaround (optional)
        <input className="cs-in" value={workaround} maxLength={300} onChange={(e) => setWorkaround(e.target.value)} data-testid="case-known-workaround" />
      </label>
      <FormButtons
        busy={busy}
        ready={!!title.trim()}
        label="Make it and close"
        testId="case-known-create"
        onCancel={onCancel}
        err={err}
        onSave={() =>
          void act(
            () => closeKnown(c.id, { create: { title: title.trim(), symptoms: symptom.trim() ? [symptom.trim()] : [], workaround: workaround.trim() } }),
            (v) => `${v.knownIssue ?? 'The known issue'} is made from ${v.id}, and ${v.id} is closed as it.`,
            onDone,
            true,
          )
        }
      />
    </div>
  )
}

/** Exception: intended or acceptable here; a reason, and a last day or none. */
function ExceptionFields({ c, busy, onCancel, act, err, onDone }: FormProps) {
  const [reason, setReason] = useState('')
  const [expires, setExpires] = useState('')
  const k = c.key
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="case-exception-form">
      <span className="tb-hint" style={{ fontSize: 12.5 }}>
        The agents stop reporting {k ? <span className="rk-mono">{k.check}</span> : 'this validation rule'}
        {k ? (k.object ? ` on ${k.object}` : ' cluster-wide') : ''}
        {k?.cluster ? ` in ${k.cluster}` : ''}. Mycroft › Exceptions lists it, with how often it matched; it can be removed there.
      </span>
      <label className="cs-lbl">
        Justification (required)
        <textarea className="cs-ta" style={{ minHeight: 64 }} value={reason} maxLength={1000} onChange={(e) => setReason(e.target.value)} placeholder="What makes this intended or acceptable here" data-testid="case-exception-reason" />
      </label>
      <label className="cs-lbl">
        Last day (optional)
        <input className="cs-in" type="date" style={{ width: 180 }} min={today()} value={expires} onChange={(e) => setExpires(e.target.value)} data-testid="case-exception-expires" />
        <span className="tb-hint">Empty: until someone removes it.</span>
      </label>
      <FormButtons
        busy={busy}
        ready={!!reason.trim()}
        label="Close as exception"
        testId="case-exception-save"
        onCancel={onCancel}
        err={err}
        onSave={() =>
          void act(
            () => closeException(c.id, reason.trim(), expires || undefined),
            (v) => `${v.id} is closed as exception ${v.exception ?? ''}; the agents stop reporting it.`,
            onDone,
            true,
          )
        }
      />
    </div>
  )
}

/** Reopen: why, always. */
function ReopenForm({ c, busy, onCancel, act, err, onDone }: FormProps) {
  const [reason, setReason] = useState('')
  return (
    <Sec title={`Reopen ${c.id}`} testId="case-reopen-form">
      <label className="cs-lbl">
        Why it is open again (required)
        <textarea className="cs-ta" style={{ minHeight: 64 }} value={reason} maxLength={1000} onChange={(e) => setReason(e.target.value)} placeholder="What is still wrong, or what was missed" data-testid="case-reopen-reason" />
      </label>
      <span className="tb-hint" style={{ fontSize: 12 }}>
        It goes back to In progress, with its history; the reason is kept in it.
      </span>
      <FormButtons
        busy={busy}
        ready={!!reason.trim()}
        label="Reopen"
        testId="case-reopen-save"
        onCancel={onCancel}
        err={err}
        onSave={() =>
          void act(
            () => reopenCase(c.id, reason.trim()),
            (v) => `${v.id} is open again.`,
            onDone,
          )
        }
      />
    </Sec>
  )
}
