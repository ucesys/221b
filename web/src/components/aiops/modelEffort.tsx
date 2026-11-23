// The Model and Effort columns of AIOps › Jobs (the owner, November 23):
// on the Schedule table, each job's agent's model and effort as set now;
// on the Runs table, what each run ran with (AI-48).
import { EFFORTS, modelLabel } from '../../api/ai'
import type { AgentSetup, RunBrief } from '../../api/jobs'

/** A cell's words, and the smaller line under them. */
export interface Said {
  text: string
  hint?: string
}

const effortWords = (e?: string) => EFFORTS.find((x) => x.id === (e ?? ''))?.label ?? e ?? 'Default'

/** A job's agent's model as set now: "Claude Opus 5.5", "Default (Claude Opus 5.5)", "Auto" from its lowest to its highest model; on its provider. */
export function setupModel(s?: AgentSetup): Said {
  if (!s?.model) return { text: '—' }
  const on = s.provider ? `on ${s.provider}` : undefined
  if (s.model === 'auto') {
    const range = s.autoFrom && s.autoTo && s.autoFrom !== s.autoTo ? `${modelLabel(s.autoFrom)} to ${modelLabel(s.autoTo)}` : modelLabel(s.autoFrom || s.autoTo)
    return { text: range ? `Auto (${range})` : 'Auto', hint: on }
  }
  if (s.defaultModel) return { text: `Default (${modelLabel(s.model)})`, hint: on }
  return { text: modelLabel(s.model), hint: on }
}

/** A job's agent's effort as set now: Default, a level, or Auto at least its floor. */
export function setupEffort(s?: AgentSetup): Said {
  if (!s) return { text: '—' }
  if (s.effort === 'auto') return { text: 'Auto', hint: s.floorEffort ? `at least ${effortWords(s.floorEffort).toLowerCase()}` : undefined }
  return { text: effortWords(s.effort) }
}

/** The model a run ran on, on its provider, and the model it moved up from; "—" for one that ran none. */
export function runModel(r: Pick<RunBrief, 'model' | 'provider' | 'movedUp'>): Said {
  if (!r.model) return { text: '—' }
  const hint = [r.provider ? `on ${r.provider}` : '', r.movedUp ? `moved up from ${modelLabel(r.movedUp.from)}` : ''].filter(Boolean).join(' · ')
  return { text: modelLabel(r.model), hint: hint || undefined }
}

/** The highest effort a run's steps ran at, with Auto's reason; "—" for a run from before 0.14.0, which kept none. */
export function runEffort(r: Pick<RunBrief, 'effort' | 'pickBy' | 'pickWhy'>): Said {
  if (!r.effort) return { text: '—' }
  return { text: effortWords(r.effort), hint: r.pickBy ? (r.pickWhy ? `Auto: ${r.pickWhy}` : 'Auto') : undefined }
}

/** A cell of words with its line under them. */
export function SaidCell({ said, testId }: { said: Said; testId?: string }) {
  return (
    <span data-testid={testId}>
      <span style={{ color: said.text === '—' ? 'var(--fg3)' : undefined }}>{said.text}</span>
      {said.hint && (
        <span className="rk-fhint" style={{ display: 'block', fontSize: 11 }}>
          {said.hint}
        </span>
      )}
    </span>
  )
}

/** The words of a cell as one line, for the export. */
export const saidText = (s: Said) => (s.text === '—' ? '' : s.hint ? `${s.text} (${s.hint})` : s.text)
