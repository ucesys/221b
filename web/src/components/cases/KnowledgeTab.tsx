// Cases › Mycroft › Wiki (Phase 12, KN-09, KN-10, KN-12, MY-02 to MY-08):
// Mycroft's wiki. Pages: browse the approved pages, each with its sources
// and their dates and whether it is stale, and search them and the
// sources' excerpts. Drafts: the runs of changes waiting for review, with
// every page's diff. Sources: what Mycroft reads, and what people added.
// Add: a note, a web address to read, or documents. Each view has its
// address: #/cases/mycroft/wiki[/drafts|/sources|/add], a page
// #/cases/mycroft/wiki/pages/<kind>/<name>.md.
//
// The Wiki first asks the server what it is (GET /api/v1/knowledge/wiki):
// whether a wiki is kept at all, and what the person may do. With no store
// it says so and offers the setup, rather than waiting (MY-02).
//
// Its views switch in the toolbar row every Mycroft sub-tab has (CF-55),
// below the intro, and its sections stay in the Mycroft column.
import { useCallback, useEffect, useMemo, useState, type JSX } from 'react'
import { CallError } from '../../api/call'
import { fetchDrafts, fetchPage, fetchPages, fetchWiki, pageHref, searchKnowledge, wikiHref, type DraftRun, type SearchHit, type WikiPage, type WikiState } from '../../api/knowledge'
import { fmtTs } from '../../lib/format'
import { Markdown } from '../../lib/markdown'
import { adminHref, navigate, type Route } from '../../lib/router'
import { DraftRunCard } from './KnowledgeDraft'
import { MycroftPage } from './MycroftPage'
import { WikiAdd } from './WikiAdd'
import { WikiSources } from './WikiSources'

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

/** The Wiki's views. */
export type WikiView = 'pages' | 'drafts' | 'sources' | 'add'
const VIEWS: WikiView[] = ['pages', 'drafts', 'sources', 'add']

/** What a route opens: a view, or a page (#/cases/mycroft/wiki/pages/<kind>/<name>.md). */
export function wikiRoute(route: Route): { view: WikiView; page: string } {
  let segs = route.path[0] === 'mycroft' ? route.path.slice(1) : route.path
  if (segs[0] === 'wiki' || segs[0] === 'knowledge') segs = segs.slice(1)
  if (VIEWS.includes(segs[0] as WikiView) && segs[0] !== 'pages') return { view: segs[0] as WikiView, page: '' }
  return { view: 'pages', page: segs.join('/') || route.params.get('page') || '' }
}

/** The Wiki of Cases › Mycroft. onPages hears how many approved pages there are. */
export function KnowledgeTab({ route, onToast, onPages }: { route: Route; onToast?: (msg: string) => void; onPages?: (n: number) => void }): JSX.Element {
  const { view, page: open } = wikiRoute(route)
  const [wiki, setWiki] = useState<WikiState | null>(null)
  const [wikiError, setWikiError] = useState('')
  const [pages, setPages] = useState<WikiPage[] | null>(null)
  const [pagesError, setPagesError] = useState('')
  const [q, setQ] = useState('')
  const [hits, setHits] = useState<SearchHit[] | null>(null)
  const [searchError, setSearchError] = useState('')
  const [runs, setRuns] = useState<DraftRun[] | null>(null)
  const [runsError, setRunsError] = useState('')
  const [tick, setTick] = useState(0)
  const refresh = useCallback(() => setTick((t) => t + 1), [])
  // After an addition, the state is read again every few seconds while
  // what was added waits to be read, for two minutes at most.
  const [pollUntil, setPollUntil] = useState(0)
  const added = useCallback(() => {
    setPollUntil(Date.now() + 120_000)
    refresh()
  }, [refresh])
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
  useEffect(() => {
    const term = q.trim()
    if (term.length < 2) return
    const t = setTimeout(() => {
      searchKnowledge(term).then(
        (r) => {
          setHits(r.hits ?? [])
          setSearchError('')
        },
        (e: unknown) => setSearchError(errorText(e)),
      )
    }, 250)
    return () => clearTimeout(t)
  }, [q])
  useEffect(() => {
    if (!wiki || Date.now() > pollUntil || !(wiki.running || wiki.added.some((a) => !a.lastRead))) return
    const t = setTimeout(refresh, 3000)
    return () => clearTimeout(t)
  }, [wiki, pollUntil, refresh])
  const groups = useMemo(() => {
    const m = new Map<string, WikiPage[]>()
    for (const p of pages ?? []) m.set(p.kind, [...(m.get(p.kind) ?? []), p])
    return [...m.entries()].sort(([a], [b]) => Object.keys(KIND_LABEL).indexOf(a) - Object.keys(KIND_LABEL).indexOf(b))
  }, [pages])
  const waiting = (runs ?? []).filter((r) => r.state === 'waiting' || r.state === 'partial')
  const shown = q.trim().length >= 2 ? hits : null
  const noStore = wiki !== null && !wiki.store
  const show = (v: WikiView) => {
    if (v !== view || open) navigate(wikiHref(v))
  }
  return (
    <MycroftPage
      testId="knowledge-tab"
      lead="Mycroft’s wiki: how things work and why, runbooks and best practices, each claim naming its source. Agents read only approved pages, citing them with their dates."
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
          <button aria-pressed={view === 'add'} onClick={() => show('add')} title="Add a note, a web address to read, or documents" data-testid="knowledge-view-add">
            Add
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
      {view === 'add' ? (
        <WikiAdd wiki={wiki} onAdded={added} onToast={onToast} />
      ) : view === 'sources' ? (
        <WikiSources wiki={wiki} onChanged={refresh} onToast={onToast} />
      ) : view === 'drafts' ? (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, minWidth: 0 }} data-testid="knowledge-drafts">
          {runs === null && <p className="rk-fhint">Loading…</p>}
          {runsError && <p className="rk-fhint" style={{ margin: 0 }}>{/allowed|only an Admin/i.test(runsError) ? 'Admins and SuperAdmins review the wiki’s changes.' : runsError}</p>}
          {runs && runs.length === 0 && !runsError && <p className="rk-fhint" style={{ margin: 0 }}>No changes from Mycroft yet.</p>}
          {(runs ?? []).map((r) => (
            <DraftRunCard key={r.id} run={r} open={r.state === 'waiting' || r.state === 'partial'} onToast={onToast} onChanged={refresh} />
          ))}
        </div>
      ) : (
        <div style={{ display: 'flex', gap: 16, alignItems: 'flex-start', flexWrap: 'wrap' }}>
          <nav className="rk-card" style={{ flex: '0 1 280px', minWidth: 220, padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Pages" data-testid="knowledge-pages">
            <label className="rk-field">
              <span className="rk-sr">Search the knowledge</span>
              <input type="search" placeholder="Search pages and sources" value={q} onChange={(e) => setQ(e.target.value)} data-testid="knowledge-search" />
            </label>
            {searchError && shown && <span className="tb-err">{searchError}</span>}
            {shown ? (
              <ul style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="knowledge-hits">
                {shown.length === 0 && <li className="rk-fhint">Nothing matches.</li>}
                {shown.map((h, i) => (
                  <li key={i} style={{ fontSize: 12.5 }}>
                    {h.kind === 'page' && h.path ? (
                      <a className="rk-link" href={pageHref(h.path)}>
                        {h.title}
                      </a>
                    ) : (
                      <span>
                        <span className="rk-pill" data-tone="muted">
                          Source
                        </span>{' '}
                        {h.url ? (
                          <a className="rk-link" href={h.url} target="_blank" rel="noreferrer">
                            {h.title}
                          </a>
                        ) : (
                          h.title
                        )}
                      </span>
                    )}
                    {h.stale && (
                      <span className="rk-pill" data-tone="warn" style={{ marginLeft: 4 }}>
                        Stale
                      </span>
                    )}
                    <div className="rk-fhint">{h.text.slice(0, 160)}</div>
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
                {noStore ? 'No pages: no wiki is kept yet.' : 'No approved pages yet: add knowledge under Add, or connect sources, and approve Mycroft’s first changes under Drafts.'}
              </span>
            ) : (
              groups.map(([kind, list]) => (
                <div key={kind}>
                  <div style={{ fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)', textTransform: 'uppercase', margin: '6px 0 2px' }}>{KIND_LABEL[kind] ?? kind}</div>
                  <ul style={{ listStyle: 'none', margin: 0, padding: 0 }}>
                    {list.map((p) => (
                      <li key={p.path} style={{ fontSize: 12.5, padding: '2px 0' }}>
                        <a className="rk-link" href={pageHref(p.path)} aria-current={p.path === open ? 'page' : undefined} style={{ fontWeight: p.path === open ? 600 : undefined }} data-testid="knowledge-page-link">
                          {p.title}
                        </a>
                        {p.stale && (
                          <span className="rk-pill" data-tone="warn" style={{ marginLeft: 4 }}>
                            Stale
                          </span>
                        )}
                      </li>
                    ))}
                  </ul>
                </div>
              ))
            )}
          </nav>
          {open ? <PageView key={open} path={open} /> : <p className="rk-fhint" style={{ flex: '1 1 400px', margin: 0 }}>Pick a page, or search the pages and the sources.</p>}
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
      <span>Mycroft keeps the wiki’s pages, the excerpts of its sources and its drafts in a git repository or an S3 bucket, chosen in Administration › Settings › Knowledge. Until one is chosen there is nothing to browse or add, and the agents answer without the wiki.</span>
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

/** One approved page: its sources with their dates, freshness, and its text. */
function PageView({ path }: { path: string }) {
  const [page, setPage] = useState<WikiPage | null>(null)
  const [error, setError] = useState('')
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
  }, [path])
  if (error) return <p className="rk-t" data-tone="err" style={{ flex: '1 1 400px', margin: 0 }}>{error}</p>
  if (!page) return <p className="rk-fhint" style={{ flex: '1 1 400px', margin: 0 }}>Loading…</p>
  return (
    <article className="rk-card" style={{ flex: '1 1 480px', minWidth: 0, padding: '14px 18px', display: 'flex', flexDirection: 'column', gap: 10 }} data-testid="knowledge-page">
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <h2 className="rk-h" style={{ fontSize: 16, margin: 0 }}>{page.title}</h2>
        <span className="rk-pill" data-tone="muted">{KIND_LABEL[page.kind] ?? page.kind}</span>
        {page.critical && <span className="rk-pill" data-tone="info" title="Changes need two people">Critical</span>}
        {page.stale && <span className="rk-pill" data-tone="warn" data-testid="knowledge-page-stale">Stale: its sources were not seen in a while</span>}
      </div>
      <span className="rk-fhint rk-mono">{page.path}{page.updated ? ` · updated ${fmtTs(page.updated)}` : ''}</span>
      {page.sources && page.sources.length > 0 && (
        <div style={{ fontSize: 12.5 }} data-testid="knowledge-page-sources">
          <strong>Sources</strong>
          <ul style={{ margin: '4px 0 0', paddingLeft: 18 }}>
            {page.sources.map((s) => (
              <li key={s.key}>
                {s.url ? (
                  <a className="rk-link" href={s.url} target="_blank" rel="noreferrer">
                    {s.title}
                  </a>
                ) : (
                  s.title
                )}
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
