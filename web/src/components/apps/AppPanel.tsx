import { useCallback, useEffect, useState } from 'react'
import { fetchAppRefs, fetchDiff, fetchDrift, fetchImages, fetchRevisions, isCard, sourceText, updateApp, type AppDiff, type AppDrift, type AppImage, type AppRefs, type AppView, type RefKind, type RevisionView } from '../../api/apps'
import { useDetails } from '../../api/details'
import type { ClusterInfo, ReleaseDetails } from '../../api/types'
import type { WatsonItem } from '../../api/watson'
import { driftText, driftTone, type AppRow } from '../../columns/apps'
import { fmtAge, fmtTs, nWord } from '../../lib/format'
import { apiPath, href, type Route } from '../../lib/router'
import { CopyName } from '../CopyName'
import { Card, Facts } from '../Drawer'
import { CloseIcon } from '../Icons'
import { ReleaseBody, SUBTABS, type ExtraTab } from '../ReleaseBody'
import { AppToolsButton, type AppAction } from './AppMenu'
import { DiffView } from './DiffView'

export const APP_TABS = ['overview', ...SUBTABS, 'images'] as const
export type AppTab = (typeof APP_TABS)[number]

interface Props {
  cluster: ClusterInfo | null
  clusterId: string
  now: number
  route: Route
  row: AppRow
  tab: AppTab
  onTab: (t: AppTab) => void
  canChange: boolean
  onAction: (a: AppAction) => void
  onToast: (msg: string) => void
  /** What a change answered: the card, or the saved record. */
  onResult: (r: WatsonItem | AppView, doing: string) => void
  /** Bumps when a change ran, so the panel reads again. */
  version: number
  onDrift: (d: AppDrift) => void
}

/**
 * The release's panel beside the table (HA-14): everything the Helm tab
 * shows, with an Overview (source, commit, drift) first, the values file
 * editable above the values as deployed, the History's newer-version
 * notice and diffs, and the Images last.
 */
export function AppPanel({ cluster, clusterId, now, route, row, tab, onTab, canChange, onAction, onToast, onResult, version, onDrift }: Props) {
  const app = row.app
  const [drift, setDrift] = useState<AppDrift | null>(app?.drift ?? null)
  const [checking, setChecking] = useState(false)
  const [refs, setRefs] = useState<AppRefs | null>(null)
  const [revisions, setRevisions] = useState<RevisionView[] | null>(null)
  const [images, setImages] = useState<{ revision: number; images: AppImage[] } | null>(null)
  const [imageRev, setImageRev] = useState<number | undefined>(undefined)
  const [from, setFrom] = useState<number | undefined>(undefined)
  const [to, setTo] = useState<number | 'source' | undefined>(undefined)
  const [diffRef, setDiffRef] = useState<{ ref: string; kind: RefKind } | null>(null)
  const [diff, setDiff] = useState<AppDiff | null>(null)
  const [diffing, setDiffing] = useState(false)
  const [err, setErr] = useState('')
  const details = useDetails<ReleaseDetails>(row.orphan ? null : apiPath(`helm/${encodeURIComponent(row.namespace)}/${encodeURIComponent(row.name)}`))
  const d = details.data
  const key = `${row.namespace}/${row.name}`

  const check = useCallback(
    (refresh: boolean) => {
      if (!app || row.orphan) return
      setChecking(true)
      fetchDrift(clusterId, row.namespace, row.name, refresh)
        .then((x) => {
          setDrift(x)
          onDrift(x)
        })
        .catch((e: unknown) => onToast(e instanceof Error ? e.message : String(e)))
        .finally(() => setChecking(false))
    },
    [app, row.orphan, clusterId, row.namespace, row.name, onDrift, onToast],
  )

  // The tab mounts the panel afresh per release and per change that ran
  // (its key), so the state above starts over; the first drift check is
  // asked for once, off the render.
  useEffect(() => {
    if (!row.app || row.app.drift || row.orphan) return
    const t = setTimeout(() => check(false), 0)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, version])

  useEffect(() => {
    if ((tab !== 'history' && tab !== 'images') || row.orphan) return
    fetchRevisions(clusterId, row.namespace, row.name)
      .then((rs) => {
        setRevisions(rs)
        if (rs.length > 0) {
          setTo((t) => t ?? rs[0]!.revision)
          setFrom((f) => f ?? (rs.length > 1 ? rs[1]!.revision : rs[0]!.revision))
        }
      })
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
    if (tab === 'history' && app && !refs) {
      fetchAppRefs(clusterId, row.namespace, row.name)
        .then(setRefs)
        .catch(() => undefined)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab, key, version])

  useEffect(() => {
    if (tab !== 'images' || row.orphan) return
    fetchImages(clusterId, row.namespace, row.name, imageRev)
      .then(setImages)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
  }, [tab, key, version, imageRev, clusterId, row.namespace, row.name, row.orphan])

  const compare = () => {
    if (!to) return
    setDiffing(true)
    setErr('')
    fetchDiff(clusterId, row.namespace, row.name, { from, to, ref: to === 'source' ? diffRef?.ref : undefined, refKind: to === 'source' ? diffRef?.kind : undefined })
      .then(setDiff)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setDiffing(false))
  }

  const commit = app?.commit ? app.commit.slice(0, 12) : ''

  const overview = (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))', gap: 12, alignItems: 'start' }}>
      {!row.orphan && (
        <Card title="Release">
          <Facts
            items={[
              ['Chart', `${row.chart} ${row.chartVersion}`],
              ['App version', row.appVersion || '—'],
              ['Revision', `${row.revision} of ${row.revisions}`],
              ['Status', row.status, row.description],
              ['Updated', row.updated ? `${fmtAge(row.updated, now)} ago` : '—', row.updated ? fmtTs(row.updated) : undefined],
              ['First deployed', row.firstDeployed ? fmtTs(row.firstDeployed) : '—'],
              ['Pods', row.pods ? `${row.podsReady}/${row.pods} ready` : 'none'],
            ]}
          />
        </Card>
      )}
      <Card title="Source" aside={canChange ? <button className="rk-btn" onClick={() => onAction('source')}>{app ? 'Change…' : 'Set…'}</button> : undefined}>
        {app ? (
          <Facts
            items={[
              ['Repository', app.source.repo],
              ['Path', app.source.path || '(root)'],
              [app.source.refKind === 'tag' ? 'Tag' : 'Branch', app.source.ref],
              ['Commit', commit ? `${commit}${app.commitSubject ? ` — ${app.commitSubject}` : ''}` : 'not deployed from here yet'],
              ['Chart at commit', app.chartVersion || '—'],
              ['Token', app.hasToken ? 'saved' : 'none'],
              ['Kept by', `${app.createdBy || 'the console'}, ${fmtTs(app.created)}`],
              ['Last change', `${app.updatedBy || '—'}, ${fmtTs(app.updated)}`],
            ]}
          />
        ) : (
          <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>None kept.</p>
        )}
      </Card>
      {!row.orphan && (
        <Card
          title="Drift"
          aside={
            app ? (
              <button className="rk-btn" onClick={() => check(true)} disabled={checking} data-testid="app-check-drift">
                {checking ? 'Checking…' : 'Check now'}
              </button>
            ) : undefined
          }
        >
          {!app ? (
            <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>Checked for releases with a source.</p>
          ) : !drift ? (
            <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>{checking ? 'Reading each object of the release as you…' : 'Not checked yet.'}</p>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="app-drift">
              <p style={{ margin: 0, fontSize: 12.5 }}>
                <span className="rk-pill" data-tone={driftTone(row)}>
                  <span className="rk-dot" />
                  {driftText(row) === 'not checked' ? driftState(drift) : driftText(row)}
                </span>{' '}
                <span style={{ color: 'var(--fg3)' }}>
                  {drift.same} as applied · {drift.changed} changed · {drift.missing} missing{drift.unknown ? ` · ${drift.unknown} unknown` : ''} · checked {fmtAge(drift.checked, now)} ago
                </span>
              </p>
              {drift.objects.filter((o) => o.state !== 'same').length > 0 && (
                <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5, display: 'flex', flexDirection: 'column', gap: 4 }}>
                  {drift.objects
                    .filter((o) => o.state !== 'same')
                    .map((o) => (
                      <li key={`${o.kind}/${o.namespace ?? ''}/${o.name}`}>
                        <span className="rk-mono">
                          {o.kind} {o.name}
                        </span>{' '}
                        <span className="rk-t" data-tone={o.state === 'missing' ? 'err' : o.state === 'changed' ? 'warn' : 'muted'}>
                          {o.state}
                        </span>
                        {o.fields && o.fields.length > 0 && <span style={{ color: 'var(--fg3)' }}>: {o.fields.join(', ')}</span>}
                        {o.error && <span style={{ color: 'var(--fg3)' }}>: {o.error}</span>}
                      </li>
                    ))}
                </ul>
              )}
              <p style={{ margin: 0, fontSize: 11.5, color: 'var(--fg3)' }}>A field the manifest sets and the cluster has differently is drift; what the cluster added (defaults, status) is not. Values are never shown.</p>
            </div>
          )}
        </Card>
      )}
    </div>
  )

  const imagesTab = (
    <Card
      title="Images"
      aside={
        revisions && revisions.length > 1 ? (
          <label className="rk-field" style={{ width: 150 }}>
            <select value={imageRev ?? ''} onChange={(e) => setImageRev(e.target.value ? Number(e.target.value) : undefined)} aria-label="Revision" data-testid="app-images-rev">
              <option value="">current revision</option>
              {revisions.map((r) => (
                <option key={r.revision} value={r.revision}>
                  revision {r.revision}
                </option>
              ))}
            </select>
          </label>
        ) : undefined
      }
      pad={false}
    >
      {!images ? (
        <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 12.5, color: 'var(--fg3)' }}>{err || 'Loading…'}</p>
      ) : images.images.length === 0 ? (
        <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 12.5, color: 'var(--fg3)' }}>The manifest of revision {images.revision} runs no container.</p>
      ) : (
        <div role="table" aria-label="Images" style={{ display: 'grid', gridTemplateColumns: '1fr auto auto 1fr', fontSize: 12.5 }} data-testid="app-images">
          {['Image', 'Tag', 'Registry', 'Used by'].map((h) => (
            <span key={h} role="columnheader" style={{ padding: '8px 12px', fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)', borderBottom: '1px solid var(--line)' }}>
              {h}
            </span>
          ))}
          {images.images.map((im, i) => (
            <div key={i} role="row" style={{ display: 'contents' }} data-image={im.image}>
              <span role="cell" className="rk-mono" style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)', minWidth: 0, overflowWrap: 'anywhere' }} title={im.image}>
                {im.repository}
                {im.digest ? <span style={{ color: 'var(--fg3)' }}>@{im.digest.slice(0, 19)}…</span> : ''}
              </span>
              <span role="cell" className="rk-mono" style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)' }}>
                {im.tag || (im.digest ? '(digest)' : 'latest')}
              </span>
              <span role="cell" className="rk-mono" style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)', color: 'var(--fg3)' }}>
                {im.registry || 'docker.io'}
              </span>
              <span role="cell" style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)', color: 'var(--fg2)' }}>
                {im.kind} <span className="rk-mono">{im.name}</span> · {im.init ? 'init container ' : ''}
                <span className="rk-mono">{im.container}</span>
              </span>
            </div>
          ))}
        </div>
      )}
      <p style={{ margin: 0, padding: '10px 16px 12px', fontSize: 11.5, color: 'var(--fg3)' }}>
        Every image the manifest of {images ? `revision ${images.revision}` : 'the release'} names: {images ? nWord(new Set(images.images.map((x) => x.image)).size, 'distinct image', 'distinct images') : ''}.
      </p>
    </Card>
  )

  const historyAbove = (
    <>
      {app && refs && (refs.newerTags.length > 0 || refs.moved) && (
        <div className="rk-callout" data-tone="info" data-testid="app-newer">
          <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
            <strong style={{ fontSize: 12.5, fontWeight: 600 }}>{refs.newerTags.length > 0 ? `A newer version is available: ${refs.newerTags[0]}` : `Branch ${app.source.ref} has new commits`}</strong>
            <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>
              {refs.newerTags.length > 1 ? `Newer tags: ${refs.newerTags.join(', ')}. ` : ''}
              {refs.moved && refs.head ? `The branch is at ${refs.head.slice(0, 12)}; ${commit || 'nothing'} is deployed. ` : ''}
              {canChange && (
                <>
                  <button className="rk-link" onClick={() => onAction(refs.newerTags.length > 0 ? 'switch' : 'upgrade')}>
                    {refs.newerTags.length > 0 ? 'Switch to it' : 'Upgrade'}
                  </button>
                  , or compare it below first.
                </>
              )}
            </span>
          </div>
        </div>
      )}
      <Card title="Compare revisions, or the deployed one with the source">
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, fontSize: 12.5 }}>
          <span>From revision</span>
          <label className="rk-field" style={{ width: 90 }}>
            <select value={from ?? ''} onChange={(e) => setFrom(Number(e.target.value))} aria-label="From revision" data-testid="app-diff-from">
              {(revisions ?? []).map((r) => (
                <option key={r.revision} value={r.revision}>
                  {r.revision}
                </option>
              ))}
            </select>
          </label>
          <span>to</span>
          <label className="rk-field" style={{ width: 170 }}>
            <select value={to === undefined ? '' : String(to)} onChange={(e) => setTo(e.target.value === 'source' ? 'source' : Number(e.target.value))} aria-label="To revision" data-testid="app-diff-to">
              {(revisions ?? []).map((r) => (
                <option key={r.revision} value={r.revision}>
                  revision {r.revision}
                </option>
              ))}
              {app && <option value="source">the source (git)</option>}
            </select>
          </label>
          {to === 'source' && app && (
            <label className="rk-field" style={{ minWidth: 200 }}>
              <select value={diffRef ? `${diffRef.kind}:${diffRef.ref}` : ''} onChange={(e) => (e.target.value ? setDiffRef({ kind: e.target.value.split(':')[0] as RefKind, ref: e.target.value.split(':').slice(1).join(':') }) : setDiffRef(null))} aria-label="At branch or tag" data-testid="app-diff-ref">
                <option value="">at {app.source.ref} (deployed ref)</option>
                {refs?.branches.map((b) => (
                  <option key={'b' + b} value={'branch:' + b}>
                    branch {b}
                  </option>
                ))}
                {refs?.tags.map((t) => (
                  <option key={'t' + t} value={'tag:' + t}>
                    tag {t}
                  </option>
                ))}
              </select>
            </label>
          )}
          <button className="rk-btn" data-on="true" onClick={compare} disabled={diffing || !to} data-testid="app-compare">
            {diffing ? 'Comparing…' : 'Compare'}
          </button>
        </div>
        {err && (
          <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
            {err}
          </p>
        )}
        {diff && <DiffView diff={diff} />}
      </Card>
    </>
  )

  const before: ExtraTab[] = [{ id: 'overview', label: 'Overview', render: () => overview }]
  const after: ExtraTab[] = [{ id: 'images', label: 'Images', count: images?.images.length, render: () => imagesTab }]

  return (
    <main className="rk-pane" aria-label={`Release ${row.name}`} style={{ flex: '999 1 560px', minWidth: 0, display: 'flex', flexDirection: 'column' }} data-testid="app-panel">
      <section aria-label={`Release ${row.name}`} style={{ padding: '18px 24px 28px', display: 'flex', flexDirection: 'column', gap: 14 }}>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 12px' }}>
          <h2 className="rk-mono" style={{ margin: 0, fontSize: 18, fontWeight: 600 }} data-testid="app-title">
            {row.name}
            <CopyName text={row.name} what="release name" />
          </h2>
          <span className="rk-pill" data-tone={row.tone} title={row.description} data-testid="app-status">
            <span className="rk-dot" />
            {row.status}
          </span>
          {!row.orphan && (
            <span className="rk-tag" style={{ fontSize: 11, padding: '2px 7px' }}>
              revision {row.revision}
            </span>
          )}
          <span style={{ flex: '1 1 auto' }} />
          {canChange && app && (
            <button className="rk-btn" data-on="true" onClick={() => onAction(row.orphan ? 'install' : 'deploy')} title={row.orphan ? 'helm install from the saved source, values and parameters' : 'helm upgrade from the saved source, values and parameters'} data-testid="app-deploy">
              {row.orphan ? 'Install' : 'Upgrade'}
            </button>
          )}
          <AppToolsButton row={row} canChange={canChange} onPick={onAction} label="Tools" />
          <a className="rk-icon-btn" href={href('helm1')} aria-label="Close the release" title="Close" data-testid="app-close">
            <CloseIcon />
          </a>
        </div>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 18px', fontSize: 12.5, color: 'var(--fg3)' }} data-testid="app-facts">
          <span>
            Namespace{' '}
            <a className="rk-link rk-mono" href={href('namespaces', [row.namespace])} style={{ fontSize: 12.5 }}>
              {row.namespace}
            </a>
          </span>
          {!row.orphan && (
            <>
              <span>
                Chart <span className="rk-mono" style={{ color: 'var(--fg)' }}>{row.chart}-{row.chartVersion}</span>
              </span>
              <span>
                App version <span className="rk-mono" style={{ color: 'var(--fg)' }}>{row.appVersion || '—'}</span>
              </span>
              <span>
                Updated <span className="rk-num" style={{ color: 'var(--fg)' }}>{row.updated ? `${fmtAge(row.updated, now)} ago · ${fmtTs(row.updated)}` : '—'}</span>
              </span>
            </>
          )}
          <span data-testid="app-source-line">
            {app ? (
              <>
                Source <span className="rk-mono" style={{ color: 'var(--fg)' }}>{sourceText(app.source)}</span>
                {commit ? (
                  <>
                    {' '}
                    · commit <span className="rk-mono" style={{ color: 'var(--fg)' }}>{commit}</span>
                  </>
                ) : (
                  ' · not deployed from it yet'
                )}
              </>
            ) : (
              <>
                The console keeps no source for this release.{' '}
                {canChange && (
                  <button className="rk-link" onClick={() => onAction('source')} data-testid="app-set-source">
                    Set its source
                  </button>
                )}
              </>
            )}
          </span>
        </div>
        {row.orphan ? (
          <>
            <div className="rk-callout" data-tone="warn" data-testid="app-orphan">
              <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
                <strong style={{ fontSize: 12.5, fontWeight: 600 }}>Not installed</strong>
                <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>
                  The console keeps this application's source, values and parameters, but there is no release {row.name} in {row.namespace}: its install failed, or the release was removed another way. Watson has the install's output.
                  {canChange ? ' Install it from the Tools, change its values first, or forget it.' : ''}
                </span>
              </div>
            </div>
            {overview}
            {app && <ValuesEditor clusterId={clusterId} row={row} app={app} canChange={canChange} onResult={onResult} />}
          </>
        ) : (
          <ReleaseBody
            cluster={cluster}
            now={now}
            onToast={onToast}
            route={route}
            current={row}
            d={d}
            error={details.error}
            sub={tab}
            onSub={(id) => onTab(id as AppTab)}
            before={before}
            after={after}
            above={{
              values: app ? <ValuesEditor clusterId={clusterId} row={row} app={app} canChange={canChange} onResult={onResult} /> : <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>A values file and parameters are kept for releases with a source.</p>,
              history: historyAbove,
            }}
          />
        )}
      </section>
    </main>
  )
}

/** The application's values file and parameters, edited in place (HA-12, HA-13): saved for the next upgrade, or deployed at once. */
function ValuesEditor({ clusterId, row, app, canChange, onResult }: { clusterId: string; row: AppRow; app: AppView; canChange: boolean; onResult: (r: WatsonItem | AppView, doing: string) => void }) {
  const [values, setValues] = useState(app.values ?? '')
  const [params, setParams] = useState((app.params ?? []).join('\n'))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const changed = values !== (app.values ?? '') || params !== (app.params ?? []).join('\n')
  const paramList = () =>
    params
      .split('\n')
      .map((l) => l.trim())
      .filter((l) => l && !l.startsWith('#'))
  const save = (upgrade: boolean) => {
    for (const p of paramList()) {
      if (!p.includes('=')) {
        setError(`"${p}" is not key=value`)
        return
      }
    }
    setBusy(true)
    setError('')
    updateApp(clusterId, row.namespace, row.name, { values, params: paramList(), upgrade, install: upgrade && row.orphan })
      .then((r) => {
        setBusy(false)
        onResult(r, isCard(r) ? `${row.orphan ? 'Installing' : 'Upgrading'} ${row.name} with the values` : 'Values saved')
      })
      .catch((e: unknown) => {
        setBusy(false)
        setError(e instanceof Error ? e.message : String(e))
      })
  }
  return (
    <Card title="Values file and parameters" aside={<span style={{ fontSize: 12, color: 'var(--fg3)' }}>what the console deploys this application with; the chart's defaults apply underneath</span>}>
      <textarea
        className="rk-area rk-mono"
        value={values}
        onChange={(e) => setValues(e.target.value)}
        readOnly={!canChange}
        spellCheck={false}
        aria-label="Values file"
        onKeyDown={(e) => {
          // Tab indents, as in an editor.
          if (e.key === 'Tab' && !e.shiftKey) {
            e.preventDefault()
            const el = e.currentTarget
            const s = el.selectionStart
            const next = values.slice(0, s) + '  ' + values.slice(el.selectionEnd)
            setValues(next)
            requestAnimationFrame(() => el.setSelectionRange(s + 2, s + 2))
          }
        }}
        style={{ minHeight: 'min(60vh, 640px)', fontSize: 13, lineHeight: 1.55, resize: 'vertical', tabSize: 2 }}
        placeholder={'replicaCount: 2\nimage:\n  tag: v1.2.0'}
        data-testid="app-values"
      />
      <div>
        <h4 className="rk-h" style={{ margin: '0 0 6px', fontSize: 12 }}>
          Parameters
        </h4>
        <textarea className="rk-area rk-mono" value={params} onChange={(e) => setParams(e.target.value)} readOnly={!canChange} spellCheck={false} aria-label="Parameters" style={{ minHeight: 64, fontSize: 12.5, lineHeight: 1.5, resize: 'vertical' }} placeholder="image.tag=v1.2.0 — one key=value per line, as helm --set takes them, applied over the values file" data-testid="app-params" />
      </div>
      {error && (
        <p className="rk-t" data-tone="err" role="alert" style={{ margin: 0, fontSize: 12.5 }}>
          {error}
        </p>
      )}
      {canChange && (
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
          <button className="rk-btn" data-on={changed ? 'true' : undefined} onClick={() => save(false)} disabled={busy || !changed} data-testid="app-values-save">
            Save
          </button>
          <button className="rk-btn" onClick={() => save(true)} disabled={busy} aria-busy={busy} data-testid="app-values-deploy">
            {busy ? 'Sending…' : row.orphan ? 'Save and install' : 'Save and upgrade'}
          </button>
          <span className="rk-hint">{changed ? 'Unsaved changes: Save keeps them for the next deploy.' : 'Saved. Upgrade, at the top, deploys the saved source, values and parameters as you, through Watson.'}</span>
        </div>
      )}
    </Card>
  )
}

function driftState(d: AppDrift): string {
  if (d.changed + d.missing > 0) return 'drifted'
  if (d.unknown > 0 && d.same === 0) return 'unknown'
  return 'in sync'
}
