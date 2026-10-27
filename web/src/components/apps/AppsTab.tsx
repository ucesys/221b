import { useCallback, useEffect, useMemo, useState } from 'react'
import { fetchApps, forgetApp, isCard, uninstallApp, upgradeApp, type AppDrift, type AppView } from '../../api/apps'
import { useMe } from '../../api/auth'
import { useRefreshTick, useView } from '../../api/live'
import type { ReleaseRow } from '../../api/types'
import type { WatsonItem } from '../../api/watson'
import { APP_COLUMNS, appLabel, appTerms, type AppRow } from '../../columns/apps'
import { fmtAge, nWord } from '../../lib/format'
import { href, navigate, withParams } from '../../lib/router'
import { useTable } from '../../lib/useTable'
import { ColumnsMenu } from '../ColumnsMenu'
import { ConfirmDialog } from '../ConfirmDialog'
import { DataTable } from '../DataTable'
import { ExportMenu } from '../ExportMenu'
import { releaseItem } from '../ReleaseBody'
import { SearchBox } from '../SearchBox'
import { SideList, type SideGroup } from '../SideList'
import type { TabProps } from '../tabs'
import { useToolbox } from '../toolbox/context'
import { AppDialog, type DialogMode } from './AppDialog'
import { AppsActions, type AppAction } from './AppMenu'
import { AppPanel, APP_TABS, type AppTab } from './AppPanel'

type ReleaseFilter = 'all' | 'deployed' | 'failed'

/** A record whose release is not there (its install failed) still gets a row (HA-08). */
function orphanRow(a: AppView): AppRow {
  return {
    key: `${a.namespace}/${a.name}`,
    name: a.name,
    namespace: a.namespace,
    chart: '',
    chartVersion: a.chartVersion ?? '',
    appVersion: '',
    revision: 0,
    status: 'not installed',
    tone: 'warn',
    description: 'The console keeps a source for this application, but there is no release: its install failed, or it was removed another way.',
    updated: a.updated,
    revisions: 0,
    pods: 0,
    podsReady: 0,
    podsFailed: 0,
    group: 'apps',
    app: a,
    orphan: true,
  }
}

/**
 * The Helm1 page (Phase 10, HA-01): every release in one table with its
 * source and drift, a release's panel beside it, and the tools that
 * install, upgrade, switch, change values and uninstall through Watson.
 */
export function AppsTab({ cluster, now, onToast, route }: TabProps) {
  const { rows: releases, loaded } = useView<ReleaseRow>('helm')
  const tb = useToolbox()
  const me = useMe()
  const tick = useRefreshTick()
  const [apps, setApps] = useState<AppView[]>([])
  const [appsErr, setAppsErr] = useState('')
  const [version, setVersion] = useState(0)
  const [dialog, setDialog] = useState<{ mode: DialogMode; row?: AppRow } | null>(null)
  const [confirm, setConfirm] = useState<{ action: 'uninstall' | 'forget'; row: AppRow } | null>(null)
  const [addons, setAddons] = useState(true)
  const [filter, setFilter] = useState<ReleaseFilter>('all')
  const [listQuery, setListQuery] = useState('')

  const reload = useCallback(() => {
    fetchApps(route.cluster).then(
      (list) => {
        setApps(list)
        setAppsErr('')
      },
      (e: unknown) => setAppsErr(e instanceof Error ? e.message : String(e)),
    )
  }, [route.cluster])
  useEffect(reload, [reload, tick, version])

  const rows = useMemo<AppRow[]>(() => {
    const byKey = new Map(apps.map((a) => [`${a.namespace}/${a.name}`, a]))
    const out: AppRow[] = releases.map((r) => ({ ...r, app: byKey.get(`${r.namespace}/${r.name}`) }))
    const have = new Set(out.map((r) => r.key))
    for (const a of apps) if (!have.has(`${a.namespace}/${a.name}`)) out.push(orphanRow(a))
    return out
  }, [releases, apps])
  const shown = useMemo(() => rows.filter((r) => (addons || r.group !== 'addons') && (filter === 'all' || (filter === 'deployed' ? r.status === 'deployed' : r.status === 'failed' || r.orphan))), [rows, addons, filter])
  const [wantNs, wantName] = route.path
  const current = rows.find((r) => r.namespace === wantNs && r.name === wantName) ?? null
  const p = route.params
  const tab: AppTab = APP_TABS.includes(p.get('tab') as AppTab) ? (p.get('tab') as AppTab) : 'overview'
  const set = useCallback((changes: Record<string, string | string[] | undefined>) => navigate(withParams(route, changes), true), [route])
  const table = useTable<AppRow>({ id: 'apps', columns: APP_COLUMNS, rows: shown, terms: appTerms, now, defaultSort: { col: 'name', dir: 'asc' } })
  const namespaces = useMemo(() => [...new Set(releases.map((r) => r.namespace))].sort(), [releases])
  // Only an Admin or a SuperAdmin changes applications (HA-02); the server refuses the rest.
  const mayManage = !!me && (me.mode === 'none' || me.admin || !!me.roles?.some((r) => r === 'admin' || r === 'superAdmin'))
  const canChange = !!tb?.available && mayManage

  // A change answered with a card: say how it went, as the Tools menu does.
  const handleResult = (r: WatsonItem | AppView, doing: string) => {
    if (isCard(r)) {
      if (r.status === 'refused') onToast(r.reason || 'Watson refused it.')
      else if (r.status === 'pending') {
        onToast(`${r.command} waits in Watson for a second person to approve it.`)
        tb?.showWatson()
      } else {
        onToast(`${doing}. Watson shows how it goes.`)
        tb?.showWatson()
      }
    } else onToast(`${doing}.`)
    setVersion((v) => v + 1)
  }

  const act = (a: AppAction, row: AppRow) => {
    switch (a) {
      case 'upgrade':
      case 'switch':
      case 'source':
        setDialog({ mode: a, row })
        break
      case 'values':
        navigate(href('helm1', [row.namespace, row.name], { tab: 'values' }))
        break
      case 'install':
      case 'deploy':
        upgradeApp(route.cluster, row.namespace, row.name, { install: a === 'install' }).then(
          (it) => handleResult(it, `${a === 'install' ? 'Installing' : 'Upgrading'} ${row.name}`),
          (e: unknown) => onToast(e instanceof Error ? e.message : String(e)),
        )
        break
      case 'uninstall':
      case 'forget':
        setConfirm({ action: a, row })
        break
    }
  }

  const onDrift = useCallback(
    (d: AppDrift) => {
      setApps((list) => list.map((a) => (current && a.namespace === current.namespace && a.name === current.name ? { ...a, drift: d, driftState: d.changed + d.missing > 0 ? 'drifted' : d.unknown > 0 && d.same === 0 ? 'unknown' : 'in sync' } : a)))
    },
    [current],
  )

  const segs: [ReleaseFilter, string, number][] = [
    ['all', 'All', rows.length],
    ['deployed', 'Deployed', rows.filter((r) => r.status === 'deployed').length],
    ['failed', 'Failed', rows.filter((r) => r.status === 'failed' || r.orphan).length],
  ]
  const addonsBox = (
    <label className="rk-hit" style={{ display: 'inline-flex', alignItems: 'center', gap: 8, cursor: 'pointer', fontSize: 12, color: 'var(--fg2)' }}>
      <input type="checkbox" checked={addons} onChange={(e) => setAddons(e.target.checked)} style={{ width: 15, height: 15, margin: 0, accentColor: 'var(--accent)', cursor: 'pointer' }} data-testid="apps-addons" />
      <span>Show cluster add-ons</span>
    </label>
  )
  const newButton = canChange ? (
    <button className="rk-btn" data-on="true" onClick={() => setDialog({ mode: 'create' })} data-testid="apps-new">
      New application…
    </button>
  ) : null

  // With a release open, the table folds into the Helm tab's side list.
  const lq = listQuery.trim().toLowerCase()
  const listed = shown.filter((r) => !lq || r.name.includes(lq) || r.chart.includes(lq) || r.namespace.includes(lq) || (r.app ? r.app.source.repo.includes(lq) : false))
  const item = (r: AppRow) => releaseItem(r, current?.key === r.key, (x) => href('helm1', [x.namespace, x.name], { tab: tab === 'overview' ? undefined : tab }), now, fmtAge)
  const groups: SideGroup[] = [
    { title: 'Applications', items: listed.filter((r) => r.group === 'apps').map(item) },
    { title: 'Cluster add-ons', items: listed.filter((r) => r.group === 'addons').map(item) },
  ]

  return (
    <AppsActions.Provider value={{ canChange, act }}>
      {current ? (
        <SideList
          label="Helm applications"
          title="Applications"
          aside={
            <div className="rk-seg" role="group" aria-label="Releases by status">
              {segs.map(([id, label, n]) => (
                <button key={id} aria-pressed={filter === id} onClick={() => setFilter(id)}>
                  {label} <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{n}</span>
                </button>
              ))}
            </div>
          }
          query={listQuery}
          onQuery={setListQuery}
          placeholder="Filter by release, chart or namespace"
          extra={
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10 }}>
              {addonsBox}
              <span style={{ flex: '1 1 auto' }} />
              {newButton}
            </div>
          }
          groups={groups}
          empty={loaded ? 'No releases match this filter.' : 'Waiting for the first list…'}
          testId="apps-table"
        />
      ) : (
        <section className="rk-pane" aria-label="Helm applications" style={{ flex: '1 1 auto', minWidth: 0, display: 'flex', flexDirection: 'column', gap: 10, padding: '14px 16px' }} data-testid="apps-table">
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 12px' }}>
            <h2 className="rk-h" style={{ margin: 0 }}>
              Applications
            </h2>
            <span className="rk-num" style={{ fontSize: 12, color: 'var(--fg3)' }}>
              {nWord(shown.length, 'release', 'releases')}
              {apps.length ? ` · ${apps.length} with a source` : ''}
            </span>
            <div className="rk-seg" role="group" aria-label="Releases by status">
              {segs.map(([id, label, n]) => (
                <button key={id} aria-pressed={filter === id} onClick={() => setFilter(id)}>
                  {label} <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{n}</span>
                </button>
              ))}
            </div>
            {addonsBox}
            <SearchBox rows={shown} terms={appTerms} value={table.query} onChange={table.setQuery} placeholder="Search releases" label="Search the releases" width={220} testId="apps-search" />
            <span style={{ flex: '1 1 auto' }} />
            {newButton}
            <div className="rk-tools" style={{ gap: 8 }}>
              <ColumnsMenu cols={APP_COLUMNS} hidden={table.hidden} defaults={table.defaults} onChange={table.setHidden} compact={false} />
              <ExportMenu onExport={(f) => table.exportRows(f, cluster?.name, 'helm-applications', onToast)} disabled={!table.shown.length} what="the releases in view" />
            </div>
          </div>
          {appsErr && (
            <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }}>
              {appsErr}
            </p>
          )}
          {!canChange && tb?.off && <p className="rk-hint" style={{ margin: 0 }}>{tb.off} The releases and their history can still be read here.</p>}
          <DataTable<AppRow>
            label="Helm releases"
            rows={table.shown}
            cols={table.cols}
            rowKey={(r) => r.key}
            rowLabel={appLabel}
            sort={table.sort}
            onSort={table.onSort}
            selected={null}
            onOpen={(k) => {
              const r = rows.find((x) => x.key === k)
              if (r) navigate(href('helm1', [r.namespace, r.name]))
            }}
            now={now}
            empty={loaded ? 'No Helm release here yet. New application installs one from a git repository.' : 'Loading…'}
            rowAttr="release"
          />
        </section>
      )}
      {current && (
        <AppPanel key={`${current.key}#${version}`} cluster={cluster} clusterId={route.cluster} now={now} route={route} row={current} tab={tab} onTab={(t) => set({ tab: t === 'overview' ? undefined : t, open: undefined })} canChange={canChange} onAction={(a) => act(a, current)} onToast={onToast} onResult={handleResult} version={version} onDrift={onDrift} />
      )}
      {dialog && (
        <AppDialog
          cluster={route.cluster}
          mode={dialog.mode}
          row={dialog.row}
          namespaces={namespaces}
          onClose={() => setDialog(null)}
          onDone={(r, mode) => {
            setDialog(null)
            handleResult(r, { create: 'Installing', source: 'Deploying', switch: 'Switching and upgrading', upgrade: 'Upgrading', values: 'Upgrading with the new values' }[mode] + (dialog.row ? ` ${dialog.row.name}` : ''))
          }}
        />
      )}
      {confirm && confirm.action === 'uninstall' && (
        <ConfirmDialog
          title={`Uninstall release ${confirm.row.name}?`}
          confirm="Uninstall"
          danger
          onCancel={() => setConfirm(null)}
          onConfirm={() => {
            const row = confirm.row
            setConfirm(null)
            uninstallApp(route.cluster, row.namespace, row.name).then(
              (it) => handleResult(it, `Uninstalling ${row.name}`),
              (e: unknown) => onToast(e instanceof Error ? e.message : String(e)),
            )
          }}
        >
          <p style={{ margin: 0 }}>
            In <span className="rk-mono">{confirm.row.namespace}</span>: every object of the release is deleted, and its history with it. The console forgets its source. Helm runs as you.
          </p>
        </ConfirmDialog>
      )}
      {confirm && confirm.action === 'forget' && (
        <ConfirmDialog
          title={`Forget the source of ${confirm.row.name}?`}
          confirm="Forget source"
          onCancel={() => setConfirm(null)}
          onConfirm={() => {
            const row = confirm.row
            setConfirm(null)
            forgetApp(route.cluster, row.namespace, row.name).then(
              () => {
                onToast(`The console no longer keeps a source for ${row.name}.`)
                if (row.orphan) navigate(href('helm1'))
                setVersion((v) => v + 1)
              },
              (e: unknown) => onToast(e instanceof Error ? e.message : String(e)),
            )
          }}
        >
          <p style={{ margin: 0 }}>{confirm.row.orphan ? 'The console drops its repository, values file, parameters and token.' : 'The release stays as it is; the console drops its repository, values file, parameters and token.'}</p>
        </ConfirmDialog>
      )}
    </AppsActions.Provider>
  )
}
