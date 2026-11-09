import { useEffect, useState } from 'react'
import { deleteStanding, fetchStanding, saveStanding, type Standing } from '../api/cards'

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))
const list = (s: string) =>
  s
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean)

const blank: Standing = { name: '', on: true, agent: '', clusters: [], actions: [], namespaces: [], condition: '', perHour: 3 }

/** The form of one standing approval, new or changed. */
function StandingForm({ init, busy, error, onSave, onCancel }: { init: Standing; busy: boolean; error: string; onSave: (st: Standing) => void; onCancel: () => void }) {
  const [st, setSt] = useState(init)
  const [clusters, setClusters] = useState(init.clusters.join(', '))
  const [actions, setActions] = useState(init.actions.join(', '))
  const [namespaces, setNamespaces] = useState((init.namespaces ?? []).join(', '))
  return (
    <form
      className="rk-card"
      style={{ padding: 12, display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 10 }}
      onSubmit={(e) => {
        e.preventDefault()
        onSave({ ...st, clusters: list(clusters), actions: list(actions), namespaces: list(namespaces) })
      }}
      data-testid="standing-form"
    >
      <label className="rk-field" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <span className="rk-flabel">Name</span>
        <input value={st.name} maxLength={80} onChange={(e) => setSt({ ...st, name: e.target.value })} placeholder="Restart crash-looping pods in development" aria-label="Name" />
      </label>
      <label className="rk-field" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <span className="rk-flabel">Clusters (ids, never production)</span>
        <input value={clusters} onChange={(e) => setClusters(e.target.value)} placeholder="dev-1, staging" aria-label="Clusters" />
      </label>
      <label className="rk-field" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <span className="rk-flabel">Actions it approves</span>
        <input value={actions} onChange={(e) => setActions(e.target.value)} placeholder="kubectl rollout restart, kubectl delete pod" aria-label="Actions" />
      </label>
      <label className="rk-field" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <span className="rk-flabel">Namespaces (empty: any)</span>
        <input value={namespaces} onChange={(e) => setNamespaces(e.target.value)} placeholder="shop" aria-label="Namespaces" />
      </label>
      <label className="rk-field" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <span className="rk-flabel">Agent (empty: any)</span>
        <input value={st.agent ?? ''} onChange={(e) => setSt({ ...st, agent: e.target.value.trim() })} placeholder="troubleshooter" aria-label="Agent" />
      </label>
      <label className="rk-field" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <span className="rk-flabel">Only with the condition</span>
        <select value={st.condition ?? ''} onChange={(e) => setSt({ ...st, condition: e.target.value as Standing['condition'] })} aria-label="Condition">
          <option value="">any</option>
          <option value="alert-firing">an alert still firing</option>
          <option value="resource-version">an object unchanged</option>
        </select>
      </label>
      <label className="rk-field" style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <span className="rk-flabel">At most, an hour</span>
        <input type="number" min={1} max={60} value={st.perHour} onChange={(e) => setSt({ ...st, perHour: Number(e.target.value) })} aria-label="At most, an hour" />
      </label>
      <label className="rk-tcheck" style={{ alignSelf: 'end', color: 'var(--fg)' }}>
        <input type="checkbox" checked={st.on} onChange={(e) => setSt({ ...st, on: e.target.checked })} />
        <span>On</span>
      </label>
      <div style={{ gridColumn: '1 / -1', display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        {error && (
          <span className="tb-err" style={{ marginRight: 'auto' }} data-testid="standing-error">
            {error}
          </span>
        )}
        <span style={{ flex: '1 1 auto' }} />
        <button type="button" className="rk-btn" onClick={onCancel}>
          Cancel
        </button>
        <button type="submit" className="rk-btn" data-on disabled={busy} data-testid="standing-save">
          Save
        </button>
      </div>
    </form>
  )
}

/**
 * Standing approvals for agents' proposals (Phase 12, ADR-102), for a
 * SuperAdmin in Settings › Watson: a narrow class outside production that
 * is approved as it arrives, at most so many an hour, as the SuperAdmin
 * who set it. The audit log names it on every card it approves.
 */
export function StandingApprovals() {
  const [items, setItems] = useState<Standing[] | null>(null)
  const [err, setErr] = useState('')
  const [editing, setEditing] = useState<Standing | null>(null)
  const [busy, setBusy] = useState(false)
  const [formErr, setFormErr] = useState('')
  const [tick, setTick] = useState(0)
  useEffect(() => {
    let gone = false
    fetchStanding().then(
      (l) => !gone && setItems(l),
      (e: unknown) => !gone && setErr(errText(e)),
    )
    return () => {
      gone = true
    }
  }, [tick])
  const save = async (st: Standing) => {
    setBusy(true)
    setFormErr('')
    try {
      await saveStanding(st)
      setEditing(null)
      setTick((t) => t + 1)
    } catch (e) {
      setFormErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  const remove = async (id: string) => {
    try {
      await deleteStanding(id)
      setTick((t) => t + 1)
    } catch (e) {
      setErr(errText(e))
    }
  }
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 8 }} aria-label="Standing approvals for agents' proposals" data-testid="standing-approvals">
      <h3 className="rk-h">Standing approvals for proposed changes</h3>
      <p className="rk-fhint" style={{ margin: 0 }}>
        A narrow class of proposed change, approved as it arrives, outside production only: for example, restarting a crash-looping pod in development, at most 3 an hour. Watson checks each card again first, its changes run as you, and the audit log names the standing approval. Actions are a kubectl or helm command’s first words, such as kubectl rollout restart, or a console action: host.label, fleet.update, automation.params.
      </p>
      {err && <p className="tb-err">{err}</p>}
      {items && items.length === 0 && !editing && <p className="tb-hint" style={{ margin: 0 }}>None set.</p>}
      {items && items.length > 0 && (
        <table className="tb-table" data-testid="standing-list">
          <thead>
            <tr>
              <th>Name</th>
              <th>Clusters</th>
              <th>Actions</th>
              <th>At most</th>
              <th>Set by</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {items.map((st) => (
              <tr key={st.id} data-testid="standing-row">
                <td>
                  {st.name}
                  {!st.on && (
                    <span className="rk-tag" style={{ marginLeft: 6 }}>
                      off
                    </span>
                  )}
                  {st.agent && <div style={{ color: 'var(--fg3)', fontSize: 12 }}>{st.agent}’s proposed changes only</div>}
                </td>
                <td>{st.clusters.join(', ')}</td>
                <td className="rk-mono" style={{ fontSize: 12 }}>
                  {st.actions.join(', ')}
                  {st.namespaces && st.namespaces.length > 0 && <div>in {st.namespaces.join(', ')}</div>}
                </td>
                <td>{st.perHour} an hour</td>
                <td>{st.by?.name ?? '—'}</td>
                <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                  <button className="rk-btn" data-size="sm" onClick={() => (setFormErr(''), setEditing(st))}>
                    Edit
                  </button>{' '}
                  <button className="rk-btn" data-size="sm" onClick={() => void remove(st.id!)} data-testid="standing-remove">
                    Remove
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {editing ? (
        <StandingForm key={editing.id ?? 'new'} init={editing} busy={busy} error={formErr} onSave={(st) => void save(st)} onCancel={() => setEditing(null)} />
      ) : (
        <div>
          <button className="rk-btn" onClick={() => (setFormErr(''), setEditing({ ...blank }))} data-testid="standing-add">
            Add a standing approval
          </button>
        </div>
      )}
    </section>
  )
}
