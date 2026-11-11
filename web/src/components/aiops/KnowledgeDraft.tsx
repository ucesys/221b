import { useState } from 'react'
import { approveDraft, contradictionText, rejectDraft, resolveConflict, type ConfluenceConflict, type DraftPage, type DraftRun } from '../../api/knowledge'
import { fixHref } from '../../api/fixes'
import { fmtTs } from '../../lib/format'
import { SourceLink } from './SourceLink'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** A page's unified diff, its lines coloured by what they do. */
/**
 * A page's diff. Its lines wrap, as a wiki page's lines are paragraphs: a
 * line never widens the page. The page's front matter (its sources and
 * dates, already listed above the diff) is folded into one line that opens.
 */
export function Diff({ text }: { text: string }) {
  const [meta, setMeta] = useState(false)
  const lines = text.split('\n')
  const fm = frontMatter(lines)
  const line = (l: string, i: number) => {
    const tone = l.startsWith('+++') || l.startsWith('---') ? 'var(--fg3)' : l.startsWith('+') ? 'var(--ok, #1a7f37)' : l.startsWith('-') ? 'var(--err, #cf222e)' : l.startsWith('@@') ? 'var(--accent)' : undefined
    return (
      <div key={i} style={{ color: tone, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
        {l || ' '}
      </div>
    )
  }
  return (
    <pre
      className="rk-mono"
      style={{ margin: 0, padding: '8px 10px', fontSize: 11.5, lineHeight: 1.45, overflow: 'auto', background: 'var(--surface)', borderRadius: 6, maxHeight: 360, minWidth: 0, whiteSpace: 'pre-wrap' }}
      data-testid="knowledge-diff"
    >
      {fm ? (
        <>
          {lines.slice(0, fm.start).map(line)}
          <button type="button" className="rk-link" style={{ font: 'inherit', fontSize: 11.5, padding: 0 }} aria-expanded={meta} onClick={() => setMeta(!meta)} data-testid="knowledge-diff-meta">
            {meta ? 'Hide the front matter' : `Front matter: ${fm.end - fm.start + 1} lines (sources, dates)`}
          </button>
          {meta && lines.slice(fm.start, fm.end + 1).map((l, i) => line(l, fm.start + i))}
          {lines.slice(fm.end + 1).map((l, i) => line(l, fm.end + 1 + i))}
        </>
      ) : (
        lines.map(line)
      )}
    </pre>
  )
}

/** Where a diff's front matter is: the lines from its first "---" to the next, both added or kept, after the headers. */
export function frontMatter(lines: string[]): { start: number; end: number } | null {
  const body = (l: string) => (l.startsWith('+++') || l.startsWith('---') || l.startsWith('@@') ? null : l.slice(1))
  let start = -1
  for (let i = 0; i < lines.length; i++) {
    const b = body(lines[i]!)
    if (b === null) continue
    if (start < 0) {
      if (b.trim() !== '---') return null
      start = i
    } else if (b.trim() === '---') {
      return i - start >= 3 ? { start, end: i } : null
    }
  }
  return null
}

const stateWords: Record<DraftPage['state'], [string, string]> = {
  waiting: ['Waiting', 'info'],
  published: ['Published', 'ok'],
  conflict: ['Changed meanwhile: waits for a person', 'warn'],
  rejected: ['Rejected', 'muted'],
  held: ['Retired page: held', 'muted'],
}

/** How many approvals a page has of those it needs. */
export function approvalsText(p: DraftPage): string {
  const need = p.critical ? 2 : 1
  return `${p.approvals?.length ?? 0} of ${need}${p.critical ? ' · critical: two people' : ''}`
}

/** One run of Mycroft's wiki changes: every page's diff, approved or rejected as one. */
export function DraftRunCard({ run, onChanged, onToast, open: startOpen }: { run: DraftRun; onChanged: () => void; onToast?: (msg: string) => void; open?: boolean }) {
  const [open, setOpen] = useState(!!startOpen)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [rejecting, setRejecting] = useState(false)
  const [comment, setComment] = useState('')
  const critical = run.pages.filter((p) => p.critical).length
  // Pages that say otherwise than an active fix, flagged for the reviewer
  // even while the card is closed (KN-33).
  const contradicting = run.pages.filter((p) => (p.contradicts ?? []).length > 0).length
  const waiting = run.state === 'waiting' || run.state === 'partial'
  const act = async (fn: () => Promise<unknown>, msg: string) => {
    setBusy(true)
    setError('')
    try {
      await fn()
      onToast?.(msg)
      onChanged()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="rk-card" style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 8, minWidth: 0 }} aria-label={`Wiki changes ${run.id}`} data-testid="knowledge-run" data-run={run.id}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <span className="rk-pill" data-tone="info">
          Wiki changes
        </span>
        <button className="rk-link" style={{ fontWeight: 600 }} onClick={() => setOpen(!open)} aria-expanded={open} data-testid="knowledge-run-open">
          {run.pages.length} {run.pages.length === 1 ? 'page' : 'pages'} from Mycroft{critical ? `, ${critical} critical` : ''}
        </button>
        {contradicting > 0 && (
          <span className="rk-pill" data-tone="warn" title="Mycroft wrote what a source says where it differs from a fix of past cases: decide which is right" data-testid="knowledge-run-contradicts">
            Contradicts a fix{contradicting > 1 ? ` (${contradicting} pages)` : ''}
          </span>
        )}
        <span className="rk-fhint">
          {run.by} · {fmtTs(run.started)} · run {run.id}
        </span>
        <span style={{ flex: '1 1 auto' }} />
        <span className="rk-pill" data-tone={waiting ? 'warn' : run.state === 'rejected' ? 'muted' : 'ok'}>
          {run.state === 'waiting' ? 'Waiting' : run.state === 'partial' ? 'Waiting for a second person' : run.state === 'rejected' ? 'Rejected' : 'Decided'}
        </span>
      </div>
      {run.reported?.map((r) => (
        <p key={r} className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="knowledge-run-reported">
          {r}
        </p>
      ))}
      {open && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, minWidth: 0 }}>
          {run.pages.map((p) => (
            <div key={p.path} style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0, paddingTop: 10, borderTop: '1px solid var(--line)' }} data-testid="knowledge-run-page">
              <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', fontSize: 12.5 }}>
                <strong>{p.title}</strong>
                <span className="rk-mono rk-fhint">{p.path}</span>
                {p.new && (
                  <span className="rk-pill" data-tone="info">
                    New
                  </span>
                )}
                {p.critical && (
                  <span className="rk-pill" data-tone="warn">
                    Critical
                  </span>
                )}
                <span className="rk-fhint">
                  +{p.added} −{p.removed} · {approvalsText(p)}
                </span>
                <span className="rk-pill" data-tone={stateWords[p.state][1]}>
                  {stateWords[p.state][0]}
                </span>
              </div>
              {p.sources && p.sources.length > 0 && (
                <span className="rk-fhint" style={{ overflowWrap: 'anywhere' }}>
                  Sources:{' '}
                  {p.sources.map((s, i) => (
                    <span key={s.key}>
                      {i > 0 ? '; ' : ''}
                      <SourceLink title={s.title} url={s.url} />
                      {s.version && s.version.length <= 12 ? `, version ${s.version}` : ''}
                      {s.date ? `, ${s.date.slice(0, 10)}` : ''}
                    </span>
                  ))}
                </span>
              )}
              {p.reason && <span className="rk-fhint">{p.reason}</span>}
              {p.contradicts?.map((c) => (
                <p key={c.fix} className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="knowledge-page-contradicts" title={contradictionText(c)}>
                  Contradicts{' '}
                  <a className="rk-link" href={fixHref(c.fix)}>
                    {c.fix} “{c.title}”
                  </a>
                  , used in {c.cases} {c.cases === 1 ? 'case' : 'cases'}
                  {c.why ? `: ${c.why}` : ''}. Decide which is right before approving.
                </p>
              ))}
              {p.diff ? <Diff text={p.diff} /> : <span className="rk-fhint">The diff is no longer kept.</span>}
              {p.clipped && <span className="rk-fhint">The diff is cut short here.</span>}
            </div>
          ))}
        </div>
      )}
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
          {error}
        </p>
      )}
      {waiting && (
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
          <button
            className="rk-btn"
            data-primary="true"
            disabled={busy || run.canApprove === false}
            onClick={() => void act(() => approveDraft(run.id), 'Approved.')}
            data-testid="knowledge-run-approve"
            title={run.canApprove === false ? 'You approved this already: a critical page needs a second person' : undefined}
          >
            Approve
          </button>
          {!rejecting ? (
            <button className="rk-btn" disabled={busy} onClick={() => setRejecting(true)} data-testid="knowledge-run-reject">
              Reject…
            </button>
          ) : (
            <>
              <span className="rk-field" style={{ width: 280 }}>
                <input value={comment} maxLength={500} placeholder="Why (optional)" onChange={(e) => setComment(e.target.value)} aria-label="Why" data-testid="knowledge-run-reject-why" />
              </span>
              <button className="rk-btn" disabled={busy} onClick={() => void act(() => rejectDraft(run.id, comment), 'Rejected.')} data-testid="knowledge-run-reject-confirm">
                Reject
              </button>
            </>
          )}
          <span className="rk-fhint">Approved pages are published to the wiki; agents read only approved pages.</span>
        </div>
      )}
    </section>
  )
}

/** A person's edit inside a Confluence page's managed part, waiting for a decision. */
export function ConflictCard({ c, onChanged, onToast }: { c: ConfluenceConflict; onChanged: () => void; onToast?: (msg: string) => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const act = async (action: 'keep' | 'replace') => {
    setBusy(true)
    setError('')
    try {
      await resolveConflict(c.id, action)
      onToast?.(action === 'keep' ? 'Kept in the record.' : 'Replaced by the record.')
      onChanged()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="rk-card" style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 8 }} aria-label={`Confluence edit ${c.title}`} data-testid="knowledge-conflict">
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <span className="rk-pill" data-tone="warn">
          Confluence edit
        </span>
        <strong style={{ fontSize: 12.5 }}>{c.title}</strong>
        <span className="rk-fhint">
          {fmtTs(c.at)}
          {c.ownerName ? ` · owner ${c.ownerName}` : ''}
        </span>
        {c.url && (
          <a className="rk-link" href={c.url} target="_blank" rel="noreferrer">
            Open the page
          </a>
        )}
      </div>
      <span className="rk-fhint">Someone edited the part of this page the console keeps. It was not overwritten: keep the edit (it is copied into the known issue’s update log) or replace it with the record.</span>
      <pre className="rk-mono" style={{ margin: 0, padding: '8px 10px', fontSize: 11.5, whiteSpace: 'pre-wrap', background: 'var(--surface)', borderRadius: 6, maxHeight: 220, overflow: 'auto' }}>
        {c.their}
      </pre>
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
          {error}
        </p>
      )}
      <div style={{ display: 'flex', gap: 8 }}>
        {c.kind === 'known' && (
          <button className="rk-btn" disabled={busy} onClick={() => void act('keep')} data-testid="knowledge-conflict-keep">
            Keep their edit
          </button>
        )}
        <button className="rk-btn" disabled={busy} onClick={() => void act('replace')} data-testid="knowledge-conflict-replace">
          Replace with the record
        </button>
      </div>
    </section>
  )
}
