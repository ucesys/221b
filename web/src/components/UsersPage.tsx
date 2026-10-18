import { useMemo, useState } from 'react'
import { changeGroups, changeUser, deleteUser, endSessions, fetchUsers, refreshLdap, setSuperRoles, useAdmin, type LocalGroup, type SuperRole, type UserRow } from '../api/admin'
import { USER_COLUMNS, userTerms } from '../columns/admin'
import { holdingParts, joinable, passwordProblem, ROLE_NAME, SOURCE_NAME, SUPER_ROLES } from '../lib/admin'
import { fmtAge, fmtClock, fmtTs, nWord } from '../lib/format'
import { adminHref, navigate } from '../lib/router'
import { useTable } from '../lib/useTable'
import type { AdminProps } from './AdminPage'
import { AddToGroup } from './AddToGroup'
import { ColumnsMenu } from './ColumnsMenu'
import { ConfirmDialog } from './ConfirmDialog'
import { CreateUser } from './CreateUser'
import { DataTable } from './DataTable'
import { Card, Drawer } from './Drawer'
import { ExportMenu } from './ExportMenu'
import { PlusIcon } from './Icons'
import { SearchBox } from './SearchBox'
import { Select } from './Select'

type SourceFilter = 'all' | 'ldap' | 'local' | 'oidc'

/** What a pending change asks the person to confirm. */
type Ask =
  | { kind: 'remove'; user: UserRow; group: string }
  | { kind: 'super'; user: UserRow; roles: SuperRole[] }
  | { kind: 'disable'; user: UserRow }
  | { kind: 'enable'; user: UserRow }
  | { kind: 'delete'; user: UserRow }
  | { kind: 'password'; user: UserRow }
  | { kind: 'signout'; user: UserRow }

/** Users (RB-45, RB-46): the people 221B knows, and a local user's groups, super roles and account. */
export function UsersPage({ route, now, cluster, onToast }: AdminProps) {
  const [every, setEvery] = useState(30_000)
  const loaded = useAdmin('users', fetchUsers, every)
  const data = loaded.data
  const running = !!data?.ldap?.running
  if ((running ? 2_000 : 30_000) !== every) setEvery(running ? 2_000 : 30_000)

  const [source, setSource] = useState<SourceFilter>('all')
  const all = useMemo(() => data?.users ?? [], [data])
  const rows = useMemo(() => all.filter((u) => source === 'all' || u.source === source), [all, source])
  const table = useTable<UserRow>({ id: 'admin-users', columns: USER_COLUMNS, rows, terms: userTerms, now, defaultSort: { col: 'name', dir: 'asc' } })
  const selected = all.find((u) => u.id === route.path[1] || u.username === route.path[1]) ?? null
  const [creating, setCreating] = useState(false)
  const [adding, setAdding] = useState<string | null>(null)
  const sources = useMemo(() => (['ldap', 'local', 'oidc'] as const).filter((s) => all.some((u) => u.source === s)), [all])

  const open = (id: string) => {
    const u = all.find((x) => x.id === id)
    if (u) navigate(adminHref('users', selected?.id === id ? [] : [u.username]), true)
  }
  const close = () => navigate(adminHref('users'), true)
  const done = (m: string) => {
    setCreating(false)
    setAdding(null)
    loaded.reload()
    onToast(m)
  }

  const refresh = async () => {
    try {
      await refreshLdap()
      loaded.reload()
    } catch (e) {
      onToast(e instanceof Error ? e.message : String(e))
    }
  }

  if (loaded.error && !data) {
    return (
      <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0 }}>
        {loaded.error}
      </p>
    )
  }
  const ldap = data?.ldap
  return (
    <>
      {ldap && (
        <section className="rk-card" aria-label="Directory sync" style={{ padding: '12px 16px', display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 14px' }} data-testid="ldap-strip">
          <span style={{ fontWeight: 600 }}>LDAP</span>
          <span className="rk-t" data-tone={ldap.state === 'connected' ? 'ok' : 'err'} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontWeight: 500 }}>
            <span className="rk-dot" />
            {ldap.state === 'connected' ? 'Connected' : 'Cannot reach the directory'}
          </span>
          <span className="rk-mono" style={{ fontSize: 12, color: 'var(--fg3)' }}>
            {ldap.url}
          </span>
          <span style={{ flex: '1 1 auto', fontSize: 12.5, color: 'var(--fg3)' }}>
            {ldap.lastSync ? `Last sync ${fmtClock(new Date(ldap.lastSync))} · ` : 'Not synced yet · '}
            {nWord(ldap.people, 'person', 'people')} in {nWord(ldap.groups, 'listed group', 'listed groups')} · {nWord(ldap.errors.length, 'error', 'errors')}
            {ldap.nextSync ? ` · next sync ${fmtClock(new Date(ldap.nextSync))}` : ''}
          </span>
          <button className="rk-btn" disabled={ldap.running} aria-busy={ldap.running} onClick={() => void refresh()} data-testid="ldap-refresh">
            {ldap.running ? 'Syncing…' : 'Refresh from LDAP'}
          </button>
          {(ldap.message || ldap.errors.length > 0) && (
            <p className="rk-t" data-tone="err" role="status" style={{ flexBasis: '100%', margin: 0, fontSize: 12.5 }}>
              {ldap.message || ldap.errors[0]}
              {ldap.state === 'error' ? ' The list below is from the last sync that worked.' : ''}
            </p>
          )}
        </section>
      )}
      {data && !data.persistent && data.canCreate && (
        <p className="rk-t" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} role="status">
          Local users created here are kept in memory and are lost when the console restarts. Keep them in a Secret with <span className="rk-mono">connections.store</span>, or define them in <span className="rk-mono">values.yaml</span>.
        </p>
      )}
      <section className="rk-card" aria-label="Users" style={{ overflow: 'hidden' }}>
        <div className="rk-tbar">
          <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>Users</h2>
          <span className="rk-count">{table.shown.length}</span>
          <SearchBox rows={rows} terms={userTerms} value={table.query} onChange={table.setQuery} placeholder="Search people" label="Search people" />
          {sources.length > 1 && (
            <div className="rk-seg" role="group" aria-label="Source">
              {(['all', ...sources] as const).map((s) => (
                <button key={s} aria-pressed={source === s} onClick={() => setSource(s)}>
                  {s === 'all' ? 'All' : SOURCE_NAME[s]}
                </button>
              ))}
            </div>
          )}
          <span style={{ flex: '1 1 auto' }} />
          {data?.canCreate && (
            <button className="rk-btn" onClick={() => setCreating(true)} data-testid="create-user-btn">
              <PlusIcon />
              Create local user
            </button>
          )}
          <ColumnsMenu cols={USER_COLUMNS} hidden={table.hidden} defaults={table.defaults} onChange={table.setHidden} compact={false} />
          <ExportMenu onExport={(f) => table.exportRows(f, cluster?.name, 'users', onToast)} disabled={!table.shown.length} what="the people shown" />
        </div>
        <DataTable<UserRow>
          label="Users"
          rows={table.shown}
          cols={table.cols}
          rowKey={(u) => u.id}
          rowLabel={(u, o) => `${u.name || u.username}, ${SOURCE_NAME[u.source]}, ${u.status}${o ? ', open' : ''}`}
          sort={table.sort}
          onSort={table.onSort}
          selected={selected?.id ?? null}
          onOpen={open}
          now={now}
          empty={loaded.loading ? 'Loading the users…' : 'No one matches.'}
          rowAttr="user"
          rowHeight={52}
        />
      </section>
      {selected && data && <UserDrawer user={selected} groups={data.groups} lastSuperAdmin={isLastSuperAdmin(selected, data.users)} now={now} onClose={close} onChanged={done} onAdd={() => setAdding(selected.username)} />}
      {creating && data && <CreateUser groups={data.groups} onClose={() => setCreating(false)} onDone={done} />}
      {adding && data && <AddToGroup users={data.users} groups={data.groups} user={adding} onClose={() => setAdding(null)} onDone={done} />}
    </>
  )
}

function PasswordField({ label, hint, autoComplete, value, onChange, testId }: { label: string; hint?: string; autoComplete: string; value: string; onChange: (v: string) => void; testId: string }) {
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 12, color: 'var(--fg3)' }}>
      {label}
      <span className="rk-field">
        <input type="password" autoComplete={autoComplete} value={value} onChange={(e) => onChange(e.target.value)} data-testid={testId} />
      </span>
      {hint && <span className="rk-fhint">{hint}</span>}
    </label>
  )
}

/** Whether a user is the only active local SuperAdmin, whom the server keeps (RB-30). */
function isLastSuperAdmin(user: UserRow, users: UserRow[]): boolean {
  const supers = users.filter((u) => u.source === 'local' && u.status === 'active' && u.superRoles.includes('superAdmin'))
  return supers.length === 1 && supers[0]!.username === user.username
}

/** "Signed in now, since 2 hours ago", "3 days ago · no open session", "Never". */
function signInText(user: UserRow, now: number): string {
  const last = user.lastSignIn ? `${fmtAge(user.lastSignIn, now)} ago` : ''
  if (user.signedIn) return last ? `Signed in now, since ${last}` : 'Signed in now'
  return last ? `${last} · no open session` : 'Never'
}

const LAST_SUPER = 'The only SuperAdmin: make someone else SuperAdmin before taking the role away, disabling or deleting this user, or nobody could run the console.'

function UserDrawer({ user, groups, lastSuperAdmin, now, onClose, onChanged, onAdd }: { user: UserRow; groups: LocalGroup[]; lastSuperAdmin: boolean; now: number; onClose: () => void; onChanged: (message: string) => void; onAdd: () => void }) {
  const [ask, setAsk] = useState<Ask | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [password, setPassword] = useState('')
  const [again, setAgain] = useState('')
  const [own, setOwn] = useState('')
  const [superPick, setSuperPick] = useState('')
  const local = user.source === 'local' && !user.managed
  const name = user.name || user.username
  const addable = joinable(user, groups)

  const cancel = () => {
    setAsk(null)
    setError('')
    setPassword('')
    setAgain('')
    setOwn('')
  }
  const run = async (fn: () => Promise<unknown>, message: string) => {
    setBusy(true)
    setError('')
    try {
      await fn()
      setBusy(false)
      cancel()
      onChanged(message)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }
  const confirm = () => {
    if (!ask) return
    switch (ask.kind) {
      case 'remove':
        return run(() => changeGroups(user.username, { remove: [ask.group] }), `Removed ${user.username} from ${ask.group}. It applies within 2 minutes.`)
      case 'super':
        return run(() => setSuperRoles(user.username, ask.roles), `Changed the super roles of ${user.username}. It applies within 2 minutes.`)
      case 'disable':
        return run(() => changeUser(user.username, { disabled: true }), `Disabled ${user.username} and ended their sessions.`)
      case 'enable':
        return run(() => changeUser(user.username, { disabled: false }), `Enabled ${user.username}.`)
      case 'delete':
        return run(() => deleteUser(user.username), `Deleted ${user.username}.`)
      case 'signout':
        return run(() => endSessions(user.username), `Ended the sessions of ${user.username}.`)
      case 'password': {
        const problem = !own ? 'Enter your own password.' : passwordProblem(password, user.username) || (password !== again ? 'The two new passwords are not the same.' : '')
        if (problem) {
          setError(problem)
          return
        }
        return run(() => changeUser(user.username, { password, currentPassword: own }), `Changed the password of ${user.username} and ended their sessions.`)
      }
    }
  }
  const nextSuper = (role: SuperRole, give: boolean): SuperRole[] => (give ? [...new Set([...user.superRoles, role])] : user.superRoles.filter((r) => r !== role))

  const titles: Record<Ask['kind'], string> = {
    remove: ask?.kind === 'remove' ? `Remove ${user.username} from ${ask.group}?` : '',
    super: 'Change the super roles?',
    disable: `Disable ${user.username}?`,
    enable: `Enable ${user.username}?`,
    delete: `Delete ${user.username}?`,
    password: `Change the password of ${user.username}`,
    signout: `Sign ${user.username} out everywhere?`,
  }
  return (
    <Drawer
      label={`${user.username} details`}
      kicker={<span>User</span>}
      title={user.username}
      facts={
        <>
          <span className="rk-tag" data-tone="muted">
            {SOURCE_NAME[user.source]}
          </span>
          <span className="rk-t" data-tone={user.status === 'active' ? 'ok' : 'muted'} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontWeight: 500 }}>
            <span className="rk-dot" />
            {user.status === 'active' ? 'Active' : 'Disabled'}
          </span>
        </>
      }
      onClose={onClose}
      width={620}
      testId="user-drawer"
    >
      <Card>
        <dl style={{ margin: 0, display: 'grid', gridTemplateColumns: 'max-content 1fr', gap: '6px 16px', fontSize: 12.5 }}>
          <dt style={{ color: 'var(--fg3)' }}>Name</dt>
          <dd style={{ margin: 0 }}>{name}</dd>
          {user.email && (
            <>
              <dt style={{ color: 'var(--fg3)' }}>Email</dt>
              <dd style={{ margin: 0 }}>{user.email}</dd>
            </>
          )}
          <dt style={{ color: 'var(--fg3)' }}>Seen by Kubernetes as</dt>
          <dd className="rk-mono" style={{ margin: 0 }}>
            {user.kubeName}
          </dd>
          <dt style={{ color: 'var(--fg3)' }}>Last sign-in</dt>
          <dd style={{ margin: 0 }} title={fmtTs(user.lastSignIn)}>
            {signInText(user, now)}
          </dd>
          {user.statusNote && (
            <>
              <dt style={{ color: 'var(--fg3)' }}>Status</dt>
              <dd style={{ margin: 0 }}>{user.statusNote}</dd>
            </>
          )}
        </dl>
      </Card>
      <Card title="Groups" label="Groups">
        {user.groups.length === 0 && <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>{local ? 'Not in any group.' : 'Groups come from the directory or the identity provider.'}</span>}
        {user.groups.map((g) => {
          const def = groups.find((x) => x.name === g)
          return (
            <div key={g} style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '4px 8px', fontSize: 12.5 }} data-group={g}>
              <span className="rk-mono" style={{ fontWeight: 600 }}>
                {g}
              </span>
              {def && <span className="rk-tag">{ROLE_NAME[def.role]}</span>}
              {def && (
                <span className="rk-mono" style={{ fontSize: 12, color: 'var(--fg3)' }}>
                  {def.namespaces.join(', ')}
                </span>
              )}
              <span style={{ flex: '1 1 auto' }} />
              {local && (
                <button className="rk-btn" aria-label={`Remove ${user.username} from ${g}`} onClick={() => setAsk({ kind: 'remove', user, group: g })}>
                  Remove
                </button>
              )}
            </div>
          )
        })}
        {local && (
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
            <button className="rk-btn" disabled={addable.length === 0} onClick={onAdd} data-testid="user-add-group">
              Add to a local group…
            </button>
            {addable.length === 0 && <span className="rk-fhint">{groups.length ? 'Already in every local group.' : 'No local groups are defined in values.yaml.'}</span>}
          </div>
        )}
        <span className="rk-fhint">
          Groups, their role and their namespaces are set in <span className="rk-mono">values.yaml</span>. Joining one changes this console, not the cluster, and takes effect within 2 minutes.
        </span>
      </Card>
      <Card title="What this person can do" label="What this person can do">
        {user.holdings.length === 0 && <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>No role yet: they see the nodes and nothing else.</span>}
        {user.holdings.map((h, i) => {
          const p = holdingParts(h)
          return (
            <div key={i} style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 6, fontSize: 12.5 }}>
              <span className="rk-tag" data-tone={h.role === 'admin' || h.role === 'superAdmin' ? 'info' : 'muted'}>
                {p.role}
              </span>
              {p.namespaces && (
                <>
                  <span style={{ color: 'var(--fg3)' }}>in</span>
                  <span className="rk-mono">{p.namespaces}</span>
                </>
              )}
              {p.via && <span style={{ color: 'var(--fg3)' }}>{p.via}</span>}
              <span style={{ flex: '1 1 auto' }} />
              <a className="rk-link" href={adminHref('roles', [h.role])}>
                Open in Roles
              </a>
            </div>
          )
        })}
        <span className="rk-fhint">Plus the nodes, for everyone who signs in.</span>
      </Card>
      {local && (
        <Card title="Super role" label="Super role">
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
            {user.superRoles.length === 0 && <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>None</span>}
            {user.superRoles.map((r) => (
              <span key={r} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                <span className="rk-tag" data-tone="info">
                  {ROLE_NAME[r]}
                </span>
                <button className="rk-btn" aria-label={`Take ${ROLE_NAME[r]} from ${user.username}`} disabled={r === 'superAdmin' && lastSuperAdmin} title={r === 'superAdmin' && lastSuperAdmin ? LAST_SUPER : undefined} onClick={() => setAsk({ kind: 'super', user, roles: nextSuper(r, false) })}>
                  Take away
                </button>
              </span>
            ))}
            {SUPER_ROLES.some((r) => !user.superRoles.includes(r)) && (
              <Select
                label="Give a super role"
                value={superPick}
                options={[['', 'Give a super role…'], ...SUPER_ROLES.filter((r) => !user.superRoles.includes(r)).map((r): [string, string] => [r, ROLE_NAME[r]])]}
                onChange={(v) => {
                  setSuperPick('')
                  if (v) setAsk({ kind: 'super', user, roles: nextSuper(v as SuperRole, true) })
                }}
                testId="user-super"
              />
            )}
          </div>
          <span className="rk-fhint">A super role applies on every cluster.</span>
          {lastSuperAdmin && (
            <span className="rk-fhint" data-testid="last-super-admin">
              {LAST_SUPER}
            </span>
          )}
        </Card>
      )}
      {local && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
          <button className="rk-btn" onClick={() => setAsk({ kind: 'password', user })}>
            Change password
          </button>
          <button className="rk-btn" onClick={() => setAsk({ kind: 'signout', user })}>
            Sign out of all sessions
          </button>
          <button className="rk-btn" disabled={lastSuperAdmin && user.status === 'active'} title={lastSuperAdmin ? LAST_SUPER : undefined} onClick={() => setAsk({ kind: user.status === 'active' ? 'disable' : 'enable', user })}>
            {user.status === 'active' ? 'Disable' : 'Enable'}
          </button>
          <button className="rk-btn" disabled={lastSuperAdmin} title={lastSuperAdmin ? LAST_SUPER : undefined} style={lastSuperAdmin ? undefined : { borderColor: 'var(--err)', color: 'var(--err)' }} onClick={() => setAsk({ kind: 'delete', user })}>
            Delete
          </button>
        </div>
      )}
      {user.managed && <span className="rk-fhint">Defined in values.yaml; change it with helm upgrade.</span>}
      {user.source === 'ldap' && <span className="rk-fhint">The directory owns this person: groups and status follow the next sync.</span>}
      {ask && (
        <ConfirmDialog title={titles[ask.kind]} confirm={ask.kind === 'password' ? 'Change password' : ask.kind === 'remove' ? 'Remove' : ask.kind === 'super' ? 'Change' : ask.kind === 'signout' ? 'Sign out' : ask.kind === 'delete' ? 'Delete' : ask.kind === 'disable' ? 'Disable' : 'Enable'} danger={ask.kind === 'delete' || ask.kind === 'disable'} busy={busy} error={error} onConfirm={() => void confirm()} onCancel={cancel}>
          {ask.kind === 'remove' && <p style={{ margin: 0 }}>{name} loses what {ask.group} gives within 2 minutes, while signed in. Nothing is written to Kubernetes; the change is kept by 221B and recorded in the audit log.</p>}
          {ask.kind === 'super' && (
            <p style={{ margin: 0 }}>
              {name} will hold {ask.roles.length ? ask.roles.map((r) => ROLE_NAME[r]).join(' and ') : 'no super role'} on every cluster. It applies within 2 minutes, without signing out, and is recorded in the audit log.
            </p>
          )}
          {ask.kind === 'disable' && <p style={{ margin: 0 }}>{name} is signed out and cannot sign in until you enable the account. Groups and super roles are kept.</p>}
          {ask.kind === 'enable' && <p style={{ margin: 0 }}>{name} can sign in again with the current password.</p>}
          {ask.kind === 'delete' && <p style={{ margin: 0 }}>{name} is removed from every group and signed out. Nothing is left in any cluster, because no binding names a user. This cannot be undone.</p>}
          {ask.kind === 'signout' && <p style={{ margin: 0 }}>Every open session of {name} ends; they can sign in again.</p>}
          {ask.kind === 'password' && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              <PasswordField label="Your password" hint="Yours, as you sign in: it confirms the change is yours." autoComplete="current-password" value={own} onChange={setOwn} testId="password-own" />
              <PasswordField label={`New password for ${user.username}`} hint="At least 12 characters, and not the user name. Their open sessions end." autoComplete="new-password" value={password} onChange={setPassword} testId="reset-password" />
              <PasswordField label="Repeat the new password" autoComplete="new-password" value={again} onChange={setAgain} testId="password-again" />
            </div>
          )}
        </ConfirmDialog>
      )}
    </Drawer>
  )
}
