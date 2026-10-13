import type { Card } from '../lib/nodes'
import { clampPct } from '../lib/format'

/** A capacity card: the hero figure, a requested/used bar and its legend. */
export function CapacityCard({ c, small = false }: { c: Card; small?: boolean }) {
  return (
    <div
      className="rk-card"
      style={{ padding: small ? '12px 13px' : '13px 16px 12px', display: 'flex', flexDirection: 'column', gap: small ? 8 : 9, minWidth: 0 }}
      data-testid={`card-${c.label.toLowerCase()}`}
    >
      {small ? (
        <h3 className="rk-h">{c.label}</h3>
      ) : (
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', gap: 8 }}>
          <h3 className="rk-h">{c.label}</h3>
          <span className="rk-num" style={{ fontSize: 11.5, color: 'var(--fg3)', textAlign: 'right' }}>
            {c.total}
          </span>
        </div>
      )}
      <div style={{ display: 'flex', alignItems: 'baseline', gap: small ? 6 : 7, flexWrap: 'wrap' }}>
        <span className="rk-num" style={{ fontSize: small ? 19 : 22, fontWeight: 600, letterSpacing: '-0.02em', lineHeight: small ? 1.1 : 1 }}>
          {c.hero}
        </span>
        <span style={{ fontSize: small ? 11.5 : 12, color: 'var(--fg3)' }}>{c.heroLabel}</span>
      </div>
      <div className="rk-bar" title={c.tip} style={small ? undefined : { height: 8, borderRadius: 4 }}>
        <span className="rk-req" style={{ width: `${clampPct(c.reqPct).toFixed(1)}%` }} />
        <span className="rk-use" data-tone={c.useTone} style={{ width: `${clampPct(c.usePct).toFixed(1)}%` }} />
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 2, fontSize: 11.5 }}>
        {c.legend.map((l) => (
          <div key={l.k} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <span className="rk-sw" data-k={l.sw} />
            {/* As in the prototype: the cluster cards cut long labels short; the
                details panel's narrower cards let them wrap. */}
            <span style={small ? { flex: '1 1 auto', color: 'var(--fg3)' } : { flex: '1 1 auto', minWidth: 0, color: 'var(--fg3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {l.k}
            </span>
            <span className="rk-num" style={small ? { fontWeight: 500 } : { fontWeight: 500, whiteSpace: 'nowrap' }}>
              {l.v}
            </span>
            <span className="rk-num" style={{ minWidth: 32, textAlign: 'right', color: 'var(--fg3)' }}>
              {l.sub}
            </span>
          </div>
        ))}
      </div>
      {small && (
        <span className="rk-num" style={{ fontSize: 11, color: 'var(--fg3)' }}>
          {c.total}
        </span>
      )}
      {c.note && (
        <p className="rk-t" data-tone={c.noteTone} style={{ margin: 0, fontSize: 11.5 }}>
          {c.note}
        </p>
      )}
    </div>
  )
}
