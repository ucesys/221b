// AIOps › Mycroft › Known issues (CS-13, CS-14; wave 6): the shared
// answers to repeat problems. Operators list them open, resolved or
// proposed, and write them in a drawer: title, symptoms in users' words,
// services, status, workaround, expected fix, owner, the dated updates,
// accepting an agent's draft, the cases closed as it, whether the agents
// still see it (how often, last), and following. A known issue can start
// from a report (#/aiops/known/new?from=12), which is linked to it once
// saved. Since wave 6g the table sorts by any of its main columns, and
// housekeeping's duplicates show as inactive, with what they duplicate;
// an operator makes one active again from its drawer.
import { useEffect, useState } from 'react'
import { acceptKnown, addKnownUpdate, createKnown, fetchKnownIssue, fetchReport, fetchServices, followKnownIssue, saveKnown, triageReport, type KnownInput, type KnownStatus, type OpKnown } from '../../api/issues'
import { caseHref } from '../../api/casework'
import { useIssueMe, useShared, type CasesAccess } from '../../lib/cases'
import { fmtTs } from '../../lib/format'
import { aiopsHref, navigate, withParams, type Route } from '../../lib/router'
import { useSorted, type SortCol } from '../../lib/useSorted'
import { SortTh } from '../automation/SortTh'
import { Drawer } from '../Drawer'
import { Select } from '../Select'
import { useAllKnown } from './count'
import { errText, firstLine, KNOWN_STATUS, knownTone, Tag, who } from './parts'
import { SERVICES } from './parts'
import { EmptyRow, MycroftPage, MycroftTable } from './MycroftPage'

type Show = 'open' | 'resolved' | 'drafts' | 'inactive' | 'all'

/** Which known issues a view keeps; the inactive ones only under Inactive and All. */
export function knownShown(list: OpKnown[], show: Show): OpKnown[] {
  return list.filter((k) => (show === 'all' ? true : show === 'inactive' ? !!k.inactive : k.inactive ? false : show === 'drafts' ? !!k.draft : show === 'resolved' ? !k.draft && k.status === 'resolved' : !k.draft && k.status !== 'resolved'))
}

const STATUS_ORDER: Record<KnownStatus, number> = { investigating: 0, identified: 1, fixInProgress: 2, resolved: 3 }

/** The table's sortable columns. */
export const KNOWN_SORT: SortCol<OpKnown>[] = [
  { id: 'id', sortValue: (k) => Number(k.id.replace(/\D/g, '')) || 0, defaultDir: 'desc' },
  { id: 'title', sortValue: (k) => k.title },
  { id: 'status', sortValue: (k) => STATUS_ORDER[k.status] ?? 4 },
  { id: 'cases', sortValue: (k) => (k.cases ?? []).length, defaultDir: 'desc' },
  { id: 'still', sortValue: (k) => (k.stillPresent ? Date.parse(k.stillPresent.last) || null : null), defaultDir: 'desc' },
  { id: 'updated', sortValue: (k) => Date.parse(k.updated) || null, defaultDir: 'desc' },
]

export function KnownTab({ route, access, onToast }: { route: Route; access: CasesAccess; onToast?: (msg: string) => void }) {
  const known = useAllKnown(true)
  const services = useShared(SERVICES, fetchServices)
  const p = route.params.get('show')
  const show: Show = p === 'resolved' || p === 'drafts' || p === 'inactive' || p === 'all' ? p : 'open'
  const id = route.path[1] ?? ''
  const [now] = useState(() => Date.now())
  const { sort, onSort, shown: rows } = useSorted('mycroft-known', KNOWN_SORT, knownShown(known.value ?? [], show), { col: 'updated', dir: 'desc' }, now)
  const th = (col: string, label: string) => <SortTh col={col} label={label} sort={sort} onSort={onSort} />
  const svc = (s: string) => services.value?.find((x) => x.id === s)?.name ?? s
  const from = Number(route.params.get('from') ?? '') || 0
  return (
    <MycroftPage
      testId="cases-known"
      lead="Known problems. A case can be closed as a Known issue. Agents will keep checking whether the problem still happens but will stop reporting on it."
      filter={
        <div className="rk-seg" role="group" aria-label="Show">
          {([['open', 'Open'], ['resolved', 'Resolved'], ...(access.operator ? [['drafts', 'Proposed'] as const, ['inactive', 'Inactive'] as const, ['all', 'All'] as const] : [])] as const).map(([v, label]) => (
            <button key={v} type="button" aria-pressed={show === v} onClick={() => navigate(withParams(route, { show: v === 'open' ? undefined : v }), true)} data-testid={`known-show-${v}`}>
              {label}
            </button>
          ))}
        </div>
      }
      action={
        access.operator && (
          <a className="rk-btn" data-on href={aiopsHref('known', ['new'])} data-testid="known-new">
            New known issue
          </a>
        )
      }
    >
      {known.error && <p className="tb-err">{known.error}</p>}
      <MycroftTable label="Known issues" testId="known-table">
        <thead>
          <tr>
            {th('id', 'Known issue')}
            {th('title', 'Title')}
            {th('status', 'Status')}
            <th>Services</th>
            <th>Expected fix</th>
            {access.operator && <th>Owner</th>}
            {access.operator && th('cases', 'Cases')}
            {th('still', 'Still seen')}
            {th('updated', 'Updated')}
          </tr>
        </thead>
        <tbody>
          {known.value !== null && rows.length === 0 && (
            <EmptyRow cols={9}>
              {show === 'drafts' ? 'No proposed known issue waits.' : show === 'resolved' ? 'Nothing resolved yet.' : show === 'inactive' ? 'No inactive known issue.' : show === 'all' ? 'No known issues yet.' : 'No known issue is open.'}
            </EmptyRow>
          )}
          {rows.map((k) => (
            <tr key={k.id} className="cs-row" aria-selected={id === k.id} onClick={() => navigate(aiopsHref('known', [k.id], show === 'open' ? {} : { show }))} data-testid={`known-row-${k.id}`}>
              <td className="rk-mono">
                <a href={aiopsHref('known', [k.id])} onClick={(e) => e.stopPropagation()}>
                  {k.id}
                </a>
              </td>
              <td>
                {k.title}
                {k.draft && (
                  <span className="rk-pill" data-tone="info" style={{ marginLeft: 6 }}>
                    proposed
                  </span>
                )}
                {k.inactive && (
                  <span className="rk-pill" data-tone="muted" style={{ marginLeft: 6 }} data-testid={`known-inactive-${k.id}`}>
                    inactive
                    {k.duplicateOf && (
                      <>
                        : duplicate of{' '}
                        <a className="rk-mono" href={aiopsHref('known', [k.duplicateOf])} onClick={(e) => e.stopPropagation()}>
                          {k.duplicateOf}
                        </a>
                      </>
                    )}
                  </span>
                )}
              </td>
              <td>
                <Tag tone={knownTone(k.status)}>{KNOWN_STATUS[k.status]}</Tag>
              </td>
              <td>{(k.services ?? []).map(svc).join(', ') || '—'}</td>
              <td>{k.kind === 'planned' && k.window ? `${fmtTs(k.window.start)} to ${fmtTs(k.window.end)}` : k.expected || '—'}</td>
              {access.operator && <td>{who(k.owner) || '—'}</td>}
              {access.operator && <td>{(k.cases ?? []).length || '—'}</td>}
              <td data-testid={`known-still-${k.id}`}>{k.stillPresent ? `×${k.stillPresent.count}, ${fmtTs(k.stillPresent.last)}` : '—'}</td>
              <td>{fmtTs(k.updated)}</td>
            </tr>
          ))}
        </tbody>
      </MycroftTable>
      {id && (
        <KnownDrawer
          key={id + from}
          id={id}
          from={from}
          operator={access.operator}
          onClose={() => navigate(aiopsHref('known', [], show === 'open' ? {} : { show }))}
          onSaved={(k, created) => {
            known.reload()
            if (created) navigate(aiopsHref('known', [k.id]), true)
          }}
          onToast={onToast}
        />
      )}
    </MycroftPage>
  )
}

interface Draft {
  title: string
  symptoms: string
  services: string[]
  status: KnownStatus
  workaround: string
  expected: string
  owner: string
  link: string
  kind: 'normal' | 'planned'
  start: string
  end: string
}

const blank: Draft = { title: '', symptoms: '', services: [], status: 'investigating', workaround: '', expected: '', owner: '', link: '', kind: 'normal', start: '', end: '' }

/** A date for a datetime-local field, in the browser's time. */
function local(at?: string): string {
  if (!at) return ''
  const d = new Date(at)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function draftOf(k: OpKnown): Draft {
  return {
    title: k.title,
    symptoms: (k.symptoms ?? []).join('\n'),
    services: k.services ?? [],
    status: k.status,
    workaround: k.workaround ?? '',
    expected: k.expected ?? '',
    owner: k.owner?.id ?? '',
    link: k.link ?? '',
    kind: k.kind,
    start: local(k.window?.start),
    end: local(k.window?.end),
  }
}

/** What the editor's fields save, for an existing known issue only what changed. */
export function inputOf(d: Draft, was?: OpKnown): KnownInput {
  const now = was ? draftOf(was) : blank
  const out: KnownInput = {}
  if (!was || d.title !== now.title) out.title = d.title.trim()
  const symptoms = d.symptoms
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
  if (!was || d.symptoms !== now.symptoms) out.symptoms = symptoms
  if (!was || d.services.join() !== now.services.join()) out.services = d.services
  if (!was || d.status !== now.status) out.status = d.status
  if (!was || d.workaround !== now.workaround) out.workaround = d.workaround.trim()
  if (!was || d.expected !== now.expected) out.expected = d.expected.trim()
  if (!was || d.owner !== now.owner) out.owner = { id: d.owner.trim() }
  if (!was || d.link !== now.link) out.link = d.link.trim()
  if (!was || d.kind !== now.kind) out.kind = d.kind
  if (d.kind === 'planned' && d.start && d.end && (!was || d.start !== now.start || d.end !== now.end)) out.window = { start: new Date(d.start).toISOString(), end: new Date(d.end).toISOString() }
  return out
}

interface DrawerProps {
  id: string
  from: number
  operator: boolean
  onClose: () => void
  onSaved: (k: OpKnown, created: boolean) => void
  onToast?: (msg: string) => void
}

/** One known issue: its editor for an operator, how it stands for anyone else. */
export function KnownDrawer({ id, from, operator, onClose, onSaved, onToast }: DrawerProps) {
  const me = useIssueMe()
  const isNew = id === 'new'
  const [k, setK] = useState<OpKnown | null>(null)
  const [d, setD] = useState<Draft | null>(isNew && !from ? blank : null)
  const [alike, setAlike] = useState<number[]>([])
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [update, setUpdate] = useState('')
  const [updateStatus, setUpdateStatus] = useState<KnownStatus | ''>('')
  const services = useShared(SERVICES, fetchServices)
  useEffect(() => {
    let gone = false
    if (!isNew) {
      fetchKnownIssue(id).then(
        (x) => {
          if (gone) return
          setK(x)
          setD(draftOf(x))
        },
        (e: unknown) => !gone && setErr(errText(e)),
      )
    } else if (from) {
      // From a report: its words are the first symptom, its service the
      // first service, and it and the open reports alike are linked once
      // the known issue is saved.
      fetchReport(from).then(
        (r) => {
          if (gone) return
          setD({ ...blank, title: firstLine(r.text, 80), symptoms: firstLine(r.text, 200), services: r.service ? [r.service] : [], owner: me?.person.id ?? '' })
          setAlike(r.similar ?? [])
        },
        (e: unknown) => !gone && setErr(errText(e)),
      )
    }
    return () => {
      gone = true
    }
    // me is read once, for the owner of a new one.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, from, isNew])
  const act = async (fn: () => Promise<OpKnown>, done: string, created = false) => {
    setBusy(true)
    setErr('')
    try {
      const x = await fn()
      setK(x)
      setD(draftOf(x))
      onSaved(x, created)
      onToast?.(done)
      return x
    } catch (e) {
      setErr(errText(e))
      return null
    } finally {
      setBusy(false)
    }
  }
  const save = async () => {
    if (!d) return
    if (isNew) {
      const x = await act(() => createKnown(inputOf(d)), 'Known issue created.', !from)
      if (!x || !from) return
      // Link the report it started from, and the open reports alike.
      const failed: number[] = []
      for (const n of [from, ...alike]) {
        try {
          await triageReport(n, { knownIssue: x.id })
        } catch {
          failed.push(n)
        }
      }
      onToast?.(failed.length ? `${x.id} created; #${failed.join(', #')} could not be linked.` : `${x.id} created, with ${1 + alike.length === 1 ? 'report' : 'reports'} #${[from, ...alike].join(', #')} linked.`)
      onSaved(x, true)
      return
    }
    if (k) await act(() => saveKnown(k.id, inputOf(d, k)), 'Saved.')
  }
  const title = isNew ? 'New known issue' : k ? k.title : id
  const kicker = isNew ? (from ? `From report #${from}` : 'Known issue') : k ? `${k.id} · ${k.source === 'agent' ? 'proposed by an agent' : `from ${k.source}`} · created ${fmtTs(k.created)}` : id
  const set = (p: Partial<Draft>) => setD((x) => (x ? { ...x, ...p } : x))
  const readOnly = !operator
  const footer =
    operator && d ? (
      <>
        {k?.draft && (
          <button className="rk-btn" disabled={busy} onClick={() => void act(() => acceptKnown(k.id), `${k.id} accepted: reports may match it now.`)} data-testid="known-accept">
            Accept the proposal
          </button>
        )}
        {k?.inactive && (
          <button className="rk-btn" disabled={busy} onClick={() => void act(() => saveKnown(k.id, { inactive: false }), `${k.id} is active again: reports and cases may match it.`)} data-testid="known-activate">
            Make active
          </button>
        )}
        {k && (
          <button className="rk-btn" disabled={busy} onClick={() => void act(() => followKnownIssue(k.id, !k.following), k.following ? 'You no longer follow it.' : 'You follow it now.')} data-testid="known-follow">
            {k.following ? 'Unfollow' : 'Follow'}
          </button>
        )}
        <span className="cs-grow" />
        {err && (
          <span className="tb-err" role="alert" data-testid="known-error">
            {err}
          </span>
        )}
        <button className="rk-btn" data-on disabled={busy || !d.title.trim()} onClick={() => void save()} data-testid="known-save">
          {isNew ? (from ? `Create and link ${alike.length + 1 === 1 ? 'the report' : `${alike.length + 1} reports`}` : 'Create') : 'Save'}
        </button>
      </>
    ) : k && !operator ? (
      <button className="rk-btn" disabled={busy} onClick={() => void act(() => followKnownIssue(k.id, !k.following), k.following ? 'You no longer follow it.' : 'You follow it now.')} data-testid="known-follow">
        {k.following ? 'Unfollow' : 'Follow'}
      </button>
    ) : undefined
  return (
    <Drawer label="Known issue" kicker={kicker} title={title} copy={false} onClose={onClose} width={760} testId="known-drawer" footer={footer} facts={k && <Tag tone={knownTone(k.status)}>{KNOWN_STATUS[k.status]}</Tag>}>
      {!d && !err && <p className="tb-hint">Loading…</p>}
      {!operator && err && <p className="tb-err">{err}</p>}
      {k?.draft && (
        <p className="rk-banner" data-tone="info" style={{ margin: 0 }}>
          Proposed by an agent: no report matches it and no reporter sees it until an operator accepts it.
        </p>
      )}
      {k?.inactive && (
        <p className="rk-banner" data-tone="info" style={{ margin: 0 }} data-testid="known-inactive">
          {k.duplicateOf ? (
            <span>
              Inactive: housekeeping found it a duplicate of{' '}
              <a className="rk-mono" href={aiopsHref('known', [k.duplicateOf])}>
                {k.duplicateOf}
              </a>
              . No case or report is matched to it until it is made active again.
            </span>
          ) : (
            <span>Inactive: no case or report is matched to it until it is made active again.</span>
          )}
        </p>
      )}
      {d && readOnly && k && (
        <dl className="cs-kv">
          <dt>Status</dt>
          <dd>{KNOWN_STATUS[k.status]}</dd>
          <dt>What you can do</dt>
          <dd>{k.workaround || '—'}</dd>
          <dt>Expected fix</dt>
          <dd>{k.expected || '—'}</dd>
          <dt>Last update</dt>
          <dd>{k.lastUpdate ? `${fmtTs(k.lastUpdate.at)}: ${k.lastUpdate.text}` : '—'}</dd>
        </dl>
      )}
      {d && !readOnly && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }} data-testid="known-editor">
          <label className="cs-lbl">
            Title
            <input className="cs-in" value={d.title} onChange={(e) => set({ title: e.target.value })} data-testid="known-title" />
          </label>
          <label className="cs-lbl">
            Symptoms, in the users' words: one a line; reports are matched against them
            <textarea className="cs-ta" value={d.symptoms} onChange={(e) => set({ symptoms: e.target.value })} data-testid="known-symptoms" />
          </label>
          <fieldset className="cs-lbl" style={{ border: 0, padding: 0, margin: 0 }}>
            <legend style={{ padding: 0, marginBottom: 4 }}>Services</legend>
            <span className="cs-checks">
              {(services.value ?? []).length === 0 && <span className="tb-hint">No services are defined yet.</span>}
              {(services.value ?? []).map((s) => (
                <label key={s.id} className="cs-inline">
                  <input type="checkbox" checked={d.services.includes(s.id)} onChange={(e) => set({ services: e.target.checked ? [...d.services, s.id] : d.services.filter((x) => x !== s.id) })} data-testid={`known-service-${s.id}`} />
                  {s.name}
                </label>
              ))}
            </span>
          </fieldset>
          <span className="cs-bar">
            <span className="cs-inline">
              Status
              <Select label="Status" value={d.status} options={(Object.keys(KNOWN_STATUS) as KnownStatus[]).map((s): [string, string] => [s, KNOWN_STATUS[s]])} onChange={(v) => set({ status: v as KnownStatus })} testId="known-status" />
            </span>
            <span className="cs-inline">
              Kind
              <Select
                label="Kind"
                value={d.kind}
                options={[
                  ['normal', 'A problem'],
                  ['planned', 'Planned maintenance'],
                ]}
                onChange={(v) => set({ kind: v as Draft['kind'] })}
                testId="known-kind"
              />
            </span>
            {d.kind === 'planned' && (
              <>
                <label className="cs-inline">
                  From
                  <input className="cs-in" type="datetime-local" value={d.start} onChange={(e) => set({ start: e.target.value })} data-testid="known-start" />
                </label>
                <label className="cs-inline">
                  to
                  <input className="cs-in" type="datetime-local" value={d.end} onChange={(e) => set({ end: e.target.value })} data-testid="known-end" />
                </label>
              </>
            )}
          </span>
          <label className="cs-lbl">
            What people can do meanwhile: sent to reporters word for word
            <textarea className="cs-ta" value={d.workaround} onChange={(e) => set({ workaround: e.target.value })} data-testid="known-workaround" />
          </label>
          <span className="cs-bar">
            <label className="cs-lbl" style={{ flex: '1 1 220px' }}>
              Expected fix, in words
              <input className="cs-in" value={d.expected} placeholder="today, 15:00" onChange={(e) => set({ expected: e.target.value })} data-testid="known-expected" />
            </label>
            <label className="cs-lbl" style={{ flex: '1 1 180px' }}>
              Owner, a user name
              <input className="cs-in" value={d.owner} onChange={(e) => set({ owner: e.target.value })} data-testid="known-owner" />
            </label>
            <label className="cs-lbl" style={{ flex: '1 1 260px' }}>
              Full page (Confluence, say)
              <input className="cs-in" value={d.link} placeholder="https://" onChange={(e) => set({ link: e.target.value })} data-testid="known-link" />
            </label>
          </span>
          {isNew && err && (
            <p className="tb-err" role="alert">
              {err}
            </p>
          )}
        </div>
      )}
      {k && (
        <div className="cs-sec" data-testid="known-updates">
          <span className="rk-h">Updates</span>
          {(k.updates ?? []).length === 0 && <span className="tb-hint">None yet.</span>}
          <ol className="cs-msgs">
            {[...(k.updates ?? [])].reverse().map((u, i) => (
              <li key={i} className="cs-msg">
                <span className="cs-meta">
                  {fmtTs(u.at)} · {who(u.by)}
                  {u.status ? ` · ${KNOWN_STATUS[u.status]}` : ''}
                </span>
                <span className="cs-text">{u.text}</span>
              </li>
            ))}
          </ol>
          {operator && (
            <form
              style={{ display: 'flex', flexDirection: 'column', gap: 6 }}
              onSubmit={(e) => {
                e.preventDefault()
                if (!update.trim()) return
                void act(() => addKnownUpdate(k.id, update.trim(), updateStatus || undefined), 'Update added; followers and linked reporters are told.').then((x) => {
                  if (!x) return
                  setUpdate('')
                  setUpdateStatus('')
                })
              }}
            >
              <textarea className="cs-ta" aria-label="An update, dated now" placeholder="What changed, for everyone who follows it" value={update} onChange={(e) => setUpdate(e.target.value)} data-testid="known-update-text" />
              <span className="cs-bar">
                <Select
                  label="Status with the update"
                  value={updateStatus}
                  options={[['', 'Status stays'], ...(Object.keys(KNOWN_STATUS) as KnownStatus[]).map((s): [string, string] => [s, `Now ${KNOWN_STATUS[s].toLowerCase()}`])]}
                  onChange={(v) => setUpdateStatus(v as KnownStatus | '')}
                  testId="known-update-status"
                />
                <button className="rk-btn" type="submit" disabled={busy || !update.trim()} data-testid="known-update-add">
                  Add update
                </button>
                {updateStatus === 'resolved' && <span className="tb-hint">Resolving tells its reporters; its closed cases stay closed. If the agents still see it, it comes back.</span>}
              </span>
            </form>
          )}
        </div>
      )}
      {k && (
        <div className="cs-sec" data-testid="known-still-present">
          <span className="rk-h">Still seen by the agents</span>
          {k.stillPresent ? (
            <span>
              ×{k.stillPresent.count}, last {fmtTs(k.stillPresent.last)}
              {k.status === 'resolved' ? ' · resolved: the next run of each job that saw it checks again' : ''}
            </span>
          ) : (
            <span className="tb-hint">Not seen by an agent.</span>
          )}
        </div>
      )}
      {k && operator && (
        <div className="cs-sec">
          <span className="rk-h">Cases closed as it</span>
          {(k.cases ?? []).length === 0 ? (
            <span className="tb-hint">None.</span>
          ) : (
            <span style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }} data-testid="known-linked">
              {k.cases!.map((id) => (
                <a key={id} className="rk-link rk-mono" href={caseHref(id)}>
                  {id}
                </a>
              ))}
            </span>
          )}
        </div>
      )}
    </Drawer>
  )
}
