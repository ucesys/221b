import { useCallback, useEffect, useState } from 'react'
import { deleteProfile, fetchProfiles, ceilingText, fetchFleets, saveProfile, type AutomationFleets, type RunProfile } from '../../api/automation'
import { ConfirmDialog } from '../ConfirmDialog'
import { Drawer } from '../Drawer'

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

const BLANK: RunProfile = {
  id: '',
  name: '',
  kind: 'ssh',
  privilege: 'none',
}

/**
 * The Run Profiles page (Auto-1, second step, then Configurations): a
 * script declares the privilege it needs, and its run profile supplies it,
 * over SSH for nodes or as a service account on a cluster.
 */
export function RunProfilesSection({ cluster, onToast }: { cluster: string; onToast: (msg: string) => void }) {
  const [profiles, setProfiles] = useState<RunProfile[] | null>(null)
  const [fleets, setFleets] = useState<AutomationFleets | null>(null)
  const [err, setErr] = useState('')
  const [editing, setEditing] = useState<RunProfile | null>(null)
  const [isNew, setIsNew] = useState(false)
  const [removing, setRemoving] = useState<RunProfile | null>(null)

  const load = useCallback(
    () =>
      Promise.all([fetchProfiles(), fetchFleets(cluster)]).then(
        ([c, f]) => {
          setProfiles(c.profiles)
          setFleets(f)
          setErr('')
        },
        (e: unknown) => setErr(errText(e)),
      ),
    [cluster],
  )
  useEffect(() => {
    void Promise.resolve().then(load)
  }, [load])

  const fleetNames = (fleets?.fleets ?? []).filter((f) => f.id !== '_all')
  return (
    <>
      <p className="tb-hint" style={{ margin: 0, maxWidth: '90ch' }}>
        A run profile is who a script runs as. A script declares the privilege it needs, and its profile supplies it: over SSH for nodes, or in Kubernetes as a service account or the person. A run is refused if the profile gives less than
        the script declares, or more than the fleet allows. Dry runs always run as nobody. Watson executes every run, and each command is audited in its name.
      </p>
      {fleets && (
        <section className="rk-card" style={{ padding: '12px 16px' }} aria-label="Fleet ceilings" data-testid="config-ceilings">
          <h3 className="rk-h" style={{ margin: '0 0 6px' }}>
            Fleet ceilings
          </h3>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: '4px 16px' }}>
            {fleets.fleets.map((f) => (
              <span key={f.id} style={{ fontSize: 12.5 }}>
                {f.name}: <b>{ceilingText(f.params?.ceiling)}</b>
              </span>
            ))}
          </div>
        </section>
      )}
      {err && <p className="tb-err">{err}</p>}
      <div>
        <button
          className="rk-btn"
          data-on
          onClick={() => {
            setEditing({ ...BLANK })
            setIsNew(true)
          }}
          data-testid="config-new"
        >
          New run profile
        </button>
      </div>
      {profiles && (
        <table className="tb-table" data-testid="profiles-table">
          <thead>
            <tr>
              <th>Run profile</th>
              <th>Kind</th>
              <th>Privilege</th>
              <th>Fleets</th>
              <th>Runs, 7 days</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {profiles.map((c) => (
              <tr key={c.id} data-testid="config-row" data-id={c.id}>
                <td>
                  <span className="rk-mono">{c.name}</span>
                  {c.desc && <div style={{ color: 'var(--fg3)', fontSize: 12 }}>{c.desc}</div>}
                </td>
                <td className="rk-mono" data-testid="config-kind">
                  {c.kind === 'kubernetes' ? 'k8s' : 'ssh'}
                </td>
                <td>
                  {c.kind === 'kubernetes' ? (
                    <>
                      Kubernetes RBAC of{' '}
                      {c.serviceAccount ? (
                        <span className="rk-mono">
                          {c.namespace}/{c.serviceAccount}
                        </span>
                      ) : (
                        'the person'
                      )}
                    </>
                  ) : c.privilege === 'none' ? (
                    'nobody: read-only'
                  ) : (
                    'elevated'
                  )}
                </td>
                <td>{c.fleets?.length ? c.fleets.join(', ') : 'all'}</td>
                <td>{c.runs ?? 0}</td>
                <td style={{ whiteSpace: 'nowrap', textAlign: 'right' }}>
                  {!c.builtin && (
                    <>
                      <button
                        className="rk-btn"
                        onClick={() => {
                          setEditing(c)
                          setIsNew(false)
                        }}
                        data-testid="config-edit"
                      >
                        Edit
                      </button>{' '}
                      <button className="rk-btn" data-danger="true" onClick={() => setRemoving(c)} data-testid="config-delete">
                        Delete
                      </button>
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {editing && (
        <ProfileDrawer
          config={editing}
          isNew={isNew}
          fleets={fleetNames.map((f) => f.id)}
          onClose={() => setEditing(null)}
          onSaved={(name) => {
            setEditing(null)
            onToast(`${name} is saved.`)
            void load()
          }}
        />
      )}
      {removing && (
        <ConfirmDialog
          title={`Delete ${removing.name}?`}
          confirm="Delete"
          danger
          onCancel={() => setRemoving(null)}
          onConfirm={() => {
            deleteProfile(removing.id)
              .then(() => {
                setRemoving(null)
                void load()
              })
              .catch((e: unknown) => setErr(errText(e)))
          }}
        >
          <p style={{ margin: 0 }}>Runs that used it keep its name. New runs cannot choose it.</p>
        </ConfirmDialog>
      )}
    </>
  )
}

function ProfileDrawer({ config, isNew, fleets, onClose, onSaved }: { config: RunProfile; isNew: boolean; fleets: string[]; onClose: () => void; onSaved: (name: string) => void }) {
  const [c, setC] = useState(config)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const save = () => {
    setBusy(true)
    setErr('')
    saveProfile(c)
      .then(() => onSaved(c.name))
      .catch((e: unknown) => setErr(errText(e)))
      .finally(() => setBusy(false))
  }
  const box = (label: string, value: string | undefined, set: (v: string) => void, testId: string, readOnly = false) => (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <span style={{ fontSize: 12.5 }}>{label}</span>
      <span className="rk-field">
        <input value={value ?? ''} readOnly={readOnly} onChange={(e) => set(e.target.value)} data-testid={testId} />
      </span>
    </label>
  )
  return (
    <Drawer
      label="Run profile"
      kicker="Run profile"
      title={c.name || 'New run profile'}
      copy={false}
      width={560}
      onClose={onClose}
      testId="config-drawer"
      footer={
        <>
          {err && (
            <p className="tb-err" style={{ flex: '1 1 100%', margin: 0 }}>
              {err}
            </p>
          )}
          <button className="rk-btn" data-on onClick={save} disabled={busy || !c.id || !c.name} data-testid="config-save">
            Save
          </button>
          <button className="rk-btn" onClick={onClose}>
            Cancel
          </button>
        </>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        {box('Id', c.id, (id) => setC({ ...c, id: id.trim() }), 'config-id', !isNew)}
        {box('Name', c.name, (name) => setC({ ...c, name }), 'config-name')}
        {box('Description', c.desc, (desc) => setC({ ...c, desc }), 'config-desc')}
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: 12.5 }}>Kind</span>
          <span className="rk-field">
            <select value={c.kind} onChange={(e) => setC({ ...c, kind: e.target.value as RunProfile['kind'] })} data-testid="config-kind">
              <option value="ssh">SSH: reaches nodes</option>
              <option value="kubernetes">Kubernetes: a service account</option>
            </select>
          </span>
        </label>
        {c.kind === 'ssh' ? (
          <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            <span style={{ fontSize: 12.5 }}>Privilege</span>
            <span className="rk-field">
              <select
                value={c.privilege ?? 'none'}
                onChange={(e) =>
                  setC({
                    ...c,
                    privilege: e.target.value as RunProfile['privilege'],
                  })
                }
                data-testid="config-privilege"
              >
                <option value="none">nobody: read-only</option>
                <option value="full">elevated: the ssh-paired account</option>
              </select>
            </span>
          </label>
        ) : (
          <>
            {box('Namespace', c.namespace, (namespace) => setC({ ...c, namespace }), 'config-namespace')}
            {box('Service account', c.serviceAccount, (serviceAccount) => setC({ ...c, serviceAccount }), 'config-sa')}
            <p className="tb-hint" style={{ margin: 0 }}>
              Whoever starts the run needs impersonate on it in Kubernetes, and the chart lists it under automation.runIdentities.
            </p>
          </>
        )}
        {c.kind === 'ssh' && fleets.length > 0 && (
          <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
            <legend style={{ fontSize: 12.5, marginBottom: 4 }}>Fleets, none for every fleet</legend>
            {fleets.map((f) => (
              <label key={f} style={{ display: 'flex', gap: 6, fontSize: 12.5 }}>
                <input
                  type="checkbox"
                  checked={(c.fleets ?? []).includes(f)}
                  onChange={(e) =>
                    setC({
                      ...c,
                      fleets: e.target.checked ? [...(c.fleets ?? []), f] : (c.fleets ?? []).filter((x) => x !== f),
                    })
                  }
                />
                {f}
              </label>
            ))}
          </fieldset>
        )}
      </div>
    </Drawer>
  )
}
