import { useEffect, useRef, useState } from 'react'
import type { ContainerDetail } from '../api/types'
import { errorText } from '../api/details'
import { DownloadIcon } from './Icons'

interface Props {
  namespace: string
  name: string
  containers: ContainerDetail[]
  /** The pod is gone: the stream cannot be opened again. */
  gone: boolean
  onToast?: (msg: string) => void
}

const TAILS: [number, string][] = [
  [100, 'Last 100 lines'],
  [500, 'Last 500 lines'],
  [2000, 'Last 2,000 lines'],
  [0, 'Everything'],
]

/** Lines kept on screen while following; older ones scroll away. */
const MAX_LINES = 5000

export function logURL(namespace: string, name: string, q: Record<string, string | undefined>): string {
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(q)) if (v) params.set(k, v)
  const s = params.toString()
  return `/api/v1/pods/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/log${s ? '?' + s : ''}`
}

/**
 * A pod's logs (P2-04): the chosen container, live while it runs, with
 * the previous container's output, timestamps, a tail length and a
 * download. The stream is the server's event stream of lines, so the
 * first lines show within a second or two.
 */
export function PodLogs({ namespace, name, containers, gone, onToast }: Props) {
  const [container, setContainer] = useState(() => (containers.find((c) => !c.init && c.state?.kind === 'running') ?? containers.find((c) => !c.init) ?? containers[0])?.name ?? '')
  const current = containers.find((c) => c.name === container)
  const canFollow = current?.state?.kind === 'running'
  const [follow, setFollow] = useState(true)
  const [previous, setPrevious] = useState(false)
  const [timestamps, setTimestamps] = useState(false)
  const [tail, setTail] = useState(500)
  const [lines, setLines] = useState<string[]>([])
  const [state, setState] = useState<'loading' | 'live' | 'ended' | 'error'>('loading')
  const [note, setNote] = useState('')
  const [stick, setStick] = useState(true)
  const box = useRef<HTMLDivElement>(null)
  const live = follow && canFollow && !previous && !gone
  // A new request (another container, option or pod) starts from nothing.
  const request = [namespace, name, container, previous, timestamps, tail, live, gone].join('|')
  const [shownRequest, setShownRequest] = useState(request)
  if (request !== shownRequest) {
    setShownRequest(request)
    setLines([])
    setNote('')
    setState('loading')
  }

  useEffect(() => {
    if (!container) return
    const q = { container, previous: previous ? '1' : undefined, timestamps: timestamps ? '1' : undefined, tail: tail ? String(tail) : undefined }
    if (!live) {
      let stale = false
      fetch(logURL(namespace, name, q))
        .then(async (r) => {
          if (stale) return
          if (!r.ok) {
            setNote(await errorText(r))
            setState('error')
            return
          }
          const text = await r.text()
          if (stale) return
          setLines(text ? text.replace(/\n$/, '').split('\n') : [])
          setState('ended')
          setNote(gone ? 'The pod is gone; this is what it had written.' : '')
        })
        .catch(() => {
          if (!stale) {
            setNote('Could not reach the server.')
            setState('error')
          }
        })
      return () => {
        stale = true
      }
    }
    const es = new EventSource(logURL(namespace, name, { ...q, follow: '1' }))
    es.addEventListener('open', () => setState('live'))
    es.addEventListener('log', (e) => {
      const batch = JSON.parse((e as MessageEvent<string>).data) as string[]
      setLines((ls) => {
        const next = ls.length + batch.length > MAX_LINES ? [...ls.slice(ls.length + batch.length - MAX_LINES), ...batch] : [...ls, ...batch]
        return next
      })
      setState('live')
    })
    es.addEventListener('end', (e) => {
      const body = JSON.parse((e as MessageEvent<string>).data) as { reason?: string }
      setNote(body.reason ?? 'The stream ended.')
      setState('ended')
      es.close()
    })
    es.addEventListener('error', () => {
      if (es.readyState === EventSource.CLOSED) {
        // An HTTP error: read why with a plain request.
        fetch(logURL(namespace, name, q))
          .then(async (r) => setNote(r.ok ? 'The stream closed.' : await errorText(r)))
          .catch(() => setNote('Could not reach the server.'))
        setState('error')
      }
    })
    return () => es.close()
  }, [namespace, name, container, previous, timestamps, tail, live, gone])

  // Keep the newest lines in view unless the reader scrolled up.
  useEffect(() => {
    const el = box.current
    if (el && stick) el.scrollTop = el.scrollHeight
  }, [lines, stick])

  const onScroll = () => {
    const el = box.current
    if (!el) return
    setStick(el.scrollHeight - el.scrollTop - el.clientHeight < 40)
  }

  const download = () => {
    const q = { container, previous: previous ? '1' : undefined, timestamps: timestamps ? '1' : undefined, download: '1' }
    const a = document.createElement('a')
    a.href = logURL(namespace, name, q)
    a.download = `${name}${container ? '-' + container : ''}${previous ? '-previous' : ''}.log`
    document.body.appendChild(a)
    a.click()
    a.remove()
    onToast?.(`Downloading ${a.download}`)
  }

  if (!containers.length) return <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>This pod has no containers.</p>

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10, flex: '1 1 auto', minHeight: 0 }} data-testid="logs">
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 14px', fontSize: 12.5 }}>
        <label className="rk-field" style={{ width: 'auto', minWidth: 180 }}>
          <span className="rk-sr">Container</span>
          <select value={container} onChange={(e) => setContainer(e.target.value)} data-testid="log-container">
            {containers.map((c) => (
              <option key={c.name} value={c.name}>
                {c.name}
                {c.init ? ' (init)' : ''}
                {c.state?.kind === 'running' ? '' : c.state?.kind === 'waiting' ? ' — waiting' : c.state?.kind === 'terminated' ? ' — terminated' : ''}
              </option>
            ))}
          </select>
        </label>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
          <input type="checkbox" checked={follow && canFollow} disabled={!canFollow || previous || gone} onChange={(e) => setFollow(e.target.checked)} style={{ margin: 0, accentColor: 'var(--accent)' }} />
          Follow
        </label>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'pointer' }} title="The output of the last run of this container, before it restarted">
          <input type="checkbox" checked={previous} disabled={!current?.lastState} onChange={(e) => setPrevious(e.target.checked)} style={{ margin: 0, accentColor: 'var(--accent)' }} data-testid="log-previous" />
          Previous container
        </label>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
          <input type="checkbox" checked={timestamps} onChange={(e) => setTimestamps(e.target.checked)} style={{ margin: 0, accentColor: 'var(--accent)' }} />
          Timestamps
        </label>
        <label className="rk-field" style={{ width: 'auto' }}>
          <span className="rk-sr">How much to read</span>
          <select value={tail} onChange={(e) => setTail(Number(e.target.value))}>
            {TAILS.map(([n, label]) => (
              <option key={n} value={n}>
                {label}
              </option>
            ))}
          </select>
        </label>
        <span style={{ flex: '1 1 auto' }} />
        <span className="rk-num" style={{ fontSize: 12, color: 'var(--fg3)' }} data-testid="log-state">
          {state === 'loading' ? 'Reading…' : state === 'live' ? `Live · ${lines.length} lines` : `${lines.length} lines`}
        </span>
        <button className="rk-btn" onClick={download} disabled={gone && previous}>
          <DownloadIcon />
          Download
        </button>
      </div>
      {note && (
        <p className="rk-callout" data-tone={state === 'error' ? 'warn' : undefined} style={{ margin: 0, fontSize: 12.5 }} data-testid="log-note">
          {note}
        </p>
      )}
      <div
        ref={box}
        onScroll={onScroll}
        className="rk-code rk-logbox"
        role="log"
        aria-live="off"
        aria-label={`Logs of ${container}`}
        tabIndex={0}
        data-testid="log-lines"
        style={{ flex: '1 1 auto', minHeight: 240, maxHeight: '70vh', padding: '8px 12px', overflow: 'auto', whiteSpace: 'pre', fontSize: 11.5, lineHeight: 1.55 }}
      >
        {lines.map((l, i) => (
          <div key={i} className="rk-logline">
            {l}
          </div>
        ))}
        {state === 'live' && lines.length === 0 && <span style={{ color: 'var(--fg3)' }}>Nothing written yet.</span>}
      </div>
    </div>
  )
}
