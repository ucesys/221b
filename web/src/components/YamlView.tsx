import { useEffect, useMemo, useState } from 'react'
import { unauthorized } from '../api/auth'
import { useRefreshTick } from '../api/live'
import { download } from '../lib/xlsx'
import { CopyIcon, DownloadIcon } from './Icons'
import { apiPath } from '../lib/router'
import { copyText } from '../lib/clipboard'

interface Props {
  /** The kind's registry name: "pods", "services", "nodes". */
  kind: string
  namespace?: string
  name: string
  /** Fetches again on every refresh tick while shown. */
  live?: boolean
  onToast?: (msg: string) => void
}

export function yamlURL(kind: string, namespace: string | undefined, name: string): string {
  return apiPath(`yaml/${encodeURIComponent(kind)}/${namespace ? encodeURIComponent(namespace) + '/' : ''}${encodeURIComponent(name)}`)
}

type Seg = [string, string]

/** Colours a YAML line the way the prototype does: keys, strings, numbers, booleans and punctuation. */
export function colorLine(line: string): Seg[] {
  const m = /^(\s*)(- )?(?:("[^"]*"|[^\s"#][^:#]*?)(:)(\s|$))?(.*)$/.exec(line)
  if (!m) return [[line, 's']]
  const [, indent = '', dash = '', key, colon, space = '', rest = ''] = m
  const segs: Seg[] = []
  if (indent) segs.push([indent, 'p'])
  if (dash) segs.push([dash, 'p'])
  if (key !== undefined && colon) {
    segs.push([key, 'k'], [colon + space, 'p'])
  }
  const v = rest
  if (v) {
    const t = v.trim()
    let c = 's'
    if (/^(true|false|null|~)$/.test(t)) c = 'b'
    else if (/^-?\d+(\.\d+)?$/.test(t)) c = 'n'
    else if (/^[|>][-+]?$/.test(t) || t === '{}' || t === '[]') c = 'p'
    else if (t.startsWith('#')) c = 'p'
    segs.push([v, c])
  }
  return segs
}

const COLOR: Record<string, string> = { k: 'var(--y-key)', s: 'var(--y-str)', n: 'var(--y-num)', b: 'var(--y-bool)', p: 'var(--y-punc)' }

/**
 * An object as YAML, the way `kubectl get -o yaml` prints it apart from the
 * managed fields, which are left out, and Secret values, which the server
 * replaces by their sizes (P2-08).
 */
export function YamlView({ kind, namespace, name, live = true, onToast }: Props) {
  const tick = useRefreshTick()
  const [text, setText] = useState<string | null>(null)
  const [error, setError] = useState('')
  const url = yamlURL(kind, namespace, name)
  // Another object in the same panel: forget the last one's YAML at once,
  // so it never shows, or is copied, under this one's name.
  const [last, setLast] = useState(url)
  if (last !== url) {
    setLast(url)
    setText(null)
    setError('')
  }
  useEffect(() => {
    let gone = false
    fetch(url, { headers: { Accept: 'application/yaml' } })
      .then(async (r) => {
        const body = await r.text()
        if (gone) return
        if (!r.ok) {
          if (unauthorized(r)) return
          let msg = `The server answered ${r.status}.`
          try {
            msg = (JSON.parse(body) as { error?: string }).error ?? msg
          } catch {
            // not JSON
          }
          setError(msg)
          return
        }
        setError('')
        setText(body)
      })
      .catch(() => {
        if (!gone) setError('Could not reach the server.')
      })
    return () => {
      gone = true
    }
    // tick: fetched again on every refresh while live.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [url, live ? tick : 0])
  const lines = useMemo(() => (text ?? '').replace(/\n$/, '').split('\n'), [text])
  const file = `${kind.replace(/s$/, '')}-${namespace ? namespace + '-' : ''}${name}.yaml`
  const copy = async () => {
    if (await copyText(text ?? '')) onToast?.('Copied the YAML.')
    else onToast?.('Could not copy: the browser refused the clipboard.')
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10, minWidth: 0 }} data-testid="yaml">
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 16px' }}>
        <span className="rk-num" style={{ marginRight: 'auto', fontSize: 12, color: 'var(--fg3)' }}>
          {error ? '' : text == null ? 'Reading from the API server…' : `${lines.length} lines · as the API server returns it, managedFields hidden${kind === 'secrets' ? ', values replaced by their sizes' : ''}`}
        </span>
        <button className="rk-btn" onClick={copy} disabled={!text}>
          <CopyIcon />
          Copy YAML
        </button>
        <button className="rk-btn" onClick={() => text && download(text, file, 'application/yaml')} disabled={!text}>
          <DownloadIcon />
          Download
        </button>
      </div>
      {error && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }}>
          {error}
        </p>
      )}
      {text != null && !error && (
        <div className="rk-code" role="region" aria-label="YAML" tabIndex={0}>
          {lines.map((l, i) => (
            <div key={i} className="rk-ln">
              <i>{i + 1}</i>
              {colorLine(l).map((s, j) => (
                <span key={j} style={{ color: COLOR[s[1]] }}>
                  {s[0]}
                </span>
              ))}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
