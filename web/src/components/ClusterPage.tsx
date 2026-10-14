import { useState } from 'react'
import { ageText, AUTH_LABELS, CA_LABELS, ClusterError, credText, envOf, expiryText, stateOf, testCluster, useClusters, type ClusterSummary, type TestResult } from '../api/clusters'
import type { Me } from '../api/types'
import { fmtTs } from '../lib/format'
import { clusterHref, LOCAL, type Route } from '../lib/router'
import { ErrorIcon, WarnIcon } from './Icons'
import { TestSteps } from './TestSteps'

interface Props {
  route: Route
  me: Me | null
  now: number
  onEdit: (id: string) => void
}

/** The facts the page lists about a connection. */
export function clusterFacts(c: ClusterSummary, now: number): [string, string, string?][] {
  const facts: [string, string, string?][] = [['API server', c.server]]
  facts.push(['Sign-in', AUTH_LABELS[c.auth] + '\n' + credText(c, now)])
  facts.push(['Certificate', c.ca === 'pem' ? 'Cluster CA certificate' + (c.caInfo ? ` (${c.caInfo})` : '') : CA_LABELS[c.ca]])
  facts.push(['Access', c.mode === 'write' ? 'Read and change' : 'Read only'])
  const k8s = c.version ? c.version + (c.synced ? `, ${c.nodes} node${c.nodes === 1 ? '' : 's'}, ${c.pods} pod${c.pods === 1 ? '' : 's'}` : '') + (c.state === 'connected' ? '' : ' (when it last answered)') : 'Not known yet'
  facts.push(['Kubernetes', k8s])
  facts.push(['Last tested', c.lastTested ? ageText(new Date(c.lastTested).getTime(), now) : 'Not yet', c.lastTested ? fmtTs(c.lastTested) : undefined])
  facts.push(['Added', ageText(new Date(c.added).getTime(), now), fmtTs(c.added)])
  if (c.updated && c.updated !== c.added) facts.push(['Changed', ageText(new Date(c.updated).getTime(), now), fmtTs(c.updated)])
  return facts
}

/**
 * A cluster's own page (P3-07): its connection as the console sees it,
 * with the state, the last test and the way to fix it. Everyone can read
 * it; administrators edit and test from here.
 */
export function ClusterPage({ route, me, now, onEdit }: Props) {
  const { clusters, loaded, error } = useClusters()
  const c = clusters.find((x) => x.id === route.cluster)
  const [test, setTest] = useState<TestResult | null>(null)
  const [testing, setTesting] = useState(false)
  const [testErr, setTestErr] = useState('')
  const admin = me?.admin ?? false

  const retest = async () => {
    if (!c) return
    setTesting(true)
    setTest(null)
    setTestErr('')
    try {
      setTest(await testCluster(c.id))
    } catch (e) {
      setTestErr(e instanceof ClusterError ? e.message : String(e))
    } finally {
      setTesting(false)
    }
  }

  if (!c) {
    return (
      <div className="rk-main" style={{ flex: '1 1 auto', display: 'flex', justifyContent: 'center', alignItems: 'flex-start', padding: '32px 24px 48px', overflow: 'auto', background: 'var(--bg)' }}>
        <section className="rk-card" style={{ width: 'min(760px, 100%)', padding: '28px 24px', display: 'flex', flexDirection: 'column', alignItems: 'flex-start', gap: 10 }} data-testid="cluster-missing">
          <h1 style={{ margin: 0, fontSize: 18, fontWeight: 600 }}>{loaded ? `No cluster named “${route.cluster}”` : 'Loading…'}</h1>
          {loaded && <p style={{ margin: 0, maxWidth: '60ch', fontSize: 13, color: 'var(--fg2)' }}>{error ? error : 'It may have been removed, or the link may be from another console.'}</p>}
          {loaded && (
            <a className="rk-btn" href={clusterHref(LOCAL, 'nodes')} style={{ height: 32 }}>
              Back to the console’s own cluster
            </a>
          )}
        </section>
      </div>
    )
  }

  const env = envOf(c.env)
  const s = stateOf(c)
  const facts = clusterFacts(c, now)
  const problem = c.state === 'failed' || c.state === 'disconnected' ? c.message || 'The API server does not answer.' : ''
  const expiry = c.cred.expires ? expiryText(c.cred, now) : ''
  const shownTest = test ?? c.lastTest ?? null

  return (
    <div className="rk-main" style={{ flex: '1 1 auto', display: 'flex', justifyContent: 'center', alignItems: 'flex-start', padding: '32px 24px 48px', overflow: 'auto', background: 'var(--bg)' }}>
      <div style={{ width: 'min(760px, 100%)', display: 'flex', flexDirection: 'column', gap: 14 }}>
        <section className="rk-card" aria-label={`Cluster ${c.name}`} style={{ padding: '18px 20px', display: 'flex', flexDirection: 'column', gap: 14 }} data-testid="cluster-page">
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 12px' }}>
            <h1 className="rk-mono" style={{ margin: 0, fontSize: 19, fontWeight: 600, overflowWrap: 'anywhere' }}>
              {c.name}
            </h1>
            <span className="rk-tag" data-tone={env.tone}>
              {env.label}
            </span>
            <span className="rk-pill" data-tone={s.tone} data-testid="cluster-page-state">
              <span className="rk-dot" />
              {s.text}
            </span>
            <span style={{ flex: '1 1 auto' }} />
            {admin && (
              <>
                <button className="rk-btn" onClick={() => void retest()} disabled={testing} style={{ height: 32 }} data-testid="cluster-retest">
                  {testing ? 'Testing…' : 'Test again'}
                </button>
                <button className="rk-btn" data-on="true" onClick={() => onEdit(c.id)} style={{ height: 32 }} data-testid="cluster-edit">
                  Edit connection
                </button>
              </>
            )}
          </div>
          {c.description && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }}>{c.description}</p>}
          {problem && (
            <div className="rk-callout" data-tone="err" data-testid="cluster-problem">
              <ErrorIcon />
              <div style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }}>
                <strong style={{ fontSize: 12.5, fontWeight: 600 }}>The console can’t connect</strong>
                <span style={{ fontSize: 12.5, color: 'var(--fg2)', overflowWrap: 'anywhere' }}>{problem}</span>
                {admin && (
                  <button className="rk-link" onClick={() => onEdit(c.id)} style={{ alignSelf: 'flex-start', fontSize: 12 }} data-testid="cluster-fix">
                    Fix the connection
                  </button>
                )}
              </div>
            </div>
          )}
          {(c.expired || c.expiring) && (
            <div className="rk-callout" data-tone={c.expired ? 'err' : 'warn'} data-testid="cluster-expiry">
              <WarnIcon />
              <div style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }}>
                <strong style={{ fontSize: 12.5, fontWeight: 600 }}>{c.expired ? 'The console’s credentials for this cluster have expired' : 'The console’s credentials for this cluster expire soon'}</strong>
                <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>
                  {credText(c, now)}. {c.expired ? 'The connection will fail or has failed; replace the credentials.' : `Replace them before they do (${expiry}).`}
                </span>
                {admin && (
                  <button className="rk-link" onClick={() => onEdit(c.id)} style={{ alignSelf: 'flex-start', fontSize: 12 }} data-testid="cluster-fix-expiry">
                    Fix the connection
                  </button>
                )}
              </div>
            </div>
          )}
          <dl className="rk-kv">
            {facts.map(([k, v, tip]) => (
              <div key={k} style={{ display: 'contents' }}>
                <dt>{k}</dt>
                <dd className={k === 'API server' ? 'rk-mono' : undefined} title={tip} style={{ whiteSpace: 'pre-line', overflowWrap: 'anywhere' }}>
                  {v}
                </dd>
              </div>
            ))}
          </dl>
        </section>
        {(shownTest || testing || testErr) && (
          <section className="rk-card" aria-label="Connection test" style={{ padding: '14px 16px', display: 'flex', flexDirection: 'column', gap: 10 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <h3 className="rk-h">Connection test</h3>
              {!testing && (c.lastTested || test) && <span style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{test ? 'just now' : ageText(new Date(c.lastTested!).getTime(), now)}</span>}
            </div>
            {testErr && (
              <span className="rk-t" data-tone="err" role="alert" style={{ fontSize: 12 }}>
                {testErr}
              </span>
            )}
            {(shownTest || testing) && <TestSteps result={testing ? null : shownTest} running={testing} />}
          </section>
        )}
        {c.local && <p style={{ margin: 0, fontSize: 11.5, color: 'var(--fg3)' }}>This is the cluster the console runs in: its connection comes from the installation. Only the name, environment and description can be changed here.</p>}
      </div>
    </div>
  )
}
