import { useMemo, useState } from 'react'
import { fetchPermissions, fetchRoles, useAdmin, type PermRole, type RoleId, type RoleRow } from '../api/admin'
import { MATRIX_COLUMNS, matrixTerms } from '../columns/admin'
import { driftText, matrixOf, permTotals, reachText, ROLE_NAME, roleTotalsText, strictText, type MatrixRow } from '../lib/admin'
import { fmtClock } from '../lib/format'
import { adminHref, navigate } from '../lib/router'
import { useTable } from '../lib/useTable'
import type { AdminProps } from './AdminPage'
import { ColumnsMenu } from './ColumnsMenu'
import { DataTable } from './DataTable'
import { Card, Drawer } from './Drawer'
import { ExportMenu } from './ExportMenu'
import { SearchBox } from './SearchBox'
import { YamlView } from './YamlView'

type Show = 'all' | 'read' | 'write'

const DEFINITION_TEXT = { full: 'Full definition', readOnly: 'Read-only definition' } as const

function Facts({ items }: { items: [string, React.ReactNode][] }) {
  return (
    <dl style={{ margin: 0, display: 'grid', gridTemplateColumns: 'max-content 1fr', gap: '6px 16px', fontSize: 12.5 }}>
      {items.map(([k, v]) => (
        <div key={k} style={{ display: 'contents' }}>
          <dt style={{ color: 'var(--fg3)' }}>{k}</dt>
          <dd style={{ margin: 0, minWidth: 0 }}>{v}</dd>
        </div>
      ))}
    </dl>
  )
}

function Bullets({ title, items }: { title: string; items: string[] }) {
  if (!items.length) return null
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

/** A role's details: what it is for, where it applies, how it is bound, and its ClusterRole as YAML. */
function RoleDrawer({ perm, row, differences, onClose, onToast }: { perm: PermRole; row: RoleRow | undefined; differences: string[]; onClose: () => void; onToast: (m: string) => void }) {
  const [tab, setTab] = useState('overview')
  const t = permTotals(perm)
  const sensitive = [...new Set(perm.rules.flatMap((r) => r.flags))]
  return (
    <Drawer
      label="Role details"
      kicker={<span>Role · {perm.clusterRole}</span>}
      title={ROLE_NAME[perm.id]}
      facts={<span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>{DEFINITION_TEXT[perm.definition]}</span>}
      tabs={[
        { id: 'overview', label: 'Overview' },
        { id: 'yaml', label: 'YAML' },
      ]}
      tab={tab}
      onTab={setTab}
      onClose={onClose}
      width={760}
      testId="perm-role-drawer"
    >
      {tab === 'overview' && (
        <>
          {row?.description && <p style={{ margin: 0 }}>{row.description}</p>}
          <Card title="The role">
            <Facts
              items={[
                ['Kubernetes object', <span className="rk-mono">ClusterRole {perm.clusterRole}</span>],
                ['Definition', perm.definition === 'full' ? 'Full: the changes Watson supports, within its reach' : 'Read-only: no verb that changes anything'],
                ['Where it applies', row?.scope ?? reachText(perm.id)],
                ['How people get it', perm.id === 'admin' || perm.id === 'viewer' ? 'A group listed for it in values (auth.ldap.roles, auth.oidc.roles or auth.local.groups), bound by Helm in each of its namespaces' : 'Held in 221B: a directory group listed for it, or given to a local user on the Users page'],
                ['On this cluster', perm.present ? 'Installed' : 'Not installed: people with it see nothing here'],
                ['Permissions', `${roleTotalsText(perm)} · ${t.kinds} ${t.kinds === 1 ? 'kind' : 'kinds'} in all`],
                ['Sensitive', sensitive.length ? sensitive.join(', ') : 'None'],
              ]}
            />
          </Card>
          {row && (row.can.length > 0 || row.cannot.length > 0) && (
            <Card title="In the console">
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 16 }}>
                <Bullets title="Can" items={row.can} />
                <Bullets title="Cannot" items={row.cannot} />
              </div>
            </Card>
          )}
          {differences.length > 0 && (
            <Card title="Differs from the shipped definition">
              <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5 }} data-testid="perm-role-drift">
                {differences.map((d) => (
                  <li key={d}>{d}</li>
                ))}
              </ul>
            </Card>
          )}
        </>
      )}
      {tab === 'yaml' && (perm.present ? <YamlView kind="clusterroles" name={perm.clusterRole} live={false} onToast={onToast} /> : <p style={{ margin: 0, color: 'var(--fg3)' }}>{perm.clusterRole} is not installed on this cluster.</p>)}
    </Drawer>
  )
}

/**
 * Permissions (RB-44): what each of the four roles may read and change on
 * the cluster open, generated from the roles deployed there, read-only for
 * everyone, and compared with the definitions shipped with 221B (RB-36).
 * The role tiles filter the table; a row opens its role's details.
 */
export function PermissionsPage({ route, now, cluster, onToast }: AdminProps) {
  const name = route.cluster
  const loaded = useAdmin(`permissions:${name}`, () => fetchPermissions(name), 60_000)
  const described = useAdmin(`roles:${name}`, () => fetchRoles(name), 60_000)
  const data = loaded.data
  const [show, setShow] = useState<Show>('all')
  const [role, setRole] = useState<RoleId | null>(null)
  const [sensitive, setSensitive] = useState(false)
  const all = useMemo(() => (data ? matrixOf(data.roles) : []), [data])
  // A role's tile keeps the rows it has, and only its column of the four.
  const rows = useMemo(() => all.filter((r) => (!role || !!r.cells[role]) && (show === 'all' || r.access === show) && (!sensitive || r.flags.length > 0)), [all, role, show, sensitive])
  const columns = useMemo(() => (role ? MATRIX_COLUMNS.filter((c) => !c.id.startsWith('role-') || c.id === `role-${role}`) : MATRIX_COLUMNS), [role])
  const table = useTable<MatrixRow>({ id: 'admin-permissions', columns, rows, terms: matrixTerms, now, defaultSort: { col: 'resource', dir: 'asc' } })
  const opened = data?.roles.find((r) => r.id === route.path[1]) ?? null
  const open = (id: RoleId) => navigate(adminHref('permissions', route.path[1] === id ? [] : [id], name), true)
  const close = () => navigate(adminHref('permissions', [], name), true)

  if (loaded.error && !data) {
    return (
      <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0 }}>
        {loaded.error}
      </p>
    )
  }
  const drift = data ? driftText(data.drift) : null
  const differing = data?.drift.roles.filter((r) => r.differences.length) ?? []
  const differencesOf = (id: RoleId) => data?.drift.roles.find((r) => r.id === id)?.differences ?? []
  return (
    <>
      <section className="rk-card" aria-label="Summary" style={{ padding: '14px 16px', display: 'flex', flexDirection: 'column', gap: 12 }}>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', gap: '4px 12px' }}>
          <h2 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>Permissions of the four roles</h2>
          <span className="rk-mono" style={{ fontSize: 12 }}>
            {cluster?.name ?? name}
          </span>
          <span style={{ fontSize: 12.5, color: 'var(--fg3)' }}>Generated from the roles deployed on this cluster. Read-only; nobody edits it here.</span>
        </div>
        {data && drift && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12.5 }} data-testid="drift">
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 12px' }}>
              <span className="rk-t" data-tone={drift.tone} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontWeight: 500 }}>
                <span className="rk-dot" />
                {drift.text}
              </span>
              {data.checkedAt && <span style={{ color: 'var(--fg3)' }}>Checked {fmtClock(new Date(data.checkedAt))}, every 5 minutes</span>}
            </div>
            <span style={{ color: 'var(--fg3)' }} data-testid="strict">
              {strictText(data.enforce)}
            </span>
          </div>
        )}
        {differing.length > 0 && (
          <>
            <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5 }} data-testid="drift-details">
              {differing.flatMap((r) => r.differences.map((d) => <li key={`${r.id}:${d}`}>{`${ROLE_NAME[r.id]}: ${d}`}</li>))}
            </ul>
            <div style={{ fontSize: 12.5 }}>
              <a className="rk-link" href="/api/v1/admin/roles/manifest" download="221b-roles.yaml" data-testid="roles-yaml">
                Download the roles as YAML
              </a>{' '}
              to put them back; 221B never writes a role itself.
            </div>
          </>
        )}
        {data && (
          <div role="group" aria-label="Show the permissions of" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(230px, 1fr))', gap: 10 }}>
            {data.roles.map((r) => {
              const on = role === r.id
              return (
                <div
                  key={r.id}
                  style={{ position: 'relative', border: `1px solid ${on ? 'var(--accent)' : 'var(--line)'}`, background: on ? 'var(--sel)' : undefined, borderRadius: 8, display: 'flex', flexDirection: 'column' }}
                  data-role-summary={r.id}
                  data-selected={on ? 'true' : undefined}
                >
                  <button
                    aria-pressed={on}
                    onClick={() => setRole(on ? null : r.id)}
                    title={on ? 'Show every role' : `Show only ${ROLE_NAME[r.id]}`}
                    className="rk-roletile"
                    data-testid={`role-filter-${r.id}`}
                  >
                    <span style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
                      <span style={{ fontWeight: 600 }}>{ROLE_NAME[r.id]}</span>
                      <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{r.definition === 'full' ? 'Full' : 'Read-only'}</span>
                      {!r.present && (
                        <span className="rk-t" data-tone="warn" style={{ fontSize: 12 }}>
                          Not installed
                        </span>
                      )}
                    </span>
                    <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>{roleTotalsText(r)}</span>
                    <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{reachText(r.id)}</span>
                  </button>
                  <button className="rk-link" onClick={() => open(r.id)} style={{ position: 'absolute', top: 10, right: 12, fontSize: 12 }} aria-label={`${ROLE_NAME[r.id]} details`} data-testid={`role-details-${r.id}`}>
                    Details
                  </button>
                </div>
              )
            })}
          </div>
        )}
      </section>
      <section className="rk-card" aria-label="Permissions" style={{ overflow: 'hidden' }}>
        <div className="rk-tbar">
          <div className="rk-seg" role="group" aria-label="Show">
            {(['all', 'read', 'write'] as const).map((s) => (
              <button key={s} aria-pressed={show === s} onClick={() => setShow(s)}>
                {s === 'all' ? 'All' : s === 'read' ? 'Read' : 'Write'}
              </button>
            ))}
          </div>
          <SearchBox rows={rows} terms={matrixTerms} value={table.query} onChange={table.setQuery} placeholder="Filter resources" label="Filter resources" />
          <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12.5 }}>
            <input type="checkbox" checked={sensitive} onChange={(e) => setSensitive(e.target.checked)} />
            Sensitive only
          </label>
          {role && (
            <button className="rk-btn" onClick={() => setRole(null)} data-testid="role-filter-clear">
              {ROLE_NAME[role]} only ✕
            </button>
          )}
          <span style={{ flex: '1 1 auto' }} />
          <ColumnsMenu cols={columns} hidden={table.hidden} defaults={table.defaults} onChange={table.setHidden} compact={false} />
          <ExportMenu onExport={(f) => table.exportRows(f, cluster?.name, role ? `permissions-${role}` : 'permissions', onToast)} disabled={!table.shown.length} what="the permissions shown" />
        </div>
        <DataTable<MatrixRow>
          label={role ? `Permissions of ${ROLE_NAME[role]}` : 'Permissions of the four roles'}
          rows={table.shown}
          cols={table.cols}
          rowKey={(r) => r.key}
          rowLabel={(r) => `${r.resource}, ${r.access === 'read' ? 'read' : 'write'}`}
          sort={table.sort}
          onSort={table.onSort}
          onOpen={role ? () => open(role) : undefined}
          now={now}
          empty={loaded.loading ? 'Loading the permissions…' : 'Nothing matches.'}
          rowAttr="perm"
          rowHeight={52}
        />
      </section>
      {opened && <RoleDrawer perm={opened} row={described.data?.roles.find((r) => r.id === opened.id)} differences={differencesOf(opened.id)} onClose={close} onToast={onToast} />}
    </>
  )
}
