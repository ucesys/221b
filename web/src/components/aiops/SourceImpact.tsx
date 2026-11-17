// What removing or excluding a source item meant for the wiki (0.12.1):
// the pages Mycroft wrote that drew on it, which are not changed by it,
// with Review those pages (the Wiki's page list showing only them) and
// Ask Mycroft to rewrite without it (one This is outdated… correction per
// page; each redraft waits for review under Drafts).
import { useState } from 'react'
import { askRewrite, pageHref, wikiPagesHref, type PageBrief } from '../../api/knowledge'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** A page's title, or its path without pages/ when no title is known. */
export const pageLabel = (p: PageBrief) => p.title || p.path.replace(/^pages\//, '')

/** The pages that drew on an item, as links. */
export function PageLinks({ pages, testId }: { pages: PageBrief[]; testId?: string }) {
  return (
    <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5 }} data-testid={testId}>
      {pages.map((p) => (
        <li key={p.path}>
          <a className="rk-link" href={pageHref(p.path)}>
            {pageLabel(p)}
          </a>
        </li>
      ))}
    </ul>
  )
}

/**
 * After a removal or an exclusion: what was done, and, when wiki pages drew
 * on the item, Review those pages and Ask Mycroft to rewrite without it.
 */
export function SourceImpact({ text, title, pages, why, mayRewrite, onToast }: { text: string; title: string; pages: PageBrief[]; why: 'removed' | 'excluded'; mayRewrite: boolean; onToast?: (msg: string) => void }) {
  const [busy, setBusy] = useState(false)
  const [sent, setSent] = useState('')
  const [error, setError] = useState('')
  const rewrite = async () => {
    setBusy(true)
    setError('')
    try {
      const r = await askRewrite(
        pages.map((p) => p.path),
        title,
        why,
      )
      setSent(r.text)
      onToast?.(r.text)
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="rk-callout" data-tone="info" role="status" style={{ flexDirection: 'column', gap: 6, fontSize: 12.5 }} data-testid="source-impact">
      <span>{text}</span>
      {pages.length === 0 ? (
        <span className="rk-fhint" data-testid="source-impact-none">
          No wiki page drew on it.
        </span>
      ) : (
        <>
          <span>{pages.length === 1 ? 'One wiki page drew on it' : `${pages.length} wiki pages drew on it`}; they are not changed by this, and grow stale until they are rewritten:</span>
          <PageLinks pages={pages} testId="source-impact-pages" />
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
            <a className="rk-btn" data-size="sm" href={wikiPagesHref(pages.map((p) => p.path))} data-testid="source-impact-review">
              Review those pages
            </a>
            {mayRewrite && !sent && (
              <button className="rk-btn" data-size="sm" disabled={busy} onClick={() => void rewrite()} data-testid="source-impact-rewrite">
                Ask Mycroft to rewrite without it
              </button>
            )}
            {sent && <span data-testid="source-impact-sent">{sent}</span>}
          </div>
          {error && (
            <span className="tb-err" role="alert" data-testid="source-impact-error">
              {error}
            </span>
          )}
        </>
      )}
    </div>
  )
}
