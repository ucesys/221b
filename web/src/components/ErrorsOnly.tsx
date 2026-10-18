/** The Nodes and Fleets filter for the nodes with an error (6.4). */
export function ErrorsOnly({ on, count, onChange }: { on: boolean; count: number; onChange: (on: boolean) => void }) {
  return (
    <label className="rk-check" title="Only the nodes with an error: not ready, not reporting, no network, or pods failing on them" style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5, whiteSpace: 'nowrap' }}>
      <input type="checkbox" checked={on} onChange={(e) => onChange(e.target.checked)} data-testid="errors-only" />
      Nodes with errors
      <span className="rk-cnt" style={count ? { color: 'var(--err)' } : undefined}>
        {count}
      </span>
    </label>
  )
}
