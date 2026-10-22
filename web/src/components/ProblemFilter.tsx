/**
 * What the Nodes and Fleets tabs show of the nodes' problems (6.4): every
 * node, the nodes with a problem (an error or a warning), or only those
 * with an error. It lives in the URL as err; err=1, from when the filter
 * was a checkbox for errors, still means errors.
 */
export type ProblemLevel = 'all' | 'problems' | 'errors'

export function problemLevel(v: string | null): ProblemLevel {
  if (v === 'problems') return 'problems'
  return v === 'errors' || v === '1' ? 'errors' : 'all'
}

/** Whether a node is shown at this level. */
export function showsNode(level: ProblemLevel, r: { errors: number; warnings: number }): boolean {
  if (level === 'errors') return r.errors > 0
  if (level === 'problems') return r.errors + r.warnings > 0
  return true
}

export function ProblemFilter({ level, problems, errors, onChange }: { level: ProblemLevel; problems: number; errors: number; onChange: (level: ProblemLevel) => void }) {
  const segs: [ProblemLevel, string, number | null, string, string][] = [
    ['all', 'All', null, 'Every node', ''],
    ['problems', 'With problems', problems, 'The nodes with an error or a warning: cordoned, under memory, disk or process pressure, or pods not ready, as well as the errors', 'var(--warn)'],
    ['errors', 'With errors', errors, 'Only the nodes with an error: not ready, not reporting, no network, or pods failing on them', 'var(--err)'],
  ]
  return (
    <div className="rk-seg" role="group" aria-label="Show nodes by problems" data-testid="problem-filter">
      {segs.map(([id, label, n, tip, tone]) => (
        <button key={id} aria-pressed={level === id} title={tip} onClick={() => onChange(id)} data-testid={`problems-${id}`}>
          {label}
          {n !== null && <span style={{ color: n ? tone : 'var(--fg3)', fontWeight: 400 }}>{n}</span>}
        </button>
      ))}
    </div>
  )
}
