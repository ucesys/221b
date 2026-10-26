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

/**
 * A session's terminal (TB-15): xterm.js over the session's WebSocket. Output
 * and keystrokes are binary messages; a resize or a keep-alive is a small
 * JSON text message; the server says "ended" before it closes. The terminal
 * replays what the session kept, so a reload or another window picks it up.
 */
export function Terminal({ cluster, id, watch = false, dark, onEnded, onInput }: Props) {
  const box = useRef<HTMLDivElement>(null)
  const ended = useRef(onEnded)
  const typed = useRef(onInput)
  useEffect(() => {
    ended.current = onEnded
    typed.current = onInput
  }, [onEnded, onInput])

  useEffect(() => {
    const el = box.current
    if (!el) return
    const term = new XTerm({
      fontFamily: '"IBM Plex Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      fontSize: 12.5,
      lineHeight: 1.2,
      cursorBlink: !watch,
      disableStdin: watch,
      scrollback: 5000,
      theme: dark ? THEME_DARK : THEME_LIGHT,
      allowProposedApi: false,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    const unmirror = typeof CSSStyleSheet !== 'undefined' && 'adoptedStyleSheets' in document ? mirrorStyles(el) : () => undefined
    term.open(el)
    try {
      fit.fit()
    } catch {
      /* not laid out yet */
    }
    const ws = new WebSocket(attachURL(cluster, id, watch))
    ws.binaryType = 'arraybuffer'
    const enc = new TextEncoder()
    const resize = () => {
      if (ws.readyState === WebSocket.OPEN && !watch) ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
    }
    let over = false
    ws.onopen = () => {
      resize()
      if (!watch) term.focus()
    }
    ws.onmessage = (e: MessageEvent<ArrayBuffer | string>) => {
      if (typeof e.data === 'string') {
        try {
          const c = JSON.parse(e.data) as { type: string; note?: string; by?: string }
          if (c.type === 'ended') {
            over = true
            const note = c.note || 'The session ended.'
            term.write(`\r\n\x1b[2m── ${note} ──\x1b[0m\r\n`)
            ended.current?.(note)
          }
        } catch {
          /* not a control message */
        }
        return
      }
      term.write(new Uint8Array(e.data))
    }
    ws.onclose = (e) => {
      if (!over) term.write(`\r\n\x1b[2m── ${e.reason || 'Disconnected from the console.'} ──\x1b[0m\r\n`)
    }
    const data = term.onData((d) => {
      if (ws.readyState !== WebSocket.OPEN || watch) return
      ws.send(enc.encode(d))
      typed.current?.()
    })
    const bin = term.onBinary((d) => {
      if (ws.readyState !== WebSocket.OPEN || watch) return
      const b = new Uint8Array(d.length)
      for (let i = 0; i < d.length; i++) b[i] = d.charCodeAt(i) & 0xff
      ws.send(b)
    })
    const sized = term.onResize(resize)
    const ro = new ResizeObserver(() => {
      try {
        fit.fit()
      } catch {
        /* hidden */
      }
    })
    ro.observe(el)
    return () => {
      ro.disconnect()
      data.dispose()
      bin.dispose()
      sized.dispose()
      ws.onclose = null
      ws.close()
      term.dispose()
      unmirror()
    }
  }, [cluster, id, watch, dark])

  return <div ref={box} className="tb-xterm" data-testid="session-terminal" />
}
