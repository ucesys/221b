import { useMemo, useState } from 'react'
import { fetchRoles, fetchUsers, useAdmin, type RoleRow, type Subject, type UsersResponse } from '../api/admin'
import { ROLE_COLUMNS, roleTerms } from '../columns/admin'
import { ROLE_NAME, SOURCE_NAME } from '../lib/admin'
import { nWord } from '../lib/format'
import { adminHref, clusterHref, navigate } from '../lib/router'
import { useTable } from '../lib/useTable'
import type { AdminProps } from './AdminPage'
import { isSuperAdmin } from './AdminPage'
import { AddToGroup } from './AddToGroup'
import { ColumnsMenu } from './ColumnsMenu'
import { DataTable } from './DataTable'
import { Card, Drawer } from './Drawer'
import { ExportMenu } from './ExportMenu'
import { InfoIcon } from './Icons'
import { SearchBox } from './SearchBox'

/** "LDAP group", "Local group · 2 members", "User, bound by hand", "Service account". */
export function subjectKind(s: Subject): string {
  if (s.kind === 'ServiceAccount') return 'Service account'
  if (!s.managed) return s.kind === 'User' ? 'User, bound by hand' : 'Group, bound by hand'
  const kind = `${SOURCE_NAME[s.source]} ${s.kind.toLowerCase()}`
  return s.members === undefined ? kind : `${kind} · ${nWord(s.members, 'member', 'members')}`
}

function Note({ children }: { children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', gap: 8, alignItems: 'flex-start', fontSize: 12.5, color: 'var(--fg2)' }}>
      <span style={{ color: 'var(--info)', marginTop: 2 }}>
        <InfoIcon />
      </span>
      <span>{children}</span>
    </div>
  )
}

function List({ title, items }: { title: string; items: string[] }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }}>
      <h3 className="rk-h">{title}</h3>
      <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 2, fontSize: 12.5 }}>
        {items.map((i) => (
          <li key={i}>{i}</li>
        ))}
      </ul>
    </div>
  )
}

function SubjectsTable({ role, cluster, canAdd, onAdd }: { role: RoleRow; cluster: string; canAdd: boolean; onAdd: (group: string) => void }) {
  if (role.subjects.length === 0) return <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>No one is bound to this role on this cluster.</p>
  const th = { textAlign: 'left' as const, fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)', padding: '6px 10px', borderBottom: '1px solid var(--line)' }
  const td = { padding: '8px 10px', borderBottom: '1px solid var(--line)', verticalAlign: 'top' as const, fontSize: 12.5 }
  return (
    <div style={{ overflowX: 'auto' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse' }} data-testid="role-subjects">
        <thead>
          <tr>
            <th scope="col" style={th}>
              Subject
            </th>
            <th scope="col" style={th}>
              Namespaces
            </th>
            <th scope="col" style={th}>
              Set by
            </th>
            <th scope="col" style={th}>
              <span className="rk-sr">Action</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {role.subjects.map((s) => (
            <tr key={`${s.kind}:${s.source}:${s.name}`} data-subject={s.name}>
              <td style={td}>
                <div className="rk-mono" style={{ fontWeight: 600 }}>
                  {s.name}
                </div>
                <div style={{ fontSize: 11.5, color: 'var(--fg3)' }}>{subjectKind(s)}</div>
              </td>
              <td style={td}>
                {s.namespaces.length ? (
                  <span className="rk-mono" style={{ fontSize: 12 }}>
                    {s.namespaces.join(', ')}
                  </span>
                ) : (
                  <span style={{ color: 'var(--fg3)' }}>Every namespace</span>
                )}
                {s.orphaned && (
                  <div>
                    <span className="rk-tag" data-tone="warn">
                      Orphaned: no such user
                    </span>
                  </div>
                )}
              </td>
              <td style={td}>
                <span className="rk-tag" data-tone={s.managed ? 'info' : 'warn'}>
                  {s.managed ? 'Helm' : 'Not managed by 221B'}
                </span>
              </td>
              <td style={td}>
                {s.managed && s.source === 'local' && s.kind === 'Group' && canAdd && (
                  <button className="rk-btn" aria-label={`Add a member to ${s.name}`} onClick={() => onAdd(s.name)}>
                    Add member
                  </button>
                )}
                {!s.managed && (
                  <a className="rk-link" href={clusterHref(cluster, 'access', [], { q: s.name })}>
                    Open in Access
                  </a>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** Roles (RB-42): the four roles, and on the right the one chosen, with its permissions and holders. */
export function RolesPage({ route, me, now, cluster, onToast }: AdminProps) {
  const sup = isSuperAdmin(me)
  const name = route.cluster
  const loaded = useAdmin(`roles:${name}`, () => fetchRoles(name), 30_000)
  const roles = useMemo(() => loaded.data?.roles ?? [], [loaded.data])
  const table = useTable<RoleRow>({ id: 'admin-roles', columns: ROLE_COLUMNS, rows: roles, terms: roleTerms, now, defaultSort: { col: 'role', dir: 'asc' } })
  const selected = roles.find((r) => r.id === route.path[1]) ?? null
  const [adding, setAdding] = useState<{ group: string; users: UsersResponse } | null>(null)

  const addMember = async (group: string) => {
    try {
      setAdding({ group, users: await fetchUsers() })
    } catch (e) {
      onToast(e instanceof Error ? e.message : String(e))
    }
  }
  const open = (id: string) => navigate(adminHref('roles', route.path[1] === id ? [] : [id], name), true)
  const close = () => navigate(adminHref('roles', [], name), true)

  if (loaded.error && !loaded.data) {
    return (
      <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0 }}>
        {loaded.error}
      </p>
    )
  }
  if (loaded.data && !loaded.data.visible) {
    return (
      <section className="rk-card" aria-label="Roles" style={{ padding: '20px 24px', display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="roles-hidden">
        <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>Roles</h2>
        <p style={{ margin: 0, color: 'var(--fg2)' }}>Your role cannot list RoleBindings on this cluster, so there is nothing to show here. Permissions shows what each role may do.</p>
      </section>
    )
  }

  const defs = loaded.data?.definitions
  return (
    <>
      <section className="rk-card" aria-label="Roles" style={{ overflow: 'hidden' }}>
        <div style={{ padding: '14px 16px 0', display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', gap: '4px 14px' }}>
          <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>Roles</h2>
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>The four roles of this console. Helm installs them; they cannot be edited here.</span>
        </div>
        {defs && (
          <div style={{ padding: '10px 16px 0', display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, fontSize: 12.5 }} data-testid="role-definitions">
            <span style={{ color: 'var(--fg3)' }}>
              Definitions in use: Admin {defs.admin === 'full' ? 'full' : 'read-only'}, SuperAdmin {defs.superAdmin === 'full' ? 'full' : 'read-only'}.
            </span>
            <span style={{ color: 'var(--fg3)' }}>
              Chosen in <span className="rk-mono">values.yaml</span> under <span className="rk-mono">rbac.roles</span>.
            </span>
          </div>
        )}
        <div className="rk-tbar">
          <SearchBox rows={roles} terms={roleTerms} value={table.query} onChange={table.setQuery} placeholder="Search roles, groups or namespaces" label="Search roles" />
          <span style={{ flex: '1 1 auto' }} />
          <ColumnsMenu cols={ROLE_COLUMNS} hidden={table.hidden} defaults={table.defaults} onChange={table.setHidden} compact={false} />
          <ExportMenu onExport={(f) => table.exportRows(f, cluster?.name, 'roles', onToast)} disabled={!table.shown.length} what="the roles shown" />
        </div>
        <DataTable<RoleRow>
          label="Roles"
          rows={table.shown}
          cols={table.cols}
          rowKey={(r) => r.id}
          rowLabel={(r, o) => `${ROLE_NAME[r.id]}, ${r.definition === 'full' ? 'full' : 'read-only'} definition${o ? ', open' : ''}`}
          sort={table.sort}
          onSort={table.onSort}
          selected={selected?.id ?? null}
          onOpen={open}
          now={now}
          empty={loaded.loading ? 'Loading the roles…' : 'No role matches.'}
          rowAttr="role"
          rowHeight={52}
        />
        <div style={{ padding: '12px 16px' }}>
          <Note>
            Everyone who signs in can also see the nodes, through <span className="rk-mono">{loaded.data?.nodesRole ?? '221b-nodes'}</span>. A person with no role sees an otherwise empty console.
            {loaded.data?.message ? ` ${loaded.data.message}` : ''}{' '}
            <a className="rk-link" href="/api/v1/admin/roles/manifest" download="221b-roles.yaml" data-testid="roles-yaml">
              Download the roles as YAML
            </a>{' '}
            to apply on a cluster the chart is not installed in; 221B never writes a role itself.
          </Note>
        </div>
      </section>
      {selected && (
        <Drawer
          label="Role details"
          kicker={<span>Role · {selected.clusterRole}</span>}
          title={ROLE_NAME[selected.id]}
          facts={
            <span className="rk-tag" data-tone={selected.definition === 'full' ? 'info' : 'muted'}>
              {selected.definition === 'full' ? 'Full definition' : 'Read-only definition'}
            </span>
          }
          onClose={close}
          width={760}
          testId="role-drawer"
        >
          <p style={{ margin: 0 }}>{selected.description}</p>
          <Card>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 16 }}>
              <List title="Can" items={selected.can} />
              <List title="Cannot" items={selected.cannot} />
            </div>
            <a className="rk-link" href={adminHref('permissions', [], name)}>
              See every permission
            </a>
          </Card>
          <Card title="Holders" aside={<span className="rk-count">{selected.subjects.length}</span>} label="Holders">
            <span style={{ fontSize: 12, color: 'var(--fg3)' }}>Groups and their namespaces, as set by Helm</span>
            <SubjectsTable role={selected} cluster={name} canAdd={sup && route.cluster === 'local'} onAdd={(g) => void addMember(g)} />
          </Card>
          <Note>
            Groups come from <span className="rk-mono">auth.ldap.roles</span> and <span className="rk-mono">auth.local.groups</span>; change them with <span className="rk-mono">helm upgrade</span>. Neither this console nor Watson creates or removes bindings. A local user gets a role by joining a local group.
          </Note>
        </Drawer>
      )}
      {adding && (
        <AddToGroup
          users={adding.users.users}
          groups={adding.users.groups}
          group={adding.group}
          onClose={() => setAdding(null)}
          onDone={(m) => {
            setAdding(null)
            loaded.reload()
            onToast(m)
          }}
        />
      )}
    </>
  )
}
