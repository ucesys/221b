import { useEffect, useState } from 'react'
import type { TestResult } from '../api/clusters'

interface Props {
  result: TestResult | null
  running: boolean
}

/**
 * A connection test's five steps (P3-06): address, TLS, sign-in,
 * Kubernetes and permissions, each with its outcome, then the verdict.
 * The server runs them in order and answers once; they are revealed one
 * at a time so a failure reads at the step it happened.
 */
export function TestSteps({ result, running }: Props) {
  // How many steps of this result are shown so far; a new result starts over.
  const [reveal, setReveal] = useState<{ of: TestResult | null; n: number }>({ of: result, n: result ? 1 : 0 })
  if (reveal.of !== result) setReveal({ of: result, n: result ? 1 : 0 })
  useEffect(() => {
    if (!result) return
    const t = setInterval(() => {
      setReveal((r) => {
        if (r.of !== result || r.n >= result.steps.length) {
          clearInterval(t)
          return r
        }
        return { of: result, n: r.n + 1 }
      })
    }, 180)
    return () => clearInterval(t)
  }, [result])
  const shown = reveal.of === result ? reveal.n : 0
  const steps = result ? result.steps.slice(0, shown) : []
  const done = !!result && shown >= result.steps.length
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }} data-testid="test-steps" data-done={done ? 'true' : 'false'}>
      <ol className="rk-csteps" aria-live="polite">
        {steps.map((s) => (
          <li key={s.k} data-tone={s.tone} data-testid="test-step" data-step={s.k}>
            <span className="rk-t" data-tone={s.tone} style={{ display: 'inline-flex' }}>
              <span className="rk-dot" />
            </span>
            <span className="rk-csk">{s.k}</span>
            <span className="rk-cst">{s.t}</span>
          </li>
        ))}
        {(running || (result && !done)) && (
          <li data-tone="muted">
            <span className="rk-t" data-tone="muted" style={{ display: 'inline-flex' }}>
              <span className="rk-dot" />
            </span>
            <span className="rk-csk">{running ? 'Testing' : '…'}</span>
            <span className="rk-cst rk-dots" aria-label="in progress">
              <i>.</i>
              <i>.</i>
              <i>.</i>
            </span>
          </li>
        )}
      </ol>
      {done && result && (
        <strong className="rk-t" data-tone={result.tone} style={{ fontSize: 12.5, fontWeight: 600 }} data-testid="test-summary">
          {result.summary}
        </strong>
      )}
    </div>
  )
}
