// The Inbox's drawers for the kinds with no drawer of their own (Phase 12,
// wave 5, IN-06): an approval and a knowledge draft, each at
// #/aiops/inbox/<kind>/<id>. A case opens in CaseDrawer, a proposed change
// in ProposalDrawer and a known-issue draft in KnownDrawer, their owners'.
import { useEffect, useState } from 'react'
import type { ApprovalRow } from '../../api/automation'
import { fetchPageRequests, type ConfluenceConflict, type DraftRun, type PageRequest } from '../../api/knowledge'
import { fmtTs } from '../../lib/format'
import { Drawer } from '../Drawer'
import { DecidedVersion, linkOf, RequestActions, WaitingRunFacts } from './InboxApprovals'
import { APPROVAL_KIND, APPROVAL_STATE } from './inboxRows'
import { ConflictCard, DraftRunCard } from './KnowledgeDraft'
import { Tag } from './parts'
import { PageRequestCard } from './WikiPageActions'

/** A drawer for what the Inbox no longer lists, or has not read yet. */
function Missing({ label, what, loaded, onClose, testId }: { label: string; what: string; loaded: boolean; onClose: () => void; testId: string }) {
  return (
    <Drawer label={label} kicker={label} title={what} onClose={onClose} width={640} copy={false} testId={testId}>
      <p className="tb-hint" data-testid="inbox-drawer-missing">
        {loaded ? `The Inbox lists no ${label.toLowerCase()} ${what} any more. All shows what was done too.` : 'Loading…'}
      </p>
    </Drawer>
  )
}

/**
 * An approval (CS-06): what waits, who asked and when, and who decided.
 * A console change is decided here, as it has no page of its own; every
 * other kind is reviewed where it lives (Review); a decided script version
 * is read here. A decision someone else made first says who and when
 * (IB-09).
 */
export function ApprovalDrawer({ id, row, loaded, onClose, onDecided }: { id: string; row: ApprovalRow | undefined; loaded: boolean; onClose: () => void; onDecided: () => void }) {
  const [conflict, setConflict] = useState('')
  if (!row) return <Missing label="Approval" what={id} loaded={loaded} onClose={onClose} testId="approval-drawer" />
  if (row.kind === 'script' && row.state !== 'pending') return <DecidedVersion row={row} onClose={onClose} />
  const link = linkOf(row)
  const state = APPROVAL_STATE[row.state]
  return (
    <Drawer
      label={`Approval: ${APPROVAL_KIND[row.kind]}`}
      kicker={`Approval · ${APPROVAL_KIND[row.kind]}`}
      title={row.what}
      facts={
        <span style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
          <Tag tone={state.tone} testId="approval-state">
            {state.text}
          </Tag>
          {(row.mine || row.own) && <Tag tone="info">yours</Tag>}
        </span>
      }
      footer={
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          {row.kind === 'admin' || (row.kind === 'run' && row.state === 'pending') ? (
            <RequestActions row={row} onConflict={setConflict} onDecided={onDecided} />
          ) : (
            link && (
              <a className="rk-btn" data-on={row.state === 'pending' ? true : undefined} href={link} data-testid={`approval-${row.kind}-review`}>
                {row.state === 'pending' ? 'Review' : 'Open'}
              </a>
            )
          )}
        </div>
      }
      onClose={onClose}
      width={640}
      copy={false}
      testId="approval-drawer"
    >
      <dl className="cs-kv">
        <dt>Cluster</dt>
        <dd>{row.kind === 'script' ? 'every fleet it runs on' : (row.clusterName ?? row.cluster ?? '—')}</dd>
        <dt>Requested by</dt>
        <dd>
          {row.requestedBy || '—'}
          {row.requestedAt ? `, ${fmtTs(row.requestedAt)}` : ''}
        </dd>
        {row.approvedBy && (
          <>
            <dt>{row.state === 'approved' ? 'Approved by' : 'Decided by'}</dt>
            <dd>
              {row.approvedBy}
              {row.approvedAt ? `, ${fmtTs(row.approvedAt)}` : ''}
            </dd>
          </>
        )}
        {row.comment && (
          <>
            <dt>Comment</dt>
            <dd data-testid="approval-comment">{row.comment}</dd>
          </>
        )}
      </dl>
      {row.overtaken && (
        <p className="tb-hint" data-testid="approval-overtaken">
          {row.overtaken}
        </p>
      )}
      {row.kind === 'run' && <WaitingRunFacts row={row} />}
      {conflict && (
        <p className="tb-err" role="alert" data-testid="approval-conflict">
          {conflict}
        </p>
      )}
      <p className="tb-hint" style={{ margin: 0 }}>
        {row.kind === 'admin'
          ? 'A console change has no page of its own: it is decided here. Whoever asked may only withdraw it.'
          : row.kind === 'run'
            ? 'A run’s own values on production are weighed as a change of the fleet’s values; these need a second admin. It is decided here: approving starts the run as whoever asked; whoever asked may only withdraw it.'
            : row.state === 'pending'
              ? 'Review opens it where it is read and approved. What you wrote or asked for yourself is left out where it takes a second person.'
              : 'Decided; Open shows it where it lives.'}
      </p>
    </Drawer>
  )
}

/** A knowledge draft (KN-08): a run of Mycroft's wiki changes with every page's diff, a Confluence page someone edited, or a request to retire a page or make it active again (KN-24). */
export function KnowledgeDrawer({
  id,
  run,
  conflict,
  request: waiting,
  loaded,
  onClose,
  onChanged,
  onToast,
}: {
  id: string
  run?: DraftRun
  conflict?: ConfluenceConflict
  request?: PageRequest
  loaded: boolean
  onClose: () => void
  onChanged: () => void
  onToast?: (msg: string) => void
}) {
  // A request the Inbox no longer lists was decided: just now, here, or
  // before the link was opened. It stays open, read from all the requests,
  // with who decided it.
  const [decided, setDecided] = useState<PageRequest>()
  useEffect(() => {
    if (waiting || run || conflict || !loaded) return
    let live = true
    fetchPageRequests(true).then(
      (r) => live && setDecided((r.requests ?? []).find((x) => x.id === id)),
      () => undefined,
    )
    return () => {
      live = false
    }
  }, [id, waiting, run, conflict, loaded])
  const request = waiting ?? (decided?.id === id ? decided : undefined)
  if (request && !run && !conflict) {
    return (
      <Drawer
        label="Wiki page request"
        kicker={`Wiki page · asked by ${request.name}, ${fmtTs(request.at)}`}
        title={`${request.action === 'activate' ? 'Make active again' : 'Retire'}: ${request.title}`}
        onClose={onClose}
        width={640}
        copy={false}
        testId="knowledge-drawer"
      >
        <PageRequestCard r={request} onChanged={onChanged} onToast={onToast} />
      </Drawer>
    )
  }
  if (!run && !conflict) return <Missing label="Knowledge draft" what={id} loaded={loaded} onClose={onClose} testId="knowledge-drawer" />
  return (
    <Drawer
      label="Knowledge draft"
      kicker={run ? `Knowledge draft · ${run.store} · started by ${run.startedBy ?? run.by}, ${fmtTs(run.started)}` : 'Knowledge draft · Confluence'}
      title={run ? `Wiki changes ${run.id}` : conflict!.title}
      onClose={onClose}
      width={960}
      copy={false}
      testId="knowledge-drawer"
    >
      {run && <DraftRunCard run={run} onChanged={onChanged} onToast={onToast} open />}
      {conflict && <ConflictCard c={conflict} onChanged={onChanged} onToast={onToast} />}
    </Drawer>
  )
}
