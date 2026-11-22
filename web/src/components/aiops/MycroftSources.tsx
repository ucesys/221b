// AIOps › Mycroft › Sources (0.12.1): what the agents can draw on. Since
// phase-13.1-ai (KN-50) it lists sources, not files: one row a source
// (GET /api/v1/knowledge/sources) with its kind, its items (and how many
// failed or are excluded), on or off, and when its last Wiki sync finished
// with a short result; a live source is looked up when asked, a source
// never read says Never. Opening a source (#/aiops/mycroft/sources/<id>)
// lists its items: uploaded documents and notes, web site pages,
// Confluence pages, git files, S3 objects, the console's records and the
// fixes, and a live source's addresses, looked up when asked and never
// copied. Sources hold thousands of items, so that list is paged and
// filtered on the server (GET /api/v1/knowledge/items?source=<id>): by
// type, state and words. A row opens its details: the first excerpts the
// agents quote, the wiki pages Mycroft wrote that drew on it, and a link
// to the original.
//
// A SuperAdmin excludes an item of a syncable source (a web site, a
// Confluence space, a git repository, an S3 bucket): Exclude this, or
// Exclude everything under …, with an optional reason. Its excerpts are
// emptied at once, every sync skips it, and the web crawler neither reads
// it nor follows the links found only on it. The Excluded filter lists
// what is excluded, with Include again, which takes effect at the next
// sync or Run now. Uploaded notes and documents are not excluded but
// removed (final). Removing or excluding says which wiki pages drew on the
// item, with Review those pages and Ask Mycroft to rewrite without it; the
// pages are never changed by it. A web page, a git file or an S3 object is
// shown by its path relative to its source, the source's name leading
// ("sandbox-knowledge › docs/a.csv", KN-49); Exclude everything under …
// is never offered at the source's root, which is turned off in Administration › AI ›
// Knowledge instead. Sources themselves are changed in
// Administration › AI › Knowledge. Admins and SuperAdmins see this page; a Viewer is
// told so. #/aiops/mycroft/wiki/sources, the Wiki's old Sources view, leads
// here (lib/router.ts).
import { useEffect, useState, type JSX } from 'react'
import { CallError } from '../../api/call'
import {
  excludeItems,
  fetchItemDetail,
  fetchItems,
  fetchSources,
  includeAgain,
  isWebUrl,
  ITEM_STATE_LABEL,
  ITEM_TYPE_LABEL,
  SOURCE_KIND_LABEL,
  SOURCES_HREF,
  sourceHref,
  type ItemDetail,
  type ItemsPage,
  type ItemStateName,
  type PageBrief,
  type SourceItem,
  type SourceSummary,
  type SourcesView,
} from '../../api/knowledge'
import { fmtTs, nWord } from '../../lib/format'
import { aiHref } from '../../lib/router'
import { ConfirmDialog } from '../ConfirmDialog'
import { Drawer } from '../Drawer'
import { Select } from '../Select'
import { RemoveUploadDialog } from '../ai/UploadedDocuments'
import { EmptyRow, MycroftPage, MycroftTable } from './MycroftPage'
import { PageLinks, SourceImpact } from './SourceImpact'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** How many items a page of the list holds. */
export const ITEMS_PAGE = 50

/** The states the filter offers, in order. */
const STATES: ItemStateName[] = ['read', 'reading', 'error', 'skipped', 'excluded', 'live']

/** An item's place in its source, the source's name leading; its id when it has no path. */
export const shownPath = (it: SourceItem): string => (it.path ? `${it.sourceName} › ${it.path}` : it.id)

/** What the page shows after an exclusion or a removal. */
interface Outcome {
  text: string
  title: string
  pages: PageBrief[]
  why: 'removed' | 'excluded'
}

/** An item's state, in words, with why when there is one. */
function StateCell({ it }: { it: SourceItem }) {
  const tone = it.state === 'error' ? 'err' : it.state === 'read' ? 'ok' : it.state === 'excluded' || it.state === 'skipped' ? 'warn' : 'muted'
  return (
    <span style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <span className="rk-pill" data-tone={tone} style={{ alignSelf: 'flex-start' }} data-testid="sources-item-state" data-state={it.state}>
        {it.state === 'live' ? 'Looked up when asked' : ITEM_STATE_LABEL[it.state]}
      </span>
      {it.state === 'live' ? (
        <span className="rk-fhint">never copied</span>
      ) : it.state === 'excluded' && it.exclusion ? (
        <span className="rk-fhint" style={{ overflowWrap: 'anywhere' }} data-testid="sources-item-exclusion">
          by {it.exclusion.by}, {fmtTs(it.exclusion.at)}
          {it.exclusion.pattern !== it.id ? ` · rule ${it.rule || it.exclusion.pattern}${it.exclusion.under ? ' and its children' : ''}` : ''}
          {it.exclusion.reason ? ` · ${it.exclusion.reason}` : ''}
        </span>
      ) : (
        it.why && (
          <span className="rk-fhint" style={{ overflowWrap: 'anywhere' }}>
            {it.why}
          </span>
        )
      )}
    </span>
  )
}

/** A source's kind, in words: Git repository, S3 bucket, Web site, Confluence, Uploaded documents, This console, Live. */
export const sourceKindText = (kind: string): string => (kind === 'live' ? 'Live' : ((SOURCE_KIND_LABEL as Record<string, string>)[kind] ?? kind))

/** When a source's last sync finished, with a short result: "<when> · 2 changed", "· failed"; Looked up when asked, or Never. */
export function lastSyncedText(s: SourceSummary): string {
  if (s.kind === 'live') return 'Looked up when asked'
  const ls = s.lastSync
  if (!ls) return 'Never'
  const when = fmtTs(ls.at)
  if (ls.failed) return `${when} · failed`
  if (ls.changed !== undefined) return `${when} · ${ls.changed === 0 ? 'nothing changed' : `${ls.changed} changed`}`
  if (ls.every) return `${when} · with every source`
  return when
}

/** A source's items, in words: "120 items", with how many failed or are excluded. */
export function itemsText(s: SourceSummary): { count: string; more: string } {
  const more = [s.failed ? `${s.failed} failed` : '', s.excluded ? `${s.excluded} excluded` : ''].filter(Boolean).join(' · ')
  return { count: nWord(s.items, 'item', 'items'), more }
}

/** AIOps › Mycroft › Sources: the sources, or the items of the one opened. */
export function MycroftSources({ open = '', onToast }: { open?: string; onToast?: (msg: string) => void }): JSX.Element {
  return open ? <SourceItems key={open} source={open} onToast={onToast} /> : <SourceList />
}

/** What the page says on top, with the link to change the sources for a SuperAdmin. */
function Lead({ text, settings }: { text: string; settings: boolean }) {
  return (
    <>
      {text}
      {settings && (
        <>
          {' '}
          <a className="rk-link" href={aiHref('knowledge')} data-testid="sources-settings-link">
            Change the sources in Administration › AI › Knowledge
          </a>
          .
        </>
      )}
    </>
  )
}

const LIST_LEAD = 'The sources the agents draw on, one row each: open one to list its items. Live sources are looked up when asked, never copied.'

/** One row a source: its kind, its items, on or off, and its last sync (KN-50). */
function SourceList(): JSX.Element {
  const [data, setData] = useState<SourcesView | null>(null)
  const [error, setError] = useState('')
  const [denied, setDenied] = useState(false)
  const [typed, setTyped] = useState('')
  useEffect(() => {
    let stop = false
    fetchSources().then(
      (r) => {
        if (stop) return
        setData(r)
        setError('')
      },
      (e: unknown) => {
        if (stop) return
        setDenied(e instanceof CallError && e.status === 403)
        setError(errorText(e))
      },
    )
    return () => {
      stop = true
    }
  }, [])
  const lead = <Lead text={LIST_LEAD} settings={!!data?.mayExclude} />
  if (denied)
    return (
      <MycroftPage testId="mycroft-sources" lead={lead} filter={null}>
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="sources-denied">
          Admins and SuperAdmins see the sources and their items.
        </p>
      </MycroftPage>
    )
  const words = typed.trim().toLowerCase().split(/\s+/).filter(Boolean)
  const all = data?.sources ?? []
  const rows = all.filter((x) => words.every((w) => `${x.name} ${sourceKindText(x.kind)}`.toLowerCase().includes(w)))
  return (
    <MycroftPage
      testId="mycroft-sources"
      lead={lead}
      filter={null}
      tools={
        <label className="rk-field" style={{ flex: '0 1 240px' }}>
          <span className="rk-sr">Search the sources</span>
          <input type="search" placeholder="Name or kind" value={typed} onChange={(e) => setTyped(e.target.value)} onKeyDown={(e) => e.key === 'Escape' && setTyped('')} data-testid="sources-list-search" />
        </label>
      }
    >
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="sources-error">
          {error}
        </p>
      )}
      <MycroftTable label="Sources" testId="sources-table">
        <thead>
          <tr>
            <th>Source</th>
            <th>Kind</th>
            <th>Items</th>
            <th>State</th>
            <th>Last synced</th>
          </tr>
        </thead>
        <tbody>
          {data === null && !error && <EmptyRow cols={5}>Loading…</EmptyRow>}
          {data !== null && rows.length === 0 && (
            <EmptyRow cols={5} testId="sources-empty">
              {all.length > 0 ? 'No source matches these words.' : 'No sources yet.'}
            </EmptyRow>
          )}
          {rows.map((x) => {
            const items = itemsText(x)
            return (
              <tr key={x.id} data-testid="sources-row" data-id={x.id}>
                <td style={{ overflowWrap: 'anywhere', maxWidth: 320 }}>
                  <a className="rk-link" href={sourceHref(x.id)} data-testid="sources-row-open">
                    {x.name}
                  </a>
                </td>
                <td data-testid="sources-row-kind">{sourceKindText(x.kind)}</td>
                <td data-testid="sources-row-items">
                  {items.count}
                  {items.more && <div className="rk-fhint">{items.more}</div>}
                </td>
                <td>
                  <span className="rk-pill" data-tone={x.off ? 'muted' : 'ok'} data-testid="sources-row-state">
                    {x.off ? 'Off' : 'On'}
                  </span>
                </td>
                <td style={{ maxWidth: 300 }} data-testid="sources-row-synced" title={x.lastSync?.error}>
                  <span className={x.lastSync?.failed ? 'rk-t' : undefined} data-tone={x.lastSync?.failed ? 'err' : undefined}>
                    {lastSyncedText(x)}
                  </span>
                </td>
              </tr>
            )
          })}
        </tbody>
      </MycroftTable>
    </MycroftPage>
  )
}

/** The items of the source opened: paged on the server, with its search and filters. */
function SourceItems({ source, onToast }: { source: string; onToast?: (msg: string) => void }): JSX.Element {
  const [type, setType] = useState('')
  const [state, setState] = useState('')
  const [typed, setTyped] = useState('')
  const [q, setQ] = useState('')
  const [offset, setOffset] = useState(0)
  const [data, setData] = useState<ItemsPage | null>(null)
  const [error, setError] = useState('')
  const [denied, setDenied] = useState(false)
  const [tick, setTick] = useState(0)
  const [open, setOpen] = useState<SourceItem | null>(null)
  const [excluding, setExcluding] = useState<SourceItem | null>(null)
  const [removing, setRemoving] = useState<SourceItem | null>(null)
  const [outcome, setOutcome] = useState<Outcome | null>(null)
  const [notice, setNotice] = useState('')
  // The words are sent once typing pauses.
  useEffect(() => {
    const t = setTimeout(() => {
      setQ(typed.trim())
      setOffset(0)
    }, 300)
    return () => clearTimeout(t)
  }, [typed])
  useEffect(() => {
    let stop = false
    fetchItems({ source, type, state, q, offset, limit: ITEMS_PAGE }).then(
      (r) => {
        if (stop) return
        setData(r)
        setError('')
      },
      (e: unknown) => {
        if (stop) return
        setDenied(e instanceof CallError && e.status === 403)
        setError(errorText(e))
      },
    )
    return () => {
      stop = true
    }
  }, [source, type, state, q, offset, tick])
  const reload = () => setTick((t) => t + 1)
  const pick = (set: (v: string) => void) => (v: string) => {
    set(v)
    setOffset(0)
  }
  const include = async (it: SourceItem) => {
    const ex = it.exclusion
    if (!ex) return
    try {
      const r = await includeAgain(it.source, ex.pattern, !!ex.under)
      setNotice(r.text)
      onToast?.(r.text)
      reload()
    } catch (e) {
      setError(errorText(e))
    }
  }
  const total = data?.total ?? 0
  const items = data?.items ?? []
  const named = data?.sources.find((x) => x.id === source)
  const name = named?.name ?? (data ? source : '')
  const lead = (
    <>
      <span style={{ display: 'flex', gap: 8, alignItems: 'baseline', flexWrap: 'wrap' }}>
        <a className="rk-link" href={SOURCES_HREF} data-testid="sources-back">
          ‹ All sources
        </a>
        {name && (
          <strong style={{ color: 'var(--fg)' }} data-testid="sources-open-name">
            {name}
          </strong>
        )}
        {named && <span className="rk-fhint">{named.kind === 'uploads' ? 'Uploaded documents' : sourceKindText(named.kind)}</span>}
        {named?.off && (
          <span className="rk-pill" data-tone="muted">
            Off
          </span>
        )}
      </span>
      <Lead text={named?.kind === 'live' ? 'Its addresses are looked up when asked, never copied.' : 'Its items, as Mycroft read them.'} settings={!!data?.mayExclude} />
    </>
  )
  if (denied)
    return (
      <MycroftPage testId="mycroft-sources" lead={lead} filter={null}>
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="sources-denied">
          Admins and SuperAdmins see the sources and their items.
        </p>
      </MycroftPage>
    )
  return (
    <MycroftPage
      testId="mycroft-sources"
      lead={lead}
      filter={
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
          <Select label="Type" value={type} options={[['', 'Every type'], ...(Object.entries(ITEM_TYPE_LABEL) as [string, string][])]} onChange={pick(setType)} testId="sources-filter-type" />
          <Select label="State" value={state} options={[['', 'Every state'], ...STATES.map((s): [string, string] => [s, s === 'live' ? 'Live' : ITEM_STATE_LABEL[s]])]} onChange={pick(setState)} testId="sources-filter-state" />
        </div>
      }
      tools={
        <label className="rk-field" style={{ flex: '0 1 240px' }}>
          <span className="rk-sr">Search the items</span>
          <input type="search" placeholder="Title, path, address or who added it" value={typed} onChange={(e) => setTyped(e.target.value)} onKeyDown={(e) => e.key === 'Escape' && setTyped('')} data-testid="sources-search" />
        </label>
      }
    >
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="sources-error">
          {error}
        </p>
      )}
      {notice && (
        <p className="rk-callout" data-tone="info" role="status" style={{ margin: 0, fontSize: 12.5 }} data-testid="sources-notice">
          {notice}
        </p>
      )}
      {outcome && <SourceImpact text={outcome.text} title={outcome.title} pages={outcome.pages} why={outcome.why} mayRewrite={!!data?.mayRemove} onToast={onToast} />}
      {state === 'excluded' && data && (
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="sources-excluded-hint">
          Excluded items are not read by any sync, and the web crawler does not follow links found only on them. Include again takes effect at the next sync of the source, or its Run now in AIOps › Jobs.
        </p>
      )}
      <MycroftTable label="Items" testId="sources-items-table">
        <thead>
          <tr>
            <th>Title</th>
            <th>Type</th>
            <th>State</th>
            <th style={{ textAlign: 'right' }}>Pages</th>
            <th>
              <span className="rk-sr">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {data === null && !error && <EmptyRow cols={5}>Loading…</EmptyRow>}
          {data !== null && items.length === 0 && (
            <EmptyRow cols={5} testId="sources-empty">
              {type || state || q ? 'No item matches these filters.' : 'No items yet: Mycroft has read nothing of this source.'}
            </EmptyRow>
          )}
          {items.map((it) => (
            <tr key={it.source + '\u0000' + it.id} data-testid="sources-item" data-source={it.source} data-id={it.id}>
              <td style={{ overflowWrap: 'anywhere', maxWidth: 380 }}>
                <button className="rk-link" style={{ textAlign: 'left' }} onClick={() => setOpen(it)} data-testid="sources-item-open">
                  {it.title || it.id}
                </button>
                {it.title && shownPath(it) !== it.title && it.type !== 'note' && (
                  <div className="rk-fhint rk-mono" data-testid="sources-item-path">
                    {shownPath(it)}
                  </div>
                )}
              </td>
              <td>{ITEM_TYPE_LABEL[it.type] ?? it.type}</td>
              <td style={{ maxWidth: 300 }}>
                <StateCell it={it} />
              </td>
              <td style={{ textAlign: 'right' }}>{it.pages ?? 0}</td>
              <td style={{ whiteSpace: 'nowrap' }}>
                {data?.mayExclude && it.excludable && it.state !== 'excluded' && it.state !== 'live' && (
                  <button className="rk-link" onClick={() => setExcluding(it)} data-testid="sources-item-exclude">
                    Exclude…
                  </button>
                )}
                {data?.mayExclude && it.state === 'excluded' && it.exclusion && (
                  <button className="rk-link" onClick={() => void include(it)} data-testid="sources-item-include">
                    Include again
                  </button>
                )}
                {data?.mayRemove && (it.type === 'note' || it.type === 'document') && it.kind === 'upload' && (
                  <button className="rk-link" onClick={() => setRemoving(it)} data-testid="sources-item-remove">
                    Remove
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </MycroftTable>
      {data && total > 0 && (
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }} data-testid="sources-paging">
          <span className="rk-fhint" data-testid="sources-count">
            {offset + 1}–{offset + items.length} of {total}
          </span>
          <span className="cs-grow" />
          <button className="rk-btn" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - ITEMS_PAGE))} data-testid="sources-prev">
            Previous
          </button>
          <button className="rk-btn" disabled={offset + items.length >= total} onClick={() => setOffset(offset + ITEMS_PAGE)} data-testid="sources-next">
            Next
          </button>
        </div>
      )}
      {open && <ItemDrawer it={open} onClose={() => setOpen(null)} />}
      {excluding && (
        <ExcludeDialog
          it={excluding}
          onCancel={() => setExcluding(null)}
          onDone={(o) => {
            setExcluding(null)
            setNotice('')
            setOutcome(o)
            onToast?.(o.text)
            reload()
          }}
        />
      )}
      {removing && (
        <RemoveUploadDialog
          item={{ source: removing.source, id: removing.id, title: removing.title, pages: undefined }}
          onCancel={() => setRemoving(null)}
          onDone={(r) => {
            setRemoving(null)
            setNotice('')
            setOutcome({ text: `“${r.title}” was removed: the agents no longer quote it.`, title: r.title, pages: r.pages, why: 'removed' })
            reload()
          }}
        />
      )}
    </MycroftPage>
  )
}

/** Exclude this, or everything under it, with an optional reason (a SuperAdmin's). */
function ExcludeDialog({ it, onCancel, onDone }: { it: SourceItem; onCancel: () => void; onDone: (o: Outcome) => void }) {
  const [what, setWhat] = useState<'this' | 'under'>('this')
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const confluence = it.kind === 'confluence'
  const go = async () => {
    setBusy(true)
    setError('')
    try {
      const input = what === 'this' ? { item: it.id, reason: reason.trim() || undefined } : confluence ? { item: it.id, under: true, reason: reason.trim() || undefined } : { pattern: it.under, reason: reason.trim() || undefined }
      const r = await excludeItems(it.source, input)
      onDone({ text: r.text, title: it.title || it.id, pages: r.pages ?? [], why: 'excluded' })
    } catch (e) {
      setError(errorText(e))
      setBusy(false)
    }
  }
  const under = it.under || confluence
  return (
    <ConfirmDialog title={`Exclude from ${it.sourceName}`} confirm="Exclude" danger busy={busy} error={error} onCancel={onCancel} onConfirm={() => void go()}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="sources-exclude-dialog">
        <label className="rk-tcheck">
          <input type="radio" name="exclude-what" checked={what === 'this'} onChange={() => setWhat('this')} data-testid="sources-exclude-this" />
          Exclude this: {it.title || it.id}
          {it.path && ` (${shownPath(it)})`}
        </label>
        {under && (
          <label className="rk-tcheck">
            <input type="radio" name="exclude-what" checked={what === 'under'} onChange={() => setWhat('under')} data-testid="sources-exclude-under" />
            Exclude everything under {it.underLabel || it.under || 'this page: the page and its children'}
          </label>
        )}
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12 }}>
          Reason (optional)
          <span className="rk-field">
            <input value={reason} maxLength={300} onChange={(e) => setReason(e.target.value)} placeholder="Why it is left out" data-testid="sources-exclude-reason" />
          </span>
        </label>
        <p className="rk-fhint" style={{ margin: 0 }}>
          Its excerpts are emptied at once and the agents no longer quote it; every sync skips it, and the web crawler neither reads it nor follows links found only on it. The wiki pages that drew on it are not changed. Include again from
          the Excluded filter.
        </p>
      </div>
    </ConfirmDialog>
  )
}

/** An item's details: its excerpts, the pages that drew on it, and the original. */
function ItemDrawer({ it, onClose }: { it: SourceItem; onClose: () => void }) {
  const [d, setD] = useState<ItemDetail | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let stop = false
    fetchItemDetail(it.source, it.id).then(
      (x) => !stop && setD(x),
      (e: unknown) => !stop && setError(errorText(e)),
    )
    return () => {
      stop = true
    }
  }, [it.source, it.id])
  const url = d?.item.url ?? it.url
  return (
    <Drawer label="Source item" kicker={`${it.sourceName} · ${ITEM_TYPE_LABEL[it.type] ?? it.type}`} title={it.title || it.id} copy={false} onClose={onClose} width={760} testId="sources-item-drawer">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12, fontSize: 12.5 }}>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <StateCell it={d?.item ?? it} />
          {it.by && <span className="rk-fhint">added by {it.by}</span>}
          {it.lastRead && <span className="rk-fhint">read {fmtTs(it.lastRead)}</span>}
        </div>
        {url &&
          (isWebUrl(url) ? (
            <a className="rk-link" href={url} target="_blank" rel="noreferrer" style={{ overflowWrap: 'anywhere' }} data-testid="sources-item-original">
              Open the original
            </a>
          ) : (
            <span className="rk-mono" style={{ overflowWrap: 'anywhere' }} data-testid="sources-item-original">
              {url}
            </span>
          ))}
        {error && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0 }}>
            {error}
          </p>
        )}
        {!d && !error && <p className="rk-fhint">Loading…</p>}
        {d && (
          <>
            <section aria-label="Excerpts" data-testid="sources-item-excerpts">
              <strong>Excerpts</strong>
              {d.excerpts.length === 0 ? (
                <p className="rk-fhint" style={{ margin: '4px 0 0' }}>
                  {it.state === 'live' ? 'A live source is never copied: it is read when an agent looks it up.' : 'No excerpts are kept of it.'}
                </p>
              ) : (
                <>
                  {d.excerpts.map((x, i) => (
                    <pre key={i} style={{ whiteSpace: 'pre-wrap', margin: '6px 0 0', padding: '8px 10px', background: 'var(--surface)', borderRadius: 6, fontSize: 12 }}>
                      {x}
                    </pre>
                  ))}
                  {d.excerptCount > d.excerpts.length && (
                    <p className="rk-fhint" style={{ margin: '4px 0 0' }}>
                      The first {d.excerpts.length} of {d.excerptCount} excerpts.
                    </p>
                  )}
                </>
              )}
            </section>
            <section aria-label="Wiki pages" data-testid="sources-item-pages">
              <strong>Wiki pages that drew on it</strong>
              {!d.provenance ? (
                <p className="rk-fhint" style={{ margin: '4px 0 0' }}>
                  The wiki does not record which pages drew on this item.
                </p>
              ) : d.pages.length === 0 ? (
                <p className="rk-fhint" style={{ margin: '4px 0 0' }}>
                  No wiki page drew on it yet.
                </p>
              ) : (
                <PageLinks pages={d.pages} />
              )}
            </section>
          </>
        )}
      </div>
    </Drawer>
  )
}
