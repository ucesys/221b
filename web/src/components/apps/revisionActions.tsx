import { createContext, useContext } from 'react'
import { DiffIcon, RollbackIcon } from '../Icons'

/** What a revision row can ask for: the diff of that revision (Phase 10 review), and a rollback to it (Phase 10.1, M-03). */
export const RevisionActions = createContext<{ diff: (revision: number) => void; rollback?: (revision: number) => void; current?: number } | null>(null)

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

/** The rollback icon in a revision row: on the older revisions only, where the page can roll back. */
export function RevisionRollbackButton({ revision }: { revision: number }) {
  const ctx = useContext(RevisionActions)
  const roll = ctx?.rollback
  if (!roll || revision === ctx?.current) return null
  return (
    <button
      className="tb-tools"
      style={{ opacity: 1, marginLeft: 0 }}
      aria-label={`Roll back to revision ${revision}`}
      title={`Roll back to revision ${revision}: helm rollback, as a Watson card to approve`}
      onClick={(e) => {
        e.stopPropagation()
        roll(revision)
      }}
      data-testid={`revision-rollback-${revision}`}
    >
      <RollbackIcon />
    </button>
  )
}
