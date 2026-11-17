// What a console change that waits for a second SuperAdmin asks for
// (AI-35): the approval drawer shows it in full, so whoever decides need
// not look for it elsewhere.
import { useEffect, useState } from 'react'
import { fetchHistory, fetchHistoryItem, type HistoryEntry, type HistoryText } from '../../api/ai'
import type { AdminRequest, ApprovalRow } from '../../api/automation'
import { fmtTs } from '../../lib/format'
import { adminHref } from '../../lib/router'
import { testText } from '../ai/HistoryDialog'
import { VersionDiff } from '../ai/TextDiff'

/** What the request changes, in a sentence, for the kinds other than an agent's instructions. */
export function requestSummary(q: AdminRequest, row: Pick<ApprovalRow, 'clusterName' | 'cluster'>): string {
  switch (q.kind) {
    case 'environment':
      return `The environment of ${row.clusterName ?? row.cluster ?? 'the cluster'} changes from ${q.from} to ${q.to}. Off production, what needs a second person on production no longer does there.`
    case 'superadmin': {
      const how = [q.super ? 'the SuperAdmin role' : '', q.groups?.length ? `the groups ${q.groups.join(', ')}` : ''].filter(Boolean).join(' and ')
      return `${q.user} becomes a SuperAdmin, by ${how || 'a change of role'}: they could change every setting and approve what others ask for.`
    }
    default:
      return ''
  }
}

/** The draft's side and the side it replaces, read from the agent's history. */
interface Sides {
  draft?: HistoryText
  base?: HistoryText
  gone?: boolean
}

/** The version a draft is compared with: the one in force while it waits; once it went live, the version before it. */
export function baseEntry(entries: HistoryEntry[], draft: HistoryEntry): HistoryEntry | undefined {
  if (draft.kind === 'live') {
    return entries.filter((e) => e.kind === 'live' && (e.n ?? 0) < (draft.n ?? 0)).sort((a, b) => (b.n ?? 0) - (a.n ?? 0))[0]
  }
  return entries.find((e) => e.inForce)
}

/** An agent's draft that waits to go live: which agent and draft, tested or not, its test, and its diff against the version in force. */
function PromptRequest({ q, row }: { q: AdminRequest; row: ApprovalRow }) {
  const agent = q.agent ?? ''
  const rev = q.rev ?? 0
  const [sides, setSides] = useState<Sides | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    if (!agent || !rev) return
    let live = true
    ;(async () => {
      const [list, draft] = await Promise.all([fetchHistory(agent), fetchHistoryItem(agent, 'draft', rev).catch(() => undefined)])
      if (!draft) return { gone: true }
      const b = baseEntry(list.entries ?? [], draft)
      const base = b ? await fetchHistoryItem(agent, 'live', b.n ?? 0) : undefined
      return { draft, base }
    })().then(
      (s) => live && setSides(s),
      (e: unknown) => live && setError(e instanceof Error ? e.message : String(e)),
    )
    return () => {
      live = false
    }
  }, [agent, rev])
  const test = sides?.draft?.test
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }} data-testid="approval-prompt">
      <dl className="cs-kv">
        <dt>Agent</dt>
        <dd>
          <a className="rk-link" href={adminHref('settings', ['agents', agent])} data-testid="approval-prompt-agent">
            {q.agentName || agent}
          </a>
        </dd>
        <dt>Draft</dt>
        <dd data-testid="approval-prompt-rev">
          Draft {rev}
          {sides?.draft?.by ? `, saved by ${sides.draft.by}` : ''}
          {sides?.draft?.at && !sides.draft.at.startsWith('0001') ? `, ${fmtTs(sides.draft.at)}` : ''}
        </dd>
        <dt>Goes live</dt>
        <dd data-testid="approval-prompt-how">{q.untested ? `untested, because: ${q.untested}` : 'tested'}</dd>
        <dt>Test</dt>
        <dd data-testid="approval-prompt-test">{test ? testText(test) : sides ? 'not tested' : '…'}</dd>
        <dt>Asked by</dt>
        <dd>
          {row.requestedBy || '—'}
          {row.requestedAt ? `, ${fmtTs(row.requestedAt)}` : ''}
        </dd>
      </dl>
      {error && (
        <p className="tb-err" role="alert">
          {error}
        </p>
      )}
      {sides?.gone && (
        <p className="tb-hint" data-testid="approval-prompt-gone">
          Draft {rev} is no longer kept: a draft that went live since dropped it.
        </p>
      )}
      {sides?.draft && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <span style={{ fontSize: 12.5, fontWeight: 600 }}>
            What changes: {sides.base ? (sides.base.n ? `version ${sides.base.n}` : 'the chart’s baseline') : 'nothing to compare with'} → draft {rev}
          </span>
          {sides.base && <VersionDiff from={sides.base} to={sides.draft} testId="approval-prompt-diff" />}
        </div>
      )}
      {!sides && !error && <p className="tb-hint">Reading the draft…</p>}
    </div>
  )
}

/** What a console change asks for (AI-35). */
export function AdminRequestDetail({ row }: { row: ApprovalRow }) {
  const q = row.request
  if (!q) return null
  if (q.kind === 'prompt') return <PromptRequest q={q} row={row} />
  const text = requestSummary(q, row)
  return text ? (
    <p style={{ margin: 0, fontSize: 13 }} data-testid="approval-request-summary">
      {text}
    </p>
  ) : null
}
