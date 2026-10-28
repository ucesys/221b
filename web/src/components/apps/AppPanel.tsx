import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchAppRefs, fetchDiff, fetchDrift, fetchImages, fetchRevisions, sourceText, updateApp, type AppDiff, type AppDrift, type AppImage, type AppRefs, type AppSource, type AppView, type RefKind, type RevisionView } from '../../api/apps'
import { useDetails } from '../../api/details'
import type { ClusterInfo, ReleaseDetails } from '../../api/types'
import type { WatsonItem } from '../../api/watson'
import { driftTone, type AppRow } from '../../columns/apps'
import { fmtAge, fmtTs, nWord } from '../../lib/format'
import { apiPath, href, type Route } from '../../lib/router'
import { CopyName } from '../CopyName'
import { Card } from '../Drawer'
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

  const overview = <SourceCard clusterId={clusterId} row={row} app={app} canChange={canChange} onResult={onResult} now={now} />

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

  const before: ExtraTab[] = [{ id: 'overview', label: 'Source', render: () => overview }]
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
          <AppToolsButton row={row} canChange={canChange} onPick={onAction} />
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
          {drift && driftState(drift) !== 'in sync' && (
            <span data-testid="app-drift" title={drift.objects.filter((o) => o.state !== 'same').map((o) => `${o.kind} ${o.name}: ${o.state}${o.fields?.length ? ' (' + o.fields.join(', ') + ')' : ''}`).join('\n')}>
              <span className="rk-pill" data-tone={driftTone(row) === 'muted' ? 'warn' : driftTone(row)}>
                <span className="rk-dot" />
                {driftState(drift)}
              </span>{' '}
              {drift.changed} changed · {drift.missing} missing{drift.unknown ? ` · ${drift.unknown} unknown` : ''} · checked {fmtAge(drift.checked, now)} ago ·{' '}
              <button className="rk-link" onClick={() => check(true)} disabled={checking} data-testid="app-check-drift">
                {checking ? 'checking…' : 'check again'}
              </button>
            </span>
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
                  <button className="rk-link" onClick={() => onTab('overview')} data-testid="app-set-source">
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

/**
 * The application's values file and parameters, edited in place (HA-12,
 * HA-13) and saved by themselves a moment after the typing stops, for the
 * next deploy; Upgrade, at the top, deploys them.
 */
function ValuesEditor({ clusterId, row, app, canChange, onResult }: { clusterId: string; row: AppRow; app: AppView; canChange: boolean; onResult: (r: WatsonItem | AppView, doing: string) => void }) {
  const [values, setValues] = useState(app.values ?? '')
  const [params, setParams] = useState((app.params ?? []).join('\n'))
  const [state, setState] = useState<'saved' | 'dirty' | 'saving' | 'error'>('saved')
  const [error, setError] = useState('')
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const queue = useRef<Promise<void>>(Promise.resolve())
  const seq = useRef(0)
  const latest = useRef({ values, params })
  useEffect(() => {
    latest.current = { values, params }
  }, [values, params])
  // A record changed elsewhere (an upgrade, another browser) shows once
  // nothing is being edited here.
  const stateRef = useRef(state)
  useEffect(() => {
    stateRef.current = state
  }, [state])
  useEffect(() => {
    if (stateRef.current !== 'saved') return
    const v = app.values ?? ''
    const p = (app.params ?? []).join('\n')
    if (v !== latest.current.values) setValues(v)
    if (p !== latest.current.params) setParams(p)
  }, [app.values, app.params])
  const save = useCallback(() => {
    const { values: v, params: p } = latest.current
    const list = p
      .split('\n')
      .map((l) => l.trim())
      .filter((l) => l && !l.startsWith('#'))
    for (const x of list) {
      if (!x.includes('=')) {
        setState('error')
        setError(`"${x}" is not key=value`)
        return
      }
    }
    setState('saving')
    setError('')
    const mine = ++seq.current
    queue.current = queue.current
      .then(() => updateApp(clusterId, row.namespace, row.name, { values: v, params: list }))
      .then((r) => {
        // Typing since this save went out keeps the editor dirty.
        if (seq.current === mine) setState('saved')
        onResult(r, 'Values saved')
      })
      .catch((e: unknown) => {
        if (seq.current === mine) {
          setState('error')
          setError(e instanceof Error ? e.message : String(e))
        }
      })
  }, [clusterId, row.namespace, row.name, onResult])
  const touch = () => {
    setState('dirty')
    seq.current++
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(save, 1200)
  }
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current)
  }, [])
  const status = state === 'saving' ? 'Saving…' : state === 'dirty' ? 'Unsaved; saves in a moment' : state === 'error' ? error : 'Saved'
  return (
    <Card title="Values file and parameters" aside={<span style={{ fontSize: 12, color: state === 'error' ? 'var(--err)' : 'var(--fg3)' }} role={state === 'error' ? 'alert' : undefined} data-testid="app-values-state">{status}</span>}>
      <textarea
        className="rk-area rk-mono"
        value={values}
        onChange={(e) => {
          setValues(e.target.value)
          touch()
        }}
        onBlur={() => state === 'dirty' && save()}
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
            touch()
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
        <textarea
          className="rk-area rk-mono"
          value={params}
          onChange={(e) => {
            setParams(e.target.value)
            touch()
          }}
          onBlur={() => state === 'dirty' && save()}
          readOnly={!canChange}
          spellCheck={false}
          aria-label="Parameters"
          style={{ minHeight: 64, fontSize: 12.5, lineHeight: 1.5, resize: 'vertical' }}
          placeholder="image.tag=v1.2.0 — one key=value per line, as helm --set takes them, applied over the values file"
          data-testid="app-params"
        />
      </div>
      <p className="rk-hint" style={{ margin: 0 }}>
        {canChange ? 'What you type here is kept with the application; Upgrade at the top deploys it, as you, after your approval in Watson. The chart\'s defaults apply underneath.' : 'What the console deploys this application with; the chart\'s defaults apply underneath.'}
      </p>
    </Card>
  )
}

/**
 * The application's source, edited in place and saved when a field is
 * left (HA-05, HA-11): the repository, the chart's path, the branch or
 * tag, a token for a private repository, and whether to make the
 * namespace at install. A release the console did not install gets its
 * record here.
 */
function SourceCard({ clusterId, row, app, canChange, onResult, now }: { clusterId: string; row: AppRow; app?: AppView; canChange: boolean; onResult: (r: WatsonItem | AppView, doing: string) => void; now: number }) {
  const [repo, setRepo] = useState(app?.source.repo ?? '')
  const [path, setPath] = useState(app?.source.path ?? '')
  const [ref, setRef] = useState(app?.source.ref ?? '')
  const [refKind, setRefKind] = useState<RefKind>(app?.source.refKind ?? 'branch')
  const [token, setToken] = useState('')
  const [createNs, setCreateNs] = useState(app?.createNamespace ?? false)
  const [state, setState] = useState<'saved' | 'dirty' | 'saving' | 'error'>('saved')
  const [error, setError] = useState('')
  const commit = app?.commit ? app.commit.slice(0, 12) : ''
  const saved: AppSource | null = app ? app.source : null
  const queue = useRef<Promise<void>>(Promise.resolve())
  const seq = useRef(0)
  // The record's source shows once nothing is being edited here.
  const stateRef = useRef(state)
  useEffect(() => {
    stateRef.current = state
  }, [state])
  useEffect(() => {
    if (stateRef.current !== 'saved' || !app) return
    setRepo(app.source.repo)
    setPath(app.source.path ?? '')
    setRef(app.source.ref)
    setRefKind(app.source.refKind)
    setCreateNs(app.createNamespace ?? false)
  }, [app])
  const dirty = (createNsNow = createNs, tok = token) => !saved || repo.trim() !== saved.repo || path.trim() !== (saved.path ?? '') || ref.trim() !== saved.ref || refKind !== saved.refKind || createNsNow !== (app?.createNamespace ?? false) || tok !== ''
  const save = (createNsNow = createNs, tok = token) => {
    if (!canChange || !dirty(createNsNow, tok)) return
    if (!repo.trim() || !ref.trim()) {
      setState('dirty')
      return
    }
    setState('saving')
    setError('')
    const input = { source: { repo: repo.trim(), path: path.trim(), ref: ref.trim(), refKind }, token: tok || undefined, createNamespace: createNsNow }
    // One save after another: two fields left in quick succession must not
    // race; and an answer marks the card clean only when nothing was typed
    // since it went out.
    const mine = ++seq.current
    queue.current = queue.current
      .then(() => updateApp(clusterId, row.namespace, row.name, input))
      .then((r) => {
        if (seq.current === mine) {
          setState('saved')
          setToken('')
        }
        onResult(r, saved ? 'Source saved' : 'Source set')
      })
      .catch((e: unknown) => {
        if (seq.current === mine) {
          setState('error')
          setError(e instanceof Error ? e.message : String(e))
        }
      })
  }
  const edited = () => {
    seq.current++
    setState('dirty')
  }
  const field = (label: string, value: string, set: (v: string) => void, placeholder: string, testId: string, type = 'text', span = 1) => (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 3, fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)', minWidth: 0, gridColumn: span > 1 ? `span ${span}` : undefined }}>
      {label}
      <span className="rk-field">
        <input className="rk-mono" type={type} autoComplete="off" value={value} readOnly={!canChange} onChange={(e) => (set(e.target.value), edited())} onBlur={() => save()} placeholder={placeholder} data-testid={testId} />
      </span>
    </label>
  )
  const status = state === 'saving' ? 'Saving…' : state === 'dirty' ? (repo.trim() && ref.trim() ? 'Saves when you leave the field' : 'Needs a repository and a branch or tag') : state === 'error' ? error : saved ? 'Saved' : 'Not set'
  return (
    <Card title="Source" aside={<span style={{ fontSize: 12, color: state === 'error' ? 'var(--err)' : 'var(--fg3)' }} role={state === 'error' ? 'alert' : undefined} data-testid="app-source-state">{status}</span>}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(6, minmax(0, 1fr))', gap: '8px 12px' }}>
        {field('Repository (https:// git URL)', repo, setRepo, 'https://github.com/org/app', 'app-src-repo', 'text', 4)}
        {field('Chart path (empty for the root)', path, setPath, 'charts/app', 'app-src-path', 'text', 2)}
        <label style={{ display: 'flex', flexDirection: 'column', gap: 3, fontSize: 11.5, fontWeight: 600, color: 'var(--fg3)', minWidth: 0, gridColumn: 'span 3' }}>
          {refKind === 'tag' ? 'Tag' : 'Branch'}
          <span style={{ display: 'flex', gap: 6 }}>
            <span className="rk-field" style={{ flex: '1 1 auto' }}>
              <input className="rk-mono" value={ref} readOnly={!canChange} onChange={(e) => (setRef(e.target.value), edited())} onBlur={() => save()} placeholder="main, or v1.2.0" data-testid="app-src-ref" />
            </span>
            <span className="rk-seg" role="group" aria-label="Kind of ref">
              <button type="button" aria-pressed={refKind === 'branch'} disabled={!canChange} onClick={() => (setRefKind('branch'), edited())}>
                Branch
              </button>
              <button type="button" aria-pressed={refKind === 'tag'} disabled={!canChange} onClick={() => (setRefKind('tag'), edited())}>
                Tag
              </button>
            </span>
          </span>
        </label>
        {field(app?.hasToken ? 'Token (saved; paste a new one to replace it)' : 'Token (private repository only)', token, setToken, app?.hasToken ? '••••••••' : 'optional', 'app-src-token', 'password', 3)}
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 18px', fontSize: 12, color: 'var(--fg3)' }}>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12.5, color: 'var(--fg)' }}>
          <input
            type="checkbox"
            checked={createNs}
            disabled={!canChange}
            onChange={(e) => {
              setCreateNs(e.target.checked)
              save(e.target.checked)
            }}
            data-testid="app-src-create-ns"
          />{' '}
          Create the namespace {row.namespace} at install if it does not exist
        </label>
        {app && (
          <>
            <span>
              Commit <span className="rk-mono" style={{ color: 'var(--fg)' }}>{commit ? `${commit}${app.commitSubject ? ` — ${app.commitSubject}` : ''}` : 'not deployed from here yet'}</span>
            </span>
            {app.chartVersion && (
              <span>
                Chart at commit <span className="rk-mono" style={{ color: 'var(--fg)' }}>{app.chartVersion}</span>
              </span>
            )}
            <span title={`kept by ${app.createdBy || 'the console'}, ${fmtTs(app.created)}`}>
              Last change <span style={{ color: 'var(--fg)' }}>{app.updatedBy || '—'}, {fmtAge(app.updated, now)} ago</span>
            </span>
          </>
        )}
        {state === 'dirty' && repo.trim() && ref.trim() ? <span>Leave the field to save.</span> : null}
        {!saved && <span>Once set, the release can be upgraded from here and checked for drift.</span>}
      </div>
    </Card>
  )
}

function driftState(d: AppDrift): string {
  if (d.changed + d.missing > 0) return 'drifted'
  if (d.unknown > 0 && d.same === 0) return 'unknown'
  return 'in sync'
}
