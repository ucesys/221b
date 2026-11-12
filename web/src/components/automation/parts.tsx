// What the Automation pages share: small pieces of their tables and forms.
import { type ReactNode } from 'react'
import { type Finding, type RolloutMode, type Tone } from '../../api/automation'

export const ROLLOUTS: [RolloutMode, string][] = [
  ['canary', 'Canary first'],
  ['size', 'Batches'],
  ['all', 'All at once'],
]

export const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

export function Pill({ tone, children, testId }: { tone?: Tone; children: ReactNode; testId?: string }) {
  return (
    <span className="rk-pill" data-tone={tone} data-testid={testId}>
      {children}
    </span>
  )
}

export function Field({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <label className="rk-flabel">
      {label}
      {children}
      {hint && <span className="rk-fhint">{hint}</span>}
    </label>
  )
}

export function Findings({ list, onLine }: { list: Finding[] | null | undefined; onLine?: (line: number) => void }) {
  if (!list?.length)
    return (
      <p className="tb-hint" style={{ margin: 0 }} data-testid="auto-findings-none">
        No findings: the header is complete and no check fired.
      </p>
    )
  return (
    <ul
      style={{
        margin: 0,
        paddingLeft: 0,
        listStyle: 'none',
        display: 'flex',
        flexDirection: 'column',
        gap: 4,
        fontSize: 12.5,
      }}
      data-testid="auto-findings"
    >
      {list.map((f, i) => (
        <li key={i} style={{ display: 'flex', gap: 8, alignItems: 'baseline' }}>
          <Pill tone={f.level === 'error' ? 'err' : 'warn'}>{f.level}</Pill>
          {onLine ? (
            <button className="rk-link rk-mono" style={{ minWidth: 48, textAlign: 'left' }} onClick={() => onLine(f.line || 1)} data-testid="finding-line">
              {f.line > 0 ? `line ${f.line}` : 'header'}
            </button>
          ) : (
            <span className="rk-mono" style={{ color: 'var(--fg3)', minWidth: 48 }}>
              {f.line > 0 ? `line ${f.line}` : 'header'}
            </span>
          )}
          <span>{f.msg}</span>
        </li>
      ))}
    </ul>
  )
}
