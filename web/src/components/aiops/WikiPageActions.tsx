// A wiki page's own actions (Phase 12, KN-23, KN-24), in its view in
// AIOps › Mycroft › Wiki and in the Inbox:
//
// - This is outdated…: an Admin or a SuperAdmin (who may add to the wiki)
//   writes what is outdated and what is true now. It becomes a note of
//   "Added in Mycroft" tied to the page, and Mycroft redrafts the page
//   with it; the redraft waits for review as every change does. The page
//   says a correction waits until the redraft is decided.
// - Retire page… and Make active again…: a reviewer asks, with a reason;
//   one other reviewer approves in AIOps › Inbox (the one who asked never
//   approves their own). A retired page is out of the index: no agent
//   reads it, and Mycroft does not rewrite it; it is never deleted.
import { useState } from 'react'
import { approvePageRequest, askPageRequest, pageHref, rejectPageRequest, sendOutdated, type PageRequest, type PageView } from '../../api/knowledge'
import { fmtTs } from '../../lib/format'
import { aiopsHref } from '../../lib/router'
import { DecideDialog } from '../automation/DecideDialog'
import { ConfirmDialog } from '../ConfirmDialog'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** What a request asks, in words. */
export const requestWords = (r: Pick<PageRequest, 'action'>) => (r.action === 'activate' ? 'make this page active again' : 'retire this page')

/** What waits for a page, or why it is retired: shown above its text. */
export function PageNotices({ page }: { page: PageView }) {
  const r = page.retired
  return (
    <>
      {r && (
        <div className="rk-callout" data-tone="warn" style={{ fontSize: 12.5, flexDirection: 'column', gap: 4 }} data-testid="wiki-page-retired">
          <strong>Retired {fmtTs(r.at)}</strong>
          <span data-testid="wiki-page-retired-reason">{r.reason}</span>
          <span className="rk-fhint">
            Asked by {r.askedBy}
            {r.askedAt ? `, ${fmtTs(r.askedAt)}` : ''}; approved by {r.approvedBy}. No agent reads it, and Mycroft does not rewrite it; its file stays where the wiki is kept.
          </span>
        </div>
      )}
      {page.correction && (
        <p className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5 }} data-testid="wiki-page-correction">
          Correction sent {fmtTs(page.correction.at)} by {page.correction.name}; {page.correction.state === 'drafted' ? 'the redraft waits for review under Drafts.' : 'Mycroft is redrafting the page with it.'}
        </p>
      )}
      {page.request && (
        <p className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5 }} data-testid="wiki-page-request">
          <span>
            {page.request.name} asked {fmtTs(page.request.at)} to {requestWords(page.request)}: “{page.request.reason}”. It waits for another Admin or SuperAdmin in{' '}
            <a className="rk-link" href={aiopsHref('inbox', ['knowledge', page.request.id])}>
              AIOps › Inbox
            </a>
            .
          </span>
        </p>
      )}
    </>
  )
}

type Dialog = 'outdated' | 'retire' | 'activate' | null

/**
 * The page's buttons and their dialogs: This is outdated… for who may add
 * to the wiki, Retire page… or Make active again… for who reviews it.
 */
export function PageActions({ page, mayAdd, mayReview, model, onChanged, onToast }: { page: PageView; mayAdd: boolean; mayReview: boolean; model: boolean; onChanged: () => void; onToast?: (msg: string) => void }) {
  const [open, setOpen] = useState<Dialog>(null)
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const retired = !!page.retired
  const show = (d: Dialog) => {
    setOpen(d)
    setText('')
    setError('')
  }
  const act = async (fn: () => Promise<string>) => {
    setBusy(true)
    setError('')
    try {
      // The call is made first: onToast?.() would skip its argument when no toast is given.
      const msg = await fn()
      onToast?.(msg)
      setOpen(null)
      onChanged()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const outdated = mayAdd && !retired
  const retire = mayReview && !retired && !page.request
  const activate = mayReview && retired && !page.request
  if (!outdated && !retire && !activate) return null
  return (
    <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }} data-testid="wiki-page-actions">
      {outdated && (
        <button className="rk-btn" onClick={() => show('outdated')} title="Say what is outdated and what is true now: Mycroft redrafts the page, for review" data-testid="wiki-page-outdated">
          This is outdated…
        </button>
      )}
      {retire && (
        <button className="rk-btn" onClick={() => show('retire')} title="Take the page out of what the agents read; another person approves" data-testid="wiki-page-retire">
          Retire page…
        </button>
      )}
      {activate && (
        <button className="rk-btn" onClick={() => show('activate')} title="Put the page back in what the agents read; another person approves" data-testid="wiki-page-activate">
          Make active again…
        </button>
      )}
      {open === 'outdated' && (
        <ConfirmDialog title="This is outdated…" confirm="Send" busy={busy} disabled={!text.trim()} error={error} onCancel={() => setOpen(null)} onConfirm={() => void act(async () => (await sendOutdated(page.path, text)).text)}>
          <span>
            What on <strong>{page.title}</strong> is outdated, and what is true now? Plain text or Markdown.
          </span>
          <textarea
            className="cs-ta"
            style={{ minHeight: 140 }}
            value={text}
            maxLength={200 << 10}
            onChange={(e) => setText(e.target.value)}
            aria-label="What is outdated, and what is true now"
            placeholder="The backups are kept 90 days now, on nas-2."
            data-testid="wiki-outdated-text"
          />
          <span className="rk-fhint" data-testid="wiki-outdated-hint">
            {model
              ? 'It is added as a correction of this page under Sources. Mycroft redrafts the page with it, the correction winning over older sources, and the redraft waits for review under Drafts.'
              : 'Mycroft has no model here: the correction is kept as a source the agents can quote, and no page is written from it.'}
          </span>
        </ConfirmDialog>
      )}
      {(open === 'retire' || open === 'activate') && (
        <ConfirmDialog
          title={open === 'retire' ? 'Retire this page' : 'Make this page active again'}
          confirm={open === 'retire' ? 'Ask to retire' : 'Ask to make active'}
          busy={busy}
          disabled={!text.trim()}
          error={error}
          onCancel={() => setOpen(null)}
          onConfirm={() => void act(async () => (await askPageRequest(open, page.path, text), 'Asked: another Admin or SuperAdmin approves it in the Inbox.'))}
        >
          <span>
            {open === 'retire'
              ? 'A retired page leaves what the agents read, and Mycroft does not rewrite it. It is never deleted, and can be made active again.'
              : 'The page goes back into what the agents read, and Mycroft may rewrite it again.'}{' '}
            Another Admin or SuperAdmin approves it in AIOps › Inbox.
          </span>
          <textarea className="cs-ta" style={{ minHeight: 80 }} value={text} maxLength={500} onChange={(e) => setText(e.target.value)} aria-label="Reason" placeholder="Why" data-testid="wiki-request-reason" />
        </ConfirmDialog>
      )}
    </div>
  )
}

const STATE: Record<PageRequest['state'], [string, string]> = { waiting: ['Waiting for approval', 'warn'], approved: ['Approved', 'ok'], rejected: ['Rejected', 'muted'] }

/** A request to retire a page or make it active again, in the Inbox: approved by one person other than who asked. */
export function PageRequestCard({ r, onChanged, onToast }: { r: PageRequest; onChanged: () => void; onToast?: (msg: string) => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [rejecting, setRejecting] = useState(false)
  const act = async (fn: () => Promise<unknown>, msg: string) => {
    setBusy(true)
    setError('')
    try {
      await fn()
      onToast?.(msg)
      setRejecting(false)
      onChanged()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const [state, tone] = STATE[r.state] ?? STATE.waiting
  const own = r.state === 'waiting' && (r.mine || r.canApprove === false)
  return (
    <section className="rk-card" style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 8 }} aria-label={`Request ${r.id}`} data-testid="page-request" data-request={r.id}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <span className="rk-pill" data-tone="info">
          {r.action === 'activate' ? 'Make active again' : 'Retire page'}
        </span>
        <a className="rk-link" style={{ fontWeight: 600 }} href={pageHref(r.path)}>
          {r.title}
        </a>
        <span className="rk-mono rk-fhint">{r.path}</span>
        <span style={{ flex: '1 1 auto' }} />
        <span className="rk-pill" data-tone={tone} data-testid="page-request-state">
          {state}
        </span>
      </div>
      <dl className="cs-kv">
        <dt>Reason</dt>
        <dd data-testid="page-request-reason">{r.reason}</dd>
        <dt>Asked by</dt>
        <dd>
          {r.name}, {fmtTs(r.at)}
        </dd>
        {r.decidedName && (
          <>
            <dt>{r.state === 'approved' ? 'Approved by' : 'Rejected by'}</dt>
            <dd>
              {r.decidedName}
              {r.decided ? `, ${fmtTs(r.decided)}` : ''}
              {r.comment ? `: ${r.comment}` : ''}
            </dd>
          </>
        )}
      </dl>
      {error && !rejecting && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
          {error}
        </p>
      )}
      {r.state === 'waiting' && (
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
          <button
            className="rk-btn"
            data-primary="true"
            disabled={busy || own}
            onClick={() => void act(() => approvePageRequest(r.id), r.action === 'activate' ? 'Active again.' : 'Retired.')}
            title={own ? 'You asked for this: another person approves it' : undefined}
            data-testid="page-request-approve"
          >
            Approve
          </button>
          <button className="rk-btn" disabled={busy} onClick={() => setRejecting(true)} data-testid="page-request-reject">
            {own ? 'Withdraw…' : 'Reject…'}
          </button>
          {/* The one Reject… and Withdraw… dialog (I-4); the reason stays optional here, as it was. */}
          {rejecting && (
            <DecideDialog
              how={own ? 'withdraw' : 'reject'}
              what={`the request to ${r.action === 'activate' ? 'make active again' : 'retire'} ${r.title}`}
              reasonRequired={false}
              busy={busy}
              error={error}
              onCancel={() => {
                setRejecting(false)
                setError('')
              }}
              onConfirm={(comment) => void act(() => rejectPageRequest(r.id, comment), own ? 'Withdrawn.' : 'Rejected.')}
            >
              {own ? 'You asked for this. Withdrawing leaves the page as it is.' : `${r.name} reads your reason. Rejecting leaves the page as it is.`}
            </DecideDialog>
          )}
          <span className="rk-fhint" data-testid="page-request-rule">
            {own ? 'You asked for this: another Admin or SuperAdmin approves it.' : 'One approval other than the person who asked decides it.'}
          </span>
        </div>
      )}
    </section>
  )
}
