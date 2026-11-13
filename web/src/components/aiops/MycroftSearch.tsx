// One search across AIOps › Mycroft (Phase 12.1, KN-35): a box beside the
// sub-tabs that searches the wiki's approved pages, the sources' excerpts,
// the known issues, the fixes and the accepted exceptions together, as the
// person may read them (GET /api/v1/aiops/mycroft/search). While it holds
// a query its results take the sub-tab's place: ranked together, each
// with its kind, title or id, date and a snippet with the words matched
// marked, and chips that narrow them to one kind. It is used from the
// keyboard: typing searches after a pause, ↓ and ↑ move through the
// results, Enter opens one, Escape clears the box. Opening a hit goes to
// its page, known issue, fix or exception (an excerpt opens its source),
// and clears the box so the sub-tab shows it.
import { useCallback, useEffect, useMemo, useRef, useState, type JSX, type KeyboardEvent, type ReactNode } from 'react'
import { CallError } from '../../api/call'
import { fixHref } from '../../api/fixes'
import { findInMycroft, pageHref, wikiHref, type FindHit, type FindKind, type FindResult } from '../../api/knowledge'
import { exceptionHref } from '../../api/lessons'
import { navigate } from '../../lib/router'
import { knownHref } from './FindingParts'

/** Each kind's name, on its chip and its hits. */
export const FIND_KIND: Record<FindKind, { label: string; one: string }> = {
  page: { label: 'Wiki pages', one: 'Page' },
  knownIssue: { label: 'Known issues', one: 'Known issue' },
  fix: { label: 'Fixes', one: 'Fix' },
  exception: { label: 'Exceptions', one: 'Exception' },
  excerpt: { label: 'Sources', one: 'Source' },
}

/** How long typing pauses before the search runs, in ms. */
export const FIND_DEBOUNCE = 250

/** Where a hit opens: its page, its record, or its source. */
export function findHref(h: FindHit): string {
  switch (h.kind) {
    case 'page':
      return h.path ? pageHref(h.path) : ''
    case 'knownIssue':
      return h.id ? knownHref(h.id) : ''
    case 'fix':
      return h.id ? fixHref(h.id) : ''
    case 'exception':
      return h.id ? exceptionHref(h.id) : ''
    default:
      // An excerpt opens its source; one with no address, the Wiki's sources.
      return h.url || wikiHref('sources')
  }
}

/** The search's state, shared by its box and its results. */
export interface MycroftFind {
  q: string
  setQ: (q: string) => void
  /** Whether the box holds a query long enough to search: the results then show. */
  active: boolean
  kind: FindKind | ''
  setKind: (k: FindKind | '') => void
  result: FindResult | null
  error: string
  loading: boolean
  /** The result the keyboard is on. */
  cursor: number
  setCursor: (i: number) => void
  open: (h: FindHit) => void
  onKey: (e: KeyboardEvent<HTMLInputElement>) => void
}

const errorText = (e: unknown) => (e instanceof CallError && e.status === 403 ? 'Your roles let you search nothing in Mycroft.' : e instanceof Error ? e.message : String(e))

/** The Mycroft search: its query, the kind chosen, the results and the keyboard. */
export function useMycroftFind(): MycroftFind {
  const [q, setQState] = useState('')
  const [kind, setKindState] = useState<FindKind | ''>('')
  const [result, setResult] = useState<FindResult | null>(null)
  const [error, setError] = useState('')
  // The query and kind the shown answer is for: it is loading until they are the box's.
  const [doneFor, setDoneFor] = useState('')
  const [cursor, setCursor] = useState(0)
  const seq = useRef(0)
  const term = q.trim()
  const active = term.length >= 2
  const key = kind + '\n' + term
  const loading = active && doneFor !== key
  useEffect(() => {
    if (!active) return
    const mine = ++seq.current
    const t = setTimeout(() => {
      findInMycroft(term, kind ? [kind] : []).then(
        (r) => {
          // Only the latest query's answer is shown.
          if (mine !== seq.current) return
          setResult(r)
          setError('')
          setDoneFor(key)
          setCursor(0)
        },
        (e: unknown) => {
          if (mine !== seq.current) return
          setError(errorText(e))
          setDoneFor(key)
        },
      )
    }, FIND_DEBOUNCE)
    return () => clearTimeout(t)
  }, [term, kind, active, key])
  const setQ = useCallback((v: string) => {
    setQState(v)
    if (v.trim().length < 2) {
      seq.current++
      setResult(null)
      setError('')
      setDoneFor('')
    }
  }, [])
  const setKind = useCallback((k: FindKind | '') => setKindState(k), [])
  const open = useCallback(
    (h: FindHit) => {
      const href = findHref(h)
      if (!href) return
      if (h.kind === 'excerpt' && h.url) {
        window.open(href, '_blank', 'noopener,noreferrer')
        return
      }
      setQ('')
      navigate(href)
    },
    [setQ],
  )
  const hits = result?.hits ?? []
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      setQ('')
    } else if (e.key === 'ArrowDown' && hits.length > 0) {
      e.preventDefault()
      setCursor(Math.min(cursor + 1, hits.length - 1))
    } else if (e.key === 'ArrowUp' && hits.length > 0) {
      e.preventDefault()
      setCursor(Math.max(cursor - 1, 0))
    } else if (e.key === 'Enter' && hits[cursor]) {
      e.preventDefault()
      open(hits[cursor])
    }
  }
  return { q, setQ, active, kind, setKind, result, error, loading, cursor, setCursor, open, onKey }
}

const optionId = (i: number) => `mycroft-find-${i}`

/** The search box, beside the sub-tabs. */
export function MycroftSearchBox({ find }: { find: MycroftFind }): JSX.Element {
  return (
    <label className="rk-field" style={{ flex: '0 1 380px', minWidth: 220 }}>
      <span className="rk-sr">Search Mycroft</span>
      <input
        type="search"
        role="combobox"
        aria-expanded={find.active}
        aria-controls="mycroft-find-results"
        aria-activedescendant={find.active && find.result && find.result.hits.length > 0 ? optionId(find.cursor) : undefined}
        aria-autocomplete="list"
        placeholder="Search the wiki, known issues, fixes and exceptions"
        title="Search the wiki’s pages, the sources, the known issues, the fixes and the exceptions together. ↓ ↑ move, Enter opens, Esc clears."
        value={find.q}
        onChange={(e) => find.setQ(e.target.value)}
        onKeyDown={find.onKey}
        data-testid="mycroft-search"
      />
    </label>
  )
}

/** The results, in the sub-tab's place while the box holds a query. */
export function MycroftSearchResults({ find }: { find: MycroftFind }): JSX.Element {
  const { result, kind } = find
  const hits = result?.hits ?? []
  const total = useMemo(() => Object.values(result?.counts ?? {}).reduce((a, b) => a + (b ?? 0), 0), [result])
  const listed = kind ? (result?.counts[kind] ?? 0) : total
  const active = useRef<HTMLLIElement | null>(null)
  useEffect(() => {
    active.current?.scrollIntoView?.({ block: 'nearest' })
  }, [find.cursor])
  return (
    <section className="cs-col" aria-label="Search results" data-testid="mycroft-search-results">
      <div className="cs-bar">
        <div className="rk-seg" role="group" aria-label="Kind of result" data-testid="mycroft-search-kinds">
          <button aria-pressed={kind === ''} onClick={() => find.setKind('')} data-testid="mycroft-search-kind-all">
            All <span className="rk-cnt">{total}</span>
          </button>
          {(result?.kinds ?? []).map((k) => (
            <button key={k} aria-pressed={kind === k} onClick={() => find.setKind(kind === k ? '' : k)} data-testid={`mycroft-search-kind-${k}`}>
              {FIND_KIND[k]?.label ?? k} <span className="rk-cnt">{result?.counts[k] ?? 0}</span>
            </button>
          ))}
        </div>
        <span className="cs-grow" />
        <span className="rk-fhint" aria-live="polite" data-testid="mycroft-search-status">
          {find.loading ? 'Searching…' : result ? (hits.length < listed ? `The best ${hits.length} of ${listed}` : `${listed} found`) : ''}
        </span>
      </div>
      {find.error && (
        <p className="tb-err" role="alert" data-testid="mycroft-search-error">
          {find.error}
        </p>
      )}
      <ul id="mycroft-find-results" role="listbox" aria-label="Results" className="rk-card" style={{ listStyle: 'none', margin: 0, padding: 4, display: 'flex', flexDirection: 'column' }} data-testid="mycroft-search-hits">
        {result && hits.length === 0 && !find.loading && (
          <li className="rk-fhint" style={{ padding: '8px 10px' }}>
            Nothing matches{kind ? ` among the ${FIND_KIND[kind]?.label.toLowerCase() ?? kind}` : ''}.
          </li>
        )}
        {hits.map((h, i) => {
          const on = i === find.cursor
          return (
            <li
              key={`${h.kind}:${h.id ?? h.path ?? h.source ?? ''}:${i}`}
              id={optionId(i)}
              ref={on ? active : undefined}
              role="option"
              aria-selected={on}
              onMouseEnter={() => find.setCursor(i)}
              onClick={() => find.open(h)}
              style={{ padding: '7px 10px', borderRadius: 6, cursor: 'pointer', background: on ? 'var(--surface)' : undefined, display: 'flex', flexDirection: 'column', gap: 2 }}
              data-testid="mycroft-search-hit"
            >
              <span style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', fontSize: 13 }}>
                <span className="rk-pill" data-tone="muted">
                  {FIND_KIND[h.kind]?.one ?? h.kind}
                </span>
                {h.id && <span className="rk-mono">{h.id}</span>}
                <strong style={{ fontWeight: 600 }}>{h.title}</strong>
                {h.status && (
                  <span className="rk-pill" data-tone={/^(active|identified|investigating|fixInProgress)$/.test(h.status) ? 'info' : 'muted'}>
                    {h.status}
                  </span>
                )}
                {h.stale && (
                  <span className="rk-pill" data-tone="warn">
                    Stale
                  </span>
                )}
                {h.date && <span className="rk-fhint">{h.date.slice(0, 10)}</span>}
                {h.kind === 'page' && h.path && <span className="rk-fhint rk-mono">{h.path.replace(/^pages\//, '')}</span>}
              </span>
              {h.text && (
                <span className="rk-fhint" style={{ fontSize: 12.5 }} data-testid="mycroft-search-snippet">
                  <Marked text={h.text} terms={h.terms ?? []} />
                </span>
              )}
            </li>
          )
        })}
      </ul>
      {result && hits.length > 0 && <p className="rk-fhint">↓ ↑ move through the results, Enter opens one, Esc clears the search. A source’s excerpt opens the source in a new tab.</p>}
    </section>
  )
}

const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

/** A snippet with each word that starts with a searched term marked. */
export function Marked({ text, terms }: { text: string; terms: string[] }): JSX.Element {
  const words = terms.filter((t) => t.length > 0)
  if (words.length === 0) return <>{text}</>
  const re = new RegExp(`(?<![\\p{L}\\p{N}])((?:${words.map(escape).join('|')})[\\p{L}\\p{N}]*)`, 'giu')
  const out: ReactNode[] = []
  text.split(re).forEach((part, i) => {
    // split keeps the captured words at the odd places.
    out.push(i % 2 === 1 ? <mark key={i}>{part}</mark> : part)
  })
  return <>{out}</>
}
