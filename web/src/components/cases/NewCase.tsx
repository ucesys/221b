// New Case (CW-02, CU-02; since wave 6 with what is like it): a case
// opened by hand, at #/cases/inbox/case/new (the case drawer's address, so
// the Inbox opens it like any case): a title, what is wrong, the cluster
// it is on (or none), what it is about in any words (a Kubernetes object,
// a host's disk, a path, a service, or nothing), a priority P0 to P4, its
// type (other unless picked) and an incident number elsewhere (wave 6c,
// CF-36, CF-37).
// While the person types, what is like it shows beside the form (the
// issue service's match; no agent runs): an open case to add to instead,
// a known issue to link (the case is made closed as it), an exception
// that explains why the agents are quiet, a case closed lately as no
// action needed (CF-90) to open or reopen, and one fixed lately to
// open this one as its regression. Opening the case anyway is always
// there. Opening it starts the case analysis job when it is on and the
// case names a cluster; the drawer of the new case opens then.
import { useEffect, useState } from 'react'
import { caseHref, caseNumber, CASE_STATUS, CASE_TYPE_IDS, CASE_TYPES, fetchCases, fetchSimilar, incidentError, INCIDENT_MAX, joinCase, OUTCOMES, openCase, reopenCase, type CasePriority, type CaseType, type NewCaseInput, type Similar, type SimilarCase } from '../../api/casework'
import { navigate } from '../../lib/router'
import { Drawer } from '../Drawer'
import { Select } from '../Select'
import { errText, isPriority, KNOWN_STATUS, knownTone, PRIORITIES, PRIORITY, Tag, who } from './parts'

/** What a new case may start with: the address's ?title=…&cluster=…&resource=…&priority=… */
export interface NewCasePrefill {
  title?: string
  detail?: string
  cluster?: string
  resource?: string
  priority?: number
  type?: CaseType
  incident?: string
}

/** The prefill an address carries, after its "?". */
export function prefillOf(params: URLSearchParams): NewCasePrefill {
  const raw = params.get('priority')
  const p = raw === null || raw === '' ? NaN : Number(raw)
  return {
    title: params.get('title') ?? undefined,
    detail: params.get('detail') ?? undefined,
    cluster: params.get('cluster') ?? undefined,
    resource: params.get('resource') ?? undefined,
    priority: isPriority(p) ? p : undefined,
    type: (CASE_TYPE_IDS as string[]).includes(params.get('type') ?? '') ? (params.get('type') as CaseType) : undefined,
    incident: params.get('incident') ?? undefined,
  }
}

/** Whether what a case is about names a Kubernetes object, which the case then links. */
export const namesObject = (s: string) => /^[^/\s]+\/[^/\s]+(\/[^/\s]+)?$/.test(s.trim())

/** How long the form waits after the last key before it asks what is like it. */
export const SIMILAR_AFTER = 500

/** Whether a similar answer has anything to show. */
export const anySimilar = (s: Similar | null) => !!s && s.open.length + s.known.length + s.exceptions.length + s.recentNoProblem.length + s.recentFixed.length > 0

export function NewCaseDrawer({ clusters: given, cluster, prefill = {}, onClose, onCreated, onToast }: { clusters?: { id: string; name: string }[]; cluster?: string; prefill?: NewCasePrefill; onClose: () => void; onCreated?: () => void; onToast?: (msg: string) => void }) {
  const [clusters, setClusters] = useState(given ?? null)
  const [title, setTitle] = useState(prefill.title ?? '')
  const [detail, setDetail] = useState(prefill.detail ?? '')
  const [where, setWhere] = useState(prefill.cluster ?? cluster ?? '')
  const [resource, setResource] = useState(prefill.resource ?? '')
  const [priority, setPriority] = useState<CasePriority>(isPriority(prefill.priority) ? (prefill.priority as CasePriority) : 2)
  const [type, setType] = useState<CaseType>(prefill.type ?? 'other')
  const [incident, setIncident] = useState(prefill.incident ?? '')
  const badIncident = incidentError(incident)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  // What is like it, kept with the words it answers.
  const [found, setFound] = useState<{ for: string; similar: Similar | null } | null>(null)
  // The clusters a case may be on, from the cases' list when not given.
  useEffect(() => {
    if (given) return
    let gone = false
    fetchCases({ pending: true }, true).then(
      (v) => !gone && setClusters(v.clusters),
      () => !gone && setClusters([]),
    )
    return () => {
      gone = true
    }
  }, [given])
  // What is like it, a moment after the person stops typing.
  const typed = title.trim().length >= 3
  const query = typed ? JSON.stringify([title.trim(), detail.trim(), where, resource.trim()]) : ''
  const similar = found && found.for === query ? found.similar : null
  const looking = typed && found?.for !== query
  useEffect(() => {
    if (!query) return
    const [t0, d0, w0, r0] = JSON.parse(query) as string[]
    let gone = false
    const t = setTimeout(() => {
      fetchSimilar({ title: t0!, detail: d0 || undefined, cluster: w0 || undefined, resource: r0 || undefined }).then(
        (v) => !gone && setFound({ for: query, similar: v }),
        // Matching is a help: without it the form still opens cases.
        () => !gone && setFound({ for: query, similar: null }),
      )
    }, SIMILAR_AFTER)
    return () => {
      gone = true
      clearTimeout(t)
    }
  }, [query])
  const list = clusters ?? []
  const ready = !!title.trim() && !badIncident
  const input = (more: Partial<NewCaseInput> = {}): NewCaseInput => ({ title: title.trim(), detail: detail.trim(), cluster: where, resource: resource.trim(), priority, type, ...(incident.trim() ? { incident: incident.trim() } : {}), ...more })
  const run = async <T extends { number: number }>(f: () => Promise<T>, done: (c: T) => string) => {
    setBusy(true)
    setErr('')
    try {
      const c = await f()
      onToast?.(done(c))
      onCreated?.()
      navigate(caseHref(c.number))
    } catch (e) {
      setErr(errText(e))
      setBusy(false)
    }
  }
  const submit = () => void run(() => openCase(input()), (c) => (c.analyzing ? `${c.id} is open; the Troubleshooter is analysing it.` : `${c.id} is open.`))
  // What is written, said once on the case it is added to.
  const words = detail.trim() ? `${title.trim()}\n\n${detail.trim()}` : title.trim()
  const choose = {
    join: (c: SimilarCase) => void run(() => joinCase(c.id, words), (v) => `You follow ${v.id}; what you wrote is added to it.`),
    reopen: (c: SimilarCase) => void run(() => reopenCase(c.id, `Reported again: ${words}`), (v) => `${v.id} is open again.`),
    known: (id: string) => void run(() => openCase(input({ knownIssue: id })), (v) => `${v.id} is made, closed as known issue ${id}.`),
    regression: (c: SimilarCase) => void run(() => openCase(input({ regressionOf: c.id })), (v) => `${v.id} is open, a regression of ${c.id}.`),
  }
  return (
    <Drawer
      label="New case"
      kicker="Cases"
      title="New case"
      onClose={onClose}
      width={980}
      copy={false}
      testId="case-new"
      footer={
        <span style={{ display: 'flex', gap: 8, width: '100%', alignItems: 'center' }}>
          <span className="tb-hint" style={{ fontSize: 12 }}>
            {anySimilar(similar) ? 'Something like it is listed: add to it, or open a new case anyway.' : 'The case analysis job analyses it when it is on and the case names a cluster (AIOps › Jobs).'}
          </span>
          <span className="cs-grow" />
          <button className="rk-btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="rk-btn" data-on disabled={busy || !ready} onClick={submit} data-testid="case-new-open">
            {anySimilar(similar) ? 'Open a new case anyway' : 'Open case'}
          </button>
        </span>
      }
    >
      <div style={{ display: 'flex', gap: 20, flexWrap: 'wrap', alignItems: 'flex-start' }}>
        <div className="cs-sec" style={{ flex: '1 1 380px', minWidth: 0 }}>
          <label className="cs-lbl">
            Title
            <input className="cs-in" value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} placeholder="What is wrong, in a line" data-testid="case-new-title" autoFocus />
          </label>
          <label className="cs-lbl">
            What you know
            <textarea className="cs-ta" value={detail} maxLength={4000} onChange={(e) => setDetail(e.target.value)} placeholder="Since when, what you saw, what you tried. Never a password or a token." data-testid="case-new-detail" />
          </label>
          <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            <span className="cs-lbl">
              Cluster
              <Select label="Cluster" value={where} options={[['', 'None'], ...list.map((c): [string, string] => [c.id, c.name || c.id])]} onChange={setWhere} testId="case-new-cluster" />
            </span>
            <span className="cs-lbl">
              Priority
              <Select label="Priority" value={String(priority)} options={PRIORITIES.map((p): [string, string] => [String(p), PRIORITY[p]!])} onChange={(v) => setPriority(Number(v) as CasePriority)} testId="case-new-priority" />
            </span>
            <span className="cs-lbl">
              Type
              <Select label="Type" value={type} options={CASE_TYPE_IDS.map((t): [string, string] => [t, CASE_TYPES[t]])} onChange={(v) => setType(v as CaseType)} testId="case-new-type" />
            </span>
          </div>
          <label className="cs-lbl">
            Incident number (optional)
            <input className="cs-in rk-mono" style={{ width: 220 }} value={incident} maxLength={INCIDENT_MAX} onChange={(e) => setIncident(e.target.value)} placeholder="INC0012345" data-testid="case-new-incident" />
            {badIncident && <span className="tb-err">{badIncident}</span>}
          </label>
          <label className="cs-lbl">
            What it is about (optional)
            <input className="cs-in" value={resource} maxLength={300} onChange={(e) => setResource(e.target.value)} placeholder="shop/Deployment/web, Node/worker-3, the disk of db-1 at 90%…" data-testid="case-new-resource" />
          </label>
          <span className="tb-hint" style={{ fontSize: 12 }} data-testid="case-new-resource-hint">
            {resource.trim() && namesObject(resource) && where ? 'A Kubernetes object: the case links it, with how long it has been running.' : 'Any words: an object (namespace/kind/name or kind/name), a host, a disk, a path, a service — or nothing.'}
          </span>
          {err && (
            <p className="tb-err" role="alert" style={{ margin: 0 }} data-testid="case-new-error">
              {err}
            </p>
          )}
        </div>
        <SimilarPanel similar={similar} looking={looking} typed={typed} busy={busy} choose={choose} />
      </div>
    </Drawer>
  )
}

interface Choose {
  join: (c: SimilarCase) => void
  reopen: (c: SimilarCase) => void
  known: (id: string) => void
  regression: (c: SimilarCase) => void
}

function Group({ title, hint, children, testId }: { title: string; hint?: string; children: React.ReactNode; testId: string }) {
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 6 }} aria-label={title} data-testid={testId}>
      <h3 className="rk-h" style={{ margin: 0, fontSize: 12 }}>
        {title}
      </h3>
      {hint && <span className="tb-hint" style={{ fontSize: 11.5 }}>{hint}</span>}
      {children}
    </section>
  )
}

const card: React.CSSProperties = { padding: '8px 10px', display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5 }

/** A case as the panel lists it: its id, title, status and owner. */
function CaseLine({ c }: { c: SimilarCase }) {
  return (
    <span style={{ display: 'flex', gap: 6, alignItems: 'baseline', flexWrap: 'wrap' }}>
      <a className="rk-link rk-mono" href={caseHref(c.number || caseNumber(c.id))} target="_blank" rel="noreferrer" title="Opens in a new tab; this form stays">
        {c.id}
      </a>
      <span style={{ minWidth: 0, overflowWrap: 'anywhere' }}>{c.title}</span>
      <span className="tb-hint">
        {c.status === 'closed' && c.outcome ? `closed as ${OUTCOMES[c.outcome].toLowerCase()}` : CASE_STATUS[c.status]}
        {c.assignee ? ` · ${who(c.assignee)}` : ''}
      </span>
    </span>
  )
}

/** What is like the case being written, beside the form. */
function SimilarPanel({ similar: s, looking, typed, busy, choose }: { similar: Similar | null; looking: boolean; typed: boolean; busy: boolean; choose: Choose }) {
  return (
    <aside className="rk-card" style={{ flex: '1 1 320px', minWidth: 0, padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 12 }} aria-label="Like this" data-testid="case-new-similar">
      <span className="rk-h">Like this</span>
      {!typed && <span className="tb-hint">Cases, known issues and exceptions like it show here as you type.</span>}
      {typed && looking && <span className="tb-hint">Looking…</span>}
      {typed && !looking && !s && (
        <span className="tb-hint" data-testid="similar-failed">
          Could not look for cases like it just now. You can still open the case.
        </span>
      )}
      {typed && s && !anySimilar(s) && (
        <span className="tb-hint" data-testid="similar-none">
          Nothing like it. Open the case.
        </span>
      )}
      {s && s.open.length > 0 && (
        <Group title="Open cases" hint="Add to one instead: you follow it, and what you wrote is added." testId="similar-open">
          {s.open.map((c) => (
            <div key={c.id} className="rk-card" style={card} data-testid={`similar-open-${c.id}`}>
              <CaseLine c={c} />
              <button className="rk-btn" data-size="sm" data-on style={{ alignSelf: 'flex-start' }} disabled={busy} onClick={() => choose.join(c)} data-testid={`similar-join-${c.id}`}>
                Add to {c.id}
              </button>
            </div>
          ))}
        </Group>
      )}
      {s && s.known.length > 0 && (
        <Group title="Known issues" hint="Link one: the case is made already closed as that known issue." testId="similar-known">
          {s.known.map((k) => (
            <div key={k.id} className="rk-card" style={card} data-testid={`similar-known-${k.id}`}>
              <span style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
                <span className="rk-mono">{k.id}</span> {k.title} <Tag tone={knownTone(k.status)}>{KNOWN_STATUS[k.status]}</Tag>
              </span>
              {k.workaround && <span>Workaround: {k.workaround}</span>}
              {k.expected && <span className="tb-hint">Expected fix: {k.expected}</span>}
              <button className="rk-btn" data-size="sm" style={{ alignSelf: 'flex-start' }} disabled={busy} onClick={() => choose.known(k.id)} data-testid={`similar-known-link-${k.id}`}>
                Link to {k.id}
              </button>
            </div>
          ))}
        </Group>
      )}
      {s && s.exceptions.length > 0 && (
        <Group title="Accepted exceptions" hint="Why the agents are quiet about this." testId="similar-exceptions">
          {s.exceptions.map((e) => (
            <div key={e.id} className="rk-card" style={card} data-testid="similar-exception">
              <span>
                <span className="rk-mono">{e.check}</span> {e.object ? `on ${e.object}` : 'cluster-wide'} · {e.cluster}
              </span>
              <span>“{e.reason}”</span>
              <span className="tb-hint">
                accepted by {e.by}
              </span>
            </div>
          ))}
        </Group>
      )}
      {s && s.recentNoProblem.length > 0 && (
        <Group title="Closed lately as no action needed" testId="similar-noproblem">
          {s.recentNoProblem.map((c) => (
            <div key={c.id} className="rk-card" style={card} data-testid={`similar-noproblem-${c.id}`}>
              <CaseLine c={c} />
              {c.explanation && <span>“{c.explanation}”</span>}
              <span style={{ display: 'flex', gap: 6 }}>
                <a className="rk-btn" data-size="sm" href={caseHref(c.number || caseNumber(c.id))} data-testid={`similar-open-case-${c.id}`}>
                  Open {c.id}
                </a>
                <button className="rk-btn" data-size="sm" disabled={busy} onClick={() => choose.reopen(c)} data-testid={`similar-reopen-${c.id}`}>
                  Reopen {c.id}
                </button>
              </span>
            </div>
          ))}
        </Group>
      )}
      {s && s.recentFixed.length > 0 && (
        <Group title="Fixed lately" hint="The problem is back? Open the case as a regression of the fixed one." testId="similar-fixed">
          {s.recentFixed.map((c) => (
            <div key={c.id} className="rk-card" style={card} data-testid={`similar-fixed-${c.id}`}>
              <CaseLine c={c} />
              {/* The RCA is part of the case (wave 6e): its cause, and the case's link above opens it whole. */}
              {c.rcaCause && (
                <span style={{ minWidth: 0, overflowWrap: 'anywhere' }} data-testid={`similar-rca-${c.id}`}>
                  RCA: {c.rcaCause}
                </span>
              )}
              <button className="rk-btn" data-size="sm" style={{ alignSelf: 'flex-start' }} disabled={busy} onClick={() => choose.regression(c)} data-testid={`similar-regression-${c.id}`}>
                Create as regression of {c.id}
              </button>
            </div>
          ))}
        </Group>
      )}
    </aside>
  )
}
