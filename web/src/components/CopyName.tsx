import { useEffect, useState } from 'react'
import { CopyIcon } from './Icons'
import { copyText } from '../lib/clipboard'

/**
 * A light copy-to-clipboard icon beside an object's name in a details
 * pane: faint until hovered or focused, and a tick for a moment after.
 */
export function CopyName({ text, what = 'name' }: { text: string; what?: string }) {
  const [done, setDone] = useState<'ok' | 'fail' | null>(null)
  useEffect(() => {
    if (!done) return
    const t = window.setTimeout(() => setDone(null), 1500)
    return () => window.clearTimeout(t)
  }, [done])
  const copy = () => {
    void copyText(text).then((ok) => setDone(ok ? 'ok' : 'fail'))
  }
  const label = done === 'ok' ? `Copied ${text}` : done === 'fail' ? 'The browser refused the clipboard' : `Copy the ${what}`
  return (
    <button type="button" className="rk-copy" onClick={copy} aria-label={label} title={label} data-copied={done ?? undefined} data-testid="copy-name">
      {done === 'ok' ? (
        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M20 6 9 17l-5-5" />
        </svg>
      ) : (
        <CopyIcon />
      )}
    </button>
  )
}
