import { useMemo, useState } from 'react'
import { fetchPermissions, useAdmin } from '../api/admin'
import { MATRIX_COLUMNS, matrixTerms } from '../columns/admin'
import { driftText, matrixOf, permTotals, ROLE_NAME, totalsText, type MatrixRow } from '../lib/admin'
import { fmtClock } from '../lib/format'
import { useTable } from '../lib/useTable'
import type { AdminProps } from './AdminPage'
import { ColumnsMenu } from './ColumnsMenu'
import { DataTable } from './DataTable'
import { ExportMenu } from './ExportMenu'
import { SearchBox } from './SearchBox'

type Show = 'all' | 'read' | 'write'

/**
 * Permissions (RB-44): what the four roles may read and change on the
 * cluster open, generated from the roles deployed there, read-only for
 * everyone, and compared with the definitions shipped with 221B (RB-36).
 */
export function PermissionsPage({ route, now, cluster, onToast }: AdminProps) {
  const name = route.cluster
  const loaded = useAdmin(`permissions:${name}`, () => fetchPermissions(name), 60_000)
  const data = loaded.data
  const [show, setShow] = useState<Show>('all')
  const [sensitive, setSensitive] = useState(false)
  const matrix = useMemo(() => (data ? matrixOf(data.roles) : []), [data])
  const rows = useMemo(() => matrix.filter((r) => (show === 'all' || r.access === show) && (!sensitive || r.flags.length > 0)), [matrix, show, sensitive])
  const table = useTable<MatrixRow>({ id: 'admin-permissions', columns: MATRIX_COLUMNS, rows, terms: matrixTerms, now, defaultSort: { col: 'resource', dir: 'asc' } })

  if (loaded.error && !data) {
    return (
      <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0 }}>
        {loaded.error}
      </p>
    )
  }
  const drift = data ? driftText(data.drift) : null
  const differing = data?.drift.roles.filter((r) => r.differences.length) ?? []
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
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 12px', fontSize: 12.5 }} data-testid="drift">
            <span className="rk-t" data-tone={drift.tone} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontWeight: 500 }}>
              <span className="rk-dot" />
              {drift.text}
            </span>
            {data.checkedAt && <span style={{ color: 'var(--fg3)' }}>Checked {fmtClock(new Date(data.checkedAt))}, every 5 minutes</span>}
            <span style={{ flex: '1 1 auto' }} />
            <span className="rk-tag" data-tone="info">
              Admin: {data.definitions.admin === 'full' ? 'full' : 'read-only'}
            </span>
            <span className="rk-tag" data-tone="info">
              SuperAdmin: {data.definitions.superAdmin === 'full' ? 'full' : 'read-only'}
            </span>
            <span className="rk-tag" data-tone={data.enforce === 'strict' ? 'ok' : 'muted'} title="With strict mode, a cluster whose roles allow more than the shipped ones refuses every change">
              Strict mode: {data.enforce === 'strict' ? 'on' : 'off'}
            </span>
          </div>
        )}
        {differing.length > 0 && (
          <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5 }} data-testid="drift-details">
            {differing.flatMap((r) => r.differences.map((d) => <li key={`${r.id}:${d}`}>{`${ROLE_NAME[r.id]}: ${d}`}</li>))}
          </ul>
        )}
        {data && (
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(230px, 1fr))', gap: 10 }}>
            {data.roles.map((r) => {
              const t = permTotals(r)
              return (
                <div key={r.id} style={{ border: '1px solid var(--line)', borderRadius: 8, padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 4 }} data-role-summary={r.id}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <span style={{ fontWeight: 600 }}>{ROLE_NAME[r.id]}</span>
                    <span className="rk-tag" data-tone={r.definition === 'full' ? 'info' : 'muted'}>
                      {r.definition === 'full' ? 'Full' : 'Read-only'}
                    </span>
                    {!r.present && (
                      <span className="rk-tag" data-tone="warn">
                        Not installed
                      </span>
                    )}
                  </div>
                  <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>{totalsText(t)}</span>
                </div>
              )
            })}
          </div>
        )}
      </section>
      <section className="rk-card" aria-label="Permission matrix" style={{ overflow: 'hidden' }}>
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
          <span style={{ flex: '1 1 auto' }} />
          <ColumnsMenu cols={MATRIX_COLUMNS} hidden={table.hidden} defaults={table.defaults} onChange={table.setHidden} compact={false} />
          <ExportMenu onExport={(f) => table.exportRows(f, cluster?.name, 'permissions', onToast)} disabled={!table.shown.length} what="the permissions shown" />
        </div>
        <DataTable<MatrixRow>
          label="Permissions of the four roles"
          rows={table.shown}
          cols={table.cols}
          rowKey={(r) => r.key}
          rowLabel={(r) => `${r.resource}, ${r.access === 'read' ? 'read' : 'write'}`}
          sort={table.sort}
          onSort={table.onSort}
          now={now}
          empty={loaded.loading ? 'Loading the permissions…' : 'Nothing matches.'}
          rowAttr="perm"
          rowHeight={52}
        />
      </section>
    </>
  )
}
