import type { HistoryText } from '../../api/ai'
import { lineDiff } from '../../lib/linediff'
import { DiffLines } from '../apps/DiffView'

/**
 * One text's diff, as Helm › History shows a revision's (DiffView): how
 * many lines each side has that the other lacks, then the unified diff,
 * additions green and removals red.
 */
export function TextDiff({ label, from, to, testId }: { label: string; from: string; to: string; testId?: string }) {
  const d = lineDiff(from, to)
  const same = !d.added && !d.removed
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }} data-testid={testId} data-same={same ? 'true' : 'false'}>
      <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>
        <b style={{ fontWeight: 600 }}>{label}</b>: {same ? 'no change' : `+${d.added} −${d.removed} lines`}
      </p>
      {d.text && (
        <pre
          className="rk-code rk-mono"
          style={{ margin: 0, padding: '10px 12px', fontSize: 12, lineHeight: 1.5, overflow: 'auto', maxHeight: 420, whiteSpace: 'pre', background: 'var(--sunken)', borderRadius: 8, border: '1px solid var(--line)' }}
        >
          <DiffLines text={d.text} />
        </pre>
      )}
    </div>
  )
}

/** A field of an agent's instructions, to compare: its label and the text on each side. */
export interface Field {
  key: string
  label: string
  from: string
  to: string
}

/** The fields two versions of an agent's instructions differ in or share: the house instructions, each part of the built-in text, and the notes for each cluster either has. */
export function versionFields(from: Pick<HistoryText, 'house' | 'notes' | 'parts'>, to: Pick<HistoryText, 'house' | 'notes' | 'parts'>, clusterName: (id: string) => string = (id) => id): Field[] {
  const out: Field[] = [{ key: 'house', label: 'House instructions', from: from.house ?? '', to: to.house ?? '' }]
  const parts = [...(to.parts ?? []), ...(from.parts ?? []).filter((p) => !(to.parts ?? []).some((q) => q.name === p.name))]
  for (const p of parts) out.push({ key: `part:${p.name}`, label: p.label, from: from.parts?.find((q) => q.name === p.name)?.text ?? '', to: to.parts?.find((q) => q.name === p.name)?.text ?? '' })
  const keys = [...new Set([...Object.keys(from.notes ?? {}), ...Object.keys(to.notes ?? {})])].sort((a, b) => (a === '*' ? -1 : b === '*' ? 1 : a.localeCompare(b)))
  for (const k of keys) out.push({ key: `notes:${k}`, label: k === '*' ? 'Notes for every cluster' : `Notes for ${clusterName(k)}`, from: from.notes?.[k] ?? '', to: to.notes?.[k] ?? '' })
  return out
}

/** Two versions of an agent's instructions compared field by field; the fields that changed first, those that did not named after them. */
export function VersionDiff({ from, to, clusterName, testId }: { from: Pick<HistoryText, 'house' | 'notes' | 'parts'>; to: Pick<HistoryText, 'house' | 'notes' | 'parts'>; clusterName?: (id: string) => string; testId?: string }) {
  const fields = versionFields(from, to, clusterName)
  const changed = fields.filter((f) => f.from !== f.to)
  const same = fields.filter((f) => f.from === f.to)
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10, minWidth: 0 }} data-testid={testId}>
      {!changed.length && (
        <p className="rk-callout" data-tone="info" style={{ margin: 0, fontSize: 12.5 }} data-testid="version-diff-same">
          The two are the same.
        </p>
      )}
      {changed.map((f) => (
        <TextDiff key={f.key} label={f.label} from={f.from} to={f.to} testId="version-diff-field" />
      ))}
      {changed.length > 0 && same.length > 0 && (
        <p className="rk-fhint" style={{ margin: 0 }} data-testid="version-diff-unchanged">
          No change: {same.map((f) => f.label).join(', ')}.
        </p>
      )}
    </div>
  )
}
