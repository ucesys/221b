import { useState } from 'react'
import { deleteGroup, saveGroup, type Group, type ScriptSummary } from '../../api/automation'
import { Drawer } from '../Drawer'

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** A script group: its name and description, and what is in it. */
export function GroupDrawer({
  group,
  scripts,
  onClose,
  onSaved,
  onDeleted,
}: {
  /** null for a new group. */
  group: Group | null
  scripts: ScriptSummary[]
  onClose: () => void
  onSaved: (name: string) => void
  onDeleted: (name: string) => void
}) {
  const [name, setName] = useState(group?.name ?? '')
  const [desc, setDesc] = useState(group?.desc ?? '')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const inside = scripts.filter((s) => group && s.group === group.name)
  const run = (f: () => Promise<unknown>, done: () => void) => {
    setBusy(true)
    setErr('')
    f()
      .then(done)
      .catch((e: unknown) => setErr(errText(e)))
      .finally(() => setBusy(false))
  }
  return (
    <Drawer
      label="Group"
      kicker={group ? 'Script group' : 'New script group'}
      title={name.trim() || 'New group'}
      copy={false}
      width={560}
      onClose={onClose}
      testId="group-drawer"
      footer={
        <>
          {err && (
            <p className="tb-err" style={{ flex: '1 1 100%', margin: 0 }} data-testid="group-error">
              {err}
            </p>
          )}
          <button
            className="rk-btn"
            data-on
            onClick={() =>
              run(
                () => saveGroup(group?.name ?? null, { name, desc }),
                () => onSaved(name.trim()),
              )
            }
            disabled={busy || !name.trim()}
            data-testid="group-save"
          >
            {group ? 'Save' : 'Add group'}
          </button>
          <button className="rk-btn" onClick={onClose}>
            Cancel
          </button>
          {group && (
            <button
              className="rk-btn"
              data-danger="true"
              style={{ marginLeft: 'auto' }}
              onClick={() =>
                run(
                  () => deleteGroup(group.name),
                  () => onDeleted(group.name),
                )
              }
              disabled={busy || inside.length > 0}
              title={inside.length > 0 ? 'Move its scripts to another group first' : undefined}
              data-testid="group-delete"
            >
              Delete
            </button>
          )}
        </>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: 12.5 }}>Name</span>
          <span className="rk-field">
            <input value={name} maxLength={40} onChange={(e) => setName(e.target.value)} data-testid="group-name" />
          </span>
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: 12.5 }}>Description</span>
          <span className="rk-field">
            <input value={desc} maxLength={400} onChange={(e) => setDesc(e.target.value)} data-testid="group-desc" />
          </span>
        </label>
        {group && (
          <section data-testid="group-scripts">
            <h3 className="rk-h" style={{ margin: '0 0 6px' }}>
              Scripts {inside.length}
            </h3>
            {inside.length === 0 ? (
              <p className="tb-hint" style={{ margin: 0 }}>
                No scripts: this group can be deleted.
              </p>
            ) : (
              <>
                <p className="tb-hint" style={{ margin: '0 0 6px' }}>
                  A group with scripts cannot be deleted: move them first, in each script&apos;s Group box. Renaming keeps them here.
                </p>
                <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5 }}>
                  {inside.map((s) => (
                    <li key={s.name} className="rk-mono">
                      {s.name}
                    </li>
                  ))}
                </ul>
              </>
            )}
          </section>
        )}
      </div>
    </Drawer>
  )
}
