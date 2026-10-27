import { useCallback, useEffect, useState } from 'react'
import { fetchAppRefs, fetchDiff, fetchDrift, fetchImages, fetchRevisions, sourceText, type AppDiff, type AppDrift, type AppImage, type AppRefs, type RefKind, type RevisionView } from '../../api/apps'
import { useDetails } from '../../api/details'
import type { ReleaseDetails } from '../../api/types'
import { driftText, driftTone, type AppRow } from '../../columns/apps'
import { fmtAge, fmtTs, nWord } from '../../lib/format'
import { apiPath, href } from '../../lib/router'
import { CopyName } from '../CopyName'
import { Card, Facts } from '../Drawer'
import { colorLine } from '../YamlView'
import { AppToolsButton, type AppAction } from './AppMenu'
import { DiffView } from './DiffView'

export const APP_TABS = ['overview', 'values', 'history', 'images'] as const
export type AppTab = (typeof APP_TABS)[number]

interface Props {
  cluster: string
  clusterName?: string
  now: number
  row: AppRow
  tab: AppTab
  onTab: (t: AppTab) => void
  canChange: boolean
  onAction: (a: AppAction) => void
  onToast: (msg: string) => void
  /** Bumps when a change ran, so the panel reads again. */
  version: number
  onDrift: (d: AppDrift) => void
}

const COLOR: Record<string, string> = { k: 'var(--y-key)', s: 'var(--y-str)', n: 'var(--y-num)', b: 'var(--y-bool)', p: 'var(--y-punc)' }

/** The release's details beside the table (HA-14): overview with drift, values, history with diffs, images. */
export function AppPanel({ cluster, clusterName, now, row, tab, onTab, canChange, onAction, onToast, version, onDrift }: Props) {
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
  const details = useDetails<ReleaseDetails>(apiPath(`helm/${encodeURIComponent(row.namespace)}/${encodeURIComponent(row.name)}`, cluster))
  const d = details.data
  const key = `${row.namespace}/${row.name}`

  const check = useCallback(
    (refresh: boolean) => {
      if (!app) return
      setChecking(true)
      fetchDrift(cluster, row.namespace, row.name, refresh)
        .then((x) => {
          setDrift(x)
          onDrift(x)
        })
        .catch((e: unknown) => onToast(e instanceof Error ? e.message : String(e)))
        .finally(() => setChecking(false))
    },
    [app, cluster, row.namespace, row.name, onDrift, onToast],
  )

  // The tab mounts the panel afresh per release and per change that ran
  // (its key), so the state above starts over; the first drift check is
  // asked for once, off the render.
  useEffect(() => {
    if (!row.app || row.app.drift) return
    const t = setTimeout(() => check(false), 0)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, version])

  useEffect(() => {
    if (tab !== 'history' && tab !== 'images') return
    fetchRevisions(cluster, row.namespace, row.name)
      .then((rs) => {
        setRevisions(rs)
        if (rs.length > 0) {
          setTo((t) => t ?? rs[0]!.revision)
          setFrom((f) => f ?? (rs.length > 1 ? rs[1]!.revision : rs[0]!.revision))
        }
      })
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
    if (tab === 'history' && app && !refs) {
      fetchAppRefs(cluster, row.namespace, row.name)
        .then(setRefs)
        .catch(() => undefined)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab, key, version])

  useEffect(() => {
    if (tab !== 'images') return
    fetchImages(cluster, row.namespace, row.name, imageRev)
      .then(setImages)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
  }, [tab, key, version, imageRev, cluster, row.namespace, row.name])

  const compare = () => {
    if (!to) return
    setDiffing(true)
    setErr('')
    fetchDiff(cluster, row.namespace, row.name, { from, to, ref: to === 'source' ? diffRef?.ref : undefined, refKind: to === 'source' ? diffRef?.kind : undefined })
      .then(setDiff)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setDiffing(false))
  }

  const tabs: [AppTab, string, number | undefined][] = [
    ['overview', 'Overview', undefined],
    ['values', 'Values', (app?.params?.length ?? 0) + (app?.values ? 1 : 0) || undefined],
    ['history', 'History', row.revisions || undefined],
    ['images', 'Images', images?.images.length],
  ]
  const commit = app?.commit ? app.commit.slice(0, 12) : ''

  return (
    <main className="rk-pane" aria-label={`Release ${row.name}`} style={{ flex: '1 1 640px', minWidth: 0, display: 'flex', flexDirection: 'column', gap: 14, padding: '14px 18px' }} data-testid="app-panel">
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 12px' }}>
        <h2 className="rk-mono" style={{ margin: 0, fontSize: 18, fontWeight: 600, display: 'inline-flex', alignItems: 'center', gap: 6 }} data-testid="app-title">
          {row.name}
          <CopyName text={row.name} what="release name" />
        </h2>
        <span className="rk-pill" data-tone={row.tone} data-testid="app-status">
          <span className="rk-dot" />
          {row.status}
        </span>
        <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>
          in <span className="rk-mono">{row.namespace}</span> · {row.chart}-{row.chartVersion}
          {row.appVersion ? ` · app ${row.appVersion}` : ''} · revision {row.revision}
        </span>
        <span style={{ flex: '1 1 auto' }} />
        <AppToolsButton row={row} canChange={canChange} onPick={onAction} label="Tools" />
        <a className="rk-btn" href={href('helm', [row.namespace, row.name])} title="Pods, resources and notes on the Helm tab">
          Helm tab
        </a>
        <a className="rk-btn" href={href('helm1')} aria-label="Close the release" data-testid="app-close">
          Close
        </a>
      </div>
      <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg2)' }} data-testid="app-source-line">
        {app ? (
          <>
            Source <span className="rk-mono">{sourceText(app.source)}</span>
            {commit && (
              <>
                {' '}
                · commit <span className="rk-mono">{commit}</span>
                {app.commitSubject ? ` “${app.commitSubject}”` : ''}
                {app.commitDate ? `, ${fmtTs(app.commitDate)}` : ''}
              </>
            )}
          </>
        ) : (
          <>
            The console keeps no source for this release: it was installed another way.{' '}
            {canChange && (
              <button className="rk-link" onClick={() => onAction('source')} data-testid="app-set-source">
                Set its source
              </button>
            )}{' '}
            to upgrade it from git, switch its branch or tag, and check its drift.
          </>
        )}
      </p>
      <nav aria-label="Release sections" style={{ display: 'flex', flexWrap: 'wrap', gap: '0 20px', borderBottom: '1px solid var(--line)' }}>
        {tabs.map(([id, label, count]) => (
          <button key={id} className="rk-tab" aria-current={tab === id ? 'page' : undefined} onClick={() => onTab(id)} style={{ height: 38 }} data-subtab={id}>
            {label}
            {count !== undefined && count !== 0 && <span className="rk-count">{count}</span>}
          </button>
        ))}
      </nav>
      {err && (
        <p className="rk-callout" data-tone="warn" style={{ margin: 0, fontSize: 12.5 }} role="alert">
          {err}
        </p>
      )}

      {tab === 'overview' && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))', gap: 12, alignItems: 'start' }}>
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
        </div>
      )}

      {tab === 'values' && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(360px, 1fr))', gap: 12, alignItems: 'start' }}>
          <Card title="Values file and parameters" aside={canChange && app ? <button className="rk-btn" onClick={() => onAction('values')} data-testid="app-edit-values">Edit…</button> : undefined}>
            {!app ? (
              <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>Kept for releases with a source.</p>
            ) : (
              <>
                {app.values ? (
                  <pre className="rk-code rk-mono" style={{ margin: 0, fontSize: 12.5, lineHeight: 1.5, whiteSpace: 'pre-wrap', maxHeight: 480, overflow: 'auto' }} data-testid="app-values-file">
                    {app.values.split('\n').map((l, i) => (
                      <span key={i} style={{ display: 'block' }}>
                        {colorLine(l).map((seg, j) => (
                          <span key={j} style={{ color: COLOR[seg[1]] }}>
                            {seg[0]}
                          </span>
                        ))}
                      </span>
                    ))}
                  </pre>
                ) : (
                  <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>No values file: the chart's defaults apply.</p>
                )}
                <div>
                  <h4 className="rk-h" style={{ margin: '0 0 6px', fontSize: 12 }}>
                    Parameters
                  </h4>
                  {app.params?.length ? (
                    <ul className="rk-mono" style={{ margin: 0, paddingLeft: 18, fontSize: 12.5 }} data-testid="app-params-list">
                      {app.params.map((p) => (
                        <li key={p}>{p}</li>
                      ))}
                    </ul>
                  ) : (
                    <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>None.</p>
                  )}
                </div>
              </>
            )}
          </Card>
          <Card title="Values as deployed" label="Values as deployed">
            {!d ? (
              <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>{details.error || 'Loading…'}</p>
            ) : d.values ? (
              <>
                {d.maskedValues?.length ? <p className="rk-hint" style={{ margin: 0 }}>Hidden by 221B: {d.maskedValues.join(', ')}.</p> : null}
                <pre className="rk-code rk-mono" style={{ margin: 0, fontSize: 12.5, lineHeight: 1.5, whiteSpace: 'pre-wrap', maxHeight: 480, overflow: 'auto' }} data-testid="app-deployed-values">
                  {d.values.split('\n').map((l, i) => (
                    <span key={i} style={{ display: 'block' }}>
                      {colorLine(l).map((seg, j) => (
                        <span key={j} style={{ color: COLOR[seg[1]] }}>
                          {seg[0]}
                        </span>
                      ))}
                    </span>
                  ))}
                </pre>
              </>
            ) : (
              <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>The release runs the chart's defaults.</p>
            )}
          </Card>
        </div>
      )}

      {tab === 'history' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
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
          <Card title="Revisions" pad={false}>
            {!revisions ? (
              <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 12.5, color: 'var(--fg3)' }}>Loading…</p>
            ) : (
              <div role="table" aria-label="Revisions" style={{ display: 'grid', gridTemplateColumns: 'auto auto auto 1fr auto auto', fontSize: 12.5 }} data-testid="app-revisions">
                {['Revision', 'Updated', 'Status', 'Description', 'Chart', 'Objects'].map((h) => (
                  <span key={h} role="columnheader" style={{ padding: '8px 12px', fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)', borderBottom: '1px solid var(--line)' }}>
                    {h}
                  </span>
                ))}
                {revisions.map((r) => (
                  <div key={r.revision} role="row" style={{ display: 'contents' }} data-revision={r.revision}>
                    <span role="cell" className="rk-num" style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)' }}>
                      {r.revision}
                    </span>
                    <span role="cell" title={fmtTs(r.updated)} style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)', whiteSpace: 'nowrap' }}>
                      {fmtAge(r.updated, now)} ago
                    </span>
                    <span role="cell" style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)' }}>
                      <span className="rk-t" data-tone={r.status === 'deployed' ? 'ok' : r.status === 'failed' ? 'err' : 'muted'}>
                        {r.status}
                      </span>
                    </span>
                    <span role="cell" style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)', minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={r.description}>
                      {r.description}
                    </span>
                    <span role="cell" className="rk-mono" style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)', whiteSpace: 'nowrap' }}>
                      {r.chart}-{r.chartVersion}
                      {r.appVersion ? ` (app ${r.appVersion})` : ''}
                    </span>
                    <span role="cell" className="rk-num" style={{ padding: '7px 12px', borderBottom: '1px solid var(--line)' }}>
                      {r.objects}
                    </span>
                  </div>
                ))}
              </div>
            )}
          </Card>
          <Card title="Compare">
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
            {diff && <DiffView diff={diff} />}
          </Card>
        </div>
      )}

      {tab === 'images' && (
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
            <p style={{ margin: 0, padding: '0 16px 12px', fontSize: 12.5, color: 'var(--fg3)' }}>Loading…</p>
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
            Every image the manifest of {images ? `revision ${images.revision}` : 'the release'} names, in {clusterName ?? 'the cluster'}: {images ? nWord(new Set(images.images.map((x) => x.image)).size, 'distinct image', 'distinct images') : ''}.
          </p>
        </Card>
      )}
    </main>
  )
}

function driftState(d: AppDrift): string {
  if (d.changed + d.missing > 0) return 'drifted'
  if (d.unknown > 0 && d.same === 0) return 'unknown'
  return 'in sync'
}
