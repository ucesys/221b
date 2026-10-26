import { useEffect, useRef } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { attachURL } from '../../api/toolbox'

interface Props {
  cluster: string
  id: string
  /** An administrator's read-only view (TB-55). */
  watch?: boolean
  dark: boolean
  /** The session is live, as the stream says: a lost connection is worth another try (TB-14). */
  live?: boolean
  /** The server says the session ended, and how. */
  onEnded?: (note: string) => void
  /** Keystrokes went out: the idle countdown starts again. */
  onInput?: () => void
}

/**
 * xterm.js writes its theme and its cell sizes into <style> elements, which
 * the console's Content-Security-Policy (style-src 'self') does not apply.
 * Their text is mirrored into constructed stylesheets, which it does.
 */
function mirrorStyles(root: HTMLElement): () => void {
  const sheets = new Map<HTMLStyleElement, CSSStyleSheet>()
  const sync = () => {
    for (const el of root.querySelectorAll('style')) {
      let sheet = sheets.get(el)
      if (!sheet) {
        sheet = new CSSStyleSheet()
        sheets.set(el, sheet)
        document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet]
      }
      try {
        sheet.replaceSync(el.textContent ?? '')
      } catch {
        /* a rule the browser does not take */
      }
    }
  }
  sync()
  const mo = new MutationObserver(sync)
  mo.observe(root, { subtree: true, childList: true, characterData: true })
  return () => {
    mo.disconnect()
    const ours = new Set(sheets.values())
    document.adoptedStyleSheets = document.adoptedStyleSheets.filter((x) => !ours.has(x))
  }
}

const THEME_DARK = { background: '#090C10', foreground: '#D5DCE4', cursor: '#F2F5F8', selectionBackground: '#2F4A66' }
const THEME_LIGHT = { background: '#0F1419', foreground: '#D5DCE4', cursor: '#F2F5F8', selectionBackground: '#2F4A66' }

/** How long to wait before trying the connection again: 1 s, then doubling to 10 s. */
export function retryDelay(attempt: number): number {
  return Math.min(10_000, 1000 * 2 ** Math.max(0, attempt))
}

/**
 * A session's terminal (TB-15): xterm.js over the session's WebSocket. Output
 * and keystrokes are binary messages; a resize or a keep-alive is a small
 * JSON text message; the server says "ended" before it closes, or "dropped"
 * when this window fell behind the output and should come back. The terminal
 * replays what the session kept, so a reload, another window, a dropped
 * window or one that lost its connection picks it up where it was (TB-14).
 */
export function Terminal({ cluster, id, watch = false, dark, live = true, onEnded, onInput }: Props) {
  const box = useRef<HTMLDivElement>(null)
  const term = useRef<XTerm | null>(null)
  const ended = useRef(onEnded)
  const typed = useRef(onInput)
  const isLive = useRef(live)
  useEffect(() => {
    ended.current = onEnded
    typed.current = onInput
    isLive.current = live
  }, [onEnded, onInput, live])

  // The theme follows the console's without reopening the session.
  useEffect(() => {
    if (term.current) term.current.options.theme = dark ? THEME_DARK : THEME_LIGHT
  }, [dark])

  useEffect(() => {
    const el = box.current
    if (!el) return
    const t = new XTerm({
      fontFamily: '"IBM Plex Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      fontSize: 12.5,
      lineHeight: 1.2,
      cursorBlink: !watch,
      disableStdin: watch,
      scrollback: 5000,
      theme: dark ? THEME_DARK : THEME_LIGHT,
      allowProposedApi: false,
    })
    term.current = t
    const fit = new FitAddon()
    t.loadAddon(fit)
    const unmirror = typeof CSSStyleSheet !== 'undefined' && 'adoptedStyleSheets' in document ? mirrorStyles(el) : () => undefined
    t.open(el)
    try {
      fit.fit()
    } catch {
      /* not laid out yet */
    }
    const enc = new TextEncoder()
    const dim = (text: string) => t.write(`\r\n\x1b[2m── ${text} ──\x1b[0m\r\n`)

    let ws: WebSocket | null = null
    // over: the server said the session ended; gone: this terminal was unmounted.
    let over = false
    let gone = false
    let attempts = 0
    let timer: ReturnType<typeof setTimeout> | undefined
    const send = (d: Uint8Array<ArrayBuffer> | string) => {
      if (ws && ws.readyState === WebSocket.OPEN) ws.send(d)
    }
    const resize = () => {
      if (!watch) send(JSON.stringify({ type: 'resize', cols: t.cols, rows: t.rows }))
    }
    const retry = (delay: number) => {
      clearTimeout(timer)
      timer = setTimeout(() => {
        if (!gone && !over && isLive.current) connect(true)
      }, delay)
    }
    // The server keeps the last of the output: a new connection starts
    // with a replay, so a window that comes back starts from a clean screen.
    const connect = (again: boolean) => {
      const sock = new WebSocket(attachURL(cluster, id, watch))
      sock.binaryType = 'arraybuffer'
      ws = sock
      sock.onopen = () => {
        attempts = 0
        if (again) t.reset()
        resize()
        if (!watch) t.focus()
      }
      sock.onmessage = (e: MessageEvent<ArrayBuffer | string>) => {
        if (typeof e.data === 'string') {
          try {
            const c = JSON.parse(e.data) as { type: string; note?: string; by?: string }
            if (c.type === 'ended') {
              over = true
              const note = c.note || 'The session ended.'
              dim(note)
              ended.current?.(note)
            } else if (c.type === 'dropped') {
              // Only this window fell behind; the session goes on.
              sock.onclose = null
              sock.close()
              dim(`${c.note || 'This window fell behind the output'}; reconnecting`)
              retry(0)
            }
          } catch {
            /* not a control message */
          }
          return
        }
        t.write(new Uint8Array(e.data))
      }
      sock.onclose = (e) => {
        if (gone || over) return
        if (!isLive.current) {
          dim(e.reason || 'Disconnected from the console.')
          return
        }
        dim(`${e.reason || 'Disconnected from the console'}; reconnecting`)
        retry(retryDelay(attempts++))
      }
    }
    connect(false)

    const data = t.onData((d) => {
      if (watch) return
      send(enc.encode(d) as Uint8Array<ArrayBuffer>)
      typed.current?.()
    })
    const bin = t.onBinary((d) => {
      if (watch) return
      const b = new Uint8Array(d.length)
      for (let i = 0; i < d.length; i++) b[i] = d.charCodeAt(i) & 0xff
      send(b)
    })
    const sized = t.onResize(resize)
    const ro = new ResizeObserver(() => {
      try {
        fit.fit()
      } catch {
        /* hidden */
      }
    })
    ro.observe(el)
    return () => {
      gone = true
      clearTimeout(timer)
      ro.disconnect()
      data.dispose()
      bin.dispose()
      sized.dispose()
      if (ws) {
        ws.onclose = null
        ws.close()
      }
      t.dispose()
      term.current = null
      unmirror()
    }
    // The theme has its own effect above: changing it must not reopen the session.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, id, watch])

  return <div ref={box} className="tb-xterm" data-testid="session-terminal" />
}
