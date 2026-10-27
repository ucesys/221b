import { useCallback, useEffect, useMemo, useState } from 'react'
import { fetchApps, forgetApp, isCard, uninstallApp, type AppDrift, type AppView } from '../../api/apps'
import { useRefreshTick, useView } from '../../api/live'
import type { ReleaseRow } from '../../api/types'
import type { WatsonItem } from '../../api/watson'
import { APP_COLUMNS, appLabel, appTerms, type AppRow } from '../../columns/apps'
import { nWord } from '../../lib/format'
import { href, navigate, withParams } from '../../lib/router'
import { useTable } from '../../lib/useTable'
import { ColumnsMenu } from '../ColumnsMenu'
import { ConfirmDialog } from '../ConfirmDialog'
import { DataTable } from '../DataTable'
import { ExportMenu } from '../ExportMenu'
import { SearchBox } from '../SearchBox'
import type { TabProps } from '../tabs'
import { useToolbox } from '../toolbox/context'
import { AppDialog, type DialogMode } from './AppDialog'
import { AppsActions, type AppAction } from './AppMenu'
import { AppPanel, APP_TABS, type AppTab } from './AppPanel'

/**
 * The Helm1 page (Phase 10, HA-01): every release in one table with its
 * source and drift, a release's details beside it, and the tools that
 * install, upgrade, switch, change values and uninstall through Watson.
 */
export function AppsTab({ cluster, now, onToast, route }: TabProps) {
  const { rows: releases, loaded } = useView<ReleaseRow>('helm')
  const tb = useToolbox()
  const tick = useRefreshTick()
  const [apps, setApps] = useState<AppView[]>([])
  const [appsErr, setAppsErr] = useState('')
  const [version, setVersion] = useState(0)
  const [dialog, setDialog] = useState<{ mode: DialogMode; row?: AppRow } | null>(null)
  const [confirm, setConfirm] = useState<{ action: 'uninstall' | 'forget'; row: AppRow } | null>(null)

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
    return releases.map((r) => ({ ...r, app: byKey.get(`${r.namespace}/${r.name}`) }))
  }, [releases, apps])
  const [wantNs, wantName] = route.path
  const current = rows.find((r) => r.namespace === wantNs && r.name === wantName) ?? null
  const p = route.params
  const tab: AppTab = APP_TABS.includes(p.get('tab') as AppTab) ? (p.get('tab') as AppTab) : 'overview'
  const set = useCallback((changes: Record<string, string | string[] | undefined>) => navigate(withParams(route, changes), true), [route])
  const table = useTable<AppRow>({ id: 'apps', columns: APP_COLUMNS, rows, terms: appTerms, now, compact: !!current, defaultSort: { col: 'name', dir: 'asc' } })
  const namespaces = useMemo(() => [...new Set(releases.map((r) => r.namespace))].sort(), [releases])
  const canChange = !!tb?.available

  // A change answered with a card: say how it went, as the Tools menu does.
  const handleCard = (it: WatsonItem, doing: string) => {
    if (it.status === 'refused') onToast(it.reason || 'Watson refused it.')
    else if (it.status === 'pending') {
      onToast(`${it.command} waits in Watson for a second person to approve it.`)
      tb?.showWatson()
    } else {
      onToast(`${doing}. Watson shows how it goes.`)
      tb?.showWatson()
    }
    setVersion((v) => v + 1)
  }

  const act = (a: AppAction, row: AppRow) => {
    switch (a) {
      case 'upgrade':
      case 'switch':
      case 'values':
      case 'source':
        setDialog({ mode: a, row })
        break
      case 'uninstall':
      case 'forget':
        setConfirm({ action: a, row })
        break
    }
  }

  const onDrift = useCallback((d: AppDrift) => {
    setApps((list) => list.map((a) => (current && a.namespace === current.namespace && a.name === current.name ? { ...a, drift: d, driftState: d.changed + d.missing > 0 ? 'drifted' : d.unknown > 0 && d.same === 0 ? 'unknown' : 'in sync' } : a)))
  }, [current])

  return (
    <AppsActions.Provider value={{ canChange, act }}>
      <section className="rk-pane" aria-label="Helm applications" style={{ flex: current ? '0 1 360px' : '1 1 auto', minWidth: 0, display: 'flex', flexDirection: 'column', gap: 10, padding: '14px 16px', borderRight: current ? '1px solid var(--line)' : undefined }} data-testid="apps-table">
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 12px' }}>
          <h2 className="rk-h" style={{ margin: 0 }}>
            Applications
          </h2>
          <span className="rk-num" style={{ fontSize: 12, color: 'var(--fg3)' }}>
            {nWord(rows.length, 'release', 'releases')}
            {apps.length ? ` · ${apps.length} with a source` : ''}
          </span>
          {!current && <SearchBox rows={rows} terms={appTerms} value={table.query} onChange={table.setQuery} placeholder="Search releases" label="Search the releases" width={240} testId="apps-search" />}
          <span style={{ flex: '1 1 auto' }} />
          {canChange && (
            <button className="rk-btn" data-on="true" onClick={() => setDialog({ mode: 'create' })} data-testid="apps-new">
              New application…
            </button>
          )}
          {!current && (
            <div className="rk-tools" style={{ gap: 8 }}>
              <ColumnsMenu cols={APP_COLUMNS} hidden={table.hidden} defaults={table.defaults} onChange={table.setHidden} compact={false} />
              <ExportMenu onExport={(f) => table.exportRows(f, cluster?.name, 'helm-applications', onToast)} disabled={!table.shown.length} what="the releases in view" />
            </div>
          )}
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
          cols={current ? table.cols.filter((c) => c.id === 'name' || c.id === 'status') : table.cols}
          rowKey={(r) => r.key}
          rowLabel={appLabel}
          sort={table.sort}
          onSort={table.onSort}
          selected={current?.key ?? null}
          onOpen={(k) => {
            const r = rows.find((x) => x.key === k)
            if (!r) return
            navigate(current?.key === k ? href('helm1') : href('helm1', [r.namespace, r.name], { tab: tab === 'overview' ? undefined : tab }))
          }}
          compact={!!current}
          now={now}
          empty={loaded ? 'No Helm release here yet. New application installs one from a git repository.' : 'Loading…'}
          rowAttr="release"
        />
      </section>
      {current && (
        <AppPanel key={`${current.key}#${version}`} cluster={route.cluster} clusterName={cluster?.name} now={now} row={current} tab={tab} onTab={(t) => set({ tab: t === 'overview' ? undefined : t })} canChange={canChange} onAction={(a) => act(a, current)} onToast={onToast} version={version} onDrift={onDrift} />
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
            if (isCard(r)) handleCard(r, { create: 'Installing', source: 'Deploying', switch: 'Switching and upgrading', upgrade: 'Upgrading', values: 'Upgrading with the new values' }[mode] + (dialog.row ? ` ${dialog.row.name}` : ''))
            else {
              onToast(mode === 'values' ? 'Values saved for the next upgrade.' : 'Source saved.')
              setVersion((v) => v + 1)
            }
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
              (it) => handleCard(it, `Uninstalling ${row.name}`),
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
                setVersion((v) => v + 1)
              },
              (e: unknown) => onToast(e instanceof Error ? e.message : String(e)),
            )
          }}
        >
          <p style={{ margin: 0 }}>The release stays as it is; the console drops its repository, values file, parameters and token.</p>
        </ConfirmDialog>
      )}
    </AppsActions.Provider>
  )
}
