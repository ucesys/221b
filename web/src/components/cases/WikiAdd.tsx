// Cases › Mycroft › Wiki › Add (Phase 12, wave 5, MY-04 to MY-07): what a
// person adds to the wiki: a note of plain text, a web address to read
// (the pages of its site it links to, as deep and as many as asked,
// robots.txt respected), or documents (PDF, Word .docx, Markdown, HTML,
// plain text, CSV). Each is a source item: scrubbed of secrets, quoted by
// the agents once read, and written up by Mycroft as pages that wait for
// review under Drafts. Admins and SuperAdmins add; the server decides.
import { useRef, useState } from 'react'
import { addKnowledge, fileBase64, wikiHref, type AddKind, type AddResult, type WikiState } from '../../api/knowledge'
import { Select } from '../Select'

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const KINDS: { id: AddKind; label: string; tip: string }[] = [
  { id: 'text', label: 'Text', tip: 'A note of plain text or Markdown' },
  { id: 'url', label: 'Web address', tip: 'A page, and the pages of its site it links to' },
  { id: 'document', label: 'Documents', tip: 'PDF, Word, Markdown, HTML, text or CSV files' },
]

const DEPTH_LABEL = ['Only this page', 'And the pages it links to', 'Two links away', 'Three links away']

const kib = (n: number) => (n >= 1 << 20 ? `${Math.round(n / (1 << 20))} MiB` : `${Math.round(n / 1024)} KiB`)

/** The extension of a file's name, lower case, with its dot. */
const extOf = (name: string) => {
  const i = name.lastIndexOf('.')
  return i < 0 ? '' : name.slice(i).toLowerCase()
}

export function WikiAdd({ wiki, onAdded, onToast }: { wiki: WikiState | null; onAdded: () => void; onToast?: (msg: string) => void }) {
  const [kind, setKind] = useState<AddKind>('text')
  const [title, setTitle] = useState('')
  const [text, setText] = useState('')
  const [url, setURL] = useState('')
  const [depth, setDepth] = useState<number | null>(null)
  const [pages, setPages] = useState('')
  const [files, setFiles] = useState<File[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [results, setResults] = useState<AddResult[]>([])
  const input = useRef<HTMLInputElement>(null)
  if (!wiki) return <p className="rk-fhint">Loading…</p>
  const l = wiki.limits
  const bytes = new TextEncoder().encode(text).length
  const why = !wiki.store ? 'No wiki is kept yet: a SuperAdmin chooses where in Administration › Settings › Knowledge.' : !wiki.mayAdd ? 'Admins and SuperAdmins add to the wiki.' : ''
  const badFile = files.find((f) => !l.formats.includes(extOf(f.name)) || f.size > l.document)
  const pagesN = pages.trim() === '' ? l.defaultPages : Number(pages)
  const ready =
    !why &&
    !busy &&
    (kind === 'text'
      ? title.trim() !== '' && text.trim() !== '' && bytes <= l.note
      : kind === 'url'
        ? /^https?:\/\/\S+$/i.test(url.trim()) && Number.isInteger(pagesN) && pagesN >= 1 && pagesN <= l.pages
        : files.length > 0 && !badFile)
  const submit = async () => {
    setBusy(true)
    setError('')
    setResults([])
    const done: AddResult[] = []
    try {
      if (kind === 'text') {
        done.push(await addKnowledge({ kind, title: title.trim(), text }))
        setTitle('')
        setText('')
      } else if (kind === 'url') {
        done.push(await addKnowledge({ kind, url: url.trim(), depth: depth ?? l.defaultDepth, pages: pagesN }))
      } else {
        // One at a time, each its own request; the first that fails stops.
        for (const f of files) {
          done.push(await addKnowledge({ kind, name: f.name, data: await fileBase64(f) }))
          setResults([...done])
        }
        setFiles([])
      }
      const items = done.flatMap((r) => r.items)
      onToast?.(items.length === 1 ? `“${items[0]?.title}” added to the wiki’s sources.` : `${items.length} ${kind === 'url' ? 'pages' : 'documents'} added to the wiki’s sources.`)
      onAdded()
    } catch (e) {
      setError(errorText(e))
      if (done.length) onAdded()
    } finally {
      setResults(done)
      setBusy(false)
    }
  }
  return (
    <section className="rk-card" style={{ padding: '14px 18px', display: 'flex', flexDirection: 'column', gap: 12, maxWidth: 760 }} aria-label="Add to the wiki" data-testid="wiki-add">
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
        <div className="rk-seg" role="group" aria-label="What to add">
          {KINDS.map((k) => (
            <button key={k.id} aria-pressed={kind === k.id} title={k.tip} onClick={() => setKind(k.id)} disabled={busy} data-testid={`wiki-add-kind-${k.id}`}>
              {k.label}
            </button>
          ))}
        </div>
        <span className="rk-fhint" style={{ flex: '1 1 260px' }}>
          It becomes a source Mycroft reads: the agents can quote it once read, and the pages Mycroft writes from it wait for review under{' '}
          <a className="rk-link" href={wikiHref('drafts')}>
            Drafts
          </a>
          . Secrets in it are taken out before anything is kept.
        </span>
      </div>
      {why && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="wiki-add-refused">
          {why}
        </p>
      )}
      {!why && !wiki.model && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }}>
          Mycroft has no model here: what is added is kept as excerpts the agents quote, and no pages are written from it.
        </p>
      )}
      <div className="cs-sec" style={{ padding: 0 }}>
        {kind === 'text' && (
          <>
            <label className="cs-lbl">
              Title
              <input className="cs-in" value={title} maxLength={120} onChange={(e) => setTitle(e.target.value)} placeholder="What it is about, in a line" disabled={busy || !!why} data-testid="wiki-add-title" />
            </label>
            <label className="cs-lbl">
              Text
              <textarea
                className="cs-ta"
                style={{ minHeight: 200 }}
                value={text}
                onChange={(e) => setText(e.target.value)}
                placeholder="Plain text or Markdown: how something works, a runbook, a decision and why. A note of the same title replaces the one before."
                disabled={busy || !!why}
                data-testid="wiki-add-text"
              />
            </label>
            <span className={bytes > l.note ? 'tb-err' : 'tb-hint'} style={{ fontSize: 12 }}>
              {kib(bytes)} of {kib(l.note)}
              {bytes > l.note ? ': add a longer text as a document.' : ''}
            </span>
          </>
        )}
        {kind === 'url' && (
          <>
            <label className="cs-lbl">
              Web address
              <input className="cs-in rk-mono" type="url" value={url} onChange={(e) => setURL(e.target.value)} placeholder="https://wiki.example.com/runbooks/" disabled={busy || !!why} data-testid="wiki-add-url" />
            </label>
            <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'flex-end' }}>
              <span className="cs-lbl">
                How far
                <Select
                  label="How far"
                  value={String(depth ?? l.defaultDepth)}
                  options={Array.from({ length: l.depth + 1 }, (_, i): [string, string] => [String(i), DEPTH_LABEL[i] ?? `${i} links away`])}
                  onChange={(v) => setDepth(Number(v))}
                  disabled={busy || !!why}
                  testId="wiki-add-depth"
                />
              </span>
              <label className="cs-lbl" style={{ width: 140 }}>
                At most, pages
                <input className="cs-in" type="number" min={1} max={l.pages} value={pages} placeholder={String(l.defaultPages)} onChange={(e) => setPages(e.target.value)} disabled={busy || !!why} data-testid="wiki-add-pages" />
              </label>
            </div>
            <span className="tb-hint" style={{ fontSize: 12 }}>
              Only pages of the same site are read, one at a time, up to {l.pages} and {l.depth} links away; robots.txt and pages that ask not to be indexed are respected. The console reads the site from its own network, so it must be reachable from there.
            </span>
          </>
        )}
        {kind === 'document' && (
          <>
            <input
              ref={input}
              type="file"
              multiple
              accept={l.formats.join(',')}
              style={{ display: 'none' }}
              data-testid="wiki-add-file"
              onChange={(e) => {
                setFiles([...(e.target.files ?? [])])
                e.target.value = ''
              }}
            />
            <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
              <button className="rk-btn" onClick={() => input.current?.click()} disabled={busy || !!why} data-testid="wiki-add-choose">
                Choose files…
              </button>
              <span className="tb-hint" style={{ fontSize: 12 }}>
                PDF, Word (.docx), Markdown, HTML, plain text or CSV, up to {kib(l.document)} each. A document of the same name replaces the one before. A PDF is read by the text its pages draw; a scanned one gives none.
              </span>
            </div>
            {files.length > 0 && (
              <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5 }} data-testid="wiki-add-files">
                {files.map((f) => (
                  <li key={f.name}>
                    {f.name} <span className="rk-fhint">{kib(f.size)}</span>
                    {!l.formats.includes(extOf(f.name)) && <span className="tb-err"> not a kind the console reads</span>}
                    {f.size > l.document && <span className="tb-err"> over {kib(l.document)}</span>}
                  </li>
                ))}
              </ul>
            )}
          </>
        )}
        {error && (
          <p className="tb-err" role="alert" style={{ margin: 0 }} data-testid="wiki-add-error">
            {error}
          </p>
        )}
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <span className="cs-grow" />
          {busy && <span className="tb-hint">{kind === 'url' ? 'Reading the site… (up to a minute and a half)' : 'Adding…'}</span>}
          <button className="rk-btn" data-on disabled={!ready} onClick={() => void submit()} data-testid="wiki-add-submit">
            {kind === 'text' ? 'Add note' : kind === 'url' ? 'Read and add' : files.length > 1 ? `Add ${files.length} documents` : 'Add document'}
          </button>
        </div>
      </div>
      {results.length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 12.5 }} data-testid="wiki-add-result">
          {results.map((r, i) => (
            <div key={i} style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              <span>{r.text}</span>
              {r.kind === 'url' && (
                <ul style={{ margin: 0, paddingLeft: 18 }}>
                  {r.items.map((it) => (
                    <li key={it.id}>
                      {it.url ? (
                        <a className="rk-link" href={it.url} target="_blank" rel="noreferrer">
                          {it.title}
                        </a>
                      ) : (
                        it.title
                      )}{' '}
                      <span className="rk-fhint">{kib(it.bytes)}</span>
                    </li>
                  ))}
                </ul>
              )}
              {r.skipped && r.skipped.length > 0 && (
                <details>
                  <summary className="rk-fhint" style={{ cursor: 'pointer' }}>
                    {r.skipped.length} left out
                  </summary>
                  <ul style={{ margin: '4px 0 0', paddingLeft: 18 }} data-testid="wiki-add-skipped">
                    {r.skipped.map((s) => (
                      <li key={s.url}>
                        <span className="rk-mono">{s.url}</span>: {s.why}
                      </li>
                    ))}
                  </ul>
                </details>
              )}
            </div>
          ))}
          <span>
            <a className="rk-link" href={wikiHref('sources')} data-testid="wiki-add-sources-link">
              See what was added
            </a>
          </span>
        </div>
      )}
    </section>
  )
}
