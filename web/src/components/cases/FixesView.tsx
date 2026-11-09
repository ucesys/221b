// Cases › Mycroft › Fixes (Phase 12, wave 6g): what worked before. A case
// closed as Fixed with Save fix on becomes a fix: its symptoms, cause,
// steps, the commands that worked and how to prevent it, with the cases it
// was seen in. Active fixes are given to the agents (case analysis,
// Sherlock's knowledge search); pending ones contradict another fix and
// wait for a person to accept them; inactive ones are duplicates or were
// turned off by a person. The list at #/cases/mycroft/fixes, a fix's
// drawer at #/cases/mycroft/fixes/<id>; those who work the cases edit,
// accept, turn off and delete them there.
import { useCallback, useEffect, useState, type JSX, type ReactNode } from 'react'
import { CallError } from '../../api/call'
import { caseHref } from '../../api/casework'
import { deleteFix, fetchFixes, fixHref, saveFix, type Fix, type FixPatch, type FixStatus } from '../../api/fixes'
import { fmtTs } from '../../lib/format'
import { navigate } from '../../lib/router'
import type { SortCol } from '../../lib/useSorted'
import { useSorted } from '../../lib/useSorted'
import { SortTh } from '../automation/SortTh'
import { ConfirmDialog } from '../ConfirmDialog'
import { Drawer } from '../Drawer'
import { errText, Tag } from './parts'
import { EmptyRow, MycroftPage, MycroftTable } from './MycroftPage'

/** The list's address, where a closed drawer goes back to. */
const LIST = '#/cases/mycroft/fixes'

const errorText = (e: unknown) => (e instanceof CallError && e.status === 403 ? 'Your role does not include the cases, and so not their fixes.' : errText(e))

type Show = FixStatus | 'all'

const SHOW: [Show, string, string][] = [
  ['active', 'Active', 'The fixes the agents are given'],
  ['pending', 'Pending', 'They contradict another fix: the agents are not given them until someone accepts them'],
  ['inactive', 'Inactive', 'Duplicates, and fixes a person turned off'],
  ['all', 'All', 'Every fix'],
]

const STATUS_TONE: Record<FixStatus, string> = { active: 'ok', pending: 'warn', inactive: 'muted' }
const STATUS_ORDER: Record<FixStatus, number> = { active: 0, pending: 1, inactive: 2 }

/** Which fixes a view keeps. */
export function fixesShown(list: Fix[], show: Show): Fix[] {
  return show === 'all' ? list : list.filter((f) => f.status === show)
}

/** "F-12" as 12, so F-10 sorts after F-9. */
const idNumber = (id: string) => Number(id.replace(/\D/g, '')) || 0
const time = (at?: string) => (at ? Date.parse(at) || null : null)

export const FIX_SORT: SortCol<Fix>[] = [
  { id: 'id', sortValue: (f) => idNumber(f.id), defaultDir: 'desc' },
  { id: 'title', sortValue: (f) => f.title },
  { id: 'status', sortValue: (f) => STATUS_ORDER[f.status] ?? 3 },
  { id: 'cluster', sortValue: (f) => f.cluster || null },
  { id: 'cases', sortValue: (f) => (f.cases ?? []).length, defaultDir: 'desc' },
  { id: 'updated', sortValue: (f) => time(f.updated), defaultDir: 'desc' },
]

/** A fix's id as a link to it, without opening the row's own drawer twice. */
function FixLink({ id }: { id: string }) {
  return (
    <a className="rk-link rk-mono" href={fixHref(id)} onClick={(e) => e.stopPropagation()}>
      {id}
    </a>
  )
}

/** Its status, with what it is a duplicate of or contradicts. */
function StatusCell({ f }: { f: Fix }) {
  return (
    <span style={{ display: 'inline-flex', gap: 6, flexWrap: 'wrap', alignItems: 'center' }}>
      <Tag tone={STATUS_TONE[f.status] ?? 'muted'} testId="fix-status">
        {f.status}
      </Tag>
      {f.duplicateOf && (
        <span className="tb-sub" data-testid="fix-duplicate-of">
          duplicate of <FixLink id={f.duplicateOf} />
        </span>
      )}
      {(f.contradicts ?? []).length > 0 && (
        <span className="tb-sub" data-testid="fix-contradicts">
          contradicts{' '}
          {f.contradicts!.map((id, i) => (
            <span key={id}>
              {i > 0 && ', '}
              <FixLink id={id} />
            </span>
          ))}
        </span>
      )}
    </span>
  )
}

/** Mycroft › Fixes; open is the fix a link names. */
export function FixesView({ open, onToast, onCount }: { open: string; onToast?: (msg: string) => void; onCount?: (n: number) => void }): JSX.Element {
  const [list, setList] = useState<Fix[] | null>(null)
  const [may, setMay] = useState(false)
  const [error, setError] = useState('')
  const [show, setShow] = useState<Show>('active')
  const [now] = useState(() => Date.now())
  const load = useCallback(() => {
    fetchFixes().then(
      (r) => {
        setList(r.fixes)
        setMay(r.mayChange)
        setError('')
        onCount?.(r.fixes.filter((f) => f.status === 'active').length)
      },
      (e: unknown) => setError(errorText(e)),
    )
  }, [onCount])
  useEffect(() => load(), [load])
  const rows = fixesShown(list ?? [], show)
  const { sort, onSort, shown } = useSorted('mycroft-fixes', FIX_SORT, rows, { col: 'updated', dir: 'desc' }, now)
  const th = (col: string, label: string) => <SortTh col={col} label={label} sort={sort} onSort={onSort} />
  const openOne = list?.find((f) => f.id === open)
  const count = (s: Show) => fixesShown(list ?? [], s).length
  // A change in the drawer: the list takes the fix as the server answered.
  const changed = (f: Fix | null, gone?: string) => {
    if (gone) {
      setList((l) => (l ? l.filter((x) => x.id !== gone) : l))
      navigate(LIST)
    } else if (f) setList((l) => (l ? l.map((x) => (x.id === f.id ? f : x)) : l))
    load()
  }
  return (
    <MycroftPage
      testId="mycroft-fixes"
      lead="What worked before: a case closed as Fixed with Save fix on becomes a fix. The agents are given the active ones when they analyse a case like it, and Sherlock finds them. Housekeeping turns duplicates off, and marks a fix that contradicts another as pending until someone accepts it."
      filter={
        <div className="rk-seg" role="group" aria-label="Show" data-testid="fixes-show">
          {SHOW.map(([v, label, tip]) => (
            <button key={v} type="button" aria-pressed={show === v} title={tip} onClick={() => setShow(v)} data-testid={`fixes-show-${v}`}>
              {label}
              {list ? <span style={{ color: 'var(--fg3)', fontWeight: 400 }}> {count(v)}</span> : null}
            </button>
          ))}
        </div>
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
          There is no fix {open}.
        </p>
      )}
      {list && (
        <MycroftTable label="Fixes" testId="fixes-table">
          <thead>
            <tr>
              {th('id', 'Fix')}
              {th('title', 'Title')}
              {th('status', 'Status')}
              {th('cluster', 'Cluster')}
              {th('cases', 'Cases')}
              {th('updated', 'Updated')}
            </tr>
          </thead>
          <tbody>
            {shown.length === 0 && (
              <EmptyRow cols={6} testId="fixes-empty">
                {!list.length
                  ? 'No fixes yet. A fix is saved when a case is closed as Fixed with Save fix on.'
                  : show === 'pending'
                    ? 'No pending fixes: none contradicts another.'
                    : show === 'inactive'
                      ? 'No inactive fixes.'
                      : 'No active fixes.'}
              </EmptyRow>
            )}
            {shown.map((f) => (
              <tr key={f.id} className="cs-row" aria-selected={f.id === open} onClick={() => navigate(fixHref(f.id))} data-testid="fix-row" data-id={f.id}>
                <td>
                  <FixLink id={f.id} />
                </td>
                <td>{f.title}</td>
                <td>
                  <StatusCell f={f} />
                </td>
                <td>{f.cluster || '—'}</td>
                <td data-testid="fix-cases">{(f.cases ?? []).length}</td>
                <td data-testid="fix-updated">{fmtTs(f.updated)}</td>
              </tr>
            ))}
          </tbody>
        </MycroftTable>
      )}
      {openOne && <FixDrawer key={openOne.id + openOne.updated} f={openOne} may={may} onClose={() => navigate(LIST)} onChanged={changed} onToast={onToast} />}
    </MycroftPage>
  )
}

interface Draft {
  title: string
  symptoms: string
  cause: string
  steps: string
  commands: string
  prevention: string
}

const draftOf = (f: Fix): Draft => ({
  title: f.title,
  symptoms: f.symptoms ?? '',
  cause: f.cause ?? '',
  steps: f.steps ?? '',
  commands: (f.commands ?? []).join('\n'),
  prevention: f.prevention ?? '',
})

/** What Save sends: only the fields that changed, commands one a line. */
export function fixPatchOf(d: Draft, f: Fix): FixPatch {
  const was = draftOf(f)
  const out: FixPatch = {}
  if (d.title !== was.title) out.title = d.title.trim()
  if (d.symptoms !== was.symptoms) out.symptoms = d.symptoms.trim()
  if (d.cause !== was.cause) out.cause = d.cause.trim()
  if (d.steps !== was.steps) out.steps = d.steps.trim()
  const commands = d.commands
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
  if (commands.join('\n') !== (f.commands ?? []).join('\n')) out.commands = commands
  if (d.prevention !== was.prevention) out.prevention = d.prevention.trim()
  return out
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt>{label}</dt>
      <dd style={{ whiteSpace: 'pre-wrap' }}>{children}</dd>
    </>
  )
}

/** One fix: every field, the cases it was seen in, and for those who may change it, Edit, Accept or Activate, Deactivate and Delete. */
function FixDrawer({ f, may, onClose, onChanged, onToast }: { f: Fix; may: boolean; onClose: () => void; onChanged: (f: Fix | null, gone?: string) => void; onToast?: (msg: string) => void }) {
  const [d, setD] = useState<Draft | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [deleting, setDeleting] = useState(false)
  const act = async (fn: () => Promise<Fix>, done: (x: Fix) => string, gone = false) => {
    setBusy(true)
    setErr('')
    try {
      const x = await fn()
      onToast?.(done(x))
      setD(null)
      setDeleting(false)
      onChanged(gone ? null : x, gone ? f.id : undefined)
    } catch (e) {
      setErr(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const set = (p: Partial<Draft>) => setD((x) => (x ? { ...x, ...p } : x))
  const patch = d ? fixPatchOf(d, f) : {}
  const contradicts = f.contradicts ?? []
  const footer = may ? (
    <>
      {!d && (
        <button className="rk-btn" disabled={busy} onClick={() => setD(draftOf(f))} data-testid="fix-edit">
          Edit
        </button>
      )}
      {!d && f.status === 'pending' && (
        <button className="rk-btn" disabled={busy} title="The agents are given it, and it is no longer flagged as contradicting" onClick={() => void act(() => saveFix(f.id, { status: 'active' }), (x) => `${x.id} accepted: the agents are given it.`)} data-testid="fix-accept">
          Accept
        </button>
      )}
      {!d && f.status === 'inactive' && (
        <button className="rk-btn" disabled={busy} onClick={() => void act(() => saveFix(f.id, { status: 'active' }), (x) => `${x.id} is active: the agents are given it.`)} data-testid="fix-activate">
          Activate
        </button>
      )}
      {!d && f.status !== 'inactive' && (
        <button className="rk-btn" disabled={busy} onClick={() => void act(() => saveFix(f.id, { status: 'inactive' }), (x) => `${x.id} is inactive: the agents are no longer given it.`)} data-testid="fix-deactivate">
          Deactivate
        </button>
      )}
      {!d && (
        <button className="rk-btn" data-danger="true" disabled={busy} onClick={() => setDeleting(true)} data-testid="fix-delete">
          Delete
        </button>
      )}
      <span className="cs-grow" />
      {err && (
        <span className="tb-err" role="alert" data-testid="fix-error">
          {err}
        </span>
      )}
      {d && (
        <>
          <button className="rk-btn" disabled={busy} onClick={() => setD(null)}>
            Cancel
          </button>
          <button className="rk-btn" data-on disabled={busy || !d.title.trim() || Object.keys(patch).length === 0} onClick={() => void act(() => saveFix(f.id, patch), (x) => `${x.id} saved.`)} data-testid="fix-save">
            Save
          </button>
        </>
      )}
    </>
  ) : undefined
  return (
    <Drawer
      label="Fix"
      kicker={`${f.id} · created ${fmtTs(f.created)}${f.createdBy ? ` by ${f.createdBy}` : ''}`}
      title={f.title}
      copy={false}
      onClose={onClose}
      width={760}
      testId="fix-drawer"
      footer={footer}
      facts={<StatusCell f={f} />}
    >
      {f.status === 'pending' && (
        <p className="rk-banner" data-tone="warn" style={{ margin: 0 }} data-testid="fix-pending">
          <span>
            Pending: it contradicts{' '}
            {contradicts.length
              ? contradicts.map((id, i) => (
                  <span key={id}>
                    {i > 0 && ', '}
                    <a className="rk-link rk-mono" href={fixHref(id)}>
                      {id}
                    </a>
                  </span>
                ))
              : 'another fix'}
            . The agents are not given it until someone accepts it.
          </span>
        </p>
      )}
      {f.status === 'inactive' && (
        <p className="rk-banner" data-tone="info" style={{ margin: 0 }} data-testid="fix-inactive">
          {f.reason === 'duplicate' && f.duplicateOf ? (
            <span>
              Inactive: a duplicate of{' '}
              <a className="rk-link rk-mono" href={fixHref(f.duplicateOf)}>
                {f.duplicateOf}
              </a>
              , which the agents are given instead.
            </span>
          ) : (
            <span>Inactive: turned off by {f.reason === 'person' && f.updatedBy ? f.updatedBy : 'a person'}. The agents are not given it.</span>
          )}
        </p>
      )}
      {d ? (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="fix-editor">
          <label className="cs-lbl">
            Title
            <input className="cs-in" value={d.title} maxLength={200} onChange={(e) => set({ title: e.target.value })} data-testid="fix-title" />
          </label>
          <label className="cs-lbl">
            Symptoms: what it looks like
            <textarea className="cs-ta" value={d.symptoms} maxLength={4000} onChange={(e) => set({ symptoms: e.target.value })} data-testid="fix-symptoms" />
          </label>
          <label className="cs-lbl">
            Cause
            <textarea className="cs-ta" value={d.cause} maxLength={4000} onChange={(e) => set({ cause: e.target.value })} data-testid="fix-cause" />
          </label>
          <label className="cs-lbl">
            Steps: what to do
            <textarea className="cs-ta" value={d.steps} maxLength={4000} onChange={(e) => set({ steps: e.target.value })} data-testid="fix-steps" />
          </label>
          <label className="cs-lbl">
            Commands that worked, one a line
            <textarea className="cs-ta rk-mono" value={d.commands} maxLength={8000} onChange={(e) => set({ commands: e.target.value })} data-testid="fix-commands" />
          </label>
          <label className="cs-lbl">
            Prevention
            <textarea className="cs-ta" style={{ minHeight: 56 }} value={d.prevention} maxLength={4000} onChange={(e) => set({ prevention: e.target.value })} data-testid="fix-prevention" />
          </label>
        </div>
      ) : (
        <dl className="cs-kv" data-testid="fix-fields">
          <Field label="Symptoms">{f.symptoms || '—'}</Field>
          <Field label="Cause">{f.cause || '—'}</Field>
          <Field label="Steps">
            <span data-testid="fix-steps-text">{f.steps || '—'}</span>
          </Field>
          <dt>Commands</dt>
          <dd>
            {(f.commands ?? []).length ? (
              <pre className="rk-code" style={{ margin: 0, padding: '8px 12px', whiteSpace: 'pre-wrap' }} data-testid="fix-commands-text">
                {f.commands!.map((c) => `$ ${c}`).join('\n')}
              </pre>
            ) : (
              '—'
            )}
          </dd>
          <Field label="Prevention">{f.prevention || '—'}</Field>
          <Field label="Where first seen">
            {f.cluster || '—'}
            {f.resource ? <span className="rk-mono"> · {f.resource}</span> : null}
          </Field>
          {f.key && (
            <Field label="Problem key">
              <span className="rk-mono">{f.key}</span>
            </Field>
          )}
          <Field label="Created">
            {fmtTs(f.created)}
            {f.createdBy ? ` · ${f.createdBy}` : ''}
          </Field>
          <Field label="Updated">
            {fmtTs(f.updated)}
            {f.updatedBy ? ` · ${f.updatedBy}` : ''}
          </Field>
        </dl>
      )}
      <div className="cs-sec" data-testid="fix-seen">
        <span className="rk-h">Seen in</span>
        {(f.cases ?? []).length === 0 ? (
          <span className="tb-hint">No case yet.</span>
        ) : (
          <ol className="cs-msgs">
            {f.cases!.map((s, i) => (
              <li key={s.case + i} className="cs-msg" data-testid="fix-seen-case">
                <span className="cs-meta">
                  <a className="rk-link rk-mono" href={caseHref(s.case)}>
                    {s.case}
                  </a>
                  {s.cluster ? ` · ${s.cluster}` : ''} · {fmtTs(s.at)}
                  {s.by ? ` · ${s.by}` : ''}
                </span>
              </li>
            ))}
          </ol>
        )}
      </div>
      {deleting && (
        <ConfirmDialog title={`Delete ${f.id}?`} confirm="Delete" danger busy={busy} error={err} onCancel={() => setDeleting(false)} onConfirm={() => void act(() => deleteFix(f.id), (x) => `${x.id} deleted.`, true)}>
          <p style={{ margin: 0 }}>“{f.title}” is gone for good: the agents are no longer given it, and the cases it was saved from keep their RCA. Deactivate keeps it listed instead.</p>
        </ConfirmDialog>
      )}
    </Drawer>
  )
}
