// Cells shared by every table.
import type { Tone as ToneKind } from '../api/types'

/** A status with its dot: "● Running". */
export function Tone({ tone, text, tip, weight = 500 }: { tone: ToneKind; text: string; tip?: string; weight?: number }) {
  return (
    <span className="rk-t" data-tone={tone} title={tip} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontWeight: weight, maxWidth: '100%' }}>
      <span className="rk-dot" />
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{text}</span>
    </span>
  )
}

/** Monospace text in the secondary colour. */
export function Mono({ text, tip, dim = true, size }: { text: string; tip?: string; dim?: boolean; size?: number }) {
  return (
    <span className="rk-mono" style={{ color: dim ? 'var(--fg2)' : undefined, fontSize: size }} title={tip}>
      {text || '—'}
    </span>
  )
}

/** A link into another tab, in the table's own monospace style. */
export function CellLink({ href, text, tip }: { href: string; text: string; tip?: string }) {
  return (
    <a className="rk-mono rk-cell-link" href={href} title={tip} style={{ fontSize: 12 }}>
      {text}
    </a>
  )
}

/** A usage bar against a request: amber above the request, red near the limit. */
export function UsageBar({ used, request, limit, text, tip }: { used: number | null; request: number; limit: number; text: string; tip: string }) {
  const base = request || limit || (used ?? 0)
  const pctUsed = used == null || !base ? 0 : Math.min(100, (used / base) * 100)
  let tone: ToneKind = 'ok'
  if (used != null) {
    if (limit && used >= limit * 0.9) tone = 'err'
    else if (request && used > request) tone = 'warn'
  }
  return (
    <span className="rk-meter" title={tip} style={{ gap: 6 }}>
      <span className="rk-num" style={{ minWidth: 42, textAlign: 'right' }}>
        {text}
      </span>
      <span className="rk-bar" style={{ width: 40 }}>
        <span className="rk-use" data-tone={tone} style={{ width: `${pctUsed.toFixed(1)}%` }} />
      </span>
    </span>
  )
}
