import { createContext, useContext } from 'react'
import { DiffIcon } from '../Icons'

/** What a revision row can ask for: the diff of that revision (Phase 10 review). */
export const RevisionActions = createContext<{ diff: (revision: number) => void } | null>(null)

/** The diff icon in a revision row; nothing where no page offers diffs. */
export function RevisionDiffButton({ revision }: { revision: number }) {
  const ctx = useContext(RevisionActions)
  if (!ctx) return null
  return (
    <button
      className="tb-tools"
      style={{ opacity: 1, marginLeft: 0 }}
      aria-label={`Diff of revision ${revision}`}
      title={`What revision ${revision} changed, against the one before it`}
      onClick={(e) => {
        e.stopPropagation()
        ctx.diff(revision)
      }}
      data-testid={`revision-diff-${revision}`}
    >
      <DiffIcon />
    </button>
  )
}
