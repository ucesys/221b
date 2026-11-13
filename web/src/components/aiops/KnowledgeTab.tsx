// AIOps › Mycroft › Wiki (Phase 12, KN-09, KN-10, KN-12, MY-02 to MY-08):
// Mycroft's wiki. Pages: browse the approved pages, each with its sources
// and their dates and whether it is stale, and search them and the
// sources' excerpts. Drafts: the runs of changes waiting for review, with
// every page's diff. Sources: what Mycroft reads, and what people added.
// Each view has its address: #/aiops/mycroft/wiki[/drafts|/sources], a
// page #/aiops/mycroft/wiki/pages/<kind>/<name>.md. Since MY-11 a note, a
// web address or documents are added in Settings › Knowledge's Added in
// Mycroft card only: the Add view's old address opens it there.
//
// A page's view has its own actions (KN-23, KN-24, WikiPageActions.tsx):
// This is outdated…, Retire page… and Make active again…; retired pages
// are listed apart, under the page list's Retired filter.
//
// The Wiki first asks the server what it is (GET /api/v1/knowledge/wiki):
// whether a wiki is kept at all, and what the person may do. With no store
// it says so and offers the setup, rather than waiting (MY-02).
//
// Its views switch in the toolbar row every Mycroft sub-tab has (CF-55),
// below the intro, and its sections stay in the Mycroft column.
//
// A wiki of hundreds of pages (Phase 12.1, KN-36): the page list filters
// as it is typed, by title or path, sorts by title or by last update,
// shows the stale pages alone, and groups the pages by kind in groups
// that fold, each showing its first PAGE_ROWS pages until asked for all.
// The full-text search of the pages, the sources and the records is the
// one at the top of Mycroft (MycroftSearch.tsx, KN-35). A wiki kept in
// the ConfigMap store says when that store is nearly full (KN-37).
import { useCallback, useEffect, useMemo, useState, type JSX } from 'react'
import { CallError } from '../../api/call'
import { fetchDrafts, fetchPage, fetchPages, fetchWiki, pageHref, wikiHref, type DraftRun, type PageView as PageData, type WikiPage, type WikiState } from '../../api/knowledge'
import { fmtTs } from '../../lib/format'
import { Markdown } from '../../lib/markdown'
import { adminHref, navigate, type Route } from '../../lib/router'
import { DraftRunCard } from './KnowledgeDraft'
import { MycroftPage } from './MycroftPage'
import { SourceLink } from './SourceLink'
import { STORE_NEAR, StoreRoom } from './WikiStoreRoom'
import { PageActions, PageNotices } from './WikiPageActions'
import { ADD_HREF, WikiSources } from './WikiSources'

export { pageHref } from '../../api/knowledge'

const errorText = (e: unknown) => (e instanceof CallError && e.status === 403 ? 'Your role does not include the wiki: it is for those who hold a role in the console.' : e instanceof Error ? e.message : String(e))

const KIND_LABEL: Record<string, string> = {
  system: 'Systems',
  application: 'Applications',
  runbook: 'Runbooks',
  incident: 'Incidents',
  practice: 'Best practices',
  decision: 'Decisions',
  // Pages written before exceptions (MY-09) and RCAs (wave 6e: an RCA is part of its case) stopped being pages of the wiki.
  rca: 'RCAs (old pages)',
  exception: 'Exceptions (old pages)',
}

/** How many pages a kind's group shows until asked for all of them. */
export const PAGE_ROWS = 50

/** Up to this many pages, every group starts unfolded. */
export const UNFOLDED_PAGES = 60

export type PageSort = 'title' | 'updated'

/** The pages a filter keeps (every word in the title or the path), stale ones alone if asked, sorted. */
export function pagesShown(pages: WikiPage[], filter: string, staleOnly: boolean, sort: PageSort): WikiPage[] {
  const words = filter.toLowerCase().split(/\s+/).filter(Boolean)
  const kept = pages.filter((p) => (!staleOnly || p.stale) && words.every((w) => p.title.toLowerCase().includes(w) || p.path.toLowerCase().includes(w)))
  return kept.sort((a, b) => (sort === 'updated' ? (b.updated ?? '').localeCompare(a.updated ?? '') : 0) || a.title.localeCompare(b.title))
}

/** The Wiki's views. */
export type WikiView = 'pages' | 'drafts' | 'sources'
const VIEWS: WikiView[] = ['pages', 'drafts', 'sources']

/** What a route opens: a view, or a page (#/aiops/mycroft/wiki/pages/<kind>/<name>.md). */
export function wikiRoute(route: Route): { view: WikiView; page: string } {
  let segs = route.path[0] === 'mycroft' ? route.path.slice(1) : route.path
  if (segs[0] === 'wiki' || segs[0] === 'knowledge') segs = segs.slice(1)
  if (VIEWS.includes(segs[0] as WikiView) && segs[0] !== 'pages') return { view: segs[0] as WikiView, page: '' }
  return { view: 'pages', page: segs.join('/') || route.params.get('page') || '' }
}

/** The Wiki of AIOps › Mycroft. onPages hears how many approved pages there are. */
export function KnowledgeTab({ route, onToast, onPages }: { route: Route; onToast?: (msg: string) => void; onPages?: (n: number) => void }): JSX.Element {
  const { view, page: open } = wikiRoute(route)
  const [wiki, setWiki] = useState<WikiState | null>(null)
  const [wikiError, setWikiError] = useState('')
  const [pages, setPages] = useState<WikiPage[] | null>(null)
  const [retired, setRetired] = useState<WikiPage[]>([])
  // The page list shows the active pages, or the retired ones (KN-24).
  const [showRetired, setShowRetired] = useState(false)
  const [pagesError, setPagesError] = useState('')
  // The page list's filter, sort and folds (KN-36).
  const [filter, setFilter] = useState('')
  const [sort, setSort] = useState<PageSort>('title')
  const [staleOnly, setStaleOnly] = useState(false)
  const [folded, setFolded] = useState<Record<string, boolean>>({})
  const [whole, setWhole] = useState<Record<string, boolean>>({})
  const [runs, setRuns] = useState<DraftRun[] | null>(null)
  const [runsError, setRunsError] = useState('')
  const [tick, setTick] = useState(0)
  const refresh = useCallback(() => setTick((t) => t + 1), [])
  useEffect(() => {
    fetchWiki().then(
      (w) => {
        setWiki(w)
        setWikiError('')
      },
      (e: unknown) => setWikiError(errorText(e)),
    )
  }, [tick])
  useEffect(() => {
    fetchPages().then(
      (r) => {
        // An answer always ends the loading, whatever it holds.
        const list = r.pages ?? []
        setPages(list)
        setRetired(r.retired ?? [])
        setPagesError('')
        onPages?.(list.length)
      },
      (e: unknown) => {
        setPages([])
        setPagesError(errorText(e))
      },
    )
  }, [tick, onPages])
  useEffect(() => {
    fetchDrafts(true).then(
      (r) => {
        setRuns(r.runs ?? [])
        setRunsError('')
      },
      (e: unknown) => {
        setRuns([])
        setRunsError(errorText(e))
      },
    )
  }, [tick])
  const kept = useMemo(() => pagesShown(pages ?? [], filter, staleOnly, sort), [pages, filter, staleOnly, sort])
  const groups = useMemo(() => {
    const m = new Map<string, WikiPage[]>()
    for (const p of kept) m.set(p.kind, [...(m.get(p.kind) ?? []), p])
    const order = (k: string) => (k in KIND_LABEL ? Object.keys(KIND_LABEL).indexOf(k) : Object.keys(KIND_LABEL).length)
    return [...m.entries()].sort(([a], [b]) => order(a) - order(b) || a.localeCompare(b))
  }, [kept])
  const stale = useMemo(() => (pages ?? []).filter((p) => p.stale).length, [pages])
  const waiting = (runs ?? []).filter((r) => r.state === 'waiting' || r.state === 'partial')
  const listRetired = showRetired && retired.length > 0
  const filtering = filter.trim() !== '' || staleOnly
  // A group is unfolded while filtering, when it holds the page open, or
  // when the wiki is small; folding it by hand wins.
  const isFolded = (kind: string, list: WikiPage[]) => folded[kind] ?? !(filtering || (pages?.length ?? 0) <= UNFOLDED_PAGES || list.some((p) => p.path === open))
  const noStore = wiki !== null && !wiki.store
  const show = (v: WikiView) => {
    if (v !== view || open) navigate(wikiHref(v))
  }
  return (
    <MycroftPage
      testId="knowledge-tab"
      lead="Mycroft’s notes on how your system works: how-tos, runbooks and good practices, written from your documents or public sources. Agents use only approved pages."
      filter={
        <div className="rk-seg" role="group" aria-label="Wiki">
          <button aria-pressed={view === 'pages'} onClick={() => show('pages')} data-testid="knowledge-view-pages">
            Pages <span className="rk-cnt">{pages?.length ?? 0}</span>
          </button>
          <button aria-pressed={view === 'drafts'} onClick={() => show('drafts')} title="Mycroft’s changes waiting for review" data-testid="knowledge-view-drafts">
            Drafts <span className="rk-cnt">{waiting.length}</span>
          </button>
          <button aria-pressed={view === 'sources'} onClick={() => show('sources')} title="What Mycroft reads, and what people added" data-testid="knowledge-view-sources">
            Sources
          </button>
        </div>
      }
    >
      {wikiError && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="wiki-error">
          {wikiError}
        </p>
      )}
      {noStore && <NoStore maySetup={wiki.maySetup} />}
      {wiki?.usage && wiki.usage.used >= wiki.usage.limit * STORE_NEAR && <StoreRoom usage={wiki.usage} maySetup={wiki.maySetup} />}
      {view === 'sources' ? (
        <WikiSources wiki={wiki} onChanged={refresh} onToast={onToast} />
      ) : view === 'drafts' ? (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, minWidth: 0 }} data-testid="knowledge-drafts">
          {runs === null && <p className="rk-fhint">Loading…</p>}
          {runsError && (
            <p className="rk-fhint" style={{ margin: 0 }}>
              {/allowed|only an Admin/i.test(runsError) ? 'Admins and SuperAdmins review the wiki’s changes.' : runsError}
            </p>
          )}
          {runs && runs.length === 0 && !runsError && (
            <p className="rk-fhint" style={{ margin: 0 }}>
              No changes from Mycroft yet.
            </p>
          )}
          {(runs ?? []).map((r) => (
            <DraftRunCard key={r.id} run={r} open={r.state === 'waiting' || r.state === 'partial'} onToast={onToast} onChanged={refresh} />
          ))}
        </div>
      ) : (
        <div style={{ display: 'flex', gap: 16, alignItems: 'flex-start', flexWrap: 'wrap' }}>
          <nav className="rk-card" style={{ flex: '0 1 280px', minWidth: 220, maxHeight: '75vh', overflowY: 'auto', padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Pages" data-testid="knowledge-pages">
            <label className="rk-field">
              <span className="rk-sr">Filter the pages</span>
              <input type="search" placeholder="Filter pages by title or path" value={filter} onChange={(e) => setFilter(e.target.value)} onKeyDown={(e) => e.key === 'Escape' && setFilter('')} data-testid="knowledge-filter" />
            </label>
            {!listRetired && (pages?.length ?? 0) > 1 && (
              <div style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
                <label className="rk-fhint" style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
                  Sort
                  <select value={sort} onChange={(e) => setSort(e.target.value as PageSort)} data-testid="knowledge-sort">
                    <option value="title">by title</option>
                    <option value="updated">last updated first</option>
                  </select>
                </label>
                {stale > 0 && (
                  <button
                    className="rk-btn"
                    aria-pressed={staleOnly}
                    data-on={staleOnly || undefined}
                    onClick={() => setStaleOnly(!staleOnly)}
                    title="Only the pages none of whose sources was seen in a while"
                    data-testid="knowledge-stale-only"
                  >
                    Stale <span className="rk-cnt">{stale}</span>
                  </button>
                )}
              </div>
            )}
            {retired.length > 0 && (
              <div className="rk-seg" role="group" aria-label="Which pages" data-testid="knowledge-pages-filter">
                <button aria-pressed={!listRetired} onClick={() => setShowRetired(false)} data-testid="knowledge-pages-active">
                  Active <span className="rk-cnt">{pages?.length ?? 0}</span>
                </button>
                <button aria-pressed={listRetired} onClick={() => setShowRetired(true)} title="Pages taken out of what the agents read; never deleted" data-testid="knowledge-pages-retired">
                  Retired <span className="rk-cnt">{retired.length}</span>
                </button>
              </div>
            )}
            {listRetired ? (
              <ul style={{ listStyle: 'none', margin: 0, padding: 0 }} data-testid="knowledge-retired">
                {pagesShown(retired, filter, false, sort).map((p) => (
                  <li key={p.path} style={{ fontSize: 12.5, padding: '2px 0' }}>
                    <a className="rk-link" href={pageHref(p.path)} aria-current={p.path === open ? 'page' : undefined} style={{ fontWeight: p.path === open ? 600 : undefined }} data-testid="knowledge-retired-link">
                      {p.title}
                    </a>
                    <span className="rk-fhint">
                      {' '}
                      · {KIND_LABEL[p.kind] ?? p.kind}
                      {p.retired ? ` · retired ${fmtTs(p.retired.at)}` : ''}
                    </span>
                  </li>
                ))}
              </ul>
            ) : pages === null ? (
              <span className="rk-fhint">Loading…</span>
            ) : pagesError ? (
              <span className="tb-err" data-testid="knowledge-pages-error">
                {pagesError}
              </span>
            ) : pages.length === 0 ? (
              <span className="rk-fhint" data-testid="knowledge-empty">
                {noStore ? (
                  'No pages: no wiki is kept yet.'
                ) : wiki?.mayAdd ? (
                  <>
                    No approved pages yet: add knowledge or connect sources in{' '}
                    <a className="rk-link" href={ADD_HREF} data-testid="knowledge-empty-add">
                      Settings › Knowledge
                    </a>
                    , and approve Mycroft’s first changes under Drafts.
                  </>
                ) : (
                  'No approved pages yet: an Admin adds knowledge, or a SuperAdmin connects sources, in Settings › Knowledge.'
                )}
              </span>
            ) : kept.length === 0 ? (
              <span className="rk-fhint" data-testid="knowledge-filter-none">
                No page’s title or path has that. The search at the top of Mycroft searches the pages’ text and the sources too.
              </span>
            ) : (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }} data-testid="knowledge-groups">
                {filtering && (
                  <span className="rk-fhint" data-testid="knowledge-filter-count">
                    {kept.length} of {pages.length} pages
                  </span>
                )}
                {groups.map(([kind, list]) => {
                  const shut = isFolded(kind, list)
                  const rows = whole[kind] ? list : list.slice(0, PAGE_ROWS)
                  return (
                    <div key={kind} data-testid="knowledge-group">
                      <button
                        className="rk-link"
                        aria-expanded={!shut}
                        onClick={() => setFolded((f) => ({ ...f, [kind]: !shut }))}
                        style={{ all: 'unset', cursor: 'pointer', display: 'flex', gap: 4, alignItems: 'center', fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)', textTransform: 'uppercase', margin: '6px 0 2px' }}
                        data-testid="knowledge-group-toggle"
                      >
                        <span aria-hidden="true">{shut ? '▸' : '▾'}</span>
                        {KIND_LABEL[kind] ?? kind} <span className="rk-cnt">{list.length}</span>
                      </button>
                      {!shut && (
                        <ul style={{ listStyle: 'none', margin: 0, padding: 0 }}>
                          {rows.map((p) => (
                            <li key={p.path} style={{ fontSize: 12.5, padding: '2px 0' }}>
                              <a className="rk-link" href={pageHref(p.path)} aria-current={p.path === open ? 'page' : undefined} style={{ fontWeight: p.path === open ? 600 : undefined }} data-testid="knowledge-page-link">
                                {p.title}
                              </a>
                              {p.stale && (
                                <span className="rk-pill" data-tone="warn" style={{ marginLeft: 4 }}>
                                  Stale
                                </span>
                              )}
                              {sort === 'updated' && p.updated && <span className="rk-fhint"> · {p.updated.slice(0, 10)}</span>}
                            </li>
                          ))}
                          {rows.length < list.length && (
                            <li>
                              <button className="rk-btn" onClick={() => setWhole((w) => ({ ...w, [kind]: true }))} data-testid="knowledge-group-more">
                                Show all {list.length}
                              </button>
                            </li>
                          )}
                        </ul>
                      )}
                    </div>
                  )
                })}
              </div>
            )}
          </nav>
          {open ? (
            <PageView key={open} path={open} wiki={wiki} onChanged={refresh} onToast={onToast} />
          ) : (
            <p className="rk-fhint" style={{ flex: '1 1 400px', margin: 0 }}>
              Pick a page, or filter the list. The search at the top of Mycroft searches the pages’ text, the sources, the known issues, the fixes and the exceptions.
            </p>
          )}
        </div>
      )}
    </MycroftPage>
  )
}

/** No wiki is kept: what that means, and the setup for a SuperAdmin (MY-02). */
function NoStore({ maySetup }: { maySetup: boolean }) {
  return (
    <div className="rk-callout" data-tone="warn" style={{ fontSize: 12.5, flexDirection: 'column', gap: 6 }} data-testid="wiki-no-store">
      <strong>No wiki is kept yet.</strong>
      <span>
        Mycroft keeps the wiki’s pages, the excerpts of its sources and its drafts in a git repository or an S3 bucket, chosen in Administration › Settings › Knowledge. Until one is chosen there is nothing to browse or add, and the agents
        answer without the wiki.
      </span>
      {maySetup ? (
        <span>
          <a className="rk-btn" data-on href={adminHref('settings', ['knowledge'])} data-testid="wiki-setup">
            Set up the wiki
          </a>
        </span>
      ) : (
        <span className="rk-fhint">A SuperAdmin sets it up there.</span>
      )}
    </div>
  )
}

/** One approved page: its sources with their dates, freshness, its text, and what waits for it; retired, why. */
function PageView({ path, wiki, onChanged, onToast }: { path: string; wiki: WikiState | null; onChanged: () => void; onToast?: (msg: string) => void }) {
  const [page, setPage] = useState<PageData | null>(null)
  const [error, setError] = useState('')
  const [tick, setTick] = useState(0)
  useEffect(() => {
    let stop = false
    fetchPage(path).then(
      (p) => {
        if (!stop) setPage(p)
      },
      (e: unknown) => {
        if (!stop) setError(errorText(e))
      },
    )
    return () => {
      stop = true
    }
  }, [path, tick])
  const changed = () => {
    setTick((t) => t + 1)
    onChanged()
  }
  if (error)
    return (
      <p className="rk-t" data-tone="err" style={{ flex: '1 1 400px', margin: 0 }}>
        {error}
      </p>
    )
  if (!page)
    return (
      <p className="rk-fhint" style={{ flex: '1 1 400px', margin: 0 }}>
        Loading…
      </p>
    )
  return (
    <article className="rk-card" style={{ flex: '1 1 480px', minWidth: 0, padding: '14px 18px', display: 'flex', flexDirection: 'column', gap: 10 }} data-testid="knowledge-page">
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <h2 className="rk-h" style={{ fontSize: 16, margin: 0 }}>
          {page.title}
        </h2>
        <span className="rk-pill" data-tone="muted">
          {KIND_LABEL[page.kind] ?? page.kind}
        </span>
        {page.critical && (
          <span className="rk-pill" data-tone="info" title="Changes need two people">
            Critical
          </span>
        )}
        {page.stale && (
          <span className="rk-pill" data-tone="warn" data-testid="knowledge-page-stale">
            Stale: its sources were not seen in a while
          </span>
        )}
        {page.retired && (
          <span className="rk-pill" data-tone="muted" data-testid="knowledge-page-retired">
            Retired
          </span>
        )}
      </div>
      <PageNotices page={page} />
      {wiki && <PageActions page={page} mayAdd={wiki.mayAdd && !!wiki.store} mayReview={wiki.mayReview} model={wiki.model} onChanged={changed} onToast={onToast} />}
      <span className="rk-fhint rk-mono">
        {page.path}
        {page.updated ? ` · updated ${fmtTs(page.updated)}` : ''}
      </span>
      {page.sources && page.sources.length > 0 && (
        <div style={{ fontSize: 12.5 }} data-testid="knowledge-page-sources">
          <strong>Sources</strong>
          <ul style={{ margin: '4px 0 0', paddingLeft: 18 }}>
            {page.sources.map((s) => (
              <li key={s.key}>
                <SourceLink title={s.title} url={s.url} />
                {s.version && s.version.length <= 12 ? `, version ${s.version}` : ''}
                {s.date ? `, ${s.date.slice(0, 10)}` : ''}
              </li>
            ))}
          </ul>
        </div>
      )}
      <div className="rk-ans-body" style={{ fontSize: 13 }}>
        <Markdown
          text={page.body ?? ''}
          link={(url, label) =>
            /^https?:\/\//.test(url) ? (
              <a className="rk-link" href={url} target="_blank" rel="noreferrer">
                {label}
              </a>
            ) : null
          }
          code={(text, _lang, _open, key) => (
            <pre key={key} className="rk-mono" style={{ margin: 0, padding: '8px 10px', fontSize: 12, background: 'var(--surface)', borderRadius: 6, overflowX: 'auto' }}>
              {text}
            </pre>
          )}
        />
      </div>
    </article>
  )
}
