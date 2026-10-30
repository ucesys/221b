import type { AppDiff } from '../../api/apps'

/** A unified diff, line by line: additions green, removals red, hunks muted (HA-17). */
export function DiffView({ diff, fill = false }: { diff: AppDiff; fill?: boolean }) {
  if (diff.same) {
    return (
      <p className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5 }} data-testid="app-diff-same">
        {diff.from} and {diff.to} render the same objects.
      </p>
    )
  }
  const summary = [
    diff.changed.length ? `${diff.changed.length} changed` : '',
    diff.added.length ? `${diff.added.length} added` : '',
    diff.removed.length ? `${diff.removed.length} removed` : '',
  ]
    .filter(Boolean)
    .join(' · ')
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, minWidth: 0, flex: fill ? '1 1 auto' : undefined, minHeight: 0 }} data-testid="app-diff">
      <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>
        <b style={{ fontWeight: 600 }}>{diff.from}</b> → <b style={{ fontWeight: 600 }}>{diff.to}</b>: {summary}
        {diff.changed.length > 0 && <span style={{ color: 'var(--fg3)' }}> — {diff.changed.slice(0, 6).join(', ')}{diff.changed.length > 6 ? ', …' : ''}</span>}
      </p>
      <pre className="rk-code rk-mono" style={{ margin: 0, padding: '10px 12px', fontSize: 12, lineHeight: 1.5, overflow: 'auto', maxHeight: fill ? undefined : 520, flex: fill ? '1 1 auto' : undefined, minHeight: 0, whiteSpace: 'pre', background: 'var(--sunken)', borderRadius: 8, border: '1px solid var(--line)' }}>
        <DiffLines text={diff.text} />
      </pre>
    </div>
  )
}

/** The lines of a unified diff, coloured. */
export function DiffLines({ text }: { text: string }) {
  return (
    <>
      {text.split('\n').map((l, i) => {
        const color = l.startsWith('+') && !l.startsWith('+++') ? 'var(--ok)' : l.startsWith('-') && !l.startsWith('---') ? 'var(--err)' : l.startsWith('@@') ? 'var(--info)' : l.startsWith('+++') || l.startsWith('---') ? 'var(--fg3)' : undefined
        return (
          <span key={i} style={{ display: 'block', color }}>
            {l || ' '}
          </span>
        )
      })}
    </>
  )
}
