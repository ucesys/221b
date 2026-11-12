import { useCallback, useEffect, useState } from 'react'
import { decideVariableChange, fetchVariableChanges, type ImpactItem, type VariableChange } from '../../api/automation'
import { fmtTs } from '../../lib/format'
import { valueChanges } from '../../lib/variables'
import { DecideDialog } from './DecideDialog'

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** What a parameter change touches, and whether each needs a person. */
export function ImpactList({ items }: { items: ImpactItem[] }) {
  if (items.length === 0)
    return (
      <p className="tb-hint" style={{ margin: 0 }}>
        No approved script reads these values.
      </p>
    )
  return (
    <table className="tb-table" data-testid="impact-list">
      <thead>
        <tr>
          <th>Script</th>
          <th>Decision</th>
          <th>Why</th>
        </tr>
      </thead>
      <tbody>
        {items.map((i) => (
          <tr key={`${i.script}-${i.version}`}>
            <td className="rk-mono">
              {i.script}
              {i.version ? ` v${i.version}` : ''}
            </td>
            <td>
              <span className="rk-pill" data-tone={i.decision === 'approval' ? 'warn' : 'ok'}>
                {i.decision === 'approval' ? 'Needs approval' : 'Safe'}
              </span>
              {i.by === 'sherlock' && <span style={{ marginLeft: 6, fontSize: 12 }}>Sherlock</span>}
            </td>
            <td>{i.why}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

/** The values a change alters, before and after. */
function ValueChanges({ change }: { change: VariableChange }) {
  const list = valueChanges(change.before, change.after)
  if (list.length === 0) return null
  const cell = (v?: string) => (v === undefined ? <span style={{ color: 'var(--fg3)' }}>not set</span> : v)
  return (
    <table className="tb-table" data-testid="change-values">
      <thead>
        <tr>
          <th>Variable</th>
          <th>Now</th>
          <th>After</th>
        </tr>
      </thead>
      <tbody>
        {list.map((c) => (
          <tr key={c.what}>
            <td className="rk-mono">{c.what}</td>
            <td className="rk-mono">{cell(c.before)}</td>
            <td className="rk-mono">{cell(c.after)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

/**
 * A fleet's variable changes that wait for a second person, to decide here.
 * What was decided is in the audit log (automation.params).
 */
export function VariableChangesPanel({
  cluster,
  fleet,
  version,
  onDecided,
}: {
  cluster: string
  fleet: string
  /** Bumped after a save, so the list reads again. */
  version: number
  onDecided: () => void
}) {
  const [list, setList] = useState<VariableChange[]>([])
  const [err, setErr] = useState('')
  // The change an Approve or Reject asks about first.
  const [deciding, setDeciding] = useState<{
    c: VariableChange
    how: 'approve' | 'reject'
  } | null>(null)
  const [busy, setBusy] = useState(false)
  const [decideErr, setDecideErr] = useState('')
  const load = useCallback(
    () =>
      fetchVariableChanges(cluster).then(
        (c) => setList(c.changes),
        (e: unknown) => setErr(errText(e)),
      ),
    [cluster],
  )
  useEffect(() => {
    void Promise.resolve().then(load)
  }, [load, version])
  const shown = list.filter((c) => c.fleet === fleet && c.state === 'waiting')
  if (shown.length === 0 && !err) return null
  return (
    <section
      className="rk-card"
      style={{
        padding: '12px 16px',
        display: 'flex',
        flexDirection: 'column',
        gap: 10,
      }}
      aria-label="Changes waiting for approval"
      data-testid="param-changes"
    >
      <h3 className="rk-h" style={{ margin: 0 }}>
        Waiting for approval
      </h3>
      {err && <p className="tb-err">{err}</p>}
      {shown.map((c) => (
        <div key={c.id} style={{ display: 'flex', flexDirection: 'column', gap: 6 }} data-testid="param-change" data-state={c.state}>
          <div style={{ fontSize: 12.5 }}>
            <b style={{ fontWeight: 600 }}>#{c.id}</b> {c.changed.join(', ')} · {c.state} · {c.by.name}, {fmtTs(c.at)}
            {c.reason ? ` · ${c.reason}` : ''}
          </div>
          {c.note && (
            <p className="tb-hint" style={{ margin: 0 }}>
              {c.note}
            </p>
          )}
          {c.state === 'waiting' && (
            <>
              <p className="tb-hint" style={{ margin: 0 }}>
                The current values stay in effect, and the versions that need approval are suspended on this fleet until it is decided.
              </p>
              <ValueChanges change={c} />
              <ImpactList items={c.impact} />
              {c.overtaken && (
                <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} data-testid="change-overtaken">
                  {c.overtaken}
                </p>
              )}
              <div style={{ display: 'flex', gap: 8 }}>
                <button className="rk-btn" data-on disabled={!!c.overtaken} title={c.overtaken || undefined} onClick={() => setDeciding({ c, how: 'approve' })} data-testid="change-approve">
                  Approve the change…
                </button>
                <button className="rk-btn" data-danger="true" onClick={() => setDeciding({ c, how: 'reject' })} data-testid="change-reject">
                  Reject…
                </button>
              </div>
            </>
          )}
        </div>
      ))}
      {deciding && (
        <DecideDialog
          how={deciding.how}
          what={`change #${deciding.c.id}`}
          busy={busy}
          error={decideErr}
          onCancel={() => {
            setDeciding(null)
            setDecideErr('')
          }}
          onConfirm={(comment) => {
            setBusy(true)
            setDecideErr('')
            decideVariableChange(cluster, deciding.c.id, deciding.how, comment)
              .then(() => {
                setDeciding(null)
                onDecided()
                return load()
              })
              .catch((e: unknown) => setDecideErr(errText(e)))
              .finally(() => setBusy(false))
          }}
        >
          {deciding.how === 'approve'
            ? `The new values of ${deciding.c.changed.join(', ')} take effect on this fleet at once, and the versions it suspended run again.`
            : 'The current values stay in effect, and the change is closed; whoever asked for it reads your reason.'}
        </DecideDialog>
      )}
    </section>
  )
}
