import { useState } from 'react'
import { changeGroups, type LocalGroup, type UserRow } from '../api/admin'
import { afterJoining, groupChoice, ROLE_NAME } from '../lib/admin'
import { Card } from './Drawer'
import { ConfirmDialog } from './ConfirmDialog'
import { Select } from './Select'

interface Props {
  /** The local users who may be added; LDAP people get their groups from the directory (RB-43). */
  users: UserRow[]
  groups: LocalGroup[]
  /** The user or the group the form opened from; the other is picked. */
  user?: string
  group?: string
  onClose: () => void
  onDone: (message: string) => void
}

/** What the person can do afterwards, as the form and the confirmation say it. */
function Result({ user, group, groups }: { user: UserRow; group: string; groups: LocalGroup[] }) {
  const rows = afterJoining(user, group ? [group] : [], groups)
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }} data-testid="after-joining">
      <span style={{ fontSize: 12, color: 'var(--fg3)' }}>After this, {user.name || user.username} can</span>
      {rows.length === 0 && <span style={{ color: 'var(--fg3)' }}>Nothing yet beyond the nodes.</span>}
      {rows.map((h, i) => (
        <span key={i} style={{ display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'center' }}>
          <span className="rk-tag" data-tone={h.role === 'admin' || h.role === 'superAdmin' ? 'info' : 'muted'}>
            {ROLE_NAME[h.role]}
          </span>
          {h.namespaces && h.namespaces.length > 0 && (
            <>
              <span style={{ fontSize: 12, color: 'var(--fg3)' }}>in</span>
              <span className="rk-mono" style={{ fontSize: 12 }}>
                {h.namespaces.join(', ')}
              </span>
            </>
          )}
          {h.via && <span style={{ fontSize: 12, color: 'var(--fg3)' }}>through {h.via}</span>}
        </span>
      ))}
      <span style={{ fontSize: 12, color: 'var(--fg3)' }}>Plus the nodes, for everyone who signs in.</span>
    </div>
  )
}

/**
 * Adds a local user to a local group (RB-43): the form says what the person
 * can do afterwards, a confirmation follows, and the change is one to 221B,
 * not to a cluster (RB-51).
 */
export function AddToGroup({ users, groups, user: userProp, group: groupProp, onClose, onDone }: Props) {
  const locals = users.filter((u) => u.source === 'local' && !u.managed)
  const [username, setUsername] = useState(userProp ?? locals.find((u) => groups.some((g) => !u.groups.includes(g.name)))?.username ?? locals[0]?.username ?? '')
  const user = locals.find((u) => u.username === username)
  const open = user ? groups.filter((g) => !user.groups.includes(g.name)) : groups
  const [group, setGroup] = useState(groupProp ?? open[0]?.name ?? '')
  const [asking, setAsking] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const g = groups.find((x) => x.name === group)
  const member = !!user && !!g && user.groups.includes(g.name)
  const ready = !!user && !!g && !member

  const apply = async () => {
    if (!user || !g) return
    setBusy(true)
    setError('')
    try {
      await changeGroups(user.username, { add: [g.name] })
      onDone(`Added ${user.username} to ${g.name}. It applies within 2 minutes.`)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  return (
    <div style={{ position: 'fixed', inset: 0, zIndex: 50, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16 }}>
      <button aria-label="Close" tabIndex={-1} onClick={onClose} style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', border: 0, padding: 0, background: 'var(--backdrop)', cursor: 'default' }} />
      <section
        role="dialog"
        aria-modal="true"
        aria-label="Add to a local group"
        className="rk-card"
        data-testid="add-to-group"
        onKeyDown={(e) => {
          if (e.key === 'Escape' && !asking) {
            e.stopPropagation()
            onClose()
          }
        }}
        style={{ position: 'relative', width: 'min(520px, 100%)', maxHeight: '100%', overflow: 'auto', padding: '18px 20px', display: 'flex', flexDirection: 'column', gap: 14, boxShadow: 'var(--shadow)' }}
      >
        <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>Add to a local group</h2>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 12, color: 'var(--fg3)' }}>
          Local user
          <Select label="Local user" value={username} options={locals.length ? locals.map((u): [string, string] => [u.username, u.email ? `${u.username} (${u.email})` : u.username]) : [['', 'No local users']]} onChange={setUsername} disabled={!!userProp || !locals.length} width="100%" testId="add-user" />
          <span className="rk-fhint">LDAP people get their groups from the directory and do not appear here.</span>
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 12, color: 'var(--fg3)' }}>
          Group
          <Select
            label="Group"
            value={group}
            options={groups.length ? groups.map((x): [string, string] => [x.name, groupChoice(x) + (user?.groups.includes(x.name) ? ' (already a member)' : '')]) : [['', 'No local groups defined']]}
            onChange={setGroup}
            disabled={!!groupProp || !groups.length}
            width="100%"
            testId="add-group"
          />
          <span className="rk-fhint">
            Groups, their role and their namespaces are defined in <span className="rk-mono">auth.local.groups</span>. For a different set of namespaces, add a group there and run <span className="rk-mono">helm upgrade</span>; a group may have one member.
          </span>
        </label>
        {user && g && <Result user={user} group={member ? '' : g.name} groups={groups} />}
        <Card pad>
          <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>This changes 221B, not the cluster. The bindings for the group already exist from Helm.</span>
        </Card>
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <button className="rk-btn" onClick={onClose}>
            Cancel
          </button>
          <button className="rk-btn" data-on="true" disabled={!ready} onClick={() => setAsking(true)} data-testid="add-submit">
            Add to group
          </button>
        </div>
      </section>
      {asking && user && g && (
        <ConfirmDialog title={`Add ${user.username} to ${g.name}?`} confirm="Add to group" busy={busy} error={error} onConfirm={() => void apply()} onCancel={() => setAsking(false)}>
          <p style={{ margin: 0 }}>
            {user.username} will be {g.role === 'admin' ? 'an' : 'a'} {ROLE_NAME[g.role]} in <span className="rk-mono">{g.namespaces.join(', ')}</span>, on every cluster that has the group’s bindings. The change applies within 2 minutes, while {user.username} is signed in.
          </p>
          <p className="rk-fhint" style={{ margin: 0 }}>
            Nothing is written to Kubernetes. The change is kept by 221B and recorded in the audit log with your name.
          </p>
        </ConfirmDialog>
      )}
    </div>
  )
}
