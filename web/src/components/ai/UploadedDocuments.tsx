// Administration › AI › Knowledge › Uploaded documents (0.12.1): the notes and the
// documents people give Mycroft, from the two built-in sources of uploads
// (the SuperAdmins' uploaded documents and what was added in Mycroft), in
// one place with two tabs. A note is a title and a text, kept as a Markdown
// document; a document is a file of a kind the console reads. Each is
// scrubbed of secrets, read by the source's Wiki sync job at once, quoted
// by the agents once read, and written up by Mycroft as pages that wait for
// review. A note or a document of the same title or name replaces the one
// before. Remove is final: it leaves the index at once; the wiki pages that
// drew on it are not changed, and the dialog names them, with Review those
// pages and Ask Mycroft to rewrite without it after. Admins see this
// section alone on their Administration page (AdminPage.tsx); SuperAdmins
// see it in Administration › AI › Knowledge after the Sources. These are not sources
// to sync again, so they are not rows of the Sources table.
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { addKnowledge, documentTrustOptions, fetchUploads, fileBase64, removeUpload, setSourceOn, wikiHref, type PageBrief, type TrustLevel, type UploadItem, type UploadsView } from '../../api/knowledge'
import { CallError } from '../../api/call'
import { fmtTs } from '../../lib/format'
import { ConfirmDialog } from '../ConfirmDialog'
import { Select } from '../Select'
import { PageLinks, SourceImpact } from '../aiops/SourceImpact'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const kib = (n: number) => (n >= 1 << 20 ? `${Math.round(n / (1 << 20))} MiB` : `${Math.round(n / 1024)} KiB`)

/** The extension of a file's name, lower case, with its dot. */
const extOf = (name: string) => {
  const i = name.lastIndexOf('.')
  return i < 0 ? '' : name.slice(i).toLowerCase()
}

/** How many rows a tab shows before Show more. */
export const UPLOAD_ROWS = 25

export type UploadTab = 'note' | 'document'

/** The items a tab lists: its type, every word of the filter in the title or who added it, newest first. */
export function uploadsShown(items: UploadItem[], tab: UploadTab, filter: string): UploadItem[] {
  const words = filter.toLowerCase().split(/\s+/).filter(Boolean)
  return items.filter((it) => it.type === tab && words.every((w) => it.title.toLowerCase().includes(w) || (it.by ?? '').toLowerCase().includes(w))).sort((a, b) => (b.at ?? '').localeCompare(a.at ?? '') || a.title.localeCompare(b.title))
}

/** An uploaded item's state, in words. */
export function UploadState({ it }: { it: Pick<UploadItem, 'state' | 'error' | 'lastRead'> }) {
  if (it.state === 'error')
    return (
      <span className="rk-t" data-tone="err" style={{ fontSize: 12, overflowWrap: 'anywhere' }} data-testid="uploads-state" data-state="error">
        Error{it.error ? `: ${it.error}` : ''}
      </span>
    )
  if (it.state === 'reading')
    return (
      <span className="rk-pill" data-tone="muted" data-testid="uploads-state" data-state="reading">
        Reading
      </span>
    )
  return (
    <span className="rk-pill" data-tone="ok" title={it.lastRead ? `Read ${fmtTs(it.lastRead)}` : undefined} data-testid="uploads-state" data-state="read">
      Read
    </span>
  )
}

/**
 * Remove an uploaded note or document: final; the dialog names the wiki
 * pages that drew on it, and after the removal they are offered for review
 * or a rewrite (onDone gets what to show).
 */
export function RemoveUploadDialog({ item, onCancel, onDone }: { item: { source: string; id: string; title: string; pages?: string[] }; onCancel: () => void; onDone: (r: { title: string; pages: PageBrief[] }) => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const pages: PageBrief[] = (item.pages ?? []).map((path) => ({ path, title: '' }))
  const remove = async () => {
    setBusy(true)
    setError('')
    try {
      const r = await removeUpload(item.source, item.id)
      onDone({ title: item.title, pages: r.pages ?? pages })
    } catch (e) {
      setError(errorText(e))
      setBusy(false)
    }
  }
  return (
    <ConfirmDialog title={`Remove “${item.title}”?`} confirm="Remove" danger busy={busy} error={error} onCancel={onCancel} onConfirm={() => void remove()}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="uploads-remove-dialog">
        <p style={{ margin: 0 }}>It leaves Mycroft’s sources at once, and the agents no longer quote it. This is final: to have it again, add it again.</p>
        {pages.length > 0 ? (
          <>
            <p style={{ margin: 0 }}>These wiki pages drew on it. They are not changed; after the removal you can review them or ask Mycroft to rewrite them without it:</p>
            <PageLinks pages={pages} testId="uploads-remove-pages" />
          </>
        ) : item.pages ? (
          <p style={{ margin: 0 }} className="rk-fhint">
            No wiki page drew on it.
          </p>
        ) : (
          <p style={{ margin: 0 }} className="rk-fhint">
            The wiki pages that drew on it, if any, are named once it is removed; they are not changed.
          </p>
        )}
      </div>
    </ConfirmDialog>
  )
}

/**
 * The section. focus scrolls to it once loaded; fallback, when given, is
 * shown instead to someone who may not add, and after follows the section
 * for someone who may.
 */
export function UploadedDocuments({ onToast, focus = false, fallback, after }: { onToast?: (msg: string) => void; focus?: boolean; fallback?: ReactNode; after?: ReactNode }) {
  const [v, setV] = useState<UploadsView | null>(null)
  const [error, setError] = useState('')
  const [denied, setDenied] = useState(false)
  const [tick, setTick] = useState(0)
  const [tab, setTab] = useState<UploadTab>('note')
  const [filter, setFilter] = useState('')
  const [rows, setRows] = useState(UPLOAD_ROWS)
  const [removing, setRemoving] = useState<UploadItem | null>(null)
  const [removed, setRemoved] = useState<{ title: string; pages: PageBrief[] } | null>(null)
  const refresh = useCallback(() => setTick((t) => t + 1), [])
  // After an addition, look again every few seconds while something is being read, two minutes at most.
  const [pollUntil, setPollUntil] = useState(0)
  const added = useCallback(() => {
    setPollUntil(Date.now() + 120_000)
    refresh()
  }, [refresh])
  useEffect(() => {
    let stop = false
    fetchUploads(tick > 0).then(
      (x) => {
        if (stop) return
        setV({ ...x, items: x.items ?? [], off: x.off ?? [] })
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
  }, [tick])
  useEffect(() => {
    if (!v || Date.now() > pollUntil || !v.items.some((it) => it.state === 'reading')) return
    const t = setTimeout(refresh, 3000)
    return () => clearTimeout(t)
  }, [v, pollUntil, refresh])
  const card = useRef<HTMLElement>(null)
  const loaded = v !== null
  useEffect(() => {
    if (focus && loaded) card.current?.scrollIntoView?.({ block: 'start' })
  }, [focus, loaded])
  const shown = useMemo(() => uploadsShown(v?.items ?? [], tab, filter), [v, tab, filter])
  if (fallback && (denied || (v && !v.mayAdd))) return <>{fallback}</>
  const count = (t: UploadTab) => (v?.items ?? []).filter((it) => it.type === t).length
  const why = !v ? '' : !v.store ? 'No wiki is kept yet: a SuperAdmin chooses where in Administration › AI › Knowledge.' : !v.mayAdd ? 'Admins and SuperAdmins add notes and documents.' : ''
  return (
    <>
      <section ref={card} className="rk-card" style={{ padding: '12px 16px 16px', display: 'flex', flexDirection: 'column', gap: 12 }} aria-label="Uploaded documents" data-testid="uploads-section">
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, flexWrap: 'wrap' }}>
          <h3 className="rk-h" style={{ margin: 0 }}>
            Uploaded documents
          </h3>
          <span className="rk-fhint" style={{ flex: '1 1 260px' }}>
            Notes and documents people give Mycroft. Each is read once when added, quoted by the agents once read, and the pages Mycroft writes from it wait for review under{' '}
            <a className="rk-link" href={wikiHref('drafts')}>
              Drafts
            </a>
            . Secrets in it are taken out before anything is kept.
          </span>
        </div>
        {error && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }} data-testid="uploads-error">
            {error}
          </p>
        )}
        {v && v.off.length > 0 && <OffNotice off={v.off} maySetup={v.maySetup} onChanged={refresh} onError={setError} />}
        {why && (
          <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="uploads-refused">
            {why}
          </p>
        )}
        {v && !why && !v.model && (
          <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }}>
            Mycroft has no model here: what is added is kept as excerpts the agents quote, and no pages are written from it.
          </p>
        )}
        {!v ? (
          !error && (
            <p className="rk-fhint" style={{ margin: 0 }}>
              Loading…
            </p>
          )
        ) : (
          <>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
              <div className="rk-seg" role="group" aria-label="Notes or documents">
                {(['note', 'document'] as const).map((t) => (
                  <button
                    key={t}
                    aria-pressed={tab === t}
                    onClick={() => {
                      setTab(t)
                      setRows(UPLOAD_ROWS)
                    }}
                    data-testid={`uploads-tab-${t === 'note' ? 'notes' : 'documents'}`}
                  >
                    {t === 'note' ? 'Notes' : 'Documents'} <span className="rk-cnt">{count(t)}</span>
                  </button>
                ))}
              </div>
              <label className="rk-field" style={{ flex: '0 1 260px' }}>
                <span className="rk-sr">Filter by title or who added it</span>
                <input
                  type="search"
                  placeholder="Filter by title or who added it"
                  value={filter}
                  onChange={(e) => {
                    setFilter(e.target.value)
                    setRows(UPLOAD_ROWS)
                  }}
                  data-testid="uploads-filter"
                />
              </label>
            </div>
            {tab === 'note' ? <AddNote v={v} disabled={!!why} onAdded={added} onToast={onToast} /> : <Upload v={v} disabled={!!why} onAdded={added} onToast={onToast} />}
            {removed && <SourceImpact text={`“${removed.title}” was removed: the agents no longer quote it.`} title={removed.title} pages={removed.pages} why="removed" mayRewrite={v.mayAdd} onToast={onToast} />}
            <div style={{ overflowX: 'auto' }}>
              <table className="tb-table" style={{ width: '100%' }} data-testid="uploads-table">
                <thead>
                  <tr>
                    <th scope="col">Title</th>
                    <th scope="col">Added by</th>
                    <th scope="col">When</th>
                    <th scope="col">State</th>
                    <th scope="col">
                      <span className="rk-sr">Remove</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {shown.length === 0 && (
                    <tr>
                      <td colSpan={5} style={{ color: 'var(--fg3)' }} data-testid="uploads-empty">
                        {filter.trim() ? `No ${tab === 'note' ? 'note' : 'document'} has that title or was added by that person.` : tab === 'note' ? 'No notes yet.' : 'No documents yet.'}
                      </td>
                    </tr>
                  )}
                  {shown.slice(0, rows).map((it) => (
                    <tr key={it.source + '/' + it.id} data-testid="uploads-row" data-source={it.source} data-id={it.id}>
                      <td style={{ overflowWrap: 'anywhere' }}>
                        <strong>{it.title}</strong>
                        {it.corrects && <div className="rk-fhint">A correction of {it.corrects.replace(/^pages\//, '')}</div>}
                        {it.reported && (
                          <span className="rk-pill" data-tone="warn" style={{ marginLeft: 6 }} title={it.reported}>
                            Tried to give instructions
                          </span>
                        )}
                      </td>
                      <td>{it.by || '—'}</td>
                      <td style={{ whiteSpace: 'nowrap' }}>{it.at ? fmtTs(it.at) : '—'}</td>
                      <td style={{ maxWidth: 320 }}>
                        <UploadState it={it} />
                      </td>
                      <td style={{ whiteSpace: 'nowrap' }}>
                        {v.mayAdd && (
                          <button className="rk-link" onClick={() => setRemoving(it)} data-testid="uploads-remove">
                            Remove
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {shown.length > rows && (
              <button className="rk-btn" style={{ alignSelf: 'flex-start' }} onClick={() => setRows((r) => r + UPLOAD_ROWS)} data-testid="uploads-more">
                Show more ({shown.length - rows} left)
              </button>
            )}
          </>
        )}
      </section>
      {after}
      {removing && (
        <RemoveUploadDialog
          item={removing}
          onCancel={() => setRemoving(null)}
          onDone={(r) => {
            setRemoving(null)
            setRemoved(r)
            onToast?.(`“${r.title}” removed: the agents no longer quote it.`)
            refresh()
          }}
        />
      )}
    </>
  )
}

/** The built-in sources of uploads switched off: nothing in them is read; a SuperAdmin turns them on. */
function OffNotice({ off, maySetup, onChanged, onError }: { off: { id: string; name: string }[]; maySetup: boolean; onChanged: () => void; onError: (msg: string) => void }) {
  const [busy, setBusy] = useState(false)
  return (
    <div className="rk-callout" data-tone="warn" style={{ fontSize: 12.5, alignItems: 'center', gap: 8, flexWrap: 'wrap' }} data-testid="uploads-off">
      <span style={{ flex: '1 1 300px' }}>
        {off.map((x) => x.name).join(' and ')} {off.length === 1 ? 'is' : 'are'} switched off: what is in {off.length === 1 ? 'it' : 'them'} is not read, and nothing can be added to {off.length === 1 ? 'it' : 'them'}.
        {!maySetup && ' A SuperAdmin turns it on.'}
      </span>
      {maySetup && (
        <button
          className="rk-btn"
          data-size="sm"
          disabled={busy}
          onClick={async () => {
            setBusy(true)
            try {
              for (const x of off) await setSourceOn(x.id, true)
              onChanged()
            } catch (e) {
              onError(errorText(e))
            } finally {
              setBusy(false)
            }
          }}
          data-testid="uploads-turn-on"
        >
          Turn on
        </button>
      )}
    </div>
  )
}

/** Add note: a title and a text, plain or Markdown; one of the same title replaces the one before. */
function AddNote({ v, disabled, onAdded, onToast }: { v: UploadsView; disabled: boolean; onAdded: () => void; onToast?: (msg: string) => void }) {
  const [open, setOpen] = useState(false)
  const [title, setTitle] = useState('')
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const l = v.limits
  const bytes = new TextEncoder().encode(text).length
  if (!open)
    return (
      <div>
        <button className="rk-btn" disabled={disabled} onClick={() => setOpen(true)} data-testid="uploads-note-open">
          Add note…
        </button>
      </div>
    )
  const ready = !disabled && !busy && title.trim() !== '' && text.trim() !== '' && bytes <= l.note
  const submit = async () => {
    setBusy(true)
    setError('')
    try {
      const r = await addKnowledge({ kind: 'text', title: title.trim(), text })
      onToast?.(r.text || `“${title.trim()}” added.`)
      setTitle('')
      setText('')
      setOpen(false)
      onAdded()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="cs-sec" style={{ padding: 0, maxWidth: 760 }} data-testid="uploads-note-form">
      <label className="cs-lbl">
        Title
        <input className="cs-in" value={title} maxLength={120} onChange={(e) => setTitle(e.target.value)} placeholder="What it is about, in a line" disabled={busy} data-testid="uploads-note-title" />
      </label>
      <label className="cs-lbl">
        Text
        <textarea
          className="cs-ta"
          style={{ minHeight: 160 }}
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder="Plain text or Markdown: how something works, a runbook, a decision and why. A note of the same title replaces the one before."
          disabled={busy}
          data-testid="uploads-note-text"
        />
      </label>
      <span className={bytes > l.note ? 'tb-err' : 'tb-hint'} style={{ fontSize: 12 }}>
        {kib(bytes)} of {kib(l.note)}
        {bytes > l.note ? ': add a longer text as a document.' : ''}
      </span>
      {error && (
        <p className="tb-err" role="alert" style={{ margin: 0 }} data-testid="uploads-note-error">
          {error}
        </p>
      )}
      <div style={{ display: 'flex', gap: 8 }}>
        <button className="rk-btn" data-on disabled={!ready} onClick={() => void submit()} data-testid="uploads-note-submit">
          Add note
        </button>
        <button className="rk-btn" disabled={busy} onClick={() => setOpen(false)}>
          Cancel
        </button>
      </div>
    </div>
  )
}

/**
 * Upload: files of the kinds the console reads, each its own request; one of the same name replaces the one before.
 * A SuperAdmin chooses the documents' trust (KN-61): their source's by default; an Admin chooses none, and a
 * document uploaded again keeps its own.
 */
function Upload({ v, disabled, onAdded, onToast }: { v: UploadsView; disabled: boolean; onAdded: () => void; onToast?: (msg: string) => void }) {
  const input = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [trust, setTrust] = useState<'' | TrustLevel>('')
  const l = v.limits
  const send = async (files: File[]) => {
    const bad = files.find((f) => !l.formats.includes(extOf(f.name)) || f.size > l.document)
    if (bad) {
      setError(!l.formats.includes(extOf(bad.name)) ? `${bad.name} is not a kind the console reads.` : `${bad.name} is over ${kib(l.document)}.`)
      return
    }
    setBusy(true)
    setError('')
    let done = 0
    try {
      // One at a time, each its own request; the first that fails stops.
      for (const f of files) {
        await addKnowledge({ kind: 'document', name: f.name, data: await fileBase64(f), ...(v.maySetup ? { trust } : {}) })
        done++
      }
      onToast?.(files.length === 1 ? `Mycroft is reading ${files[0]?.name}.` : `Mycroft is reading ${files.length} documents.`)
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
      if (done) onAdded()
    }
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        <input
          ref={input}
          type="file"
          multiple
          accept={l.formats.join(',')}
          style={{ display: 'none' }}
          data-testid="uploads-file"
          onChange={(e) => {
            const files = [...(e.target.files ?? [])]
            e.target.value = ''
            if (files.length) void send(files)
          }}
        />
        {v.maySetup && (
          <span style={{ display: 'flex', gap: 6, alignItems: 'center' }} title="How its passages rank in the search, and whether Mycroft writes pages from it by itself (only from high trust)">
            <span style={{ fontSize: 12.5 }}>Trust</span>
            <Select label="Trust of the documents uploaded" value={trust} options={documentTrustOptions(v.sourceTrust)} onChange={(t) => setTrust(t as '' | TrustLevel)} disabled={disabled || busy} testId="uploads-trust" />
          </span>
        )}
        <button className="rk-btn" disabled={disabled || busy} onClick={() => input.current?.click()} data-testid="uploads-upload">
          {busy ? 'Uploading…' : 'Upload…'}
        </button>
        <span className="tb-hint" style={{ fontSize: 12 }}>
          PDF, Word (.docx), Markdown, HTML, plain text or CSV, up to {kib(l.document)} each. A document of the same name replaces the one before. A PDF is read by the text its pages draw; a scanned one gives none.
          {v.maySetup && ' A document’s trust ranks it in the search and says whether Mycroft writes pages from it by itself; change it later on its row in AIOps › Mycroft › Sources.'}
        </span>
      </div>
      {error && (
        <p className="tb-err" role="alert" style={{ margin: 0 }} data-testid="uploads-upload-error">
          {error}
        </p>
      )}
    </div>
  )
}
