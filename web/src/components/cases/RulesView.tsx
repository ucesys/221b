// Cases › Mycroft › Validation rules (Phase 12, ADR-138): the check ids
// the agents name their findings by, as a table as wide as the other
// Mycroft tables, sortable by any column, filtered by state, job and
// words. Since CF-85 it is one table: the rules, the ids the agents made
// up that are unlisted (other.…), and the rules the daily
// housekeeping suggests from them are rows alike, each with the same
// columns (id, meaning, case type, jobs, findings, runs, clusters, open
// cases, exceptions, last found, origin, state, and a control once any
// rule has one). Show: Active | Suggested | Unlisted | Retired |
// Replaced | All. Every row opens its drawer at
// #/cases/mycroft/rules/<id>, and its counts link into it
// (…/<id>/<section>): its cases with their status, the runs that found
// it, its latest findings with what became of each, its exceptions. In a
// rule's drawer a SuperAdmin edits its meaning, case type, jobs and
// control, retires it or makes it active, replaces it by another (its id
// kept as an alias), or deletes a rule added that nothing holds; Add rule
// adds one. Ids never change. A found id's drawer shows the agents'
// titles and descriptions of it (CF-86) and promotes it, as it is or
// edited, or dismisses it; a suggestion's says why it was made and lists
// the ids it groups: Promote, Edit then promote, or Dismiss, also from
// the Inbox, where it is a SuperAdmin's row (CF-87). Everyone who reads
// the cases reads the page.
import { useCallback, useEffect, useMemo, useRef, useState, type JSX, type ReactNode } from 'react'
import { CallError } from '../../api/call'
import { CASE_STATUS, CASE_TYPE_IDS, caseHref, caseTypeText, OUTCOMES, type CaseStatus, type Outcome } from '../../api/casework'
import { exceptionHref } from '../../api/lessons'
import {
  activateRule,
  addRule,
  deleteRule,
  dismissSuggestion,
  editRule,
  fetchRules,
  promoteRule,
  replaceRule,
  retireRule,
  ruleHref,
  type Behind,
  type Description,
  type FoundFinding,
  type FoundId,
  type PromoteInput,
  type Rule,
  type RuleCase,
  type RuleJob,
  type RulePatch,
  type RulesPage,
  type RuleState,
  type Suggestion,
} from '../../api/rules'
import { fmtTs } from '../../lib/format'
import { navigate } from '../../lib/router'
import { useSorted, type SortCol } from '../../lib/useSorted'
import { SortTh } from '../automation/SortTh'
import { ConfirmDialog } from '../ConfirmDialog'
import { Drawer } from '../Drawer'
import { Select } from '../Select'
import { knownHref, ROUTING } from './FindingParts'
import { runHref } from './jobsRoute'
import { errText, Tag } from './parts'
import { EmptyRow, MycroftPage, MycroftTable } from './MycroftPage'

/** The list's address, where a closed drawer goes back to. */
const LIST = '#/cases/mycroft/rules'

const errorText = (e: unknown) => (e instanceof CallError && e.status === 403 ? 'Only a SuperAdmin changes the validation rules; your role reads them, or not at all.' : errText(e))

/** A row's state: a rule's, a suggestion's, or an id found outside the list. */
export type RowState = RuleState | 'suggested' | 'found'

type Show = RowState | 'all'

const SHOW: [Show, string, string][] = [
  ['active', 'Active', 'The rules the agents are offered'],
  ['suggested', 'Suggested', 'Drafted by the daily housekeeping from the ids the agents made up: promote, edit or dismiss each'],
  ['found', 'Unlisted', 'Ids the agents made up (other.…) when no rule fitted: promote one to a rule'],
  ['retired', 'Retired', 'No longer offered; the cases and exceptions keyed by them still match'],
  ['replaced', 'Replaced', 'Merged into another rule: their ids are its aliases'],
  ['all', 'All', 'Every row'],
]

/** A state as its tag says it. */
const STATE_TEXT: Record<RowState, string> = { active: 'active', retired: 'retired', replaced: 'replaced', suggested: 'suggested', found: 'unlisted' }
const STATE_TONE: Record<RowState, string> = { active: 'ok', retired: 'muted', replaced: 'info', suggested: 'warn', found: 'muted' }
const STATE_ORDER: Record<RowState, number> = { active: 0, suggested: 1, found: 2, retired: 3, replaced: 4 }

/** Where a rule comes from, in words. */
export function originText(r: Rule): string {
  if (r.builtIn) return r.edited ? 'Built-in, edited' : 'Built-in'
  return r.auto ? 'Added by housekeeping' : 'Added'
}

/** One row of the table (CF-85): a rule, an id found outside the list, or a suggestion, with the same columns. */
export interface RuleRow {
  kind: 'rule' | 'found' | 'suggestion'
  id: string
  says: string
  /** The agents' latest description of a found id (CF-86); a suggestion's reasons. */
  description?: string
  type: string
  jobs: string[]
  findings: number
  runs: number
  clusters: string[]
  openCases: number
  exceptions: number
  lastFound?: string
  origin: string
  state: RowState
  control?: string
  /** Searched too: a rule's aliases, a suggestion's ids. */
  also: string[]
  rule?: Rule
  found?: FoundId
  suggestion?: Suggestion
}

/** The latest of the agents' descriptions. */
const latestText = (list?: Description[]) => [...(list ?? [])].sort((a, b) => (b.at ?? '').localeCompare(a.at ?? ''))[0]?.text

/** The meaning a description gives: its first sentence, at most 200 characters (as the server drafts it, CF-86). */
export function meaningOf(text?: string): string {
  const t = (text ?? '').trim()
  const i = t.indexOf('. ')
  const first = i >= 0 ? t.slice(0, i + 1) : t
  return first.length > 200 ? `${first.slice(0, 199)}…` : first
}

/** The words of an id an agent made up: other.etcd-slow is "etcd slow". */
const slugWords = (id: string) => id.replace(/^other\./, '').replace(/-/g, ' ')

const later = (a?: string, b?: string) => (!a ? b : !b ? a : a > b ? a : b)

/** The found ids a suggestion groups, as the page has them. */
const groupOf = (s: Suggestion, found: FoundId[]) => s.from.map((id) => found.find((f) => f.id === id)).filter((f): f is FoundId => !!f)

/** The table's rows: the rules, then the ids found outside the list, then the suggestions. */
export function rowsOf(page: RulesPage): RuleRow[] {
  const out: RuleRow[] = page.rules.map((r) => ({
    kind: 'rule',
    id: r.id,
    says: r.says,
    type: r.type,
    jobs: r.jobs,
    findings: r.findings,
    runs: r.runs ?? 0,
    clusters: r.clusters ?? [],
    openCases: r.openCases,
    exceptions: r.exceptions,
    lastFound: r.lastFound,
    origin: originText(r),
    state: r.state,
    control: r.control,
    also: r.aliases ?? [],
    rule: r,
  }))
  for (const f of page.found)
    out.push({
      kind: 'found',
      id: f.id,
      says: f.title ?? '',
      description: latestText(f.descriptions),
      type: f.type || 'other',
      jobs: f.jobs,
      findings: f.findings,
      runs: f.runs,
      clusters: f.clusters,
      openCases: f.openCases,
      exceptions: f.exceptions,
      lastFound: f.last,
      origin: 'Agent',
      state: 'found',
      also: f.suggestion ? [f.suggestion] : [],
      found: f,
    })
  for (const s of page.suggestions) {
    const group = groupOf(s, page.found)
    out.push({
      kind: 'suggestion',
      id: s.id,
      says: s.says,
      description: s.why,
      type: s.type,
      jobs: s.jobs,
      findings: group.reduce((n, f) => n + f.findings, 0),
      runs: s.runs,
      clusters: [...new Set(group.flatMap((f) => f.clusters))],
      openCases: group.reduce((n, f) => n + f.openCases, 0),
      exceptions: group.reduce((n, f) => n + f.exceptions, 0),
      lastFound: group.reduce<string | undefined>((at, f) => later(at, f.last), undefined),
      origin: 'Housekeeping',
      state: 'suggested',
      also: s.from,
      suggestion: s,
    })
  }
  return out
}

/** The rows a view keeps: by state, job and words in the id, meaning, description, control, aliases or grouped ids. */
export function rowsShown(list: RuleRow[], show: Show, job: string, q: string): RuleRow[] {
  const words = q.trim().toLowerCase()
  return list.filter(
    (r) =>
      (show === 'all' || r.state === show) &&
      (!job || r.jobs.includes(job)) &&
      (!words || [r.id, r.says, r.description ?? '', r.control ?? '', ...r.also].some((s) => s.toLowerCase().includes(words))),
  )
}

/** The row an address names: a rule first, then a suggestion, then an id found (a suggestion named after the one id it groups opens as the suggestion, which lists what that id found). */
export function rowOf(rows: RuleRow[], id: string): RuleRow | undefined {
  return rows.find((r) => r.kind === 'rule' && r.id === id) ?? rows.find((r) => r.kind === 'suggestion' && r.id === id) ?? rows.find((r) => r.kind === 'found' && r.id === id)
}

const time = (at?: string) => (at ? Date.parse(at) || null : null)

export const RULE_SORT: SortCol<RuleRow>[] = [
  { id: 'id', sortValue: (r) => r.id },
  { id: 'says', sortValue: (r) => r.says },
  { id: 'type', sortValue: (r) => caseTypeText(r.type) },
  { id: 'jobs', sortValue: (r) => r.jobs.length, defaultDir: 'desc' },
  { id: 'findings', sortValue: (r) => r.findings, defaultDir: 'desc' },
  { id: 'runs', sortValue: (r) => r.runs, defaultDir: 'desc' },
  { id: 'clusters', sortValue: (r) => r.clusters.length, defaultDir: 'desc' },
  { id: 'open', sortValue: (r) => r.openCases, defaultDir: 'desc' },
  { id: 'exceptions', sortValue: (r) => r.exceptions, defaultDir: 'desc' },
  { id: 'lastFound', sortValue: (r) => time(r.lastFound), defaultDir: 'desc' },
  { id: 'origin', sortValue: (r) => r.origin },
  { id: 'state', sortValue: (r) => STATE_ORDER[r.state] ?? 5 },
  { id: 'control', sortValue: (r) => r.control || null },
]

/** A row's id as a link to its drawer, without opening the row's own drawer twice. */
function RuleLink({ id, testId }: { id: string; testId?: string }) {
  return (
    <a className="rk-link rk-mono" href={ruleHref(id)} onClick={(e) => e.stopPropagation()} data-testid={testId}>
      {id}
    </a>
  )
}

/** A count in the table, linked to its list in the row's drawer (CF-85); a zero is plain. */
function CountLink({ id, n, section, testId }: { id: string; n: number; section: string; testId: string }) {
  if (!n) return <span data-testid={testId}>0</span>
  return (
    <a className="rk-link" href={`${ruleHref(id)}/${section}`} onClick={(e) => e.stopPropagation()} data-testid={testId}>
      {n}
    </a>
  )
}

/** Clusters by their names, each id in the hover, as cases name them (CF-60). */
function ClusterNames({ ids, names, none }: { ids: string[]; names?: Record<string, string>; none: string }) {
  if (!ids.length) return <>{none}</>
  return (
    <>
      {ids.map((id, i) => (
        <span key={id}>
          {i ? ', ' : ''}
          <span title={id} data-testid="rule-cluster">
            {names?.[id] || id}
          </span>
        </span>
      ))}
    </>
  )
}

/** How many of a row's cases are open, in words: "both closed", "all open", "1 open". */
export function casesState(total: number, open: number): string {
  const all = total === 1 ? '' : total === 2 ? 'both ' : 'all '
  if (open === 0) return `${all}closed`
  if (open === total) return `${all}open`
  return `${open} open`
}

/** A row's cases (CF-85): "2: C-8, C-9, both closed", the first five linked. */
function CasesSummary({ total, open, list }: { total: number; open: number; list?: RuleCase[] }) {
  if (!total) return <>none</>
  const shown = (list ?? []).slice(0, 5)
  const more = total - shown.length
  return (
    <span data-testid="rule-cases-summary">
      {total}
      {shown.length > 0 && ': '}
      {shown.map((c, i) => (
        <span key={c.id}>
          {i ? ', ' : ''}
          <a className="rk-link rk-mono" href={caseHref(c.id)}>
            {c.id}
          </a>
        </span>
      ))}
      {shown.length > 0 && more > 0 ? ` and ${more} more` : ''}, {casesState(total, open)}
    </span>
  )
}

/** A row's findings: "2, from 1 run". */
const findingsText = (findings: number, runs: number) => `${findings}, from ${runs} ${runs === 1 ? 'run' : 'runs'}`

function StateCell({ r }: { r: Pick<RuleRow, 'state'> & { replacedBy?: string; dismissed?: boolean } }) {
  return (
    <span style={{ display: 'inline-flex', gap: 6, flexWrap: 'wrap', alignItems: 'center' }}>
      <Tag tone={STATE_TONE[r.state] ?? 'muted'} testId="rule-state">
        {STATE_TEXT[r.state] ?? r.state}
        {r.dismissed ? ' · dismissed' : ''}
      </Tag>
      {r.replacedBy && (
        <span className="tb-sub" data-testid="rule-replaced-by">
          by <RuleLink id={r.replacedBy} />
        </span>
      )}
    </span>
  )
}

const jobNames = (jobs: RuleJob[], ids: string[]) => ids.map((id) => jobs.find((j) => j.id === id)?.label ?? id).join(', ')

/** A text cut short for a cell, whole in its title. */
const short = (t: string, n = 140) => (t.length > n ? `${t.slice(0, n - 1)}…` : t)

/** Mycroft › Validation rules; open is the row a link names, section the list in its drawer a count links to. */
export function RulesView({ open, section = '', onToast, onCount }: { open: string; section?: string; onToast?: (msg: string) => void; onCount?: (n: number) => void }): JSX.Element {
  const [page, setPage] = useState<RulesPage | null>(null)
  const [error, setError] = useState('')
  const [show, setShow] = useState<Show>('active')
  const [job, setJob] = useState('')
  const [q, setQ] = useState('')
  const [form, setForm] = useState<FormState | null>(null)
  const [busy, setBusy] = useState(false)
  const [now] = useState(() => Date.now())
  const took = useCallback(
    (p: RulesPage) => {
      setPage(p)
      setError('')
      onCount?.(p.rules.filter((r) => r.state === 'active').length)
    },
    [onCount],
  )
  useEffect(() => {
    fetchRules().then(took, (e: unknown) => setError(errorText(e)))
  }, [took])
  const all = useMemo(() => (page ? rowsOf(page) : null), [page])
  const rows = useMemo(() => rowsShown(all ?? [], show, job, q), [all, show, job, q])
  const { sort, onSort, shown } = useSorted('mycroft-rules', RULE_SORT, rows, { col: 'id', dir: 'asc' }, now)
  const th = (col: string, label: string) => <SortTh col={col} label={label} sort={sort} onSort={onSort} />
  const may = !!page?.mayChange
  const jobs = page?.jobs ?? []
  const controls = (page?.rules ?? []).some((r) => r.control)
  const openOne = all && open ? rowOf(all, open) : undefined
  const count = (s: Show) => rowsShown(all ?? [], s, job, q).length
  /** A change made: the page as the server answered, a toast, and the form closed. */
  const act = async (fn: () => Promise<RulesPage>, done: string): Promise<boolean> => {
    setBusy(true)
    try {
      took(await fn())
      onToast?.(done)
      return true
    } catch (e) {
      setError(errorText(e))
      return false
    } finally {
      setBusy(false)
    }
  }
  return (
    <MycroftPage
      testId="mycroft-rules"
      lead="The validation rules the agents name their findings by: each finding becomes a case by its cluster, object and rule. The ids the agents made up when no rule fitted, and the rules the housekeeping suggests from them, are rows too: promote one to a rule, or dismiss it. Retire a rule to stop offering it (what is keyed by it still matches); replace one by another to merge them. Ids never change."
      filter={
        <div className="rk-seg" role="group" aria-label="Show" data-testid="rules-show">
          {SHOW.map(([v, label, tip]) => (
            <button key={v} type="button" aria-pressed={show === v} title={tip} onClick={() => setShow(v)} data-testid={`rules-show-${v}`}>
              {label}
              {all ? <span style={{ color: 'var(--fg3)', fontWeight: 400 }}> {count(v)}</span> : null}
            </button>
          ))}
        </div>
      }
      tools={
        <>
          <Select label="Job" value={job} options={[['', 'Every job'], ...jobs.map((j): [string, string] => [j.id, j.label])]} onChange={setJob} testId="rules-job" />
          <input className="cs-in" style={{ flex: '0 1 260px', minWidth: 0 }} value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search rules" aria-label="Search rules" data-testid="rules-search" />
        </>
      }
      action={
        may && (
          <button className="rk-btn" data-on disabled={busy} onClick={() => setForm({ mode: 'add', input: { from: [], id: '', says: '', type: 'other', jobs: jobs[0] ? [jobs[0].id] : [] } })} data-testid="rule-add">
            Add rule
          </button>
        )
      }
    >
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="rules-error">
          {error}
        </p>
      )}
      {all === null && !error && <p className="rk-fhint">Loading…</p>}
      {open && all && !openOne && (
        <p className="rk-fhint" style={{ margin: 0 }}>
          There is no validation rule {open}.
        </p>
      )}
      {all && (
        <MycroftTable label="Validation rules" testId="rules-table">
          <thead>
            <tr>
              {th('id', 'Validation rule')}
              {th('says', 'Meaning')}
              {th('type', 'Case type')}
              {th('jobs', 'Jobs')}
              {th('findings', 'Findings')}
              {th('runs', 'Runs')}
              {th('clusters', 'Clusters')}
              {th('open', 'Open cases')}
              {th('exceptions', 'Exceptions')}
              {th('lastFound', 'Last found')}
              {th('origin', 'Origin')}
              {th('state', 'State')}
              {controls && th('control', 'Control')}
            </tr>
          </thead>
          <tbody>
            {shown.length === 0 && (
              <EmptyRow cols={controls ? 13 : 12} testId="rules-empty">
                {show === 'suggested' ? 'No suggested rules waiting.' : show === 'found' ? 'None: every finding named a rule on the list.' : 'No rules match.'}
              </EmptyRow>
            )}
            {shown.map((r) => {
              const at = openOne === r
              return (
                <tr key={`${r.kind}:${r.id}`} className="cs-row" aria-selected={at} aria-current={at ? 'true' : undefined} onClick={() => navigate(ruleHref(r.id))} data-testid="rule-row" data-id={r.id} data-kind={r.kind}>
                  <td>
                    <RuleLink id={r.id} />
                    {r.kind === 'rule' && r.also.length > 0 && (
                      <div className="tb-sub" data-testid="rule-aliases">
                        also {r.also.join(', ')}
                      </div>
                    )}
                    {r.kind === 'found' && r.found?.suggestion && (
                      <div className="tb-sub" data-testid="rule-in-suggestion">
                        in suggestion <RuleLink id={r.found.suggestion} />
                      </div>
                    )}
                    {r.kind === 'suggestion' && (
                      <div className="tb-sub rk-mono" data-testid="rule-from">
                        from {r.also.join(', ')}
                      </div>
                    )}
                  </td>
                  <td>
                    {r.says || '—'}
                    {r.kind === 'found' && r.description && (
                      <div className="tb-sub" title={r.description} data-testid="rule-description">
                        {short(r.description)}
                      </div>
                    )}
                  </td>
                  <td>{caseTypeText(r.type)}</td>
                  <td data-testid="rule-jobs">{jobNames(jobs, r.jobs) || '—'}</td>
                  <td>
                    <CountLink id={r.id} n={r.findings} section="findings" testId="rule-findings" />
                  </td>
                  <td>
                    <CountLink id={r.id} n={r.runs} section="runs" testId="rule-runs" />
                  </td>
                  <td data-testid="rule-clusters">
                    <ClusterNames ids={r.clusters} names={page?.clusterNames} none="—" />
                  </td>
                  <td>
                    <CountLink id={r.id} n={r.openCases} section="cases" testId="rule-open" />
                  </td>
                  <td>
                    <CountLink id={r.id} n={r.exceptions} section="exceptions" testId="rule-exceptions" />
                  </td>
                  <td data-testid="rule-last-found">{r.lastFound ? fmtTs(r.lastFound) : '—'}</td>
                  <td data-testid="rule-origin">{r.origin}</td>
                  <td>
                    <StateCell r={{ state: r.state, replacedBy: r.rule?.replacedBy, dismissed: r.found?.dismissed }} />
                  </td>
                  {controls && <td>{r.control || '—'}</td>}
                </tr>
              )
            })}
          </tbody>
        </MycroftTable>
      )}
      {openOne && page && (
        <RowDrawer
          key={`${openOne.kind}:${openOne.id}:${openOne.rule?.updated ?? ''}`}
          row={openOne}
          page={page}
          may={may}
          section={section}
          onClose={() => navigate(LIST)}
          act={act}
          onForm={setForm}
        />
      )}
      {form && page && (
        <RuleForm
          state={form}
          page={page}
          busy={busy}
          onClose={() => setForm(null)}
          onSubmit={(input) => {
            const add = form.mode === 'add'
            const id = input.id?.trim() || input.from[0] || ''
            void act(
              () => (add ? addRule({ id, says: input.says, type: input.type, jobs: input.jobs, control: input.control || undefined }) : promoteRule({ ...input, id: input.id?.trim() || undefined })),
              add ? `${id} added: the next runs offer it.` : `${id} promoted: the next runs offer it.`,
            ).then((ok) => {
              if (ok) {
                setForm(null)
                if (!add) navigate(ruleHref(id))
              }
            })
          }}
        />
      )}
    </MycroftPage>
  )
}

/** A found id as Promote starts from it: kept as it is, the agents' description (else its findings' title) as its meaning, its cases' type, the jobs that found it. */
export function promoteOf(f: FoundId, jobs: RuleJob[]): PromoteInput {
  const known = f.jobs.filter((j) => jobs.some((x) => x.id === j))
  const says = meaningOf(latestText(f.descriptions)) || f.title || slugWords(f.id)
  return { from: [f.id], id: '', says, type: f.type || 'other', jobs: known.length ? known : jobs[0] ? [jobs[0].id] : [] }
}

/** A suggestion as Promote takes it, as drafted. */
const promoteSuggested = (s: Suggestion): PromoteInput => ({ from: s.from, id: s.id, says: s.says, type: s.type, jobs: s.jobs })

/** Add rule, or Promote: the form's start. */
interface FormState {
  mode: 'add' | 'promote'
  input: PromoteInput
}

/** The jobs as checkboxes. */
function JobBoxes({ jobs, value, onChange, testId }: { jobs: RuleJob[]; value: string[]; onChange: (v: string[]) => void; testId: string }) {
  return (
    <span style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
      {jobs.map((j) => (
        <label key={j.id} className="cs-inline" style={{ color: 'var(--fg)' }}>
          <input type="checkbox" checked={value.includes(j.id)} onChange={(e) => onChange(e.target.checked ? [...value, j.id] : value.filter((x) => x !== j.id))} data-testid={`${testId}-${j.id}`} />
          {j.label}
        </label>
      ))}
    </span>
  )
}

const typeOptions = (types: string[]): [string, string][] => (types.length ? types : CASE_TYPE_IDS).map((t) => [t, caseTypeText(t)])

/** Add rule and Promote: the id (fixed once used), meaning, case type, jobs and control. */
function RuleForm({ state, page, busy, onClose, onSubmit }: { state: FormState; page: RulesPage; busy: boolean; onClose: () => void; onSubmit: (in_: PromoteInput) => void }) {
  const [d, setD] = useState<PromoteInput>(state.input)
  const add = state.mode === 'add'
  const set = (p: Partial<PromoteInput>) => setD((x) => ({ ...x, ...p }))
  const ok = !!d.says.trim() && d.jobs.length > 0 && (!add || !!d.id?.trim())
  return (
    <Drawer
      label={add ? 'Add rule' : 'Promote'}
      kicker={add ? 'A new validation rule' : `From ${state.input.from.join(', ')}`}
      title={add ? 'Add rule' : 'Promote to a validation rule'}
      copy={false}
      onClose={onClose}
      width={640}
      testId="rule-form"
      footer={
        <>
          <span className="cs-grow" />
          <button className="rk-btn" disabled={busy} onClick={onClose}>
            Cancel
          </button>
          <button className="rk-btn" data-on disabled={busy || !ok} onClick={() => onSubmit(d)} data-testid="rule-form-submit">
            {add ? 'Add' : 'Promote'}
          </button>
        </>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        <label className="cs-lbl">
          {add ? 'Id' : 'Id (empty keeps it as it is; another makes the found ids aliases of it)'}
          <input className="cs-in rk-mono" value={d.id ?? ''} maxLength={64} placeholder={add ? 'pod.host-path' : state.input.from[0]} onChange={(e) => set({ id: e.target.value })} data-testid="rule-form-id" />
          <span className="tb-hint" style={{ fontSize: 12 }}>
            Lower case words joined by dots. It never changes once a finding uses it.
          </span>
        </label>
        <label className="cs-lbl">
          Meaning
          <input className="cs-in" value={d.says} maxLength={200} placeholder="a pod mounts a host path" onChange={(e) => set({ says: e.target.value })} data-testid="rule-form-says" />
        </label>
        <span className="cs-lbl">
          Case type
          <Select label="Case type" value={d.type} options={typeOptions(page.types)} onChange={(v) => set({ type: v })} testId="rule-form-type" />
        </span>
        <span className="cs-lbl">
          Jobs offered it
          <JobBoxes jobs={page.jobs} value={d.jobs} onChange={(jobs) => set({ jobs })} testId="rule-form-job" />
        </span>
        {add && (
          <label className="cs-lbl">
            Standard control (optional)
            <input className="cs-in" value={d.control ?? ''} maxLength={60} placeholder="CIS 5.2.2" onChange={(e) => set({ control: e.target.value })} data-testid="rule-form-control" />
          </label>
        )}
      </div>
    </Drawer>
  )
}

function Field({ label, children, testId }: { label: string; children: ReactNode; testId?: string }) {
  return (
    <>
      <dt>{label}</dt>
      <dd style={{ whiteSpace: 'pre-wrap' }} data-testid={testId}>
        {children}
      </dd>
    </>
  )
}

/** A change, as a drawer asks the page for it. */
type Act = (fn: () => Promise<RulesPage>, done: string) => Promise<boolean>

/** A row's drawer (CF-85): a rule's, a found id's or a suggestion's. */
function RowDrawer({ row, page, may, section, onClose, act, onForm }: { row: RuleRow; page: RulesPage; may: boolean; section: string; onClose: () => void; act: Act; onForm: (f: FormState) => void }) {
  if (row.rule) return <RuleDrawer r={row.rule} page={page} may={may} section={section} onClose={onClose} act={act} />
  if (row.suggestion) return <SuggestionDrawer s={row.suggestion} page={page} may={may} section={section} onClose={onClose} act={act} onForm={onForm} />
  if (row.found) return <FoundDrawer f={row.found} page={page} may={may} section={section} onClose={onClose} act={act} onForm={onForm} />
  return null
}

const statusText = (c: { status: string; outcome?: string }) =>
  c.status === 'closed' && c.outcome ? `Closed: ${(OUTCOMES[c.outcome as Outcome] ?? c.outcome).toLowerCase()}` : (CASE_STATUS[c.status as CaseStatus] ?? c.status)

/** What became of a finding, as a tag, with the case, known issue or exception it went to. */
function FindingRouting({ f }: { f: FoundFinding }) {
  const r = f.routing ? (ROUTING[f.routing as keyof typeof ROUTING] ?? { label: f.routing, tone: 'muted', hint: '' }) : null
  return (
    <span style={{ display: 'inline-flex', gap: 6, alignItems: 'center' }} data-testid="rule-finding-routing" data-routing={f.routing ?? ''}>
      {r && (
        <span className="rk-tag" data-tone={r.tone} title={r.hint}>
          {r.label}
        </span>
      )}
      {f.case ? (
        <a className="rk-link rk-mono" href={caseHref(f.case)}>
          {f.case}
        </a>
      ) : f.knownIssue ? (
        <a className="rk-link rk-mono" href={knownHref(f.knownIssue)}>
          {f.knownIssue}
        </a>
      ) : f.exception ? (
        <a className="rk-link rk-mono" href={exceptionHref(f.exception)}>
          {f.exception}
        </a>
      ) : null}
    </span>
  )
}

/** One list of a drawer, scrolled into view when a count in the table linked to it. */
function Section({ id, title, at, testId, children }: { id: string; title: string; at: string; testId: string; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (at === id) ref.current?.scrollIntoView?.({ block: 'start' })
  }, [at, id])
  return (
    <div ref={ref} className="cs-sec" data-section={id} data-at={at === id ? 'true' : undefined} data-testid={testId}>
      <span className="rk-h">{title}</span>
      {children}
    </div>
  )
}

const listStyle = { margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5 } as const

/** What a row's counts stand for (CF-85): the agents' descriptions and titles of an id they made up, its latest findings, the runs that found it, its cases and its exceptions. */
function BehindLists({ b, jobs, names, at, showIds }: { b: Partial<Behind>; jobs: RuleJob[]; names?: Record<string, string>; at: string; showIds?: boolean }) {
  const descriptions = b.descriptions ?? []
  const titles = b.titles ?? []
  const latest = b.latest ?? []
  const runs = b.runIds ?? []
  const cases = b.caseList ?? []
  const exceptions = b.exceptionIds ?? []
  return (
    <>
      {descriptions.length > 0 && (
        <Section id="descriptions" title="What the agents say it means" at={at} testId="rule-descriptions">
          <ul style={listStyle}>
            {descriptions.map((d) => (
              <li key={`${d.id ?? ''}${d.job}${d.run}`} data-testid="rule-description-item">
                {showIds && d.id && <span className="rk-mono">{d.id}: </span>}
                {d.text}{' '}
                <span className="tb-sub">
                  — {jobNames(jobs, [d.kind || d.job])}, <a className="rk-link rk-mono" href={runHref(d.run)}>{d.run}</a>, {fmtTs(d.at)}
                </span>
              </li>
            ))}
          </ul>
        </Section>
      )}
      {titles.length > 0 && (
        <Section id="titles" title="Titles the agents gave" at={at} testId="rule-titles">
          <ul style={listStyle}>
            {titles.map((t) => (
              <li key={t.text}>
                {t.text} <span className="tb-sub">×{t.n}</span>
              </li>
            ))}
          </ul>
        </Section>
      )}
      <Section id="findings" title="Latest findings" at={at} testId="rule-findings-list">
        {latest.length === 0 ? (
          <span className="tb-hint">None kept: what was found before the runs were counted, or long ago, is counted only.</span>
        ) : (
          <ul style={listStyle}>
            {latest.map((f) => (
              <li key={`${f.run}${f.id ?? ''}${f.at}`} data-testid="rule-finding" data-run={f.run}>
                <span style={{ display: 'inline-flex', flexWrap: 'wrap', gap: 6, alignItems: 'center' }}>
                  <a className="rk-link rk-mono" href={runHref(f.run)} data-testid="rule-finding-run">
                    {f.run}
                  </a>
                  <span>{f.title || '—'}</span>
                  <span className="tb-sub">
                    <span title={f.cluster}>{(f.cluster && names?.[f.cluster]) || f.cluster}</span>
                    {f.object ? ` · ${f.object}` : ' · cluster-wide'}
                    {f.check ? ` · as ${f.check}` : ''} · {fmtTs(f.at)}
                  </span>
                  <FindingRouting f={f} />
                </span>
              </li>
            ))}
          </ul>
        )}
      </Section>
      <Section id="runs" title="Runs that found it" at={at} testId="rule-runs-list">
        {runs.length === 0 ? (
          <span className="tb-hint">None kept.</span>
        ) : (
          <span style={{ display: 'flex', flexWrap: 'wrap', gap: 8, fontSize: 12.5 }}>
            {runs.map((id) => (
              <a key={id} className="rk-link rk-mono" href={runHref(id)} data-testid="rule-run">
                {id}
              </a>
            ))}
          </span>
        )}
      </Section>
      <Section id="cases" title="Cases" at={at} testId="rule-cases-list">
        {cases.length === 0 ? (
          <span className="tb-hint">None.</span>
        ) : (
          <ul style={listStyle}>
            {cases.map((c) => (
              <li key={c.id} data-testid="rule-case" data-open={c.open ? 'true' : 'false'}>
                <a className="rk-link rk-mono" href={caseHref(c.id)}>
                  {c.id}
                </a>{' '}
                <Tag tone={c.open ? 'warn' : 'muted'} testId="rule-case-status">
                  {statusText(c)}
                </Tag>{' '}
                P{c.priority} · {c.title || '—'}
              </li>
            ))}
          </ul>
        )}
      </Section>
      {exceptions.length > 0 && (
        <Section id="exceptions" title="Active exceptions" at={at} testId="rule-exceptions-list">
          <span style={{ display: 'flex', flexWrap: 'wrap', gap: 8, fontSize: 12.5 }}>
            {exceptions.map((id) => (
              <a key={id} className="rk-link rk-mono" href={exceptionHref(id)} data-testid="rule-exception">
                {id}
              </a>
            ))}
          </span>
        </Section>
      )}
    </>
  )
}

/** The kinds of objects found, most first: "ClusterRoleBinding ×4". */
const kindsText = (kinds?: Record<string, number>) =>
  Object.entries(kinds ?? {})
    .sort((a, b) => b[1] - a[1])
    .map(([k, n]) => `${k} ×${n}`)
    .join(', ')

/** Several found ids' lists as one, for a suggestion's drawer. */
export function behindOf(list: FoundId[]): Partial<Behind> {
  const out: Behind = { caseList: [], runIds: [], latest: [], exceptionIds: [], titles: [], kinds: {}, descriptions: [] }
  for (const f of list) {
    for (const c of f.caseList ?? []) if (!out.caseList.some((x) => x.id === c.id)) out.caseList.push(c)
    for (const r of f.runIds ?? []) if (!out.runIds.includes(r)) out.runIds.push(r)
    out.latest.push(...(f.latest ?? []))
    for (const e of f.exceptionIds ?? []) if (!out.exceptionIds.includes(e)) out.exceptionIds.push(e)
    for (const [k, n] of Object.entries(f.kinds ?? {})) out.kinds![k] = (out.kinds![k] ?? 0) + n
  }
  const num = (s: string) => Number(s.replace(/^\D+/, '')) || 0
  out.caseList.sort((a, b) => Number(b.open) - Number(a.open) || num(b.id) - num(a.id))
  out.runIds.sort((a, b) => num(b) - num(a))
  out.latest.sort((a, b) => b.at.localeCompare(a.at))
  out.latest = out.latest.slice(0, 50)
  return out
}

/** An id an agent made up, unlisted: what the agents said of it and found, and for a SuperAdmin Promote, Edit then promote, or Dismiss. */
function FoundDrawer({ f, page, may, section, onClose, act, onForm }: { f: FoundId; page: RulesPage; may: boolean; section: string; onClose: () => void; act: Act; onForm: (f: FormState) => void }) {
  const [busy, setBusy] = useState(false)
  const run = async (fn: () => Promise<RulesPage>, done: string) => {
    setBusy(true)
    const ok = await act(fn, done)
    setBusy(false)
    return ok
  }
  const input = promoteOf(f, page.jobs)
  const footer = may ? (
    <>
      <button className="rk-btn" data-on disabled={busy} title="Promote it as it is, with the meaning drafted from what the agents said" onClick={() => void run(() => promoteRule(input), `${f.id} promoted: the next runs offer it.`).then((ok) => ok && navigate(ruleHref(f.id)))} data-testid="found-promote">
        Promote
      </button>
      <button className="rk-btn" disabled={busy} title="Change its id, meaning, case type or jobs, then promote it" onClick={() => onForm({ mode: 'promote', input })} data-testid="found-edit">
        Edit then promote
      </button>
      {!f.dismissed && !f.suggestion && (
        <button className="rk-btn" disabled={busy} title="The housekeeping no longer suggests it" onClick={() => void run(() => dismissSuggestion(f.id), `${f.id} dismissed: it is not suggested.`)} data-testid="found-dismiss">
          Dismiss
        </button>
      )}
    </>
  ) : undefined
  return (
    <Drawer label="Validation rule" kicker="Unlisted · named by an agent" title={f.id} onClose={onClose} width={720} testId="rule-drawer" footer={footer} facts={<StateCell r={{ state: 'found' }} />}>
      {f.suggestion && (
        <p className="rk-banner" data-tone="info" style={{ margin: 0 }} data-testid="found-in-suggestion">
          <span>
            In suggestion <RuleLink id={f.suggestion} />: decide it there.
          </span>
        </p>
      )}
      {f.dismissed && (
        <p className="rk-banner" data-tone="info" style={{ margin: 0 }} data-testid="found-dismissed">
          Dismissed: the housekeeping does not suggest it. It can still be promoted.
        </p>
      )}
      <dl className="cs-kv" data-testid="rule-fields">
        <Field label="Meaning">{f.title || 'no title yet'}</Field>
        <Field label="Case type">{caseTypeText(f.type || 'other')}</Field>
        <Field label="Jobs">{jobNames(page.jobs, f.jobs) || 'none'}</Field>
        <Field label="Clusters">
          <ClusterNames ids={f.clusters} names={page.clusterNames} none="none" />
        </Field>
        {kindsText(f.kinds) && <Field label="Objects">{kindsText(f.kinds)}</Field>}
        <Field label="Findings">{findingsText(f.findings, f.runs)}</Field>
        <Field label="Cases">
          <CasesSummary total={f.cases} open={f.openCases} list={f.caseList} />
        </Field>
        {f.exceptions > 0 && <Field label="Exceptions">{f.exceptions} active</Field>}
        <Field label="Found">
          {f.first ? `first ${fmtTs(f.first)}` : '—'}
          {f.last ? `, last ${fmtTs(f.last)}` : ''}
        </Field>
      </dl>
      <BehindLists b={f} jobs={page.jobs} names={page.clusterNames} at={section} />
    </Drawer>
  )
}

/** A suggested rule: why the housekeeping made it, the ids it groups and what they found, and for a SuperAdmin Promote, Edit then promote, or Dismiss. */
function SuggestionDrawer({ s, page, may, section, onClose, act, onForm, inbox }: { s: Suggestion; page: RulesPage; may: boolean; section: string; onClose: () => void; act: Act; onForm: (f: FormState) => void; inbox?: boolean }) {
  const [busy, setBusy] = useState(false)
  const group = groupOf(s, page.found)
  const b = behindOf(group)
  b.descriptions = s.descriptions ?? group.flatMap((f) => (f.descriptions ?? []).map((d) => ({ ...d, id: f.id })))
  const decided = async (fn: () => Promise<RulesPage>, done: string) => {
    setBusy(true)
    const ok = await act(fn, done)
    setBusy(false)
    if (ok) onClose()
  }
  const footer = may ? (
    <>
      <button className="rk-btn" data-on disabled={busy} onClick={() => void decided(() => promoteRule(promoteSuggested(s)), `${s.id} promoted: the next runs offer it.`)} data-testid="suggestion-promote">
        Promote
      </button>
      <button className="rk-btn" disabled={busy} onClick={() => onForm({ mode: 'promote', input: promoteSuggested(s) })} data-testid="suggestion-edit">
        Edit then promote
      </button>
      <button className="rk-btn" disabled={busy} onClick={() => void decided(() => dismissSuggestion(s.from[0] ?? s.id), `${s.id} dismissed: it is not suggested again.`)} data-testid="suggestion-dismiss">
        Dismiss
      </button>
      {inbox && (
        <a className="rk-link" href={ruleHref(s.id)} style={{ marginLeft: 'auto', fontSize: 12.5 }} data-testid="suggestion-rules-link">
          Open in Validation rules
        </a>
      )}
    </>
  ) : undefined
  return (
    <Drawer label="Suggested rule" kicker="Suggested by the housekeeping" title={s.id} onClose={onClose} width={720} testId="rule-drawer" footer={footer} facts={<StateCell r={{ state: 'suggested' }} />}>
      {s.why && (
        <p className="rk-banner" data-tone="info" style={{ margin: 0 }} data-testid="suggestion-why">
          {s.why}
        </p>
      )}
      <dl className="cs-kv" data-testid="rule-fields">
        <Field label="Meaning">{s.says}</Field>
        <Field label="Case type">{caseTypeText(s.type)}</Field>
        <Field label="Jobs">{jobNames(page.jobs, s.jobs) || 'none'}</Field>
        <Field label="Groups" testId="suggestion-from">
          <span style={{ display: 'inline-flex', flexWrap: 'wrap', gap: 8 }}>
            {s.from.map((id) => (
              <a key={id} className="rk-link rk-mono" href={ruleHref(id)} data-testid="suggestion-from-id">
                {id}
              </a>
            ))}
          </span>
        </Field>
        <Field label="Found">
          in {s.runs} {s.runs === 1 ? 'run' : 'runs'} on {s.clusters} {s.clusters === 1 ? 'cluster' : 'clusters'}
        </Field>
        {kindsText(b.kinds) && <Field label="Objects">{kindsText(b.kinds)}</Field>}
        <Field label="Suggested">{fmtTs(s.created)}</Field>
      </dl>
      <BehindLists b={b} jobs={page.jobs} names={page.clusterNames} at={section} showIds />
    </Drawer>
  )
}

interface Draft {
  says: string
  type: string
  jobs: string[]
  control: string
}

const draftOf = (r: Rule): Draft => ({ says: r.says, type: r.type, jobs: [...r.jobs], control: r.control ?? '' })

/** What Save sends: only the fields that changed. */
export function rulePatchOf(d: Draft, r: Rule): RulePatch {
  const out: RulePatch = {}
  if (d.says.trim() !== r.says) out.says = d.says.trim()
  if (d.type !== r.type) out.type = d.type
  if ([...d.jobs].sort().join() !== [...r.jobs].sort().join()) out.jobs = d.jobs
  if (d.control.trim() !== (r.control ?? '')) out.control = d.control.trim()
  return out
}

/** One rule: everything about it, and for a SuperAdmin Edit, Retire or Make active, Replace by, and Delete for a rule added that nothing holds. */
function RuleDrawer({ r, page, may, section, onClose, act }: { r: Rule; page: RulesPage; may: boolean; section: string; onClose: () => void; act: Act }) {
  const [d, setD] = useState<Draft | null>(null)
  const [by, setBy] = useState<string | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [busy, setBusy] = useState(false)
  const run = async (fn: () => Promise<RulesPage>, done: string) => {
    setBusy(true)
    const ok = await act(fn, done)
    setBusy(false)
    if (ok) {
      setD(null)
      setBy(null)
      setDeleting(false)
    }
  }
  const set = (p: Partial<Draft>) => setD((x) => (x ? { ...x, ...p } : x))
  const patch = d ? rulePatchOf(d, r) : {}
  const targets = page.rules.filter((x) => x.state === 'active' && x.id !== r.id)
  const deletable = !r.builtIn && !r.used
  const idle = !d && by === null
  const footer = may ? (
    <>
      {idle && (
        <button className="rk-btn" disabled={busy} onClick={() => setD(draftOf(r))} data-testid="rule-edit">
          Edit
        </button>
      )}
      {idle && r.state === 'active' && (
        <button className="rk-btn" disabled={busy} title="The agents are no longer offered it; the cases and exceptions keyed by it still match" onClick={() => void run(() => retireRule(r.id), `${r.id} retired: the agents are no longer offered it.`)} data-testid="rule-retire">
          Retire
        </button>
      )}
      {idle && r.state !== 'active' && (
        <button className="rk-btn" disabled={busy} onClick={() => void run(() => activateRule(r.id), `${r.id} is active: the agents are offered it.`)} data-testid="rule-activate">
          Make active
        </button>
      )}
      {idle && r.state !== 'replaced' && targets.length > 0 && (
        <button className="rk-btn" disabled={busy} title="Merge it into another rule: its id is kept as an alias" onClick={() => setBy(targets[0]!.id)} data-testid="rule-replace">
          Replace by…
        </button>
      )}
      {idle && deletable && (
        <button className="rk-btn" data-danger="true" disabled={busy} onClick={() => setDeleting(true)} data-testid="rule-delete">
          Delete
        </button>
      )}
      <span className="cs-grow" />
      {d && (
        <>
          <button className="rk-btn" disabled={busy} onClick={() => setD(null)}>
            Cancel
          </button>
          <button className="rk-btn" data-on disabled={busy || !d.says.trim() || d.jobs.length === 0 || Object.keys(patch).length === 0} onClick={() => void run(() => editRule(r.id, patch), `${r.id} saved.`)} data-testid="rule-save">
            Save
          </button>
        </>
      )}
      {by !== null && (
        <>
          <Select label="Replace by" value={by} options={targets.map((x): [string, string] => [x.id, x.id])} onChange={setBy} testId="rule-replace-by" />
          <button className="rk-btn" disabled={busy} onClick={() => setBy(null)}>
            Cancel
          </button>
          <button className="rk-btn" data-on disabled={busy || !by} onClick={() => void run(() => replaceRule(r.id, by), `${r.id} replaced by ${by}: its findings route by ${by}.`)} data-testid="rule-replace-confirm">
            Replace
          </button>
        </>
      )}
    </>
  ) : undefined
  return (
    <Drawer label="Validation rule" kicker={`${originText(r)} · ${r.state}`} title={r.id} onClose={onClose} width={720} testId="rule-drawer" footer={footer} facts={<StateCell r={r} />}>
      {r.state === 'replaced' && r.replacedBy && (
        <p className="rk-banner" data-tone="info" style={{ margin: 0 }} data-testid="rule-replaced">
          <span>
            Replaced by <RuleLink id={r.replacedBy} />: its findings route by it, and the cases and exceptions keyed by either match both.
          </span>
        </p>
      )}
      {r.state === 'retired' && (
        <p className="rk-banner" data-tone="info" style={{ margin: 0 }} data-testid="rule-retired">
          Retired: the agents are no longer offered it. The cases and exceptions keyed by it still match.
        </p>
      )}
      {d ? (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="rule-editor">
          <label className="cs-lbl">
            Meaning
            <input className="cs-in" value={d.says} maxLength={200} onChange={(e) => set({ says: e.target.value })} data-testid="rule-says" />
          </label>
          <span className="cs-lbl">
            Case type
            <Select label="Case type" value={d.type} options={typeOptions(page.types)} onChange={(v) => set({ type: v })} testId="rule-type" />
          </span>
          <span className="cs-lbl">
            Jobs offered it
            <JobBoxes jobs={page.jobs} value={d.jobs} onChange={(jobs) => set({ jobs })} testId="rule-job" />
          </span>
          <label className="cs-lbl">
            Standard control (optional)
            <input className="cs-in" value={d.control} maxLength={60} placeholder="CIS 5.2.2" onChange={(e) => set({ control: e.target.value })} data-testid="rule-control" />
          </label>
        </div>
      ) : (
        <dl className="cs-kv" data-testid="rule-fields">
          <Field label="Meaning">{r.says}</Field>
          <Field label="Case type">{caseTypeText(r.type)}</Field>
          <Field label="Jobs">{jobNames(page.jobs, r.jobs) || 'none'}</Field>
          {r.control && <Field label="Control">{r.control}</Field>}
          <Field label="Origin">{originText(r)}</Field>
          {(r.aliases ?? []).length > 0 && (
            <Field label="Aliases">
              <span className="rk-mono" data-testid="rule-drawer-aliases">
                {r.aliases!.join(', ')}
              </span>
            </Field>
          )}
          <Field label="Cases">
            <CasesSummary total={r.cases} open={r.openCases} list={r.caseList} />
          </Field>
          {r.exceptions > 0 && <Field label="Exceptions">{r.exceptions} active</Field>}
          <Field label="Findings">
            {findingsText(r.findings, r.runs ?? 0)}
            {r.firstFound ? `, first ${fmtTs(r.firstFound)}` : ''}
            {r.lastFound ? `, last ${fmtTs(r.lastFound)}` : ''}
          </Field>
          <Field label="Clusters">
            <ClusterNames ids={r.clusters ?? []} names={page.clusterNames} none="none" />
          </Field>
          {r.updated && (
            <Field label="Changed">
              {fmtTs(r.updated)}
              {r.by ? ` · ${r.by}` : ''}
            </Field>
          )}
        </dl>
      )}
      {!d && <BehindLists b={r} jobs={page.jobs} names={page.clusterNames} at={section} />}
      {deleting && (
        <ConfirmDialog title={`Delete ${r.id}?`} confirm="Delete" danger busy={busy} onCancel={() => setDeleting(false)} onConfirm={() => void run(() => deleteRule(r.id), `${r.id} deleted.`).then(() => navigate(LIST))}>
          <p style={{ margin: 0 }}>Nothing holds it yet, so it goes for good. Once a finding uses a rule, it can only be retired.</p>
        </ConfirmDialog>
      )}
    </Drawer>
  )
}

/**
 * A suggested rule in the Inbox (CF-87): a SuperAdmin's row, opened in
 * the Inbox's own drawer, read from the rules' page, with Promote, Edit
 * then promote, and Dismiss; done, the Inbox reads its suggestions again.
 */
export function InboxSuggestionDrawer({ id, onClose, onDone, onToast }: { id: string; onClose: () => void; onDone: () => void; onToast?: (msg: string) => void }): JSX.Element {
  const [page, setPage] = useState<RulesPage | null>(null)
  const [error, setError] = useState('')
  const [form, setForm] = useState<FormState | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    fetchRules().then(setPage, (e: unknown) => setError(errorText(e)))
  }, [])
  const s = page?.suggestions.find((x) => x.id === id || x.from[0] === id)
  const act: Act = async (fn, done) => {
    setBusy(true)
    try {
      setPage(await fn())
      onToast?.(done)
      onDone()
      return true
    } catch (e) {
      setError(errorText(e))
      return false
    } finally {
      setBusy(false)
    }
  }
  if (!page || !s) {
    return (
      <Drawer label="Suggested rule" kicker="Suggested by the housekeeping" title={id} onClose={onClose} width={720} testId="rule-drawer">
        <p className={error ? 'rk-t' : 'rk-fhint'} data-tone={error ? 'err' : undefined} role={error ? 'alert' : undefined} style={{ margin: 0 }} data-testid="rules-error">
          {error || (page ? `There is no suggested rule ${id} any more: it was promoted or dismissed.` : 'Loading…')}
        </p>
      </Drawer>
    )
  }
  return (
    <>
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="rules-error">
          {error}
        </p>
      )}
      <SuggestionDrawer s={s} page={page} may={page.mayChange} section="" onClose={onClose} act={act} onForm={setForm} inbox />
      {form && (
        <RuleForm
          state={form}
          page={page}
          busy={busy}
          onClose={() => setForm(null)}
          onSubmit={(input) => {
            const to = input.id?.trim() || input.from[0] || ''
            void act(() => promoteRule({ ...input, id: input.id?.trim() || undefined }), `${to} promoted: the next runs offer it.`).then((ok) => {
              if (ok) {
                setForm(null)
                onClose()
              }
            })
          }}
        />
      )}
    </>
  )
}
