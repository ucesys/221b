import { useEffect, useMemo, useState } from 'react'
import { endedWords, endSession, fBytes, fetchSessionHistory, fetchSessions, fetchToolbox, NODE_TOOLS, POD_TOOLS, tabLabel, toolLabel, transcriptHref, type SessionRecord, type SessionView, type ToolboxInfo } from '../../api/toolbox'
import { fmtTs } from '../../lib/format'
import type { AdminProps } from '../AdminPage'
import { ConfirmDialog } from '../ConfirmDialog'

function minutesBetween(a?: string, b?: string): string {
  if (!a || !b) return '—'
  const m = Math.max(0, Math.round((Date.parse(b) - Date.parse(a)) / 60_000))
  return m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${m % 60} min`
}

/**
 * Administration › Sessions (TB-44): the sessions open on the cluster, and
 * the history of those that ended, each with how it ended; a SuperAdmin
 * ends a live one with a reason its owner sees.
 */
export function AdminSessions({ route, cluster, onToast }: AdminProps) {
  const c = route.cluster
  const [live, setLive] = useState<SessionView[] | null>(null)
  const [history, setHistory] = useState<SessionRecord[] | null>(null)
  const [err, setErr] = useState('')
  const [q, setQ] = useState('')
  const [ending, setEnding] = useState<SessionView | null>(null)
  const [tick, setTick] = useState(0)
  useEffect(() => {
    let on = true
    Promise.all([fetchSessions(c), fetchSessionHistory(c)]).then(
      ([l, h]) => {
        if (!on) return
        setLive(l.filter((x) => x.state !== 'ended'))
        setHistory(h)
        setErr('')
      },
      (e: unknown) => on && setErr(e instanceof Error ? e.message : String(e)),
    )
    const t = setTimeout(() => setTick((n) => n + 1), 10_000)
    return () => {
      on = false
      clearTimeout(t)
    }
  }, [c, tick])
  const match = (s: { summary: string; owner: string; approver?: string; reason?: string }) => !q || [s.summary, s.owner, s.approver ?? '', s.reason ?? ''].join(' ').toLowerCase().includes(q.toLowerCase())
  const rows = useMemo(() => (history ?? []).filter(match), [history, q]) // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <section className="rk-card" style={{ padding: '16px 18px', display: 'flex', flexDirection: 'column', gap: 14 }} data-testid="admin-sessions">
      {ending && (
        <ConfirmDialogEnd
          s={ending}
          onDone={() => {
            setEnding(null)
            setTick((n) => n + 1)
          }}
          cluster={c}
          onToast={onToast}
        />
      )}
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 12 }}>
        <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>
          Sessions on <span className="rk-mono">{cluster?.name ?? c}</span>
        </h2>
        <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>Shells, commands, port forwards, debug containers and pods, node shells: who opened them, who approved them, how they ended.</span>
        <span className="rk-field" style={{ marginLeft: 'auto', width: 260 }}>
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Find a pod, node, person or reason" aria-label="Find a session" />
        </span>
      </div>
      {err && <p className="tb-err">{err}</p>}
      <h3 className="rk-h" style={{ margin: 0 }}>Open now</h3>
      {live === null ? (
        <p className="tb-hint">Loading…</p>
      ) : live.filter(match).length === 0 ? (
        <p className="tb-hint" style={{ margin: 0 }}>
          None.
        </p>
      ) : (
        <table className="tb-table">
          <thead>
            <tr>
              <th>Session</th>
              <th>Owner</th>
              <th>Approved by</th>
              <th>Started</th>
              <th>Ends</th>
              <th>Watched by</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {live.filter(match).map((s) => (
              <tr key={s.id}>
                <td>
                  <span className="rk-mono">{tabLabel(s)}</span>
                  <span className="tb-sub">{s.summary}</span>
                  {s.request.reason && <span className="tb-sub">“{s.request.reason}”</span>}
                </td>
                <td>{s.owner}</td>
                <td>{s.approver || '—'}</td>
                <td>{s.started ? fmtTs(s.started) : 'opening'}</td>
                <td>{s.ends ? fmtTs(s.ends) : '—'}</td>
                <td>{s.watchers?.join(', ') || '—'}</td>
                <td style={{ textAlign: 'right' }}>
                  <button className="rk-btn" data-danger="true" onClick={() => setEnding(s)}>
                    End…
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <h3 className="rk-h" style={{ margin: 0 }}>History</h3>
      {history === null ? (
        <p className="tb-hint">Loading…</p>
      ) : rows.length === 0 ? (
        <p className="tb-hint" style={{ margin: 0 }}>
          {q ? 'No session matches.' : 'No session has ended here yet.'}
        </p>
      ) : (
        <table className="tb-table" data-testid="admin-sessions-history">
          <thead>
            <tr>
              <th>Ended</th>
              <th>Session</th>
              <th>Owner</th>
              <th>Approved by</th>
              <th>Lasted</th>
              <th>How it ended</th>
              <th className="rk-num">Traffic</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id}>
                <td style={{ whiteSpace: 'nowrap' }}>{fmtTs(r.ended)}</td>
                <td>
                  {toolLabel(r.tool)}
                  <span className="tb-sub rk-mono">{r.node ? `node ${r.node}` : `${r.namespace}/${r.pod}${r.container ? ' · ' + r.container : ''}`}</span>
                  {r.reason && <span className="tb-sub">“{r.reason}”</span>}
                </td>
                <td>{r.owner}</td>
                <td>{r.approver || '—'}</td>
                <td>{minutesBetween(r.started || r.created, r.ended)}</td>
                <td>
                  {endedWords(r)}
                  {r.exit !== undefined && r.exit !== 0 && <span className="tb-sub">exit {r.exit}</span>}
                  {(r.watchers?.length ?? 0) > 0 && <span className="tb-sub">watched by {r.watchers!.join(', ')}</span>}
                </td>
                <td className="rk-num">{r.tool === 'forward' ? `${r.connections ?? 0} conn · ${fBytes(r.bytes ?? 0)}` : '—'}</td>
                <td>
                  {r.recorded && (
                    <a className="rk-btn" href={transcriptHref(c, r.id)} download>
                      Transcript
                    </a>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}

function ConfirmDialogEnd({ s, cluster, onDone, onToast }: { s: SessionView; cluster: string; onDone: () => void; onToast: (m: string) => void }) {
  const [why, setWhy] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  return (
    <ConfirmDialog
      title={`End ${s.owner}’s session?`}
      confirm="End it"
      danger
      busy={busy}
      error={err}
      onCancel={onDone}
      onConfirm={() => {
        if (why.trim().length < 5) return setErr('Say why: they see it.')
        setBusy(true)
        endSession(cluster, s.id, why.trim()).then(
          () => {
            onToast(`Ended ${s.owner}’s session.`)
            onDone()
          },
          (e: unknown) => {
            setBusy(false)
            setErr(e instanceof Error ? e.message : String(e))
          },
        )
      }}
    >
      <span className="rk-mono">{s.summary}</span>
      <textarea className="rk-area" value={why} onChange={(e) => setWhy(e.target.value)} aria-label="Why" placeholder="Why it ends: they see it" />
    </ConfirmDialog>
  )
}

/**
 * Administration › Toolbox (TB-46): what the chart allows on this cluster,
 * read-only: each tool and why it is off, the limits, the approval rule,
 * the images, and the values they come from.
 */
export function AdminToolbox({ route, cluster }: AdminProps) {
  const c = route.cluster
  const [info, setInfo] = useState<ToolboxInfo | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    let on = true
    fetchToolbox(c, [], true).then(
      (i) => on && setInfo(i),
      (e: unknown) => on && setErr(e instanceof Error ? e.message : String(e)),
    )
    return () => {
      on = false
    }
  }, [c])
  if (err) return <p className="tb-err">{err}</p>
  if (!info) return <p className="tb-hint">Loading…</p>
  const tools = [...POD_TOOLS, ...NODE_TOOLS]
  const who: Record<string, string> = {
    shell: info.values && (info.values as { adminExec?: boolean }).adminExec ? 'SuperAdmin; Admin in its namespaces' : 'SuperAdmin (Admin with rbac.roles.admin.exec)',
    command: info.values && (info.values as { adminExec?: boolean }).adminExec ? 'SuperAdmin; Admin in its namespaces' : 'SuperAdmin (Admin with rbac.roles.admin.exec)',
    forward: 'SuperAdmin; Admin in its namespaces',
    'debug-container': 'SuperAdmin',
    'debug-pod': 'SuperAdmin; Admin in its namespaces',
    'node-shell': 'SuperAdmin',
    'node-command': 'SuperAdmin',
  }
  return (
    <section className="rk-card" style={{ padding: '16px 18px', display: 'flex', flexDirection: 'column', gap: 14 }} data-testid="admin-toolbox">
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 12 }}>
        <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>
          Toolbox on <span className="rk-mono">{cluster?.name ?? c}</span>
        </h2>
        <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>From the chart’s toolbox values, read-only: a change is a helm upgrade. The roles decide who may use each tool.</span>
      </div>
      <div className="tb-stats">
        <div className="tb-stat">
          <span>Idle timeout</span>
          <b>{Math.round(info.idleSeconds / 60)} min</b>
        </div>
        <div className="tb-stat">
          <span>Hard limit</span>
          <b>{info.maxMinutes} min</b>
        </div>
        <div className="tb-stat">
          <span>Approval</span>
          <b style={{ fontSize: 13 }}>{info.twoPerson ? 'a second person' : 'self-approval'}</b>
        </div>
        <div className="tb-stat">
          <span>Node tools</span>
          <b style={{ fontSize: 13 }}>{info.nodeApproval === 'always' ? 'a second SuperAdmin, always' : 'as the cluster'}</b>
        </div>
        <div className="tb-stat">
          <span>Transcripts</span>
          <b style={{ fontSize: 13 }}>{info.transcripts === 'pvc' ? 'kept on a volume' : 'none'}</b>
        </div>
        <div className="tb-stat">
          <span>221b command</span>
          <b style={{ fontSize: 13 }}>{info.cliMinVersion ? `${info.cliMinVersion} or newer` : 'any'}</b>
        </div>
      </div>
      <table className="tb-table">
        <thead>
          <tr>
            <th>Tool</th>
            <th>Here</th>
            <th>Who may use it</th>
          </tr>
        </thead>
        <tbody>
          {tools.map((t) => (
            <tr key={t}>
              <td>{toolLabel(t)}</td>
              <td>
                <span className="rk-t" data-tone={info.off[t] ? 'muted' : 'ok'}>
                  {info.off[t] ? info.off[t] : 'on'}
                </span>
              </td>
              <td>{who[t]}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <h3 className="rk-h" style={{ margin: 0 }}>Images</h3>
      <table className="tb-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Image</th>
            <th>Adds where allowed</th>
            <th>Has</th>
            <th>Used by</th>
          </tr>
        </thead>
        <tbody>
          {info.images.map((im) => (
            <tr key={im.name}>
              <td>{im.name}</td>
              <td className="rk-mono" style={{ fontSize: 12 }}>
                {im.image}
                {!im.image.includes('@sha256:') && <span className="tb-sub">not pinned by digest</span>}
              </td>
              <td>{im.caps?.join(', ') || '—'}</td>
              <td>{im.tools || '—'}</td>
              <td>{[info.debugContainerImages.includes(im.name) && 'debug containers', info.debugPodProfiles.includes(im.name) && 'debug pods', (info.values as { nodeShell?: { image?: string } })?.nodeShell?.image === im.name && 'node shells'].filter(Boolean).join(', ') || '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="tb-hint" style={{ margin: 0 }}>
        Node shells run in the namespace <span className="rk-mono">{info.nodeNamespace}</span>, at Pod Security privileged, with the profiles {info.nodeProfiles.join(', ')}.
      </p>
      <details>
        <summary style={{ cursor: 'pointer', fontSize: 12.5 }}>The values</summary>
        <pre className="rk-code" style={{ marginTop: 8, maxHeight: 420 }}>
          {JSON.stringify(info.values, null, 2)}
        </pre>
      </details>
    </section>
  )
}
