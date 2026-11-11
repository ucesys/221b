// The console's error screen (Phase 12, CS-19): a page that failed to
// draw shows what went wrong instead of going blank, with Reload and, for
// anyone who may report, Report this problem, which opens the report page
// with the screen and the error filled in.
import { Component, type ErrorInfo, type ReactNode } from 'react'
import { errorSummary } from '../lib/reportLink'
import { ReportPageLink } from './aiops/ReportPageLink'

interface Props {
  /** The screen, as the report names it. */
  screen: string
  children: ReactNode
}

interface State {
  error: Error | null
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: unknown): State {
    return { error: error instanceof Error ? error : new Error(String(error)) }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('221B could not draw the page', error, info.componentStack)
  }

  render() {
    if (this.state.error) return <ErrorScreen screen={this.props.screen} error={this.state.error.message || 'An unknown error'} />
    return this.props.children
  }
}

export function ErrorScreen({ screen, error }: { screen: string; error: string }) {
  return (
    <main
      className="rk"
      style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 20, background: 'var(--bg)', color: 'var(--fg)', fontFamily: 'var(--font)', fontSize: 13 }}
      data-testid="error-screen"
    >
      <section className="rk-card" role="alert" style={{ maxWidth: 560, padding: '20px 24px', display: 'flex', flexDirection: 'column', gap: 10 }}>
        <h1 style={{ margin: 0, fontSize: 17, fontWeight: 600 }}>Something went wrong on this page</h1>
        <p style={{ margin: 0, color: 'var(--fg2)' }}>221B could not show {screen}. Reloading usually helps; if it happens again, tell the people who run 221B.</p>
        <pre className="rk-mono" style={{ margin: 0, padding: '8px 10px', borderRadius: 8, background: 'var(--surface2)', fontSize: 12, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
          {error}
        </pre>
        <span style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          <button className="rk-btn" data-on onClick={() => window.location.reload()}>
            Reload
          </button>
          <ReportPageLink text={() => errorSummary(screen, error)} testId="error-report">
            Report this problem ↗
          </ReportPageLink>
        </span>
      </section>
    </main>
  )
}
